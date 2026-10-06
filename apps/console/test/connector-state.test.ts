import test from "node:test";
import assert from "node:assert/strict";
import { APIError } from "../src/api.ts";
import {
  ConnectorController,
  connectorItems,
  connectorPresence,
  parseConnectorName,
} from "../src/connector-state.ts";
import type { Connector } from "../src/connector-state.ts";
import { parseServerDraft } from "../src/mcp-servers.ts";
const connector: Connector = {
  id: "connector-id",
  workspace_id: "workspace",
  name: "Staging",
  enabled: true,
  targets: [
    { name: "inventory", fingerprint: "a".repeat(64), transport: "stdio" },
  ],
  created_at: "2026-10-06T00:00:00Z",
};
function client(
  request: (
    path: string,
    options?: { method?: string; body?: unknown; signal?: AbortSignal },
  ) => Promise<unknown>,
) {
  return {
    async request<T>(
      path: string,
      options?: { method?: string; body?: unknown; signal?: AbortSignal },
    ) {
      return (await request(path, options)) as T;
    },
  };
}
function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((yes) => {
    resolve = yes;
  });
  return { resolve, promise };
}

test("private connection payload uses advertised targets and drops stale direct credentials", () => {
  const draft = {
    name: "Private inventory",
    namespace: "inventory",
    url: "https://old.example/mcp",
    credential: "OLD_SECRET",
    timeout: "10000",
    transport: "connector" as const,
    connectorID: connector.id,
    targetName: "inventory",
  };
  assert.deepEqual(parseServerDraft(draft, [connector]), {
    name: "Private inventory",
    namespace: "inventory",
    url: "",
    credential_ref: "",
    timeout_ms: 10000,
    connector_id: connector.id,
    target_name: "inventory",
  });
  assert.throws(() => parseServerDraft(draft), /enabled private connector/);
  assert.throws(
    () => parseServerDraft(draft, [{ ...connector, enabled: false }]),
    /enabled private connector/,
  );
  assert.throws(
    () => parseServerDraft({ ...draft, targetName: "other" }, [connector]),
    /advertised/,
  );
  const direct = parseServerDraft({ ...draft, transport: "http" }, [connector]);
  assert.equal(direct.credential_ref, "OLD_SECRET");
  assert.equal("connector_id" in direct, false);
});

test("presence is bounded by contact freshness and revocation overrides heartbeat", () => {
  const now = Date.parse("2026-10-06T00:00:30Z");
  assert.equal(connectorPresence(connector, now), "Awaiting connection");
  assert.equal(
    connectorPresence(
      { ...connector, last_seen_at: "2026-10-06T00:00:01Z" },
      now,
    ),
    "Online",
  );
  assert.equal(
    connectorPresence(
      { ...connector, last_seen_at: "2026-10-06T00:00:00Z" },
      now,
    ),
    "Offline",
  );
  assert.equal(
    connectorPresence({ ...connector, last_seen_at: "invalid" }, now),
    "Offline",
  );
  assert.equal(
    connectorPresence(
      { ...connector, last_seen_at: "2026-10-07T00:00:00Z" },
      now,
    ),
    "Offline",
  );
  assert.equal(
    connectorPresence(
      { ...connector, enabled: false, last_seen_at: "2026-10-06T00:00:29Z" },
      now,
    ),
    "Revoked",
  );
});

test("registration validation handles names and rejects malformed target lists", () => {
  assert.equal(parseConnectorName(" Staging "), "Staging");
  for (const name of ["", "中".repeat(41), "line\nbreak", "a\0b", "a\tb"])
    assert.throws(() => parseConnectorName(name));
  assert.deepEqual(connectorItems({ items: [connector], total: 1 }), [
    connector,
  ]);
  assert.throws(() => connectorItems({ items: [connector], total: 2 }));
  assert.throws(() =>
    connectorItems({
      items: [
        {
          ...connector,
          targets: [{ ...connector.targets[0], transport: "shell" as "stdio" }],
        },
      ],
      total: 1,
    }),
  );
});

test("one-time token is discarded and is never recreated by a list refresh", async () => {
  const controller = new ConnectorController(
    client(async (_path, options) =>
      options?.method === "POST"
        ? { connector, token: "private-token" }
        : { items: [connector], total: 1 },
    ),
  );
  assert.equal(await controller.create("Staging"), true);
  assert.deepEqual(controller.getSnapshot().secret, {
    connectorID: connector.id,
    token: "private-token",
  });
  assert.equal(
    await controller.create("Second"),
    false,
    "Do not lose an unsaved token by creating another registration",
  );
  controller.discardSecret();
  await controller.load();
  assert.equal(controller.getSnapshot().secret, null);
  assert.equal(
    JSON.stringify(controller.getSnapshot().items).includes("private-token"),
    false,
  );
});

test("leaving the token panel fences late registration and unmounted responses", async () => {
  for (const close of ["discard", "cancel"]) {
    const response = deferred<unknown>();
    const controller = new ConnectorController(
      client(async () => response.promise),
    );
    const creation = controller.create("Staging");
    if (close === "discard") controller.discardSecret();
    else controller.cancel();
    response.resolve({ connector, token: "late-token" });
    await creation;
    assert.equal(controller.getSnapshot().secret, null);
    assert.equal(
      controller.getSnapshot().items.length,
      close === "discard" ? 1 : 0,
    );
  }
});

test("revocation clears credentials, updates the record, and never automatically replays", async () => {
  const calls: string[] = [];
  const controller = new ConnectorController(
    client(async (path) => {
      calls.push(path);
      return path.endsWith("/revoke")
        ? { ...connector, enabled: false }
        : { connector, token: "private-token" };
    }),
  );
  await controller.create("Staging");
  await controller.revoke(connector.id);
  assert.equal(controller.getSnapshot().secret, null);
  assert.equal(controller.getSnapshot().items[0].enabled, false);
  assert.deepEqual(calls, ["/connectors", "/connectors/connector-id/revoke"]);
});

test("access loss clears cached registrations and the one-time token", async () => {
  const controller = new ConnectorController(
    client(async (_path, options) => {
      if (options?.method === "POST")
        return { connector, token: "private-token" };
      throw new APIError("Access denied", 403, "forbidden");
    }),
  );
  await controller.create("Staging");
  await controller.load();
  assert.equal(controller.getSnapshot().secret, null);
  assert.deepEqual(controller.getSnapshot().items, []);
});

test("ambiguous registration failures require checking state and are not retried", async () => {
  let calls = 0;
  const controller = new ConnectorController(
    client(async () => {
      calls++;
      throw new TypeError("Network failure");
    }),
  );
  await controller.create("Staging");
  assert.equal(calls, 1);
  await controller.create("Staging again");
  assert.equal(calls, 1, "Ambiguous registration cannot be resubmitted before refresh");
  assert.equal(controller.getSnapshot().requiresReload, true);
  assert.match(
    controller.getSnapshot().error,
    /registration may have completed/,
  );
  assert.equal(controller.getSnapshot().secret, null);
});

test("registration aborts an older list fetch so stale data cannot overwrite the new record", async () => {
  const response = deferred<unknown>();
  const controller = new ConnectorController(
    client(async (_path, options) =>
      options?.method === "POST"
        ? { connector, token: "private-token" }
        : response.promise,
    ),
  );
  const loading = controller.load();
  await controller.create("Staging");
  response.resolve({ items: [], total: 0 });
  await loading;
  assert.deepEqual(controller.getSnapshot().items, [connector]);
});
