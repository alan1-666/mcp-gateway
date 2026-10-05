import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  useSyncExternalStore,
} from "react";
import type { ReactNode } from "react";
import { APIClient, APIError, messageOf } from "./api";
import { ToolSearchController } from "./tool-search";
import { AdminAction } from "./admin-state";

export function useResource<T>(api: APIClient, path: string, revision = "") {
  const [data, setData] = useState<T | null>(null),
    [error, setError] = useState(""),
    [loading, setLoading] = useState(true);
  const request = useRef<AbortController | null>(null);
  const reload = useCallback(async () => {
    request.current?.abort();
    const controller = new AbortController();
    request.current = controller;
    setLoading(true);
    setError("");
    try {
      const result = await api.request<T>(path, { signal: controller.signal });
      if (!controller.signal.aborted) {
        setData(result);
        return true;
      }
    } catch (error) {
      if (!controller.signal.aborted) {
        if (error instanceof APIError && [401, 403].includes(error.status))
          setData(null);
        setError(messageOf(error));
      }
    } finally {
      if (!controller.signal.aborted) setLoading(false);
    }
    return false;
  }, [api, path]);
  useEffect(() => {
    setData(null);
    void reload();
    return () => request.current?.abort();
  }, [reload, revision]);
  return { data, loading, error, reload };
}
export function useAdminAction(api: APIClient) {
  const controller = useMemo(() => new AdminAction(api), [api]);
  const state = useSyncExternalStore(
    controller.subscribe,
    controller.getSnapshot,
  );
  useEffect(() => () => controller.cancel(), [controller]);
  return { ...state, controller };
}
export function AdminError({
  error,
  onRetry,
}: {
  error: string;
  onRetry?: () => void;
}) {
  return error ? (
    <div className="notice notice-error" role="alert">
      <span>{error}</span>
      {onRetry ? (
        <button type="button" className="button secondary" onClick={onRetry}>
          Reload current data
        </button>
      ) : null}
    </div>
  ) : null;
}
export function AdminEmpty({ children }: { children: ReactNode }) {
  return <div className="admin-empty">{children}</div>;
}
export function AdminLoading() {
  return (
    <div className="tool-search-loading" role="status">
      <span className="spinner" />
      Loading current records…
    </div>
  );
}
export function AdminJSON({ value, label }: { value: unknown; label: string }) {
  return (
    <div className="json-block">
      <div className="json-label">{label}</div>
      <pre>{JSON.stringify(value, null, 2)}</pre>
    </div>
  );
}
export function dateLabel(value: string) {
  return new Date(value).toLocaleString();
}
export function SecretReveal({
  secret,
  onClose,
}: {
  secret: string;
  onClose: () => void;
}) {
  const [copy, setCopy] = useState("");
  return (
    <section className="panel secret-reveal" aria-label="New API key">
      <div className="panel-heading">
        <h2>Save this API key now</h2>
        <button type="button" className="button secondary" onClick={onClose}>
          Close and discard
        </button>
      </div>
      <div className="panel-body">
        <p>
          This key is shown once. Closing this panel or leaving this page
          removes it from the console.
        </p>
        <label>
          New API key
          <input type="password" readOnly autoComplete="off" value={secret} />
        </label>
        <button
          type="button"
          className="button secondary"
          onClick={() => {
            if (!navigator.clipboard) {
              setCopy(
                "Clipboard unavailable in this browser. Use a secure HTTPS connection.",
              );
              return;
            }
            void navigator.clipboard.writeText(secret).then(
              () => setCopy("Copied to clipboard."),
              () =>
                setCopy("Copy unavailable. Select and copy the key manually."),
            );
          }}
        >
          Copy key
        </button>
        <span role="status" className="field-help">
          {copy}
        </span>
      </div>
    </section>
  );
}

export function useRecordPages<T extends { id: string }>(
  api: APIClient,
  path: string,
  revision = "",
  cursorName = "cursor",
) {
  const controller = useMemo(
    () =>
      new ToolSearchController<T>(async (_query, cursor, signal) => {
        const separator = path.includes("?") ? "&" : "?";
        const page = await api.request<{
          items: T[];
          next_cursor?: string;
          total?: number;
        }>(
          `${path}${cursor ? `${separator}${cursorName}=${encodeURIComponent(cursor)}` : ""}`,
          { signal },
        );
        return { ...page, total: page.total ?? page.items.length };
      }),
    [api, path, cursorName],
  );
  const state = useSyncExternalStore(
    controller.subscribe,
    controller.getSnapshot,
  );
  useEffect(() => {
    void controller.reload();
    return () => controller.cancel();
  }, [controller, revision]);
  return { state, controller };
}
export function MoreRecords<T extends { id: string }>({
  pages,
}: {
  pages: ReturnType<typeof useRecordPages<T>>;
}) {
  return (
    <div className="tool-pagination">
      <AdminError
        error={pages.state.error}
        onRetry={() => void pages.controller.retry()}
      />
      <div className="tool-pagination-row">
        <span>{pages.state.items.length} records loaded</span>
        {pages.state.nextCursor ? (
          <button
            type="button"
            className="button secondary"
            disabled={pages.state.phase !== "idle"}
            onClick={() => void pages.controller.loadMore()}
          >
            {pages.state.phase === "loading-more" ? "Loading…" : "Load more"}
          </button>
        ) : null}
      </div>
    </div>
  );
}
