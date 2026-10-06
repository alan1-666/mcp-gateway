import { useI18n } from "./i18n";
import { useCallback, useEffect, useRef, useState } from "react";
import type { FormEvent } from "react";
import { APIClient, APIError, messageOf } from "./api";
import type { Identity, Operation } from "./types";
import {
  canControlRun,
  canResumeRun,
  consumeRunEvents,
  displayedOutput,
  emptyFeed,
  EVENT_HISTORY_LIMIT,
  isResumableState,
  terminalRunStates,
  validatePrompt,
} from "./run-state";
import type { AgentRun, EventFeed, RunEvent, RuntimeStatus } from "./run-state";
import { readTaskDraft, saveTaskDraft } from "./run-draft";

function date(value?: string, locale = "en-US") {
  if (!value || Number.isNaN(Date.parse(value))) return "Not reported";
  return new Intl.DateTimeFormat(locale, {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  }).format(new Date(value));
}

function RunStatus({ state }: { state: string }) {
  const { t } = useI18n();
  return (
    <span className={`status status-${state.toLowerCase()}`}>
      <span />
      {t(state.toLowerCase().replaceAll("_", " "))}
    </span>
  );
}

function accessLost(error: unknown) {
  return error instanceof APIError && [401, 403, 404].includes(error.status);
}

function RuntimeCard({
  runtime,
  error,
  loading,
}: {
  runtime: RuntimeStatus | null;
  error: string;
  loading: boolean;
}) {
  const { t, locale } = useI18n();
  const ready = runtime?.online && runtime.model_ready;
  const title =
    loading && !runtime
      ? "Checking Agent runtime"
      : !runtime
        ? "Runtime status unavailable"
        : !runtime.online
          ? "Worker offline"
          : !runtime.model_ready
            ? "Model account needs attention"
            : "Runtime ready";
  const description = !runtime
    ? error ||
      "Checking the workspace's configured worker and model connection."
    : !runtime.online
      ? "Tasks can be queued. An administrator needs to bring the workspace worker online before they can run."
      : !runtime.model_ready
        ? "An administrator must connect the worker's model account. Tasks may wait for credentials; resume them after the account is available."
        : "The configured worker reports a model connection. Tasks run with governed tools and may pause for approval.";
  return (
    <section
      className={`agent-runtime ${ready ? "is-ready" : "needs-attention"}`}
      aria-label={t("Agent runtime status")}
    >
      <div className="agent-runtime-indicator" aria-hidden="true" />
      <div className="agent-runtime-copy">
        <h2>{t(title)}</h2>
        <p>{t(description)}</p>
        {error && runtime ? (
          <p className="agent-runtime-error">
            {t(error)} {t("Showing the last reported status.")}{" "}
          </p>
        ) : null}
      </div>
      <dl className="agent-runtime-meta">
        {runtime?.provider || runtime?.model_id ? (
          <div>
            <dt>{t("Configured model")}</dt>
            <dd>
              {[runtime.provider, runtime.model_id].filter(Boolean).join(" / ")}
            </dd>
          </div>
        ) : null}
        <div>
          <dt>{t("Worker last seen")}</dt>
          <dd>{t(date(runtime?.last_seen_at, locale))}</dd>
        </div>
      </dl>
    </section>
  );
}

export function AgentTasks({
  api,
  identity,
  active,
  refreshVersion,
  onOperation,
  onApprovals,
}: {
  api: APIClient;
  identity: Identity;
  active: boolean;
  refreshVersion: string;
  onOperation: (id: string) => void;
  onApprovals: () => void;
}) {
  const { t, locale } = useI18n();
  const [runs, setRuns] = useState<AgentRun[]>([]);
  const [runtime, setRuntime] = useState<RuntimeStatus | null>(null);
  const [runtimeError, setRuntimeError] = useState("");
  const [error, setError] = useState("");
  const [loaded, setLoaded] = useState(false);
  const [loading, setLoading] = useState(false);
  const [selected, setSelected] = useState<string | null>(null);
  const [initialDraft] = useState(() => readTaskDraft(identity));
  const [creating, setCreating] = useState(!!initialDraft);
  const [prompt, setPrompt] = useState(initialDraft?.prompt ?? "");
  const [draftKey, setDraftKey] = useState(
    () => initialDraft?.idempotency_key ?? crypto.randomUUID(),
  );
  const [submitted, setSubmitted] = useState<{
    prompt: string;
    idempotency_key: string;
  } | null>(() =>
    initialDraft?.submitted
      ? {
          prompt: initialDraft.prompt,
          idempotency_key: initialDraft.idempotency_key,
        }
      : null,
  );
  const [draftStored, setDraftStored] = useState(true);
  const [createError, setCreateError] = useState("");
  const [createBusy, setCreateBusy] = useState(false);
  const pending = useRef(false);
  const listSignal = useRef<AbortSignal | null>(null);
  const createPending = useRef(false);
  const canCreate = identity.role === "admin" || identity.role === "operator";

  useEffect(() => {
    setDraftStored(
      saveTaskDraft(identity, {
        version: 1,
        prompt: submitted?.prompt ?? prompt,
        idempotency_key: submitted?.idempotency_key ?? draftKey,
        submitted: !!submitted,
      }),
    );
  }, [identity, prompt, submitted, draftKey]);

  const load = useCallback(
    async (signal: AbortSignal) => {
      if ((pending.current && !listSignal.current?.aborted) || signal.aborted)
        return;
      pending.current = true;
      listSignal.current = signal;
      setLoading(true);
      try {
        const [records, status] = await Promise.allSettled([
          api.request<{ items: AgentRun[] }>("/runs", { signal }),
          api.request<RuntimeStatus>("/runs/runtime", { signal }),
        ]);
        if (signal.aborted) return;
        if (records.status === "fulfilled") {
          setRuns(records.value.items ?? []);
          setError("");
          setLoaded(true);
        } else {
          setError(messageOf(records.reason));
          if (accessLost(records.reason)) {
            setRuns([]);
            setSelected(null);
            setLoaded(false);
          }
        }
        if (status.status === "fulfilled") {
          setRuntime(status.value);
          setRuntimeError("");
        } else {
          setRuntimeError(messageOf(status.reason));
          if (accessLost(status.reason)) setRuntime(null);
        }
      } finally {
        if (listSignal.current === signal) {
          pending.current = false;
          if (!signal.aborted) setLoading(false);
        }
      }
    },
    [api],
  );

  useEffect(() => {
    if (!active) return;
    const controller = new AbortController();
    void load(controller.signal);
    const interval = window.setInterval(() => {
      if (document.visibilityState === "visible") void load(controller.signal);
    }, 8000);
    const visible = () => {
      if (document.visibilityState === "visible") void load(controller.signal);
    };
    document.addEventListener("visibilitychange", visible);
    return () => {
      controller.abort();
      window.clearInterval(interval);
      document.removeEventListener("visibilitychange", visible);
    };
  }, [active, load, refreshVersion]);

  function changed(run: AgentRun) {
    setRuns((current) =>
      [run, ...current.filter((item) => item.id !== run.id)]
        .sort((a, b) => b.created_at.localeCompare(a.created_at))
        .slice(0, 100),
    );
  }

  async function create(event: FormEvent) {
    event.preventDefault();
    if (createPending.current || !canCreate) return;
    setCreateError("");
    let request: { prompt: string; idempotency_key: string };
    try {
      request = submitted ?? {
        prompt: validatePrompt(prompt),
        idempotency_key: draftKey,
      };
    } catch (error) {
      setCreateError(messageOf(error));
      return;
    }
    setSubmitted(request);
    setDraftStored(
      saveTaskDraft(identity, { version: 1, ...request, submitted: true }),
    );
    createPending.current = true;
    setCreateBusy(true);
    try {
      const run = await api.request<AgentRun>("/runs", {
        method: "POST",
        body: request,
      });
      changed(run);
      setSelected(run.id);
      setCreating(false);
      saveTaskDraft(identity, {
        version: 1,
        prompt: "",
        idempotency_key: draftKey,
        submitted: false,
      });
      setPrompt("");
      setSubmitted(null);
      setDraftKey(crypto.randomUUID());
    } catch (error) {
      setCreateError(messageOf(error));
      // An invalid request was not admitted. Ambiguous network/server failures
      // keep the exact prompt and key locked so Retry cannot create another task.
      if (error instanceof APIError && [400, 422].includes(error.status))
        setSubmitted(null);
    } finally {
      createPending.current = false;
      setCreateBusy(false);
    }
  }

  return (
    <div className="agent-tasks">
      <RuntimeCard runtime={runtime} error={runtimeError} loading={loading} />
      {error ? (
        <div role="alert" className="notice notice-error">
          {t(error)}
          {loaded ? t(" The task list may be out of date.") : ""}
        </div>
      ) : null}
      <div className="agent-toolbar">
        <p>
          {identity.role === "admin"
            ? t("Workspace administrators can review all workspace tasks.")
            : t(
                "Only your own tasks are shown. Workspace administrators can review them.",
              )}
        </p>
        {canCreate ? (
          <button
            className="button primary"
            onClick={() => {
              setCreating(true);
              setSelected(null);
            }}
          >
            + {submitted ? t("Return to pending request") : t("New Agent task")}
          </button>
        ) : null}
      </div>
      <div className="agent-task-layout">
        <section
          className="panel agent-task-list"
          aria-label={t("Agent task history")}
        >
          <div className="panel-heading">
            <h2>
              {t("Task history")}{" "}
              <span className="count-label">{runs.length}</span>
            </h2>
            <span className="muted">{t("Latest 100")}</span>
          </div>
          {!loaded ? (
            <div className="agent-empty" role="status">
              {loading
                ? t("Loading task history…")
                : t(
                    "Task history could not be loaded. Use Refresh to try again.",
                  )}
            </div>
          ) : runs.length ? (
            <ol>
              {runs.map((run) => (
                <li key={run.id}>
                  <button
                    className={`agent-task-item ${selected === run.id && !creating ? "is-selected" : ""}`}
                    onClick={() => {
                      setSelected(run.id);
                      setCreating(false);
                    }}
                    aria-current={
                      selected === run.id && !creating ? "true" : undefined
                    }
                  >
                    <div>
                      <RunStatus state={run.state} />
                      <time dateTime={run.created_at}>
                        {t(date(run.created_at, locale))}
                      </time>
                    </div>
                    <strong>{run.prompt}</strong>
                    <span className="mono">{run.id}</span>
                  </button>
                </li>
              ))}
            </ol>
          ) : (
            <div className="agent-empty">
              <h3>{t("No tasks recorded")}</h3>
              <p>
                {canCreate
                  ? t(
                      "Describe a task to start a governed Agent run. Its prompt, output, and activity stay together.",
                    )
                  : t(
                      "Your role cannot create Agent tasks. An administrator or operator can start a task.",
                    )}
              </p>
            </div>
          )}
        </section>
        <div className="agent-task-content">
          {creating && canCreate ? (
            <form className="panel agent-composer" onSubmit={create}>
              <div className="panel-heading">
                <div>
                  <span className="eyebrow">
                    {t("A TASK WITH A TRACEABLE OUTCOME")}{" "}
                  </span>
                  <h2>{t("What needs to be done?")}</h2>
                </div>
              </div>
              <div className="panel-body">
                <p>
                  {t(
                    "The Agent can discover and call published workspace tools. Writes remain subject to the gateway's approval policy.",
                  )}{" "}
                </p>
                {createError ? (
                  <div className="notice notice-error" role="alert">
                    {t(createError)}
                    {submitted
                      ? t(
                          " The result of this request may be uncertain. Retry sends the same prompt and request ID.",
                        )
                      : ""}
                  </div>
                ) : null}
                <label htmlFor="agent-task-prompt">
                  {t("Task instructions")}{" "}
                  <textarea
                    id="agent-task-prompt"
                    rows={10}
                    maxLength={16000}
                    value={submitted?.prompt ?? prompt}
                    readOnly={!!submitted}
                    disabled={createBusy}
                    onChange={(event) => {
                      setPrompt(event.target.value);
                      setDraftKey(crypto.randomUUID());
                    }}
                    required
                    placeholder={t(
                      "Describe the outcome, relevant service, and constraints. The Agent will use only the tools available in this workspace.",
                    )}
                    aria-describedby="agent-prompt-help"
                  />
                </label>
                <div id="agent-prompt-help" className="agent-prompt-help">
                  <span>
                    {submitted
                      ? t(
                          "Request locked for safe retry. The prompt will remain immutable.",
                        )
                      : t("The prompt is immutable once submitted.")}
                  </span>
                  <span>
                    {[...(submitted?.prompt ?? prompt)].length.toLocaleString(
                      locale,
                    )}{" "}
                    {t("/ 8,000 characters")}{" "}
                  </span>
                </div>
                <div className="request-id">
                  <span>{t("Creation request ID")}</span>
                  <code>{submitted?.idempotency_key ?? draftKey}</code>
                </div>
                <p className="field-help">
                  {draftStored
                    ? t(
                        "An unfinished request is saved in this browser tab for recovery after a refresh. Signing out clears the draft.",
                      )
                    : t(
                        "This browser could not save the recovery draft. Keep this page open while a request result is uncertain.",
                      )}
                </p>
                {runtime && (!runtime.online || !runtime.model_ready) ? (
                  <div className="notice notice-info">
                    {t(
                      "The runtime needs attention. You can queue this task; it will wait for a worker or model credentials.",
                    )}{" "}
                  </div>
                ) : null}
                <div className="action-row">
                  <button
                    className="button primary"
                    disabled={createBusy || (!submitted && !prompt.trim())}
                  >
                    {createBusy
                      ? t("Creating task…")
                      : submitted
                        ? t("Retry same request")
                        : t("Create Agent task")}
                  </button>
                  <button
                    className="button secondary"
                    type="button"
                    onClick={() => setCreating(false)}
                    disabled={createBusy}
                  >
                    {t("Back to tasks")}{" "}
                  </button>
                </div>
              </div>
            </form>
          ) : selected ? (
            <TaskDetail
              key={selected}
              id={selected}
              api={api}
              identity={identity}
              active={active}
              refreshVersion={refreshVersion}
              onChanged={changed}
              onOperation={onOperation}
              onApprovals={onApprovals}
            />
          ) : (
            <section className="panel agent-task-welcome">
              <span className="eyebrow">{t("AGENT WORKSPACE")}</span>
              <h2>{t("One task. Its complete record.")}</h2>
              <p>
                {t(
                  "Select a task to inspect its immutable instructions, live output, governed operations, and event history.",
                )}{" "}
              </p>
              <div className="agent-principles">
                <div>
                  <span>01</span>
                  <strong>{t("Describe an outcome")}</strong>
                  <p>
                    {t(
                      "The Agent works with the published tools your account may use.",
                    )}{" "}
                  </p>
                </div>
                <div>
                  <span>02</span>
                  <strong>{t("Review sensitive actions")}</strong>
                  <p>
                    {t("Write requests pause for independent human approval.")}
                  </p>
                </div>
                <div>
                  <span>03</span>
                  <strong>{t("Check the evidence")}</strong>
                  <p>
                    {t(
                      "Task completion and business operation outcomes have separate records.",
                    )}{" "}
                  </p>
                </div>
              </div>
            </section>
          )}
        </div>
      </div>
    </div>
  );
}

function TaskDetail({
  id,
  api,
  identity,
  active,
  refreshVersion,
  onChanged,
  onOperation,
  onApprovals,
}: {
  id: string;
  api: APIClient;
  identity: Identity;
  active: boolean;
  refreshVersion: string;
  onChanged: (run: AgentRun) => void;
  onOperation: (id: string) => void;
  onApprovals: () => void;
}) {
  const { t, locale } = useI18n();
  const [run, setRun] = useState<AgentRun | null>(null);
  const [feed, setFeed] = useState<EventFeed>(emptyFeed);
  const [operation, setOperation] = useState<Operation | null>(null);
  const [operationError, setOperationError] = useState("");
  const [error, setError] = useState("");
  const [actionError, setActionError] = useState("");
  const [busy, setBusy] = useState("");
  const [loading, setLoading] = useState(false);
  const [catchingUp, setCatchingUp] = useState(false);
  const [confirmCancel, setConfirmCancel] = useState(false);
  const [followOutput, setFollowOutput] = useState(true);
  const feedRef = useRef<EventFeed>(emptyFeed());
  const readController = useRef<AbortController | null>(null);
  const readPending = useRef(false);
  const actionPending = useRef(false);
  const onChangedRef = useRef(onChanged);
  onChangedRef.current = onChanged;
  const outputElement = useRef<HTMLPreElement | null>(null);
  const activeRef = useRef(active);
  activeRef.current = active;
  const keepPolling = useRef(true);
  keepPolling.current = !run || !terminalRunStates.has(run.state) || catchingUp;

  const load = useCallback(async () => {
    if (!activeRef.current || readPending.current || actionPending.current)
      return;
    const controller = new AbortController();
    readController.current = controller;
    readPending.current = true;
    setLoading(true);
    try {
      const [current, firstPage] = await Promise.all([
        api.request<AgentRun>(`/runs/${encodeURIComponent(id)}`, {
          signal: controller.signal,
        }),
        api.request<{ items: RunEvent[] }>(
          `/runs/${encodeURIComponent(id)}/events?after=${encodeURIComponent(feedRef.current.cursor)}`,
          { signal: controller.signal },
        ),
      ]);
      if (controller.signal.aborted) return;
      setRun(current);
      onChangedRef.current(current);
      let page = firstPage;
      let nextFeed = feedRef.current;
      for (let index = 0; index < 5; index += 1) {
        const items = page.items ?? [];
        if (items.some((event) => event.run_id !== id))
          throw new Error(
            "The gateway returned an event for a different task.",
          );
        const previousCursor = nextFeed.cursor;
        nextFeed = consumeRunEvents(nextFeed, items);
        if (controller.signal.aborted) return;
        feedRef.current = nextFeed;
        setFeed(nextFeed);
        const more = items.length >= 200 && previousCursor !== nextFeed.cursor;
        setCatchingUp(more);
        if (!more || index === 4) break;
        page = await api.request<{ items: RunEvent[] }>(
          `/runs/${encodeURIComponent(id)}/events?after=${encodeURIComponent(nextFeed.cursor)}`,
          { signal: controller.signal },
        );
      }
      setError("");
      if (current.waiting_operation_id) {
        try {
          const related = await api.request<Operation>(
            `/operations/${encodeURIComponent(current.waiting_operation_id)}`,
            { signal: controller.signal },
          );
          if (!controller.signal.aborted) {
            setOperation(related);
            setOperationError("");
          }
        } catch (error) {
          if (!controller.signal.aborted) {
            setOperation(null);
            setOperationError(messageOf(error));
          }
        }
      } else {
        setOperation(null);
        setOperationError("");
      }
    } catch (error) {
      if (!controller.signal.aborted) {
        setError(messageOf(error));
        if (accessLost(error)) {
          setRun(null);
          setFeed(emptyFeed());
          feedRef.current = emptyFeed();
          setOperation(null);
        }
      }
    } finally {
      if (readController.current === controller) {
        readPending.current = false;
        if (activeRef.current) setLoading(false);
      }
    }
  }, [api, id]);

  useEffect(() => {
    if (!active) {
      readController.current?.abort();
      readController.current = null;
      readPending.current = false;
      return;
    }
    setActionError("");
    void load();
    const interval = window.setInterval(() => {
      if (document.visibilityState === "visible" && keepPolling.current)
        void load();
    }, 3000);
    return () => {
      readController.current?.abort();
      readController.current = null;
      readPending.current = false;
      window.clearInterval(interval);
    };
  }, [active, load, refreshVersion]);

  async function act(action: "cancel" | "resume") {
    if (!run || actionPending.current || !canControlRun(identity, run)) return;
    actionPending.current = true;
    readController.current?.abort();
    readController.current = null;
    readPending.current = false;
    setLoading(false);
    setBusy(action);
    setActionError("");
    try {
      const current = await api.request<AgentRun>(
        `/runs/${encodeURIComponent(id)}/${action}`,
        { method: "POST", body: {} },
      );
      setRun(current);
      onChangedRef.current(current);
      setConfirmCancel(false);
    } catch (error) {
      setActionError(messageOf(error));
    } finally {
      setBusy("");
      actionPending.current = false;
      void load();
    }
  }

  const output = run
    ? displayedOutput(run, feed)
    : { text: "", truncated: false };
  useEffect(() => {
    if (followOutput && outputElement.current)
      outputElement.current.scrollTop = outputElement.current.scrollHeight;
  }, [output.text, followOutput]);
  const controls = run && canControlRun(identity, run);
  const linked = operation?.id === run?.waiting_operation_id ? operation : null;
  const resumeBlocked =
    !!run?.waiting_operation_id &&
    (!linked || !canResumeRun(run, linked.state));

  if (!run)
    return (
      <section className="panel agent-task-detail">
        <div className="panel-body">
          {error ? (
            <div className="notice notice-error" role="alert">
              {t(error)}
            </div>
          ) : (
            <p role="status">{t("Loading task and event history…")}</p>
          )}
          <button
            className="button secondary"
            disabled={loading}
            onClick={() => void load()}
          >
            {t("Refresh task")}{" "}
          </button>
        </div>
      </section>
    );
  return (
    <section className="panel agent-task-detail">
      <div className="panel-heading">
        <div>
          <span className="eyebrow">{t("AGENT TASK RECORD")}</span>
          <h2>{t("Task details")}</h2>
        </div>
        <RunStatus state={run.state} />
      </div>
      <div className="panel-body">
        {error ? (
          <div className="notice notice-error" role="alert">
            {t(error)} {t("The record may be out of date.")}{" "}
          </div>
        ) : null}
        {actionError ? (
          <div className="notice notice-error" role="alert">
            {t(actionError)}{" "}
            {t("Refresh the task before trying another action.")}{" "}
          </div>
        ) : null}
        <div className="agent-task-identifiers">
          <code>{run.id}</code>
          <button
            className="text-button"
            disabled={!!busy || loading}
            onClick={() => void load()}
          >
            {loading ? t("Refreshing…") : t("Refresh task")}
          </button>
        </div>
        <dl className="metadata-grid">
          <div>
            <dt>{t("Created by")}</dt>
            <dd>{run.actor_id}</dd>
          </div>
          <div>
            <dt>{t("Attempt")}</dt>
            <dd>{run.attempt}</dd>
          </div>
          <div>
            <dt>{t("Created")}</dt>
            <dd>{t(date(run.created_at, locale))}</dd>
          </div>
          <div>
            <dt>{t("Updated")}</dt>
            <dd>{t(date(run.updated_at, locale))}</dd>
          </div>
        </dl>
        <div
          className={`notice ${run.state === "NEEDS_REVIEW" || run.state === "FAILED" ? "notice-warning" : "notice-info"}`}
          role="status"
        >
          {t(stateDescription(run, linked))}
        </div>
        {run.error_code ? (
          <p className="agent-error-code">
            {t("Recorded reason:")} <code>{run.error_code}</code>
          </p>
        ) : null}
        <section className="agent-instructions">
          <h3>
            {t("Original instructions")} <span>{t("Immutable")}</span>
          </h3>
          <p>{run.prompt}</p>
        </section>
        {run.waiting_operation_id ? (
          <section className="agent-linked-operation">
            <div>
              <span className="eyebrow">{t("RELATED TOOL OPERATION")}</span>
              <code>{run.waiting_operation_id}</code>
            </div>
            {linked ? <RunStatus state={linked.state} /> : null}
            <p>
              {linked?.state === "UNKNOWN"
                ? t(
                    "The external action may have completed. Verify its downstream state before taking further action. Resume is blocked while the operation is uncertain.",
                  )
                : linked?.state === "DISPATCHING"
                  ? t(
                      "An admitted action is still in progress. Its operation record determines the eventual outcome.",
                    )
                  : linked?.state === "WAITING_APPROVAL"
                    ? t(
                        "An independent authorized reviewer must approve the exact tool arguments. Resume the task after approval.",
                      )
                    : t(
                        "Inspect the tool's recorded outcome before continuing the task.",
                      )}
            </p>
            {operationError ? (
              <p className="agent-linked-error">
                {t("The related operation could not be loaded:")}{" "}
                {t(operationError)}
              </p>
            ) : null}
            <div className="action-row">
              <button
                className="button secondary"
                onClick={() => onOperation(run.waiting_operation_id!)}
              >
                {t("Inspect operation")}{" "}
              </button>
              {linked?.state === "WAITING_APPROVAL" ? (
                <button className="text-button" onClick={onApprovals}>
                  {t("Open approval inbox →")}{" "}
                </button>
              ) : null}
            </div>
          </section>
        ) : null}
        {controls && !terminalRunStates.has(run.state) ? (
          <div className="agent-run-actions">
            {isResumableState(run.state) ? (
              <button
                className="button primary"
                disabled={!!busy || resumeBlocked || !!error}
                onClick={() => void act("resume")}
              >
                {busy === "resume" ? t("Resuming…") : t("Resume task")}
              </button>
            ) : null}
            <button
              className="button danger"
              disabled={!!busy}
              onClick={() => setConfirmCancel(true)}
            >
              {t("Cancel task")}{" "}
            </button>
            {resumeBlocked ? (
              <p>
                {t(
                  "Resume is unavailable until the related operation is approved, completed, or safely resolved.",
                )}{" "}
              </p>
            ) : null}
          </div>
        ) : null}
        {confirmCancel && controls && !terminalRunStates.has(run.state) ? (
          <div className="notice notice-warning">
            <strong>{t("Cancel this task?")}</strong>
            <p>
              {t(
                "Cancellation stops new tool actions. A downstream action already admitted may still finish; cancellation does not undo it.",
              )}{" "}
            </p>
            <div className="action-row">
              <button
                className="button danger"
                disabled={!!busy}
                onClick={() => void act("cancel")}
              >
                {busy === "cancel"
                  ? t("Cancelling…")
                  : t("Confirm cancellation")}
              </button>
              <button
                className="button secondary"
                disabled={!!busy}
                onClick={() => setConfirmCancel(false)}
              >
                {t("Keep task")}{" "}
              </button>
            </div>
          </div>
        ) : null}
        <div className="agent-output-heading">
          <h3>{t("Agent output")}</h3>
          {run.state === "RUNNING" ? (
            <label>
              <input
                type="checkbox"
                checked={followOutput}
                onChange={(event) => setFollowOutput(event.target.checked)}
              />
              {t("Follow output")}{" "}
            </label>
          ) : null}
        </div>
        {catchingUp ? (
          <p className="field-help" role="status">
            {t("Catching up with the recorded event stream…")}{" "}
          </p>
        ) : null}
        {output.truncated ? (
          <p className="notice notice-warning">
            {t(
              "The output is long. This view shows its most recent 262,144 characters.",
            )}{" "}
          </p>
        ) : null}
        {output.text ? (
          <pre
            className="agent-output"
            ref={outputElement}
            tabIndex={0}
            aria-label={t("Agent output")}
          >
            {output.text}
          </pre>
        ) : (
          <div className="agent-output-empty">
            {run.state === "RUNNING"
              ? t("Waiting for text from this attempt…")
              : t("No Agent output has been recorded for this task state.")}
          </div>
        )}
        <p className="agent-outcome-note">
          {t(
            "A successful Agent task means the task finished. Check each tool operation to confirm whether a requested business action succeeded.",
          )}{" "}
        </p>
        <div className="event-heading">
          <h3>{t("Task activity")}</h3>
          <span className="count-label">{feed.count}</span>
        </div>
        {feed.count > EVENT_HISTORY_LIMIT ? (
          <p className="field-help">
            {t(
              "Showing the latest {count} loaded events. Output is reconstructed across the complete loaded stream.",
              { count: EVENT_HISTORY_LIMIT },
            )}
          </p>
        ) : null}
        {feed.items.length ? (
          <ol className="event-timeline agent-event-timeline">
            {feed.items.map((event) => (
              <li key={event.id}>
                <span className="event-marker" />
                <div>
                  <strong>
                    {event.type.replaceAll("_", " ").toLowerCase()}
                  </strong>
                  <time dateTime={event.created_at}>
                    {t(date(event.created_at, locale))}
                  </time>
                  {event.type === "TEXT_DELTA" ? (
                    <span className="event-actor">
                      {t("Text added to Agent output")}{" "}
                    </span>
                  ) : Object.keys(event.data ?? {}).length ? (
                    <details>
                      <summary>
                        {t("Event details · #{id}", { id: event.id })}
                      </summary>
                      <pre>{JSON.stringify(event.data, null, 2)}</pre>
                    </details>
                  ) : (
                    <span className="event-actor">
                      {t("Event #{id}", { id: event.id })}
                    </span>
                  )}
                </div>
              </li>
            ))}
          </ol>
        ) : (
          <p className="muted">{t("No task events returned yet.")}</p>
        )}
      </div>
    </section>
  );
}

function stateDescription(run: AgentRun, operation: Operation | null): string {
  switch (run.state) {
    case "QUEUED":
      return "Waiting for the workspace worker. The task will start when a worker can claim it.";
    case "RUNNING":
      return "The Agent is working through the task. Published tools remain subject to gateway access and approval checks.";
    case "WAITING_APPROVAL":
      return operation?.state === "READY"
        ? "The related operation has been approved. Resume this task to continue with the recorded operation."
        : "The task is paused for a tool operation to be reviewed. Inspect the exact request, then resume after approval.";
    case "WAITING_CREDENTIALS":
      return "The model account is unavailable. An administrator must restore the worker's model connection; then resume this same task.";
    case "NEEDS_REVIEW":
      return "Execution paused for review. Inspect recorded operations before resuming; an interrupted connection does not prove a downstream action failed.";
    case "SUCCEEDED":
      return "The Agent task finished. Its output and tool operation records remain available for review.";
    case "FAILED":
      return "The task stopped with a recorded failure. Review its output and related operation before starting another task.";
    case "CANCELLED":
      return "The task was cancelled. Already admitted downstream actions may still finish; inspect their operation records.";
  }
}
