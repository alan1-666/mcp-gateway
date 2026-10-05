import { executeCloudPi, probeCloudModel, type CloudExecutionContext, type ModelReadiness } from "./cloud-runtime.js";
import { GatewayError } from "./client.js";
import { RunStore } from "./worker-state.js";
import { LeaseLostError, WorkerError, type Finish, type Lease, type WorkerAPI, type WorkerStatus } from "./worker-client.js";

export type WorkerOptions = {
  api: WorkerAPI; stateDir: string; workerId: string;
  probeModel?: () => Promise<ModelReadiness>; execute?: (context: CloudExecutionContext) => Promise<Finish>;
  heartbeatMs?: number; pollMs?: number; runTimeoutMs?: number;
  log?: (event: { event: string; run_id?: string; error_code?: string }) => void;
  recoverIntentLock?: (path: string) => void;
};
export class CloudWorker {
  constructor(private readonly options: WorkerOptions) {}
  private async readiness(): Promise<ModelReadiness> { try { return await (this.options.probeModel ?? (() => probeCloudModel(this.options.stateDir)))(); } catch { return { ready: false, errorCode: "MODEL_NOT_CONFIGURED" }; } }
  private status(readiness: ModelReadiness): WorkerStatus { return { worker_id: this.options.workerId, model_ready: readiness.ready, provider: readiness.provider, model_id: readiness.modelId, error_code: readiness.errorCode }; }
  async check(signal?: AbortSignal): Promise<WorkerStatus> { const status = this.status(await this.readiness()); await this.options.api.status(status, signal); return status; }
  async runOnce(signal?: AbortSignal): Promise<boolean> {
    signal?.throwIfAborted();
    const readiness = await this.readiness();
    await this.options.api.status(this.status(readiness), signal);
    const lease = await this.options.api.claim(this.options.workerId, signal);
    if (!lease) return false;
    await this.executeLease(lease, readiness, signal); return true;
  }
  async run(signal: AbortSignal): Promise<void> {
    while (!signal.aborted) {
      try { await this.runOnce(signal); }
      catch (error) { if (!signal.aborted) this.options.log?.({ event: "worker_poll_failed", error_code: safeWorkerCode(error) }); }
      if (!signal.aborted) await sleep(this.options.pollMs ?? 5_000, signal).catch(() => {});
    }
  }
  async executeLease(lease: Lease, readiness: ModelReadiness, shutdown?: AbortSignal): Promise<void> {
    const api = this.options.api;
    const engine = new AbortController(); const control = new AbortController();
    let lost = false; let failure: string | undefined; let store: RunStore | undefined;
    let eventFailure: string | undefined;
    let expiry: ReturnType<typeof setTimeout> | undefined;
    let heartbeatPending: Promise<void> | undefined; let draining: Promise<void> | undefined;
    const loseLease = () => { if (lost) return; lost = true; engine.abort(); control.abort(); this.options.log?.({ event: "lease_lost", run_id: lease.run.id }); };
    const stopEngine = (code: string) => { failure ??= code; engine.abort(); };
    const failEvents = (error: unknown) => {
      if (error instanceof LeaseLostError || control.signal.aborted) { loseLease(); return; }
      eventFailure = safeWorkerCode(error); stopEngine(eventFailure);
    };
    const scheduleExpiry = (at: string) => {
      if (expiry) clearTimeout(expiry);
      const remaining = Date.parse(at) - Date.now() - 1_000;
      if (!Number.isFinite(remaining) || remaining <= 0) { loseLease(); return; }
      expiry = setTimeout(loseLease, remaining);
    };
    scheduleExpiry(lease.lease_expires_at);
    const shutdownRun = () => stopEngine("WORKER_SHUTDOWN");
    shutdown?.addEventListener("abort", shutdownRun, { once: true });
    if (shutdown?.aborted) shutdownRun();
    const timeout = setTimeout(() => stopEngine("RUN_DEADLINE_EXCEEDED"), this.options.runTimeoutMs ?? 300_000);
    const heartbeat = setInterval(() => {
      if (lost || heartbeatPending) return;
      heartbeatPending = (async () => { lease.lease_expires_at = await api.heartbeat(lease, control.signal); scheduleExpiry(lease.lease_expires_at); await api.status(this.status(readiness), control.signal); })().catch(loseLease).finally(() => { heartbeatPending = undefined; });
    }, this.options.heartbeatMs ?? 10_000);
    const flush = (): Promise<void> => {
      if (draining) return draining.then(() => store?.nextEvent() ? flush() : undefined);
      draining = (async () => {
        for (;;) {
          const event = store?.nextEvent(); if (!event) return;
          let sent = false;
          for (let attempt = 0; attempt < 3; attempt++) {
            try { await api.event(lease, event, control.signal); sent = true; break; }
            catch (error) {
              if (error instanceof LeaseLostError || control.signal.aborted) throw error;
              if (attempt === 2) throw error;
              await sleep(100 * (attempt + 1), control.signal);
            }
          }
          if (!sent) throw new WorkerError("EVENT_DELIVERY_FAILED");
          store!.acknowledge(event.event_key);
        }
      })().finally(() => { draining = undefined; });
      return draining;
    };
    const eventsTimer = setInterval(() => {
      if (!store || lost || failure) return;
      try { store.flushText(); void flush().catch(failEvents); } catch (error) { failEvents(error); }
    }, 200);
    let outcome: Finish | undefined;
    try {
      if (lost) throw new LeaseLostError();
      store = new RunStore(this.options.stateDir, this.options.workerId, lease.run);
      await flush();
      if (!readiness.ready) outcome = { state: "WAITING_CREDENTIALS", output: "", error_code: readiness.errorCode ?? "MODEL_NOT_CONFIGURED" };
      else {
        const gateway = api.gateway(lease, engine.signal, loseLease);
        outcome = await (this.options.execute ?? executeCloudPi)({
          run: lease.run, store, gateway, signal: engine.signal,
          emit: (type, data) => { if (engine.signal.aborted) throw new WorkerError("RUN_INTERRUPTED"); store!.enqueue(type, data); void flush().catch(failEvents); },
          text: text => { if (engine.signal.aborted) throw new WorkerError("RUN_INTERRUPTED"); store!.appendText(text); },
          flush,
          recoverIntentLock: this.options.recoverIntentLock ?? (() => {}),
        });
      }
    } catch (error) {
      if (error instanceof LeaseLostError) loseLease();
      failure ??= safeWorkerCode(error);
      outcome = { state: "NEEDS_REVIEW", output: store?.output ?? "", error_code: failure };
    } finally {
      clearInterval(eventsTimer); clearInterval(heartbeat); clearTimeout(timeout);
      shutdown?.removeEventListener("abort", shutdownRun);
    }
    try {
      if (heartbeatPending) await heartbeatPending;
      if (lost) return;
      if (failure) outcome = { state: "NEEDS_REVIEW", output: store?.output ?? "", error_code: failure };
      try { if (!eventFailure) { store?.flushText(); await flush(); } }
      catch (error) { failEvents(error); }
      if (eventFailure) outcome = { state: "NEEDS_REVIEW", output: store?.output ?? "", error_code: eventFailure };
      if (!lost && outcome) {
        control.signal.throwIfAborted();
        // Finish is never retried after an ambiguous response; expiry/review is authoritative.
        await api.finish(lease, { ...outcome, output: store?.output ?? outcome.output }, control.signal);
        this.options.log?.({ event: "run_finished", run_id: lease.run.id });
      }
    } catch (error) { this.options.log?.({ event: "run_finish_unconfirmed", run_id: lease.run.id, error_code: safeWorkerCode(error) }); }
    finally { if (expiry) clearTimeout(expiry); control.abort(); }
  }
}
export function safeWorkerCode(error: unknown): string {
  if (error instanceof WorkerError || error instanceof GatewayError) return /^[A-Z0-9_]{1,64}$/.test(error.message) ? error.message : "WORKER_FAILED";
  return "WORKER_FAILED";
}
function sleep(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    if (signal.aborted) { reject(new WorkerError("RUN_INTERRUPTED")); return; }
    const abort = () => { clearTimeout(timer); reject(new WorkerError("RUN_INTERRUPTED")); };
    const timer = setTimeout(() => { signal.removeEventListener("abort", abort); resolve(); }, ms);
    signal.addEventListener("abort", abort, { once: true });
  });
}
