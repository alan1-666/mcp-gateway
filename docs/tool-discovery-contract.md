# Tool discovery contract

This work follows the on-demand discovery design in [Uber Engineering's MCP Gateway article](https://www.uber.com/jp/en/blog/designing-mcp-gateway/). A search returns a bounded page; the client fetches a selected tool's schema separately.

## Transport contract

| Entry point | Visibility and payload |
| --- | --- |
| `GET /api/v1/tools` | Registry page: administrators may see drafts and disabled tools; other roles see only enabled, published tools. Items retain the existing Tool fields. |
| `GET /api/v1/catalog/tools` | Discovery page: all identities, including administrators, see only enabled, published tool summaries. |
| MCP `search_tools` | Same discovery page and rules. |
| `GET /internal/runner/runs/{id}/gateway/tools` | Same discovery page, after checking the current Run lease and creator permissions. |

Inputs are `query` (optional, trimmed, at most 200 UTF-8 bytes), `limit` (optional integer from 1 to 50) and `cursor` (optional opaque string, at most 2048 bytes). Omitted limits default to 50 for the registry and 25 for discovery. Explicit zero, negative, fractional, repeated or unknown URL query parameters are invalid. Matching is a case-insensitive literal substring of name plus description; `%`, `_` and backslash have no wildcard meaning. This is lexical search, not semantic search.

Responses have `{ "items": [...], "next_cursor": "...", "total": 123 }`. `next_cursor` is omitted or empty on the final page. `total` describes visible matches within the page's creation-time boundary before its position cursor; it can change following live publication or disablement. Discovery items contain only `id`, `name`, `description`, `risk` and `version`; no schema, downstream URL or credential reference is returned. Summary descriptions are at most 512 UTF-8 bytes, with an ellipsis when shortened; the detail endpoint retains the full description. This bounds encoded discovery pages below the Pi client's 256 KiB response limit even with HTML/JSON escaping.

## Pagination and access

- Filter by workspace, current visibility and query in PostgreSQL before limiting results. There is no 500-tool preselection.
- Machine clients also require a live unexpired key, enabled client, `tools:read` scope and an explicit tool grant or the imported tool's server grant. Apply this predicate before counting and limiting. Schema reads use the same live grants; `tools:invoke` is additionally required for preparation/execution. Clients cannot administer or approve tools.
- Order by `created_at DESC, id DESC` with keyset pagination. Subsequent pages retain the first request's creation-time upper bound.
- Validate cursors against the current workspace, actor, role, normalized query and registry/discovery scope. Reusing a cursor with a different limit is allowed.
- Cursors are pagination positions, not authorization grants. Validate their structure and bounds and apply current access checks to every page. An unsigned cursor does not claim tamper resistance.
- Pages are not a frozen database snapshot: newly disabled tools disappear immediately; publication can change the live result set. Restart discovery to include tools created after the original boundary.
- Registry and discovery cursors are not interchangeable. A malformed or mismatched cursor produces `invalid_input` (HTTP 400 or an MCP tool error).

## Core and client interfaces

Go: `ToolSearchInput{Query, Cursor string; Limit int}`. `Service.SearchTools(ctx, actor, input)` returns a registry `ToolPage`; `Service.DiscoverTools(ctx, actor, input)` returns a `ToolDiscoveryPage` of summaries. Both pages expose `Items`, `NextCursor`, and `Total`. Repository search receives an explicit visibility scope from the service. Transport code never filters a limited in-memory list.

TypeScript Gateway clients: `search(query, signal?, options?)` returns `{items: ToolSummary[], next_cursor?: string, total: number}`, where options has optional `cursor` and `limit`. The local client uses `/catalog/tools`; the leased cloud client retains its existing internal path. Pi's `search_tools` accepts optional `cursor` and `limit`, returns the page unchanged, and never exhausts the catalog automatically.

The console queries the server, offers Load more, clears page state on a new query, discards stale responses, and fetches tool details by ID. Invocation selection also searches the complete published catalog. A failed next-page request retains existing results and can be retried. Counts must distinguish loaded records from all matches.

## Operation and audit history

Tool catalog pagination and operational history use separate contracts:

| Route | Filters and access |
| --- | --- |
| `GET /api/v1/operations` | Exact `state`, `tool_id`, `actor_id`, inclusive `from`/`to`; administrators and approvers see workspace records, operators/viewers see their own, clients additionally pass live grants |
| `GET /api/v1/audit` | Exact `actor_id`, `action`, `resource_id`, inclusive `from`/`to`; administrators only |
| `GET /api/v1/operations/{id}/reconciliations` | History of one visible operation, with the same ownership and client checks as its detail endpoint |

These routes use `limit` 1–100 (default 50) and an opaque `cursor` of at most 4096 bytes. The response is `{items,total,next_cursor?}`. Time filters accept RFC3339 with optional nanoseconds, years 1970–9999, and require `from <= to`. Unknown or repeated query fields are invalid. Exact identifier filters are at most 128 UTF-8 bytes without NUL/CR/LF.

History is ordered by immutable `created_at DESC, id DESC`, with the first request's creation-time upper bound retained across pages. Cursors bind the history kind, workspace, actor, role, client/key identity and normalized filters; changing only the page size is allowed. Count and page selection share a database statement. `total` is the current authorized count inside the creation-time boundary before the position cursor. A later state change or revoked permission can change that count and remove rows; these pages are not a frozen database snapshot. Start over to include later-created records. Operation update timestamps are not ordering keys.

History cursors cannot be interchanged with tool catalog, check-history or version-history cursors. MCP checks instead use an exclusive decimal-string `before` ID; versions use an exclusive integer `before` version. Audit and reconciliation IDs are decimal strings to preserve int64 precision in JavaScript.

Independent outcome verification appends reconciliation entries; it never changes the original `UNKNOWN` state or dispatches a tool. Evidence read permissions follow the operation. Audit payloads contain evidence record IDs/outcome, without copying submitted notes or evidence-reference text. See the [remote MCP contract](remote-mcp-contract.md#operational-history-and-uncertain-outcomes) for write permissions and optimistic concurrency.

## Acceptance coverage

- More than 500 tools: old matches remain searchable; a full page traversal has no duplicates or omissions in an unchanged catalog.
- Equal creation times, case and Chinese queries, literal wildcard characters, bounds, invalid and cross-context cursors.
- Drafts, disabled tools and other workspaces excluded from discovery, including when the caller is an administrator. Live disablement and identity revocation take effect on later requests.
- Equivalent results over REST, an actual MCP SDK client, and the leased Node worker client. Summaries never include schemas or connection details.
- Console query reset, Load more, stale response isolation, error retry, selection of an older tool, and small-screen rendering.

- History traversal beyond 200 records, stable ordering across later inserts, filter/cursor binding, live client revocation and independent append-only reconciliation are covered by PostgreSQL and HTTP tests.

Broader import, reviewed versions, response projection, encrypted credentials and upstream protocol behavior are described in the [remote MCP contract](remote-mcp-contract.md). Source-level verification does not imply that every capability has already been deployed to a particular environment.
