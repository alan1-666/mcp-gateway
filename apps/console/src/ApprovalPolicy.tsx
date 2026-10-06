import { useI18n } from "./i18n";
import { useState } from "react";
import type { APIClient } from "./api";
import { AdminError, useAdminAction } from "./AdminUI";
import type { Tool } from "./types";

export function ApprovalPolicy({
  api,
  tool,
  onChanged,
}: {
  api: APIClient;
  tool: Tool;
  onChanged: () => Promise<void>;
}) {
  const { t } = useI18n();
  const saved =
    tool.approval_policy ?? (tool.risk === "write" ? "required" : "none");
  const [policy, setPolicy] = useState(saved);
  const action = useAdminAction(api);
  return (
    <section className="panel" aria-label={t("Invocation approval policy")}>
      <div className="panel-heading">
        <h2>{t("Invocation policy")}</h2>
        <span className="version-label">v{tool.version}</span>
      </div>
      <div className="panel-body">
        <p>
          {t(
            "Choose whether this tool requires an independent approval for each operation. Client permissions always apply.",
          )}{" "}
        </p>
        <label>
          {t("Approval policy")}{" "}
          <select
            value={policy}
            disabled={action.busy || action.needsReload}
            onChange={(event) =>
              setPolicy(event.target.value as "required" | "none")
            }
          >
            <option value="required">
              {t("Require independent approval")}
            </option>
            <option value="none">
              {t("Allow authorized calls without approval")}{" "}
            </option>
          </select>
        </label>
        {tool.risk === "write" && policy === "none" ? (
          <p className="field-help">
            {t(
              "This allows authorized clients to perform this tool's writes without per-call approval. Existing pending approvals are retained.",
            )}{" "}
          </p>
        ) : null}
        <AdminError error={action.error} onRetry={() => void onChanged()} />
        <button
          type="button"
          className="button primary"
          disabled={policy === saved || action.busy || action.needsReload}
          onClick={async () => {
            const updated = await action.controller.run<Tool>(
              `/tools/${encodeURIComponent(tool.id)}/approval-policy`,
              { expected_version: tool.version, approval_policy: policy },
            );
            if (updated) await onChanged();
          }}
        >
          {action.busy ? t("Saving…") : t("Publish policy change")}
        </button>
      </div>
    </section>
  );
}
