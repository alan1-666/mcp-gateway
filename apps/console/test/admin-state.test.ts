import assert from "node:assert/strict";
import test from "node:test";
import { APIClient, APIError, requiresConflictReload } from "../src/api.ts";
import {
  AdminAction,
  clientScopes,
  credentialHeaders,
  filterPath,
  isoDate,
  toggleGrant,
} from "../src/admin-state.ts";
function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((yes, no) => {
    resolve = yes;
    reject = no;
  });
  return { promise, resolve, reject };
}

test("a pending mutation admits one request even before React disables its button", async () => {
  const response = deferred<unknown>();
  let count = 0;
  const action = new AdminAction({
    async request<T>() {
      count++;
      return response.promise as Promise<T>;
    },
  });
  const first = action.run("/clients", { name: "service" });
  assert.equal(await action.run("/clients", { name: "service" }), null);
  assert.equal(count, 1);
  assert.equal(action.getSnapshot().busy, true);
  response.resolve({ client: { version: 1 } });
  assert.deepEqual(await first, { client: { version: 1 } });
  assert.equal(action.getSnapshot().busy, false);
});
test("conflict blocks resubmission until an explicit reload acknowledges current data", async () => {
  let count = 0;
  const received: unknown[] = [];
  const action = new AdminAction({
    async request<T>(_path: string, options?: { body?: unknown }) {
      count++;
      received.push(options?.body);
      if (count === 1)
        throw new APIError(
          "resource conflict: client was changed; reload before editing",
          409,
          "conflict",
        );
      return { version: 3 } as T;
    },
  });
  assert.equal(
    await action.run("/clients/client-id", { expected_version: 1 }),
    null,
  );
  assert.equal(action.getSnapshot().needsReload, true);
  assert.match(action.getSnapshot().error, /Reload current data/);
  assert.match(action.getSnapshot().error, /client was changed/);
  assert.equal(
    await action.run("/clients/client-id", { expected_version: 1 }),
    null,
  );
  assert.equal(count, 1);
  action.reset();
  await action.run("/clients/client-id", { expected_version: 2 });
  assert.deepEqual(received, [
    { expected_version: 1 },
    { expected_version: 2 },
  ]);
});
test("candidate business conflicts preserve their reason and allow an explicit corrected submission", async () => {
  for (const reason of [
    "upstream contract changed; create and review a fresh candidate",
    "upstream tool no longer exists",
    "candidate has no changes",
    "discard stale candidates before creating more",
  ]) {
    let count = 0;
    const message = `resource conflict: ${reason}`;
    const action = new AdminAction({
      async request<T>(): Promise<T> {
        count++;
        if (count === 1) throw new APIError(message, 409, "conflict");
        return { id: "new-candidate" } as T;
      },
    });
    await action.run("/tools/tool-id/candidates", { source_version: 1 });
    assert.equal(count, 1, "business failures must not automatically retry");
    assert.deepEqual(action.getSnapshot(), {
      busy: false,
      error: message,
      needsReload: false,
    });
    assert.deepEqual(
      await action.run("/tools/tool-id/candidates", { risk: "read" }),
      { id: "new-candidate" },
    );
    assert.equal(count, 2);
  }
});
test("only known record conflicts require reload; ambiguous legacy conflicts keep their original reason", () => {
  for (const message of [
    "resource conflict",
    "resource conflict: client was changed; reload before rotating",
    "resource conflict: credential changed; reload before retrying",
    "resource conflict: reconciliation changed; reload before adding evidence",
    "resource conflict: capacity limits changed; reload before saving",
    "resource conflict: tool changed while this candidate was under review",
  ]) {
    assert.equal(
      requiresConflictReload(new APIError(message, 409, "conflict")),
      true,
    );
  }
  assert.equal(
    requiresConflictReload(
      new APIError(
        "resource conflict: client name or version conflicts with an existing record",
        409,
        "conflict",
      ),
    ),
    false,
  );
  assert.equal(
    requiresConflictReload(new APIError("resource conflict", 403, "forbidden")),
    false,
  );
});
test("API errors preserve the gateway message and code without exposing extra response fields", async () => {
  const originalFetch = globalThis.fetch;
  const message =
    "resource conflict: upstream contract changed; create and review a fresh candidate";
  globalThis.fetch = async () =>
    new Response(
      JSON.stringify({
        error: {
          code: "conflict",
          message,
          upstream_body: "private-upstream-body",
        },
        headers: { Authorization: "private-token" },
      }),
      { status: 409, headers: { "Content-Type": "application/json" } },
    );
  try {
    await assert.rejects(
      new APIClient().request("/tools/tool-id/candidates", {
        method: "POST",
        body: { source_version: 1 },
      }),
      (error: unknown) => {
        assert.ok(error instanceof APIError);
        assert.equal(error.code, "conflict");
        assert.equal(error.status, 409);
        assert.equal(error.message, message);
        assert.doesNotMatch(String(error), /private-/);
        return true;
      },
    );
  } finally {
    globalThis.fetch = originalFetch;
  }
});
test("ambiguous network responses require reload and never automatically repeat a key rotation", async () => {
  let count = 0;
  const action = new AdminAction({
    async request<T>(): Promise<T> {
      count++;
      throw new TypeError("Network failed");
    },
  });
  await action.run("/clients/id/rotate", { expected_version: 4 });
  assert.match(action.getSnapshot().error, /may have completed/);
  assert.equal(action.getSnapshot().needsReload, true);
  await action.run("/clients/id/rotate", { expected_version: 4 });
  assert.equal(count, 1);
});
test("closing a page during key creation discards late one-time secret responses", async () => {
  const response = deferred<unknown>();
  const action = new AdminAction({
    async request<T>() {
      return response.promise as Promise<T>;
    },
  });
  const pending = action.run("/clients", { name: "background request" });
  action.cancel();
  response.resolve({ api_key: "test-secret-that-must-not-reappear" });
  assert.equal(await pending, null);
});
test("rate limiting is explained without an automatic retry or conflicting-version flag", async () => {
  let count = 0;
  const action = new AdminAction({
    async request<T>(): Promise<T> {
      count++;
      throw new APIError("limit exceeded", 429, "capacity_exceeded");
    },
  });
  await action.run("/operations/id/execute", {});
  assert.equal(count, 1);
  assert.equal(action.getSnapshot().needsReload, false);
  assert.match(action.getSnapshot().error, /capacity or request rate/);
});
test("credential headers preserve secret values and reject silent case-insensitive overwrites", () => {
  assert.deepEqual(
    credentialHeaders([
      { name: "Authorization", value: "Bearer exact-token" },
      { name: "X-Api-Key", value: "other" },
    ]),
    { Authorization: "Bearer exact-token", "X-Api-Key": "other" },
  );
  assert.throws(
    () =>
      credentialHeaders([
        { name: "authorization", value: "one" },
        { name: "Authorization", value: "two" },
      ]),
    /unique/,
  );
  assert.throws(
    () => credentialHeaders([{ name: "Authorization", value: "" }]),
    /secret value/,
  );
  assert.throws(() => credentialHeaders([]), /between 1 and 16/);
});
test("grant edits preserve selected tools outside loaded pages and never grant invoke without read", () => {
  const existing = ["old-tool-beyond-page", "current-tool"];
  assert.deepEqual(toggleGrant(existing, "new-tool"), [
    ...existing,
    "new-tool",
  ]);
  assert.deepEqual(toggleGrant(existing, "current-tool"), [
    "old-tool-beyond-page",
  ]);
  assert.deepEqual(existing, ["old-tool-beyond-page", "current-tool"]);
  assert.deepEqual(clientScopes(false, true), []);
  assert.deepEqual(clientScopes(true, false), ["tools:read"]);
  assert.deepEqual(clientScopes(true, true), ["tools:read", "tools:invoke"]);
});
test("filter URLs preserve opaque identifiers and RFC3339 times without stale cursor parameters", () => {
  const path = filterPath("/audit", {
    actor_id: " client:one ",
    action: "",
    from: isoDate("2026-10-05T10:30:00Z") ?? "",
    limit: "50",
  });
  const url = new URL(path, "https://gateway.test");
  assert.equal(url.searchParams.get("actor_id"), "client:one");
  assert.equal(url.searchParams.get("from"), "2026-10-05T10:30:00.000Z");
  assert.equal(url.searchParams.has("cursor"), false);
  assert.equal(url.searchParams.has("action"), false);
  assert.equal(isoDate(""), undefined);
  assert.throws(() => isoDate("not-a-date"), /valid date/);
});
