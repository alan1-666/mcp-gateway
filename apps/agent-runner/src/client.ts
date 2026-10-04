export type JsonObject = Record<string, unknown>;
export type Tool = {
  id: string; name: string; description: string; risk: "read" | "write";
  input_schema: JsonObject; output_schema?: JsonObject; enabled: boolean; status: string; version: number;
};
export type Operation = {
  id: string; tool_id: string; state: string; risk: "read" | "write";
  result?: unknown; error?: string;
};
export type GatewayAPI = Pick<GatewayClient, "search" | "tool" | "prepare" | "operation" | "execute">;

export class GatewayError extends Error {
  constructor(public readonly code: string, public readonly ambiguous = false) { super(code); this.name = "GatewayError"; }
}

export class ArgumentNumberError extends GatewayError {}
export function validateArgumentNumbers(value: unknown): void {
  if (typeof value === "number") {
    if (!Number.isFinite(value)) throw new ArgumentNumberError("NON_FINITE_NUMBER_NOT_ALLOWED");
    if (Number.isInteger(value) && !Number.isSafeInteger(value)) throw new ArgumentNumberError("UNSAFE_INTEGER_USE_STRING");
  } else if (Array.isArray(value)) {
    for (const item of value) validateArgumentNumbers(item);
  } else if (value !== null && typeof value === "object") {
    for (const item of Object.values(value)) validateArgumentNumbers(item);
  }
}

export class GatewayClient {
  readonly origin: string;
  constructor(url: string, private readonly token: string, private readonly timeoutMs = 30_000) {
    let parsed: URL;
    try { parsed = new URL(url); } catch { throw new GatewayError("INVALID_GATEWAY_URL"); }
    if (parsed.username || parsed.password || parsed.search || parsed.hash || !["", "/"].includes(parsed.pathname)) throw new GatewayError("INVALID_GATEWAY_URL");
    const loopback = ["127.0.0.1", "localhost", "[::1]"].includes(parsed.hostname);
    if (parsed.protocol !== "https:" && !(parsed.protocol === "http:" && loopback)) throw new GatewayError("HTTPS_REQUIRED_FOR_REMOTE_GATEWAY");
    if (!token.trim() || /[\r\n]/.test(token)) throw new GatewayError("GATEWAY_TOKEN_REQUIRED");
    this.origin = parsed.origin;
  }

  async request<T>(path: string, body?: unknown, signal?: AbortSignal): Promise<T> {
    const mutating = body !== undefined;
    const deadline = AbortSignal.timeout(this.timeoutMs);
    try {
      const response = await fetch(`${this.origin}/api/v1${path}`, {
        method: mutating ? "POST" : "GET", redirect: "error",
        headers: { authorization: `Bearer ${this.token}`, accept: "application/json", ...(mutating ? { "content-type": "application/json" } : {}) },
        body: mutating ? JSON.stringify(body) : undefined,
        signal: signal ? AbortSignal.any([deadline, signal]) : deadline,
      });
      if (!response.ok) {
        await response.body?.cancel();
        // Never echo an upstream error body, URL or request credentials.
        throw new GatewayError(`GATEWAY_HTTP_${response.status}`, mutating && response.status >= 500);
      }
      if (!response.headers.get("content-type")?.includes("application/json")) {
        await response.body?.cancel();
        throw new GatewayError("INVALID_GATEWAY_RESPONSE", mutating);
      }
      const reader = response.body?.getReader();
      if (!reader) throw new GatewayError("EMPTY_GATEWAY_RESPONSE", mutating);
      const chunks: Uint8Array[] = [];
      let length = 0;
      for (;;) {
        const part = await reader.read();
        if (part.done) break;
        length += part.value.byteLength;
        if (length > 262_144) { await reader.cancel(); throw new GatewayError("GATEWAY_RESPONSE_TOO_LARGE", mutating); }
        chunks.push(part.value);
      }
      try { return JSON.parse(Buffer.concat(chunks).toString("utf8")) as T; }
      catch { throw new GatewayError("INVALID_GATEWAY_RESPONSE", mutating); }
    } catch (error) {
      if (error instanceof GatewayError) throw error;
      throw new GatewayError(signal?.aborted ? "REQUEST_CANCELLED" : "GATEWAY_UNREACHABLE_OR_TIMEOUT", mutating);
    }
  }

  me(signal?: AbortSignal) { return this.request<{ id: string; workspace_id: string; role: string }>("/me", undefined, signal); }
  async search(query: string, signal?: AbortSignal): Promise<Tool[]> {
    const response = await this.request<{ items: Tool[] }>(`/tools?query=${encodeURIComponent(query)}`, undefined, signal);
    if (!Array.isArray(response.items)) throw new GatewayError("INVALID_GATEWAY_RESPONSE");
    return response.items;
  }
  tool(id: string, signal?: AbortSignal) { return this.request<Tool>(`/tools/${encodeURIComponent(id)}`, undefined, signal); }
  prepare(toolId: string, args: JsonObject, idempotencyKey: string, signal?: AbortSignal) {
    validateArgumentNumbers(args);
    return this.request<Operation>("/operations", { tool_id: toolId, arguments: args, idempotency_key: idempotencyKey }, signal);
  }
  operation(id: string, signal?: AbortSignal) { return this.request<Operation>(`/operations/${encodeURIComponent(id)}`, undefined, signal); }
  execute(id: string, signal?: AbortSignal) { return this.request<Operation>(`/operations/${encodeURIComponent(id)}/execute`, {}, signal); }
}
