# MCP catalog consistency

Rillgate separates **published definitions**, **remote observations** and **execution checks**. An upstream notification invalidates an observation; it cannot publish a definition, change an approval policy or authorize a business call.

## Sources of truth

| Data | Lifetime and isolation | Permitted use |
| --- | --- | --- |
| Published tool version | Immutable PostgreSQL definition, scoped to a workspace | Reviewed parameters, risk, result policy and execution binding; live permissions and enabled state are still checked |
| Catalog review history | Last 50 successful observations per server, with source and timestamps | Human comparison and candidate review; historical evidence, never proof that the upstream is currently unchanged |
| Execution observation | Complete catalog read on one exclusive session lease | Verify the named tool against the published schema immediately before one business call |
| SDK internal catalog cache | SDK-owned and invalidated by its notification handler | Not used by gateway execution; Rillgate explicitly invokes `tools/list` for every execution |

No gateway catalog TTL cache is enabled. Adding one later requires the same identity/server/credential/grant scope as session retention, bounded memory and lifetime, explicit stale status, invalidation on configuration or credential changes, and a fresh verification path for execution/import/publication. Missed notifications must not make cached definitions authoritative. Review history is not silently relabeled as a fresh catalog.

## Changes on the supported transport

The gateway observes complete `notifications/tools/list_changed` messages in SSE responses to the original HTTP POST. Its bounded wire reader processes notifications before exposing those bytes to the SDK, avoiding a race with asynchronous SDK handlers. JSON tool text, unrelated notifications, custom SSE event names and incomplete frames are not catalog notifications. LF, CRLF, CR, multiline data and a leading BOM follow the supported SSE framing profile.

Each transport has a revision counter. Complete catalog inspection starts at one revision; a notification increments that revision and clears its verified state. A change during any pagination stage rejects the entire observation. Diagnostics return `catalog_changed` with no partial compatible-tool count. Discovery/import/refresh and scheduled comparison cannot use rejected pages as a successful catalog.

The final `tools/call` dispatch checks verified state under the same mutex used by invalidation. A notification observed before this check blocks network dispatch. Read and write operations fail with a rediscovery instruction; a write is not marked `UNKNOWN` when this guard proves it was never sent. No automatic rediscovery loop or business-call retry is introduced.

After dispatch, a notification does not undo an external action or rewrite a confirmed result. A valid result can still succeed; an unconfirmed write remains `UNKNOWN`. The invalidated execution session is discarded instead of returned as healthy to the pool. The next independently authorized operation reads a new complete catalog.

## Boundaries

- Notifications are hints scoped to the receiving connection. Other sessions perform their own live checks; there is no cross-workspace/global notification cache.
- Standalone GET/SSE, stream resumption and modern subscription-listen channels remain unsupported. This is not a continuously connected push service. Silent changes are caught by the next live schema check, not guaranteed notification delivery.
- An upstream can change after dispatch; revision checks do not provide a transactional snapshot or exactly-once business effects in that upstream.
- Connector discovery retains its fixed binding and complete-result validation; this release does not add streaming notifications.
- Notifications do not automatically import, refresh or publish tools, change grants, expand egress policy or solve Microsoft Learn's session-dependent schemas.
- Bounds remain 100 pages, 1,000 tools, 4 MiB aggregate catalog and 1 MiB per original protocol response. Notifications consume the same wire-response budget; no background queue is added.

Protocol reference: MCP [tool-list notifications](https://modelcontextprotocol.io/specification/2025-11-25/server/tools#list-changed-notification) and [Streamable HTTP responses](https://modelcontextprotocol.io/specification/2025-11-25/basic/transports). See [session management](mcp-sessions.md), [scheduled review](catalog-scheduling.md) and [release verification](verification.md) for related evidence.
