import { useI18n } from "./i18n";
import { useEffect, useMemo, useState, useSyncExternalStore } from "react";
import type { FormEvent } from "react";
import { APIClient } from "./api";
import { AdminError, dateLabel } from "./AdminUI";
import { ConnectorController, connectorPresence } from "./connector-state";

export function Connectors({
  api,
  active,
}: {
  api: APIClient;
  active: boolean;
}) {
  const { t, locale } = useI18n();
  const controller = useMemo(() => new ConnectorController(api), [api]);
  const state = useSyncExternalStore(
    controller.subscribe,
    controller.getSnapshot,
  );
  const [name, setName] = useState("");
  const [showForm, setShowForm] = useState(false);
  const [confirm, setConfirm] = useState("");
  const [now, setNow] = useState(Date.now);
  useEffect(() => {
    void controller.load();
    return () => controller.cancel();
  }, [controller]);
  useEffect(() => {
    if (!active) {
      controller.discardSecret();
      return;
    }
    setNow(Date.now());
    const timer = window.setInterval(() => setNow(Date.now()), 10000);
    return () => window.clearInterval(timer);
  }, [active, controller]);
  async function create(event: FormEvent) {
    event.preventDefault();
    if (await controller.create(name)) {
      setName("");
      setShowForm(false);
    }
  }
  return (
    <div className="connector-workspace">
      <div className="toolbar">
        <p className="muted mcp-toolbar-copy">
          {t("Private connections · Outbound only")}
        </p>
        <div className="action-row">
          <button
            className="button secondary"
            disabled={state.loading || !!state.busy}
            onClick={() => void controller.load()}
          >
            {state.loading ? t("Refreshing…") : t("Refresh status")}
          </button>
          <button
            className="button primary"
            disabled={!!state.busy || !!state.secret || state.requiresReload}
            onClick={() => setShowForm((value) => !value)}
          >
            {showForm ? t("Close form") : t("Register connector")}
          </button>
        </div>
      </div>
      <p className="field-help">
        {t(
          "Run a connector inside your network to expose locally configured HTTP or stdio MCP targets.",
        )}{" "}
        <a
          href="https://github.com/alan1-666/mcp-gateway/blob/main/docs/private-connectors.md"
          target="_blank"
          rel="noreferrer"
        >
          {t("Setup guide ↗")}
        </a>
      </p>
      <AdminError error={t(state.error)} />
      {showForm ? (
        <form className="panel tool-form" onSubmit={create}>
          <div className="panel-heading">
            <h2>{t("Register a connector")}</h2>
          </div>
          <div className="panel-body">
            <label>
              {t("Connector name")}
              <input
                required
                maxLength={120}
                autoComplete="off"
                value={name}
                onChange={(event) => setName(event.target.value)}
                disabled={!!state.busy || state.requiresReload}
                placeholder={t("Staging network")}
              />
            </label>
            <p className="field-help">
              {t(
                "Registration creates a dedicated credential. The connector advertises its targets when it first connects.",
              )}
            </p>
            <button
              className="button primary"
              disabled={!!state.busy || state.requiresReload}
            >
              {state.busy === "create"
                ? t("Registering…")
                : t("Create registration")}
            </button>
          </div>
        </form>
      ) : null}
      {state.secret ? (
        <RegistrationToken
          key={state.secret.connectorID}
          token={state.secret.token}
          connectorID={state.secret.connectorID}
          onDiscard={controller.discardSecret}
        />
      ) : null}
      <section className="panel" aria-label={t("Private connectors")}>
        <div className="panel-heading">
          <h2>{t("Connectors")}</h2>
          <span className="muted">
            {state.loaded ? state.items.length : "—"}
          </span>
        </div>
        {!state.loaded ? (
          <div className="panel-body" role="status">
            {state.loading
              ? t("Loading connectors…")
              : t("Refresh to load connector registrations.")}
          </div>
        ) : !state.items.length ? (
          <div className="panel-body">
            <h3>{t("No private connectors")}</h3>
            <p>
              {t(
                "Register one, install it on your private host, then add its advertised targets from Connections.",
              )}
            </p>
          </div>
        ) : (
          state.items.map((connector) => {
            const presence = connectorPresence(connector, now);
            return (
              <article className="connector-record" key={connector.id}>
                <div className="connector-heading">
                  <div>
                    <h3>{connector.name}</h3>
                    <span className="mono field-help break-word">
                      {connector.id}
                    </span>
                  </div>
                  <span
                    className={`status status-${presence === "Online" ? "published" : "disabled"}`}
                  >
                    <span />
                    {t(presence)}
                  </span>
                </div>
                <p className="field-help">
                  {t("Last contact:")}{" "}
                  {connector.last_seen_at
                    ? dateLabel(connector.last_seen_at, locale)
                    : t("Not connected yet")}{" "}
                  {t("· Status from the last refresh")}
                </p>
                {connector.targets.length ? (
                  <div className="connector-targets">
                    {connector.targets.map((target) => (
                      <div key={target.name} className="connector-target">
                        <strong className="mono break-word">
                          {target.name}
                        </strong>
                        <span className="method-tag">{target.transport}</span>
                        <details>
                          <summary>{t("Target fingerprint")}</summary>
                          <code className="break-word">
                            {target.fingerprint}
                          </code>
                        </details>
                      </div>
                    ))}
                  </div>
                ) : (
                  <p className="field-help">
                    {t(
                      "Start the connector to register its configured targets",
                    )}
                  </p>
                )}
                {connector.enabled ? (
                  <div className="action-row">
                    {confirm === connector.id ? (
                      <>
                        <span className="field-help">
                          {t(
                            "Permanently revoke this connector and block its targets?",
                          )}
                        </span>
                        <button
                          className="button danger"
                          disabled={!!state.busy || state.requiresReload}
                          onClick={() =>
                            void controller
                              .revoke(connector.id)
                              .then(() => setConfirm(""))
                          }
                        >
                          {state.busy === connector.id
                            ? t("Revoking…")
                            : t("Confirm revocation")}
                        </button>
                        <button
                          className="button secondary"
                          disabled={!!state.busy || state.requiresReload}
                          onClick={() => setConfirm("")}
                        >
                          {t("Cancel")}
                        </button>
                      </>
                    ) : (
                      <button
                        className="text-button"
                        disabled={!!state.busy || state.requiresReload}
                        onClick={() => setConfirm(connector.id)}
                      >
                        {t("Revoke connector")}
                      </button>
                    )}
                  </div>
                ) : (
                  <p className="field-help">
                    {t(
                      "Revoked credentials cannot be re-enabled. Register a new connector to reconnect.",
                    )}
                  </p>
                )}
              </article>
            );
          })
        )}
      </section>
    </div>
  );
}

function RegistrationToken({
  token,
  connectorID,
  onDiscard,
}: {
  token: string;
  connectorID: string;
  onDiscard: () => void;
}) {
  const { t } = useI18n();
  const [visible, setVisible] = useState(false);
  const [copy, setCopy] = useState("");
  return (
    <section
      className="panel secret-reveal"
      aria-label={t("Connector registration token")}
    >
      <div className="panel-heading">
        <h2>{t("Save the registration token")}</h2>
        <button className="button secondary" onClick={onDiscard}>
          {t("Close and discard")}
        </button>
      </div>
      <div className="panel-body">
        <p>
          {t(
            "This token is shown once. Leaving this tab removes it from the console. Store it in the connector’s protected token file.",
          )}
        </p>
        <p className="field-help">
          {t("Connector ID:")} <code>{connectorID}</code>
        </p>
        <label>
          {t("Registration token")}
          <input
            type={visible ? "text" : "password"}
            readOnly
            autoComplete="off"
            spellCheck={false}
            value={token}
          />
        </label>
        <div className="action-row">
          <button
            className="button secondary"
            onClick={() => setVisible((value) => !value)}
          >
            {visible ? t("Hide token") : t("Show token")}
          </button>
          <button
            className="button secondary"
            onClick={() => {
              if (!navigator.clipboard) {
                setCopy(
                  "Clipboard unavailable. Select and copy the token manually.",
                );
                return;
              }
              void navigator.clipboard.writeText(token).then(
                () => setCopy("Token copied"),
                () =>
                  setCopy(
                    "Copy unavailable. Select and copy the token manually.",
                  ),
              );
            }}
          >
            {t("Copy token")}
          </button>
          <span className="field-help" role="status">
            {t(copy)}
          </span>
        </div>
      </div>
    </section>
  );
}
