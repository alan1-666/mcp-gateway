import { createHash, randomUUID } from "node:crypto";
import { closeSync, existsSync, fsyncSync, mkdirSync, openSync, readFileSync, renameSync, rmSync, statSync, writeFileSync } from "node:fs";
import { hostname } from "node:os";
import { dirname, join } from "node:path";
import { WorkerError, validID, type CloudRun, type PendingEvent } from "./worker-client.js";
import type { JsonObject } from "./client.js";

export const MAX_OUTPUT_BYTES = 65_536;
export const MAX_EVENT_BYTES = 16_384;
type RunData = {
  version: 1; run_id: string; worker_id: string; workspace_id: string; actor_id: string; prompt_hash: string;
  attempt: number; session_file?: string; session_id?: string; started: boolean; output: string; text_buffer: string;
  events: PendingEvent[]; event_count: number; event_bytes: number; tool_calls: number;
};
export function atomicJSON(path: string, value: unknown): void {
  const temp = `${path}.${randomUUID()}.tmp`;
  try {
    const fd = openSync(temp, "wx", 0o600);
    try { writeFileSync(fd, JSON.stringify(value)); fsyncSync(fd); } finally { closeSync(fd); }
    renameSync(temp, path);
    syncFile(dirname(path));
  } catch { rmSync(temp, { force: true }); throw new WorkerError("LOCAL_STATE_WRITE_FAILED"); }
}
export function syncFile(path: string): void { const fd = openSync(path, "r"); try { fsyncSync(fd); } finally { closeSync(fd); } }
function readJSON<T>(path: string, maxBytes: number): T {
  if (statSync(path).size > maxBytes) throw new WorkerError("LOCAL_STATE_TOO_LARGE");
  try { return JSON.parse(readFileSync(path, "utf8")) as T; } catch { throw new WorkerError("LOCAL_STATE_INVALID"); }
}

export class RunStore {
  readonly directory: string;
  private readonly path: string;
  private data: RunData;
  private failed = false;
  constructor(root: string, workerId: string, run: CloudRun) {
    if (!validID(run.id) || !validID(workerId)) throw new WorkerError("INVALID_RUN_IDENTITY");
    this.directory = join(root, "runs", run.id); this.path = join(this.directory, "state.json");
    const hash = createHash("sha256").update(run.prompt).digest("hex");
    if (existsSync(this.path)) {
      this.data = readJSON<RunData>(this.path, 2_097_152);
      if (this.data.version !== 1 || this.data.run_id !== run.id || this.data.worker_id !== workerId || this.data.workspace_id !== run.workspace_id || this.data.actor_id !== run.actor_id || this.data.prompt_hash !== hash || !Array.isArray(this.data.events) || this.data.attempt > run.attempt || typeof this.data.output !== "string" || typeof this.data.text_buffer !== "string") throw new WorkerError("LOCAL_STATE_IDENTITY_MISMATCH");
      if (this.data.started && (!this.data.session_file || !existsSync(join(this.directory, "sessions", this.data.session_file)))) throw new WorkerError("PRIOR_SESSION_MISSING");
      if (this.data.attempt !== run.attempt) {
        const previousAttempt = this.data.attempt;
        const archivedEvents = this.data.events.length;
        const unconfirmedTextBytes = Buffer.byteLength(this.data.text_buffer);
        if (archivedEvents || unconfirmedTextBytes) atomicJSON(join(this.directory, `attempt-${previousAttempt}-unconfirmed.json`), { version: 1, attempt: previousAttempt, events: this.data.events, text_buffer: this.data.text_buffer, output: this.data.output });
        this.data.events = []; this.data.text_buffer = "";
        this.data.event_count = 0; this.data.event_bytes = 0;
        this.data.attempt = run.attempt; this.data.output = ""; this.save();
        this.enqueue("RECOVERY_CHECKPOINT", { previous_attempt: previousAttempt, archived_events: archivedEvents, unconfirmed_text_bytes: unconfirmedTextBytes });
      } else this.flushText();
    } else {
      if (run.attempt > 1) throw new WorkerError("PRIOR_RUN_STATE_MISSING");
      mkdirSync(this.directory, { recursive: true, mode: 0o700 });
      this.data = { version: 1, run_id: run.id, worker_id: workerId, workspace_id: run.workspace_id, actor_id: run.actor_id, prompt_hash: hash, attempt: run.attempt, started: false, output: "", text_buffer: "", events: [], event_count: 0, event_bytes: 0, tool_calls: 0 };
      this.save();
    }
  }
  get output(): string { return this.data.output; }
  get session(): { file?: string; id?: string; started: boolean } { return { file: this.data.session_file, id: this.data.session_id, started: this.data.started }; }
  setSession(file: string, id: string): void {
    if (file.includes("/") || file.includes("\\") || !validID(id)) throw new WorkerError("INVALID_LOCAL_SESSION");
    if (this.data.session_id && this.data.session_id !== id) throw new WorkerError("SESSION_ID_CHANGED");
    this.data.session_file = file; this.data.session_id = id; this.save();
  }
  markStarted(): void { this.data.started = true; this.save(); }
  claimToolCall(): void { if (this.data.tool_calls >= 40) throw new WorkerError("TOOL_BUDGET_EXHAUSTED"); this.data.tool_calls++; this.save(); }
  appendText(text: string): void {
    this.ensure();
    if (Buffer.byteLength(this.data.output) + Buffer.byteLength(text) > MAX_OUTPUT_BYTES) throw new WorkerError("OUTPUT_LIMIT_EXCEEDED");
    this.data.output += text; this.data.text_buffer += text; this.save();
    if (Buffer.byteLength(this.data.text_buffer) >= 512) this.flushText();
  }
  flushText(): void {
    while (this.data.text_buffer) {
      let chunk = "";
      for (const char of this.data.text_buffer) { if (Buffer.byteLength(chunk) + Buffer.byteLength(char) > 8_000) break; chunk += char; }
      this.enqueue("TEXT_DELTA", { text: chunk }, false, chunk.length);
    }
  }
  enqueue(type: string, data: JsonObject, flushText = true, consumedText = 0): void {
    this.ensure();
    if (flushText) this.flushText();
    if (!/^[A-Z_]{1,64}$/.test(type) || type.startsWith("RUN_")) throw new WorkerError("INVALID_WORKER_EVENT_TYPE");
    const body = { ...data, attempt: this.data.attempt };
    const bytes = Buffer.byteLength(JSON.stringify(body));
    if (bytes > MAX_EVENT_BYTES) throw new WorkerError("EVENT_BODY_TOO_LARGE");
    if (this.data.event_count >= 1000 || this.data.event_bytes + bytes > 1_048_576) throw new WorkerError("EVENT_BUDGET_EXHAUSTED");
    this.data.events.push({ event_key: randomUUID(), type, data: body });
    this.data.event_count++; this.data.event_bytes += bytes;
    if (consumedText) this.data.text_buffer = this.data.text_buffer.slice(consumedText);
    this.save();
  }
  nextEvent(): PendingEvent | undefined { this.ensure(); return this.data.events[0] ? structuredClone(this.data.events[0]) : undefined; }
  acknowledge(key: string): void { this.ensure(); if (this.data.events[0]?.event_key !== key) throw new WorkerError("EVENT_ACK_OUT_OF_ORDER"); this.data.events.shift(); this.save(); }
  private ensure(): void { if (this.failed) throw new WorkerError("LOCAL_STATE_UNAVAILABLE"); }
  private save(): void { this.ensure(); try { atomicJSON(this.path, this.data); } catch (error) { this.failed = true; throw error; } }
}

type Owner = { pid: number; host: string; instance: string; boot?: string; start?: string };
function processIdentity(pid: number): { boot: string; start: string } | undefined {
  try { const stat = readFileSync(`/proc/${pid}/stat`, "utf8"); return { boot: readFileSync("/proc/sys/kernel/random/boot_id", "utf8").trim(), start: stat.slice(stat.lastIndexOf(")") + 2).split(/\s+/)[19] }; } catch { return undefined; }
}

/** Exclusive single-host daemon ownership; unknown/live owners are never removed. */
export function acquireWorkerState(root: string, configuredId?: string): { workerId: string; close(): void; recoverIntentLock(path: string): void } {
  mkdirSync(root, { recursive: true, mode: 0o700 });
  const lock = join(root, "worker.lock");
  const owner: Owner = { pid: process.pid, host: hostname(), instance: randomUUID(), ...processIdentity(process.pid) };
  const create = () => {
    const fd = openSync(lock, "wx", 0o600);
    try { writeFileSync(fd, JSON.stringify(owner)); fsyncSync(fd); } finally { closeSync(fd); }
  };
  try { create(); }
  catch {
    const recovery = join(root, "worker-lock-recovery");
    try { mkdirSync(recovery, { mode: 0o700 }); } catch { throw new WorkerError("WORKER_LOCK_RECOVERY_REQUIRES_OPERATOR_REVIEW"); }
    try {
      const prior = readJSON<Owner>(lock, 2048);
      if (prior.host !== owner.host) throw new WorkerError("WORKER_LOCK_REQUIRES_OPERATOR_REVIEW");
      let live = true;
      try { process.kill(prior.pid, 0); } catch (error) { if ((error as NodeJS.ErrnoException).code === "ESRCH") live = false; }
      const identity = live ? processIdentity(prior.pid) : undefined;
      const reused = identity && prior.boot && prior.start && (identity.boot !== prior.boot || identity.start !== prior.start);
      if (live && !reused) throw new WorkerError("WORKER_STATE_IN_USE_OR_OWNER_UNVERIFIABLE");
      const stale = `${lock}.stale.${owner.instance}`;
      renameSync(lock, stale);
      try { create(); } catch { throw new WorkerError("WORKER_STATE_IN_USE"); }
      finally { rmSync(stale, { force: true }); }
    } finally { rmSync(recovery, { recursive: true, force: true }); }
  }
  const identityPath = join(root, "worker.json");
  try {
    const previous = existsSync(identityPath) ? readJSON<{ worker_id: string }>(identityPath, 2048).worker_id : undefined;
    const workerId = configuredId ?? previous ?? `worker-${randomUUID()}`;
    if (!validID(workerId) || (previous && previous !== workerId)) throw new WorkerError("WORKER_ID_CHANGED");
    if (!previous) atomicJSON(identityPath, { worker_id: workerId });
    let closed = false;
    return {
      workerId,
      close() { if (closed) return; closed = true; const current = readJSON<Owner>(lock, 2048); if (current.instance === owner.instance) rmSync(lock); },
      recoverIntentLock(path) { if (!path.startsWith(`${root}/runs/`) || !path.endsWith(".json.lock")) throw new WorkerError("INVALID_INTENT_LOCK_PATH"); if (existsSync(path)) rmSync(path); },
    };
  } catch (error) { rmSync(lock, { force: true }); throw error; }
}
