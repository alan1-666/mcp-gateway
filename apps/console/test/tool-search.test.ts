import assert from "node:assert/strict";
import test from "node:test";
import { APIError } from "../src/api.ts";
import { fetchToolDetails } from "../src/tool-details.ts";
import {
  normalizeToolQuery,
  toolSearchPath,
  ToolSearchController,
  validateToolPage,
} from "../src/tool-search.ts";
import type { Tool, ToolPage } from "../src/types.ts";

type Item = { id: string; name: string };

function page(ids: string[], total = 602, cursor = ""): ToolPage<Item> {
  return {
    items: ids.map((id) => ({ id, name: id })),
    total,
    ...(cursor ? { next_cursor: cursor } : {}),
  };
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

function harness() {
  const calls: {
    query: string;
    cursor: string;
    signal: AbortSignal;
    result: ReturnType<typeof deferred<ToolPage<Item>>>;
  }[] = [];
  const controller = new ToolSearchController<Item>((query, cursor, signal) => {
    const result = deferred<ToolPage<Item>>();
    calls.push({ query, cursor, signal, result });
    return result.promise;
  });
  return { controller, calls };
}

test("tool search encodes literal queries and opaque cursors without broadening them", () => {
  const query = "  大写_100%\\  ";
  const path = toolSearchPath("discovery", query, "opaque+/== &value", 30);
  const url = new URL(path, "https://gateway.test");
  assert.equal(url.pathname, "/catalog/tools");
  assert.deepEqual(
    [...url.searchParams],
    [
      ["limit", "30"],
      ["query", "大写_100%\\"],
      ["cursor", "opaque+/== &value"],
    ],
  );
  assert.equal(toolSearchPath("registry", "  "), "/tools?limit=50");
  assert.equal(toolSearchPath("discovery", ""), "/catalog/tools?limit=25");
});

test("query limits use trimmed UTF-8 bytes, and pagination bounds are enforced", () => {
  assert.equal(normalizeToolQuery(" 文 "), "文");
  assert.equal(normalizeToolQuery("文".repeat(66)).length, 66);
  assert.throws(() => normalizeToolQuery("文".repeat(67)), /200 UTF-8 bytes/);
  assert.equal(normalizeToolQuery("a".repeat(200)).length, 200);
  for (const limit of [0, -1, 51, 1.5, NaN]) {
    assert.throws(() => toolSearchPath("registry", "", "", limit), /Page size/);
  }
  assert.throws(
    () => toolSearchPath("discovery", "", "a".repeat(2049)),
    /invalid search cursor/,
  );
});

test("query changes clear page state immediately and discard late first-page responses", async () => {
  const { controller, calls } = harness();
  const old = controller.setQuery("old");
  const current = controller.setQuery("new");
  assert.equal(calls[0].signal.aborted, true);
  assert.deepEqual(controller.getSnapshot().items, []);
  assert.equal(controller.getSnapshot().total, null);
  assert.equal(controller.getSnapshot().nextCursor, "");
  assert.equal(controller.getSnapshot().phase, "loading");
  calls[1].result.resolve(page(["new-match"], 1));
  await current;
  calls[0].result.resolve(page(["wrong-old-match"], 999, "wrong-cursor"));
  await old;
  assert.deepEqual(
    controller.getSnapshot().items.map((item) => item.id),
    ["new-match"],
  );
  assert.equal(controller.getSnapshot().query, "new");
  assert.equal(controller.getSnapshot().total, 1);
  assert.equal(controller.getSnapshot().nextCursor, "");
});

test("late next-page responses cannot append to a new query", async () => {
  const { controller, calls } = harness();
  const first = controller.setQuery("old");
  calls[0].result.resolve(page(["old-1"], 602, "old-next"));
  await first;
  const next = controller.loadMore();
  const current = controller.setQuery("current");
  calls[2].result.resolve(page(["current-1"], 1));
  await current;
  calls[1].result.resolve(page(["old-2"], 602));
  await next;
  assert.deepEqual(
    controller.getSnapshot().items.map((item) => item.id),
    ["current-1"],
  );
  assert.equal(controller.getSnapshot().total, 1);
});

test("late failures and cancelled view requests never replace current state", async () => {
  const { controller, calls } = harness();
  const first = controller.setQuery("old");
  const next = controller.setQuery("current");
  calls[1].result.resolve(page(["current"], 1));
  await next;
  calls[0].result.reject(new Error("Old request failed"));
  await first;
  assert.equal(controller.getSnapshot().error, "");
  const pending = controller.reload();
  const beforeCancel = controller.getSnapshot();
  controller.cancel();
  calls[2].result.resolve(page(["should-not-render"], 1));
  await pending;
  assert.equal(controller.getSnapshot(), beforeCancel);
});

test("load more retains server totals, deduplicates records, and prevents duplicate requests", async () => {
  const { controller, calls } = harness();
  const first = controller.setQuery("");
  calls[0].result.resolve(page(["one", "two"], 602, "page-two"));
  await first;
  const more = controller.loadMore();
  await controller.loadMore();
  assert.equal(calls.length, 2);
  assert.equal(calls[1].cursor, "page-two");
  assert.equal(controller.getSnapshot().phase, "loading-more");
  calls[1].result.resolve({
    ...page(["two", "three"], 601),
    items: [
      { id: "two", name: "updated" },
      { id: "three", name: "three" },
    ],
  });
  await more;
  const state = controller.getSnapshot();
  assert.deepEqual(
    state.items.map((item) => item.id),
    ["one", "two", "three"],
  );
  assert.equal(state.items[1].name, "updated");
  assert.equal(state.total, 601);
  assert.equal(state.nextCursor, "");
  await controller.loadMore();
  assert.equal(calls.length, 2);
});

test("failed next pages preserve loaded data and retry the same query and cursor", async () => {
  const { controller, calls } = harness();
  const first = controller.setQuery("invoice");
  calls[0].result.resolve(page(["one"], 602, "opaque-next"));
  await first;
  const more = controller.loadMore();
  calls[1].result.reject(new Error("Temporary service failure"));
  await more;
  const failed = controller.getSnapshot();
  assert.equal(failed.error, "Temporary service failure");
  assert.deepEqual(
    failed.items.map((item) => item.id),
    ["one"],
  );
  assert.equal(failed.total, 602);
  assert.equal(failed.nextCursor, "opaque-next");
  const retry = controller.retry();
  assert.equal(calls[2].query, "invoice");
  assert.equal(calls[2].cursor, "opaque-next");
  calls[2].result.resolve(page(["old-tool-beyond-500"], 602));
  await retry;
  assert.equal(controller.getSnapshot().error, "");
  assert.equal(controller.getSnapshot().items.length, 2);
});

test("retrying an initial failure starts the first page and refresh drops the old boundary", async () => {
  const { controller, calls } = harness();
  const first = controller.setQuery("name");
  calls[0].result.reject(new Error("Offline"));
  await first;
  assert.equal(controller.getSnapshot().loaded, false);
  const retry = controller.retry();
  assert.equal(calls[1].cursor, "");
  calls[1].result.resolve(page(["one"], 10, "old-boundary"));
  await retry;
  const restart = controller.reload();
  assert.equal(calls[2].cursor, "");
  assert.equal(calls[2].query, "name");
  assert.deepEqual(controller.getSnapshot().items, []);
  calls[2].result.resolve(page(["newly-created"], 11));
  await restart;
  assert.equal(controller.getSnapshot().total, 11);
});

test("invalid new queries erase prior results without issuing a request", async () => {
  const { controller, calls } = harness();
  const first = controller.setQuery("");
  calls[0].result.resolve(page(["one"], 602, "next"));
  await first;
  await controller.setQuery("文".repeat(67));
  assert.equal(calls.length, 1);
  assert.deepEqual(controller.getSnapshot().items, []);
  assert.equal(controller.getSnapshot().total, null);
  assert.equal(controller.getSnapshot().nextCursor, "");
  assert.match(controller.getSnapshot().error, /200 UTF-8 bytes/);
});

test("revoked permissions clear previously loaded tool records instead of retaining them", async () => {
  for (const status of [401, 403]) {
    const { controller, calls } = harness();
    const first = controller.setQuery("");
    calls[0].result.resolve(page(["private-tool"], 602, "next"));
    await first;
    const more = controller.loadMore();
    calls[1].result.reject(new APIError("Access denied", status, "forbidden"));
    await more;
    assert.deepEqual(controller.getSnapshot().items, []);
    assert.equal(controller.getSnapshot().total, null);
    assert.equal(controller.getSnapshot().nextCursor, "");
    assert.equal(controller.getSnapshot().loaded, false);
  }
});

test("empty results are a completed search, invalid pages cannot loop pagination", async () => {
  const controller = new ToolSearchController<Item>(async () => page([], 0));
  await controller.setQuery("no-match");
  assert.equal(controller.getSnapshot().loaded, true);
  assert.equal(controller.getSnapshot().total, 0);
  assert.equal(controller.getSnapshot().phase, "idle");
  assert.throws(() => validateToolPage(page([], -1)), /invalid tool page/);
  assert.throws(
    () => validateToolPage(page([], Infinity)),
    /invalid tool page/,
  );
  assert.throws(() => validateToolPage(page([""], 1)), /invalid tool page/);
  assert.throws(
    () => validateToolPage(page(["one"], 2, "same"), "same"),
    /did not advance/,
  );
});

const oldTool: Tool = {
  id: "tool-older-than-500",
  workspace_id: "workspace",
  name: "old.invoice.lookup",
  description: "Look up an invoice",
  risk: "read",
  version: 3,
  input_schema: { type: "object", required: ["invoice_id"] },
  http: {
    method: "GET",
    url: "https://example.test/invoices",
    timeout_ms: 5000,
  },
  enabled: true,
  status: "published",
  created_at: "2020-01-01T00:00:00Z",
};

test("selected tool schema loads by ID without depending on any search page", async () => {
  const requested: string[] = [];
  const signal = new AbortController().signal;
  const api = {
    async request<T>(
      path: string,
      options?: { signal?: AbortSignal },
    ): Promise<T> {
      requested.push(path);
      assert.equal(options?.signal, signal);
      return oldTool as T;
    },
  };
  const selected = await fetchToolDetails(api, oldTool.id, true, signal);
  assert.deepEqual(requested, ["/tools/tool-older-than-500"]);
  assert.deepEqual(selected.input_schema, oldTool.input_schema);
});

test("invocation refuses a selected tool that became disabled or draft", async () => {
  for (const tool of [
    { ...oldTool, enabled: false },
    { ...oldTool, status: "draft" as const },
  ]) {
    const api = {
      async request<T>(): Promise<T> {
        return tool as T;
      },
    };
    await assert.rejects(
      () => fetchToolDetails(api, oldTool.id, true),
      /no longer published and enabled/,
    );
    assert.equal(await fetchToolDetails(api, oldTool.id, false), tool);
  }
  const wrongID = {
    async request<T>(): Promise<T> {
      return { ...oldTool, id: "another" } as T;
    },
  };
  await assert.rejects(
    () => fetchToolDetails(wrongID, oldTool.id, true),
    /different tool/,
  );
});


test("server filters are encoded independently from queries and cursors", () => {
  const path = toolSearchPath("discovery", "task", "cursor", 5, "server-1");
  const params = new URL(path, "https://gateway.example").searchParams;
  assert.equal(params.get("server_id"), "server-1");
  assert.equal(params.get("query"), "task");
  assert.equal(params.get("cursor"), "cursor");
  assert.throws(() => toolSearchPath("discovery", "", "", 5, "bad server"));
  assert.throws(() => toolSearchPath("discovery", "", "", 5, "a".repeat(129)));
});
