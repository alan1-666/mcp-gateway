/** Connection checks use a supplied machine key, never the administrator session. */
export interface ClientAccess {
  id: string;
  enabled: boolean;
  scopes: string[];
  server_ids: string[];
  tool_ids: string[];
  key_expires_at: string;
}

export function connectionWarnings(
  client: ClientAccess,
  now = Date.now(),
): string[] {
  const warnings: string[] = [];
  if (!client.enabled) warnings.push("This client is disabled.");
  const expiry = Date.parse(client.key_expires_at);
  if (!Number.isFinite(expiry) || expiry <= now)
    warnings.push("The client key has expired or its expiry is invalid.");
  if (!client.scopes.includes("tools:read"))
    warnings.push("Enable discovery access to find tools.");
  if (!client.scopes.includes("tools:invoke"))
    warnings.push(
      "Invocation access is not enabled; this client can only discover tools.",
    );
  if (!client.server_ids.length && !client.tool_ids.length)
    warnings.push("Grant a published tool or server before connecting.");
  return warnings;
}

export function connectionConfig(origin: string): string {
  const url = new URL(origin);
  const loopback = ["localhost", "127.0.0.1", "[::1]"].includes(url.hostname);
  if (
    (url.protocol !== "https:" && !(url.protocol === "http:" && loopback)) ||
    url.username ||
    url.password ||
    url.pathname !== "/" ||
    url.search ||
    url.hash
  ) {
    throw new Error(
      "Use an HTTPS gateway origin, or loopback HTTP for local development.",
    );
  }
  return JSON.stringify(
    {
      mcpServers: {
        rillgate: {
          url: `${url.origin}/mcp`,
          headers: { Authorization: "Bearer REPLACE_WITH_CLIENT_KEY" },
        },
      },
    },
    null,
    2,
  );
}

export interface ConnectionReport {
  total: number;
  tools: { id: string; name: string }[];
}

type Fetcher = (input: string, init: RequestInit) => Promise<Response>;
const maxResponseBytes = 256 * 1024;
const protocol = "2025-06-18";
const supportedProtocols = new Set([
  "2024-11-05",
  "2025-03-26",
  protocol,
  "2025-11-25",
]);

function object(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

async function boundedJSON(response: Response): Promise<unknown> {
  if (
    !response.headers.get("Content-Type")?.includes("application/json") ||
    !response.body
  ) {
    throw new Error(
      "Expected a JSON response from the gateway. Check the proxy configuration.",
    );
  }
  const reader = response.body.getReader();
  const chunks: Uint8Array[] = [];
  let size = 0;
  try {
    while (true) {
      const { done, value } = await reader.read();
      if (done) break;
      size += value.byteLength;
      if (size > maxResponseBytes)
        throw new Error("The connection check response exceeded 256 KiB.");
      chunks.push(value);
    }
  } finally {
    await reader.cancel();
    reader.releaseLock();
  }
  const bytes = new Uint8Array(size);
  let offset = 0;
  for (const chunk of chunks) {
    bytes.set(chunk, offset);
    offset += chunk.byteLength;
  }
  try {
    return JSON.parse(new TextDecoder().decode(bytes));
  } catch {
    throw new Error("The gateway returned invalid JSON.");
  }
}

/** Bounded check for Rillgate's stateless JSON transport; no upstream tool executes. */
export async function checkClientConnection(
  clientID: string,
  key: string,
  signal: AbortSignal,
  fetcher: Fetcher = fetch,
): Promise<ConnectionReport> {
  if (!/^mgc_[A-Za-z0-9_-]{43}$/.test(key))
    throw new Error(
      "Enter a machine client API key from Create client or Rotate key.",
    );
  const headers: Record<string, string> = {
    Authorization: `Bearer ${key}`,
    Accept: "application/json, text/event-stream",
    "Content-Type": "application/json",
  };
  const requestSignal = AbortSignal.any([signal, AbortSignal.timeout(20000)]);
  async function request(path: string, body?: unknown): Promise<Response> {
    requestSignal.throwIfAborted();
    let response: Response;
    try {
      response = await fetcher(path, {
        method: body === undefined ? "GET" : "POST",
        headers: { ...headers },
        ...(body === undefined ? {} : { body: JSON.stringify(body) }),
        credentials: "omit",
        redirect: "error",
        cache: "no-store",
        signal: requestSignal,
      });
    } catch {
      throw new Error(
        requestSignal.aborted
          ? "Connection check cancelled or timed out."
          : "Cannot reach the gateway. Check your network and HTTPS endpoint.",
      );
    }
    if (!response.ok) {
      await response.body?.cancel();
      if (response.status === 401)
        throw new Error(
          "Client key rejected. It may be expired, revoked or replaced.",
        );
      if (response.status === 403)
        throw new Error(
          "Access denied. Check the client scopes and gateway origin policy.",
        );
      if (response.status === 429)
        throw new Error(
          "Gateway capacity reached. Wait before checking again.",
        );
      throw new Error(`Gateway connection failed (HTTP ${response.status}).`);
    }
    return response;
  }
  async function rpc(
    id: number,
    method: string,
    params: unknown,
  ): Promise<Record<string, unknown>> {
    const envelope = await boundedJSON(
      await request("/mcp", { jsonrpc: "2.0", id, method, params }),
    );
    if (
      !object(envelope) ||
      envelope.jsonrpc !== "2.0" ||
      envelope.id !== id ||
      envelope.error ||
      !object(envelope.result)
    ) {
      throw new Error("The gateway returned an incompatible MCP response.");
    }
    return envelope.result;
  }
  const identity = await boundedJSON(await request("/api/v1/me"));
  if (!object(identity) || identity.client_id !== clientID)
    throw new Error("This key does not belong to the selected client.");
  const initialized = await rpc(1, "initialize", {
    protocolVersion: protocol,
    capabilities: {},
    clientInfo: { name: "rillgate-connection-check", version: "1.0.0" },
  });
  if (
    typeof initialized.protocolVersion !== "string" ||
    !supportedProtocols.has(initialized.protocolVersion)
  ) {
    throw new Error("The gateway negotiated an unsupported MCP version.");
  }
  headers["MCP-Protocol-Version"] = initialized.protocolVersion;
  const notification = await request("/mcp", {
    jsonrpc: "2.0",
    method: "notifications/initialized",
  });
  await notification.body?.cancel();
  const result = await rpc(2, "tools/call", {
    name: "search_tools",
    arguments: { limit: 5 },
  });
  if (result.isError)
    throw new Error(
      "Tool discovery was denied or failed. Check the client grants and gateway health.",
    );
  if (
    !Array.isArray(result.content) ||
    result.content.length !== 1 ||
    !object(result.content[0]) ||
    result.content[0].type !== "text" ||
    typeof result.content[0].text !== "string"
  ) {
    throw new Error("The gateway returned an incompatible discovery result.");
  }
  let page: unknown;
  try {
    page = JSON.parse(result.content[0].text);
  } catch {
    throw new Error("The gateway returned invalid discovery JSON.");
  }
  if (
    !object(page) ||
    !Number.isSafeInteger(page.total) ||
    (page.total as number) < 0 ||
    !Array.isArray(page.items) ||
    page.items.length > 5 ||
    page.items.length > (page.total as number)
  ) {
    throw new Error("The gateway returned an invalid discovery page.");
  }
  const tools = page.items.map((item: unknown) => {
    if (
      !object(item) ||
      typeof item.id !== "string" ||
      !item.id ||
      typeof item.name !== "string" ||
      !item.name
    ) {
      throw new Error("The gateway returned an invalid tool summary.");
    }
    return { id: item.id, name: item.name };
  });
  return { total: page.total as number, tools };
}
