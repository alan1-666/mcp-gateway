import { basename, join } from "node:path";
import { chmodSync, existsSync, mkdirSync } from "node:fs";
import { createAgentSession, DefaultResourceLoader, getAgentDir, SessionManager, SettingsManager, type AgentSession } from "@earendil-works/pi-coding-agent";
import { loadConfiguredModel, SYSTEM_PROMPT } from "./runtime.js";
import { IntentJournal } from "./journal.js";
import { createGatewayTools, type Pause } from "./tools.js";
import { WorkerError, type CloudRun, type Finish } from "./worker-client.js";
import { RunStore, syncFile } from "./worker-state.js";
import type { GatewayAPI, JsonObject } from "./client.js";

export type ModelReadiness = { ready: boolean; provider?: string; modelId?: string; errorCode?: string };
export type CloudExecutionContext = {
  run: CloudRun; store: RunStore; gateway: GatewayAPI; signal: AbortSignal;
  emit(type: string, data: JsonObject): void; text(text: string): void; flush(): Promise<void>;
  recoverIntentLock(path: string): void;
};
export async function probeCloudModel(stateDir: string): Promise<ModelReadiness> {
  try { const { model } = await loadConfiguredModel(stateDir); return { ready: true, provider: model.provider, modelId: model.id }; }
  catch { return { ready: false, errorCode: "MODEL_NOT_CONFIGURED" }; }
}

export async function executeCloudPi(ctx: CloudExecutionContext): Promise<Finish> {
  const sessionDir = join(ctx.store.directory, "sessions");
  mkdirSync(sessionDir, { recursive: true, mode: 0o700 });
  let manager: SessionManager;
  const prior = ctx.store.session;
  if (prior.file) {
    if (basename(prior.file) !== prior.file || !existsSync(join(sessionDir, prior.file))) throw new WorkerError("PRIOR_SESSION_MISSING");
    manager = SessionManager.open(join(sessionDir, prior.file), sessionDir, ctx.store.directory);
    if (manager.getSessionId() !== prior.id) throw new WorkerError("SESSION_ID_CHANGED");
  } else {
    manager = SessionManager.create(ctx.store.directory, sessionDir);
    const file = manager.getSessionFile();
    if (!file) throw new WorkerError("SESSION_CREATION_FAILED");
    ctx.store.setSession(basename(file), manager.getSessionId());
    // Persist a host-origin session marker before inference. Pi writes setup-only sessions lazily.
    manager.appendMessage({ role: "user", content: `Host initialized task ${ctx.run.id}. The next message contains the user's task.`, timestamp: Date.now() });
    chmodSync(file, 0o600); syncFile(file); syncFile(sessionDir);
  }
  let configured: Awaited<ReturnType<typeof loadConfiguredModel>>;
  try { configured = await loadConfiguredModel(ctx.store.directory, manager.buildSessionContext().model); }
  catch { return { state: "WAITING_CREDENTIALS", output: ctx.store.output, error_code: "MODEL_NOT_CONFIGURED" }; }
  const journalDir = join(ctx.store.directory, "intents");
  if (prior.started && !existsSync(join(journalDir, `${manager.getSessionId()}.json`))) throw new WorkerError("PRIOR_INTENT_JOURNAL_MISSING");
  ctx.recoverIntentLock(join(journalDir, `${manager.getSessionId()}.json.lock`));
  const journal = new IntentJournal(journalDir, `${ctx.run.workspace_id}|${ctx.run.actor_id}|${ctx.run.id}`, manager.getSessionId());
  let session: AgentSession | undefined;
  let pause: Pause | undefined;
  let fatal: WorkerError | undefined;
  let unsubscribe = () => {};
  const abort = () => { void session?.abort().catch(() => {}); };
  ctx.signal.addEventListener("abort", abort, { once: true });
  try {
    const customTools = createGatewayTools(ctx.gateway, journal, value => { pause = value; });
    for (const tool of customTools) {
      const original = tool.execute;
      tool.execute = async (...args) => {
        try {
          ctx.signal.throwIfAborted();
          ctx.store.claimToolCall();
          const file = manager.getSessionFile(); if (!file || !existsSync(file)) throw new WorkerError("PRIOR_SESSION_MISSING");
          syncFile(file); // Final model tool request is durable before any governed call.
          const response = await original(...args);
          for (const block of response.content) if (block.type === "text") {
            try { if (JSON.parse(block.text).error === "RUN_TOOL_BUDGET_EXHAUSTED") { fatal = new WorkerError("TOOL_BUDGET_EXHAUSTED"); abort(); } } catch { /* Plain text remains model-facing data. */ }
          }
          return response;
        } catch (error) {
          fatal = error instanceof WorkerError ? error : new WorkerError(ctx.signal.aborted ? "RUN_INTERRUPTED" : "LOCAL_STATE_UNAVAILABLE");
          abort();
          return { content: [{ type: "text", text: JSON.stringify({ error: fatal.code, executed: null }) }], details: {}, isError: true };
        }
      };
    }
    const settingsManager = SettingsManager.inMemory({ retry: { enabled: false, maxRetries: 0, provider: { maxRetries: 0 } }, cacheWarming: "off", compaction: { enabled: false }, defaultProjectTrust: "never" });
    const resourceLoader = new DefaultResourceLoader({ cwd: ctx.store.directory, agentDir: getAgentDir(), settingsManager, noExtensions: true, noSkills: true, noPromptTemplates: true, noThemes: true, noContextFiles: true, systemPromptOverride: () => SYSTEM_PROMPT, appendSystemPromptOverride: () => [] });
    await resourceLoader.reload();
    const allowed = customTools.map(tool => tool.name).sort();
    const created = await createAgentSession({ cwd: ctx.store.directory, modelRuntime: configured.runtime, model: configured.model, settingsManager, resourceLoader, sessionManager: manager, tools: allowed, customTools });
    session = created.session;
    if (created.modelFallbackMessage || session.model?.provider !== configured.model.provider || session.model?.id !== configured.model.id || JSON.stringify(session.getCallableToolNames().sort()) !== JSON.stringify(allowed)) throw new WorkerError("UNEXPECTED_MODEL_OR_TOOL_CONFIGURATION");
    unsubscribe = session.subscribe(event => {
      try {
        if (event.type === "message_update" && event.assistantMessageEvent.type === "text_delta") ctx.text(event.assistantMessageEvent.delta);
        if (event.type === "tool_execution_start") ctx.emit("TOOL_STARTED", { tool: event.toolName, tool_call_id: event.toolCallId });
        if (event.type === "tool_execution_end") {
          ctx.emit("TOOL_COMPLETED", { tool: event.toolName, tool_call_id: event.toolCallId, is_error: event.isError, ...(pause?.operation_id ? { operation_id: pause.operation_id } : {}) });
          if (pause || fatal) abort();
        }
      } catch (error) { fatal = error instanceof WorkerError ? error : new WorkerError("EVENT_PERSISTENCE_FAILED"); abort(); }
    });
    ctx.emit("MODEL_STARTED", { provider: configured.model.provider, model_id: configured.model.id });
    await ctx.flush();
    const references = journal.list().filter(intent => intent.operation_id).map(intent => ({ operation_id: intent.operation_id, execution_request_already_sent: intent.dispatched === true }));
    ctx.signal.throwIfAborted(); ctx.store.markStarted();
    await session.prompt(prior.started ? `Host resumed the same task after an explicit user action. Recorded operations: ${JSON.stringify(references)}. Inspect their states before continuing; do not create replacements.\nOriginal task:\n${ctx.run.prompt}` : ctx.run.prompt, { expandPromptTemplates: false });
    const file = manager.getSessionFile(); if (file) { chmodSync(file, 0o600); syncFile(file); }
    if (fatal) throw fatal;
    if (pause) return { state: pause.reason === "WAITING_APPROVAL" ? "WAITING_APPROVAL" : "NEEDS_REVIEW", output: ctx.store.output, error_code: pause.reason, ...(pause.operation_id ? { waiting_operation_id: pause.operation_id } : {}) };
    if (ctx.signal.aborted) throw new WorkerError("RUN_INTERRUPTED");
    const last = session.messages.findLast(message => message.role === "assistant");
    if (!last || last.role !== "assistant") throw new WorkerError("NO_MODEL_RESPONSE");
    if (last.stopReason === "error") {
      const credentialError = /401|unauthorized|credential|oauth|not logged|expired.{0,20}token/i.test(last.errorMessage ?? "");
      return { state: credentialError ? "WAITING_CREDENTIALS" : "FAILED", output: ctx.store.output, error_code: credentialError ? "MODEL_CREDENTIALS_UNAVAILABLE" : "MODEL_REQUEST_FAILED" };
    }
    if (last.stopReason === "aborted") throw new WorkerError("MODEL_INTERRUPTED");
    return { state: "SUCCEEDED", output: ctx.store.output };
  } finally { ctx.signal.removeEventListener("abort", abort); unsubscribe(); session?.dispose(); journal.close(); }
}
