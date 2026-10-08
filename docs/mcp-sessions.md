# MCP execution sessions

Rillgate can reuse an upstream Streamable HTTP execution session within one exact identity and credential scope. Discovery, import, refresh and connection diagnostics remain independent observations. Session reuse reduces repeated MCP handshakes and permits TCP reuse; it does not cache tool definitions or rewrite a reviewed schema.

## Configuration

API and Gateway processes enable retention by default:

| Setting | Default | Boundary |
| --- | --- | --- |
| `MCP_SESSION_POOL_SIZE` | `32` | Retained sessions per process; `0` disables retention, maximum `256` |
| Idle timeout | 60 seconds | Successful release starts the idle interval; periodic cleanup closes expired sessions |
| Maximum lifetime | 10 minutes | Measured from creation; successful operations never renew it |
| Teardown | At most 2 seconds per network DELETE | Independent sessions close concurrently during process shutdown |

The shared Compose environment forwards the size setting. Invalid startup configuration fails closed. API, Gateway and Worker have separate process-local pools; there is no shared or distributed session store. Library callers explicitly enable retention before serving requests and close the adapter before its database dependencies.

The size limit controls **retained sessions**, including initialization and active retained leases. It does not replace operation capacity budgets. A busy or full pool uses a disposable independent connection; it never shares an active session or introduces a post-claim wait queue. Existing database-backed workspace/client/upstream concurrency and rate budgets still govern admitted operations.

## Isolation and live checks

Pool identity includes workspace, actor ID and role, machine client and key IDs, exact server configuration and timestamps, credential reference/version/headers and the OAuth grant version where present. Identity digests stay in memory and are never API data, metrics labels or logs. Gateway bearer tokens and browser cookies are never forwarded upstream.

Before reuse, the adapter resolves current credentials and OAuth state. Before every retained-session network request it checks the live server configuration and credential version; OAuth transports independently check grant status, version and expiry. Rotation to identical header values still changes a managed credential version and requires a new session. Revocation or policy changes block subsequent requests. An already dispatched business action is not retroactively undone.

Each lease is exclusive and permits at most one `tools/call`. It performs complete bounded discovery and compares the live tool with the persisted schema and reviewed hash immediately before the call. The result is consumed and validated before a healthy lease returns to the pool. Changing contracts, invalid responses, authentication failures, cancellation and timeouts discard that session. Captured wire responses and replay guards are reset only between successful exclusive leases.

Connectors, locally approved custom transports and actors without a stable ID use their existing isolated lifecycle. An OAuth provider without an explicit grant-version identity also uses disposable sessions. Startup hooks must not be reconfigured while requests are running.

## Recovery and outcome safety

An upstream may expire a negotiated session. If a **reused** session returns HTTP 404 during catalog discovery, Rillgate retires it and establishes one fresh session within the original operation deadline. The new session must pass the same complete catalog and reviewed-contract checks. Fresh-session failures, authentication errors and other discovery failures are not automatically retried.

Once `tools/call` has been attempted, the operation is never replayed or resumed. Unconfirmed writes remain `UNKNOWN`; the next independently authorized operation may establish a new session. This is not an exactly-once guarantee in the upstream business system.

Idle cleanup and shutdown send a bounded best-effort DELETE. If credentials or server configuration have been revoked, cleanup must not reuse stale authority just to send DELETE; the local session is closed and upstream expiry remains the provider's responsibility. Initializing sessions are cancelled if the pool closes. Sessions do not survive process replacement.

The protocol basis is the MCP [Streamable HTTP session lifecycle](https://modelcontextprotocol.io/specification/2025-11-25/basic/transports#session-management). Rillgate's supported transport profile still excludes standalone SSE, stream resumption, legacy HTTP+SSE and arbitrary inbound capabilities.

## Compatibility boundary

Microsoft Learn's observed schemas contain a changing session binding. Execution pooling alone cannot make a contract imported from an independent discovery session valid in a later execution session. The gateway continues to reject that mismatch; no `const`/`default` constraint is removed and no reviewed argument is silently changed. See [compatibility evidence](mcp-compatibility.md).

Tests cover actual stateful MCP session reuse, independent concurrent connections, identity/key/role and credential/grant-version isolation, actual TCP reuse, live credential/server fences, expiry, initialization/shutdown races, pre-call session recovery, schema drift, non-replay and PostgreSQL client-grant revocation. Runtime release acceptance is recorded separately in [verification](verification.md).
