import assert from "node:assert/strict";
import test from "node:test";
import {
  APIClient,
  APIError,
  messageOf,
  requiresConflictReload,
} from "../src/api";

test("requests isolate machine authentication from browser sessions and preserve cancellation", async () => {
  const original = globalThis.fetch;
  const calls: RequestInit[] = [];
  globalThis.fetch = async (path, init) => {
    assert.equal(path, "/api/v1/me");
    calls.push(init!);
    return Response.json({ id: "actor" });
  };
  try {
    const session = new APIClient();
    session.setCSRF("csrf-fixture");
    const controller = new AbortController();
    assert.deepEqual(
      await session.request("/me", {
        method: "POST",
        body: { value: 1 },
        signal: controller.signal,
      }),
      { id: "actor" },
    );
    const browser = calls[0];
    assert.equal(browser.credentials, "same-origin");
    assert.equal(browser.redirect, "error");
    assert.equal(browser.cache, "no-store");
    assert.equal(
      (browser.headers as Record<string, string>)["X-CSRF-Token"],
      "csrf-fixture",
    );
    assert.equal(
      (browser.headers as Record<string, string>)["Content-Type"],
      "application/json",
    );
    assert.equal(browser.body, '{"value":1}');
    controller.abort();
    assert.equal(browser.signal?.aborted, true);
    await new APIClient("machine-fixture").request("/me", { method: "GET" });
    assert.equal(calls[1].credentials, "omit");
    assert.equal(
      (calls[1].headers as Record<string, string>).Authorization,
      "Bearer machine-fixture",
    );
    assert.equal(calls[1].body, undefined);
  } finally {
    globalThis.fetch = original;
  }
});

test("API rejects HTML and classifies structured and malformed error envelopes", async () => {
  const original = globalThis.fetch;
  try {
    for (const body of [
      null,
      "error",
      {},
      { error: null },
      { error: "error" },
      { error: {} },
      { error: { message: 5, code: 7 } },
      { error: { message: "denied", code: "forbidden" } },
    ]) {
      globalThis.fetch = async () => Response.json(body, { status: 403 });
      await assert.rejects(new APIClient().request("/me"), (error: unknown) => {
        assert.ok(error instanceof APIError);
        assert.equal(error.status, 403);
        const expected =
          body &&
          typeof body === "object" &&
          "error" in body &&
          typeof body.error === "object" &&
          body.error &&
          "message" in body.error &&
          body.error.message === "denied";
        assert.equal(
          error.message,
          expected ? "denied" : "Request failed (403).",
        );
        assert.equal(error.code, expected ? "forbidden" : "request_failed");
        return true;
      });
    }
    globalThis.fetch = async () =>
      new Response("<!doctype html>", { status: 502 });
    await assert.rejects(
      new APIClient().request("/me"),
      (error: unknown) =>
        error instanceof APIError && error.code === "invalid_response",
    );
  } finally {
    globalThis.fetch = original;
  }
});

test("action errors distinguish stale configuration, limits and transport failures", () => {
  assert.equal(
    requiresConflictReload(
      new APIError(" resource conflict ", 409, "conflict"),
    ),
    true,
  );
  assert.equal(
    requiresConflictReload(new APIError("needs approval", 409, "conflict")),
    false,
  );
  assert.equal(
    requiresConflictReload(new APIError("resource conflict", 403, "forbidden")),
    false,
  );
  assert.equal(requiresConflictReload(new Error("resource conflict")), false);
  assert.match(messageOf(new APIError("limit", 429, "limited")), /capacity/);
  assert.equal(messageOf(new APIError("denied", 403, "forbidden")), "denied");
  assert.match(
    messageOf(new DOMException("timeout", "TimeoutError")),
    /did not respond/,
  );
  assert.match(messageOf(new TypeError("network")), /Unable to reach/);
  assert.equal(messageOf(new Error("ordinary")), "ordinary");
  assert.equal(messageOf(null), "The request could not be completed.");
});
