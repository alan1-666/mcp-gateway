import test from "node:test";
import assert from "node:assert/strict";
import { APIClient, APIError } from "../src/api.ts";
import { canManageMCPServers, MCPDiscoveryController, parseResponsePolicy, parseServerDraft } from "../src/mcp-servers.ts";
import type { MCPServer, RemoteTool, Tool } from "../src/types.ts";

const server: MCPServer = {
  id: "orders-server", workspace_id: "workspace", name: "Orders", namespace: "orders",
  url: "https://tools.example.com/mcp", timeout_ms: 10000, enabled: true,
  created_at: "2026-10-05T00:00:00Z", updated_at: "2026-10-05T00:00:00Z",
};
const remote: RemoteTool = {
  name: "get_order", description: "Look up an order", input_schema: { type: "object" },
  read_only_hint: true, schema_hash: "schema-a", gateway_name: "orders__get_order_12345678",
};
const imported: Tool = {
  id: "imported-tool", workspace_id: "workspace", name: remote.gateway_name,
  description: remote.description, input_schema: remote.input_schema, risk: "write",
  mcp: { server_id: server.id, tool_name: remote.name, schema_hash: remote.schema_hash },
  response_policy: { max_bytes: 65536 },
  http: { method: "", url: "", timeout_ms: 0 },
  status: "draft", enabled: false, version: 1, created_at: server.created_at,
};
const discovery = (items = [remote]) => ({ items, total: items.length });
const api = (request: (path: string, options?: { method?: string; body?: unknown; signal?: AbortSignal }) => Promise<unknown>) => ({
  async request<T>(path: string, options?: { method?: string; body?: unknown; signal?: AbortSignal }): Promise<T> {
    return await request(path, options) as T;
  },
});
function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}

test("MCP server forms validate namespace, URL, reference, UTF-8 name and timeout before submitting", () => {
  const draft = { name: " Orders ", namespace: "orders", url: server.url, credential: "ORDERS_TOKEN", timeout: "10000" };
  assert.deepEqual(parseServerDraft(draft), { name: "Orders", namespace: "orders", url: server.url, credential_ref: "ORDERS_TOKEN", timeout_ms: 10000 });
  for (const namespace of ["Orders", "2orders", "orders.status", "a".repeat(25), ""]) {
    assert.throws(() => parseServerDraft({ ...draft, namespace }), /Namespace/);
  }
  for (const url of ["file:///etc/passwd", `${server.url}?token=secret`, `${server.url}?`, `${server.url}#anchor`, "https://secret:token@example.com/mcp", "https://@example.com/mcp", "not-a-url"]) {
    assert.throws(() => parseServerDraft({ ...draft, url }));
  }
  assert.throws(() => parseServerDraft({ ...draft, name: "中".repeat(41) }), /UTF-8/);
  assert.throws(() => parseServerDraft({ ...draft, name: "orders\nadmin" }), /line breaks/);
  assert.throws(() => parseServerDraft({ ...draft, credential: "actual token secret" }), /Credential references/);
  for (const timeout of ["99", "120001", "100.5", "Infinity", ""]) assert.throws(() => parseServerDraft({ ...draft, timeout }), /Timeout/);
});

test("response policies preserve exact JSON Pointer escapes, defaults and pagination selections", () => {
  assert.deepEqual(parseResponsePolicy("", "65536"), { max_bytes: 65536 });
  assert.deepEqual(parseResponsePolicy(" /order/id\n/next_cursor\r\n/a~1b/~0tag\n/results ", "131072"), {
    include: ["/order/id", "/next_cursor", "/a~1b/~0tag", "/results"], max_bytes: 131072,
  });
  // Numeric and '-' object keys are valid. Runtime rejects actual array traversal.
  assert.deepEqual(parseResponsePolicy("/lookup/0\n/lookup/-", "1024").include, ["/lookup/0", "/lookup/-"]);
  for (const value of ["1023", "131073", "1.5", "NaN", ""]) assert.throws(() => parseResponsePolicy("", value), /Response limit/);
});

test("response policies reject invalid escapes, invalid wildcards, empty segments, duplicates and parent-child overlap", () => {
  for (const include of ["order/id", "/a~2b", "/a~", "/", "/a\0b", "/a//b", "/orders/*", "/orders/a*b", "/a\n/a", "/a\n/a/b", "/a/b\n/a", "/a~1b\n/a~1b/c", "/" + "中".repeat(86)]) {
    assert.throws(() => parseResponsePolicy(include, "65536"), undefined, include);
  }
  assert.throws(() => parseResponsePolicy(Array.from({ length: 33 }, (_, index) => `/key${index}`).join("\n"), "65536"), /32/);
  assert.deepEqual(parseResponsePolicy("/order/id\n/order/items\n/orders", "65536").include, ["/order/id", "/order/items", "/orders"]);
});

test("response policies support array element fields and reject mixed object/array nodes", () => {
  const paths = ["/results/*/title", "/results/*/url", "/results/*/authors/*/name", "/next_cursor"];
  assert.deepEqual(parseResponsePolicy(paths.join("\n"), "65536").include, paths);
  for (const include of [
    "/results/*/title\n/results/id",
    "/results/0/title\n/results/*/url",
    "/results/*/author/*/name\n/results/*/author/id",
    "/results/*/author\n/results/*/author/name",
    "/results\n/results/*/title",
    "/results/*/title\n/results/*/title",
    "/results/*/title*",
    "/results/*",
  ]) assert.throws(() => parseResponsePolicy(include, "65536"), undefined, include);
  assert.deepEqual(parseResponsePolicy("/results/*/title\n/other/0/title", "65536").include, ["/results/*/title", "/other/0/title"]);
});

test("only administrators can access server actions; disabled servers never dispatch discovery or import", async () => {
  let requests = 0;
  const client = api(async () => { requests++; return discovery(); });
  for (const role of ["viewer", "operator", "approver", "", "administrator"]) {
    assert.equal(canManageMCPServers({ role }), false);
    const controller = new MCPDiscoveryController(client, { role });
    controller.select(server);
    assert.equal(controller.getSnapshot().server, null);
    await controller.discover();
    await controller.importTool(remote);
  }
  assert.equal(canManageMCPServers({ role: "admin" }), true);
  const controller = new MCPDiscoveryController(client, { role: "admin" });
  controller.select({ ...server, enabled: false });
  await controller.discover();
  await controller.importTool(remote);
  assert.match(controller.getSnapshot().error, /Enable this MCP server/);
  assert.equal(requests, 0);
});

test("server switching ignores late discovery responses even when transport ignores abort", async () => {
  const old = deferred<unknown>();
  let signal: AbortSignal | undefined;
  const controller = new MCPDiscoveryController(api(async (path, options) => {
    if (path.includes(server.id)) { signal = options?.signal; return old.promise; }
    return discovery([{ ...remote, name: "new_tool", gateway_name: "new__tool_hash" }]);
  }), { role: "admin" });
  controller.select(server);
  const first = controller.discover();
  controller.select({ ...server, id: "new-server" });
  await controller.discover();
  old.resolve(discovery());
  await first;
  assert.equal(signal?.aborted, true);
  assert.equal(controller.getSnapshot().items[0].name, "new_tool");
  assert.equal(controller.getSnapshot().server?.id, "new-server");
});

test("failed discovery can retry; old errors and revoked access cannot retain stale contracts", async () => {
  let fail = true;
  const controller = new MCPDiscoveryController(api(async () => {
    if (fail) throw new APIError("Access denied", 403, "forbidden");
    return discovery();
  }), { role: "admin" });
  controller.select(server);
  await controller.discover();
  assert.equal(controller.getSnapshot().loaded, false);
  assert.match(controller.getSnapshot().error, /Access denied/);
  fail = false;
  await controller.discover();
  assert.equal(controller.getSnapshot().loaded, true);
  assert.equal(controller.getSnapshot().error, "");
  fail = true;
  await controller.discover();
  assert.deepEqual(controller.getSnapshot().items, []);
});

test("read-only annotations do not downgrade default risk and simultaneous imports issue one write", async () => {
  const write = deferred<unknown>();
  const bodies: unknown[] = [];
  const controller = new MCPDiscoveryController(api(async (path, options) => {
    if (path.endsWith("/discover")) return discovery();
    bodies.push(options?.body);
    return write.promise;
  }), { role: "admin" });
  controller.select(server);
  await controller.discover();
  const first = controller.importTool(remote);
  assert.equal(await controller.importTool(remote, "read"), null);
  assert.equal(await controller.importTool(remote, "read"), null);
  assert.equal(bodies.length, 1);
  assert.deepEqual(bodies[0], { tool_name: remote.name, schema_hash: remote.schema_hash, risk: "write", response_policy: { max_bytes: 65536 } });
  write.resolve(imported);
  assert.equal((await first)?.id, imported.id);
  assert.equal(controller.getSnapshot().items[0].imported_tool_id, imported.id);
  assert.equal(await controller.importTool(remote, "read"), null);
  await controller.discover(); // An eventually consistent discovery cannot reopen the imported tool.
  assert.equal(controller.getSnapshot().items[0].imported_tool_id, imported.id);
  assert.equal(bodies.length, 1);
});

test("an ambiguous import cannot be resent until fresh discovery confirms its registry state", async () => {
  let imports = 0;
  let wasImported = false;
  const controller = new MCPDiscoveryController(api(async (path) => {
    if (path.endsWith("/discover")) return discovery([{ ...remote, ...(wasImported ? { imported_tool_id: imported.id } : {}) }]);
    imports++;
    wasImported = true;
    throw new TypeError("network lost after commit");
  }), { role: "admin" });
  controller.select(server);
  await controller.discover();
  assert.equal(await controller.importTool(remote), null);
  assert.match(controller.getSnapshot().importError, /previous import may have completed/);
  assert.equal(await controller.importTool(remote), null);
  assert.equal(imports, 1);
  await controller.discover();
  assert.equal(controller.getSnapshot().items[0].imported_tool_id, imported.id);
  assert.equal(await controller.importTool(remote), null);
  assert.equal(imports, 1);
});

test("import can retry after a new discovery confirms no import; changed schemas require fresh review", async () => {
  let imports = 0;
  const current = { ...remote, schema_hash: "changed-schema" };
  const controller = new MCPDiscoveryController(api(async (path) => {
    if (path.endsWith("/discover")) return discovery([current]);
    imports++;
    if (imports === 1) throw new APIError("Upstream unavailable", 502, "upstream_error");
    return { ...imported, risk: "read", mcp: { ...imported.mcp, schema_hash: current.schema_hash } };
  }), { role: "admin" });
  controller.select(server);
  await controller.discover();
  assert.equal(await controller.importTool(remote), null);
  assert.equal(imports, 0);
  assert.equal(await controller.importTool(current, "read"), null);
  assert.equal(imports, 1);
  await controller.discover();
  assert.equal((await controller.importTool(current, "read"))?.risk, "read");
  assert.equal(imports, 2);
});

test("late imports do not replace another server's review and known imported IDs remain reconciled", async () => {
  const write = deferred<unknown>();
  const controller = new MCPDiscoveryController(api(async (path) => path.endsWith("/discover") ? discovery() : write.promise), { role: "admin" });
  controller.select(server);
  await controller.discover();
  const first = controller.importTool(remote);
  controller.select({ ...server, id: "other" });
  await controller.discover();
  write.resolve(imported);
  await first;
  assert.equal(controller.getSnapshot().server?.id, "other");
  assert.equal(controller.getSnapshot().items[0].imported_tool_id, undefined);
  controller.select(server);
  await controller.discover();
  assert.equal(controller.getSnapshot().items[0].imported_tool_id, imported.id);
});

test("refresh cancellation does not leave a completed or failed import permanently busy", async () => {
  for (const fails of [false, true]) {
    const write = deferred<unknown>();
    const controller = new MCPDiscoveryController(api(async (path) => path.endsWith("/discover") ? discovery() : write.promise), { role: "admin" });
    controller.select(server);
    await controller.discover();
    const importing = controller.importTool(remote);
    controller.cancel(); // A refresh invalidates reads while the import may still commit.
    if (fails) write.reject(new TypeError("response lost"));
    else write.resolve(imported);
    await importing;
    assert.equal(controller.getSnapshot().importing, "");
    if (fails) assert.match(controller.getSnapshot().importError, /previous import may have completed/);
    else assert.equal(controller.getSnapshot().items[0].imported_tool_id, imported.id);
  }
});

test("API discovery accepts complete responses above ordinary tool result size", async () => {
  const originalFetch = globalThis.fetch;
  const items = Array.from({ length: 120 }, (_, index) => ({ ...remote, name: `tool_${index}`, description: "x".repeat(4000) }));
  const body = JSON.stringify(discovery(items));
  assert.ok(Buffer.byteLength(body) > 256 * 1024);
  assert.ok(Buffer.byteLength(body) < 4 * 1024 * 1024);
  globalThis.fetch = async () => new Response(body, { headers: { "Content-Type": "application/json" } });
  try {
    const result = await new APIClient().request<{ items: RemoteTool[]; total: number }>("/mcp/servers/orders/discover", { method: "POST", body: {} });
    assert.equal(result.items.length, 120);
    assert.equal(result.items[119].description.length, 4000);
  } finally { globalThis.fetch = originalFetch; }
});

test("import preserves explicit artifact policy and validates it before sending", async () => {
  const requests: unknown[] = [];
  const controller = new MCPDiscoveryController(api(async (path, options) => {
    if (path.endsWith("/discover")) return discovery();
    requests.push(options?.body);
    return imported;
  }), { role: "admin" });
  controller.select(server);
  await controller.discover();
  const policy = { max_bytes: 65536, artifact: { max_bytes: 262144, ttl_seconds: 600 } };
  assert.equal(await controller.importTool(remote, "read", { ...policy, artifact: { ...policy.artifact, ttl_seconds: 59 } }), null);
  assert.equal(requests.length, 0);
  await controller.importTool(remote, "read", policy);
  assert.deepEqual(requests, [{ tool_name: remote.name, schema_hash: remote.schema_hash, risk: "read", response_policy: policy }]);
});
