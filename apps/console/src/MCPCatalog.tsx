import { useState } from "react";
import type { FormEvent } from "react";
import { APIClient, messageOf } from "./api";
import {
  AdminEmpty,
  AdminError,
  AdminLoading,
  MoreRecords,
  dateLabel,
  useAdminAction,
  useRecordPages,
} from "./AdminUI";
import { catalogCandidate, catalogLabels } from "./mcp-catalog";
import type { CatalogEntry, CatalogReview } from "./mcp-catalog";

export function CatalogSummary({ review }: { review: CatalogReview }) {
  return (
    <div className="catalog-summary" role="status">
      <strong>Compared with registered tools</strong>
      <div className="catalog-counts">
        {Object.entries(review.counts).map(([status, count]) => (
          <span key={status}>
            <b>{count}</b> {catalogLabels[status as keyof typeof catalogLabels]}
          </span>
        ))}
      </div>
      <p className="field-help">
        Checked {dateLabel(review.checked_at)}. Includes drafts and retired
        tools. Publication and access remain separate.
      </p>
    </div>
  );
}

export function CatalogHistory({
  expanded = false,
  api,
  serverID,
  revision,
}: {
  expanded?: boolean;
  api: APIClient;
  serverID: string;
  revision: string;
}) {
  const [open, setOpen] = useState(expanded);
  return (
    <section className="admin-subsection">
      {expanded ? (
        <h3>Catalog history</h3>
      ) : (
        <button
          type="button"
          className="text-button"
          aria-expanded={open}
          onClick={() => setOpen(!open)}
        >
          {open ? "Hide catalog history" : "Open catalog history"}
        </button>
      )}
      {open ? (
        <History api={api} serverID={serverID} revision={revision} />
      ) : null}
    </section>
  );
}
function History({
  api,
  serverID,
  revision,
}: {
  api: APIClient;
  serverID: string;
  revision: string;
}) {
  const pages = useRecordPages<CatalogReview>(
    api,
    `/mcp/servers/${serverID}/catalog-reviews?limit=10`,
    revision,
    "before",
  );
  return (
    <>
      <p className="field-help">
        Last 50 successful discoveries. These are historical observations, not
        live health. Failed or incomplete discovery creates no comparison.
        Discover tools to check again.
      </p>
      {pages.state.phase === "loading" ? (
        <AdminLoading />
      ) : !pages.state.items.length && !pages.state.error ? (
        <AdminEmpty>No catalog comparisons recorded.</AdminEmpty>
      ) : null}
      {pages.state.items.map((review) => (
        <details key={review.id} className="admin-record">
          <summary>
            {dateLabel(review.checked_at)} ·{" "}
            {review.counts.schema_changed + review.counts.description_changed}{" "}
            changed · {review.counts.missing} missing ·{" "}
            {review.counts.unimported} not imported
          </summary>
          <p className="field-help">
            Discovery started {dateLabel(review.started_at)} · Review #
            {review.id}
          </p>
          <div className="table-scroll catalog-history-table">
            <table>
              <thead>
                <tr>
                  <th>Tool</th>
                  <th>Comparison</th>
                  <th>Registered version</th>
                </tr>
              </thead>
              <tbody>
                {review.items.map((entry) => (
                  <tr key={entry.name}>
                    <td className="break-word">{entry.name}</td>
                    <td>{catalogLabels[entry.status]}</td>
                    <td>
                      {entry.imported_version
                        ? `v${entry.imported_version} · ${entry.imported_status}${entry.imported_enabled ? "" : " · disabled"}`
                        : "—"}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </details>
      ))}
      <MoreRecords pages={pages} />
    </>
  );
}

export function CatalogRefresh({
  api,
  entry,
  disabled,
  onRegistry,
}: {
  api: APIClient;
  entry: CatalogEntry;
  disabled: boolean;
  onRegistry: (id: string) => void;
}) {
  const [reason, setReason] = useState("");
  const [error, setError] = useState("");
  const [created, setCreated] = useState(false);
  const action = useAdminAction(api);
  async function submit(event: FormEvent) {
    event.preventDefault();
    if (disabled || created) return;
    setError("");
    try {
      const body = catalogCandidate(entry, reason);
      const candidate = await action.controller.run<{ id: string }>(
        `/tools/${entry.imported_tool_id}/candidates`,
        body,
      );
      if (candidate) setCreated(true);
    } catch (error) {
      setError(messageOf(error));
    }
  }
  return (
    <section className="mcp-tool-review catalog-refresh">
      <div className="panel-heading">
        <div>
          <span className="eyebrow">{catalogLabels[entry.status]}</span>
          <h3>{entry.name}</h3>
        </div>
        <span className="version-label">
          Registered v{entry.imported_version}
        </span>
      </div>
      <div className="panel-body">
        <p>
          The discovered definition differs from the registered tool. Create a
          candidate, then review its parameters, risk and response fields in the
          registry before publishing.
        </p>
        {entry.status === "schema_changed" ? (
          <dl className="metadata-grid">
            <div>
              <dt>Registered schema hash</dt>
              <dd className="mono break-word">{entry.imported_schema_hash}</dd>
            </div>
            <div>
              <dt>Discovered schema hash</dt>
              <dd className="mono break-word">{entry.schema_hash}</dd>
            </div>
          </dl>
        ) : null}
        <AdminError error={error || action.error} />
        {created ? (
          <div className="notice notice-info" role="status">
            Candidate saved. Open the registry and select it under Versions
            &amp; candidates to review the field changes.
          </div>
        ) : (
          <form onSubmit={(event) => void submit(event)}>
            <fieldset disabled={disabled || action.busy || action.needsReload}>
              <label>
                Change reason
                <textarea
                  required
                  rows={2}
                  maxLength={1000}
                  value={reason}
                  onChange={(event) => setReason(event.target.value)}
                />
              </label>
              <button className="button primary">
                {action.busy
                  ? "Creating candidate…"
                  : "Create review candidate"}
              </button>
            </fieldset>
            <p className="field-help">
              The candidate initially keeps the tool’s risk and response policy.
              Review them against the changed behavior. The registered tool
              remains on its current version until publication.
            </p>
          </form>
        )}
        {action.needsReload ? (
          <p className="notice notice-warning" role="status">
            Open the registry to check whether the previous request saved a
            candidate. Discover tools again before retrying.
          </p>
        ) : null}
        <button
          className="button secondary"
          disabled={action.busy}
          onClick={() => onRegistry(entry.imported_tool_id!)}
        >
          Open tool registry
        </button>
      </div>
    </section>
  );
}
