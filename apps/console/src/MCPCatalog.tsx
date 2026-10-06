import { useI18n } from "./i18n";
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
  const { t, locale } = useI18n();
  return (
    <div className="catalog-summary" role="status">
      <strong>{t("Compared with registered tools")}</strong>
      <div className="catalog-counts">
        {Object.entries(review.counts).map(([status, count]) => (
          <span key={status}>
            <b>{count}</b>{" "}
            {t(catalogLabels[status as keyof typeof catalogLabels])}
          </span>
        ))}
      </div>
      <p className="field-help">
        {t(
          "Checked {date}. Includes drafts and retired tools. Publication and access remain separate.",
          { date: dateLabel(review.checked_at, locale) },
        )}
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
  const { t } = useI18n();
  const [open, setOpen] = useState(expanded);
  return (
    <section className="admin-subsection">
      {expanded ? (
        <h3>{t("Catalog history")}</h3>
      ) : (
        <button
          type="button"
          className="text-button"
          aria-expanded={open}
          onClick={() => setOpen(!open)}
        >
          {open ? t("Hide catalog history") : t("Open catalog history")}
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
  const { t, locale } = useI18n();
  const pages = useRecordPages<CatalogReview>(
    api,
    `/mcp/servers/${serverID}/catalog-reviews?limit=10`,
    revision,
    "before",
  );
  return (
    <>
      <button
        type="button"
        className="text-button"
        disabled={pages.state.phase !== "idle"}
        onClick={() => void pages.controller.reload()}
      >
        {t("Reload catalog history")}
      </button>
      <p className="field-help">
        {t(
          "Last 50 successful discoveries. These are historical observations, not live health. Failed or incomplete discovery creates no comparison. Reload history for new scheduled results, or discover tools to check now.",
        )}
      </p>
      {pages.state.phase === "loading" ? (
        <AdminLoading />
      ) : !pages.state.items.length && !pages.state.error ? (
        <AdminEmpty>{t("No catalog comparisons recorded.")}</AdminEmpty>
      ) : null}
      {pages.state.items.map((review) => (
        <details key={review.id} className="admin-record">
          <summary>
            {review.source === "scheduled" ? t("Scheduled") : t("Manual")} ·{" "}
            {dateLabel(review.checked_at, locale)} ·{" "}
            {t(
              "{changed} changed · {missing} missing · {unimported} not imported",
              {
                changed:
                  review.counts.schema_changed +
                  review.counts.description_changed,
                missing: review.counts.missing,
                unimported: review.counts.unimported,
              },
            )}
          </summary>
          <p className="field-help">
            {t("Discovery started {date} · Review #{id}", {
              date: dateLabel(review.started_at, locale),
              id: review.id,
            })}
          </p>
          <div className="table-scroll catalog-history-table">
            <table>
              <thead>
                <tr>
                  <th>{t("Tool")}</th>
                  <th>{t("Comparison")}</th>
                  <th>{t("Registered version")}</th>
                </tr>
              </thead>
              <tbody>
                {review.items.map((entry) => (
                  <tr key={entry.name}>
                    <td className="break-word">{entry.name}</td>
                    <td>{t(catalogLabels[entry.status])}</td>
                    <td>
                      {entry.imported_version
                        ? `v${entry.imported_version} · ${entry.imported_status}${entry.imported_enabled ? "" : t(" · disabled")}`
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
  const { t } = useI18n();
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
          <span className="eyebrow">{t(catalogLabels[entry.status])}</span>
          <h3>{entry.name}</h3>
        </div>
        <span className="version-label">
          {t("Registered v{version}", {
            version: entry.imported_version ?? "—",
          })}
        </span>
      </div>
      <div className="panel-body">
        <p>
          {t(
            "The discovered definition differs from the registered tool. Create a candidate, then review its parameters, risk and response fields in the registry before publishing.",
          )}
        </p>
        {entry.status === "schema_changed" ? (
          <dl className="metadata-grid">
            <div>
              <dt>{t("Registered schema hash")}</dt>
              <dd className="mono break-word">{entry.imported_schema_hash}</dd>
            </div>
            <div>
              <dt>{t("Discovered schema hash")}</dt>
              <dd className="mono break-word">{entry.schema_hash}</dd>
            </div>
          </dl>
        ) : null}
        <AdminError error={error || action.error} />
        {created ? (
          <div className="notice notice-info" role="status">
            {t(
              "Candidate saved. Open the registry and select it under Versions & candidates to review the field changes.",
            )}
          </div>
        ) : (
          <form onSubmit={(event) => void submit(event)}>
            <fieldset disabled={disabled || action.busy || action.needsReload}>
              <label>
                {t("Change reason")}
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
                  ? t("Creating candidate…")
                  : t("Create review candidate")}
              </button>
            </fieldset>
            <p className="field-help">
              {t(
                "The candidate initially keeps the tool’s risk and response policy. Review them against the changed behavior. The registered tool remains on its current version until publication.",
              )}
            </p>
          </form>
        )}
        {action.needsReload ? (
          <p className="notice notice-warning" role="status">
            {t(
              "Open the registry to check whether the previous request saved a candidate. Discover tools again before retrying.",
            )}
          </p>
        ) : null}
        <button
          className="button secondary"
          disabled={action.busy}
          onClick={() => onRegistry(entry.imported_tool_id!)}
        >
          {t("Open tool registry")}
        </button>
      </div>
    </section>
  );
}
