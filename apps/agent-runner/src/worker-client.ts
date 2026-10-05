import { GatewayError, toolSearchParams, validateArgumentNumbers, validateToolDiscoveryPage, type GatewayAPI, type JsonObject, type Operation, type Tool } from "./client.js";

export type CloudRun = { id: string; workspace_id: string; actor_id: string; prompt: string; state: string; attempt: number; output?: string; error_code?: string; waiting_operation_id?: string };
export type Lease = { run: CloudRun; lease_token: string; lease_expires_at: string };
export type WorkerStatus = { worker_id: string; model_ready: boolean; provider?: string; model_id?: string; error_code?: string };
export type Finish = { state: "WAITING_APPROVAL" | "WAITING_CREDENTIALS" | "NEEDS_REVIEW" | "SUCCEEDED" | "FAILED"; output: string; error_code?: string; waiting_operation_id?: string };
export type PendingEvent = { event_key: string; type: string; data: JsonObject };
export class WorkerError extends GatewayError { constructor(code: string, ambiguous = false) { super(code, ambiguous); this.name = "WorkerError"; } }
export class LeaseLostError extends WorkerError { constructor() { super("LEASE_LOST"); } }
export interface WorkerAPI {
  status(value: WorkerStatus, signal?: AbortSignal): Promise<void>;
  claim(workerId: string, signal?: AbortSignal): Promise<Lease | null>;
  heartbeat(lease: Lease, signal?: AbortSignal): Promise<string>;
  event(lease: Lease, event: PendingEvent, signal?: AbortSignal): Promise<void>;
  finish(lease: Lease, finish: Finish, signal?: AbortSignal): Promise<void>;
  gateway(lease: Lease, signal: AbortSignal, onLeaseLost?: () => void): GatewayAPI;
}

export class WorkerClient implements WorkerAPI {
  readonly origin: string;
  constructor(url: string, private readonly secret: string, httpAllowedHosts: string[] = [], private readonly timeoutMs = 8_000) {
    let parsed: URL;
    try { parsed = new URL(url); } catch { throw new WorkerError("INVALID_RUNNER_API_URL"); }
    const allowedHTTP = ["127.0.0.1", "localhost", "[::1]", ...httpAllowedHosts].includes(parsed.hostname);
    if (parsed.username || parsed.password || parsed.search || parsed.hash || !["", "/"].includes(parsed.pathname) || (parsed.protocol !== "https:" && !(parsed.protocol === "http:" && allowedHTTP))) throw new WorkerError("RUNNER_API_REQUIRES_HTTPS_OR_EXPLICIT_INTERNAL_HOST");
    if (secret.length < 32 || secret.length > 4096 || /\s/.test(secret)) throw new WorkerError("INVALID_RUNNER_SHARED_SECRET");
    this.origin = parsed.origin;
  }
  async request<T>(path: string, body?: unknown, lease?: Lease, signal?: AbortSignal, timeoutMs = this.timeoutMs): Promise<T> {
    const deadline = AbortSignal.timeout(timeoutMs);
    try {
      signal?.throwIfAborted();
      const response = await fetch(`${this.origin}/internal/runner${path}`, {
        method: body === undefined ? "GET" : "POST", redirect: "error",
        headers: { authorization: `Bearer ${this.secret}`, accept: "application/json", "content-type": "application/json", ...(lease ? { "x-run-lease": lease.lease_token } : {}) },
        body: body === undefined ? undefined : JSON.stringify(body), signal: signal ? AbortSignal.any([deadline, signal]) : deadline,
      });
      if (!response.ok) {
        const code = await errorCode(response);
        if (lease && (code === "lease_expired" || response.status === 401 || response.status === 403)) throw new LeaseLostError();
        if (code === "budget_exhausted") throw new WorkerError("RUN_TOOL_BUDGET_EXHAUSTED");
        if (code === "conflict") throw new WorkerError("RUNNER_CONFLICT");
        throw new GatewayError(`GATEWAY_HTTP_${response.status}`, body !== undefined && response.status >= 500);
      }
      if (!response.headers.get("content-type")?.includes("application/json")) { await response.body?.cancel(); throw new WorkerError("INVALID_RUNNER_RESPONSE", body !== undefined); }
      const reader = response.body?.getReader();
      if (!reader) throw new WorkerError("INVALID_RUNNER_RESPONSE", body !== undefined);
      const chunks: Uint8Array[] = []; let size = 0;
      for (;;) {
        const { done, value } = await reader.read(); if (done) break;
        size += value.byteLength;
        if (size > 262_144) { await reader.cancel(); throw new WorkerError("RUNNER_RESPONSE_TOO_LARGE", body !== undefined); }
        chunks.push(value);
      }
      try { return JSON.parse(Buffer.concat(chunks).toString("utf8")) as T; } catch { throw new WorkerError("INVALID_RUNNER_RESPONSE", body !== undefined); }
    } catch (error) {
      if (error instanceof WorkerError || error instanceof GatewayError) throw error;
      throw new GatewayError(signal?.aborted ? "REQUEST_CANCELLED" : "RUNNER_API_UNREACHABLE", body !== undefined);
    }
  }
  async status(value: WorkerStatus, signal?: AbortSignal) { await this.request("/status", value, undefined, signal); }
  async claim(workerId: string, signal?: AbortSignal): Promise<Lease | null> {
    const response = await this.request<Lease | { run: null }>("/claim", { worker_id: workerId }, undefined, signal);
    if (response.run === null) return null;
    const lease = response as Lease;
    if (!validID(lease.run.id) || !validID(lease.run.workspace_id) || !validID(lease.run.actor_id) || typeof lease.run.prompt !== "string" || [...lease.run.prompt].length > 8000 || !Number.isInteger(lease.run.attempt) || lease.run.attempt < 1 || typeof lease.lease_token !== "string" || !lease.lease_token || !Number.isFinite(Date.parse(lease.lease_expires_at))) throw new WorkerError("INVALID_RUNNER_CLAIM");
    return lease;
  }
  async heartbeat(lease: Lease, signal?: AbortSignal) {
    const response = await this.request<{ lease_expires_at: string }>(`/runs/${encodeURIComponent(lease.run.id)}/heartbeat`, {}, lease, signal);
    if (!Number.isFinite(Date.parse(response.lease_expires_at))) throw new LeaseLostError();
    return response.lease_expires_at;
  }
  async event(lease: Lease, event: PendingEvent, signal?: AbortSignal) { await this.request(`/runs/${encodeURIComponent(lease.run.id)}/events`, event, lease, signal); }
  async finish(lease: Lease, finish: Finish, signal?: AbortSignal) { await this.request(`/runs/${encodeURIComponent(lease.run.id)}/finish`, finish, lease, signal); }
  gateway(lease: Lease, signal: AbortSignal, onLeaseLost?: () => void): GatewayAPI {
    const prefix = `/runs/${encodeURIComponent(lease.run.id)}/gateway`;
    const request = async <T>(path: string, body?: unknown, toolSignal?: AbortSignal) => {
      try { return await this.request<T>(`${prefix}${path}`, body, lease, toolSignal ? AbortSignal.any([signal, toolSignal]) : signal, path.endsWith("/execute") ? 130_000 : this.timeoutMs); }
      catch (error) { if (error instanceof LeaseLostError) onLeaseLost?.(); throw error; }
    };
    return {
      search: async (query, toolSignal, options) => {
        const { parameters, limit } = toolSearchParams(query, options);
        return validateToolDiscoveryPage(await request<unknown>(`/tools?${parameters}`, undefined, toolSignal), limit, () => new WorkerError("INVALID_RUNNER_RESPONSE"));
      },
      tool: (id, toolSignal) => request<Tool>(`/tools/${encodeURIComponent(id)}`, undefined, toolSignal),
      prepare: (id, args, key, toolSignal) => { validateArgumentNumbers(args); return request<Operation>("/operations", { tool_id: id, arguments: args, idempotency_key: key }, toolSignal); },
      operation: (id, toolSignal) => request<Operation>(`/operations/${encodeURIComponent(id)}`, undefined, toolSignal),
      execute: (id, toolSignal) => request<Operation>(`/operations/${encodeURIComponent(id)}/execute`, {}, toolSignal),
    };
  }
}
export function validID(value: unknown): value is string { return typeof value === "string" && /^[a-zA-Z0-9_-]{1,128}$/.test(value); }
async function errorCode(response: Response): Promise<string | undefined> {
  try {
    const reader = response.body?.getReader(); if (!reader) return undefined;
    const chunks: Uint8Array[] = []; let size = 0;
    for (;;) { const part = await reader.read(); if (part.done) break; size += part.value.byteLength; if (size > 16_384) { await reader.cancel(); return undefined; } chunks.push(part.value); }
    const code = JSON.parse(Buffer.concat(chunks).toString("utf8"))?.error?.code;
    return ["lease_expired", "budget_exhausted", "conflict"].includes(code) ? code : undefined;
  } catch { return undefined; }
}
