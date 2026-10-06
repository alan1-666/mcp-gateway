import { APIError, messageOf } from "./api";
import type { APIClient } from "./api";

export interface ConnectorTarget {
  name: string;
  fingerprint: string;
  transport: "http" | "stdio";
}
export interface Connector {
  id: string;
  workspace_id: string;
  name: string;
  enabled: boolean;
  targets: ConnectorTarget[];
  last_seen_at?: string;
  created_at: string;
}
export function connectorPresence(connector: Connector, now = Date.now()) {
  if (!connector.enabled) return "Revoked";
  if (!connector.last_seen_at) return "Awaiting connection";
  const elapsed = now - Date.parse(connector.last_seen_at);
  return Number.isFinite(elapsed) && elapsed >= -5000 && elapsed < 30000
    ? "Online"
    : "Offline";
}
export function parseConnectorName(value: string) {
  const name = value.trim();
  if (
    !name ||
    new TextEncoder().encode(name).length > 120 ||
    /\p{Cc}/u.test(name)
  )
    throw new Error(
      "Connector name must contain 1–120 UTF-8 bytes and no control characters.",
    );
  return name;
}
export function connectorItems(value: { items: Connector[]; total: number }) {
  if (
    !Array.isArray(value?.items) ||
    value.total !== value.items.length ||
    value.items.some(
      (item) =>
        !item?.id ||
        !item.name ||
        typeof item.enabled !== "boolean" ||
        !Array.isArray(item.targets) ||
        item.targets.some(
          (target) =>
            !target?.name ||
            !target.fingerprint ||
            !["http", "stdio"].includes(target.transport),
        ),
    )
  )
    throw new Error(
      "The gateway returned an invalid connector list. Refresh to try again.",
    );
  return value.items;
}
interface ConnectorState {
  items: Connector[];
  loaded: boolean;
  loading: boolean;
  busy: string;
  error: string;
  requiresReload: boolean;
  secret: { connectorID: string; token: string } | null;
}
const emptyState = (): ConnectorState => ({
  items: [],
  loaded: false,
  loading: false,
  busy: "",
  error: "",
  requiresReload: false,
  secret: null,
});

// Registration credentials exist only in the current controller. Late creation
// responses cannot reveal a token after the user has left the panel.
export class ConnectorController {
  private state = emptyState();
  private listeners = new Set<() => void>();
  private request: AbortController | null = null;
  private generation = 0;
  private secretGeneration = 0;
  constructor(private readonly api: Pick<APIClient, "request">) {}
  getSnapshot = () => this.state;
  subscribe = (listener: () => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };
  private publish(next: ConnectorState) {
    this.state = next;
    for (const listener of this.listeners) listener();
  }
  discardSecret = () => {
    this.secretGeneration++;
    this.publish({ ...this.state, secret: null });
  };
  cancel() {
    this.generation++;
    this.request?.abort();
    this.discardSecret();
  }
  async load() {
    if (this.state.busy) return;
    this.request?.abort();
    const request = new AbortController();
    this.request = request;
    const generation = this.generation;
    this.publish({ ...this.state, loading: true, error: "" });
    try {
      const result = await this.api.request<{
        items: Connector[];
        total: number;
      }>("/connectors", { signal: request.signal });
      if (generation !== this.generation || request.signal.aborted) return;
      this.publish({
        ...this.state,
        items: connectorItems(result),
        loaded: true,
        requiresReload: false,
        loading: false,
      });
    } catch (error) {
      if (generation !== this.generation || request.signal.aborted) return;
      const denied =
        error instanceof APIError && [401, 403].includes(error.status);
      if (denied) this.secretGeneration++;
      this.publish({
        ...this.state,
        ...(denied ? { items: [], loaded: false, secret: null } : {}),
        loading: false,
        error: messageOf(error),
      });
    }
  }
  async create(value: string) {
    if (this.state.busy || this.state.secret || this.state.requiresReload)
      return false;
    let name: string;
    try {
      name = parseConnectorName(value);
    } catch (error) {
      this.publish({ ...this.state, error: messageOf(error) });
      return false;
    }
    const generation = this.generation;
    const secretGeneration = this.secretGeneration;
    this.request?.abort();
    this.publish({ ...this.state, busy: "create", loading: false, error: "" });
    try {
      const result = await this.api.request<{
        connector: Connector;
        token: string;
      }>("/connectors", { method: "POST", body: { name } });
      if (generation !== this.generation) return false;
      if (typeof result.token !== "string" || !result.token)
        throw new Error("The gateway did not return a registration token.");
      connectorItems({ items: [result.connector], total: 1 });
      this.publish({
        ...this.state,
        items: [
          ...this.state.items.filter((item) => item.id !== result.connector.id),
          result.connector,
        ],
        busy: "",
        secret:
          secretGeneration === this.secretGeneration
            ? { connectorID: result.connector.id, token: result.token }
            : null,
      });
      return true;
    } catch (error) {
      const denied =
        error instanceof APIError && [401, 403].includes(error.status);
      if (generation === this.generation)
        this.publish({
          ...this.state,
          ...(denied ? { items: [], loaded: false, secret: null } : {}),
          busy: "",
          requiresReload: true,
          error: `${messageOf(error)} Refresh the list before creating another connector; registration may have completed. If its token was lost, revoke it and create a new registration.`,
        });
      return false;
    }
  }
  async revoke(id: string) {
    if (this.state.busy || this.state.requiresReload) return;
    const generation = this.generation;
    this.request?.abort();
    this.discardSecret();
    this.publish({ ...this.state, busy: id, loading: false, error: "" });
    try {
      const connector = await this.api.request<Connector>(
        `/connectors/${encodeURIComponent(id)}/revoke`,
        { method: "POST", body: {} },
      );
      if (generation !== this.generation) return;
      if (connector.id !== id || connector.enabled)
        throw new Error("Revocation could not be confirmed.");
      this.publish({
        ...this.state,
        busy: "",
        items: this.state.items.map((item) =>
          item.id === id ? connector : item,
        ),
      });
    } catch (error) {
      const denied =
        error instanceof APIError && [401, 403].includes(error.status);
      if (generation === this.generation)
        this.publish({
          ...this.state,
          ...(denied ? { items: [], loaded: false, secret: null } : {}),
          busy: "",
          requiresReload: true,
          error: `${messageOf(error)} Refresh the list to check whether revocation completed.`,
        });
    }
  }
}
