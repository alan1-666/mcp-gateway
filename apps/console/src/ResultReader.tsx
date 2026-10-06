import { useI18n } from "./i18n";
import { useEffect, useRef, useState } from "react";
import { APIError, messageOf } from "./api";
import type { APIClient } from "./api";
import { AdminError } from "./AdminUI";
import { appendResultPage } from "./result-reader";
import type { ResultPage } from "./result-reader";

export function ResultReader({
  api,
  operationID,
}: {
  api: APIClient;
  operationID: string;
}) {
  const { t, locale } = useI18n();
  const [text, setText] = useState("");
  const [page, setPage] = useState<ResultPage | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const request = useRef<AbortController | null>(null);
  useEffect(() => () => request.current?.abort(), []);
  async function read() {
    if (busy) return;
    const controller = new AbortController();
    request.current = controller;
    setBusy(true);
    setError("");
    try {
      const params = new URLSearchParams({ limit_bytes: "8192" });
      if (page?.next_cursor) params.set("cursor", page.next_cursor);
      const next = await api.request<ResultPage>(
        `/operations/${encodeURIComponent(operationID)}/result?${params}`,
        { signal: controller.signal },
      );
      if (controller.signal.aborted) return;
      const combined = appendResultPage(
        operationID,
        text,
        page?.sha256 ?? "",
        next,
      );
      setText(combined);
      setPage(next);
    } catch (failure) {
      if (controller.signal.aborted) return;
      if (
        failure instanceof APIError &&
        [401, 403, 404].includes(failure.status)
      ) {
        setText("");
        setPage(null);
      }
      setError(messageOf(failure));
    } finally {
      if (!controller.signal.aborted) setBusy(false);
    }
  }
  return (
    <section className="panel-body" aria-label={t("Read large result")}>
      <p>
        {t(
          "Large result stored with an expiry. Load only the chunks you need; each request checks current access.",
        )}{" "}
      </p>
      <AdminError error={error} />
      {!page || page.next_cursor ? (
        <button
          type="button"
          className="button secondary"
          disabled={busy}
          onClick={() => void read()}
        >
          {busy ? t("Loading…") : t("Read next 8 KiB")}
        </button>
      ) : (
        <p role="status">
          {t("Complete result loaded · {bytes} bytes", {
            bytes: page.bytes.toLocaleString(locale),
          })}
        </p>
      )}
      {text ? (
        <pre className="connection-config" tabIndex={0}>
          {text}
        </pre>
      ) : null}
    </section>
  );
}
