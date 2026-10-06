import { SectionTabs } from "./SectionTabs";
import { Clients } from "./Clients";
import { Credentials } from "./Credentials";
import { Capacity } from "./Capacity";
import { AuditTrail, Reconciliations } from "./Observability";
import { ToolVersions } from "./ToolVersions";
import {
  AdminError,
  AdminLoading,
  MoreRecords,
  useRecordPages,
} from "./AdminUI";
import { filterPath, isoDate } from "./admin-state";
import { useCallback, useEffect, useRef, useState } from "react";
import type { FormEvent, ReactNode } from "react";
import { APIClient, messageOf, parseObject } from "./api";
import { hasUnsafeNumbers } from "./json";
import { Account, CloudLogin, restoreSession } from "./Account";
import { AgentTasks } from "./AgentTasks";
import { MCPServers } from "./MCPServers";
import { canManageMCPServers } from "./mcp-servers";
import { cleanOAuthCallbackURL, oauthCallback } from "./mcp-oauth";
import type { OAuthCallback } from "./mcp-oauth";
import { ToolConnection, ToolResponsePolicy } from "./ToolConnection";
import { ResponsePolicyEditor } from "./ResponsePolicyEditor";
import { clearTaskDraft } from "./run-draft";
import { useToolDetails, useToolSearch } from "./useToolSearch";
import { validateToolPage } from "./tool-search";
import type { ToolSearchController, ToolSearchState } from "./tool-search";
import type {
  Identity,
  Operation,
  OperationEvent,
  OperationState,
  Tool,
  ToolPage,
  ToolSummary,
} from "./types";

type Page =
  | "credentials"
  | "clients"
  | "audit"
  | "capacity"
  | "overview"
  | "tools"
  | "mcp"
  | "invoke"
  | "operations"
  | "approvals"
  | "runs"
  | "account";
type Session = {
  api: APIClient;
  identity: Identity;
  cloud?: boolean;
  username?: string;
};
const navigation: { id: Page; label: string; icon: string }[] = [
  { id: "overview", label: "Overview", icon: "overview" },
  { id: "tools", label: "Tool registry", icon: "tools" },
  { id: "mcp", label: "MCP Servers", icon: "agent" },
  { id: "credentials", label: "Credentials", icon: "approvals" },
  { id: "clients", label: "Clients", icon: "agent" },
  { id: "audit", label: "Audit trail", icon: "search" },
  { id: "capacity", label: "Capacity", icon: "operations" },
  { id: "invoke", label: "New invocation", icon: "invoke" },
  { id: "operations", label: "Operations", icon: "operations" },
  { id: "approvals", label: "Approvals", icon: "approvals" },
];
const pageCopy: Record<Page, { title: string; description: string }> = {
  credentials: {
    title: "Outbound credentials",
    description:
      "Manage encrypted authentication references for your integrations.",
  },
  clients: {
    title: "Client access",
    description:
      "Grant independent application identities exactly the tools they need.",
  },
  audit: {
    title: "Audit trail",
    description:
      "Inspect configuration and authorization changes across the workspace.",
  },
  capacity: {
    title: "Capacity & limits",
    description:
      "Review execution demand and control admission by workspace, client, and upstream.",
  },
  mcp: {
    title: "MCP Servers",
    description:
      "Connect upstream servers and review each tool before it enters your registry.",
  },
  runs: {
    title: "Agent tasks",
    description:
      "Describe an outcome, review tool actions, and follow the task to its recorded result.",
  },
  account: {
    title: "Team & account",
    description: "Manage access, invitations, and personal credentials.",
  },
  overview: {
    title: "Overview",
    description: "A clear view of the tools and operations in your workspace.",
  },
  tools: {
    title: "Tool registry",
    description:
      "Define an integration, review its contract, and make it available.",
  },
  invoke: {
    title: "New invocation",
    description: "Prepare a tracked operation with a published tool.",
  },
  operations: {
    title: "Operations",
    description: "Follow every invocation from intent to recorded outcome.",
  },
  approvals: {
    title: "Approval inbox",
    description:
      "Review the exact action and arguments before a write can execute.",
  },
};
const activeStates = new Set<OperationState>([
  "WAITING_APPROVAL",
  "READY",
  "DISPATCHING",
]);
const defaultSchema = JSON.stringify(
  { type: "object", properties: {}, additionalProperties: false },
  null,
  2,
);

function Icon({ name, size = 18 }: { name: string; size?: number }) {
  const paths: Record<string, ReactNode> = {
    agent: (
      <>
        <path d="M7 4h10v6H7zM3 17h6v4H3zM15 17h6v4h-6zM12 10v4M6 17v-3h12v3" />
      </>
    ),
    overview: (
      <>
        <rect x="3" y="3" width="7" height="7" rx="1" />
        <rect x="14" y="3" width="7" height="7" rx="1" />
        <rect x="3" y="14" width="7" height="7" rx="1" />
        <rect x="14" y="14" width="7" height="7" rx="1" />
      </>
    ),
    tools: (
      <>
        <path d="m12 3 9 5-9 5-9-5 9-5Z" />
        <path d="m3 12 9 5 9-5M3 16l9 5 9-5" />
      </>
    ),
    invoke: (
      <>
        <path d="m9 4 12 8-12 8V4ZM3 5v14" />
      </>
    ),
    operations: (
      <>
        <path d="M3 6h18M3 12h18M3 18h18" />
        <circle cx="7" cy="6" r="2" fill="currentColor" />
        <circle cx="16" cy="12" r="2" fill="currentColor" />
        <circle cx="10" cy="18" r="2" fill="currentColor" />
      </>
    ),
    approvals: (
      <>
        <path d="m12 3 8 3v6c0 4-4 7-8 9-4-2-8-5-8-9V6l8-3Z" />
        <path d="m8 12 3 3 5-6" />
      </>
    ),
    arrow: (
      <>
        <path d="M5 12h14m-5-5 5 5-5 5" />
      </>
    ),
    refresh: (
      <>
        <path d="M20 7v5h-5M4 17v-5h5" />
        <path d="M6 7a7 7 0 0 1 12-1l2 3M4 15l2 3a7 7 0 0 0 12-1" />
      </>
    ),
    plus: <path d="M12 5v14M5 12h14" />,
    exit: (
      <>
        <path d="M9 4H4v16h5m6-12 4 4-4 4M9 12h10" />
      </>
    ),
    close: <path d="m6 6 12 12M6 18 18 6" />,
    search: (
      <>
        <circle cx="10" cy="10" r="6" />
        <path d="m15 15 6 6" />
      </>
    ),
  };
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.6"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      {paths[name] ?? paths.tools}
    </svg>
  );
}

function Brand() {
  return (
    <div className="brand">
      <span className="brand-mark" aria-hidden="true">
        <svg viewBox="0 0 32 32" fill="none">
          <path
            d="M8 26V6h8a7 7 0 0 1 0 14H8m9 0 8 6"
            stroke="currentColor"
            strokeWidth="2.5"
          />
        </svg>
      </span>
      <div>
        Rillgate<span>Team workspace</span>
      </div>
    </div>
  );
}

function Status({ state }: { state: string }) {
  const text = state.toLowerCase().replaceAll("_", " ");
  return (
    <span className={`status status-${state.toLowerCase()}`}>
      <span />
      {text}
    </span>
  );
}

function Notice({
  children,
  kind = "error",
}: {
  children: ReactNode;
  kind?: "error" | "warning" | "info";
}) {
  return (
    <div
      className={`notice notice-${kind}`}
      role={kind === "error" ? "alert" : "status"}
    >
      {children}
    </div>
  );
}

function Empty({
  title,
  children,
  action,
}: {
  title: string;
  children: ReactNode;
  action?: ReactNode;
}) {
  return (
    <div className="empty">
      <span className="empty-symbol">
        <Icon name="tools" size={26} />
      </span>
      <h3>{title}</h3>
      <p>{children}</p>
      {action}
    </div>
  );
}

function JsonBlock({ value, label }: { value: unknown; label: string }) {
  return (
    <div className="json-block">
      <div className="json-label">
        {label}
        <span>JSON</span>
      </div>
      <pre tabIndex={0} aria-label={label}>
        {JSON.stringify(value, null, 2) ?? "null"}
      </pre>
    </div>
  );
}

function formatDate(value: string) {
  const date = new Date(value);
  return Number.isNaN(date.getTime())
    ? "—"
    : new Intl.DateTimeFormat("en-US", {
        month: "short",
        day: "numeric",
        hour: "2-digit",
        minute: "2-digit",
        second: "2-digit",
      }).format(date);
}

export function App() {
  const [oauthReturn, setOAuthReturn] = useState(() =>
    oauthCallback(window.location.search),
  );
  const [session, setSession] = useState<Session | null>(null);
  const [mode, setMode] = useState("loading");
  const [error, setError] = useState("");
  useEffect(() => {
    if (new URLSearchParams(window.location.search).has("oauth"))
      window.history.replaceState(
        window.history.state,
        "",
        cleanOAuthCallbackURL(window.location.href),
      );
  }, []);
  useEffect(() => {
    let live = true;
    void (async () => {
      try {
        const result = await new APIClient().request<{
          mode: string;
          session_present?: boolean;
        }>("/auth/status");
        if (
          result.mode === "cloud" &&
          result.session_present !== false &&
          !window.location.hash.startsWith("#invite=")
        ) {
          try {
            const restored = await restoreSession();
            if (live) setSession(restored);
          } catch (e) {
            if (!(e instanceof Error && "status" in e && e.status === 401))
              throw e;
          }
        }
        if (live) setMode(result.mode);
      } catch (e) {
        if (live) setError(messageOf(e));
      }
    })();
    return () => {
      live = false;
    };
  }, []);
  async function logout() {
    try {
      if (session?.cloud)
        await session.api.request("/auth/logout", { method: "POST", body: {} });
      if (session) clearTaskDraft(session.identity);
      setSession(null);
      setError("");
    } catch (e) {
      if (e instanceof Error && "status" in e && e.status === 401) {
        if (session) clearTaskDraft(session.identity);
        setSession(null);
        setError("");
        return;
      }
      setError(messageOf(e));
    }
  }
  return (
    <>
      {error && (
        <div className="notice notice-error" role="alert">
          {error}{" "}
          <button
            className="button secondary"
            onClick={() => window.location.reload()}
          >
            Reload
          </button>
        </div>
      )}
      {session ? (
        <Workspace
          key={session.identity.id}
          {...session}
          oauthReturn={oauthReturn}
          onDismissOAuth={() => setOAuthReturn(null)}
          onLogout={() => void logout()}
          onSignedOut={() => {
            clearTaskDraft(session.identity);
            setSession(null);
          }}
        />
      ) : mode === "loading" ? (
        <div className="loading-panel" role="status">
          Connecting to workspace…
        </div>
      ) : mode === "cloud" ? (
        <CloudLogin onConnect={setSession} />
      ) : (
        <Connect onConnect={setSession} />
      )}
    </>
  );
}

function Connect({ onConnect }: { onConnect: (session: Session) => void }) {
  const [token, setToken] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  async function connect(event: FormEvent) {
    event.preventDefault();
    if (busy) return;
    setBusy(true);
    setError("");
    try {
      const api = new APIClient(token.trim());
      const identity = await api.request<Identity>("/me");
      onConnect({ api, identity });
    } catch (error) {
      setError(messageOf(error));
      setBusy(false);
    }
  }
  return (
    <main className="connect-layout">
      <section className="connect-story">
        <Brand />
        <div className="connect-story-body">
          <span className="eyebrow">THE TOOL ACCESS LAYER</span>
          <h1>
            Connect tools.
            <br />
            Keep control.
          </h1>
          <p>
            One workspace for tool contracts, human approval, and a traceable
            record of every operation.
          </p>
          <div className="flow-strip">
            <span>Discover</span>
            <i />
            <span>Authorize</span>
            <i />
            <span>Execute</span>
          </div>
        </div>
        <div className="connect-story-foot">
          <span className="connection-dot" /> Built around explicit access and
          recorded outcomes.
        </div>
      </section>
      <section className="connect-form-wrap">
        <form className="connect-form" onSubmit={connect}>
          <span className="eyebrow">WORKSPACE CONNECTION</span>
          <h2>Open your gateway</h2>
          <p>Use an access token issued for your workspace.</p>
          {error ? <Notice>{error}</Notice> : null}
          <label htmlFor="access-token">Workspace access token</label>
          <input
            id="access-token"
            type="password"
            autoComplete="off"
            spellCheck={false}
            required
            value={token}
            onChange={(event) => setToken(event.target.value)}
            placeholder="Enter your access token"
            disabled={busy}
          />
          <p className="field-help">
            Kept in memory for this session. Reloading or signing out clears the
            connection.
          </p>
          <button
            className="button primary connect-button"
            disabled={busy || !token.trim()}
          >
            {busy ? "Connecting…" : "Connect to workspace"}
            <Icon name="arrow" />
          </button>
          <div className="connect-note">
            <Icon name="approvals" />
            <p>
              Your identity and permissions are verified by the gateway before
              any workspace data is loaded.
            </p>
          </div>
        </form>
        <span className="connect-caption">
          Rillgate · Enterprise tool governance
        </span>
      </section>
    </main>
  );
}

function Workspace({
  api,
  identity,
  onLogout,
  onSignedOut,
  cloud,
  username,
  oauthReturn,
  onDismissOAuth,
}: Session & {
  onLogout: () => void;
  onSignedOut: () => void;
  oauthReturn: OAuthCallback;
  onDismissOAuth: () => void;
}) {
  const [page, setPage] = useState<Page>(() =>
    oauthReturn && canManageMCPServers(identity) ? "mcp" : "overview",
  );
  const [menuOpen, setMenuOpen] = useState(false);
  const menuButton = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    const desktop = window.matchMedia("(min-width: 641px)");
    const closeOnDesktop = () => {
      if (desktop.matches) setMenuOpen(false);
    };
    desktop.addEventListener("change", closeOnDesktop);
    return () => desktop.removeEventListener("change", closeOnDesktop);
  }, []);
  const [availableTools, setAvailableTools] = useState(0);
  const [registryTotal, setRegistryTotal] = useState(0);
  const [operations, setOperations] = useState<Operation[]>([]);
  const [operationTotal, setOperationTotal] = useState(0);
  const [pendingTotal, setPendingTotal] = useState(0);
  const [unknownTotal, setUnknownTotal] = useState(0);
  const [operationListVersion, setOperationListVersion] = useState(0);
  const operationsFingerprint = useRef("");
  const operationRevisions = useRef(new Map<string, string>());
  const [loaded, setLoaded] = useState(false);
  const [refreshing, setRefreshing] = useState(false);
  const [error, setError] = useState("");
  const [updated, setUpdated] = useState("");
  const [selectedOperation, setSelectedOperation] = useState<string | null>(
    null,
  );
  const [invokeTool, setInvokeTool] = useState("");
  const [registryTool, setRegistryTool] = useState("");
  const [registrySection, setRegistrySection] = useState("contract");
  const [runRefreshVersion, setRunRefreshVersion] = useState(0);
  const [mcpRefreshVersion, setMCPRefreshVersion] = useState(0);
  const requestController = useRef<AbortController | null>(null);
  const refreshPending = useRef(false);
  const canManage = identity.role === "admin";
  const canInvoke = identity.role === "admin" || identity.role === "operator";
  const canApprove = identity.role === "admin" || identity.role === "approver";

  const refresh = useCallback(async () => {
    requestController.current?.abort();
    const controller = new AbortController();
    requestController.current = controller;
    refreshPending.current = true;
    setRefreshing(true);
    try {
      const [
        toolResult,
        catalogResult,
        operationResult,
        pendingResult,
        unknownResult,
      ] = await Promise.all([
        api.request<ToolPage>("/tools?limit=1", { signal: controller.signal }),
        api.request<ToolPage<ToolSummary>>("/catalog/tools?limit=1", {
          signal: controller.signal,
        }),
        api.request<{ items: Operation[]; total: number }>(
          "/operations?limit=50",
          {
            signal: controller.signal,
          },
        ),
        api.request<{ total: number }>(
          "/operations?state=WAITING_APPROVAL&limit=1",
          { signal: controller.signal },
        ),
        api.request<{ total: number }>("/operations?state=UNKNOWN&limit=1", {
          signal: controller.signal,
        }),
      ]);
      if (controller.signal.aborted) return;
      setRegistryTotal(validateToolPage(toolResult).total);
      setAvailableTools(validateToolPage(catalogResult).total);
      const signature = JSON.stringify(operationResult.items);
      if (signature !== operationsFingerprint.current) {
        operationsFingerprint.current = signature;
        setOperationListVersion((value) => value + 1);
      }
      for (const op of operationResult.items)
        operationRevisions.current.set(op.id, `${op.state}:${op.updated_at}`);
      setOperations(operationResult.items ?? []);
      setOperationTotal(operationResult.total);
      setPendingTotal(pendingResult.total);
      setUnknownTotal(unknownResult.total);
      setLoaded(true);
      setUpdated(new Date().toISOString());
      setError("");
    } catch (error) {
      if (!controller.signal.aborted) setError(messageOf(error));
    } finally {
      if (!controller.signal.aborted) {
        setRefreshing(false);
        refreshPending.current = false;
      }
    }
  }, [api]);

  useEffect(() => {
    void refresh();
    return () => requestController.current?.abort();
  }, [refresh]);

  const hasActive = operations.some((operation) =>
    activeStates.has(operation.state),
  );
  useEffect(() => {
    if (!hasActive || (page !== "operations" && page !== "approvals")) return;
    const interval = window.setInterval(() => {
      if (document.visibilityState === "visible" && !refreshPending.current)
        void refresh();
    }, 5000);
    return () => window.clearInterval(interval);
  }, [hasActive, page, refresh]);

  function showOperation(id: string) {
    setSelectedOperation(id);
    setPage("operations");
  }
  function navigate(nextPage: Page) {
    if (menuOpen) menuButton.current?.focus();
    setMenuOpen(false);
    setPage(nextPage);
    setSelectedOperation(null);
    setRegistryTool("");
    setRegistrySection("contract");
  }
  function startInvocation(toolId = "") {
    setInvokeTool(toolId);
    setPage("invoke");
    setSelectedOperation(null);
  }

  return (
    <div
      className="app-shell"
      onKeyDown={(event) => {
        if (menuOpen && event.key === "Escape") {
          setMenuOpen(false);
          menuButton.current?.focus();
        }
      }}
    >
      <a className="skip-link" href="#main-content">
        Skip to content
      </a>
      <button
        className="icon-button mobile-menu"
        type="button"
        aria-label="Toggle navigation"
        aria-expanded={menuOpen}
        aria-controls="workspace-navigation"
        ref={menuButton}
        onClick={() => setMenuOpen(!menuOpen)}
      >
        <Icon name={menuOpen ? "close" : "operations"} />
      </button>
      <aside
        id="workspace-navigation"
        className={`sidebar ${menuOpen ? "mobile-open" : ""}`}
      >
        <Brand />
        <div className="workspace-label">
          <span className="workspace-avatar">W</span>
          <div>
            <span>Workspace</span>
            <strong title={identity.workspace_id}>
              {identity.workspace_id}
            </strong>
          </div>
        </div>
        <nav aria-label="Main navigation">
          {[
            {
              label: "Gateway",
              pages: ["overview", "mcp", "tools", "clients"],
            },
            {
              label: "Activity",
              pages: [
                "operations",
                "approvals",
                ...(cloud ? ["runs"] : []),
                "audit",
              ],
            },
            {
              label: "Manage",
              pages: ["credentials", "capacity", ...(cloud ? ["account"] : [])],
            },
          ].map((group) => {
            const items = group.pages
              .map(
                (id) =>
                  [
                    ...navigation,
                    { id: "runs" as Page, label: "Agent tasks", icon: "agent" },
                    {
                      id: "account" as Page,
                      label: "Team & account",
                      icon: "approvals",
                    },
                  ].find((item) => item.id === id)!,
              )
              .filter(
                (item) =>
                  (!["credentials", "clients", "audit", "capacity"].includes(
                    item.id,
                  ) ||
                    (canManage && !identity.client_id)) &&
                  (item.id !== "mcp" || canManageMCPServers(identity)),
              );
            return items.length ? (
              <div className="nav-group" key={group.label}>
                <span className="nav-group-label">{group.label}</span>
                {items.map((item) => (
                  <button
                    key={item.id}
                    className={`nav-item ${page === item.id ? "active" : ""}`}
                    onClick={() => navigate(item.id)}
                    aria-current={page === item.id ? "page" : undefined}
                  >
                    <Icon name={item.icon} />
                    <span>{item.label}</span>
                    {item.id === "approvals" && pendingTotal > 0 ? (
                      <span className="nav-count">{pendingTotal}</span>
                    ) : null}
                  </button>
                ))}
              </div>
            ) : null;
          })}
        </nav>
        <div className="sidebar-bottom">
          <div className="identity">
            <span className="identity-avatar">
              {identity.id.slice(0, 1).toUpperCase()}
            </span>
            <div>
              <strong title={identity.id}>{username ?? identity.id}</strong>
              <span>{identity.role}</span>
            </div>
            <button
              className="icon-button"
              aria-label="Sign out"
              title="Sign out"
              onClick={onLogout}
            >
              <Icon name="exit" />
            </button>
          </div>
        </div>
      </aside>
      <div className="workspace-main">
        <header className="topbar">
          <div className="breadcrumb">
            <span
              className="breadcrumb-workspace"
              title={identity.workspace_id}
            >
              {identity.workspace_id}
            </span>
            <span>/</span>
            <strong>
              {page === "account"
                ? "Team & account"
                : page === "runs"
                  ? "Agent tasks"
                  : navigation.find((item) => item.id === page)?.label}
            </strong>
          </div>
          <div className="topbar-end">
            <span className="workspace-status">
              <span className="connection-dot" />
              Connected
            </span>
            <span className="role-tag">{identity.role}</span>
          </div>
        </header>
        <main
          id="main-content"
          className="content"
          tabIndex={-1}
          inert={menuOpen}
        >
          <div className="page-heading">
            <div>
              <h1>{pageCopy[page].title}</h1>
              <p>{pageCopy[page].description}</p>
            </div>
            <button
              className="button secondary refresh-button"
              onClick={() =>
                page === "runs"
                  ? setRunRefreshVersion((current) => current + 1)
                  : page === "mcp"
                    ? setMCPRefreshVersion((current) => current + 1)
                    : (setOperationListVersion((value) => value + 1),
                      void refresh())
              }
              disabled={page !== "runs" && page !== "mcp" && refreshing}
            >
              <Icon name="refresh" />
              {page !== "runs" && page !== "mcp" && refreshing
                ? "Refreshing…"
                : "Refresh"}
            </button>
          </div>
          {error ? (
            <Notice>
              {error}
              {loaded ? " The view may show previously loaded data." : ""}
            </Notice>
          ) : null}
          {cloud ? (
            <div hidden={page !== "runs"}>
              <AgentTasks
                api={api}
                identity={identity}
                active={page === "runs"}
                refreshVersion={String(runRefreshVersion)}
                onOperation={showOperation}
                onApprovals={() => navigate("approvals")}
              />
            </div>
          ) : null}
          {!loaded && page !== "runs" ? (
            <div className="loading-panel" role="status">
              {refreshing ? (
                <>
                  <span className="spinner" />
                  Loading workspace…
                </>
              ) : (
                "Workspace data is unavailable. Refresh to try again."
              )}
            </div>
          ) : (
            <>
              {page === "account" && cloud ? (
                <Account
                  api={api}
                  identity={identity}
                  onSignedOut={onSignedOut}
                  refreshVersion={updated}
                />
              ) : null}
              {page === "credentials" && canManage ? (
                <Credentials api={api} refreshVersion={updated} />
              ) : null}
              {page === "clients" && canManage ? (
                <Clients api={api} refreshVersion={updated} />
              ) : null}
              {page === "audit" && canManage ? (
                <AuditTrail api={api} refreshVersion={updated} />
              ) : null}
              {page === "capacity" && canManage ? (
                <Capacity api={api} refreshVersion={updated} />
              ) : null}
              {page === "overview" ? (
                <Overview
                  canInvoke={canInvoke}
                  availableTools={availableTools}
                  registryTotal={registryTotal}
                  operationTotal={operationTotal}
                  pendingTotal={pendingTotal}
                  unknownTotal={unknownTotal}
                  operations={operations}
                  onNavigate={navigate}
                  onOperation={showOperation}
                  onInvoke={() => startInvocation()}
                />
              ) : null}
              {page === "tools" ? (
                <Registry
                  initialSelectedId={registryTool}
                  initialSection={registrySection}
                  canInvoke={canInvoke}
                  api={api}
                  refreshVersion={updated}
                  canManage={canManage}
                  onRefresh={refresh}
                  onInvoke={startInvocation}
                />
              ) : null}
              {page === "mcp" && canManageMCPServers(identity) ? (
                <>
                  {oauthReturn ? (
                    <div
                      className={`notice notice-${oauthReturn === "connected" ? "info" : "warning"}`}
                      role="status"
                    >
                      {oauthReturn === "connected"
                        ? "Authorization completed. Select the server and open Settings to review its current OAuth connection."
                        : "Authorization could not be completed. Select the server and refresh its OAuth status in Settings before connecting again."}
                      <button
                        className="text-button"
                        type="button"
                        onClick={onDismissOAuth}
                      >
                        Dismiss
                      </button>
                    </div>
                  ) : null}
                  <MCPServers
                    api={api}
                    identity={identity}
                    cloud={!!cloud}
                    refreshVersion={String(mcpRefreshVersion)}
                    onImported={() => void refresh()}
                    onRegistry={(id, section = "contract") => {
                      setRegistrySection(section);
                      setRegistryTool(id);
                      setSelectedOperation(null);
                      setPage("tools");
                    }}
                  />
                </>
              ) : null}
              {page === "invoke" && canInvoke ? (
                <Invocation
                  key={invokeTool}
                  api={api}
                  refreshVersion={updated}
                  initialTool={invokeTool}
                  onCreated={(operation) => {
                    setOperations((current) => [
                      operation,
                      ...current.filter((item) => item.id !== operation.id),
                    ]);
                    showOperation(operation.id);
                  }}
                  onRegistry={() => navigate("tools")}
                />
              ) : null}
              {page === "operations" || page === "approvals" ? (
                <div
                  className={
                    selectedOperation
                      ? "operations-layout has-detail"
                      : "operations-layout"
                  }
                >
                  <OperationList
                    key={page}
                    canInvoke={canInvoke}
                    api={api}
                    refreshVersion={String(operationListVersion)}
                    approvalView={page === "approvals"}
                    selectedId={selectedOperation}
                    onSelect={setSelectedOperation}
                    onInvoke={() => startInvocation()}
                  />
                  {selectedOperation ? (
                    <OperationDetail
                      key={selectedOperation}
                      id={selectedOperation}
                      api={api}
                      identity={identity}
                      canApprove={canApprove}
                      onClose={() => setSelectedOperation(null)}
                      onChanged={(operation) => {
                        const revision = `${operation.state}:${operation.updated_at}`;
                        const previous = operationRevisions.current.get(
                          operation.id,
                        );
                        operationRevisions.current.set(operation.id, revision);
                        if (previous && previous !== revision) {
                          setOperationListVersion((value) => value + 1);
                          void refresh();
                        }
                        setOperations((current) =>
                          current.map((item) =>
                            item.id === operation.id ? operation : item,
                          ),
                        );
                      }}
                    />
                  ) : null}
                </div>
              ) : null}
              {page === "approvals" && !canApprove ? (
                <p className="section-note">
                  Your role can inspect pending operations. An approver or
                  administrator must review write requests.
                </p>
              ) : null}
            </>
          )}
          <footer className="workspace-footer">
            <span>
              {page === "runs"
                ? "Latest 100 accessible Agent tasks · Creator and administrator access"
                : "Paginated tool catalog · Authorized operation history"}
            </span>
            <span>
              {page === "runs"
                ? "Task status refreshes while this view is open."
                : updated
                  ? `Last refreshed ${formatDate(updated)}`
                  : "Waiting for workspace data"}
            </span>
          </footer>
        </main>
      </div>
    </div>
  );
}

function Overview({
  canInvoke,
  availableTools,
  registryTotal,
  operationTotal,
  pendingTotal,
  unknownTotal,
  operations,
  onNavigate,
  onOperation,
  onInvoke,
}: {
  canInvoke: boolean;
  availableTools: number;
  registryTotal: number;
  operationTotal: number;
  pendingTotal: number;
  unknownTotal: number;
  operations: Operation[];
  onNavigate: (page: Page) => void;
  onOperation: (id: string) => void;
  onInvoke: () => void;
}) {
  const published = availableTools;
  const pending = pendingTotal;
  const uncertain = unknownTotal;
  const stats = [
    {
      label: "Available tools",
      value: published,
      detail: `${registryTotal.toLocaleString()} visible registry entries`,
      page: "tools" as Page,
      icon: "tools",
    },
    {
      label: "Recorded operations",
      value: operationTotal,
      detail: "Across your authorized history",
      page: "operations" as Page,
      icon: "operations",
    },
    {
      label: "Awaiting approval",
      value: pending,
      detail: "Write requests requiring review",
      page: "approvals" as Page,
      icon: "approvals",
    },
    {
      label: "Uncertain outcomes",
      value: uncertain,
      detail: "Verify downstream state before action",
      page: "operations" as Page,
      icon: "search",
    },
  ];
  return (
    <>
      <div className="metrics-grid">
        {stats.map((stat) => (
          <button
            key={stat.label}
            className="metric"
            onClick={() => onNavigate(stat.page)}
          >
            <div className="metric-label">
              {stat.label}
              <Icon name={stat.icon} />
            </div>
            <strong
              className={
                stat.label === "Uncertain outcomes" && uncertain
                  ? "text-amber"
                  : ""
              }
            >
              {stat.value.toLocaleString()}
            </strong>
            <span className="metric-detail">
              {stat.detail}
              <Icon name="arrow" size={14} />
            </span>
          </button>
        ))}
      </div>
      {uncertain ? (
        <Notice kind="warning">
          {uncertain} operation{uncertain === 1 ? " has" : "s have"} an
          uncertain outcome. A connection failure does not prove an external
          action failed. Inspect the downstream system before considering
          another action.
        </Notice>
      ) : null}
      <div className="overview-shortcuts" aria-label="Quick actions">
        <div>
          <strong>Work with your tools</strong>
          <span>Explore the catalog or follow a call through the gateway.</span>
        </div>
        <button
          className="button secondary"
          onClick={() => onNavigate("tools")}
        >
          Browse tools
          <Icon name="tools" size={16} />
        </button>
        {canInvoke ? (
          <button className="button primary" onClick={onInvoke}>
            New invocation
            <Icon name="plus" size={16} />
          </button>
        ) : null}
      </div>
      <div className="section-heading">
        <div>
          <h2>Recent operations</h2>
        </div>
        <button
          className="text-button"
          onClick={() => onNavigate("operations")}
        >
          View operations
          <Icon name="arrow" size={16} />
        </button>
      </div>
      <OperationTable
        operations={operations.slice(0, 6)}
        onSelect={onOperation}
        empty={
          <Empty title="No operations recorded">
            Published tools will appear in the invocation workspace. Prepare a
            request to begin its execution record.
          </Empty>
        }
      />
    </>
  );
}

function ToolPagination<T extends { id: string }>({
  state,
  controller,
}: {
  state: ToolSearchState<T>;
  controller: ToolSearchController<T>;
}) {
  const busy = state.phase !== "idle";
  return (
    <div className="tool-pagination">
      {state.error ? (
        <div className="notice notice-error" role="alert">
          <span>
            {state.error}
            {state.loaded ? " Loaded results are retained." : ""}
          </span>
          <div className="action-row">
            <button
              type="button"
              className="button secondary"
              disabled={busy}
              onClick={() => void controller.retry()}
            >
              {state.loaded ? "Retry loading more" : "Retry search"}
            </button>
            {state.loaded ? (
              <button
                type="button"
                className="text-button"
                disabled={busy}
                onClick={() => void controller.reload()}
              >
                Restart search
              </button>
            ) : null}
          </div>
        </div>
      ) : null}
      <div className="tool-pagination-row">
        <span role="status">
          {state.loaded
            ? `${state.items.length.toLocaleString()} loaded · ${state.total?.toLocaleString()} matching tools`
            : busy
              ? "Searching the complete visible catalog…"
              : "No search results loaded"}
        </span>
        {state.nextCursor && !state.error ? (
          <button
            type="button"
            className="button secondary"
            disabled={busy}
            onClick={() => void controller.loadMore()}
          >
            {state.phase === "loading-more"
              ? "Loading more…"
              : "Load more tools"}
          </button>
        ) : state.loaded && !state.error ? (
          <span className="muted">End of results</span>
        ) : null}
      </div>
    </div>
  );
}

function Registry({
  initialSelectedId,
  initialSection,
  canInvoke,
  api,
  refreshVersion,
  canManage,
  onRefresh,
  onInvoke,
}: {
  initialSelectedId: string;
  initialSection: string;
  canInvoke: boolean;
  api: APIClient;
  refreshVersion: string;
  canManage: boolean;
  onRefresh: () => Promise<void>;
  onInvoke: (id: string) => void;
}) {
  const [showForm, setShowForm] = useState(false);
  const [detailTab, setDetailTab] = useState({
    id: initialSelectedId,
    value: initialSection,
  });
  const [selectedId, setSelectedId] = useState<string | null>(
    initialSelectedId || null,
  );
  const search = useToolSearch<Tool>(api, "registry", refreshVersion);
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const details = useToolDetails(api, selectedId, false, refreshVersion);
  const selected = details.tool;
  const visible = search.state.items;
  async function changeTool(tool: Tool, action: "publish" | "enabled") {
    if (busy) return;
    setBusy(tool.id);
    setError("");
    try {
      await api.request<Tool>(
        `/tools/${encodeURIComponent(tool.id)}/${action}`,
        {
          method: "POST",
          body: action === "publish" ? {} : { enabled: !tool.enabled },
        },
      );
      details.reload();
      await Promise.all([search.controller.reload(), onRefresh()]);
    } catch (error) {
      setError(messageOf(error));
    } finally {
      setBusy("");
    }
  }
  return (
    <>
      <div className="toolbar" hidden={!!selectedId}>
        <div className="search-field">
          <Icon name="search" />
          <input
            aria-label="Search tools"
            placeholder="Search tools by name or description"
            type="search"
            value={search.state.input}
            onChange={(event) => {
              setSelectedId(null);
              void search.controller.setQuery(event.target.value, 250);
            }}
          />
        </div>
        {canManage ? (
          <button
            className="button primary"
            onClick={() => setShowForm((current) => !current)}
          >
            <Icon name={showForm ? "close" : "plus"} />
            {showForm ? "Close form" : "Register tool"}
          </button>
        ) : (
          <span className="muted">
            Tool configuration is managed by administrators.
          </span>
        )}
      </div>
      {error ? <Notice>{error}</Notice> : null}
      {showForm && !selectedId ? (
        <ToolForm
          api={api}
          onCancel={() => setShowForm(false)}
          onCreated={async (tool) => {
            setSelectedId(tool.id);
            setShowForm(false);
            await Promise.all([search.controller.reload(), onRefresh()]);
          }}
        />
      ) : null}
      <div className="panel registry-list" hidden={!!selectedId}>
        <div className="panel-heading">
          <h2>
            Registered tools{" "}
            <span className="count-label">{search.state.total ?? "—"}</span>
          </h2>
          <span className="muted">HTTP & MCP integrations</span>
        </div>
        {visible.length ? (
          <div className="table-scroll">
            <table>
              <thead>
                <tr>
                  <th>Tool</th>
                  <th>Contract</th>
                  <th>Risk</th>
                  <th>Status</th>
                  <th>
                    <span className="sr-only">Details</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                {visible.map((tool) => (
                  <tr
                    key={tool.id}
                    className={selectedId === tool.id ? "selected-row" : ""}
                  >
                    <td>
                      <button
                        className="table-link"
                        onClick={() => setSelectedId(tool.id)}
                      >
                        {tool.mcp?.tool_name ?? tool.name}
                      </button>
                      <span className="table-description">
                        {tool.description || "No description provided"}
                      </span>
                    </td>
                    <td>
                      <span className="method-tag">
                        {tool.mcp ? "MCP" : tool.http.method}
                      </span>
                      <span className="version-label">v{tool.version}</span>
                      {tool.mcp ? (
                        <span className="table-description mono">
                          {tool.name}
                        </span>
                      ) : null}
                    </td>
                    <td>
                      <Status state={tool.risk} />
                    </td>
                    <td>
                      <Status
                        state={
                          tool.status !== "published"
                            ? tool.status
                            : tool.enabled
                              ? "published"
                              : "disabled"
                        }
                      />
                    </td>
                    <td>
                      <button
                        className="icon-button"
                        aria-label={`Inspect ${tool.name}`}
                        onClick={() => setSelectedId(tool.id)}
                      >
                        <Icon name="arrow" />
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : search.state.phase === "loading" ? (
          <div className="tool-search-loading" role="status">
            <span className="spinner" />
            Searching tools…
          </div>
        ) : search.state.loaded && !search.state.error ? (
          <Empty
            title={
              search.state.query
                ? "No tools match your search"
                : "Your tool registry starts here"
            }
          >
            {search.state.query
              ? "Try another name or description."
              : "Register an HTTP integration or import a tool from MCP Servers, then review its contract before publishing."}
          </Empty>
        ) : null}
        <ToolPagination {...search} />
      </div>
      {selectedId ? (
        <button
          className="text-button detail-back"
          onClick={() => setSelectedId(null)}
        >
          ← All tools
        </button>
      ) : null}
      {selectedId && !selected ? (
        <section className="panel detail-panel">
          <div className="panel-heading">
            <h2>Tool contract</h2>
            <button
              type="button"
              className="icon-button"
              aria-label="Close tool details"
              onClick={() => setSelectedId(null)}
            >
              <Icon name="close" />
            </button>
          </div>
          <div className="panel-body">
            {details.loading ? (
              <p role="status">Loading the selected tool's contract…</p>
            ) : (
              <>
                <Notice>{details.error}</Notice>
                <button
                  type="button"
                  className="button secondary"
                  onClick={details.reload}
                >
                  Retry loading contract
                </button>
              </>
            )}
          </div>
        </section>
      ) : null}
      {selected ? (
        <>
          <div className="tool-detail-header">
            <div>
              <span className="eyebrow">
                {selected.mcp ? "MCP tool" : "HTTP tool"} · v{selected.version}
              </span>
              <h2>{selected.mcp?.tool_name ?? selected.name}</h2>
              {selected.mcp ? (
                <p className="tool-gateway-name mono">{selected.name}</p>
              ) : null}
            </div>
            <div className="tool-detail-tags">
              <Status state={selected.risk} />
              <Status
                state={
                  selected.status === "published" && !selected.enabled
                    ? "disabled"
                    : selected.status
                }
              />
            </div>
          </div>
          <SectionTabs
            key={selected.id}
            label="Tool details"
            value={detailTab.id === selected.id ? detailTab.value : "contract"}
            onChange={(value) => setDetailTab({ id: selected.id, value })}
            tabs={[
              {
                id: "contract",
                label: "Contract",
                content: (
                  <section className="panel detail-panel">
                    <div className="panel-body">
                      <p>{selected.description}</p>
                      <dl className="metadata-grid">
                        <div>
                          <dt>Tool ID</dt>
                          <dd className="mono">{selected.id}</dd>
                        </div>
                        <div>
                          <dt>Version</dt>
                          <dd>v{selected.version}</dd>
                        </div>
                        <ToolConnection tool={selected} />
                      </dl>
                      <ToolResponsePolicy tool={selected} />
                      <JsonBlock
                        label="Input schema"
                        value={selected.input_schema}
                      />
                      {selected.output_schema ? (
                        <JsonBlock
                          label="Output schema"
                          value={selected.output_schema}
                        />
                      ) : null}
                      <div className="action-row">
                        {canManage && selected.status === "draft" ? (
                          <button
                            className="button primary"
                            disabled={!!busy}
                            onClick={() => void changeTool(selected, "publish")}
                          >
                            {busy ? "Publishing…" : "Publish tool"}
                          </button>
                        ) : null}
                        {canManage && selected.status === "published" ? (
                          <button
                            className="button secondary"
                            disabled={!!busy}
                            onClick={() => void changeTool(selected, "enabled")}
                          >
                            {busy
                              ? "Updating…"
                              : selected.enabled
                                ? "Disable tool"
                                : "Enable tool"}
                          </button>
                        ) : null}
                        {canInvoke &&
                        selected.status === "published" &&
                        selected.enabled ? (
                          <button
                            className="button primary"
                            onClick={() => onInvoke(selected.id)}
                          >
                            Prepare invocation
                            <Icon name="arrow" />
                          </button>
                        ) : null}
                      </div>
                    </div>
                  </section>
                ),
              },
              ...(canManage
                ? [
                    {
                      id: "versions",
                      label: "Versions & changes",
                      content: (
                        <ToolVersions
                          key={`${selected.id}:${selected.version}`}
                          api={api}
                          tool={selected}
                          onChanged={async () => {
                            details.reload();
                            await Promise.all([
                              search.controller.reload(),
                              onRefresh(),
                            ]);
                          }}
                        />
                      ),
                    },
                  ]
                : []),
              ...(canManage && selected.mcp && selected.status !== "retired"
                ? [
                    {
                      id: "response",
                      label: "Response policy",
                      content: (
                        <ResponsePolicyEditor
                          key={selectedId}
                          api={api}
                          toolID={selected.id}
                          tool={selected}
                          canManage={canManage}
                          loading={details.loading || !!busy}
                          onChanged={async () => {
                            details.reload();
                            await Promise.all([
                              search.controller.reload(),
                              onRefresh(),
                            ]);
                          }}
                        />
                      ),
                    },
                  ]
                : []),
            ]}
          />
        </>
      ) : null}
    </>
  );
}

function ToolForm({
  api,
  onCreated,
  onCancel,
}: {
  api: APIClient;
  onCreated: (tool: Tool) => Promise<void>;
  onCancel: () => void;
}) {
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [url, setUrl] = useState("");
  const [method, setMethod] = useState("GET");
  const [risk, setRisk] = useState<"read" | "write">("read");
  const [timeout, setTimeoutValue] = useState("10000");
  const [credential, setCredential] = useState("");
  const [schema, setSchema] = useState(defaultSchema);
  const [outputSchema, setOutputSchema] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  async function submit(event: FormEvent) {
    event.preventDefault();
    if (busy) return;
    setError("");
    setBusy(true);
    try {
      const inputSchema = parseObject(schema, "Input schema");
      const output = outputSchema.trim()
        ? parseObject(outputSchema, "Output schema")
        : undefined;
      if (!/^[A-Za-z][A-Za-z0-9._-]{0,63}$/.test(name.trim()))
        throw new Error(
          "Tool name must start with a letter and contain at most 64 letters, digits, dots, underscores, or hyphens.",
        );
      if (new TextEncoder().encode(description.trim()).length > 4000)
        throw new Error("Description must not exceed 4000 UTF-8 bytes.");
      if (risk === "read" && method !== "GET")
        throw new Error("Read tools require the GET method.");
      if (credential.trim() && !/^[A-Z][A-Z0-9_]*$/.test(credential.trim()))
        throw new Error(
          "Credential references use uppercase letters, digits, and underscores, starting with a letter.",
        );
      if (inputSchema.type !== "object")
        throw new Error('Input schema must declare type "object".');
      const destination = new URL(url);
      if (!["http:", "https:"].includes(destination.protocol))
        throw new Error("The destination must use HTTP or HTTPS.");
      if (destination.hash)
        throw new Error("Destination URLs must not contain a fragment.");
      if (destination.username || destination.password)
        throw new Error(
          "Use a credential reference instead of credentials in the URL.",
        );
      const tool = await api.request<Tool>("/tools", {
        method: "POST",
        body: {
          name: name.trim(),
          description: description.trim(),
          risk,
          input_schema: inputSchema,
          ...(output ? { output_schema: output } : {}),
          http: {
            url: url.trim(),
            method,
            timeout_ms: Number(timeout),
            ...(credential.trim() ? { credential_ref: credential.trim() } : {}),
          },
        },
      });
      await onCreated(tool);
    } catch (error) {
      setError(messageOf(error));
      setBusy(false);
    }
  }
  return (
    <form className="panel tool-form" onSubmit={submit}>
      <div className="panel-heading">
        <div>
          <span className="eyebrow">NEW INTEGRATION</span>
          <h2>Register an HTTP tool</h2>
        </div>
        <Status state="draft" />
      </div>
      <div className="panel-body">
        {error ? <Notice>{error}</Notice> : null}
        <fieldset disabled={busy}>
          <div className="form-grid">
            <label>
              Tool name
              <input
                required
                maxLength={64}
                value={name}
                onChange={(event) => setName(event.target.value)}
                placeholder="service_status"
                autoComplete="off"
              />
            </label>
            <label>
              Risk classification
              <select
                value={risk}
                onChange={(event) => {
                  const next = event.target.value as "read" | "write";
                  setRisk(next);
                  if (next === "read") setMethod("GET");
                }}
              >
                <option value="read">Read — no external state changes</option>
                <option value="write">Write — requires approval</option>
              </select>
            </label>
            <label className="span-two">
              Description
              <textarea
                required
                rows={2}
                maxLength={4000}
                value={description}
                onChange={(event) => setDescription(event.target.value)}
                placeholder="Describe what the tool does and when it should be used."
              />
            </label>
            <label>
              HTTP method
              <select
                value={method}
                onChange={(event) => {
                  const next = event.target.value;
                  setMethod(next);
                  if (next !== "GET") setRisk("write");
                }}
              >
                {["GET", "POST", "PUT", "PATCH", "DELETE"].map((item) => (
                  <option key={item}>{item}</option>
                ))}
              </select>
            </label>
            <label>
              Timeout (milliseconds)
              <input
                type="number"
                min={100}
                max={120000}
                step={100}
                required
                value={timeout}
                onChange={(event) => setTimeoutValue(event.target.value)}
              />
            </label>
            <label className="span-two">
              Destination URL
              <input
                type="url"
                required
                value={url}
                onChange={(event) => setUrl(event.target.value)}
                placeholder="https://service.example.com/status"
                autoComplete="off"
              />
            </label>
            <label className="span-two">
              Credential reference <span className="optional">optional</span>
              <input
                value={credential}
                onChange={(event) => setCredential(event.target.value)}
                placeholder="SERVICE_ACCESS_TOKEN"
                autoComplete="off"
              />
              <span className="field-help">
                Enter the reference name. Never paste a secret or access token
                here.
              </span>
            </label>
            <label className="span-two">
              Input schema
              <textarea
                className="code-input"
                rows={8}
                required
                spellCheck={false}
                value={schema}
                onChange={(event) => setSchema(event.target.value)}
              />
            </label>
            <label className="span-two">
              Output schema <span className="optional">optional</span>
              <textarea
                className="code-input"
                rows={5}
                spellCheck={false}
                value={outputSchema}
                onChange={(event) => setOutputSchema(event.target.value)}
                placeholder='{ "type": "object", "required": ["status"] }'
              />
              <span className="field-help">
                Define the JSON response contract required for successful
                execution.
              </span>
            </label>
          </div>
        </fieldset>
        <div className="action-row">
          <button className="button primary" disabled={busy}>
            {busy ? "Registering…" : "Create draft tool"}
          </button>
          <button
            className="button secondary"
            type="button"
            disabled={busy}
            onClick={onCancel}
          >
            Cancel
          </button>
          <span className="field-help">
            Review and publish the draft to make it available.
          </span>
        </div>
      </div>
    </form>
  );
}

function Invocation({
  api,
  refreshVersion,
  initialTool,
  onCreated,
  onRegistry,
}: {
  api: APIClient;
  refreshVersion: string;
  initialTool: string;
  onCreated: (operation: Operation) => void;
  onRegistry: () => void;
}) {
  const search = useToolSearch<ToolSummary>(api, "discovery", refreshVersion);
  const [toolId, setToolId] = useState(initialTool);
  const [argumentsText, setArgumentsText] = useState("{}");
  const [key, setKey] = useState(() => crypto.randomUUID());
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const details = useToolDetails(api, toolId || null, true, refreshVersion);
  const tool = details.tool;

  function selectTool(id: string) {
    if (id === toolId || busy) return;
    setToolId(id);
    setArgumentsText("{}");
    setKey(crypto.randomUUID());
    setError("");
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!tool || busy) return;
    setError("");
    setBusy(true);
    try {
      const args = parseObject(argumentsText, "Arguments");
      const operation = await api.request<Operation>("/operations", {
        method: "POST",
        body: { tool_id: tool.id, arguments: args, idempotency_key: key },
      });
      onCreated(operation);
    } catch (error) {
      setError(messageOf(error));
      setBusy(false);
    }
  }

  return (
    <div className="invocation-workspace">
      <section
        className="panel tool-picker"
        aria-labelledby="tool-picker-title"
      >
        <div className="panel-heading">
          <h2 id="tool-picker-title">Select a published tool</h2>
          <span className="step-label">CATALOG</span>
        </div>
        <div className="tool-picker-search">
          <label className="search-field">
            <Icon name="search" />
            <span className="sr-only">Search published tools</span>
            <input
              type="search"
              value={search.state.input}
              placeholder="Search all published tools by name or description…"
              disabled={busy}
              onChange={(event) =>
                void search.controller.setQuery(event.target.value, 250)
              }
            />
          </label>
          <p className="field-help">
            Search covers the complete available catalog. Select a tool to load
            its contract.
          </p>
        </div>
        {search.state.items.length ? (
          <fieldset className="tool-options" disabled={busy}>
            <legend className="sr-only">Available tools</legend>
            {search.state.items.map((item) => (
              <label
                key={item.id}
                className={`tool-option ${toolId === item.id ? "selected" : ""}`}
              >
                <input
                  type="radio"
                  name="invocation-tool"
                  value={item.id}
                  checked={toolId === item.id}
                  onChange={() => selectTool(item.id)}
                />
                <span className="tool-option-copy">
                  <strong>
                    {item.name}
                    <span className="version-label">v{item.version}</span>
                  </strong>
                  <span>{item.description || "No description provided."}</span>
                </span>
                <Status state={item.risk} />
              </label>
            ))}
          </fieldset>
        ) : search.state.phase === "loading" ? (
          <div className="tool-search-loading" role="status">
            <span className="spinner" /> Searching published tools…
          </div>
        ) : search.state.loaded && !search.state.error ? (
          <Empty
            title={
              search.state.query ? "No matching tools" : "No available tools"
            }
            action={
              !search.state.query ? (
                <button className="button secondary" onClick={onRegistry}>
                  Open tool registry
                  <Icon name="arrow" />
                </button>
              ) : undefined
            }
          >
            {search.state.query
              ? "Try another name or a word from the tool description."
              : "An administrator can register, enable and publish an integration."}
          </Empty>
        ) : null}
        <ToolPagination {...search} />
      </section>
      <div className="invocation-layout">
        <form className="panel" onSubmit={submit}>
          <div className="panel-heading">
            <h2>Prepare the request</h2>
            <span className="step-label">01 / 02</span>
          </div>
          <div className="panel-body">
            {error ? (
              <Notice>
                {error} You can submit unchanged arguments again with the same
                request ID.
              </Notice>
            ) : null}
            <div className="selected-tool-label">
              <span>Selected tool</span>
              <strong>
                {tool
                  ? tool.name
                  : details.loading
                    ? "Loading contract…"
                    : toolId
                      ? "Contract unavailable"
                      : "Choose a tool above"}
              </strong>
              {tool ? <span className="mono">v{tool.version}</span> : null}
            </div>
            <fieldset disabled={busy}>
              <label>
                Arguments
                <textarea
                  rows={13}
                  className="code-input"
                  spellCheck={false}
                  value={argumentsText}
                  onChange={(event) => {
                    setArgumentsText(event.target.value);
                    setKey(crypto.randomUUID());
                  }}
                  required
                />
              </label>
            </fieldset>
            <div className="request-id">
              <span>Request ID</span>
              <code>{key}</code>
            </div>
            {tool?.risk === "write" ? (
              <Notice kind="warning">
                This tool can change external state. The prepared operation will
                wait for approval before it can execute.
              </Notice>
            ) : (
              <Notice kind="info">
                Preparing records your intent. You will review and execute the
                operation on the next screen.
              </Notice>
            )}
            <button className="button primary" disabled={busy || !tool}>
              {busy ? "Preparing…" : "Prepare operation"}
              <Icon name="arrow" />
            </button>
          </div>
        </form>
        <aside className="panel contract-preview" aria-live="polite">
          <div className="panel-heading">
            <h2>Published contract</h2>
            {tool ? <Status state={tool.risk} /> : null}
          </div>
          <div className="panel-body">
            {details.loading ? (
              <p className="tool-search-loading" role="status">
                <span className="spinner" /> Loading selected contract…
              </p>
            ) : details.error ? (
              <Notice>
                {details.error}
                <button className="button secondary" onClick={details.reload}>
                  Retry loading contract
                </button>
              </Notice>
            ) : tool ? (
              <>
                <h3>{tool.name}</h3>
                <p className="muted">{tool.description}</p>
                <dl className="metadata-grid">
                  <div>
                    <dt>Version</dt>
                    <dd>v{tool.version}</dd>
                  </div>
                  <div>
                    <dt>Transport</dt>
                    <dd>
                      {tool.mcp
                        ? "MCP · Streamable HTTP"
                        : `HTTP · ${tool.http.method}`}
                    </dd>
                  </div>
                  {tool.mcp ? (
                    <>
                      <div>
                        <dt>Remote tool</dt>
                        <dd className="mono break-word">
                          {tool.mcp.tool_name}
                        </dd>
                      </div>
                      <div>
                        <dt>MCP server ID</dt>
                        <dd className="mono break-word">
                          {tool.mcp.server_id}
                        </dd>
                      </div>
                    </>
                  ) : null}
                </dl>
                <ToolResponsePolicy tool={tool} />
                <JsonBlock
                  label="Expected arguments"
                  value={tool.input_schema}
                />
              </>
            ) : (
              <p className="muted">
                Select an available tool to inspect its current input schema.
              </p>
            )}
          </div>
        </aside>
      </div>
    </div>
  );
}

function OperationTable({
  operations,
  selectedId,
  onSelect,
  empty,
}: {
  operations: Operation[];
  selectedId?: string | null;
  onSelect: (id: string) => void;
  empty: ReactNode;
}) {
  if (!operations.length) return <div className="panel">{empty}</div>;
  return (
    <div className="panel table-scroll">
      <table>
        <thead>
          <tr>
            <th>Operation / tool</th>
            <th>State</th>
            <th>Requested by</th>
            <th>Created</th>
            <th>
              <span className="sr-only">Inspect</span>
            </th>
          </tr>
        </thead>
        <tbody>
          {operations.map((operation) => (
            <tr
              key={operation.id}
              className={selectedId === operation.id ? "selected-row" : ""}
            >
              <td>
                <button
                  className="table-link"
                  onClick={() => onSelect(operation.id)}
                >
                  {operation.tool_name || operation.tool_id}
                </button>
                <span className="table-description mono">{operation.id}</span>
              </td>
              <td>
                <Status state={operation.state} />
              </td>
              <td>
                <span className="actor-label">{operation.actor_id}</span>
              </td>
              <td className="date-cell">{formatDate(operation.created_at)}</td>
              <td>
                <button
                  className="icon-button"
                  aria-label={`Inspect operation ${operation.id}`}
                  onClick={() => onSelect(operation.id)}
                >
                  <Icon name="arrow" />
                </button>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function OperationList({
  canInvoke,
  api,
  refreshVersion,
  approvalView,
  selectedId,
  onSelect,
  onInvoke,
}: {
  canInvoke: boolean;
  api: APIClient;
  refreshVersion: string;
  approvalView: boolean;
  selectedId: string | null;
  onSelect: (id: string) => void;
  onInvoke: () => void;
}) {
  const [filters, setFilters] = useState({
    state: approvalView ? "WAITING_APPROVAL" : "",
    tool_id: "",
    actor_id: "",
    from: "",
    to: "",
  });
  const [applied, setApplied] = useState<Record<string, string>>({
    state: approvalView ? "WAITING_APPROVAL" : "",
    limit: "50",
  });
  const [error, setError] = useState("");
  const path = filterPath("/operations", {
    ...applied,
    ...(approvalView ? { state: "WAITING_APPROVAL" } : {}),
  });
  const pages = useRecordPages<Operation>(api, path, refreshVersion);
  function submit(event: FormEvent) {
    event.preventDefault();
    setError("");
    try {
      setApplied({
        ...filters,
        from: isoDate(filters.from) ?? "",
        to: isoDate(filters.to) ?? "",
        limit: "50",
      });
    } catch (error) {
      setError(messageOf(error));
    }
  }
  return (
    <section className="operation-list">
      <div className="toolbar">
        <div className="list-summary">
          <strong>{pages.state.total ?? "—"}</strong>{" "}
          {approvalView ? "pending review" : "matching operations"}
        </div>
        {canInvoke && !approvalView ? (
          <button className="button primary" onClick={onInvoke}>
            <Icon name="plus" />
            New invocation
          </button>
        ) : null}
      </div>
      <form className="panel panel-body admin-filter-form" onSubmit={submit}>
        {!approvalView ? (
          <label>
            State
            <select
              value={filters.state}
              onChange={(event) =>
                setFilters({ ...filters, state: event.target.value })
              }
            >
              <option value="">All states</option>
              {[
                "WAITING_APPROVAL",
                "READY",
                "DISPATCHING",
                "SUCCEEDED",
                "FAILED",
                "UNKNOWN",
                "REJECTED",
              ].map((state) => (
                <option key={state}>{state}</option>
              ))}
            </select>
          </label>
        ) : null}
        {(["tool_id", "actor_id", "from", "to"] as const).map((name) => (
          <label key={name}>
            {name.replaceAll("_", " ")}
            <input
              type={
                name === "from" || name === "to" ? "datetime-local" : "text"
              }
              value={filters[name]}
              onChange={(event) =>
                setFilters({ ...filters, [name]: event.target.value })
              }
            />
          </label>
        ))}
        <button className="button secondary">Apply filters</button>
      </form>
      <AdminError error={error} />
      {pages.state.phase === "loading" ? (
        <AdminLoading />
      ) : pages.state.loaded ? (
        <OperationTable
          operations={pages.state.items}
          selectedId={selectedId}
          onSelect={onSelect}
          empty={
            <Empty
              title={
                approvalView
                  ? "No requests awaiting approval"
                  : "No matching operations"
              }
            >
              Try another filter or prepare an invocation from a published tool.
            </Empty>
          }
        />
      ) : null}
      <MoreRecords pages={pages} />
    </section>
  );
}

function OperationDetail({
  id,
  api,
  identity,
  canApprove,
  onClose,
  onChanged,
}: {
  id: string;
  api: APIClient;
  identity: Identity;
  canApprove: boolean;
  onClose: () => void;
  onChanged: (operation: Operation) => void;
}) {
  const [operation, setOperation] = useState<Operation | null>(null);
  const [events, setEvents] = useState<OperationEvent[]>([]);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState("");
  const [rejectOpen, setRejectOpen] = useState(false);
  const actionPending = useRef(false);
  const loadSequence = useRef(0);
  const onChangedRef = useRef(onChanged);
  onChangedRef.current = onChanged;
  const load = useCallback(
    async (signal?: AbortSignal) => {
      const sequence = ++loadSequence.current;
      try {
        const [current, history] = await Promise.all([
          api.request<Operation>(`/operations/${encodeURIComponent(id)}`, {
            signal,
          }),
          api.request<{ items: OperationEvent[] }>(
            `/operations/${encodeURIComponent(id)}/events`,
            { signal },
          ),
        ]);
        if (signal?.aborted || sequence !== loadSequence.current) return;
        setOperation(current);
        setEvents(history.items ?? []);
        setError("");
        onChangedRef.current(current);
      } catch (error) {
        if (!signal?.aborted && sequence === loadSequence.current)
          setError(messageOf(error));
      }
    },
    [api, id],
  );
  useEffect(() => {
    const controller = new AbortController();
    void load(controller.signal);
    return () => controller.abort();
  }, [load]);
  const state = operation?.state;
  useEffect(() => {
    if (!state || !activeStates.has(state)) return;
    const controller = new AbortController();
    let pending = false;
    const interval = window.setInterval(() => {
      if (
        document.visibilityState !== "visible" ||
        pending ||
        actionPending.current
      )
        return;
      pending = true;
      void load(controller.signal).finally(() => {
        pending = false;
      });
    }, 3000);
    return () => {
      controller.abort();
      window.clearInterval(interval);
    };
  }, [load, state]);

  async function act(action: "execute" | "approve" | "reject") {
    if (actionPending.current) return;
    if (
      (action === "approve" || action === "execute") &&
      operation &&
      hasUnsafeNumbers(operation.arguments)
    ) {
      setError(
        "Approval and execution require arguments that this console can represent exactly.",
      );
      return;
    }
    actionPending.current = true;
    loadSequence.current += 1;
    setBusy(action);
    setError("");
    try {
      const current = await api.request<Operation>(
        `/operations/${encodeURIComponent(id)}/${action}`,
        { method: "POST", body: {} },
      );
      setOperation(current);
      onChangedRef.current(current);
      setRejectOpen(false);
      await load();
    } catch (error) {
      setError(messageOf(error));
    } finally {
      setBusy("");
      actionPending.current = false;
    }
  }
  const selfApproval = operation?.actor_id === identity.id;
  const unsafeArguments = operation
    ? hasUnsafeNumbers(operation.arguments)
    : false;
  return (
    <section className="panel operation-detail">
      <div className="panel-heading">
        <div>
          <span className="eyebrow">OPERATION RECORD</span>
          <h2>{operation?.tool_name || "Loading operation"}</h2>
        </div>
        <button
          className="icon-button"
          onClick={onClose}
          aria-label="Close operation details"
        >
          <Icon name="close" />
        </button>
      </div>
      <div className="panel-body">
        {error ? (
          <Notice>
            {error} Refresh the record before taking another action.
          </Notice>
        ) : null}
        {!operation ? (
          <div role="status">
            {error ? (
              <button className="button secondary" onClick={() => void load()}>
                Retry loading record
              </button>
            ) : (
              "Loading operation and events…"
            )}
          </div>
        ) : (
          <>
            <div className="operation-state-row">
              <Status state={operation.state} />
              <button
                className="text-button"
                onClick={() => void load()}
                disabled={!!busy}
              >
                <Icon name="refresh" size={14} />
                Refresh record
              </button>
            </div>
            <p className="record-id mono">{operation.id}</p>
            {operation.state === "UNKNOWN" ? (
              <Notice kind="warning">
                Outcome unknown. The downstream action may have completed.
                Verify its state before creating another operation. This record
                cannot be executed again.
              </Notice>
            ) : null}
            {operation.state === "DISPATCHING" ? (
              <Notice kind="info">
                The operation has been dispatched. Its recorded outcome will
                appear here when available.
              </Notice>
            ) : null}
            {operation.error ? <Notice>{operation.error}</Notice> : null}
            <dl className="metadata-grid">
              <div>
                <dt>Requested by</dt>
                <dd>{operation.actor_id}</dd>
              </div>
              <div>
                <dt>Tool version</dt>
                <dd>v{operation.tool_version}</dd>
              </div>
              <div>
                <dt>Created</dt>
                <dd>{formatDate(operation.created_at)}</dd>
              </div>
              <div>
                <dt>Updated</dt>
                <dd>{formatDate(operation.updated_at)}</dd>
              </div>
              {operation.approved_by ? (
                <div>
                  <dt>Approved by</dt>
                  <dd>{operation.approved_by}</dd>
                </div>
              ) : null}
            </dl>
            <JsonBlock label="Exact arguments" value={operation.arguments} />
            {unsafeArguments ? (
              <Notice kind="warning">
                These arguments contain numbers outside the browser's exact
                numeric range. The displayed values may be rounded. Approval and
                execution are blocked in this console; inspect the original
                request with a client that preserves numeric precision.
              </Notice>
            ) : null}
            {operation.state === "WAITING_APPROVAL" ? (
              <div className="approval-actions">
                <h3>Review required</h3>
                <p className="muted">
                  Approval authorizes this operation and its recorded arguments.
                </p>
                {canApprove && !selfApproval ? (
                  <>
                    <div className="action-row">
                      <button
                        className="button primary"
                        disabled={!!busy || unsafeArguments}
                        onClick={() => void act("approve")}
                      >
                        {busy === "approve"
                          ? "Approving…"
                          : "Approve operation"}
                      </button>
                      <button
                        className="button danger"
                        disabled={!!busy}
                        onClick={() => setRejectOpen((current) => !current)}
                      >
                        Reject
                      </button>
                    </div>
                    {rejectOpen ? (
                      <div className="reject-form">
                        <p>
                          This operation will be rejected and cannot execute. A
                          new request would require a new operation.
                        </p>
                        <div className="action-row">
                          <button
                            className="button danger"
                            disabled={!!busy}
                            onClick={() => void act("reject")}
                          >
                            {busy === "reject"
                              ? "Rejecting…"
                              : "Confirm rejection"}
                          </button>
                          <button
                            className="button secondary"
                            disabled={!!busy}
                            onClick={() => setRejectOpen(false)}
                          >
                            Cancel
                          </button>
                        </div>
                      </div>
                    ) : null}
                  </>
                ) : (
                  <Notice kind="info">
                    {selfApproval
                      ? "A different authorized reviewer must approve this request. You cannot approve your own operation."
                      : "An approver or administrator must review this request."}
                  </Notice>
                )}
              </div>
            ) : null}
            {operation.state === "READY" &&
            (identity.role === "admin" ||
              (identity.role === "operator" &&
                operation.actor_id === identity.id)) ? (
              <div className="execution-actions">
                <p className="muted">
                  The operation is ready. Execution calls the configured
                  downstream service.
                </p>
                <button
                  className="button primary"
                  disabled={!!busy || unsafeArguments}
                  onClick={() => void act("execute")}
                >
                  <Icon name="invoke" />
                  {busy === "execute" ? "Executing…" : "Execute operation"}
                </button>
              </div>
            ) : null}
            {operation.result !== undefined ? (
              <JsonBlock label="Recorded result" value={operation.result} />
            ) : null}
            {operation.state === "UNKNOWN" ? (
              <Reconciliations
                api={api}
                operation={operation}
                identity={identity}
              />
            ) : null}
            <details className="request-details">
              <summary>Request identity</summary>
              <dl>
                <dt>Idempotency key</dt>
                <dd className="mono">{operation.idempotency_key}</dd>
                <dt>Arguments hash</dt>
                <dd className="mono">{operation.arguments_hash}</dd>
              </dl>
            </details>
            <div className="event-heading">
              <h3>Event timeline</h3>
              <span className="count-label">{events.length}</span>
            </div>
            {events.length ? (
              <ol className="event-timeline">
                {events.map((event) => (
                  <li key={event.id}>
                    <span className="event-marker" />
                    <div>
                      <strong>
                        {event.type.replaceAll("_", " ").toLowerCase()}
                      </strong>
                      <time dateTime={event.created_at}>
                        {formatDate(event.created_at)}
                      </time>
                      <span className="event-actor">
                        {event.actor_id || "Gateway"}
                      </span>
                      {event.data && Object.keys(event.data).length ? (
                        <details>
                          <summary>Event details</summary>
                          <pre>{JSON.stringify(event.data, null, 2)}</pre>
                        </details>
                      ) : null}
                    </div>
                  </li>
                ))}
              </ol>
            ) : (
              <p className="muted">No events returned for this operation.</p>
            )}
          </>
        )}
      </div>
    </section>
  );
}
