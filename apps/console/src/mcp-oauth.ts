import { APIError, messageOf } from "./api";
import type { APIClient } from "./api";

export type OAuthMethod = "none" | "client_secret_basic";
export type OAuthPhase =
  | "unconfigured"
  | "disconnected"
  | "pending"
  | "exchanging"
  | "connected"
  | "refreshing"
  | "reconnect_required";
export interface OAuthConfiguration {
  issuer: string;
  client_id: string;
  auth_method: OAuthMethod;
  scopes: string[];
}
export interface OAuthStatus {
  server_id: string;
  version: number;
  status: OAuthPhase;
  redirect_uri: string;
  configuration?: OAuthConfiguration;
  expires_at?: string;
  updated_at?: string;
}
export interface OAuthMetadata {
  resource: string;
  issuers: string[];
  issuer: string;
  authorization_endpoint: string;
  token_endpoint: string;
  scopes_supported: string[];
  auth_methods: string[];
  redirect_uri: string;
}
export interface OAuthDraft {
  clientID: string;
  authMethod: OAuthMethod;
  scopes: string;
}
interface OAuthState {
  saved: OAuthStatus | null;
  metadata: OAuthMetadata | null;
  busy: "" | "load" | "discover" | "save" | "connect" | "disconnect";
  error: string;
  notice: string;
  needsReload: boolean;
}
export type OAuthCallback = "connected" | "reconnect_required" | null;

// Only fixed, local messages are shown. Never render provider errors from a URL.
export function oauthCallback(search: string): OAuthCallback {
  const values = new URLSearchParams(search).getAll("oauth");
  return values.length === 1 &&
    (values[0] === "connected" || values[0] === "reconnect_required")
    ? values[0]
    : null;
}
export function cleanOAuthCallbackURL(href: string): string {
  const url = new URL(href);
  url.searchParams.delete("oauth");
  // These values belong only at the backend callback, never in console history.
  for (const key of ["code", "state", "error", "error_description", "iss"])
    url.searchParams.delete(key);
  return `${url.pathname}${url.search}${url.hash}`;
}

function httpsURL(value: string): URL {
  const url = new URL(value);
  if (
    url.protocol !== "https:" ||
    url.username ||
    url.password ||
    url.hash ||
    /^https:\/\/[^/?#]*@/i.test(value)
  )
    throw new Error(
      "OAuth endpoints must use HTTPS without embedded credentials or fragments.",
    );
  return url;
}

export function parseOAuthConfiguration(
  draft: OAuthDraft,
  metadata: OAuthMetadata,
  secret: string,
) {
  const clientID = draft.clientID.trim();
  if (
    !clientID ||
    new TextEncoder().encode(clientID).length > 2048 ||
    /[\u0000-\u001f\u007f]/.test(clientID)
  )
    throw new Error(
      "Enter the registered client ID, using at most 2,048 UTF-8 bytes and no control characters.",
    );
  if (
    !["none", "client_secret_basic"].includes(draft.authMethod) ||
    !metadata.auth_methods.includes(draft.authMethod)
  )
    throw new Error(
      "Choose a client authentication method supported by this provider.",
    );
  if (
    draft.authMethod === "client_secret_basic" &&
    (!secret ||
      new TextEncoder().encode(secret).length > 8192 ||
      /[\r\n\0]/.test(secret))
  )
    throw new Error(
      "Enter the registered client secret, using at most 8,192 UTF-8 bytes and no line breaks.",
    );
  const scopes = [...new Set(draft.scopes.trim().split(/\s+/).filter(Boolean))];
  if (
    scopes.length > 64 ||
    scopes.some(
      (scope) =>
        scope.length > 256 || !/^[\x21\x23-\x5b\x5d-\x7e]+$/.test(scope),
    )
  )
    throw new Error(
      "Use at most 64 space-separated OAuth scopes of up to 256 ASCII characters each, without quotes or backslashes.",
    );
  httpsURL(metadata.issuer);
  return {
    issuer: metadata.issuer,
    client_id: clientID,
    auth_method: draft.authMethod,
    scopes,
    ...(draft.authMethod === "client_secret_basic"
      ? { client_secret: secret }
      : {}),
  };
}

const phases = new Set<OAuthPhase>([
  "unconfigured",
  "disconnected",
  "pending",
  "exchanging",
  "connected",
  "refreshing",
  "reconnect_required",
]);
function statusOf(value: OAuthStatus, serverID: string): OAuthStatus {
  if (
    !value ||
    value.server_id !== serverID ||
    !Number.isSafeInteger(value.version) ||
    value.version < 0 ||
    !phases.has(value.status)
  )
    throw new Error(
      "The gateway returned an invalid OAuth status. Refresh status before continuing.",
    );
  httpsURL(value.redirect_uri);
  if (
    value.status !== "unconfigured" &&
    (!value.configuration?.client_id ||
      !Array.isArray(value.configuration.scopes) ||
      value.configuration.scopes.some((scope) => typeof scope !== "string") ||
      !["none", "client_secret_basic"].includes(
        value.configuration.auth_method,
      ))
  )
    throw new Error(
      "The gateway returned an incomplete OAuth configuration. Refresh status before continuing.",
    );
  if (value.configuration) httpsURL(value.configuration.issuer);
  if (value.expires_at && !Number.isFinite(Date.parse(value.expires_at)))
    throw new Error(
      "The gateway returned an invalid token expiry. Refresh status before continuing.",
    );
  // Retain only the public contract, even if a future response includes more fields.
  const c = value.configuration;
  return {
    server_id: value.server_id,
    version: value.version,
    status: value.status,
    redirect_uri: value.redirect_uri,
    expires_at: value.expires_at,
    updated_at: value.updated_at,
    ...(c
      ? {
          configuration: {
            issuer: c.issuer,
            client_id: c.client_id,
            auth_method: c.auth_method,
            scopes: c.scopes,
          },
        }
      : {}),
  };
}
function metadataOf(value: OAuthMetadata): OAuthMetadata {
  if (
    !value ||
    !Array.isArray(value.issuers) ||
    !value.issuers.includes(value.issuer) ||
    !Array.isArray(value.auth_methods) ||
    !Array.isArray(value.scopes_supported)
  )
    throw new Error(
      "The gateway returned incomplete provider metadata. Discover the provider again.",
    );
  for (const endpoint of [
    value.resource,
    value.issuer,
    value.authorization_endpoint,
    value.token_endpoint,
    value.redirect_uri,
    ...value.issuers,
  ])
    httpsURL(endpoint);
  return value;
}

export class MCPOAuthController {
  private state: OAuthState = {
    saved: null,
    metadata: null,
    busy: "",
    error: "",
    notice: "",
    needsReload: false,
  };
  private listeners = new Set<() => void>();
  private request: AbortController | null = null;
  private generation = 0;
  constructor(
    private readonly api: Pick<APIClient, "request">,
    private readonly serverID: string,
  ) {}
  getSnapshot = () => this.state;
  subscribe = (listener: () => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };
  private publish(patch: Partial<OAuthState>) {
    this.state = { ...this.state, ...patch };
    this.listeners.forEach((listener) => listener());
  }
  cancel = () => {
    this.generation++;
    this.request?.abort();
    this.request = null;
    this.publish({ busy: "", needsReload: true });
  };
  private get path() {
    return `/mcp/servers/${encodeURIComponent(this.serverID)}/oauth`;
  }

  private async run<T>(
    kind: OAuthState["busy"],
    work: (signal: AbortSignal) => Promise<T>,
    success: (value: T) => Partial<OAuthState>,
    mutation = false,
  ): Promise<T | null> {
    if (this.state.busy) return null;
    if (mutation && (!this.state.saved || this.state.needsReload)) {
      this.publish({
        error: "Refresh OAuth status before taking another action.",
      });
      return null;
    }
    const request = new AbortController();
    this.request = request;
    const generation = ++this.generation;
    this.publish({
      busy: kind,
      error: "",
      notice: "",
      ...(kind === "discover" ? { metadata: null } : {}),
    });
    try {
      const value = await work(request.signal);
      if (request.signal.aborted || generation !== this.generation) return null;
      this.publish({ ...success(value), busy: "" });
      return value;
    } catch (error) {
      if (request.signal.aborted || generation !== this.generation) return null;
      const denied =
        error instanceof APIError && [401, 403].includes(error.status);
      this.publish({
        busy: "",
        error:
          messageOf(error) +
          (mutation
            ? " Refresh status before trying again; the previous action may have completed."
            : ""),
        ...(mutation || kind === "load" ? { needsReload: true } : {}),
        ...(denied ? { saved: null, metadata: null, needsReload: true } : {}),
      });
      return null;
    } finally {
      if (generation === this.generation) this.request = null;
    }
  }
  load = () =>
    this.run(
      "load",
      async (signal) =>
        statusOf(
          await this.api.request<OAuthStatus>(this.path, { signal }),
          this.serverID,
        ),
      (saved) => ({ saved, needsReload: false }),
    );
  discover = (issuer?: string) =>
    this.run(
      "discover",
      async (signal) =>
        metadataOf(
          await this.api.request<OAuthMetadata>(`${this.path}/discover`, {
            method: "POST",
            body: issuer ? { issuer } : {},
            signal,
          }),
        ),
      (metadata) => ({ metadata }),
    );
  save = (draft: OAuthDraft, secret: string) => {
    if (!this.state.metadata) return Promise.resolve(null);
    let configuration;
    try {
      configuration = parseOAuthConfiguration(
        draft,
        this.state.metadata,
        secret,
      );
    } catch (error) {
      this.publish({ error: messageOf(error) });
      return Promise.resolve(null);
    }
    return this.run(
      "save",
      async (signal) =>
        statusOf(
          await this.api.request<OAuthStatus>(this.path, {
            method: "PUT",
            body: {
              ...configuration,
              expected_version: this.state.saved!.version,
            },
            signal,
          }),
          this.serverID,
        ),
      (saved) => ({
        saved,
        needsReload: false,
        notice: "Configuration saved. Connect to authorize this server.",
      }),
      true,
    );
  };
  connect = () =>
    this.run(
      "connect",
      async (signal) => {
        if (!this.state.saved?.configuration)
          throw new Error("Save an OAuth configuration before connecting.");
        const result = await this.api.request<{
          authorization_url: string;
          expires_at: string;
        }>(`${this.path}/connect`, {
          method: "POST",
          body: { expected_version: this.state.saved.version },
          signal,
        });
        const url = httpsURL(result.authorization_url);
        if (
          url.searchParams.get("response_type") !== "code" ||
          url.searchParams.get("client_id") !==
            this.state.saved.configuration.client_id ||
          url.searchParams.get("redirect_uri") !==
            this.state.saved.redirect_uri ||
          !url.searchParams.get("state") ||
          url.searchParams.get("code_challenge_method") !== "S256" ||
          !url.searchParams.get("code_challenge")
        )
          throw new Error(
            "The gateway returned an invalid authorization link.",
          );
        return result.authorization_url;
      },
      () => ({
        needsReload: true,
        notice: "Opening the provider’s authorization page…",
      }),
      true,
    );
  disconnect = () =>
    this.run(
      "disconnect",
      async (signal) =>
        statusOf(
          await this.api.request<OAuthStatus>(`${this.path}/disconnect`, {
            method: "POST",
            body: { expected_version: this.state.saved!.version },
            signal,
          }),
          this.serverID,
        ),
      (saved) => ({
        saved,
        needsReload: false,
        notice: "Stored tokens cleared. Provider-side consent is unchanged.",
      }),
      true,
    );
}
