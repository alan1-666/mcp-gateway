import assert from "node:assert/strict";
import test from "node:test";
import {
  canControlRun,
  canResumeRun,
  compareEventIDs,
  consumeRunEvents,
  displayedOutput,
  emptyFeed,
  EVENT_HISTORY_LIMIT,
  OUTPUT_LIMIT,
  validatePrompt,
} from "../src/run-state.ts";
import type { AgentRun, RunEvent } from "../src/run-state.ts";
import {
  clearTaskDraft,
  readTaskDraft,
  saveTaskDraft,
} from "../src/run-draft.ts";

const identity = { id: "creator", workspace_id: "team", role: "operator" };
const run: AgentRun = {
  id: "run-1",
  workspace_id: "team",
  actor_id: "creator",
  prompt: "Inspect service status",
  state: "RUNNING",
  attempt: 1,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};
function event(
  id: string,
  type: string,
  data: Record<string, unknown> = {},
): RunEvent {
  return { id, type, data, run_id: run.id, created_at: run.created_at };
}

test("event cursors retain exact decimal ordering beyond the safe integer range", () => {
  assert.equal(compareEventIDs("9007199254740993", "9007199254740992"), 1);
  assert.equal(
    compareEventIDs("100000000000000000000", "99999999999999999999"),
    1,
  );
  assert.equal(compareEventIDs("0007", "7"), 0);
  assert.throws(() => compareEventIDs("1e20", "2"), /invalid event cursor/);
});

test("repeated and overlapping event pages never duplicate streamed output", () => {
  let feed = consumeRunEvents(emptyFeed(), [
    event("9007199254740992", "RUN_CLAIMED", { attempt: 1 }),
    event("9007199254740993", "TEXT_DELTA", { attempt: 1, text: "hello " }),
    event("9007199254740993", "TEXT_DELTA", { attempt: 1, text: "hello " }),
  ]);
  feed = consumeRunEvents(feed, [
    event("9007199254740994", "TEXT_DELTA", { attempt: 1, text: "world" }),
    event("9007199254740993", "TEXT_DELTA", { attempt: 1, text: "hello " }),
  ]);
  assert.equal(feed.output, "hello world");
  assert.equal(feed.count, 3);
  assert.equal(feed.cursor, "9007199254740994");
  assert.equal(consumeRunEvents(feed, feed.items), feed);
});

test("new attempts reset streamed output and completed snapshots replace it", () => {
  let feed = consumeRunEvents(emptyFeed(), [
    event("1", "RUN_CLAIMED", { attempt: 1 }),
    event("2", "TEXT_DELTA", { attempt: 1, text: "old" }),
  ]);
  assert.equal(displayedOutput(run, feed).text, "old");
  assert.equal(displayedOutput({ ...run, attempt: 2 }, feed).text, "");
  feed = consumeRunEvents(feed, [
    event("3", "RUN_RESUMED"),
    event("4", "RUN_CLAIMED", { attempt: 2 }),
    event("5", "TEXT_DELTA", { attempt: 2, text: "new" }),
  ]);
  assert.equal(displayedOutput({ ...run, attempt: 2 }, feed).text, "new");
  assert.equal(
    displayedOutput(
      { ...run, state: "WAITING_APPROVAL", output: "checkpoint" },
      feed,
    ).text,
    "checkpoint",
  );
  assert.equal(
    displayedOutput(
      { ...run, state: "SUCCEEDED", output: "final output" },
      feed,
    ).text,
    "final output",
  );
});

test("visible event history and output are bounded without losing cursor or deduplication", () => {
  const events = [
    event("1", "RUN_CLAIMED", { attempt: 1 }),
    ...Array.from({ length: EVENT_HISTORY_LIMIT + 10 }, (_, index) =>
      event(String(index + 2), "TEXT_DELTA", { attempt: 1, text: "x" }),
    ),
  ];
  const feed = consumeRunEvents(emptyFeed(), events);
  assert.equal(feed.items.length, EVENT_HISTORY_LIMIT);
  assert.equal(feed.count, events.length);
  assert.equal(feed.output.length, events.length - 1);
  const large = consumeRunEvents(feed, [
    event(String(events.length + 1), "TEXT_DELTA", {
      attempt: 1,
      text: "y".repeat(OUTPUT_LIMIT + 10),
    }),
  ]);
  assert.equal(large.output.length, OUTPUT_LIMIT);
  assert.equal(large.truncated, true);
});

test("late text from an older attempt and untagged legacy text never enter current output", () => {
  const feed = consumeRunEvents(emptyFeed(), [
    event("1", "RUN_CLAIMED", { attempt: 2 }),
    event("2", "TEXT_DELTA", { text: "current", attempt: 2 }),
    event("3", "TEXT_DELTA", { text: "old replay", attempt: 1 }),
    event("4", "TEXT_DELTA", { text: "legacy without attempt" }),
    event("5", "TEXT_DELTA", { text: " forged fractional", attempt: 2.5 }),
  ]);
  assert.equal(feed.output, "current");
  assert.equal(feed.items.length, 5);
  assert.equal(feed.cursor, "5");
  assert.equal(
    displayedOutput(
      { ...run, state: "SUCCEEDED", output: "authoritative final" },
      feed,
    ).text,
    "authoritative final",
  );
});

test("task controls stay within the creator or same-workspace administrator", () => {
  assert.equal(canControlRun(identity, run), true);
  assert.equal(canControlRun({ ...identity, id: "other" }, run), false);
  assert.equal(
    canControlRun({ ...identity, id: "reviewer", role: "approver" }, run),
    false,
  );
  assert.equal(
    canControlRun({ ...identity, id: "admin", role: "admin" }, run),
    true,
  );
  assert.equal(
    canControlRun({ ...identity, workspace_id: "another", role: "admin" }, run),
    false,
  );
});

test("resume waits for approval and a known completed or ready operation", () => {
  const paused = { ...run, state: "NEEDS_REVIEW" as const };
  for (const state of ["UNKNOWN", "DISPATCHING", "WAITING_APPROVAL"] as const)
    assert.equal(canResumeRun(paused, state), false);
  assert.equal(canResumeRun(paused, "READY"), true);
  assert.equal(canResumeRun(paused, "SUCCEEDED"), true);
  assert.equal(canResumeRun({ ...run, state: "WAITING_CREDENTIALS" }), true);
  for (const state of [
    "RUNNING",
    "QUEUED",
    "SUCCEEDED",
    "FAILED",
    "CANCELLED",
  ] as const)
    assert.equal(canResumeRun({ ...run, state }), false);
});

test("prompt length counts Unicode characters and requires nonempty instructions", () => {
  assert.equal(
    validatePrompt("  Check service health  "),
    "Check service health",
  );
  assert.equal(validatePrompt("🔎".repeat(8000)).length, 16000);
  assert.throws(() => validatePrompt("🔎".repeat(8001)), /8,000/);
  assert.throws(() => validatePrompt(" \n "), /1–8,000/);
});

function storage() {
  const values = new Map<string, string>();
  return {
    getItem: (key: string) => values.get(key) ?? null,
    setItem: (key: string, value: string) => {
      values.set(key, value);
    },
    removeItem: (key: string) => {
      values.delete(key);
    },
    values,
  };
}

test("an ambiguous creation request survives refresh with its exact prompt and key", () => {
  const cache = storage();
  const draft = {
    version: 1 as const,
    prompt: "Check service health",
    idempotency_key: "09f00511-a183-48c8-8d9c-3ca8eb929381",
    submitted: true,
  };
  assert.equal(saveTaskDraft(identity, draft, cache), true);
  assert.deepEqual(readTaskDraft(identity, cache), draft);
  assert.equal(readTaskDraft({ ...identity, id: "other" }, cache), null);
  assert.equal(
    readTaskDraft({ ...identity, workspace_id: "other" }, cache),
    null,
  );
  clearTaskDraft(identity, cache);
  assert.equal(readTaskDraft(identity, cache), null);
});

test("malformed or unavailable browser storage never prevents workspace access", () => {
  const cache = storage();
  saveTaskDraft(
    identity,
    {
      version: 1,
      prompt: "ok",
      idempotency_key: "09f00511-a183-48c8-8d9c-3ca8eb929381",
      submitted: false,
    },
    cache,
  );
  const key = [...cache.values.keys()][0];
  cache.values.set(key, '{"version":1,"idempotency_key":"untrusted"}');
  assert.equal(readTaskDraft(identity, cache), null);
  const blocked = {
    getItem: () => {
      throw new Error("blocked");
    },
    setItem: () => {
      throw new Error("blocked");
    },
    removeItem: () => {
      throw new Error("blocked");
    },
  };
  assert.equal(readTaskDraft(identity, blocked), null);
  assert.equal(
    saveTaskDraft(
      identity,
      { version: 1, prompt: "test", idempotency_key: "x", submitted: true },
      blocked,
    ),
    false,
  );
  assert.doesNotThrow(() => clearTaskDraft(identity, blocked));
});
