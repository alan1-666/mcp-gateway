import { useCallback, useEffect, useRef, useState } from "react";
import type { FormEvent } from "react";
import { APIClient, messageOf } from "./api";
import { dateLabel } from "./AdminUI";

interface Schedule {
  server_id: string;
  enabled: boolean;
  interval_seconds: number;
  revision: number;
  next_check_at: string | null;
  running: boolean;
  last_finished_at: string | null;
  last_success_at: string | null;
  last_error_code: string;
  consecutive_failures: number;
}
const errors: Record<string, string> = {
  discovery_failed:
    "The server could not provide a complete catalog. Run connection diagnostics before checking again.",
  timeout: "The catalog check timed out.",
  server_changed:
    "Server access changed while the check was running. Its result was discarded.",
  comparison_failed:
    "The catalog could not be compared within the supported limits.",
};

export function CatalogSchedule({
  api,
  serverID,
  serverEnabled,
}: {
  api: APIClient;
  serverID: string;
  serverEnabled: boolean;
}) {
  const [saved, setSaved] = useState<Schedule | null>(null);
  const [enabled, setEnabled] = useState(false);
  const [interval, setInterval] = useState("60");
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [needsReload, setNeedsReload] = useState(false);
  const [notice, setNotice] = useState("");
  const pending = useRef(false);
  const mounted = useRef(true);
  const read = useRef<AbortController | null>(null);
  const path = `/mcp/servers/${encodeURIComponent(serverID)}/catalog-schedule`;

  const load = useCallback(async () => {
    if (pending.current) return;
    read.current?.abort();
    const request = new AbortController();
    read.current = request;
    setLoading(true);
    setError("");
    setNotice("");
    try {
      const value = await api.request<Schedule>(path, {
        signal: request.signal,
      });
      if (request.signal.aborted) return;
      setSaved(value);
      setEnabled(value.enabled);
      setInterval(String(value.interval_seconds / 60));
      setNeedsReload(false);
    } catch (error) {
      if (!request.signal.aborted) {
        setError(messageOf(error));
        setNeedsReload(true);
        setSaved(null);
      }
    } finally {
      if (!request.signal.aborted) setLoading(false);
    }
  }, [api, path]);
  useEffect(() => {
    mounted.current = true;
    void load();
    return () => {
      mounted.current = false;
      read.current?.abort();
    };
  }, [load]);

  async function save(event: FormEvent) {
    event.preventDefault();
    if (pending.current || loading || needsReload || !saved) return;
    const seconds = Number(interval) * 60;
    if (!Number.isSafeInteger(seconds) || seconds < 300 || seconds > 86400) {
      setError("Choose an interval between 5 minutes and 24 hours.");
      return;
    }
    pending.current = true;
    setBusy(true);
    setError("");
    setNotice("");
    try {
      const value = await api.request<Schedule>(path, {
        method: "PUT",
        body: {
          enabled,
          interval_seconds: seconds,
          expected_revision: saved.revision,
        },
      });
      if (!mounted.current) return;
      setSaved(value);
      setEnabled(value.enabled);
      setInterval(String(value.interval_seconds / 60));
      setNotice(
        value.enabled
          ? "Schedule saved. The worker will check this server when due."
          : "Scheduled checks paused.",
      );
    } catch (error) {
      if (mounted.current) {
        setError(
          `${messageOf(error)} Reload the schedule to check its saved state before trying again.`,
        );
        setNeedsReload(true);
      }
    } finally {
      pending.current = false;
      if (mounted.current) setBusy(false);
    }
  }
  const status = !saved?.enabled
    ? "Off"
    : !serverEnabled
      ? "Paused with server"
      : saved.running
        ? "Checking"
        : saved.last_error_code
          ? "Retry scheduled"
          : "Scheduled";
  return (
    <section
      className="admin-subsection catalog-schedule"
      aria-label="Scheduled catalog checks"
    >
      <div className="schedule-heading">
        <h3>Catalog checks</h3>
        <button
          className="text-button"
          type="button"
          disabled={busy || loading}
          onClick={() => void load()}
        >
          Reload schedule
        </button>
      </div>
      <p className="field-help">
        Check for new, changed, or removed tools automatically. Review changes
        in Activity before publishing a tool version.
      </p>
      {error ? (
        <p className="notice notice-error" role="alert">
          {error}
        </p>
      ) : null}
      {loading ? (
        <p role="status">Loading schedule…</p>
      ) : saved ? (
        <>
          <dl className="metadata-grid schedule-status">
            <div>
              <dt>Status</dt>
              <dd>{status}</dd>
            </div>
            <div>
              <dt>Last successful check</dt>
              <dd>
                {saved.last_success_at
                  ? dateLabel(saved.last_success_at)
                  : "Not checked yet"}
              </dd>
            </div>
            <div>
              <dt>Next check</dt>
              <dd>
                {saved.enabled && serverEnabled && saved.next_check_at
                  ? saved.running
                    ? "In progress"
                    : dateLabel(saved.next_check_at)
                  : "—"}
              </dd>
            </div>
          </dl>
          {saved.last_error_code ? (
            <p className="notice notice-warning" role="status">
              {errors[saved.last_error_code] ??
                "The last check did not complete."}{" "}
              {saved.consecutive_failures} consecutive failures ·{" "}
              {dateLabel(saved.last_finished_at ?? "")}. Previous catalog
              history is preserved.
            </p>
          ) : null}
          <form onSubmit={(event) => void save(event)}>
            <fieldset disabled={busy || needsReload}>
              <label className="catalog-filter">
                <input
                  type="checkbox"
                  checked={enabled}
                  onChange={(event) => setEnabled(event.target.checked)}
                />
                Enable scheduled checks
              </label>
              <div className="schedule-controls">
                <label>
                  Check every (minutes)
                  <input
                    type="number"
                    min="5"
                    max="1440"
                    step="any"
                    required
                    value={interval}
                    onChange={(event) => setInterval(event.target.value)}
                  />
                </label>
                <button
                  className="button secondary"
                  disabled={
                    saved.enabled === enabled &&
                    Number(interval) * 60 === saved.interval_seconds
                  }
                >
                  {busy ? "Saving…" : "Save schedule"}
                </button>
              </div>
            </fieldset>
          </form>
          <p className="field-help">
            5 minutes to 24 hours. Repeated failures slow checks down, up to
            once a day. Disabling server access pauses checks automatically.
          </p>
        </>
      ) : null}
      {notice ? (
        <p className="notice notice-success" role="status">
          {notice}
        </p>
      ) : null}
    </section>
  );
}
