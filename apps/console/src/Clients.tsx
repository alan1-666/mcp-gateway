import { useState } from "react";
import type { FormEvent } from "react";
import { APIClient, messageOf } from "./api";
import {
  AdminEmpty,
  AdminError,
  AdminLoading,
  SecretReveal,
  dateLabel,
  useAdminAction,
  useResource,
} from "./AdminUI";
import { clientScopes, isoDate, toggleGrant } from "./admin-state";
import { useToolSearch } from "./useToolSearch";
import type { MCPServer, Tool } from "./types";
interface Client {
  id: string;
  name: string;
  enabled: boolean;
  version: number;
  scopes: string[];
  server_ids: string[];
  tool_ids: string[];
  key_id: string;
  key_created_at: string;
  key_expires_at: string;
  created_at: string;
}
interface Issued {
  client: Client;
  api_key: string;
}
export function Clients({
  api,
  refreshVersion,
}: {
  api: APIClient;
  refreshVersion: string;
}) {
  const resource = useResource<{ items: Client[] }>(
      api,
      "/clients",
      refreshVersion,
    ),
    action = useAdminAction(api);
  const [selected, setSelected] = useState<string | null>(null),
    [creating, setCreating] = useState(false),
    [secret, setSecret] = useState("");
  const [rotation, setRotation] = useState<Client | null>(null);
  const [rotationExpiry, setRotationExpiry] = useState("");
  const [rotationError, setRotationError] = useState("");
  const current = resource.data?.items.find((item) => item.id === selected);
  async function reload() {
    if (await resource.reload()) {
      action.controller.reset();
      setRotation(null);
    }
  }
  async function rotate(client: Client) {
    setRotationError("");
    let expires_at: string | undefined;
    try {
      expires_at = isoDate(rotationExpiry);
    } catch (error) {
      setRotationError(messageOf(error));
      return;
    }
    const result = await action.controller.run<Issued>(
      `/clients/${client.id}/rotate`,
      { expected_version: client.version, expires_at },
    );
    if (result) {
      setSecret(result.api_key);
      setRotation(null);
      setRotationExpiry("");
      await resource.reload();
    }
  }
  return (
    <div className="admin-workspace">
      <div className="toolbar">
        <p className="muted">
          Independent machine identities. Empty grants deny every tool.
        </p>
        <button
          className="button primary"
          disabled={action.busy}
          onClick={() => {
            setCreating(true);
            setSelected(null);
            setSecret("");
          }}
        >
          Create client
        </button>
      </div>
      <AdminError
        error={resource.error || action.error}
        onRetry={() => void reload()}
      />
      {rotation ? (
        <section className="panel panel-body">
          <h2>Rotate {rotation.name}</h2>
          <p>
            The current API key stops working immediately. Save the replacement
            and update the application using it.
          </p>
          <AdminError error={rotationError} />
          <label>
            Replacement key expiry
            <input
              type="datetime-local"
              value={rotationExpiry}
              onChange={(event) => setRotationExpiry(event.target.value)}
            />
            <span className="field-help">Optional; defaults to 30 days.</span>
          </label>
          <div className="action-row">
            <button
              type="button"
              className="button danger"
              disabled={action.busy || action.needsReload}
              onClick={() => void rotate(rotation)}
            >
              {action.busy ? "Rotating…" : "Replace API key"}
            </button>
            <button
              type="button"
              className="button secondary"
              disabled={action.busy}
              onClick={() => setRotation(null)}
            >
              Cancel
            </button>
          </div>
        </section>
      ) : null}
      {secret ? (
        <SecretReveal secret={secret} onClose={() => setSecret("")} />
      ) : null}
      <div className="panel">
        <div className="panel-heading">
          <h2>
            Clients{" "}
            <span className="count-label">
              {resource.data?.items.length ?? "—"}
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
                  <th>Client</th>
                  <th>Access</th>
                  <th>Key expires</th>
                  <th>Version</th>
                  <th>Manage</th>
                </tr>
              </thead>
              <tbody>
                {resource.data.items.map((item) => (
                  <tr key={item.id}>
                    <td>
                      <button
                        className="table-link"
                        onClick={() => {
                          setSelected(item.id);
                          setCreating(false);
                          setSecret("");
                        }}
                      >
                        {item.name}
                      </button>
                      <span className="table-description mono">{item.id}</span>
                    </td>
                    <td>
                      {item.enabled ? "Enabled" : "Disabled"}
                      <span className="table-description">
                        {item.tool_ids.length} tool grants ·{" "}
                        {item.server_ids.length} server grants
                      </span>
                    </td>
                    <td>{dateLabel(item.key_expires_at)}</td>
                    <td>v{item.version}</td>
                    <td>
                      <button
                        className="button secondary"
                        disabled={action.busy || action.needsReload}
                        onClick={() => {
                          setRotation(item);
                          setRotationExpiry("");
                          setSecret("");
                        }}
                      >
                        Rotate key
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : !resource.error ? (
          <AdminEmpty>
            No clients yet. Create a named client and explicitly grant its
            tools.
          </AdminEmpty>
        ) : null}
      </div>
      {creating || current ? (
        <ClientForm
          key={current ? `${current.id}:${current.version}` : "new"}
          api={api}
          initial={current}
          onClose={() => {
            setCreating(false);
            setSelected(null);
          }}
          onSaved={async (value) => {
            if ("api_key" in value) setSecret(value.api_key);
            setCreating(false);
            await resource.reload();
          }}
        />
      ) : null}
    </div>
  );
}
function ClientForm({
  api,
  initial,
  onClose,
  onSaved,
}: {
  api: APIClient;
  initial?: Client;
  onClose: () => void;
  onSaved: (value: Client | Issued) => Promise<void>;
}) {
  const [name, setName] = useState(initial?.name ?? ""),
    [enabled, setEnabled] = useState(initial?.enabled ?? true),
    [read, setRead] = useState(initial?.scopes.includes("tools:read") ?? false),
    [invoke, setInvoke] = useState(
      initial?.scopes.includes("tools:invoke") ?? false,
    ),
    [tools, setTools] = useState(initial?.tool_ids ?? []),
    [servers, setServers] = useState(initial?.server_ids ?? []),
    [expires, setExpires] = useState(""),
    [error, setError] = useState("");
  const action = useAdminAction(api),
    catalog = useToolSearch<Tool>(api, "registry", ""),
    upstreams = useResource<{ items: MCPServer[] }>(api, "/mcp/servers");
  async function submit(event: FormEvent) {
    event.preventDefault();
    setError("");
    try {
      const body = {
        name,
        scopes: clientScopes(read, invoke),
        tool_ids: tools,
        server_ids: servers,
        ...(initial
          ? { enabled, expected_version: initial.version }
          : { expires_at: isoDate(expires) }),
      };
      const result = await action.controller.run<Client | Issued>(
        initial ? `/clients/${initial.id}` : "/clients",
        body,
      );
      if (result) await onSaved(result);
    } catch (error) {
      setError(messageOf(error));
    }
  }
  return (
    <form className="panel" onSubmit={submit}>
      <div className="panel-heading">
        <h2>{initial ? `Edit ${initial.name}` : "Create client"}</h2>
        <button
          type="button"
          className="button secondary"
          disabled={action.busy}
          onClick={onClose}
        >
          Close
        </button>
      </div>
      <div className="panel-body">
        <AdminError error={error || action.error} />
        {action.needsReload ? (
          <p className="field-help">
            Close this editor and reload the client list before editing again.
          </p>
        ) : null}
        <fieldset disabled={action.busy || action.needsReload}>
          <div className="form-grid">
            <label>
              Client name
              <input
                required
                maxLength={120}
                value={name}
                onChange={(event) => setName(event.target.value)}
              />
            </label>
            {initial ? (
              <label className="admin-check">
                <input
                  type="checkbox"
                  checked={enabled}
                  onChange={(event) => setEnabled(event.target.checked)}
                />
                Client enabled
              </label>
            ) : (
              <label>
                Key expiry{" "}
                <span className="field-help">
                  Optional; defaults to 30 days.
                </span>
                <input
                  type="datetime-local"
                  value={expires}
                  onChange={(event) => setExpires(event.target.value)}
                />
              </label>
            )}
          </div>
          <h3>API scopes</h3>
          <div className="action-row">
            <label className="admin-check">
              <input
                type="checkbox"
                checked={read}
                onChange={(event) => {
                  setRead(event.target.checked);
                  if (!event.target.checked) setInvoke(false);
                }}
              />
              Discover and read granted tools
            </label>
            <label className="admin-check">
              <input
                type="checkbox"
                disabled={!read}
                checked={invoke}
                onChange={(event) => setInvoke(event.target.checked)}
              />
              Prepare and execute granted tools
            </label>
          </div>
          <div className="admin-grants">
            <section>
              <h3>Server grants</h3>
              <p className="field-help">
                A server grant includes all its published tools, including
                future imports.
              </p>
              <AdminError
                error={upstreams.error}
                onRetry={() => void upstreams.reload()}
              />
              {upstreams.loading ? (
                <AdminLoading />
              ) : upstreams.data?.items.length ? (
                upstreams.data.items.map((server) => (
                  <label className="admin-check" key={server.id}>
                    <input
                      type="checkbox"
                      checked={servers.includes(server.id)}
                      onChange={() =>
                        setServers(toggleGrant(servers, server.id))
                      }
                    />
                    {server.name}
                  </label>
                ))
              ) : (
                <p className="muted">No MCP servers registered.</p>
              )}
            </section>
            <section>
              <h3>Specific tool grants</h3>
              <label>
                Search registry
                <input
                  type="search"
                  value={catalog.state.input}
                  onChange={(event) =>
                    void catalog.controller.setQuery(event.target.value, 250)
                  }
                />
              </label>
              <p className="field-help">
                {tools.length} selected · {catalog.state.total ?? "—"} matching
                tools
              </p>
              <AdminError
                error={catalog.state.error}
                onRetry={() => void catalog.controller.retry()}
              />
              <div className="admin-grant-list">
                {catalog.state.items.map((tool) => (
                  <label className="admin-check" key={tool.id}>
                    <input
                      type="checkbox"
                      checked={tools.includes(tool.id)}
                      onChange={() => setTools(toggleGrant(tools, tool.id))}
                    />
                    <span>
                      {tool.name}
                      <small>{tool.status}</small>
                    </span>
                  </label>
                ))}
              </div>
              {catalog.state.nextCursor ? (
                <button
                  type="button"
                  className="button secondary"
                  disabled={catalog.state.phase !== "idle"}
                  onClick={() => void catalog.controller.loadMore()}
                >
                  Load more tools
                </button>
              ) : null}
              {tools.length ? (
                <details>
                  <summary>Review all selected tool IDs</summary>
                  {tools.map((id) => (
                    <div className="grant-chip" key={id}>
                      <code>{id}</code>
                      <button
                        type="button"
                        className="text-button"
                        onClick={() => setTools(toggleGrant(tools, id))}
                      >
                        Remove
                      </button>
                    </div>
                  ))}
                </details>
              ) : null}
            </section>
          </div>
        </fieldset>
        <button
          className="button primary"
          disabled={action.busy || action.needsReload}
        >
          {action.busy
            ? "Saving…"
            : initial
              ? "Save client permissions"
              : "Create and reveal API key"}
        </button>
      </div>
    </form>
  );
}
