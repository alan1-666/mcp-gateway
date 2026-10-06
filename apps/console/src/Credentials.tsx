import { useI18n } from "./i18n";
import { credentialHeaders } from "./admin-state";
import { useState } from "react";
import type { FormEvent } from "react";
import { APIClient, messageOf } from "./api";
import {
  AdminEmpty,
  AdminError,
  AdminLoading,
  dateLabel,
  useAdminAction,
  useResource,
} from "./AdminUI";
interface Credential {
  ref: string;
  origin: string;
  version: number;
  enabled: boolean;
  header_names: string[];
  updated_at: string;
}
export function Credentials({
  api,
  refreshVersion,
}: {
  api: APIClient;
  refreshVersion: string;
}) {
  const { t, locale } = useI18n();
  const resource = useResource<{ items: Credential[]; total: number }>(
      api,
      "/credentials",
      refreshVersion,
    ),
    action = useAdminAction(api);
  const [editing, setEditing] = useState<Credential | "new" | null>(null);
  async function reload() {
    if (await resource.reload()) {
      action.controller.reset();
      setEditing(null);
    }
  }
  async function toggle(item: Credential) {
    if (
      await action.controller.run(
        `/credentials/${encodeURIComponent(item.ref)}/enabled`,
        { expected_version: item.version, enabled: !item.enabled },
      )
    )
      await resource.reload();
  }
  return (
    <div className="admin-workspace">
      <div className="toolbar">
        <p className="muted">
          {t("Encrypted outbound authentication, scoped to an exact origin.")}
        </p>
        <button className="button primary" onClick={() => setEditing("new")}>
          {t("Add credential")}
        </button>
      </div>
      <AdminError
        error={t(resource.error || action.error)}
        onRetry={() => void reload()}
      />
      <section className="panel">
        <div className="panel-heading">
          <h2>
            {t("Credential references")}{" "}
            <span className="count-label">
              {resource.data?.total.toLocaleString(locale) ?? "—"}
            </span>
          </h2>
        </div>
        {resource.loading && !resource.data ? (
          <AdminLoading />
        ) : resource.data?.items.length ? (
          <div className="table-scroll">
            <table>
              <thead>
                <tr>
                  <th>{t("Reference / origin")}</th>
                  <th>{t("Headers")}</th>
                  <th>{t("Status")}</th>
                  <th>{t("Updated")}</th>
                  <th>{t("Actions")}</th>
                </tr>
              </thead>
              <tbody>
                {resource.data.items.map((item) => (
                  <tr key={item.ref}>
                    <td>
                      <strong className="mono">{item.ref}</strong>
                      <span className="table-description">{item.origin}</span>
                    </td>
                    <td>
                      {item.header_names.join(", ")}
                      <span className="table-description">v{item.version}</span>
                    </td>
                    <td>{item.enabled ? t("Enabled") : t("Disabled")}</td>
                    <td>{dateLabel(item.updated_at, locale)}</td>
                    <td>
                      <div className="action-row">
                        <button
                          className="button secondary"
                          disabled={action.busy}
                          onClick={() => setEditing(item)}
                        >
                          {t("Rotate")}
                        </button>
                        <button
                          className="button secondary"
                          disabled={action.busy || action.needsReload}
                          onClick={() => void toggle(item)}
                        >
                          {item.enabled ? t("Disable") : t("Enable")}
                        </button>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : !resource.error ? (
          <AdminEmpty>
            {t(
              "No stored credentials. Add a reference for authenticated integrations.",
            )}
          </AdminEmpty>
        ) : null}
      </section>
      {editing ? (
        <CredentialForm
          key={editing === "new" ? "new" : `${editing.ref}:${editing.version}`}
          api={api}
          initial={editing === "new" ? undefined : editing}
          onClose={() => setEditing(null)}
          onSaved={async () => {
            setEditing(null);
            await resource.reload();
          }}
        />
      ) : null}
    </div>
  );
}
function CredentialForm({
  api,
  initial,
  onClose,
  onSaved,
}: {
  api: APIClient;
  initial?: Credential;
  onClose: () => void;
  onSaved: () => Promise<void>;
}) {
  const { t } = useI18n();
  const [ref, setRef] = useState(initial?.ref ?? ""),
    [origin, setOrigin] = useState(initial?.origin ?? ""),
    [headers, setHeaders] = useState([
      { name: initial?.header_names[0] ?? "Authorization", value: "" },
    ]);
  const action = useAdminAction(api);
  const [validation, setValidation] = useState("");
  async function submit(event: FormEvent) {
    event.preventDefault();
    setValidation("");
    try {
      const values = credentialHeaders(headers);
      const result = await action.controller.run(
        initial
          ? `/credentials/${encodeURIComponent(ref)}/rotate`
          : "/credentials",
        initial
          ? { expected_version: initial.version, headers: values }
          : { ref, origin, headers: values },
      );
      setHeaders(headers.map((header) => ({ ...header, value: "" })));
      if (result) await onSaved();
    } catch (error) {
      setValidation(messageOf(error));
    }
  }

  return (
    <form className="panel" onSubmit={submit}>
      <div className="panel-heading">
        <h2>
          {initial
            ? t("Rotate {name}", { name: initial.ref })
            : t("Add encrypted credential")}
        </h2>
        <button
          type="button"
          className="button secondary"
          disabled={action.busy}
          onClick={onClose}
        >
          {t("Close")}
        </button>
      </div>
      <div className="panel-body">
        <AdminError error={t(validation || action.error)} />
        {action.needsReload ? (
          <p className="field-help">
            {t(
              "Close this editor and reload credential metadata before another change.",
            )}
          </p>
        ) : null}
        <fieldset disabled={action.busy || action.needsReload}>
          <div className="form-grid">
            <label>
              {t("Reference")}
              <input
                required
                pattern="[A-Z][A-Z0-9_]{0,127}"
                value={ref}
                disabled={!!initial}
                onChange={(event) => setRef(event.target.value)}
              />
            </label>
            <label>
              {t("Allowed origin")}
              <input
                required
                type="url"
                placeholder="https://api.example.com"
                value={origin}
                disabled={!!initial}
                onChange={(event) => setOrigin(event.target.value)}
              />
            </label>
          </div>
          <p className="field-help">
            {t(
              "Enter the complete authentication value, for example a Bearer value. Stored values cannot be read back. Rotation replaces every header.",
            )}
          </p>
          {headers.map((header, index) => (
            <div className="form-grid" key={index}>
              <label>
                {t("Header name")}
                <input
                  required
                  value={header.name}
                  onChange={(event) =>
                    setHeaders(
                      headers.map((item, i) =>
                        i === index
                          ? { ...item, name: event.target.value }
                          : item,
                      ),
                    )
                  }
                />
              </label>
              <label>
                {t("Secret value")}
                <input
                  required
                  type="password"
                  autoComplete="new-password"
                  value={header.value}
                  onChange={(event) =>
                    setHeaders(
                      headers.map((item, i) =>
                        i === index
                          ? { ...item, value: event.target.value }
                          : item,
                      ),
                    )
                  }
                />
              </label>
              {headers.length > 1 ? (
                <button
                  type="button"
                  className="text-button"
                  onClick={() =>
                    setHeaders(headers.filter((_, i) => i !== index))
                  }
                >
                  {t("Remove header")}
                </button>
              ) : null}
            </div>
          ))}
          <div className="action-row">
            <button
              type="button"
              className="button secondary"
              disabled={headers.length >= 16}
              onClick={() => setHeaders([...headers, { name: "", value: "" }])}
            >
              {t("Add header")}
            </button>
            <button className="button primary">
              {action.busy
                ? t("Saving…")
                : initial
                  ? t("Replace credential values")
                  : t("Save encrypted credential")}
            </button>
          </div>
        </fieldset>
      </div>
    </form>
  );
}
