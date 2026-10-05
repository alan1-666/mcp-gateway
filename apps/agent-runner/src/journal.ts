import { createHash, randomUUID } from "node:crypto";
import { closeSync, existsSync, fsyncSync, mkdirSync, openSync, readFileSync, renameSync, rmSync, statSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import type { JsonObject } from "./client.js";

export type Intent = { fingerprint: string; call_id: string; idempotency_key: string; operation_id?: string; dispatched?: boolean };
export class JournalError extends Error {}
type JournalData = { version: 1; scope: string; session_id: string; intents: Intent[] };
const MAX_BYTES = 1_048_576;
const MAX_INTENTS = 500;
function canonical(value: unknown): string {
  if (Array.isArray(value)) return `[${value.map(canonical).join(",")}]`;
  if (value && typeof value === "object") return `{${Object.keys(value).sort().map(key => `${JSON.stringify(key)}:${canonical((value as JsonObject)[key])}`).join(",")}}`;
  return JSON.stringify(value);
}
export function fingerprint(toolId: string, args: JsonObject): string {
  return createHash("sha256").update(canonical({ tool_id: toolId, arguments: args })).digest("hex");
}

/** One process owns a session journal. A crash leaves a lock for explicit operator recovery. */
export class IntentJournal {
  private readonly path: string;
  private readonly lock: string;
  private data: JournalData;
  private closed = false;
  private failed = false;
  constructor(private readonly directory: string, scope: string, sessionId: string) {
    if (!/^[a-zA-Z0-9_-]{1,128}$/.test(sessionId)) throw new JournalError("INVALID_SESSION_ID");
    mkdirSync(directory, { recursive: true, mode: 0o700 });
    this.path = join(directory, `${sessionId}.json`);
    this.lock = `${this.path}.lock`;
    try {
      const fd = openSync(this.lock, "wx", 0o600);
      try { writeFileSync(fd, JSON.stringify({ pid: process.pid })); fsyncSync(fd); } finally { closeSync(fd); }
    } catch { throw new JournalError("SESSION_ALREADY_LOCKED: stop the other runner; after a crash inspect the journal and remove only this session's .json.lock file"); }
    try {
      if (existsSync(this.path)) {
        if (statSync(this.path).size > MAX_BYTES) throw new Error("JOURNAL_TOO_LARGE");
        this.data = JSON.parse(readFileSync(this.path, "utf8")) as JournalData;
        if (this.data.version !== 1 || this.data.scope !== scope || this.data.session_id !== sessionId || !Array.isArray(this.data.intents) || this.data.intents.length > MAX_INTENTS) throw new Error("JOURNAL_SCOPE_OR_FORMAT_MISMATCH");
        for (const intent of this.data.intents) {
          if (!intent || typeof intent.call_id !== "string" || typeof intent.fingerprint !== "string" || typeof intent.idempotency_key !== "string" || (intent.operation_id !== undefined && typeof intent.operation_id !== "string") || (intent.dispatched !== undefined && typeof intent.dispatched !== "boolean")) throw new Error("INVALID_JOURNAL");
        }
      } else {
        this.data = { version: 1, scope, session_id: sessionId, intents: [] };
        this.persist();
      }
    } catch (error) { this.close(); throw error; }
  }
  reserve(callId: string, toolId: string, args: JsonObject, deduplicateArguments = true): Intent {
    this.assertWritable();
    if (!callId || callId.length > 256) throw new JournalError("INVALID_TOOL_CALL_ID");
    const hash = fingerprint(toolId, args);
    const sameCall = this.data.intents.find(intent => intent.call_id === callId);
    if (sameCall && sameCall.fingerprint !== hash) throw new Error("TOOL_CALL_ARGUMENTS_CHANGED");
    const existing = sameCall ?? (deduplicateArguments ? this.data.intents.find(intent => intent.fingerprint === hash) : undefined);
    if (existing) return { ...existing };
    if (this.data.intents.length >= MAX_INTENTS) throw new Error("SESSION_INTENT_LIMIT_REACHED");
    const intent: Intent = { fingerprint: hash, call_id: callId, idempotency_key: randomUUID() };
    this.data.intents.push(intent);
    this.persist(); // Intent exists on disk before any network request can create an operation.
    return { ...intent };
  }
  bind(intent: Intent, operationId: string): void {
    this.assertWritable();
    const current = this.find(intent);
    if (!operationId || (current.operation_id && current.operation_id !== operationId)) throw new Error("OPERATION_ID_CHANGED");
    current.operation_id = operationId;
    this.persist();
  }
  markDispatched(operationId: string): void {
    this.assertWritable();
    const intent = this.data.intents.find(item => item.operation_id === operationId);
    if (!intent) throw new Error("OPERATION_NOT_OWNED_BY_SESSION");
    intent.dispatched = true;
    this.persist();
  }
  lookup(operationId: string): Intent | undefined {
    this.assertWritable();
    const intent = this.data.intents.find(item => item.operation_id === operationId);
    return intent ? { ...intent } : undefined;
  }
  list(): Intent[] { this.assertWritable(); return this.data.intents.map(intent => ({ ...intent })); }
  close(): void { if (this.closed) return; this.closed = true; rmSync(this.lock, { force: true }); }
  private assertWritable(): void {
    if (this.closed || this.failed) throw new JournalError("JOURNAL_UNAVAILABLE: stop and inspect the recorded operations before resuming");
  }
  private find(intent: Intent): Intent {
    const item = this.data.intents.find(value => value.idempotency_key === intent.idempotency_key);
    if (!item) throw new Error("INTENT_NOT_FOUND");
    return item;
  }
  private persist(): void {
    const temp = `${this.path}.${randomUUID()}.tmp`;
    try {
      const content = JSON.stringify(this.data);
      if (Buffer.byteLength(content) > MAX_BYTES) throw new JournalError("JOURNAL_TOO_LARGE");
      const fd = openSync(temp, "wx", 0o600);
      try { writeFileSync(fd, content); fsyncSync(fd); } finally { closeSync(fd); }
      renameSync(temp, this.path);
      const dir = openSync(this.directory, "r");
      try { fsyncSync(dir); } finally { closeSync(dir); }
    } catch {
      this.failed = true;
      rmSync(temp, { force: true });
      throw new JournalError("JOURNAL_WRITE_FAILED: no further requests will be sent; inspect local storage before resuming");
    }
  }
}
