import { useEffect, useRef, useState } from "react";
import type { FormEvent } from "react";
import { APIClient, messageOf, parseObject } from "./api";
import {
  AdminEmpty,
  AdminError,
  AdminJSON,
  AdminLoading,
  dateLabel,
  useAdminAction,
  useResource,
} from "./AdminUI";
import type { MCPServer, Tool } from "./types";
interface Version {
  version: number;
  definition: Record<string, unknown>;
  created_at: string;
}
interface Candidate {
  id: string;
  tool_id: string;
  base_version: number;
  definition: Record<string, unknown>;
  changes: { field: string; before: unknown; after: unknown }[];
  reason: string;
  created_at: string;
  source_version?: number;
  published_version?: number;
}
function definition(tool: Tool) {
  return {
    name: tool.name,
    description: tool.description,
    risk: tool.risk,
    input_schema: tool.input_schema,
    output_schema: tool.output_schema,
    http: tool.http,
    mcp: tool.mcp,
    response_policy: tool.response_policy,
  };
}
export function ToolVersions({
  api,
  tool,
  onChanged,
}: {
  api: APIClient;
  tool: Tool;
  onChanged: () => Promise<void>;
}) {
  const prefix = `/tools/${tool.id}`;
  const versions = useResource<{ items: Version[]; next_cursor?: number }>(
      api,
      `${prefix}/versions`,
      String(tool.version),
    ),
    candidates = useResource<{ items: Candidate[] }>(
      api,
      `${prefix}/candidates`,
      String(tool.version),
    );
  const upstreams = useResource<{ items: MCPServer[] }>(api, "/mcp/servers");
  const [serverID, setServerID] = useState(tool.mcp?.server_id ?? "");
  const [toolName, setToolName] = useState(tool.mcp?.tool_name ?? "");
  const action = useAdminAction(api),
    [history, setHistory] = useState<Version[]>([]),
    [cursor, setCursor] = useState<number | undefined>(),
    [loadingMore, setLoadingMore] = useState(false),
    [historyError, setHistoryError] = useState("");
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);
  useEffect(() => {
    setHistory(versions.data?.items ?? []);
    setCursor(versions.data?.next_cursor);
  }, [versions.data]);
  const [reason, setReason] = useState(""),
    [json, setJSON] = useState(() => JSON.stringify(definition(tool), null, 2)),
    [risk, setRisk] = useState(tool.risk),
    [policy, setPolicy] = useState(() =>
      JSON.stringify(tool.response_policy ?? { max_bytes: 65536 }, null, 2),
    ),
    [selected, setSelected] = useState<Candidate | null>(null),
    [error, setError] = useState(""),
    [retiring, setRetiring] = useState(false);
  async function reload() {
    const results = await Promise.all([
      versions.reload(),
      candidates.reload(),
      onChanged(),
    ]);
    if (results[0] && results[1]) {
      action.controller.reset();
      setSelected(null);
    }
  }
  async function more() {
    if (loadingMore || !cursor) return;
    setLoadingMore(true);
    setHistoryError("");
    try {
      const page = await api.request<{
        items: Version[];
        next_cursor?: number;
      }>(`${prefix}/versions?before=${cursor}`);
      if (mounted.current) {
        setHistory((old) => [
          ...new Map(
            [...old, ...page.items].map((item) => [item.version, item]),
          ).values(),
        ]);
        setCursor(page.next_cursor);
      }
    } catch (error) {
      if (mounted.current) setHistoryError(messageOf(error));
    } finally {
      if (mounted.current) setLoadingMore(false);
    }
  }
  async function create(source?: number) {
    setError("");
    try {
      if (!reason.trim())
        throw new Error("Provide a reason before creating a candidate.");
      const body = {
        expected_version: tool.version,
        reason,
        ...(source
          ? { source_version: source }
          : tool.mcp
            ? {
                risk,
                server_id: serverID,
                tool_name: toolName,
                response_policy: parseObject(policy, "Response policy"),
              }
            : { definition: parseObject(json, "Tool definition") }),
      };
      const value = await action.controller.run<Candidate>(
        `${prefix}/candidates`,
        body,
      );
      if (value) {
        setSelected(value);
        await candidates.reload();
      }
    } catch (error) {
      setError(messageOf(error));
    }
  }
  async function publish() {
    if (!selected) return;
    const value = await action.controller.run<Tool>(
      `${prefix}/candidates/${selected.id}/publish`,
      { expected_version: selected.base_version },
    );
    if (value) {
      setSelected(null);
      await reload();
    }
  }
  return (
    <section className="panel detail-panel">
      <div className="panel-heading">
        <div>
          <span className="eyebrow">REVIEWED RELEASES</span>
          <h2>Versions & candidates</h2>
        </div>
        <span className="version-label">
          Current v{tool.version} · {tool.status}
        </span>
      </div>
      <div className="panel-body">
        <AdminError
          error={error || action.error || versions.error || candidates.error}
          onRetry={() => void reload()}
        />
        <p className="field-help">
          Publishing creates a new immutable definition. Prepared operations
          retain their original snapshot. Rollback creates a candidate for
          review.
        </p>
        <form
          onSubmit={(event: FormEvent) => {
            event.preventDefault();
            void create();
          }}
        >
          <fieldset disabled={action.busy || action.needsReload}>
            <label>
              Change reason
              <textarea
                required
                rows={2}
                maxLength={1000}
                value={reason}
                onChange={(event) => setReason(event.target.value)}
              />
            </label>
            {tool.mcp ? (
              <div className="form-grid">
                <label>
                  Upstream server
                  <select
                    value={serverID}
                    onChange={(event) => setServerID(event.target.value)}
                  >
                    {upstreams.data?.items.map((server) => (
                      <option key={server.id} value={server.id}>
                        {server.name}
                        {server.enabled ? "" : " (disabled)"}
                      </option>
                    ))}
                  </select>
                  <AdminError
                    error={upstreams.error}
                    onRetry={() => void upstreams.reload()}
                  />
                </label>
                <label>
                  Upstream tool name
                  <input
                    required
                    value={toolName}
                    onChange={(event) => setToolName(event.target.value)}
                  />
                </label>
                <label>
                  Risk for refreshed upstream contract
                  <select
                    value={risk}
                    onChange={(event) =>
                      setRisk(event.target.value as Tool["risk"])
                    }
                  >
                    <option value="read">Read</option>
                    <option value="write">Write — approval required</option>
                  </select>
                </label>
                <label>
                  Response policy
                  <textarea
                    rows={5}
                    className="code-input"
                    value={policy}
                    onChange={(event) => setPolicy(event.target.value)}
                  />
                </label>
              </div>
            ) : (
              <label>
                Proposed HTTP definition
                <textarea
                  rows={12}
                  className="code-input"
                  value={json}
                  onChange={(event) => setJSON(event.target.value)}
                />
              </label>
            )}
            <button className="button primary">
              {action.busy
                ? "Working…"
                : tool.mcp
                  ? "Probe upstream and create candidate"
                  : "Create candidate for review"}
            </button>
          </fieldset>
        </form>
        <div className="admin-subsection">
          <h3>Saved candidates</h3>
          {candidates.loading ? (
            <AdminLoading />
          ) : candidates.data?.items.length ? (
            <div className="admin-record-list">
              {candidates.data.items.map((item) => (
                <button
                  type="button"
                  className="admin-record"
                  key={item.id}
                  disabled={action.busy}
                  onClick={() => setSelected(item)}
                >
                  <strong>{item.reason}</strong>
                  <span>
                    Based on v{item.base_version} ·{" "}
                    {item.published_version
                      ? `Published as v${item.published_version}`
                      : "Awaiting review"}
                  </span>
                </button>
              ))}
            </div>
          ) : (
            <AdminEmpty>No candidates yet.</AdminEmpty>
          )}
        </div>
        {selected ? (
          <section className="candidate-preview">
            <h3>Review candidate changes</h3>
            <p>{selected.reason}</p>
            <AdminJSON label="Field changes" value={selected.changes} />
            <details>
              <summary>Complete proposed definition</summary>
              <AdminJSON
                label="Proposed contract"
                value={selected.definition}
              />
            </details>
            {selected.base_version !== tool.version &&
            !selected.published_version ? (
              <div className="notice notice-warning">
                This candidate is stale. Create a new candidate from the current
                version.
              </div>
            ) : null}
            {!selected.published_version ? (
              <div className="action-row">
                <button
                  type="button"
                  className="button primary"
                  disabled={
                    action.busy ||
                    action.needsReload ||
                    selected.base_version !== tool.version
                  }
                  onClick={() => void publish()}
                >
                  Publish reviewed candidate
                </button>
                <button
                  type="button"
                  className="button danger"
                  disabled={action.busy || action.needsReload}
                  onClick={() =>
                    void action.controller
                      .run(`${prefix}/candidates/${selected.id}/discard`, {})
                      .then(async (result) => {
                        if (result) {
                          setSelected(null);
                          await candidates.reload();
                        }
                      })
                  }
                >
                  Discard candidate
                </button>
              </div>
            ) : null}
          </section>
        ) : null}
        <div className="admin-subsection">
          <h3>Version history</h3>
          {versions.loading ? (
            <AdminLoading />
          ) : history.length ? (
            history.map((version) => (
              <details key={version.version} className="admin-record">
                <summary>
                  Version {version.version} · {dateLabel(version.created_at)}
                </summary>
                <AdminJSON
                  label="Immutable definition"
                  value={version.definition}
                />
                <button
                  type="button"
                  className="button secondary"
                  disabled={action.busy || action.needsReload || !reason.trim()}
                  onClick={() => void create(version.version)}
                >
                  Create rollback candidate from v{version.version}
                </button>
              </details>
            ))
          ) : (
            <AdminEmpty>No version history available.</AdminEmpty>
          )}
          <AdminError error={historyError} onRetry={() => void more()} />
          {cursor ? (
            <button
              type="button"
              className="button secondary"
              disabled={loadingMore}
              onClick={() => void more()}
            >
              {loadingMore ? "Loading…" : "Load older versions"}
            </button>
          ) : null}
        </div>
        {tool.status !== "retired" ? (
          <div className="admin-subsection">
            <button
              type="button"
              className="button danger"
              disabled={action.busy}
              onClick={() => setRetiring(!retiring)}
            >
              Retire tool
            </button>
            {retiring ? (
              <div className="notice notice-warning">
                <p>
                  Retirement removes this tool from discovery and stops new
                  dispatches. Restoration requires publishing a reviewed
                  candidate.
                </p>
                <button
                  type="button"
                  className="button danger"
                  disabled={action.busy || action.needsReload}
                  onClick={() =>
                    void action.controller
                      .run(`${prefix}/retire`, {
                        expected_version: tool.version,
                      })
                      .then(async (result) => {
                        if (result) {
                          setRetiring(false);
                          await reload();
                        }
                      })
                  }
                >
                  Confirm retirement
                </button>
              </div>
            ) : null}
          </div>
        ) : (
          <p className="notice notice-info">
            This tool is retired. A reviewed candidate can restore it.
          </p>
        )}
      </div>
    </section>
  );
}
