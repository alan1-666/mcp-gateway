import { APIClient } from "./api";
import { AdminError, AdminLoading, dateLabel, useResource } from "./AdminUI";
import { useI18n } from "./i18n";
import {
  executionHealthLabel,
  executionPhaseLabels,
  type MCPExecutionMetrics as Metrics,
} from "./mcp-execution-metrics";

export function MCPExecutionMetrics({
  api,
  serverID,
}: {
  api: APIClient;
  serverID: string;
}) {
  const { t, locale } = useI18n();
  const resource = useResource<Metrics>(
    api,
    `/mcp/servers/${serverID}/execution-metrics`,
  );
  const metrics = resource.data;
  return (
    <section className="admin-subsection">
      <div className="action-row">
        <h3>{t("Observed MCP calls")}</h3>
        <button
          type="button"
          className="button secondary"
          disabled={resource.loading}
          onClick={() => void resource.reload()}
        >
          {t("Refresh call observations")}
        </button>
      </div>
      <p className="field-help">
        {t(
          "Samples the latest 1,000 workspace operations created in the last 24 hours. Remote HTTP MCP calls only. Health reflects the latest sampled call for five minutes, not an uptime guarantee.",
        )}
      </p>
      <AdminError
        error={resource.error}
        onRetry={() => void resource.reload()}
      />
      {resource.loading ? (
        <AdminLoading />
      ) : metrics && !resource.error ? (
        <>
          <p>
            <strong>{t(executionHealthLabel(metrics.health))}</strong>
            {metrics.last_observed_at ? (
              <>
                {" "}
                · {dateLabel(metrics.last_observed_at, locale)} ·{" "}
                <code>{metrics.last_code}</code>
              </>
            ) : null}
          </p>
          <p className="field-help">
            {t(
              "{calls} observed · {attempted} call attempts · {scanned} workspace operations sampled",
              {
                calls: metrics.calls,
                attempted: metrics.attempted,
                scanned: metrics.operations_scanned,
              },
            )}
          </p>
          {metrics.truncated ? (
            <p className="field-help">
              {t("Sample limit reached; older operations are excluded")}
            </p>
          ) : null}
          {metrics.calls > 0 ? (
            <details className="admin-record">
              <summary>
                P50 {metrics.p50_ms} {t("ms")} · P95 {metrics.p95_ms} {t("ms")}{" "}
                · {t("Phase averages and error codes")}
              </summary>
              <div className="table-scroll">
                <table>
                  <thead>
                    <tr>
                      <th>{t("Execution phase")}</th>
                      <th>{t("Mean duration")}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {Object.entries(executionPhaseLabels).map(
                      ([phase, label]) => (
                        <tr key={phase}>
                          <td>{t(label)}</td>
                          <td>
                            {metrics.mean_phases_ms[phase] ?? 0} {t("ms")}
                          </td>
                        </tr>
                      ),
                    )}
                  </tbody>
                </table>
              </div>
              <p className="field-help">
                {t(
                  "Timing excludes admission, approval, result persistence and session teardown. A call attempt does not prove the upstream received it.",
                )}
              </p>
              {Object.entries(metrics.states).map(([state, count]) => (
                <p key={state}>
                  <code>{state}</code> · {count}
                </p>
              ))}
              {Object.entries(metrics.errors).map(([code, count]) => (
                <p key={code}>
                  <code>{code}</code> · {count}
                </p>
              ))}
            </details>
          ) : null}
        </>
      ) : null}
    </section>
  );
}
