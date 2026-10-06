import { useEffect, useMemo, useSyncExternalStore } from "react";
import type { FormEvent } from "react";
import type { APIClient } from "./api";
import { ResponsePolicyEditorController } from "./response-policy-editor";
import { ToolResponsePolicy } from "./ToolConnection";
import type { Tool } from "./types";

export function ResponsePolicyEditor({
  api,
  toolID,
  tool,
  canManage,
  loading,
  onChanged,
}: {
  api: APIClient;
  toolID: string;
  tool: Tool | null;
  canManage: boolean;
  loading: boolean;
  onChanged: () => Promise<void>;
}) {
  const controller = useMemo(
    () => new ResponsePolicyEditorController(api, toolID, canManage),
    [api, toolID, canManage],
  );
  const state = useSyncExternalStore(
    controller.subscribe,
    controller.getSnapshot,
  );
  useEffect(() => {
    if (tool) controller.observe(tool);
  }, [controller, tool]);
  useEffect(() => () => controller.cancel(), [controller]);

  if (!canManage || !state.tool?.mcp) return null;
  const busy = state.phase !== "idle";
  const disabled = loading || busy || state.denied;

  async function save(event: FormEvent) {
    event.preventDefault();
    if (loading) return;
    const saved = await controller.save();
    if (saved) await onChanged();
  }
  async function reload() {
    const loaded = await controller.reload();
    if (loaded) await onChanged();
  }

  return (
    <section
      className="panel detail-panel policy-editor"
      aria-label="Edit response policy"
    >
      <div className="panel-heading">
        <div>
          <span className="eyebrow">MCP RESPONSE POLICY</span>
          <h2>Choose the result fields agents receive</h2>
        </div>
        <span className="method-tag">v{state.tool.version}</span>
      </div>
      <form className="panel-body" onSubmit={(event) => void save(event)}>
        <p>
          Changes apply to newly prepared operations. Existing operations keep
          the policy in their original snapshot.
        </p>
        {state.error ? (
          <div className="notice notice-error" role="alert">
            {state.error}
          </div>
        ) : null}
        {state.notice ? (
          <div className="notice notice-info" role="status">
            {state.notice}
          </div>
        ) : null}
        {state.denied ? null : (
          <>
            {state.needsReload ? (
              <div className="action-row policy-conflict-actions">
                <button
                  type="button"
                  className="button secondary"
                  disabled={busy || loading}
                  onClick={() => void reload()}
                >
                  {state.phase === "reloading"
                    ? "Reloading…"
                    : "Reload latest contract"}
                </button>
                <span className="field-help">
                  Your draft is retained. Reloading does not save it.
                </span>
              </div>
            ) : null}
            <details className="policy-saved-contract">
              <summary>Saved policy · version {state.tool.version}</summary>
              <ToolResponsePolicy tool={state.tool} />
            </details>
            <fieldset disabled={disabled}>
              <div className="form-grid">
                <label className="span-two">
                  Response fields to keep{" "}
                  <span className="optional">optional</span>
                  <textarea
                    className="code-input"
                    rows={5}
                    spellCheck={false}
                    value={state.include}
                    onChange={(event) =>
                      controller.edit("include", event.target.value)
                    }
                    placeholder={
                      "/results/*/title\n/results/*/url\n/next_cursor"
                    }
                  />
                  <span className="field-help">
                    One field path per line, up to 32. Leave blank to retain all
                    fields. Use /results/*/title to keep title from each array
                    element, or /results to keep the whole array.
                  </span>
                </label>
                <label className="span-two">
                  Maximum response size (bytes)
                  <input
                    type="number"
                    required
                    min={1024}
                    max={131072}
                    step={1}
                    value={state.maxBytes}
                    onChange={(event) =>
                      controller.edit("maxBytes", event.target.value)
                    }
                  />
                  <span className="field-help">
                    1,024–131,072 bytes inline. Oversized results are rejected
                    unless bounded large-result storage is enabled.
                  </span>
                </label>
              </div>
              <label className="admin-check">
                <input
                  type="checkbox"
                  checked={state.artifactEnabled === "true"}
                  onChange={(event) =>
                    controller.edit(
                      "artifactEnabled",
                      String(event.target.checked),
                    )
                  }
                />
                Enable bounded large-result storage
              </label>
              {state.artifactEnabled === "true" ? (
                <div className="form-grid">
                  <label>
                    Maximum large result (bytes)
                    <input
                      type="number"
                      min={state.maxBytes}
                      max={1048576}
                      step={1}
                      value={state.artifactMaxBytes}
                      onChange={(event) =>
                        controller.edit("artifactMaxBytes", event.target.value)
                      }
                    />
                  </label>
                  <label>
                    Retention (seconds)
                    <input
                      type="number"
                      min={60}
                      max={86400}
                      step={1}
                      value={state.artifactTTL}
                      onChange={(event) =>
                        controller.edit("artifactTTL", event.target.value)
                      }
                    />
                  </label>
                  <p className="field-help span-two">
                    Only validated, filtered structured MCP results are stored.
                    Large results return an operation reference; authorized
                    clients use read_result to retrieve chunks before expiry. No
                    public download link is created.
                  </p>
                </div>
              ) : null}
              <p className="field-help">
                Array selection keeps the original element order and count.
                Every element must contain each selected field, otherwise the
                result is rejected. Numeric path segments select object keys;
                array indices are not supported.
              </p>
              <p className="field-help">
                Top-level nextCursor and next_cursor are kept when present.
                Include other pagination fields explicitly. Text is rebuilt from
                the retained result.
              </p>
              <label className="policy-sample">
                Sample MCP response (JSON)
                <textarea
                  className="code-input"
                  rows={10}
                  spellCheck={false}
                  value={state.sample}
                  onChange={(event) =>
                    controller.edit("sample", event.target.value)
                  }
                />
                <span className="field-help">
                  The initial JSON is an example, not a response from this tool.
                  Paste a raw MCP CallToolResult envelope with content and, when
                  available, structuredContent. The preview request must fit
                  within 256 KiB.
                </span>
              </label>
            </fieldset>
            <div className="action-row">
              <button
                type="button"
                className="button secondary"
                disabled={disabled || state.needsReload}
                onClick={() => void controller.preview()}
              >
                {state.phase === "previewing"
                  ? "Previewing…"
                  : "Preview sample"}
              </button>
              <button
                className="button primary"
                disabled={disabled || state.needsReload}
              >
                {state.phase === "saving" ? "Saving…" : "Save response policy"}
              </button>
            </div>
            <p className="field-help policy-preview-note">
              Preview processes only the sample. It does not call the upstream
              server or save a policy.
            </p>
            {state.preview ? (
              <section
                className="policy-preview"
                aria-label="Response policy preview"
              >
                <dl className="policy-byte-counts">
                  <div>
                    <dt>Original sample</dt>
                    <dd>
                      {state.preview.original_bytes.toLocaleString()}{" "}
                      <span>bytes</span>
                    </dd>
                  </div>
                  <div>
                    <dt>Projected result</dt>
                    <dd>
                      {state.preview.projected_bytes.toLocaleString()}{" "}
                      <span>bytes</span>
                    </dd>
                  </div>
                </dl>
                <div className="json-block">
                  <div className="json-label">
                    Preview result · tool v{state.preview.tool_version}
                    <span>JSON</span>
                  </div>
                  <pre tabIndex={0} aria-label="Projected MCP result">
                    {JSON.stringify(state.preview.result, null, 2)}
                  </pre>
                </div>
              </section>
            ) : null}
          </>
        )}
      </form>
    </section>
  );
}
