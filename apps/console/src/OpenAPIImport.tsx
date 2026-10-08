import { useEffect, useRef, useState } from "react";
import type { ChangeEvent, FormEvent } from "react";
import { useI18n } from "./i18n";
import { MAX_OPENAPI_BYTES, previewOpenAPI } from "./openapi-import";
import type { OpenAPIImportOperation } from "./openapi-import";

type Candidate = NonNullable<OpenAPIImportOperation["candidate"]>;

export function OpenAPIImport({
  onSelect,
  onCancel,
}: {
  onSelect: (candidate: Candidate) => void;
  onCancel: () => void;
}) {
  const { t, locale } = useI18n();
  const [source, setSource] = useState("");
  const [server, setServer] = useState("");
  const [operations, setOperations] = useState<OpenAPIImportOperation[] | null>(
    null,
  );
  const [selected, setSelected] = useState<number | null>(null);
  const [error, setError] = useState("");
  const [reading, setReading] = useState(false);
  const generation = useRef(0);
  const mounted = useRef(true);
  const fileReader = useRef<FileReader | null>(null);

  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
      generation.current++;
      fileReader.current?.abort();
    };
  }, []);

  function invalidate() {
    generation.current++;
    fileReader.current?.abort();
    fileReader.current = null;
    setReading(false);
    setOperations(null);
    setSelected(null);
    setError("");
  }

  function loadFile(event: ChangeEvent<HTMLInputElement>) {
    const file = event.target.files?.[0];
    event.target.value = "";
    if (!file) return;
    invalidate();
    setSource("");
    if (file.size > MAX_OPENAPI_BYTES) {
      setError("OpenAPI documents must not exceed 1 MiB.");
      return;
    }
    const version = generation.current;
    const reader = new FileReader();
    fileReader.current = reader;
    setReading(true);
    reader.onload = () => {
      if (!mounted.current || version !== generation.current) return;
      fileReader.current = null;
      setReading(false);
      if (typeof reader.result !== "string") {
        setError(
          "The selected file could not be read. Choose it again or paste JSON.",
        );
        return;
      }
      setSource(reader.result);
    };
    reader.onerror = () => {
      if (!mounted.current || version !== generation.current) return;
      fileReader.current = null;
      setReading(false);
      setError(
        "The selected file could not be read. Choose it again or paste JSON.",
      );
    };
    reader.readAsText(file);
  }

  function preview(event: FormEvent) {
    event.preventDefault();
    if (reading) return;
    setSelected(null);
    setOperations(null);
    setError("");
    try {
      setOperations(previewOpenAPI(source, server.trim() || undefined));
    } catch (error) {
      setError(
        error instanceof Error
          ? error.message
          : "The OpenAPI document could not be inspected.",
      );
    }
  }

  const candidate =
    selected === null ? undefined : operations?.[selected]?.candidate;
  const supported =
    operations?.filter((operation) => operation.candidate).length ?? 0;

  return (
    <section className="panel tool-form" aria-label={t("Import from OpenAPI")}>
      <div className="panel-heading">
        <div>
          <span className="eyebrow">OpenAPI</span>
          <h2>{t("Turn an API operation into a tool")}</h2>
        </div>
        <button
          className="button secondary"
          type="button"
          onClick={() => {
            invalidate();
            onCancel();
          }}
        >
          {t("Cancel import")}
        </button>
      </div>
      <div className="panel-body">
        <p>
          {t(
            "Upload or paste an OpenAPI document, inspect its operations, then review one tool draft. Preview stays in this browser and makes no network requests.",
          )}
        </p>
        <p className="field-help">
          {t(
            "Supported profile: OpenAPI 3.0 or 3.1 JSON, static paths, GET with scalar query parameters, or other supported methods with a JSON object body. Unsupported operations are listed with a reason; nothing is imported automatically.",
          )}
        </p>
        <form onSubmit={preview}>
          <div className="form-grid">
            <label className="span-two">
              {t("Upload JSON file")}
              <input
                type="file"
                accept=".json,application/json"
                onChange={loadFile}
              />
              <span className="field-help">
                {t(
                  "Maximum file size: 1 MiB. YAML and remote document URLs are not supported.",
                )}
              </span>
            </label>
            <label className="span-two">
              {t("OpenAPI JSON")}
              <textarea
                className="code-input"
                rows={12}
                spellCheck={false}
                value={source}
                onChange={(event) => {
                  invalidate();
                  setSource(event.target.value);
                }}
                placeholder={t("Paste the complete OpenAPI JSON document")}
              />
            </label>
            <label className="span-two">
              {t("Server URL override (optional)")}
              <input
                type="url"
                value={server}
                onChange={(event) => {
                  invalidate();
                  setServer(event.target.value);
                }}
                placeholder="https://api.example.com/v1"
              />
              <span className="field-help">
                {t(
                  "Use an absolute HTTP or HTTPS base URL when the document has no usable server URL. This changes only the preview destination; no connection is made.",
                )}
              </span>
            </label>
          </div>
          {error ? (
            <p className="notice notice-error" role="alert">
              {t(error)}
            </p>
          ) : null}
          <div className="action-row">
            <button
              className="button primary"
              type="submit"
              disabled={reading || !source.trim()}
            >
              {reading ? t("Reading file…") : t("Preview operations")}
            </button>
            {reading ? (
              <span role="status" className="field-help">
                {t("Reading the selected file locally…")}
              </span>
            ) : null}
          </div>
        </form>
        {operations ? (
          <section
            className="admin-subsection"
            aria-label={t("Operation preview")}
          >
            <h3>{t("Choose one operation")}</h3>
            <p className="field-help" role="status">
              {t("{supported} supported · {unsupported} unsupported", {
                supported: supported.toLocaleString(locale),
                unsupported: (operations.length - supported).toLocaleString(
                  locale,
                ),
              })}
            </p>
            {!operations.length ? (
              <p>{t("No operations were found in this document.")}</p>
            ) : null}
            <div
              className="mcp-remote-list"
              aria-label={t("Document operations")}
            >
              {operations.map((operation, index) => (
                <div key={operation.id}>
                  <button
                    type="button"
                    className={`mcp-remote-tool ${selected === index ? "selected" : ""}`}
                    disabled={!operation.candidate}
                    aria-pressed={selected === index}
                    onClick={() => setSelected(index)}
                  >
                    <strong>{operation.name}</strong>
                    <span className="mono break-word">
                      {operation.method} {operation.path}
                    </span>
                    <small>
                      {operation.candidate ? t("Supported") : t("Unsupported")}
                    </small>
                  </button>
                  {!operation.candidate ? (
                    <p className="field-help">
                      {t(
                        operation.reason ||
                          "This operation is outside the supported import profile.",
                      )}
                    </p>
                  ) : null}
                </div>
              ))}
            </div>
            {candidate ? (
              <section
                className="admin-subsection"
                aria-label={t("Selected operation")}
              >
                <h3>{t("Review the generated tool")}</h3>
                <dl className="metadata-grid">
                  <div>
                    <dt>{t("Tool name")}</dt>
                    <dd className="break-word">{candidate.name}</dd>
                  </div>
                  <div>
                    <dt>{t("Description")}</dt>
                    <dd className="break-word">
                      {candidate.description ||
                        t("No description provided by this document.")}
                    </dd>
                  </div>
                  <div>
                    <dt>{t("Destination")}</dt>
                    <dd className="mono break-word">
                      {candidate.method} {candidate.url}
                    </dd>
                  </div>
                  <div>
                    <dt>{t("Credentials")}</dt>
                    <dd>
                      {candidate.credentialRequired
                        ? t(
                            "The document declares authentication. Configure a credential reference when reviewing the draft.",
                          )
                        : t(
                            "No authentication declared. Verify the API requirements before publishing.",
                          )}
                    </dd>
                  </div>
                </dl>
                {candidate.warnings.length ? (
                  <div className="notice notice-warning" role="status">
                    <strong>{t("Review notes")}</strong>
                    <ul>
                      {candidate.warnings.map((warning, index) => (
                        <li key={index}>{t(warning)}</li>
                      ))}
                    </ul>
                  </div>
                ) : null}
                <details className="mcp-schema" open>
                  <summary>{t("Generated input schema")}</summary>
                  <pre tabIndex={0} aria-label={t("Generated input schema")}>
                    {JSON.stringify(candidate.inputSchema, null, 2)}
                  </pre>
                </details>
                <p className="field-help">
                  {t(
                    "Review draft opens the normal tool form. Confirm the name, destination, risk, credentials, and schema there before creating a draft. Publication and client access remain separate.",
                  )}
                </p>
                <button
                  className="button primary"
                  type="button"
                  onClick={() => onSelect(candidate)}
                >
                  {t("Review draft")}
                </button>
              </section>
            ) : null}
          </section>
        ) : null}
      </div>
    </section>
  );
}
