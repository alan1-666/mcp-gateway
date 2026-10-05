import { CatalogSchedule } from "./CatalogSchedule";
import { SectionTabs } from "./SectionTabs";
import { MCPDiagnostics } from "./MCPDiagnostics";
import { CatalogHistory, CatalogRefresh, CatalogSummary } from "./MCPCatalog";
import { catalogLabels } from "./mcp-catalog";
import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  useSyncExternalStore,
} from "react";
import type { FormEvent } from "react";
import { APIClient, messageOf } from "./api";
import {
  canManageMCPServers,
  MCPDiscoveryController,
  parseResponsePolicy,
  parseServerDraft,
} from "./mcp-servers";
import type { MCPServerDraft } from "./mcp-servers";
import type { Identity, MCPServer, RemoteTool, Tool } from "./types";

function ErrorNotice({ error }: { error: string }) {
  return error ? (
    <div className="notice notice-error" role="alert">
      {error}
    </div>
  ) : null;
}

export function MCPServers({
  api,
  identity,
  refreshVersion,
  onRegistry,
  onImported,
}: {
  api: APIClient;
  identity: Identity;
  refreshVersion: string;
  onRegistry: (toolID: string, section?: "contract" | "versions") => void;
  onImported: () => void;
}) {
  const [servers, setServers] = useState<MCPServer[]>([]);
  const [loaded, setLoaded] = useState(false);
  const [loading, setLoading] = useState(false);
  const [showForm, setShowForm] = useState(false);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState("");
  const [reviewName, setReviewName] = useState("");
  const [changesOnly, setChangesOnly] = useState(false);
  const [tab, setTab] = useState("tools");
  const loadRequest = useRef<AbortController | null>(null);
  const mutationPending = useRef(false);
  const mounted = useRef(true);
  const controller = useMemo(
    () => new MCPDiscoveryController(api, identity),
    [api, identity],
  );
  const discovery = useSyncExternalStore(
    controller.subscribe,
    controller.getSnapshot,
  );
  const canManage = canManageMCPServers(identity);
  const selected =
    servers.find((server) => server.id === discovery.server?.id) ?? null;
  const remote = discovery.items.find((tool) => tool.name === reviewName);
  const comparison = useMemo(
    () =>
      new Map(
        discovery.review?.items.map((entry) => [entry.name, entry]) ?? [],
      ),
    [discovery.review],
  );
  const change = comparison.get(reviewName);
  const hasContractChange =
    change && ["schema_changed", "description_changed"].includes(change.status);
  const visibleTools = changesOnly
    ? discovery.items.filter(
        (tool) => comparison.get(tool.name)?.status !== "unchanged",
      )
    : discovery.items;
  const locked = !!busy || !!discovery.importing;

  const load = useCallback(async () => {
    if (!canManage) return;
    loadRequest.current?.abort();
    const request = new AbortController();
    loadRequest.current = request;
    setLoading(true);
    setError("");
    try {
      const result = await api.request<{ items: MCPServer[]; total: number }>(
        "/mcp/servers",
        { signal: request.signal },
      );
      if (request.signal.aborted) return;
      if (
        !Array.isArray(result.items) ||
        !Number.isSafeInteger(result.total) ||
        result.items.some((server) => !server?.id)
      )
        throw new Error(
          "The gateway returned an invalid server list. Reload servers to try again.",
        );
      setServers(result.items);
      setLoaded(true);
      const current = controller.getSnapshot().server;
      if (current) {
        const updated = result.items.find((server) => server.id === current.id);
        if (
          !updated ||
          updated.enabled !== current.enabled ||
          updated.updated_at !== current.updated_at
        ) {
          controller.select(updated ?? null);
          setReviewName("");
        }
      }
    } catch (error) {
      if (request.signal.aborted) return;
      // Revoked access must also clear previously loaded administrator data.
      if (
        error instanceof Error &&
        "status" in error &&
        (error.status === 401 || error.status === 403)
      ) {
        setServers([]);
        setLoaded(false);
        controller.select(null);
      }
      setError(messageOf(error));
    } finally {
      if (!request.signal.aborted) setLoading(false);
    }
  }, [api, canManage, controller]);

  useEffect(() => {
    mounted.current = true;
    void load();
    return () => {
      mounted.current = false;
      loadRequest.current?.abort();
      controller.cancel();
    };
  }, [load, controller, refreshVersion]);

  async function toggle(server: MCPServer) {
    if (
      mutationPending.current ||
      controller.getSnapshot().importing ||
      !canManage
    )
      return;
    mutationPending.current = true;
    setBusy(server.id);
    setError("");
    // Cancel discovery immediately so a late response cannot re-enable review.
    if (controller.getSnapshot().server?.id === server.id) {
      controller.select({ ...server, enabled: false });
      setReviewName("");
    }
    try {
      const updated = await api.request<MCPServer>(
        `/mcp/servers/${encodeURIComponent(server.id)}/enabled`,
        { method: "POST", body: { enabled: !server.enabled } },
      );
      if (!mounted.current) return;
      setServers((current) =>
        current.map((item) => (item.id === updated.id ? updated : item)),
      );
      if (controller.getSnapshot().server?.id === server.id)
        controller.select(updated);
    } catch (error) {
      if (mounted.current) {
        await load();
        if (mounted.current)
          setError(
            `${messageOf(error)} The server list has been refreshed; check its current state before trying again.`,
          );
      }
    } finally {
      mutationPending.current = false;
      if (mounted.current) setBusy("");
    }
  }

  function select(server: MCPServer) {
    if (locked) return;
    controller.select(server);
    setReviewName("");
    setChangesOnly(false);
    setTab("tools");
  }
  function discover() {
    setTab("tools");
    setReviewName("");
    void controller.discover();
  }

  if (!canManage) return null;
  return (
    <div className="mcp-workspace">
      <div className="toolbar">
        <p className="muted mcp-toolbar-copy">
          {loaded
            ? `${servers.length} ${servers.length === 1 ? "connection" : "connections"}`
            : "Connections"}{" "}
          · Streamable HTTP
        </p>
        <button
          className="button primary"
          onClick={() => setShowForm((current) => !current)}
          disabled={locked}
        >
          {showForm ? "Close form" : "Add MCP server"}
        </button>
      </div>
      <ErrorNotice error={error} />
      {error ? (
        <button
          className="button secondary mcp-retry"
          disabled={loading || locked}
          onClick={() => void load()}
        >
          Reload servers
        </button>
      ) : null}
      {showForm ? (
        <ServerForm
          api={api}
          onCancel={() => setShowForm(false)}
          onCreated={(server) => {
            loadRequest.current?.abort();
            setLoading(false);
            setLoaded(true);
            setServers((current) => [
              server,
              ...current.filter((item) => item.id !== server.id),
            ]);
            controller.select(server);
            setReviewName("");
            setShowForm(false);
          }}
        />
      ) : null}
      <div className="mcp-layout">
        <section className="panel mcp-server-list" aria-label="MCP servers">
          <div className="panel-heading">
            <h2>
              Servers{" "}
              <span className="count-label">
                {loaded ? servers.length : "—"}
              </span>
            </h2>
          </div>
          {!loaded ? (
            <div className="panel-body" role="status">
              {loading
                ? "Loading MCP servers…"
                : "Reload servers to load your connections."}
            </div>
          ) : !servers.length ? (
            <div className="panel-body">
              <h3>No servers connected</h3>
              <p>
                Add a server’s Streamable HTTP URL to discover the tools it
                offers.
              </p>
            </div>
          ) : (
            <div className="mcp-server-items">
              {servers.map((server) => (
                <div
                  className={`mcp-server-item ${selected?.id === server.id ? "selected" : ""}`}
                  key={server.id}
                >
                  <button
                    className="mcp-server-select"
                    disabled={locked}
                    aria-pressed={selected?.id === server.id}
                    onClick={() => select(server)}
                  >
                    <strong>{server.name}</strong>
                    <span className="mono">{server.namespace}</span>
                  </button>
                  <div className="mcp-server-actions">
                    <span
                      className={`status status-${server.enabled ? "published" : "disabled"}`}
                    >
                      <span />
                      {server.enabled ? "enabled" : "disabled"}
                    </span>
                  </div>
                </div>
              ))}
            </div>
          )}
        </section>
        <section
          className="panel mcp-discovery"
          aria-label="MCP tool discovery"
        >
          {selected ? (
            <>
              <div className="panel-heading">
                <div>
                  <span className="eyebrow">{selected.namespace}</span>
                  <h2>{selected.name}</h2>
                </div>
                <button
                  className="button primary"
                  disabled={!selected.enabled || locked || discovery.loading}
                  onClick={discover}
                >
                  {discovery.loading ? "Discovering…" : "Discover tools"}
                </button>
              </div>
              <SectionTabs
                key={selected.id}
                label="Server details"
                value={tab}
                onChange={setTab}
                tabs={[
                  {
                    id: "tools",
                    label: "Tools",
                    content: (
                      <>
                        <div className="panel-body">
                          {!selected.enabled ? (
                            <div
                              className="notice notice-warning"
                              role="status"
                            >
                              This server is disabled. Enable it to discover,
                              import, or execute its tools.
                            </div>
                          ) : null}
                          <ErrorNotice error={discovery.error} />
                          {discovery.error ? (
                            <button
                              className="button secondary"
                              disabled={
                                !selected.enabled || locked || discovery.loading
                              }
                              onClick={discover}
                            >
                              Retry discovery
                            </button>
                          ) : null}
                          {discovery.loading ? (
                            <p role="status">
                              Reading the server’s tool contracts…
                            </p>
                          ) : discovery.loaded ? (
                            <>
                              {discovery.review ? (
                                <>
                                  <CatalogSummary review={discovery.review} />
                                  <label className="catalog-filter">
                                    <input
                                      type="checkbox"
                                      checked={changesOnly}
                                      onChange={(event) =>
                                        setChangesOnly(event.target.checked)
                                      }
                                    />
                                    Only tools needing review
                                  </label>
                                </>
                              ) : null}
                              <p
                                className="mcp-discovery-summary"
                                role="status"
                              >
                                {discovery.total} tools discovered · Select one
                                to review before importing.
                              </p>
                              {visibleTools.length ? (
                                <div
                                  className="mcp-remote-list"
                                  aria-label="Discovered tools"
                                >
                                  {visibleTools.map((tool) => (
                                    <button
                                      key={tool.name}
                                      className={`mcp-remote-tool ${reviewName === tool.name ? "selected" : ""}`}
                                      disabled={locked}
                                      aria-pressed={reviewName === tool.name}
                                      onClick={() => setReviewName(tool.name)}
                                    >
                                      <span>
                                        <strong>{tool.name}</strong>
                                        <small className="mono">
                                          {tool.gateway_name}
                                        </small>
                                      </span>
                                      <span className="mcp-remote-state">
                                        {comparison.has(tool.name)
                                          ? catalogLabels[
                                              comparison.get(tool.name)!.status
                                            ]
                                          : tool.imported_tool_id
                                            ? "Imported"
                                            : "Review →"}
                                      </span>
                                    </button>
                                  ))}
                                </div>
                              ) : (
                                <p>
                                  {discovery.items.length
                                    ? "No available tools need review."
                                    : "This server currently exposes no tools."}
                                </p>
                              )}
                              {discovery.review?.counts.missing ? (
                                <section className="catalog-missing">
                                  <h3>Missing from the upstream catalog</h3>
                                  <p className="field-help">
                                    These registered tools were absent from this
                                    complete discovery. Review their
                                    dependencies and retire them in the registry
                                    if appropriate.
                                  </p>
                                  {discovery.review.items
                                    .filter(
                                      (entry) => entry.status === "missing",
                                    )
                                    .map((entry) => (
                                      <div
                                        className="admin-record"
                                        key={entry.name}
                                      >
                                        <strong className="break-word">
                                          {entry.name}
                                        </strong>
                                        <span>
                                          v{entry.imported_version} ·{" "}
                                          {entry.imported_status}
                                          {entry.imported_enabled
                                            ? ""
                                            : " · disabled"}
                                        </span>
                                        <button
                                          type="button"
                                          className="text-button"
                                          onClick={() =>
                                            onRegistry(entry.imported_tool_id!)
                                          }
                                        >
                                          Review registered tool
                                        </button>
                                      </div>
                                    ))}
                                </section>
                              ) : null}
                            </>
                          ) : !discovery.error && !discovery.loading ? (
                            <p className="muted">
                              Discover the available tools, then review each
                              contract and its risk classification.
                            </p>
                          ) : null}
                        </div>
                        {remote && !hasContractChange ? (
                          <ToolReview
                            key={`${selected.id}:${remote.name}:${remote.schema_hash}:${discovery.revision}`}
                            remote={remote}
                            disabled={
                              !selected.enabled || locked || discovery.loading
                            }
                            importing={discovery.importing === remote.name}
                            mustRediscover={discovery.requiresDiscovery.includes(
                              remote.name,
                            )}
                            error={discovery.importError}
                            onRegistry={onRegistry}
                            onImport={async (risk, include, maxBytes) => {
                              const tool = await controller.importTool(
                                remote,
                                risk,
                                parseResponsePolicy(include, maxBytes),
                              );
                              if (tool) onImported();
                            }}
                          />
                        ) : null}
                        {remote &&
                        change &&
                        ["schema_changed", "description_changed"].includes(
                          change.status,
                        ) ? (
                          <CatalogRefresh
                            key={`${selected.id}:${remote.name}:${discovery.revision}`}
                            api={api}
                            entry={change}
                            disabled={
                              !selected.enabled || locked || discovery.loading
                            }
                            onRegistry={(id) => onRegistry(id, "versions")}
                          />
                        ) : null}
                      </>
                    ),
                  },
                  {
                    id: "activity",
                    label: "Activity",
                    content: (
                      <div className="panel-body">
                        {" "}
                        <MCPDiagnostics
                          expanded
                          key={selected.id}
                          api={api}
                          serverID={selected.id}
                        />
                        <CatalogHistory
                          expanded
                          key={`catalog:${selected.id}`}
                          api={api}
                          serverID={selected.id}
                          revision={String(discovery.revision)}
                        />
                      </div>
                    ),
                  },
                  {
                    id: "settings",
                    label: "Settings",
                    content: (
                      <div className="panel-body">
                        {" "}
                        <dl className="metadata-grid mcp-server-metadata">
                          <div>
                            <dt>Namespace</dt>
                            <dd className="mono">{selected.namespace}</dd>
                          </div>
                          <div>
                            <dt>Timeout</dt>
                            <dd>{selected.timeout_ms.toLocaleString()} ms</dd>
                          </div>
                          <div>
                            <dt>Server URL</dt>
                            <dd className="mono break-word">{selected.url}</dd>
                          </div>
                          <div>
                            <dt>Credential reference</dt>
                            <dd className="mono break-word">
                              {selected.credential_ref || "None configured"}
                            </dd>
                          </div>
                        </dl>
                        <CatalogSchedule
                          key={selected.id}
                          api={api}
                          serverID={selected.id}
                          serverEnabled={selected.enabled}
                        />
                        <div className="server-control">
                          <div>
                            <h3>Server access</h3>
                            <p className="field-help">
                              Disabling blocks discovery, imports and calls to
                              this server.
                            </p>
                          </div>
                          <button
                            className="button secondary"
                            disabled={locked}
                            onClick={() => void toggle(selected)}
                          >
                            {busy === selected.id
                              ? "Updating…"
                              : selected.enabled
                                ? "Disable server"
                                : "Enable server"}
                          </button>
                        </div>
                      </div>
                    ),
                  },
                ]}
              />
            </>
          ) : (
            <div className="empty">
              <h3>Review what a server exposes</h3>
              <p>
                Select a connection to discover its tool names, schemas, and
                import status.
              </p>
            </div>
          )}
        </section>
      </div>
    </div>
  );
}

function ServerForm({
  api,
  onCreated,
  onCancel,
}: {
  api: APIClient;
  onCreated: (server: MCPServer) => void;
  onCancel: () => void;
}) {
  const [draft, setDraft] = useState<MCPServerDraft>({
    name: "",
    namespace: "",
    url: "",
    credential: "",
    timeout: "10000",
  });
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const pending = useRef(false);
  const field = (key: keyof MCPServerDraft, value: string) =>
    setDraft((current) => ({ ...current, [key]: value }));
  async function submit(event: FormEvent) {
    event.preventDefault();
    if (pending.current) return;
    pending.current = true;
    setBusy(true);
    setError("");
    try {
      const body = parseServerDraft(draft);
      const server = await api.request<MCPServer>("/mcp/servers", {
        method: "POST",
        body,
      });
      onCreated(server);
    } catch (error) {
      setError(messageOf(error));
    } finally {
      pending.current = false;
      setBusy(false);
    }
  }
  return (
    <form className="panel tool-form" onSubmit={submit}>
      <div className="panel-heading">
        <div>
          <span className="eyebrow">NEW CONNECTION</span>
          <h2>Add an MCP server</h2>
        </div>
        <span className="method-tag">MCP</span>
      </div>
      <div className="panel-body">
        <ErrorNotice error={error} />
        <fieldset disabled={busy}>
          <div className="form-grid">
            <label>
              Server name
              <input
                required
                maxLength={120}
                autoComplete="off"
                value={draft.name}
                onChange={(event) => field("name", event.target.value)}
                placeholder="Order operations"
              />
            </label>
            <label>
              Namespace
              <input
                required
                maxLength={24}
                autoCapitalize="none"
                autoComplete="off"
                spellCheck={false}
                value={draft.namespace}
                onChange={(event) => field("namespace", event.target.value)}
                placeholder="orders"
              />
              <span className="field-help">
                Unique in this workspace. Used in every imported tool’s gateway
                name.
              </span>
            </label>
            <label className="span-two">
              Server URL
              <input
                required
                type="url"
                autoComplete="off"
                value={draft.url}
                onChange={(event) => field("url", event.target.value)}
                placeholder="https://tools.example.com/mcp"
              />
              <span className="field-help">
                Use the server’s Streamable HTTP endpoint. No query string or
                credentials.
              </span>
            </label>
            <label>
              Credential reference <span className="optional">optional</span>
              <input
                autoComplete="off"
                spellCheck={false}
                value={draft.credential}
                onChange={(event) => field("credential", event.target.value)}
                placeholder="ORDERS_ACCESS_TOKEN"
              />
              <span className="field-help">
                Reference an operator-configured credential. Never paste a
                secret.
              </span>
            </label>
            <label>
              Timeout (milliseconds)
              <input
                required
                type="number"
                min={100}
                max={120000}
                step={1}
                value={draft.timeout}
                onChange={(event) => field("timeout", event.target.value)}
              />
            </label>
          </div>
        </fieldset>
        <div className="action-row">
          <button className="button primary" disabled={busy}>
            {busy ? "Adding server…" : "Add server"}
          </button>
          <button
            type="button"
            className="button secondary"
            disabled={busy}
            onClick={onCancel}
          >
            Cancel
          </button>
        </div>
      </div>
    </form>
  );
}

function ToolReview({
  remote,
  disabled,
  importing,
  mustRediscover,
  error,
  onImport,
  onRegistry,
}: {
  remote: RemoteTool;
  disabled: boolean;
  importing: boolean;
  mustRediscover: boolean;
  error: string;
  onImport: (
    risk: Tool["risk"],
    include: string,
    maxBytes: string,
  ) => Promise<void>;
  onRegistry: (toolID: string) => void;
}) {
  // Remote annotations are untrusted hints. Every fresh review starts as write.
  const [risk, setRisk] = useState<Tool["risk"]>("write");
  const [include, setInclude] = useState("");
  const [maxBytes, setMaxBytes] = useState("65536");
  const [validation, setValidation] = useState("");
  const pending = useRef(false);
  async function submit(event: FormEvent) {
    event.preventDefault();
    if (
      pending.current ||
      disabled ||
      remote.imported_tool_id ||
      mustRediscover
    )
      return;
    pending.current = true;
    setValidation("");
    try {
      await onImport(risk, include, maxBytes);
    } catch (error) {
      setValidation(messageOf(error));
    } finally {
      pending.current = false;
    }
  }
  return (
    <form className="mcp-tool-review" onSubmit={submit}>
      <div className="panel-heading">
        <div>
          <span className="eyebrow">REVIEW TOOL CONTRACT</span>
          <h2>{remote.name}</h2>
        </div>
        <span className="method-tag">MCP</span>
      </div>
      <div className="panel-body">
        <p className="break-word">
          {remote.description || "No description provided by this server."}
        </p>
        <div className="mcp-alias">
          <span>Gateway name</span>
          <strong className="mono">{remote.gateway_name}</strong>
        </div>
        <div className="mcp-schema-grid">
          <Schema value={remote.input_schema} label="Input schema" />
          {remote.output_schema ? (
            <Schema value={remote.output_schema} label="Output schema" />
          ) : null}
        </div>
        <p className="field-help">
          Server annotation:{" "}
          {remote.read_only_hint === true
            ? "read-only"
            : remote.read_only_hint === false
              ? "may change external state"
              : "not provided"}
          . Review the tool’s behavior before choosing its risk classification.
        </p>
        {remote.imported_tool_id ? (
          <div className="notice notice-info" role="status">
            <span>
              Already imported into the tool registry. Review its draft or
              publication status there.
            </span>
            <div className="action-row">
              <button
                type="button"
                className="button secondary"
                onClick={() => onRegistry(remote.imported_tool_id!)}
              >
                Open tool registry
              </button>
            </div>
          </div>
        ) : (
          <>
            <ErrorNotice error={validation || error} />
            <fieldset disabled={disabled || mustRediscover}>
              <div className="form-grid">
                <label className="span-two">
                  Risk classification
                  <select
                    value={risk}
                    onChange={(event) =>
                      setRisk(event.target.value as Tool["risk"])
                    }
                  >
                    <option value="write">
                      Write — requires independent approval
                    </option>
                    <option value="read">
                      Read — I confirm no external state changes
                    </option>
                  </select>
                </label>
                <label className="span-two">
                  Response fields to keep{" "}
                  <span className="optional">optional</span>
                  <textarea
                    className="code-input"
                    rows={4}
                    spellCheck={false}
                    value={include}
                    onChange={(event) => setInclude(event.target.value)}
                    placeholder={
                      "/results/*/title\n/results/*/url\n/next_cursor"
                    }
                  />
                  <span className="field-help">
                    One field path per line, up to 32. Leave blank to keep all
                    fields. Use /results/*/title to retain title from every
                    array element, or /results to keep the whole array. Numeric
                    segments refer to object keys, never array indices.
                  </span>
                </label>
                <label className="span-two">
                  Maximum response size (bytes)
                  <input
                    required
                    type="number"
                    min={1024}
                    max={131072}
                    step={1}
                    value={maxBytes}
                    onChange={(event) => setMaxBytes(event.target.value)}
                  />
                  <span className="field-help">
                    Default 65,536 bytes. Responses beyond this limit are
                    rejected.
                  </span>
                </label>
              </div>
              <p className="field-help">
                Array selection preserves order and element count. If any
                element lacks a selected field, the result is rejected.
                Top-level nextCursor and next_cursor are preserved when present;
                include other pagination fields explicitly. Text is rebuilt from
                the retained result.
              </p>
            </fieldset>
            <div className="action-row">
              <button
                className="button primary"
                disabled={disabled || mustRediscover}
              >
                {importing ? "Importing…" : "Import draft tool"}
              </button>
              <span className="field-help">
                {mustRediscover
                  ? "Discover tools again to check the previous import."
                  : "Review and publish the draft in the tool registry."}
              </span>
            </div>
          </>
        )}
      </div>
    </form>
  );
}

function Schema({ value, label }: { value: unknown; label: string }) {
  return (
    <details className="mcp-schema">
      <summary>{label}</summary>
      <pre tabIndex={0} aria-label={label}>
        {JSON.stringify(value, null, 2)}
      </pre>
    </details>
  );
}
