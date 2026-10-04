import type { Identity } from "./types";

type DraftStorage = Pick<Storage, "getItem" | "setItem" | "removeItem">;
export interface TaskDraft {
  version: 1;
  prompt: string;
  idempotency_key: string;
  submitted: boolean;
}

function key(identity: Identity) {
  return `mcp-gateway:task-draft:v1:${encodeURIComponent(identity.workspace_id)}:${encodeURIComponent(identity.id)}`;
}

export function readTaskDraft(
  identity: Identity,
  storage?: DraftStorage,
): TaskDraft | null {
  try {
    const target = storage ?? window.sessionStorage;
    const value: unknown = JSON.parse(target.getItem(key(identity)) ?? "null");
    if (
      value &&
      typeof value === "object" &&
      "version" in value &&
      value.version === 1 &&
      "prompt" in value &&
      typeof value.prompt === "string" &&
      value.prompt.length <= 16000 &&
      "idempotency_key" in value &&
      typeof value.idempotency_key === "string" &&
      /^[a-f0-9-]{36}$/i.test(value.idempotency_key) &&
      "submitted" in value &&
      typeof value.submitted === "boolean"
    ) {
      return {
        version: 1,
        prompt: value.prompt,
        idempotency_key: value.idempotency_key,
        submitted: value.submitted,
      };
    }
    target.removeItem(key(identity));
  } catch {
    /* A blocked storage area must not prevent opening the workspace. */
  }
  return null;
}

export function saveTaskDraft(
  identity: Identity,
  draft: TaskDraft,
  storage?: DraftStorage,
): boolean {
  try {
    const target = storage ?? window.sessionStorage;
    if (!draft.prompt && !draft.submitted) target.removeItem(key(identity));
    else target.setItem(key(identity), JSON.stringify(draft));
    return true;
  } catch {
    return false;
  }
}

export function clearTaskDraft(
  identity: Identity,
  storage?: DraftStorage,
): void {
  try {
    (storage ?? window.sessionStorage).removeItem(key(identity));
  } catch {
    /* Storage can be unavailable. */
  }
}
