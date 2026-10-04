import assert from "node:assert/strict";
import { mkdtempSync, rmSync } from "node:fs";
import { createServer, type IncomingMessage, type ServerResponse } from "node:http";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import { GatewayClient, GatewayError, type ToolDiscoveryPage } from "../src/client.js";
import { IntentJournal } from "../src/journal.js";
import { createGatewayTools } from "../src/tools.js";
import { WorkerClient, type Lease } from "../src/worker-client.js";

const summary = { id: "tool-1", name: "Service status", description: "Read service health", risk: "read" as const, version: 1 };
const token = "test-discovery-secret-at-least-32-characters";
const lease: Lease = { run: { id: "run-1", workspace_id: "workspace-1", actor_id: "actor-1", prompt: "test", state: "RUNNING", attempt: 1 }, lease_token: "test-lease", lease_expires_at: new Date(Date.now() + 45_000).toISOString() };
async function fixture(kind: "local" | "leased", handler: (req: IncomingMessage, res: ServerResponse) => void) {
  const path = kind === "local" ? "/api/v1/catalog/tools" : "/internal/runner/runs/run-1/gateway/tools";
  const server = createServer((req, res) => {
    assert.equal(req.headers.authorization, `Bearer ${token}`);
    assert.equal(req.headers["x-run-lease"], kind === "leased" ? "test-lease" : undefined);
    handler(req, res);
  });
  await new Promise<void>(resolve => server.listen(0, "127.0.0.1", resolve));
  const address = server.address(); assert(address && typeof address !== "string");
  const origin = `http://127.0.0.1:${address.port}`;
  const client = kind === "local" ? new GatewayClient(origin, token) : new WorkerClient(origin, token).gateway(lease, new AbortController().signal);
  return { client, path, close: () => new Promise<void>(resolve => { server.close(() => resolve()); server.closeAllConnections(); }) };
}
function json(res: ServerResponse, value: unknown) { res.setHeader("content-type", "application/json"); res.end(JSON.stringify(value)); }

for (const kind of ["local", "leased"] as const) {
  test(`${kind} discovery returns one page and preserves literal query, opaque cursor and explicit limits`, async () => {
    const requests: URL[] = [];
    const cursor = "opaque+/=中文&position";
    const first: ToolDiscoveryPage = { items: [summary], next_cursor: cursor, total: 2 };
    const second: ToolDiscoveryPage = { items: [{ ...summary, id: "tool-2" }], total: 2 };
    const server = await fixture(kind, (req, res) => { requests.push(new URL(req.url!, "http://fixture")); json(res, requests.length === 1 ? first : second); });
    try {
      const query = "中文 %_\\";
      assert.deepEqual(await server.client.search(`  ${query}  `, undefined, { limit: 1 }), first);
      assert.equal(requests.length, 1, "a next cursor must not trigger automatic requests");
      assert.equal(requests[0].pathname, server.path);
      assert.deepEqual([...requests[0].searchParams], [["query", query], ["limit", "1"]]);
      assert.deepEqual(await server.client.search(query, undefined, { cursor, limit: 2 }), second);
      assert.deepEqual([...requests[1].searchParams], [["query", query], ["limit", "2"], ["cursor", cursor]]);
      await server.client.search("中".repeat(66) + "ab", undefined, { cursor: "c".repeat(2048), limit: 50 });
      assert.equal(requests[2].searchParams.get("limit"), "50");
      await server.client.search("");
      assert.deepEqual([...requests[3].searchParams], [["query", ""]], "omitted limits use the server default");
    } finally { await server.close(); }
  });

  test(`${kind} discovery rejects invalid UTF-8 bounds and noninteger limits before HTTP`, async () => {
    let requests = 0;
    const server = await fixture(kind, (_req, res) => { requests++; json(res, { items: [], total: 0 }); });
    try {
      for (const query of ["x".repeat(201), "中".repeat(67), null, 1]) {
        await assert.rejects(server.client.search(query as string), /INVALID_TOOL_SEARCH_QUERY/);
      }
      for (const limit of [0, -1, 51, 1.1, NaN, Infinity, "1", null]) {
        await assert.rejects(server.client.search("", undefined, { limit: limit as number }), /INVALID_TOOL_SEARCH_LIMIT/);
      }
      for (const cursor of ["x".repeat(2049), "中".repeat(683), null, 1]) {
        await assert.rejects(server.client.search("", undefined, { cursor: cursor as string }), /INVALID_TOOL_SEARCH_CURSOR/);
      }
      await assert.rejects(server.client.search("", undefined, { unknown: true } as never), /INVALID_TOOL_SEARCH_OPTIONS/);
      assert.equal(requests, 0);
    } finally { await server.close(); }
  });

  test(`${kind} discovery rejects malformed pages and schema or connection leaks; response remains bounded`, async () => {
    let payload: unknown;
    const server = await fixture(kind, (_req, res) => json(res, payload));
    const page = { items: [summary], total: 1 };
    try {
      for (payload of [null, [], {}, { ...page, items: null }, { ...page, total: "1" }, { ...page, total: -1 }, { ...page, total: 1.5 }, { ...page, total: Number.MAX_SAFE_INTEGER + 1 }, { ...page, next_cursor: null }, { ...page, next_cursor: "中".repeat(683) }, { ...page, items: [summary, { ...summary, id: "tool-2" }] }, { ...page, private: "private-discovery-secret" }, ...[
        { ...summary, input_schema: { private: "private-discovery-secret" } },
        { ...summary, http: { url: "https://private-discovery-secret" } },
        { ...summary, credential_ref: "private-discovery-secret" },
        { ...summary, risk: "admin" }, { ...summary, version: 0 }, { ...summary, description: null },
      ].map(tool => ({ ...page, items: [tool] }))]) {
        await assert.rejects(server.client.search("", undefined, { limit: 1 }), error => error instanceof GatewayError && /INVALID_.*RESPONSE/.test(error.code) && !String(error).includes("private-discovery-secret"));
      }
      payload = { items: [summary, summary], total: 2 };
      await assert.rejects(server.client.search("", undefined, { limit: 2 }), /INVALID_.*RESPONSE/);
      payload = { items: [{ ...summary, description: "x".repeat(262_144) }], total: 1 };
      await assert.rejects(server.client.search(""), error => error instanceof GatewayError && /RESPONSE_TOO_LARGE/.test(error.code) && !error.ambiguous);
    } finally { await server.close(); }
  });

  test(`${kind} Pi tool bridges pagination without invoking a model or loading schemas`, async () => {
    const requests: URL[] = [];
    const first = { items: [summary], next_cursor: "page-2", total: 2 };
    const last = { items: [{ ...summary, id: "tool-2" }], next_cursor: "", total: 2 };
    const server = await fixture(kind, (req, res) => { requests.push(new URL(req.url!, "http://fixture")); json(res, requests.length === 1 ? first : last); });
    const directory = mkdtempSync(join(tmpdir(), "discovery-tool-"));
    const journal = new IntentJournal(directory, "test", "session-test");
    try {
      const tool = createGatewayTools(server.client, journal).find(value => value.name === "search_tools")!;
      const invoke = async (args: Record<string, unknown>) => {
        const result = await tool.execute("discovery-call", args, undefined, undefined, {} as never);
        const content = result.content[0]; assert.equal(content.type, "text");
        return JSON.parse((content as { text: string }).text);
      };
      assert.deepEqual(await invoke({ query: "status", limit: 1 }), first);
      assert.equal(requests.length, 1);
      assert.deepEqual(await invoke({ query: "status", limit: 1, cursor: first.next_cursor }), last);
      assert.equal(requests[1].searchParams.get("cursor"), first.next_cursor);
      assert(requests.every(request => request.pathname === server.path));
      assert.deepEqual(journal.list(), []);
      const invalid = await invoke({ query: "中".repeat(67) });
      assert.equal(invalid.error, "INVALID_TOOL_SEARCH_QUERY"); assert.match(invalid.instruction, /UTF-8/);
      assert.equal(requests.length, 2);
      await invoke({});
      assert.equal(requests[2].searchParams.get("query"), "");
      assert.equal(requests[2].searchParams.has("limit"), false);
    } finally { journal.close(); rmSync(directory, { recursive: true, force: true }); await server.close(); }
  });
}
