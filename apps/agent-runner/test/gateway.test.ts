import assert from "node:assert/strict";
import { createServer, type IncomingMessage, type ServerResponse } from "node:http";
import { mkdtempSync, readFileSync, renameSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import type { ToolDefinition } from "@earendil-works/pi-coding-agent";
import { GatewayClient, GatewayError } from "../src/client.js";
import { IntentJournal } from "../src/journal.js";
import { createGatewayTools, type Pause } from "../src/tools.js";

async function stub(handler: (req: IncomingMessage, res: ServerResponse, body: Record<string, unknown>) => void | Promise<void>) {
  const server = createServer(async (req, res) => {
    let body = "";
    for await (const chunk of req) body += chunk;
    await handler(req, res, body ? JSON.parse(body) : {});
  });
  await new Promise<void>(resolve => server.listen(0, "127.0.0.1", resolve));
  const address = server.address();
  assert(address && typeof address !== "string");
  return { url: `http://127.0.0.1:${address.port}`, close: () => new Promise<void>((resolve, reject) => { server.close(error => error ? reject(error) : resolve()); server.closeAllConnections(); }) };
}
const json = (res: ServerResponse, data: unknown, status = 200) => { res.writeHead(status, { "content-type": "application/json" }); res.end(JSON.stringify(data)); };
async function call(tools: ToolDefinition[], name: string, params: Record<string, unknown>, callId = "call_1") {
  const tool = tools.find(item => item.name === name)!;
  const response = await tool.execute(callId, params, undefined, undefined, {} as never);
  const content = response.content[0];
  assert.equal(content.type, "text");
  return JSON.parse((content as { text: string }).text);
}
function localJournal(scope = "test", session = "session_test") {
  const dir = mkdtempSync(join(tmpdir(), "gateway-pi-test-"));
  const journal = new IntentJournal(dir, scope, session);
  return { dir, journal, close: () => { journal.close(); rmSync(dir, { recursive: true, force: true }); } };
}

test("sends bearer header only to configured origin; rejects redirect and hides upstream secrets", async () => {
  const token = "private-gateway-test-token";
  let requests = 0;
  const server = await stub((req, res) => {
    requests++;
    assert.equal(req.headers.authorization, `Bearer ${token}`);
    if (req.url === "/api/v1/me") { json(res, { error: `${token} https://upstream/private` }, 401); return; }
    res.writeHead(307, { location: "/leak" }); res.end();
  });
  try {
    const client = new GatewayClient(server.url, token);
    await assert.rejects(client.me(), error => error instanceof GatewayError && error.code === "GATEWAY_HTTP_401" && !String(error).includes(token));
    await assert.rejects(client.tool("x"), error => error instanceof GatewayError && !String(error).includes(token));
    assert.equal(requests, 2);
    assert.throws(() => new GatewayClient("http://example.com", token), /HTTPS_REQUIRED/);
    assert.throws(() => new GatewayClient("https://user:secret@example.com", token), /INVALID_GATEWAY_URL/);
    assert.throws(() => new GatewayClient(server.url, ""), /TOKEN_REQUIRED/);
  } finally { await server.close(); }
});

test("journal persists before preparation; reuses intent across call IDs and reload; rejects concurrent session", () => {
  const local = localJournal();
  try {
    const first = local.journal.reserve("c1", "retry_job", { job: "job-1", count: 1 });
    const disk = JSON.parse(readFileSync(join(local.dir, "session_test.json"), "utf8"));
    assert.equal(disk.intents[0].idempotency_key, first.idempotency_key);
    assert(!readFileSync(join(local.dir, "session_test.json"), "utf8").includes("job-1"));
    assert.equal(local.journal.reserve("c2", "retry_job", { count: 1, job: "job-1" }).idempotency_key, first.idempotency_key);
    assert.throws(() => local.journal.reserve("c1", "retry_job", { job: "different" }), /ARGUMENTS_CHANGED/);
    assert.throws(() => new IntentJournal(local.dir, "test", "session_test"), /SESSION_ALREADY_LOCKED/);
    local.journal.bind(first, "op1"); local.journal.markDispatched("op1"); local.journal.close();
    const resumed = new IntentJournal(local.dir, "test", "session_test");
    try { assert.equal(resumed.lookup("op1")?.dispatched, true); assert.equal(resumed.reserve("c3", "retry_job", { job: "job-1", count: 1 }).idempotency_key, first.idempotency_key); }
    finally { resumed.close(); }
    assert.throws(() => new IntentJournal(local.dir, "other-workspace", "session_test"), /SCOPE_OR_FORMAT/);
  } finally { local.close(); }
});

test("only exposes governed tools and hides HTTP configuration from discovery", async () => {
  const local = localJournal();
  const server = await stub((req, res) => {
    const tool = { id: "tool1", name: "status", description: "Read status", risk: "read", input_schema: { type: "object" }, version: 1, http: { url: "https://secret.internal", credential_ref: "secret-ref" } };
    const { id, name, description, risk, version } = tool;
    json(res, req.url?.startsWith("/api/v1/catalog/tools?") ? { items: [{ id, name, description, risk, version }], total: 1 } : tool);
  });
  try {
    const tools = createGatewayTools(new GatewayClient(server.url, "operator"), local.journal);
    assert.deepEqual(tools.map(tool => tool.name), ["search_tools", "get_tool_schema", "prepare_action", "invoke_tool", "get_operation"]);
    const found = await call(tools, "search_tools", { query: "status" });
    const schema = await call(tools, "get_tool_schema", { tool_id: "tool1" });
    assert.equal(schema.input_schema.type, "object");
    assert.equal(found.total, 1);
    assert(!JSON.stringify(found).includes("input_schema"));
    assert(!JSON.stringify({ found, schema }).includes("secret"));
    assert(tools.every(tool => tool.executionMode === "sequential"));
  } finally { local.close(); await server.close(); }
});

test("write preparation pauses for approval and cannot execute before approval", async () => {
  const local = localJournal(); const pauses: Pause[] = []; let executes = 0;
  const operation = { id: "op1", tool_id: "tool1", state: "WAITING_APPROVAL", risk: "write" };
  const server = await stub((req, res, body) => {
    if (req.url?.startsWith("/api/v1/tools/")) { json(res, { id: "tool1", risk: "write" }); return; }
    if (req.url === "/api/v1/operations") {
      const disk = JSON.parse(readFileSync(join(local.dir, "session_test.json"), "utf8"));
      assert.equal(body.idempotency_key, disk.intents[0].idempotency_key);
    }
    if (req.url?.endsWith("/execute")) executes++;
    json(res, operation);
  });
  try {
    const tools = createGatewayTools(new GatewayClient(server.url, "operator"), local.journal, pause => pauses.push(pause));
    const foreign = await call(tools, "invoke_tool", { operation_id: "foreign" });
    assert.equal(foreign.error, "OPERATION_NOT_OWNED_BY_SESSION");
    const prepared = await call(tools, "prepare_action", { tool_id: "tool1", arguments: { job: "1" } });
    assert.equal(prepared.executed, false); assert.equal(prepared.state, "WAITING_APPROVAL");
    const invoked = await call(tools, "invoke_tool", { operation_id: "op1" });
    assert.equal(invoked.error, "RUNNER_PAUSED"); assert.equal(executes, 0); assert.equal(pauses[0].reason, "WAITING_APPROVAL");
  } finally { local.close(); await server.close(); }
});

test("lost preparation reply retains key; retry only retrieves the same prepared operation", async () => {
  const local = localJournal(); const keys: unknown[] = []; const pauses: Pause[] = [];
  const server = await stub((req, res, body) => {
    if (req.url?.startsWith("/api/v1/tools/")) { json(res, { id: "tool1", risk: "write" }); return; }
    assert.equal(req.url, "/api/v1/operations"); keys.push(body.idempotency_key);
    if (keys.length === 1) { req.socket.destroy(); return; }
    json(res, { id: "op1", tool_id: "tool1", state: "READY", risk: "read" });
  });
  try {
    let tools = createGatewayTools(new GatewayClient(server.url, "operator"), local.journal, pause => pauses.push(pause));
    assert.equal((await call(tools, "prepare_action", { tool_id: "tool1", arguments: {} }, "c1")).reason, "UNKNOWN");
    tools = createGatewayTools(new GatewayClient(server.url, "operator"), local.journal, pause => pauses.push(pause));
    assert.equal((await call(tools, "prepare_action", { tool_id: "tool1", arguments: {} }, "c2")).operation_id, "op1");
    assert.equal(keys[0], keys[1]); assert.equal(pauses.length, 1);
  } finally { local.close(); await server.close(); }
});

test("ambiguous execution is never sent a second time, including after local restart", async () => {
  const local = localJournal(); let executions = 0;
  const operation = { id: "op1", tool_id: "tool1", state: "READY", risk: "write" };
  const server = await stub((req, res) => {
    if (req.url?.startsWith("/api/v1/tools/")) { json(res, { id: "tool1", risk: "write" }); return; }
    if (req.url?.endsWith("/execute")) {
      executions++;
      assert.equal(JSON.parse(readFileSync(join(local.dir, "session_test.json"), "utf8")).intents[0].dispatched, true);
      req.socket.destroy(); return;
    }
    json(res, operation);
  });
  try {
    const client = new GatewayClient(server.url, "operator");
    let tools = createGatewayTools(client, local.journal);
    await call(tools, "prepare_action", { tool_id: "tool1", arguments: {} });
    const first = await call(tools, "invoke_tool", { operation_id: "op1" });
    assert.equal(first.reason, "UNKNOWN"); assert.equal(first.executed, null);
    local.journal.close();
    const resumed = new IntentJournal(local.dir, "test", "session_test");
    try {
      tools = createGatewayTools(client, resumed);
      assert.equal((await call(tools, "invoke_tool", { operation_id: "op1" })).reason, "EXECUTION_ALREADY_DISPATCHED");
      assert.equal(executions, 1);
      operation.state = "SUCCEEDED";
      tools = createGatewayTools(client, resumed);
      assert.equal((await call(tools, "get_operation", { operation_id: "op1" })).executed, true);
    } finally { resumed.close(); }
  } finally { local.close(); await server.close(); }
});

test("new read calls get fresh operations while replaying one call keeps its key", async () => {
  const local = localJournal(); const keys: string[] = [];
  const server = await stub((req, res, body) => {
    if (req.url?.startsWith("/api/v1/tools/")) { json(res, { id: "tool1", risk: "read" }); return; }
    if (req.method === "POST") keys.push(String(body.idempotency_key));
    json(res, { id: keys.length === 1 ? "op1" : "op2", tool_id: "tool1", risk: "read", state: "READY" });
  });
  try {
    const tools = createGatewayTools(new GatewayClient(server.url, "operator"), local.journal);
    await call(tools, "prepare_action", { tool_id: "tool1", arguments: {} }, "read1");
    await call(tools, "prepare_action", { tool_id: "tool1", arguments: {} }, "read1");
    await call(tools, "prepare_action", { tool_id: "tool1", arguments: {} }, "read2");
    assert.equal(keys.length, 2); assert.notEqual(keys[0], keys[1]);
  } finally { local.close(); await server.close(); }
});

test("failed durable journal write poisons the session before any further operation can be sent", () => {
  const local = localJournal();
  const moved = `${local.dir}-moved`;
  try {
    renameSync(local.dir, moved);
    assert.throws(() => local.journal.reserve("c1", "tool", {}), /JOURNAL_WRITE_FAILED/);
    assert.throws(() => local.journal.reserve("c1", "tool", {}), /JOURNAL_UNAVAILABLE/);
    assert.throws(() => local.journal.list(), /JOURNAL_UNAVAILABLE/);
  } finally { renameSync(moved, local.dir); local.close(); }
});

test("oversized response is bounded and mutation outcome remains ambiguous", async () => {
  const server = await stub((_req, res) => json(res, { payload: "x".repeat(300_000) }));
  try {
    const client = new GatewayClient(server.url, "operator");
    await assert.rejects(client.execute("op1"), error => error instanceof GatewayError && error.code === "GATEWAY_RESPONSE_TOO_LARGE" && error.ambiguous);
  } finally { await server.close(); }
});

test("unsafe integers and non-finite nested arguments are rejected before journal reservation or any request", async () => {
  const local = localJournal(); let requests = 0;
  const server = await stub((_req, res) => { requests++; json(res, {}); });
  try {
    const client = new GatewayClient(server.url, "operator");
    const tools = createGatewayTools(client, local.journal);
    for (const [index, number] of [Number.MAX_SAFE_INTEGER + 1, Number.MIN_SAFE_INTEGER - 1, Infinity, -Infinity, NaN].entries()) {
      const args = { nested: [{ number }] };
      const response = await call(tools, "prepare_action", { tool_id: "tool1", arguments: args }, `number-${index}`);
      assert.equal(response.executed, false);
      assert.match(response.error, /UNSAFE_INTEGER|NON_FINITE/);
      assert.match(response.instruction, /decimal strings/);
      assert.throws(() => client.prepare("tool1", args, "key"), /UNSAFE_INTEGER|NON_FINITE/);
    }
    assert.equal(requests, 0);
    assert.deepEqual(local.journal.list(), []);
  } finally { local.close(); await server.close(); }
});

test("failure to read an operation never claims that it has not executed", async () => {
  const local = localJournal();
  const server = await stub((_req, res) => json(res, { error: "upstream unavailable" }, 503));
  try {
    const tools = createGatewayTools(new GatewayClient(server.url, "operator"), local.journal);
    const response = await call(tools, "get_operation", { operation_id: "previously-executed" });
    assert.equal(response.error, "GATEWAY_HTTP_503");
    assert.equal(response.executed, null);
    assert.match(response.instruction, /could not be established/);
  } finally { local.close(); await server.close(); }
});
