export type JsonObject = Record<string, unknown>;
export type ToolSummary = { id: string; name: string; description: string; risk: "read" | "write"; version: number };
export type ToolDiscoveryPage = { items: ToolSummary[]; next_cursor?: string; total: number };
export type ToolSearchOptions = { cursor?: string; limit?: number };
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

export class ToolSearchInputError extends GatewayError {}
export function toolSearchParams(query: string, options?: ToolSearchOptions): { parameters: string; limit: number } {
  if (typeof query !== "string" || Buffer.byteLength(query.trim(), "utf8") > 200) throw new ToolSearchInputError("INVALID_TOOL_SEARCH_QUERY");
  if (options !== undefined && (!isObject(options) || Object.keys(options).some(key => !["cursor", "limit"].includes(key)))) throw new ToolSearchInputError("INVALID_TOOL_SEARCH_OPTIONS");
  const limit = options?.limit ?? 25;
  if ((options?.limit !== undefined && typeof options.limit !== "number") || !Number.isInteger(limit) || limit < 1 || limit > 50) throw new ToolSearchInputError("INVALID_TOOL_SEARCH_LIMIT");
  if (options?.cursor !== undefined && (typeof options.cursor !== "string" || Buffer.byteLength(options.cursor, "utf8") > 2048)) throw new ToolSearchInputError("INVALID_TOOL_SEARCH_CURSOR");
  const parameters = new URLSearchParams({ query: query.trim() });
  if (options?.limit !== undefined) parameters.set("limit", String(limit));
  if (options?.cursor !== undefined) parameters.set("cursor", options.cursor);
  return { parameters: parameters.toString(), limit };
}

/** Reject contract drift, including accidental registry schemas or connection metadata. */
export function validateToolDiscoveryPage(value: unknown, limit: number, invalid: () => GatewayError = () => new GatewayError("INVALID_GATEWAY_RESPONSE")): ToolDiscoveryPage {
  if (!isObject(value) || Object.keys(value).some(key => !["items", "next_cursor", "total"].includes(key)) || !Array.isArray(value.items) || value.items.length > limit || typeof value.total !== "number" || !Number.isSafeInteger(value.total) || value.total < 0) throw invalid();
  if (value.next_cursor !== undefined && (typeof value.next_cursor !== "string" || Buffer.byteLength(value.next_cursor, "utf8") > 2048)) throw invalid();
  const ids = new Set<string>();
  for (const tool of value.items) {
    if (!isObject(tool) || Object.keys(tool).some(key => !["id", "name", "description", "risk", "version"].includes(key)) || typeof tool.id !== "string" || !tool.id || typeof tool.name !== "string" || !tool.name || typeof tool.description !== "string" || !["read", "write"].includes(tool.risk as string) || typeof tool.version !== "number" || !Number.isSafeInteger(tool.version) || tool.version < 1 || ids.has(tool.id)) throw invalid();
    ids.add(tool.id);
  }
  return value as ToolDiscoveryPage;
}
function isObject(value: unknown): value is Record<string, unknown> { return value !== null && typeof value === "object" && !Array.isArray(value); }

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
  async search(query: string, signal?: AbortSignal, options?: ToolSearchOptions): Promise<ToolDiscoveryPage> {
    const { parameters, limit } = toolSearchParams(query, options);
    return validateToolDiscoveryPage(await this.request<unknown>(`/catalog/tools?${parameters}`, undefined, signal), limit);
  }
  tool(id: string, signal?: AbortSignal) { return this.request<Tool>(`/tools/${encodeURIComponent(id)}`, undefined, signal); }
  prepare(toolId: string, args: JsonObject, idempotencyKey: string, signal?: AbortSignal) {
    validateArgumentNumbers(args);
    return this.request<Operation>("/operations", { tool_id: toolId, arguments: args, idempotency_key: idempotencyKey }, signal);
  }
  operation(id: string, signal?: AbortSignal) { return this.request<Operation>(`/operations/${encodeURIComponent(id)}`, undefined, signal); }
  execute(id: string, signal?: AbortSignal) { return this.request<Operation>(`/operations/${encodeURIComponent(id)}/execute`, {}, signal); }
}
