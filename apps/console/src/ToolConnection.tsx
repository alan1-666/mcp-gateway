import type { Tool } from "./types";

export function ToolConnection({ tool }: { tool: Tool }) {
  return tool.mcp ? <>
    <div><dt>Transport</dt><dd>MCP · Streamable HTTP</dd></div>
    <div><dt>MCP server ID</dt><dd className="mono break-word">{tool.mcp.server_id}</dd></div>
    <div><dt>Remote tool</dt><dd className="mono break-word">{tool.mcp.tool_name}</dd></div>
  </> : <>
    <div><dt>Destination</dt><dd className="mono break-word">{tool.http.method} {tool.http.url}</dd></div>
    <div><dt>Timeout</dt><dd>{tool.http.timeout_ms.toLocaleString()} ms</dd></div>
    <div><dt>Credential reference</dt><dd>{tool.http.credential_ref || "None configured"}</dd></div>
  </>;
}

export function ToolResponsePolicy({ tool }: { tool: Tool }) {
  const policy = tool.response_policy;
  if (!policy) return null;
  return <div className="mcp-response-policy">
    <h3>Response policy</h3>
    <p>{policy.include?.length ? "Only these fields are retained:" : "All result fields are retained."}</p>
    {policy.include?.length ? <ul>{policy.include.map((path) => <li className="mono" key={path}>{path}</li>)}</ul> : null}
    {policy.artifact ? <p>Large results: up to {policy.artifact.max_bytes.toLocaleString()} bytes; retained for {policy.artifact.ttl_seconds} seconds.</p> : null}
    <p>Inline maximum {policy.max_bytes.toLocaleString()} bytes. Top-level nextCursor and next_cursor are preserved when present.</p>
  </div>;
}
