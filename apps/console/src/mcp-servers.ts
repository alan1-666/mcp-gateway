import { APIError, messageOf } from "./api";
import type { APIClient } from "./api";
import type { CatalogReview } from "./mcp-catalog";
import type { Identity, MCPServer, RemoteTool, ResponsePolicy, Tool } from "./types";

export const canManageMCPServers = (identity: Pick<Identity, "role">) =>
  identity.role === "admin";

export interface MCPServerDraft {
  name: string;
  namespace: string;
  url: string;
  credential: string;
  timeout: string;
}

export function parseServerDraft(draft: MCPServerDraft) {
  const name = draft.name.trim();
  const namespace = draft.namespace.trim();
  const credential = draft.credential.trim();
  if (!name || new TextEncoder().encode(name).length > 120 || /[\r\n\0]/.test(name))
    throw new Error("Server name must contain 1–120 UTF-8 bytes and no line breaks.");
  if (!/^[a-z][a-z0-9_-]{0,23}$/.test(namespace))
    throw new Error("Namespace must start with a lowercase letter and use at most 24 lowercase letters, digits, underscores, or hyphens.");
  let url: URL;
  try {
    url = new URL(draft.url.trim());
  } catch {
    throw new Error("Enter a complete HTTP or HTTPS server URL.");
  }
  if (!["http:", "https:"].includes(url.protocol) || /[?#]/.test(draft.url.trim()) || new TextEncoder().encode(draft.url.trim()).length > 2048)
    throw new Error("Server URL must use HTTP or HTTPS, contain no query or fragment, and fit within 2,048 UTF-8 bytes.");
  if (url.username || url.password || /^https?:\/\/[^/?#]*@/i.test(draft.url.trim()))
    throw new Error("Use a credential reference instead of credentials in the URL.");
  if (credential && !/^[A-Z][A-Z0-9_]*$/.test(credential))
    throw new Error("Credential references use uppercase letters, digits, and underscores, starting with a letter.");
  const timeout = Number(draft.timeout);
  if (!Number.isSafeInteger(timeout) || timeout < 100 || timeout > 120000)
    throw new Error("Timeout must be an integer between 100 and 120,000 milliseconds.");
  return {
    name, namespace, url: draft.url.trim(), timeout_ms: timeout,
    ...(credential ? { credential_ref: credential } : {}),
  };
}

export function parseResponsePolicy(include: string, maxBytes: string): ResponsePolicy {
  const maximum = Number(maxBytes);
  if (!Number.isSafeInteger(maximum) || maximum < 1024 || maximum > 131072)
    throw new Error("Response limit must be an integer between 1,024 and 131,072 bytes.");
  const paths = include.split(/\r?\n/).map((line) => line.trim()).filter(Boolean);
  if (paths.length > 32) throw new Error("Keep at most 32 response field paths.");
  const parsed = paths.map((path) => {
    if (!path.startsWith("/") || path.includes("\0") || new TextEncoder().encode(path).length > 256 || /~(?![01])/.test(path))
      throw new Error("Each response field must be a JSON Pointer beginning with /, with valid ~0 or ~1 escapes, and at most 256 UTF-8 bytes.");
    const parts = path.slice(1).split("/").map((part) => part.replaceAll("~1", "/").replaceAll("~0", "~"));
    if (parts.some((part) => !part || (part.includes("*") && part !== "*")))
      throw new Error("Response field paths must not contain empty segments or embedded wildcards. Use a complete * segment to select a field from each array element.");
    if (parts.at(-1) === "*")
      throw new Error("An array selector * must be followed by a field path. Select the array field itself to keep each complete element.");
    return parts;
  });
  for (let i = 0; i < parsed.length; i += 1) {
    for (let j = 0; j < i; j += 1) {
      const left = parsed[i];
      const right = parsed[j];
      const length = Math.min(left.length, right.length);
      let shared = 0;
      while (shared < length && left[shared] === right[shared]) shared += 1;
      if (shared === length)
        throw new Error("Response field paths must not repeat or overlap a parent and child field.");
      if (left[shared] === "*" || right[shared] === "*")
        throw new Error("A response node cannot mix an array selector * with specific object keys.");
    }
  }
  return { ...(paths.length ? { include: paths } : {}), max_bytes: maximum };
}

export interface MCPDiscoveryState {
  server: MCPServer | null;
  items: RemoteTool[];
  total: number;
  loaded: boolean;
  loading: boolean;
  importing: string;
  error: string;
  importError: string;
  requiresDiscovery: string[];
  revision: number;
  review: CatalogReview | null;
}

const emptyDiscovery = (): MCPDiscoveryState => ({
  server: null, items: [], total: 0, loaded: false, loading: false,
  importing: "", error: "", importError: "", requiresDiscovery: [], revision: 0, review: null,
});

// Selection generations fence stale reads even if an HTTP transport ignores
// cancellation. Import outcomes are remembered across discovery refreshes.
export class MCPDiscoveryController {
  private state = emptyDiscovery();
  private generation = 0;
  private request: AbortController | null = null;
  private listeners = new Set<() => void>();
  private pending = new Set<string>();
  private uncertain = new Set<string>();
  private imported = new Map<string, string>();

  constructor(
    private readonly api: Pick<APIClient, "request">,
    private readonly identity: Pick<Identity, "role">,
  ) {}

  getSnapshot = () => this.state;
  subscribe = (listener: () => void) => {
    this.listeners.add(listener);
    return () => { this.listeners.delete(listener); };
  };
  private publish(next: MCPDiscoveryState) {
    this.state = next;
    for (const listener of this.listeners) listener();
  }
  cancel() { this.generation += 1; this.request?.abort(); this.request = null; }
  select(server: MCPServer | null) {
    this.cancel();
    this.publish({ ...emptyDiscovery(), server: canManageMCPServers(this.identity) ? server : null });
  }
  private assertAccess() {
    if (!canManageMCPServers(this.identity)) throw new Error("Only administrators can manage MCP servers.");
    if (!this.state.server?.enabled) throw new Error("Enable this MCP server before discovering or importing tools.");
    return this.state.server;
  }

  discover = async () => {
    let server: MCPServer;
    try { server = this.assertAccess(); }
    catch (error) { this.publish({ ...this.state, error: messageOf(error) }); return; }
    if (this.state.importing) return;
    this.cancel();
    const generation = this.generation;
    const request = new AbortController();
    this.request = request;
    this.publish({ ...this.state, items: [], total: 0, loaded: false, loading: true, error: "", importError: "", review: null });
    try {
      const result = await this.api.request<{ items: RemoteTool[]; total: number; review?: CatalogReview }>(`/mcp/servers/${encodeURIComponent(server.id)}/discover`, {
        method: "POST", body: {}, signal: request.signal,
      });
      if (generation !== this.generation || request.signal.aborted) return;
      if (!Array.isArray(result.items) || !Number.isSafeInteger(result.total) || result.total !== result.items.length || result.items.some((item) => !item?.name || !item.schema_hash || !item.gateway_name))
        throw new Error("The gateway returned an invalid discovery result. Discover tools again.");
      if (result.review && (result.review.server_id !== server.id || !Array.isArray(result.review.items) || !result.review.counts))
        throw new Error("The gateway returned an invalid catalog comparison. Discover tools again.");
      const names = new Set<string>();
      const items = result.items.map((item) => {
        if (names.has(item.name)) throw new Error("The server returned duplicate tool names. Review its configuration before importing.");
        names.add(item.name);
        const key = `${server.id}\0${item.name}`;
        this.uncertain.delete(key);
        // A complete comparison is authoritative after a tool is rebound to a
        // different upstream. The local import cache only serves older servers.
        if (result.review && !item.imported_tool_id) this.imported.delete(key);
        return { ...item, imported_tool_id: item.imported_tool_id || this.imported.get(key) };
      });
      this.publish({ ...this.state, items, total: result.total, loaded: true, loading: false, error: "", requiresDiscovery: [], revision: this.state.revision + 1, review: result.review ?? null });
    } catch (error) {
      if (generation !== this.generation || request.signal.aborted) return;
      const message = error instanceof APIError && error.code === "unavailable"
        ? "Discovery did not complete. Previous catalog reviews remain available; run connection diagnostics before checking again."
        : messageOf(error);
      this.publish({ ...this.state, items: [], loaded: false, loading: false, error: message });
    }
  };

  async importTool(remote: RemoteTool, risk: Tool["risk"] = "write", policy: ResponsePolicy = { max_bytes: 65536 }): Promise<Tool | null> {
    let key = "";
    let attempted = false;
    let serverID = "";
    const generation = this.generation;
    try {
      const server = this.assertAccess();
      serverID = server.id;
      const current = this.state.items.find((item) => item.name === remote.name);
      if (!this.state.loaded || this.state.loading || !current || current.schema_hash !== remote.schema_hash)
        throw new Error("Discover tools again and review the current tool schema before importing.");
      key = `${server.id}\0${remote.name}`;
      if (current.imported_tool_id || this.imported.has(key))
        throw new Error("This tool has already been imported. Open it in the tool registry.");
      if (this.state.importing || this.pending.has(key)) return null;
      if (this.uncertain.has(key))
        throw new Error("Discover tools again before retrying. The previous import may have completed.");
      if (risk !== "read" && risk !== "write") throw new Error("Choose a read or write risk classification.");
      const responsePolicy = parseResponsePolicy((policy.include ?? []).join("\n"), String(policy.max_bytes));
      this.pending.add(key);
      attempted = true;
      this.publish({ ...this.state, importing: remote.name, importError: "" });
      const tool = await this.api.request<Tool>(`/mcp/servers/${encodeURIComponent(server.id)}/import`, {
        method: "POST", body: { tool_name: remote.name, schema_hash: remote.schema_hash, risk, response_policy: responsePolicy },
      });
      if (!tool?.id || tool.mcp?.server_id !== server.id || tool.mcp.tool_name !== remote.name)
        throw new Error("Import returned an unexpected tool. Discover tools again to check the registry.");
      this.imported.set(key, tool.id);
      this.uncertain.delete(key);
      if (this.state.server?.id === server.id) this.publish({
        ...this.state, review: null, revision: this.state.revision + 1, ...(this.state.importing === remote.name ? { importing: "", importError: "" } : {}),
        items: this.state.items.map((item) => item.name === remote.name ? { ...item, imported_tool_id: tool.id } : item),
      });
      return tool;
    } catch (error) {
      if (attempted) this.uncertain.add(key);
      if (generation === this.generation || (attempted && this.state.server?.id === serverID && this.state.importing === remote.name)) {
        const denied = error instanceof APIError && [401, 403].includes(error.status);
        this.publish({ ...this.state, ...(denied ? { items: [], loaded: false, error: messageOf(error) } : {}), importing: "",
          importError: messageOf(error) + (attempted ? " Discover tools again before retrying; the previous import may have completed." : ""),
          requiresDiscovery: attempted ? [...new Set([...this.state.requiresDiscovery, remote.name])] : this.state.requiresDiscovery,
        });
      }
      return null;
    } finally {
      if (attempted) this.pending.delete(key);
    }
  }
}
