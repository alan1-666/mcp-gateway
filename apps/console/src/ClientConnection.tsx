import { useI18n } from "./i18n";
import { useEffect, useRef, useState } from "react";
import type { FormEvent } from "react";
import { AdminError } from "./AdminUI";
import {
  checkClientConnection,
  connectionConfig,
  connectionWarnings,
} from "./client-connection";
import type { ClientAccess, ConnectionReport } from "./client-connection";

export function ClientConnection({ client }: { client: ClientAccess }) {
  const { t, locale } = useI18n();
  const [key, setKey] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [copyStatus, setCopyStatus] = useState("");
  const [report, setReport] = useState<ConnectionReport | null>(null);
  const active = useRef<AbortController | null>(null);
  useEffect(() => () => active.current?.abort(), []);
  const warnings = connectionWarnings(client);
  const config = connectionConfig(window.location.origin);

  async function check(event: FormEvent) {
    event.preventDefault();
    active.current?.abort();
    const controller = new AbortController();
    active.current = controller;
    setBusy(true);
    setError("");
    setReport(null);
    const suppliedKey = key;
    setKey("");
    try {
      const result = await checkClientConnection(
        client.id,
        suppliedKey,
        controller.signal,
      );
      if (!controller.signal.aborted) setReport(result);
    } catch (failure) {
      if (!controller.signal.aborted)
        setError(
          failure instanceof Error
            ? failure.message
            : "Connection check failed.",
        );
    } finally {
      if (!controller.signal.aborted) setBusy(false);
    }
  }

  return (
    <section className="panel" aria-label={t("Connect your Agent")}>
      <div className="panel-heading">
        <h2>{t("Connect your Agent")}</h2>
        <span className="count-label">Streamable HTTP</span>
      </div>
      <div className="panel-body">
        <p>
          {t(
            "Use this client's key in your MCP application. The gateway exposes search, schema and execution tools.",
          )}
        </p>
        {warnings.length > 0 ? (
          <ul className="field-help">
            {warnings.map((warning) => (
              <li key={warning}>{t(warning)}</li>
            ))}
          </ul>
        ) : null}
        <label>
          {t("MCP endpoint")}
          <input readOnly value={`${window.location.origin}/mcp`} />
        </label>
        <details>
          <summary>{t("Connection configuration")}</summary>
          <p className="field-help">
            {t(
              "For clients accepting an mcpServers JSON configuration. Replace the placeholder locally; other clients may ask for the URL and Bearer header separately.",
            )}
          </p>
          <pre className="connection-config">{config}</pre>
          <button
            type="button"
            className="button secondary"
            onClick={async () => {
              try {
                await navigator.clipboard.writeText(config);
                setCopyStatus(
                  "Configuration copied. Add your client key in your application's private settings.",
                );
              } catch {
                setCopyStatus(
                  "Copy unavailable. Select the configuration and copy it manually.",
                );
              }
            }}
          >
            {t("Copy configuration")}
          </button>
          <p role="status" className="field-help">
            {t(copyStatus)}
          </p>
        </details>
        <form onSubmit={(event) => void check(event)}>
          <h3>{t("Check this client key")}</h3>
          <p className="field-help">
            {t(
              "Checks identity, MCP connection and visible tools using only the supplied key. No business tool runs. The key is cleared when the check starts.",
            )}
          </p>
          <label>
            {t("Client key for connection check")}
            <input
              type="password"
              autoComplete="off"
              spellCheck={false}
              value={key}
              disabled={busy}
              onChange={(event) => {
                setKey(event.target.value);
                setReport(null);
                setError("");
              }}
            />
          </label>
          <div className="action-row">
            <button
              type="submit"
              className="button primary"
              disabled={busy || !key}
            >
              {busy ? t("Checking…") : t("Check connection")}
            </button>
            {busy ? (
              <button
                type="button"
                className="button secondary"
                onClick={() => {
                  active.current?.abort();
                  setBusy(false);
                  setError("Connection check cancelled.");
                }}
              >
                {t("Cancel check")}
              </button>
            ) : null}
          </div>
          <AdminError error={t(error)} />
          {report ? (
            <div role="status">
              <p>
                {t("Connected · {total} authorized published tools visible", {
                  total: report.total.toLocaleString(locale),
                })}
              </p>
              {report.total === 0 ? (
                <p className="field-help">
                  {t(
                    "Publish and enable tools, then grant this client access. A valid key alone does not grant tools.",
                  )}
                </p>
              ) : (
                <ul>
                  {report.tools.map((tool) => (
                    <li key={tool.id}>{tool.name}</li>
                  ))}
                </ul>
              )}
              {report.total > report.tools.length ? (
                <p className="field-help">
                  {t(
                    "Showing the first {count} tools. Your Agent can search the full authorized catalog.",
                    { count: report.tools.length.toLocaleString(locale) },
                  )}
                </p>
              ) : null}
            </div>
          ) : null}
        </form>
        <details>
          <summary>{t("Make your first tool call")}</summary>
          <ol>
            <li>{t("Use search_tools to find an authorized tool.")}</li>
            <li>
              {t(
                "Use get_tool_schema with its tool_id to read the required parameters.",
              )}
            </li>
            <li>
              {t(
                "Use call_tool with the tool ID, arguments and a stable idempotency key. The gateway handles preparation and execution.",
              )}
            </li>
            <li>
              {t(
                "If approval is required, wait for it and repeat the same call with the same key. Read the recorded outcome with get_operation.",
              )}
            </li>
            <li>
              {t(
                "For a large-result reference, use read_result only as needed. Do not replay an UNKNOWN write with a new key.",
              )}
            </li>
          </ol>
        </details>
      </div>
    </section>
  );
}
