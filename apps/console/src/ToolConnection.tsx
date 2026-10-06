import { useI18n } from "./i18n";
import type { Tool } from "./types";

export function ToolConnection({ tool }: { tool: Tool }) {
  const { t, locale } = useI18n();
  return tool.mcp ? (
    <>
      <div>
        <dt>{t("Transport")}</dt>
        <dd>MCP · Streamable HTTP</dd>
      </div>
      <div>
        <dt>{t("MCP server ID")}</dt>
        <dd className="mono break-word">{tool.mcp.server_id}</dd>
      </div>
      <div>
        <dt>{t("Remote tool")}</dt>
        <dd className="mono break-word">{tool.mcp.tool_name}</dd>
      </div>
    </>
  ) : (
    <>
      <div>
        <dt>{t("Destination")}</dt>
        <dd className="mono break-word">
          {tool.http.method} {tool.http.url}
        </dd>
      </div>
      <div>
        <dt>{t("Timeout")}</dt>
        <dd>{tool.http.timeout_ms.toLocaleString(locale)} ms</dd>
      </div>
      <div>
        <dt>{t("Credential reference")}</dt>
        <dd>{tool.http.credential_ref || t("None configured")}</dd>
      </div>
    </>
  );
}

export function ToolResponsePolicy({ tool }: { tool: Tool }) {
  const { t, locale } = useI18n();
  const policy = tool.response_policy;
  if (!policy) return null;
  return (
    <div className="mcp-response-policy">
      <h3>{t("Response policy")}</h3>
      <p>
        {policy.include?.length
          ? t("Only these fields are retained:")
          : t("All result fields are retained.")}
      </p>
      {policy.include?.length ? (
        <ul>
          {policy.include.map((path) => (
            <li className="mono" key={path}>
              {path}
            </li>
          ))}
        </ul>
      ) : null}
      {policy.artifact ? (
        <p>
          {t(
            "Large results: up to {bytes} bytes; retained for {seconds} seconds.",
            {
              bytes: policy.artifact.max_bytes.toLocaleString(locale),
              seconds: policy.artifact.ttl_seconds,
            },
          )}
        </p>
      ) : null}
      <p>
        {t(
          "Inline maximum {bytes} bytes. Top-level nextCursor and next_cursor are preserved when present.",
          { bytes: policy.max_bytes.toLocaleString(locale) },
        )}
      </p>
    </div>
  );
}
