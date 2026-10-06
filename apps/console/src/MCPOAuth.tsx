import { useI18n } from "./i18n";
import {
  useEffect,
  useMemo,
  useRef,
  useState,
  useSyncExternalStore,
} from "react";
import type { FormEvent } from "react";
import { APIClient } from "./api";
import { dateLabel } from "./AdminUI";
import { MCPOAuthController } from "./mcp-oauth";
import type { OAuthDraft, OAuthMethod, OAuthPhase } from "./mcp-oauth";
import type { MCPServer } from "./types";

const labels: Record<OAuthPhase, string> = {
  unconfigured: "Not configured",
  disconnected: "Disconnected",
  pending: "Awaiting authorization",
  exchanging: "Finishing authorization",
  connected: "Connected",
  refreshing: "Refreshing tokens",
  reconnect_required: "Reconnect required",
};

export function MCPOAuth({
  api,
  server,
  cloud,
}: {
  api: APIClient;
  server: MCPServer;
  cloud: boolean;
}) {
  const { t } = useI18n();
  if (server.credential_ref || !cloud)
    return (
      <section className="admin-subsection" aria-label={t("Upstream OAuth")}>
        <h3>{t("OAuth connection")}</h3>
        <p className="field-help">
          {server.credential_ref
            ? t(
                "This server uses a credential reference. OAuth and static credentials cannot be combined. Add a connection without a credential reference to use OAuth.",
              )
            : t(
                "OAuth connections require a cloud browser session. Sign in to the cloud workspace to authorize a provider.",
              )}
        </p>
      </section>
    );
  return (
    <OAuthSettings
      key={`${server.id}:${server.enabled}`}
      api={api}
      server={server}
    />
  );
}

function OAuthSettings({ api, server }: { api: APIClient; server: MCPServer }) {
  const { t, locale } = useI18n();
  const controller = useMemo(
    () => new MCPOAuthController(api, server.id),
    [api, server.id],
  );
  const state = useSyncExternalStore(
    controller.subscribe,
    controller.getSnapshot,
  );
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState<OAuthDraft>({
    clientID: "",
    authMethod: "none",
    scopes: "",
  });
  const [confirmDisconnect, setConfirmDisconnect] = useState(false);
  const secret = useRef<HTMLInputElement>(null);
  const { saved, metadata, busy, needsReload } = state;
  const locked = !!busy || needsReload || !saved;
  const working =
    saved?.status === "exchanging" || saved?.status === "refreshing";

  useEffect(() => {
    void controller.load();
    return () => {
      if (secret.current) secret.current.value = "";
      controller.cancel();
    };
  }, [controller]);

  function initializeDraft() {
    const configuration = saved?.configuration;
    setDraft({
      clientID: configuration?.client_id ?? "",
      authMethod: configuration?.auth_method ?? "none",
      scopes: configuration?.scopes.join(" ") ?? "",
    });
    if (secret.current) secret.current.value = "";
  }
  async function configure() {
    if (busy) return;
    initializeDraft();
    setEditing(true);
    setConfirmDisconnect(false);
    const value = await controller.discover(saved?.configuration?.issuer);
    if (
      value &&
      !saved?.configuration &&
      !value.auth_methods.includes("none") &&
      value.auth_methods.includes("client_secret_basic")
    )
      setDraft((current) => ({
        ...current,
        authMethod: "client_secret_basic",
      }));
  }
  async function save(event: FormEvent) {
    event.preventDefault();
    const result = await controller.save(draft, secret.current?.value ?? "");
    if (result) {
      if (secret.current) secret.current.value = "";
      setEditing(false);
    }
  }
  async function connect() {
    const url = await controller.connect();
    if (url) window.location.assign(url);
  }
  async function disconnect() {
    const result = await controller.disconnect();
    if (result) {
      if (secret.current) secret.current.value = "";
      setConfirmDisconnect(false);
      setEditing(false);
    }
  }
  async function refresh() {
    const result = await controller.load();
    if (result) {
      setEditing(false);
      setConfirmDisconnect(false);
      if (secret.current) secret.current.value = "";
    }
  }
  return (
    <section
      className="admin-subsection mcp-oauth"
      aria-label={t("Upstream OAuth")}
    >
      <div className="schedule-heading">
        <h3>{t("OAuth connection")}</h3>
        <button
          className="text-button"
          type="button"
          disabled={!!busy}
          onClick={() => void refresh()}
        >
          {busy === "load" ? t("Refreshing…") : t("Refresh status")}
        </button>
      </div>
      <p className="field-help">
        {t(
          "Authorize this MCP server with its provider. Imported tools share this connection within the workspace.",
        )}
      </p>
      {state.error ? (
        <p className="notice notice-error" role="alert">
          {t(state.error)}
        </p>
      ) : null}
      {state.notice ? (
        <p className="notice notice-info" role="status">
          {t(state.notice)}
        </p>
      ) : null}
      {!saved ? (
        <p className="field-help" role="status">
          {busy === "load"
            ? t("Loading OAuth status…")
            : t("Refresh status to load this connection.")}
        </p>
      ) : (
        <>
          <dl className="metadata-grid schedule-status">
            <div>
              <dt>{t("Status")}</dt>
              <dd>{t(labels[saved.status])}</dd>
            </div>
            {saved.expires_at ? (
              <div>
                <dt>{t("Token expires")}</dt>
                <dd>{dateLabel(saved.expires_at, locale)}</dd>
              </div>
            ) : null}
            {saved.configuration ? (
              <>
                <div>
                  <dt>{t("Provider")}</dt>
                  <dd className="mono break-word">
                    {saved.configuration.issuer}
                  </dd>
                </div>
                <div>
                  <dt>{t("Client ID")}</dt>
                  <dd className="mono break-word">
                    {saved.configuration.client_id}
                  </dd>
                </div>
                <div>
                  <dt>{t("Requested scopes")}</dt>
                  <dd className="mono break-word">
                    {saved.configuration.scopes.join(" ") ||
                      t("Provider default")}
                  </dd>
                </div>
              </>
            ) : null}
            <div className="oauth-callback">
              <dt>{t("Registered callback URL")}</dt>
              <dd className="mono break-word">{saved.redirect_uri}</dd>
            </div>
          </dl>
          {!server.enabled ? (
            <p className="notice notice-warning" role="status">
              {t(
                "This server is disabled. Enable it before connecting. Its previous grant has been invalidated.",
              )}
            </p>
          ) : null}
          {saved.status === "reconnect_required" ? (
            <p className="notice notice-warning" role="status">
              {t(
                "Authorize the provider again to resume tool calls. Failed calls are not replayed automatically.",
              )}
            </p>
          ) : null}
          {saved.status === "pending" ? (
            <p className="field-help">
              {t(
                "An authorization attempt is pending. Connecting again replaces that attempt.",
              )}
            </p>
          ) : null}
          {saved.status === "connected" ? (
            <p className="field-help">
              {t(
                "Reconnecting replaces the current grant. Tool calls pause until authorization is complete.",
              )}
            </p>
          ) : null}
          {working ? (
            <p className="field-help" role="status">
              {t(
                "An exchange is in progress. Refresh status to check its result before connecting again.",
              )}
            </p>
          ) : null}
          <div className="action-row">
            {saved.configuration ? (
              <button
                className="button primary"
                type="button"
                disabled={locked || !server.enabled || working || editing}
                onClick={() => void connect()}
              >
                {busy === "connect"
                  ? t("Opening provider…")
                  : saved.status === "connected"
                    ? t("Reconnect provider")
                    : t("Connect provider")}
              </button>
            ) : null}
            <button
              className="button secondary"
              type="button"
              disabled={locked || !server.enabled || working}
              onClick={() => void configure()}
            >
              {busy === "discover"
                ? t("Discovering provider…")
                : saved.configuration
                  ? t("Edit configuration")
                  : t("Configure OAuth")}
            </button>
            {saved.configuration && saved.status !== "disconnected" ? (
              <button
                className="text-button"
                type="button"
                disabled={locked}
                onClick={() => setConfirmDisconnect((current) => !current)}
              >
                {t("Disconnect")}
              </button>
            ) : null}
          </div>
          {confirmDisconnect ? (
            <div className="oauth-confirm">
              <p>
                {t(
                  "Clear the stored tokens and pending authorization for this server? Tool calls will require a new connection. Consent at the provider is unchanged.",
                )}
              </p>
              <div className="action-row">
                <button
                  className="button danger"
                  type="button"
                  disabled={locked}
                  onClick={() => void disconnect()}
                >
                  {busy === "disconnect"
                    ? t("Disconnecting…")
                    : t("Confirm disconnect")}
                </button>
                <button
                  className="button secondary"
                  type="button"
                  disabled={!!busy}
                  onClick={() => setConfirmDisconnect(false)}
                >
                  {t("Cancel")}
                </button>
              </div>
            </div>
          ) : null}
        </>
      )}
      {editing && metadata ? (
        <form className="oauth-form" onSubmit={(event) => void save(event)}>
          <h4>{t("Provider configuration")}</h4>
          <p className="field-help">
            {t(
              "Register the callback URL above with the provider, then enter its client credentials. Saving replaces any previous grant and requires authorization again.",
            )}
          </p>
          <label>
            {t("Authorization provider")}
            <select
              value={metadata.issuer}
              disabled={locked}
              onChange={(event) => {
                if (secret.current) secret.current.value = "";
                void controller.discover(event.target.value).then((value) => {
                  if (value)
                    setDraft((current) => ({
                      ...current,
                      authMethod: value.auth_methods.includes(
                        current.authMethod,
                      )
                        ? current.authMethod
                        : value.auth_methods.includes("none")
                          ? "none"
                          : "client_secret_basic",
                    }));
                });
              }}
            >
              {metadata.issuers.map((issuer) => (
                <option key={issuer} value={issuer}>
                  {issuer}
                </option>
              ))}
            </select>
          </label>
          <label>
            {t("Client ID")}
            <input
              autoComplete="off"
              spellCheck={false}
              value={draft.clientID}
              maxLength={2048}
              disabled={locked}
              onChange={(event) =>
                setDraft({ ...draft, clientID: event.target.value })
              }
              required
            />
          </label>
          <label>
            {t("Client authentication")}
            <select
              value={draft.authMethod}
              disabled={locked}
              onChange={(event) => {
                if (secret.current) secret.current.value = "";
                setDraft({
                  ...draft,
                  authMethod: event.target.value as OAuthMethod,
                });
              }}
            >
              {metadata.auth_methods.includes("none") ? (
                <option value="none">{t("Public client · PKCE")}</option>
              ) : null}
              {metadata.auth_methods.includes("client_secret_basic") ? (
                <option value="client_secret_basic">
                  {t("Client secret · HTTP Basic + PKCE")}
                </option>
              ) : null}
            </select>
          </label>
          {draft.authMethod === "client_secret_basic" ? (
            <label>
              {t("Client secret")}
              <input
                ref={secret}
                type="password"
                autoComplete="new-password"
                maxLength={8192}
                disabled={locked}
                required
              />
              <span className="field-help">
                {t(
                  "Enter it again when saving configuration. Stored secrets are never returned.",
                )}
              </span>
            </label>
          ) : null}
          <label>
            {t("Requested scopes")}
            <input
              value={draft.scopes}
              maxLength={16447}
              autoComplete="off"
              spellCheck={false}
              placeholder={t("Space-separated scopes")}
              disabled={locked}
              onChange={(event) =>
                setDraft({ ...draft, scopes: event.target.value })
              }
            />
            <span className="field-help">
              {t(
                "Request only the permissions these tools need. Leave empty to use the provider default.",
              )}
            </span>
          </label>
          {metadata.scopes_supported.length ? (
            <p className="field-help break-word">
              {t("Advertised scopes:")}{" "}
              <span className="mono">
                {metadata.scopes_supported.join(" ")}
              </span>
            </p>
          ) : null}
          <details className="request-details">
            <summary>{t("Provider endpoints")}</summary>
            <dl>
              <dt>{t("Resource")}</dt>
              <dd className="mono break-word">{metadata.resource}</dd>
              <dt>{t("Authorization")}</dt>
              <dd className="mono break-word">
                {metadata.authorization_endpoint}
              </dd>
              <dt>{t("Token exchange")}</dt>
              <dd className="mono break-word">{metadata.token_endpoint}</dd>
            </dl>
          </details>
          <div className="action-row">
            <button
              className="button primary"
              disabled={
                locked ||
                !server.enabled ||
                !metadata.auth_methods.includes(draft.authMethod)
              }
            >
              {busy === "save" ? t("Saving…") : t("Save configuration")}
            </button>
            <button
              className="button secondary"
              type="button"
              disabled={!!busy}
              onClick={() => {
                if (secret.current) secret.current.value = "";
                setEditing(false);
              }}
            >
              {t("Cancel")}
            </button>
          </div>
        </form>
      ) : null}
    </section>
  );
}
