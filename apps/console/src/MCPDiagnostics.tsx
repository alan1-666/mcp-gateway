import { useI18n } from "./i18n";
import { useState } from "react";
import { APIClient } from "./api";
import { sessionContractLabel } from "./mcp-compatibility";
import {
  AdminEmpty,
  AdminError,
  AdminLoading,
  MoreRecords,
  dateLabel,
  useAdminAction,
  useRecordPages,
} from "./AdminUI";
interface Report {
  id: string;
  checked_at: string;
  status: string;
  stage: string;
  code: string;
  message: string;
  duration_ms: number;
  compatible_count: number;
  incompatible_count: number;
  session_contract_status?: string;
  tools: { name: string; status: string; code: string; message: string }[];
}
export function MCPDiagnostics({
  expanded = false,
  api,
  serverID,
}: {
  expanded?: boolean;
  api: APIClient;
  serverID: string;
}) {
  const { t } = useI18n();
  const [open, setOpen] = useState(expanded);
  return (
    <section className="admin-subsection">
      <div className="action-row">
        <h3>{t("Connection diagnostics")}</h3>
        {!expanded ? (
          <button
            type="button"
            className="button secondary"
            onClick={() => setOpen(!open)}
          >
            {open ? t("Hide checks") : t("Open connection checks")}
          </button>
        ) : null}
      </div>
      {open ? <Reports api={api} serverID={serverID} /> : null}
    </section>
  );
}
function Reports({ api, serverID }: { api: APIClient; serverID: string }) {
  const { t, locale } = useI18n();
  const action = useAdminAction(api),
    pages = useRecordPages<Report>(
      api,
      `/mcp/servers/${serverID}/checks?limit=20`,
      "",
      "before",
    );
  async function check() {
    const result = await action.controller.run<Report>(
      `/mcp/servers/${serverID}/check`,
      {},
    );
    if (result) await pages.controller.reload();
  }
  return (
    <>
      <p className="field-help">
        {t(
          "Checks inspect connectivity and tool compatibility without invoking a business tool.",
        )}
      </p>
      <AdminError
        error={action.error}
        onRetry={() => {
          void pages.controller.reload().then(() => action.controller.reset());
        }}
      />
      <button
        type="button"
        className="button primary"
        disabled={action.busy || action.needsReload}
        onClick={() => void check()}
      >
        {action.busy ? t("Checking connection…") : t("Run connection check")}
      </button>
      {pages.state.phase === "loading" ? (
        <AdminLoading />
      ) : !pages.state.items.length && !pages.state.error ? (
        <AdminEmpty>{t("No checks recorded.")}</AdminEmpty>
      ) : null}
      {pages.state.items.map((report) => (
        <details key={report.id} className="admin-record">
          <summary>
            <strong>{report.status}</strong> · {report.stage} ·{" "}
            {report.duration_ms} {t("ms")}{" "}
            <span>{dateLabel(report.checked_at, locale)}</span>
          </summary>
          <p>{t(report.message)}</p>
          <p className="field-help">
            {t("Fresh-session contracts")}:{" "}
            {t(sessionContractLabel(report.session_contract_status))}
          </p>
          <p className="field-help">
            {report.code} ·{" "}
            {t("{compatible} compatible · {incompatible} incompatible", {
              compatible: report.compatible_count,
              incompatible: report.incompatible_count,
            })}
          </p>
          {report.tools.length ? (
            <div className="table-scroll">
              <table>
                <thead>
                  <tr>
                    <th>{t("Tool")}</th>
                    <th>{t("Compatibility")}</th>
                    <th>{t("Detail")}</th>
                  </tr>
                </thead>
                <tbody>
                  {report.tools.map((tool) => (
                    <tr key={tool.name}>
                      <td>{tool.name}</td>
                      <td>{tool.status}</td>
                      <td>
                        {t(tool.message)}
                        <span className="table-description">{tool.code}</span>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ) : null}
        </details>
      ))}
      <MoreRecords pages={pages} />
    </>
  );
}
