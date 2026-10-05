import type { Identity, OperationState } from "./types";

export type RunState =
  | "QUEUED"
  | "RUNNING"
  | "WAITING_APPROVAL"
  | "WAITING_CREDENTIALS"
  | "NEEDS_REVIEW"
  | "SUCCEEDED"
  | "FAILED"
  | "CANCELLED";

export interface AgentRun {
  id: string;
  workspace_id: string;
  actor_id: string;
  prompt: string;
  state: RunState;
  attempt: number;
  output?: string;
  error_code?: string;
  waiting_operation_id?: string;
  created_at: string;
  updated_at: string;
}

export interface RunEvent {
  id: string;
  run_id: string;
  type: string;
  data: Record<string, unknown>;
  created_at: string;
}

export interface RuntimeStatus {
  online: boolean;
  model_ready: boolean;
  provider?: string;
  model_id?: string;
  error_code?: string;
  last_seen_at?: string;
}

export const OUTPUT_LIMIT = 262144;
export const EVENT_HISTORY_LIMIT = 500;
export const terminalRunStates = new Set<RunState>([
  "SUCCEEDED",
  "FAILED",
  "CANCELLED",
]);
const resumableStates = new Set<RunState>([
  "WAITING_APPROVAL",
  "WAITING_CREDENTIALS",
  "NEEDS_REVIEW",
]);

export function canControlRun(identity: Identity, run: AgentRun): boolean {
  return (
    identity.workspace_id === run.workspace_id &&
    (identity.id === run.actor_id || identity.role === "admin")
  );
}

export function canResumeRun(
  run: AgentRun,
  operationState?: OperationState,
): boolean {
  return (
    resumableStates.has(run.state) &&
    operationState !== "UNKNOWN" &&
    operationState !== "DISPATCHING" &&
    operationState !== "WAITING_APPROVAL"
  );
}

export function isResumableState(state: RunState): boolean {
  return resumableStates.has(state);
}

function decimalID(id: string): string {
  if (!/^\d+$/.test(id))
    throw new Error("The gateway returned an invalid event cursor.");
  return id.replace(/^0+(?=\d)/, "");
}

export function compareEventIDs(a: string, b: string): number {
  const left = decimalID(a);
  const right = decimalID(b);
  return left.length === right.length
    ? left === right
      ? 0
      : left < right
        ? -1
        : 1
    : left.length < right.length
      ? -1
      : 1;
}

export interface EventFeed {
  cursor: string;
  items: RunEvent[];
  output: string;
  attempt: number;
  truncated: boolean;
  count: number;
}

export function emptyFeed(): EventFeed {
  return {
    cursor: "0",
    items: [],
    output: "",
    attempt: 0,
    truncated: false,
    count: 0,
  };
}

// Cursors stay decimal strings: a database sequence can exceed JavaScript's
// exact integer range. Deduplication also prevents a repeated page adding text twice.
export function consumeRunEvents(
  feed: EventFeed,
  incoming: RunEvent[],
): EventFeed {
  const unique = new Map<string, RunEvent>();
  for (const event of incoming) {
    const id = decimalID(event.id);
    if (compareEventIDs(id, feed.cursor) > 0) unique.set(id, event);
  }
  const fresh = [...unique.values()].sort((a, b) =>
    compareEventIDs(a.id, b.id),
  );
  if (!fresh.length) return feed;
  let { output, attempt, truncated } = feed;
  for (const event of fresh) {
    if (event.type === "RUN_RESUMED" || event.type === "RUN_CLAIMED") {
      output = "";
      truncated = false;
      attempt =
        event.type === "RUN_CLAIMED" && Number.isSafeInteger(event.data.attempt)
          ? Number(event.data.attempt)
          : 0;
    }
    // Replayed events may arrive after a newer attempt was claimed. Text must
    // carry the matching attempt; legacy untagged text stays in event history
    // and is only displayed through the authoritative Run.output checkpoint.
    if (
      event.type === "TEXT_DELTA" &&
      typeof event.data.text === "string" &&
      Number.isSafeInteger(event.data.attempt) &&
      event.data.attempt === attempt
    ) {
      output += event.data.text;
      if (output.length > OUTPUT_LIMIT) {
        output = output.slice(-OUTPUT_LIMIT);
        truncated = true;
      }
    }
  }
  return {
    cursor: fresh[fresh.length - 1].id,
    items: [...feed.items, ...fresh].slice(-EVENT_HISTORY_LIMIT),
    output,
    attempt,
    truncated,
    count: feed.count + fresh.length,
  };
}

export function displayedOutput(run: AgentRun, feed: EventFeed) {
  if (run.state === "RUNNING") {
    return feed.attempt === run.attempt
      ? { text: feed.output, truncated: feed.truncated }
      : { text: "", truncated: false };
  }
  const text = run.output ?? "";
  return {
    text: text.slice(-OUTPUT_LIMIT),
    truncated: text.length > OUTPUT_LIMIT,
  };
}

export function validatePrompt(prompt: string): string {
  const normalized = prompt.trim();
  const length = [...normalized].length;
  if (length < 1 || length > 8000) {
    throw new Error("Describe your task in 1–8,000 characters.");
  }
  return normalized;
}
