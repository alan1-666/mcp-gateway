import { useState } from "react";
import { APIClient } from "./api";
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
  tools: { name: string; status: string; code: string; message: string }[];
}
export function MCPDiagnostics({
  api,
  serverID,
}: {
  api: APIClient;
  serverID: string;
}) {
  const [open, setOpen] = useState(false);
  return (
    <section className="admin-subsection">
      <div className="action-row">
        <h3>Connection diagnostics</h3>
        <button
          type="button"
          className="button secondary"
          onClick={() => setOpen(!open)}
        >
          {open ? "Hide checks" : "Open connection checks"}
        </button>
      </div>
      {open ? <Reports api={api} serverID={serverID} /> : null}
    </section>
  );
}
function Reports({ api, serverID }: { api: APIClient; serverID: string }) {
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
        Checks inspect connectivity and tool compatibility without invoking a
        business tool.
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
        {action.busy ? "Checking connection…" : "Run connection check"}
      </button>
      {pages.state.phase === "loading" ? (
        <AdminLoading />
      ) : !pages.state.items.length && !pages.state.error ? (
        <AdminEmpty>No checks recorded.</AdminEmpty>
      ) : null}
      {pages.state.items.map((report) => (
        <details key={report.id} className="admin-record">
          <summary>
            <strong>{report.status}</strong> · {report.stage} ·{" "}
            {report.duration_ms} ms <span>{dateLabel(report.checked_at)}</span>
          </summary>
          <p>{report.message}</p>
          <p className="field-help">
            {report.code} · {report.compatible_count} compatible ·{" "}
            {report.incompatible_count} incompatible
          </p>
          {report.tools.length ? (
            <div className="table-scroll">
              <table>
                <thead>
                  <tr>
                    <th>Tool</th>
                    <th>Compatibility</th>
                    <th>Detail</th>
                  </tr>
                </thead>
                <tbody>
                  {report.tools.map((tool) => (
                    <tr key={tool.name}>
                      <td>{tool.name}</td>
                      <td>{tool.status}</td>
                      <td>
                        {tool.message}
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
