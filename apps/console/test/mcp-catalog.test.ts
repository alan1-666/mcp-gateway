import test from "node:test";
import assert from "node:assert/strict";
import { catalogCandidate } from "../src/mcp-catalog.ts";
import type { CatalogEntry } from "../src/mcp-catalog.ts";
import { MCPDiscoveryController } from "../src/mcp-servers.ts";
import type { APIClient } from "../src/api.ts";
import type { MCPServer } from "../src/types.ts";

const entry: CatalogEntry = {
  name: "search",
  gateway_name: "docs_search",
  status: "schema_changed",
  schema_hash: "b".repeat(64),
  imported_tool_id: "tool",
  imported_version: 3,
  imported_schema_hash: "a".repeat(64),
  imported_status: "published",
  imported_enabled: true,
};
test("catalog refresh pins reviewed schema and version, preserves policy by omitting overrides", () => {
  assert.deepEqual(catalogCandidate(entry, " review the changed query "), {
    expected_version: 3,
    expected_schema_hash: "b".repeat(64),
    reason: "review the changed query",
  });
  for (const status of ["missing", "unimported", "unchanged"] as const)
    assert.throws(() => catalogCandidate({ ...entry, status }, "review"));
  for (const imported_version of [0, 1.5, undefined])
    assert.throws(() =>
      catalogCandidate({ ...entry, imported_version }, "review"),
    );
  assert.throws(() =>
    catalogCandidate({ ...entry, schema_hash: "invalid" }, "review"),
  );
  for (const reason of ["", "   ", "中".repeat(334), "bad\0reason"])
    assert.throws(() => catalogCandidate(entry, reason));
  assert.doesNotThrow(() =>
    catalogCandidate(
      { ...entry, status: "description_changed" },
      "Review description",
    ),
  );
});

test("a failed refresh clears current comparison; switching servers discards stale results", async () => {
  const server: MCPServer = {
    id: "first",
    name: "Docs",
    namespace: "docs",
    workspace_id: "w",
    url: "https://docs.example/mcp",
    timeout_ms: 1000,
    enabled: true,
    created_at: "",
    updated_at: "",
  };
  const review = {
    id: "1",
    server_id: server.id,
    counts: { schema_changed: 1 },
    items: [entry],
  };
  let fail = false;
  let pending: ((result: unknown) => void) | undefined;
  const api = {
    request: async () => {
      if (fail) throw new Error("upstream timed out");
      if (pending)
        return new Promise((resolve) => {
          pending = resolve;
        });
      return { items: [], total: 0, review };
    },
  } as unknown as APIClient;
  const controller = new MCPDiscoveryController(api, { role: "admin" });
  controller.select(server);
  await controller.discover();
  assert.equal(controller.getSnapshot().review?.id, "1");
  fail = true;
  await controller.discover();
  assert.equal(controller.getSnapshot().review, null);
  assert.equal(controller.getSnapshot().loaded, false);
  fail = false;
  pending = () => {};
  const request = controller.discover();
  controller.select({ ...server, id: "second" });
  pending!({ items: [], total: 0, review });
  await request;
  assert.equal(controller.getSnapshot().server?.id, "second");
  assert.equal(controller.getSnapshot().review, null);
});
