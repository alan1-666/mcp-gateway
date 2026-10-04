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
- Order by `created_at DESC, id DESC` with keyset pagination. Subsequent pages retain the first request's creation-time upper bound.
- Validate cursors against the current workspace, actor, role, normalized query and registry/discovery scope. Reusing a cursor with a different limit is allowed.
- Cursors are pagination positions, not authorization grants. Validate their structure and bounds and apply current access checks to every page. An unsigned cursor does not claim tamper resistance.
- Pages are not a frozen database snapshot: newly disabled tools disappear immediately; publication can change the live result set. Restart discovery to include tools created after the original boundary.
- Registry and discovery cursors are not interchangeable. A malformed or mismatched cursor produces `invalid_input` (HTTP 400 or an MCP tool error).

## Core and client interfaces

Go: `ToolSearchInput{Query, Cursor string; Limit int}`. `Service.SearchTools(ctx, actor, input)` returns a registry `ToolPage`; `Service.DiscoverTools(ctx, actor, input)` returns a `ToolDiscoveryPage` of summaries. Both pages expose `Items`, `NextCursor`, and `Total`. Repository search receives an explicit visibility scope from the service. Transport code never filters a limited in-memory list.

TypeScript Gateway clients: `search(query, signal?, options?)` returns `{items: ToolSummary[], next_cursor?: string, total: number}`, where options has optional `cursor` and `limit`. The local client uses `/catalog/tools`; the leased cloud client retains its existing internal path. Pi's `search_tools` accepts optional `cursor` and `limit`, returns the page unchanged, and never exhausts the catalog automatically.

The console queries the server, offers Load more, clears page state on a new query, discards stale responses, and fetches tool details by ID. Invocation selection also searches the complete published catalog. A failed next-page request retains existing results and can be retried. Counts must distinguish loaded records from all matches.

## Acceptance coverage

- More than 500 tools: old matches remain searchable; a full page traversal has no duplicates or omissions in an unchanged catalog.
- Equal creation times, case and Chinese queries, literal wildcard characters, bounds, invalid and cross-context cursors.
- Drafts, disabled tools and other workspaces excluded from discovery, including when the caller is an administrator. Live disablement and identity revocation take effect on later requests.
- Equivalent results over REST, an actual MCP SDK client, and the leased Node worker client. Summaries never include schemas or connection details.
- Console query reset, Load more, stale response isolation, error retry, selection of an older tool, and small-screen rendering.

Each change is verified before deployment. Broader import, versioning, response projection and upstream protocol adapters remain separate Gateway capabilities.
