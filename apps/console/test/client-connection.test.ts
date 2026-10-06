import assert from "node:assert/strict";
import test from "node:test";
import {
  checkClientConnection,
  connectionConfig,
  connectionWarnings,
} from "../src/client-connection";
import type { ClientAccess } from "../src/client-connection";
const key = `mgc_${"a".repeat(43)}`;
const client: ClientAccess = {
  id: "client-1",
  enabled: true,
  scopes: ["tools:read", "tools:invoke"],
  server_ids: [],
  tool_ids: ["tool-1"],
  key_expires_at: "2030-01-01T00:00:00Z",
};
const signal = () => new AbortController().signal;
const json = (value: unknown) => Response.json(value);
const initialize = (version: unknown = "2025-06-18") =>
  json({ jsonrpc: "2.0", id: 1, result: { protocolVersion: version } });
const resultResponse = (result: unknown) =>
  json({ jsonrpc: "2.0", id: 2, result });
const discovery = (
  page: unknown = {
    total: 1,
    items: [{ id: "tool-1", name: "Status", extra: "not returned" }],
  },
) =>
  resultResponse({ content: [{ type: "text", text: JSON.stringify(page) }] });
function fixture(
  responses = [
    json({ client_id: client.id }),
    initialize(),
    new Response(null, { status: 202 }),
    discovery(),
  ],
) {
  const calls: { path: string; init: RequestInit }[] = [];
  return {
    calls,
    fetcher: async (path: string, init: RequestInit) => {
      calls.push({ path, init });
      const response = responses.shift();
      assert.ok(response, "unexpected extra request");
      return response;
    },
  };
}
const withResult = (response: Response) =>
  fixture([
    json({ client_id: client.id }),
    initialize(),
    new Response(null, { status: 202 }),
    response,
  ]);

test("configuration uses active origin and a placeholder, rejecting unsafe origins", () => {
  for (const origin of [
    "https://rillgate.cn",
    "https://gateway.example:8443",
    "http://localhost:4782",
    "http://127.0.0.1:4782",
    "http://[::1]:4782",
  ]) {
    const config = JSON.parse(connectionConfig(origin));
    assert.equal(config.mcpServers.rillgate.url, `${origin}/mcp`);
    assert.equal(
      config.mcpServers.rillgate.headers.Authorization,
      "Bearer REPLACE_WITH_CLIENT_KEY",
    );
    assert.ok(!connectionConfig(origin).includes(key));
  }
  for (const origin of [
    "invalid",
    "http://gateway.example",
    "file:///",
    "https://user:password@example.com",
    "https://example.com/console/",
    "https://example.com?key=secret",
    "https://example.com#secret",
  ])
    assert.throws(() => connectionConfig(origin));
});
test("readiness checks expiry, disablement, scopes and grants", () => {
  assert.deepEqual(connectionWarnings(client), []);
  assert.deepEqual(
    connectionWarnings({ ...client, tool_ids: [], server_ids: ["server-1"] }),
    [],
  );
  assert.equal(
    connectionWarnings({
      ...client,
      enabled: false,
      key_expires_at: "invalid",
      scopes: [],
      tool_ids: [],
    }).length,
    5,
  );
  assert.equal(
    connectionWarnings({ ...client, key_expires_at: "2020-01-01T00:00:00Z" })
      .length,
    1,
  );
  assert.equal(
    connectionWarnings(client, Date.parse(client.key_expires_at)).length,
    1,
  );
});
test("realistic MCP handshake checks the selected machine identity and only discovers five tools", async () => {
  const f = fixture();
  assert.deepEqual(
    await checkClientConnection(client.id, key, signal(), f.fetcher),
    { total: 1, tools: [{ id: "tool-1", name: "Status" }] },
  );
  assert.deepEqual(
    f.calls.map((c) => c.path),
    ["/api/v1/me", "/mcp", "/mcp", "/mcp"],
  );
  for (const { init } of f.calls) {
    assert.equal(init.credentials, "omit");
    assert.equal(init.redirect, "error");
    assert.equal(init.cache, "no-store");
    assert.equal(
      (init.headers as Record<string, string>).Authorization,
      `Bearer ${key}`,
    );
  }
  assert.equal(f.calls[0].init.body, undefined);
  const messages = f.calls
    .slice(1)
    .map((c) => JSON.parse(c.init.body as string));
  assert.deepEqual(
    messages.map((m) => m.method),
    ["initialize", "notifications/initialized", "tools/call"],
  );
  assert.deepEqual(messages[2].params, {
    name: "search_tools",
    arguments: { limit: 5 },
  });
  assert.equal(
    (f.calls[3].init.headers as Record<string, string>)["MCP-Protocol-Version"],
    "2025-06-18",
  );
});
test("empty catalogs and negotiated protocols are supported", async () => {
  for (const version of ["2024-11-05", "2025-03-26", "2025-11-25"]) {
    const f = fixture([
      json({ client_id: client.id }),
      initialize(version),
      new Response(null, { status: 202 }),
      discovery({ total: 0, items: [] }),
    ]);
    assert.deepEqual(
      await checkClientConnection(client.id, key, signal(), f.fetcher),
      { total: 0, tools: [] },
    );
    assert.equal(
      (f.calls[3].init.headers as Record<string, string>)[
        "MCP-Protocol-Version"
      ],
      version,
    );
  }
});
test("invalid keys and another client or administrator identity never proceed to MCP", async () => {
  for (const invalid of [
    "",
    "personal_key",
    `${key}\n`,
    ` ${key}`,
    "mgc_short",
  ]) {
    const f = fixture();
    await assert.rejects(
      checkClientConnection(client.id, invalid, signal(), f.fetcher),
      /machine client/,
    );
    assert.equal(f.calls.length, 0);
  }
  for (const identity of [
    { client_id: "another-client" },
    { id: client.id, role: "admin" },
    null,
    [],
  ]) {
    const f = fixture([json(identity)]);
    await assert.rejects(
      checkClientConnection(client.id, key, signal(), f.fetcher),
      /selected client/,
    );
    assert.equal(f.calls.length, 1);
  }
});
test("HTTP, network and cancellation failures do not echo secrets or raw responses", async () => {
  for (const [status, message] of [
    [401, /key rejected/],
    [403, /Access denied/],
    [429, /capacity/],
    [500, /HTTP 500/],
  ] as const) {
    await assert.rejects(
      checkClientConnection(
        client.id,
        key,
        signal(),
        fixture([new Response(key, { status })]).fetcher,
      ),
      message,
    );
  }
  await assert.rejects(
    checkClientConnection(client.id, key, signal(), async () => {
      throw new Error(key);
    }),
    /Cannot reach/,
  );
  const before = new AbortController();
  before.abort();
  const f = fixture();
  await assert.rejects(
    checkClientConnection(client.id, key, before.signal, f.fetcher),
    { name: "AbortError" },
  );
  assert.equal(f.calls.length, 0);
  const during = new AbortController();
  await assert.rejects(
    checkClientConnection(client.id, key, during.signal, async () => {
      during.abort();
      throw new Error(key);
    }),
    /cancelled or timed out/,
  );
});
test("HTML, missing bodies, invalid JSON and oversized streamed responses fail closed", async () => {
  for (const [response, message] of [
    [new Response("<!doctype html>"), /Expected a JSON/],
    [
      new Response(null, { headers: { "Content-Type": "application/json" } }),
      /Expected a JSON/,
    ],
    [
      new Response("{", { headers: { "Content-Type": "application/json" } }),
      /invalid JSON/,
    ],
    [
      new Response('"' + "a".repeat(256 * 1024) + '"', {
        headers: { "Content-Type": "application/json" },
      }),
      /exceeded/,
    ],
  ] as const)
    await assert.rejects(
      checkClientConnection(
        client.id,
        key,
        signal(),
        fixture([response]).fetcher,
      ),
      message,
    );
});
test("MCP envelopes, versions and discovery response shapes are validated", async () => {
  for (const envelope of [
    null,
    [],
    { jsonrpc: "1.0", id: 1, result: {} },
    { jsonrpc: "2.0", id: 99, result: {} },
    { jsonrpc: "2.0", id: 1, error: { message: key } },
    { jsonrpc: "2.0", id: 1, result: [] },
  ]) {
    await assert.rejects(
      checkClientConnection(
        client.id,
        key,
        signal(),
        fixture([json({ client_id: client.id }), json(envelope)]).fetcher,
      ),
      /incompatible MCP/,
    );
  }
  for (const version of ["future-version", null])
    await assert.rejects(
      checkClientConnection(
        client.id,
        key,
        signal(),
        fixture([json({ client_id: client.id }), initialize(version)]).fetcher,
      ),
      /unsupported MCP/,
    );
  for (const [result, message] of [
    [
      { isError: true, content: [{ type: "text", text: key }] },
      /discovery was denied/,
    ],
    [{}, /incompatible discovery/],
    [{ content: [] }, /incompatible discovery/],
    [{ content: [null] }, /incompatible discovery/],
    [{ content: [{ type: "image", text: "{}" }] }, /incompatible discovery/],
    [{ content: [{ type: "text", text: 5 }] }, /incompatible discovery/],
    [{ content: [{ type: "text", text: "{" }] }, /invalid discovery JSON/],
  ] as const)
    await assert.rejects(
      checkClientConnection(
        client.id,
        key,
        signal(),
        withResult(resultResponse(result)).fetcher,
      ),
      message,
    );
});
test("invalid catalog counts and tool summaries cannot appear as successful checks", async () => {
  for (const page of [
    null,
    {},
    { total: -1, items: [] },
    { total: 1.2, items: [] },
    { total: 0, items: [{}] },
    { total: 6, items: Array(6).fill({}) },
  ]) {
    await assert.rejects(
      checkClientConnection(
        client.id,
        key,
        signal(),
        withResult(discovery(page)).fetcher,
      ),
      /invalid discovery page/,
    );
  }
  for (const item of [
    null,
    {},
    { id: "", name: "x" },
    { id: "x", name: 5 },
    { id: "x", name: "" },
  ]) {
    await assert.rejects(
      checkClientConnection(
        client.id,
        key,
        signal(),
        withResult(discovery({ total: 1, items: [item] })).fetcher,
      ),
      /invalid tool summary/,
    );
  }
});
test("default transport uses fetch", async () => {
  const saved = globalThis.fetch;
  const f = fixture();
  globalThis.fetch = f.fetcher as typeof fetch;
  try {
    assert.equal(
      (await checkClientConnection(client.id, key, signal())).total,
      1,
    );
  } finally {
    globalThis.fetch = saved;
  }
});
