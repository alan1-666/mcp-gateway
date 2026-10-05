import { useState } from "react";
import type { FormEvent } from "react";
import { APIClient, messageOf } from "./api";
import {
  AdminEmpty,
  AdminError,
  AdminJSON,
  AdminLoading,
  MoreRecords,
  dateLabel,
  useAdminAction,
  useRecordPages,
} from "./AdminUI";
import { filterPath, isoDate } from "./admin-state";
import type { Identity, Operation } from "./types";
interface Audit {
  id: string;
  actor_id: string;
  action: string;
  resource_id: string;
  data: unknown;
  created_at: string;
}
export function AuditTrail({
  api,
  refreshVersion,
}: {
  api: APIClient;
  refreshVersion: string;
}) {
  const [filters, setFilters] = useState({
      actor_id: "",
      action: "",
      resource_id: "",
      from: "",
      to: "",
    }),
    [path, setPath] = useState("/audit?limit=50"),
    [error, setError] = useState("");
  const pages = useRecordPages<Audit>(api, path, refreshVersion);
  function submit(event: FormEvent) {
    event.preventDefault();
    setError("");
    try {
      setPath(
        filterPath("/audit", {
          ...filters,
          from: isoDate(filters.from) ?? "",
          to: isoDate(filters.to) ?? "",
          limit: "50",
        }),
      );
    } catch (error) {
      setError(messageOf(error));
    }
  }
  return (
    <div className="admin-workspace">
      <form className="panel panel-body admin-filter-form" onSubmit={submit}>
        {Object.entries(filters).map(([name, value]) => (
          <label key={name}>
            {name.replaceAll("_", " ")}
            <input
              type={
                name === "from" || name === "to" ? "datetime-local" : "text"
              }
              value={value}
              onChange={(event) =>
                setFilters({ ...filters, [name]: event.target.value })
              }
            />
          </label>
        ))}
        <button className="button secondary">Apply filters</button>
      </form>
      <AdminError error={error} />
      <div className="panel">
        <div className="panel-heading">
          <h2>Audit records</h2>
          <span className="muted">
            {pages.state.total ?? "—"} matching records
          </span>
        </div>
        {pages.state.phase === "loading" ? (
          <AdminLoading />
        ) : !pages.state.items.length && !pages.state.error ? (
          <AdminEmpty>No audit records match these filters.</AdminEmpty>
        ) : null}
        {pages.state.items.map((item) => (
          <details className="admin-record" key={item.id}>
            <summary>
              <strong>{item.action}</strong>
              <span>{dateLabel(item.created_at)}</span>
            </summary>
            <dl className="metadata-grid">
              <div>
                <dt>Actor</dt>
                <dd className="mono">{item.actor_id}</dd>
              </div>
              <div>
                <dt>Resource</dt>
                <dd className="mono">{item.resource_id}</dd>
              </div>
            </dl>
            <AdminJSON label="Recorded change" value={item.data} />
          </details>
        ))}
        <MoreRecords pages={pages} />
      </div>
    </div>
  );
}
interface Reconciliation {
  id: string;
  operation_id: string;
  outcome: string;
  evidence_ref: string;
  note: string;
  actor_id: string;
  created_at: string;
}
export function Reconciliations({
  api,
  operation,
  identity,
}: {
  api: APIClient;
  operation: Operation;
  identity: Identity;
}) {
  const path = `/operations/${operation.id}/reconciliations`,
    pages = useRecordPages<Reconciliation>(api, path),
    action = useAdminAction(api);
  const [outcome, setOutcome] = useState("inconclusive"),
    [evidence, setEvidence] = useState(""),
    [note, setNote] = useState("");
  const canRecord =
    ["admin", "approver"].includes(identity.role) &&
    identity.id !== operation.actor_id;
  async function reload() {
    await pages.controller.reload();
    if (!pages.controller.getSnapshot().error) action.controller.reset();
  }
  async function submit(event: FormEvent) {
    event.preventDefault();
    const result = await action.controller.run<Reconciliation>(path, {
      expected_last_id: pages.state.items[0]?.id ?? "0",
      outcome,
      evidence_ref: evidence,
      note,
    });
    if (result) {
      setNote("");
      setEvidence("");
      await reload();
    }
  }
  return (
    <section className="admin-subsection">
      <h3>Outcome verification</h3>
      <p className="field-help">
        Record evidence from the downstream system. The original operation
        remains UNKNOWN; verification does not execute or retry it.
      </p>
      <AdminError error={action.error} onRetry={() => void reload()} />
      {pages.state.phase === "loading" ? (
        <AdminLoading />
      ) : !pages.state.items.length && !pages.state.error ? (
        <p className="muted">No verification evidence recorded.</p>
      ) : null}
      {pages.state.items.map((item) => (
        <div className="admin-record" key={item.id}>
          <strong>{item.outcome.replaceAll("_", " ")}</strong>
          <span className="field-help">
            {dateLabel(item.created_at)} · {item.actor_id}
          </span>
          <p className="break-word">{item.note}</p>
          <code className="break-word">{item.evidence_ref}</code>
        </div>
      ))}
      <MoreRecords pages={pages} />
      {canRecord ? (
        <form onSubmit={submit}>
          <fieldset
            disabled={
              action.busy ||
              action.needsReload ||
              !pages.state.loaded ||
              !!pages.state.error
            }
          >
            <label>
              Observed outcome
              <select
                value={outcome}
                onChange={(event) => setOutcome(event.target.value)}
              >
                <option value="inconclusive">Inconclusive</option>
                <option value="confirmed_success">Confirmed success</option>
                <option value="confirmed_failure">Confirmed failure</option>
              </select>
            </label>
            <label>
              Evidence reference
              <input
                required
                value={evidence}
                onChange={(event) => setEvidence(event.target.value)}
                placeholder="HTTPS URL or record ID"
              />
              <span className="field-help">
                HTTPS links must omit query parameters and fragments.
              </span>
            </label>
            <label>
              Verification notes
              <textarea
                rows={3}
                required
                value={note}
                onChange={(event) => setNote(event.target.value)}
                maxLength={2000}
              />
            </label>
            <button className="button primary">
              {action.busy ? "Recording…" : "Record verification evidence"}
            </button>
          </fieldset>
        </form>
      ) : (
        <p className="notice notice-info">
          An independent administrator or approver must verify this outcome.
        </p>
      )}
    </section>
  );
}
