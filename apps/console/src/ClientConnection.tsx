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
    <section className="panel" aria-label="Connect your Agent">
      <div className="panel-heading">
        <h2>Connect your Agent</h2>
        <span className="count-label">Streamable HTTP</span>
      </div>
      <div className="panel-body">
        <p>
          Use this client's key in your MCP application. The gateway exposes
          search, schema and execution tools.
        </p>
        {warnings.length > 0 ? (
          <ul className="field-help">
            {warnings.map((warning) => (
              <li key={warning}>{warning}</li>
            ))}
          </ul>
        ) : null}
        <label>
          MCP endpoint
          <input readOnly value={`${window.location.origin}/mcp`} />
        </label>
        <details>
          <summary>Connection configuration</summary>
          <p className="field-help">
            For clients accepting an mcpServers JSON configuration. Replace the
            placeholder locally; other clients may ask for the URL and Bearer
            header separately.
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
            Copy configuration
          </button>
          <p role="status" className="field-help">
            {copyStatus}
          </p>
        </details>
        <form onSubmit={(event) => void check(event)}>
          <h3>Check this client key</h3>
          <p className="field-help">
            Checks identity, MCP connection and visible tools using only the
            supplied key. No business tool runs. The key is cleared when the
            check starts.
          </p>
          <label>
            Client key for connection check
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
              {busy ? "Checking…" : "Check connection"}
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
                Cancel check
              </button>
            ) : null}
          </div>
          <AdminError error={error} />
          {report ? (
            <div role="status">
              <p>
                Connected · {report.total} authorized published tools visible
              </p>
              {report.total === 0 ? (
                <p className="field-help">
                  Publish and enable tools, then grant this client access. A
                  valid key alone does not grant tools.
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
                  Showing the first {report.tools.length} tools. Your Agent can
                  search the full authorized catalog.
                </p>
              ) : null}
            </div>
          ) : null}
        </form>
        <details>
          <summary>Make your first tool call</summary>
          <ol>
            <li>
              Use <code>search_tools</code> to find an authorized tool.
            </li>
            <li>
              Use <code>get_tool_schema</code> with its <code>tool_id</code> to
              read the required parameters.
            </li>
            <li>
              Use <code>call_tool</code> with the tool ID, arguments and a
              stable idempotency key. The gateway handles preparation and
              execution.
            </li>
            <li>
              If approval is required, wait for it and repeat the same call with
              the same key. Read the recorded outcome with{" "}
              <code>get_operation</code>.
            </li>
            <li>
              For a large-result reference, use <code>read_result</code> only as
              needed. Do not replay an UNKNOWN write with a new key.
            </li>
          </ol>
        </details>
      </div>
    </section>
  );
}
