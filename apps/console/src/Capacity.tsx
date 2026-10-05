import { useState } from "react";
import type { FormEvent } from "react";
import { APIClient } from "./api";
import {
  AdminEmpty,
  AdminError,
  AdminLoading,
  dateLabel,
  useAdminAction,
  useResource,
} from "./AdminUI";
interface Limit {
  scope: "workspace" | "client" | "upstream";
  scope_id: string;
  version: number;
  max_concurrent: number;
  requests_per_minute: number;
  updated_at: string;
}
interface Policy {
  defaults: {
    scope: Limit["scope"];
    max_concurrent: number;
    requests_per_minute: number;
  }[];
  items: Limit[];
  total: number;
}
interface Metrics {
  calls: {
    transport: string;
    state: string;
    count: number;
    duration_ms_total: number;
  }[];
  rejections: { scope: string; reason: string; count: number }[];
  active_leases: number;
  observed_at: string;
}
export function Capacity({
  api,
  refreshVersion,
}: {
  api: APIClient;
  refreshVersion: string;
}) {
  const policy = useResource<Policy>(api, "/capacity", refreshVersion),
    metrics = useResource<Metrics>(api, "/capacity/metrics", refreshVersion);
  const [editing, setEditing] = useState<Limit | "new" | null>(null);
  return (
    <div className="admin-workspace">
      <AdminError
        error={policy.error || metrics.error}
        onRetry={() => void Promise.all([policy.reload(), metrics.reload()])}
      />
      <section className="panel">
        <div className="panel-heading">
          <h2>Execution capacity</h2>
          <span className="muted">
            {metrics.data ? dateLabel(metrics.data.observed_at) : ""}
          </span>
        </div>
        <div className="panel-body">
          {metrics.loading && !metrics.data ? (
            <AdminLoading />
          ) : metrics.data ? (
            <>
              <p>
                <strong className="capacity-number">
                  {metrics.data.active_leases}
                </strong>{" "}
                active execution leases
              </p>
              <div className="admin-grants">
                <section>
                  <h3>Recorded calls</h3>
                  {metrics.data.calls.length ? (
                    <table>
                      <thead>
                        <tr>
                          <th>Transport / outcome</th>
                          <th>Count</th>
                          <th>Total duration</th>
                        </tr>
                      </thead>
                      <tbody>
                        {metrics.data.calls.map((item) => (
                          <tr key={`${item.transport}:${item.state}`}>
                            <td>
                              {item.transport} · {item.state}
                            </td>
                            <td>{item.count}</td>
                            <td>{item.duration_ms_total} ms</td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  ) : (
                    <p className="muted">No calls recorded.</p>
                  )}
                </section>
                <section>
                  <h3>Admission rejections</h3>
                  {metrics.data.rejections.length ? (
                    <table>
                      <thead>
                        <tr>
                          <th>Scope / reason</th>
                          <th>Count</th>
                        </tr>
                      </thead>
                      <tbody>
                        {metrics.data.rejections.map((item) => (
                          <tr key={`${item.scope}:${item.reason}`}>
                            <td>
                              {item.scope} · {item.reason}
                            </td>
                            <td>{item.count}</td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  ) : (
                    <p className="muted">No capacity rejections recorded.</p>
                  )}
                </section>
              </div>
            </>
          ) : null}
        </div>
      </section>
      <section className="panel">
        <div className="panel-heading">
          <h2>Admission limits</h2>
          <button className="button primary" onClick={() => setEditing("new")}>
            Add override
          </button>
        </div>
        <div className="panel-body">
          {policy.loading && !policy.data ? <AdminLoading /> : null}
          {policy.data ? (
            <>
              <h3>Defaults</h3>
              <div className="table-scroll">
                <table>
                  <thead>
                    <tr>
                      <th>Scope</th>
                      <th>Concurrent executions</th>
                      <th>Requests / minute</th>
                    </tr>
                  </thead>
                  <tbody>
                    {policy.data.defaults.map((item) => (
                      <tr key={item.scope}>
                        <td>{item.scope}</td>
                        <td>{item.max_concurrent}</td>
                        <td>{item.requests_per_minute}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
              <h3>Overrides</h3>
              {policy.data.items.length ? (
                <div className="table-scroll">
                  <table>
                    <thead>
                      <tr>
                        <th>Scope / identifier</th>
                        <th>Concurrent</th>
                        <th>Requests / minute</th>
                        <th>Version</th>
                        <th>Edit</th>
                      </tr>
                    </thead>
                    <tbody>
                      {policy.data.items.map((item) => (
                        <tr key={`${item.scope}:${item.scope_id}`}>
                          <td>
                            {item.scope}
                            <span className="table-description mono">
                              {item.scope_id}
                            </span>
                          </td>
                          <td>{item.max_concurrent}</td>
                          <td>{item.requests_per_minute}</td>
                          <td>{item.version}</td>
                          <td>
                            <button
                              className="button secondary"
                              onClick={() => setEditing(item)}
                            >
                              Edit limit
                            </button>
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              ) : (
                <AdminEmpty>
                  All scopes currently use their default limits.
                </AdminEmpty>
              )}
            </>
          ) : null}
        </div>
      </section>
      {editing ? (
        <LimitForm
          key={
            editing === "new"
              ? "new"
              : `${editing.scope}:${editing.scope_id}:${editing.version}`
          }
          api={api}
          initial={editing === "new" ? undefined : editing}
          onClose={() => setEditing(null)}
          onSaved={async () => {
            setEditing(null);
            await policy.reload();
          }}
        />
      ) : null}
    </div>
  );
}
function LimitForm({
  api,
  initial,
  onClose,
  onSaved,
}: {
  api: APIClient;
  initial?: Limit;
  onClose: () => void;
  onSaved: () => Promise<void>;
}) {
  const [scope, setScope] = useState<Limit["scope"]>(
      initial?.scope ?? "workspace",
    ),
    [id, setID] = useState(initial?.scope_id ?? "*"),
    [concurrent, setConcurrent] = useState(
      String(initial?.max_concurrent ?? 32),
    ),
    [rpm, setRPM] = useState(String(initial?.requests_per_minute ?? 600));
  const action = useAdminAction(api);
  async function submit(event: FormEvent) {
    event.preventDefault();
    const result = await action.controller.run<Limit>("/capacity/limits", {
      scope,
      scope_id: id,
      expected_version: initial?.version ?? 0,
      max_concurrent: Number(concurrent),
      requests_per_minute: Number(rpm),
    });
    if (result) await onSaved();
  }
  return (
    <form className="panel panel-body" onSubmit={submit}>
      <h2>{initial ? "Edit admission limit" : "Add admission override"}</h2>
      <AdminError error={action.error} />
      {action.needsReload ? (
        <p>Close this form and refresh limits before editing again.</p>
      ) : null}
      <fieldset disabled={action.busy || action.needsReload}>
        <div className="form-grid">
          <label>
            Scope
            <select
              disabled={!!initial}
              value={scope}
              onChange={(event) => {
                const value = event.target.value as Limit["scope"];
                setScope(value);
                setID(value === "workspace" ? "*" : "");
              }}
            >
              <option value="workspace">Workspace</option>
              <option value="client">Client</option>
              <option value="upstream">Upstream</option>
            </select>
          </label>
          <label>
            Scope identifier
            <input
              required
              disabled={!!initial || scope === "workspace"}
              value={id}
              onChange={(event) => setID(event.target.value)}
              placeholder={
                scope === "client"
                  ? "client_…"
                  : "mcp:server-id or http:https://api.example.com"
              }
            />
          </label>
          <label>
            Maximum concurrent executions
            <input
              required
              type="number"
              min={1}
              max={256}
              step={1}
              value={concurrent}
              onChange={(event) => setConcurrent(event.target.value)}
            />
          </label>
          <label>
            Requests per minute
            <input
              required
              type="number"
              min={1}
              max={60000}
              step={1}
              value={rpm}
              onChange={(event) => setRPM(event.target.value)}
            />
          </label>
        </div>
        <button className="button primary">
          {action.busy ? "Saving…" : "Save limit"}
        </button>
      </fieldset>
      <button
        type="button"
        className="button secondary"
        disabled={action.busy}
        onClick={onClose}
      >
        Close
      </button>
    </form>
  );
}
