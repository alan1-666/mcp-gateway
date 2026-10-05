import assert from "node:assert/strict";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { createServer } from "node:http";
import { hostname, tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import { CloudWorker } from "../src/worker.js";
import { WorkerClient, WorkerError, LeaseLostError, type CloudRun, type Finish, type Lease, type PendingEvent, type WorkerAPI, type WorkerStatus } from "../src/worker-client.js";
import { acquireWorkerState, atomicJSON, RunStore } from "../src/worker-state.js";
import type { GatewayAPI, Operation } from "../src/client.js";
import { createGatewayTools } from "../src/tools.js";
import { IntentJournal } from "../src/journal.js";

const run = (attempt = 1): CloudRun => ({ id: "run-test", workspace_id: "team", actor_id: "user-test", prompt: "private business task", state: "RUNNING", attempt });
const lease = (attempt = 1): Lease => ({ run: run(attempt), lease_token: "lease-private", lease_expires_at: new Date(Date.now() + 45_000).toISOString() });
const ready = async () => ({ ready: true, provider: "test", modelId: "fixture" });
function fixture() { const directory = mkdtempSync(join(tmpdir(), "gateway-worker-")); return { directory, close: () => rmSync(directory, { recursive: true, force: true }) }; }

class FakeAPI implements WorkerAPI {
  leases = [lease()]; statuses: WorkerStatus[] = []; events: PendingEvent[] = []; finishes: Finish[] = [];
  heartbeatFailure?: Error; droppedEventReply = false; eventAttempts: string[] = []; executed = 0;
  operationValue: Operation = { id: "op-test", tool_id: "tool-test", risk: "write", state: "WAITING_APPROVAL" };
  async status(value: WorkerStatus) { this.statuses.push(value); }
  async claim() { return this.leases.shift() ?? null; }
  async heartbeat() { if (this.heartbeatFailure) throw this.heartbeatFailure; return new Date(Date.now() + 45_000).toISOString(); }
  async event(_lease: Lease, event: PendingEvent) {
    this.eventAttempts.push(event.event_key);
    if (!this.events.some(item => item.event_key === event.event_key)) this.events.push(structuredClone(event));
    if (this.droppedEventReply) { this.droppedEventReply = false; throw new Error("private upstream request data"); }
  }
  async finish(_lease: Lease, value: Finish) { this.finishes.push(value); }
  gateway(_lease: Lease, signal: AbortSignal): GatewayAPI {
    return {
      search: async () => ({ items: [], total: 0 }), tool: async () => ({ id: "tool-test", name: "retry", description: "Retry", risk: "write", input_schema: {}, enabled: true, status: "published", version: 1 }),
      prepare: async () => { signal.throwIfAborted(); return { ...this.operationValue }; },
      operation: async () => ({ ...this.operationValue }),
      execute: async () => { signal.throwIfAborted(); this.executed++; this.operationValue.state = "SUCCEEDED"; return { ...this.operationValue }; },
    };
  }
}

test("missing model configuration claims into WAITING_CREDENTIALS without executing or leaking errors", async () => {
  const local = fixture(); const api = new FakeAPI(); let executed = false;
  try {
    const worker = new CloudWorker({ api, stateDir: local.directory, workerId: "worker-test", probeModel: async () => { throw new Error("secret credential material"); }, execute: async () => { executed = true; return { state: "SUCCEEDED", output: "" }; } });
    assert.equal(await worker.runOnce(), true);
    assert.equal(executed, false); assert.equal(api.statuses[0].model_ready, false);
    assert.equal(api.finishes[0].state, "WAITING_CREDENTIALS");
    assert(!JSON.stringify(api.statuses).includes("secret"));
  } finally { local.close(); }
});

test("events remain ordered and stable after a lost acknowledgement; text deltas are batched", async () => {
  const local = fixture(); const api = new FakeAPI(); api.droppedEventReply = true;
  try {
    const worker = new CloudWorker({ api, stateDir: local.directory, workerId: "worker-test", probeModel: ready, execute: async ctx => {
      ctx.emit("MODEL_STARTED", { provider: "fixture" });
      for (let i = 0; i < 100; i++) ctx.text("a");
      ctx.emit("TOOL_STARTED", { tool: "fixture" }); ctx.emit("TOOL_COMPLETED", { tool: "fixture" });
      return { state: "SUCCEEDED", output: ctx.store.output };
    } });
    await worker.runOnce();
    assert.deepEqual(api.events.map(event => event.type), ["MODEL_STARTED", "TEXT_DELTA", "TOOL_STARTED", "TOOL_COMPLETED"]);
    assert.equal(api.eventAttempts[0], api.eventAttempts[1]);
    assert.equal(api.finishes[0].state, "SUCCEEDED"); assert.equal(api.finishes[0].output, "a".repeat(100));
    assert(api.events.every(event => event.data.attempt === 1));
  } finally { local.close(); }
});

for (const reason of [new LeaseLostError(), new Error("Bearer sensitive-data")]) {
  test(`heartbeat ${reason instanceof LeaseLostError ? "cancellation" : "unavailability"} aborts engine and never submits stale finish`, async () => {
    const local = fixture(); const api = new FakeAPI(); api.heartbeatFailure = reason; let aborted = false; const logs: unknown[] = [];
    try {
      const worker = new CloudWorker({ api, stateDir: local.directory, workerId: "worker-test", probeModel: ready, heartbeatMs: 5, log: event => logs.push(event), execute: async ctx => {
        await new Promise<void>(resolve => ctx.signal.addEventListener("abort", () => { aborted = true; resolve(); }, { once: true }));
        return { state: "SUCCEEDED", output: "must not finish" };
      } });
      await worker.runOnce(); assert.equal(aborted, true); assert.equal(api.finishes.length, 0);
      assert(!JSON.stringify(logs).includes("sensitive-data"));
    } finally { local.close(); }
  });
}

test("approval resume uses the same operation and persisted dispatch protection", async () => {
  const local = fixture(); const api = new FakeAPI();
  try {
    const worker = new CloudWorker({ api, stateDir: local.directory, workerId: "worker-test", probeModel: ready, execute: async ctx => {
      const journal = new IntentJournal(join(ctx.store.directory, "intents"), ctx.run.id, "session-test");
      let pause: { reason: string; operation_id?: string } | undefined;
      try {
        const tools = createGatewayTools(ctx.gateway, journal, value => { pause = value; });
        const prepare = tools.find(tool => tool.name === "prepare_action")!;
        const prepared = await prepare.execute(`call-${ctx.run.attempt}`, { tool_id: "tool-test", arguments: { job: "1" } }, ctx.signal, undefined, {} as never);
        const text = prepared.content[0]; assert.equal(text.type, "text");
        if (pause) return { state: "WAITING_APPROVAL", output: "", waiting_operation_id: pause.operation_id };
        const invoke = tools.find(tool => tool.name === "invoke_tool")!;
        await invoke.execute("execute", { operation_id: "op-test" }, ctx.signal, undefined, {} as never);
        return { state: "SUCCEEDED", output: "" };
      } finally { journal.close(); }
    } });
    await worker.runOnce(); assert.equal(api.finishes[0].state, "WAITING_APPROVAL"); assert.equal(api.executed, 0);
    api.operationValue.state = "READY"; api.leases.push(lease(2)); await worker.runOnce();
    assert.equal(api.finishes[1].state, "SUCCEEDED"); assert.equal(api.executed, 1);
    api.leases.push(lease(3)); await worker.runOnce(); assert.equal(api.executed, 1);
  } finally { local.close(); }
});

test("missing prior state or a started Pi session fails closed before executing", async () => {
  const local = fixture(); const api = new FakeAPI(); let calls = 0;
  try {
    const worker = new CloudWorker({ api, stateDir: local.directory, workerId: "worker-test", execute: async () => { calls++; return { state: "SUCCEEDED", output: "" }; } });
    await worker.executeLease(lease(2), await ready());
    assert.equal(api.finishes[0].error_code, "PRIOR_RUN_STATE_MISSING");
    const store = new RunStore(local.directory, "worker-test", run()); store.setSession("missing.jsonl", "session-test"); store.markStarted();
    await worker.executeLease(lease(2), await ready());
    assert.equal(api.finishes[1].error_code, "PRIOR_SESSION_MISSING"); assert.equal(calls, 0);
  } finally { local.close(); }
});

test("a new attempt archives unsent old text instead of replaying it into the new output", () => {
  const local = fixture();
  try {
    const first = new RunStore(local.directory, "worker-test", run()); first.enqueue("MODEL_STARTED", {}); first.appendText("old private output");
    const oldKey = first.nextEvent()!.event_key;
    const resumed = new RunStore(local.directory, "worker-test", run(2));
    assert.equal(resumed.output, ""); assert.equal(resumed.nextEvent()!.type, "RECOVERY_CHECKPOINT");
    assert.equal(resumed.nextEvent()!.data.attempt, 2);
    const archived = JSON.parse(readFileSync(join(first.directory, "attempt-1-unconfirmed.json"), "utf8"));
    assert.equal(archived.events[0].event_key, oldKey); assert.equal(archived.text_buffer, "old private output");
    assert(!JSON.stringify(resumed.nextEvent()).includes("old private output"));
  } finally { local.close(); }
});

test("event exhaustion still records NEEDS_REVIEW without an invalid flush blocking finish", async () => {
  const local = fixture(); const api = new FakeAPI();
  try {
    const store = new RunStore(local.directory, "worker-test", run());
    const path = join(store.directory, "state.json"); const data = JSON.parse(readFileSync(path, "utf8")); data.event_count = 1000; atomicJSON(path, data);
    const worker = new CloudWorker({ api, stateDir: local.directory, workerId: "worker-test", probeModel: ready, execute: async ctx => { ctx.text("pending final text"); return { state: "SUCCEEDED", output: ctx.store.output }; } });
    await worker.runOnce();
    assert.equal(api.finishes[0].state, "NEEDS_REVIEW"); assert.equal(api.finishes[0].error_code, "EVENT_BUDGET_EXHAUSTED");
    assert.equal(api.finishes[0].output, "pending final text");
  } finally { local.close(); }
});

test("event and output bounds reject oversized payloads without leaking arbitrary exception bodies", async () => {
  const local = fixture(); const api = new FakeAPI();
  try {
    const store = new RunStore(local.directory, "worker-test", run());
    assert.throws(() => store.enqueue("TOOL_COMPLETED", { value: "x".repeat(16384) }), /EVENT_BODY_TOO_LARGE/);
    assert.throws(() => store.appendText("x".repeat(65537)), /OUTPUT_LIMIT_EXCEEDED/);
    const worker = new CloudWorker({ api, stateDir: local.directory, workerId: "worker-test", probeModel: ready, execute: async () => { throw new Error("secret-access-token"); } });
    await worker.runOnce(); assert.equal(api.finishes[0].error_code, "WORKER_FAILED"); assert(!JSON.stringify(api.finishes).includes("secret-access-token"));
  } finally { local.close(); }
});

test("worker identity is persistent, live locks fail closed, and a verified dead owner can be recovered", () => {
  const local = fixture();
  try {
    const owner = acquireWorkerState(local.directory, "worker-test");
    assert.throws(() => acquireWorkerState(local.directory, "worker-test"), /STATE_IN_USE_OR_OWNER_UNVERIFIABLE/); owner.close();
    assert.throws(() => acquireWorkerState(local.directory, "changed-worker"), /WORKER_ID_CHANGED/);
    atomicJSON(join(local.directory, "worker.lock"), { pid: 2147483647, host: hostname(), instance: "dead" });
    const restored = acquireWorkerState(local.directory); assert.equal(restored.workerId, "worker-test"); restored.close();
  } finally { local.close(); }
});

test("worker transport binds lease, distinguishes conflict/budget/expiry and allows a slow execution", async () => {
  const secret = "test-daemon-secret-longer-than-32-characters"; let mode = "success"; let lost = false;
  const server = createServer((req, res) => {
    assert.equal(req.headers.authorization, `Bearer ${secret}`); assert.equal(req.headers["x-run-lease"], "lease-private");
    const send = () => {
      res.setHeader("content-type", "application/json");
      if (mode !== "success") { res.statusCode = 409; res.end(JSON.stringify({ error: { code: mode, message: `private ${secret}` } })); }
      else res.end(JSON.stringify({ id: "op-test", tool_id: "tool-test", state: "SUCCEEDED", risk: "read" }));
    };
    if (req.url?.endsWith("/execute")) setTimeout(send, 50); else send();
  });
  await new Promise<void>(resolve => server.listen(0, "127.0.0.1", resolve));
  const address = server.address(); assert(address && typeof address !== "string");
  try {
    const client = new WorkerClient(`http://127.0.0.1:${address.port}`, secret, [], 20);
    const gateway = client.gateway(lease(), new AbortController().signal, () => { lost = true; });
    assert.equal((await gateway.execute("op-test")).state, "SUCCEEDED");
    mode = "conflict"; await assert.rejects(gateway.prepare("tool-test", {}, "key"), error => error instanceof WorkerError && error.code === "RUNNER_CONFLICT"); assert.equal(lost, false);
    mode = "budget_exhausted"; await assert.rejects(gateway.prepare("tool-test", {}, "key"), /RUN_TOOL_BUDGET_EXHAUSTED/); assert.equal(lost, false);
    mode = "lease_expired"; await assert.rejects(gateway.operation("op-test"), LeaseLostError); assert.equal(lost, true);
    assert.throws(() => new WorkerClient("http://untrusted.example", secret), /HTTPS_OR_EXPLICIT/);
    assert.equal(new WorkerClient("http://api:8090", secret, ["api"]).origin, "http://api:8090");
  } finally { await new Promise<void>(resolve => { server.close(() => resolve()); server.closeAllConnections(); }); }
});
