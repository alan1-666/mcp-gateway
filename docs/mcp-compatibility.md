# MCP compatibility

Rillgate connects to existing MCP servers through reviewed tools and scoped client access. Compatibility is recorded per server, authentication profile and operation. A successful handshake alone does not prove that a published tool can execute.

## Connection checks

For remote Streamable HTTP servers, **MCP Servers → Connection diagnostics** initializes a connection and inspects the complete bounded catalog. If its definitions are supported, it closes that session, opens an independent session and compares the complete catalog again. Both sessions share the original configured timeout; the second check does not restart the budget. No `tools/call` is sent.

The optional `session_contract_status` field records:

| Value | Meaning |
| --- | --- |
| `stable` | Tool names and canonical input/output schema hashes matched in two observed sessions |
| `changed` | Tools were added, removed or their schema hashes changed; the check is `degraded` |
| `unverified` | The second connection/catalog could not be verified; the check is `failed` |
| `not_checked` | The initial catalog was not compatible/complete, or the target uses a Connector whose session lifecycle is not checked here |

Historical reports may omit the field and the console shows **Not checked**. A changed or missing original tool receives `session_contract_changed`; newly added tools change the catalog status without falsely marking unchanged tools incompatible. Reports retain the bounded first catalog, not a potentially oversized union of both catalogs.

The comparison preserves `const`, `default`, numeric precision and all accepted schema constraints. Changes in whitespace, object-key order, descriptions or advisory annotations do not change execution contract identity. The check cannot determine whether a difference is caused by a session binding, a deployment or another upstream behavior. Two matching observations do not guarantee future stability: import, publication and execution retain their existing live checks.

Fixed diagnostic codes distinguish `authentication_rejected`, `check_timeout`, `check_cancelled`, `upstream_rate_limited`, `upstream_unavailable`, `endpoint_incompatible` and a generic connection/discovery failure. No upstream error body, raw schema, session ID or credential value is exposed. The check never retries a business action or changes an active tool definition.

## Named-provider evidence — 2026-10-08

| Target | Authentication | Observed result | Scope |
| --- | --- | --- | --- |
| [Cloudflare documentation MCP](https://developers.cloudflare.com/agents/model-context-protocol/cloudflare/servers-for-cloudflare/) | Public, no account | Two tools stable across independent sessions; official SDK query through an isolated Gateway succeeded | Complete discovery, persisted diagnostic receipt, reviewed import/publication, grant filtering, schema read, one public query, idempotency and revocation |
| [Microsoft Learn MCP](https://learn.microsoft.com/en-us/training/support/mcp) | Public, no account | All three observed tools changed contracts across independent sessions | Fresh-session diagnostic correctly reports `degraded`; the existing execution drift gate continues to block a stale reviewed schema |
| Authorized Sport test integration | Existing restricted test integration | Earlier cloud.23 SDK read acceptance passed on 2026-10-06 | Historical business evidence, not a new observation or a guarantee of arbitrary vendor compatibility |
| Authenticated vendor MCP via OAuth | Pre-registered client profile | Implemented and fixture-tested; real vendor consent not yet accepted | Requires a named provider/client and actual consent/refresh/revocation acceptance |

See [public-provider check evidence](evidence/mcp-public-compatibility-2026-10-08.json), [Cloudflare SDK evidence](evidence/cloudflare-public-mcp-2026-10-08.json), and [verification](verification.md). Public checks run locally with fixed allowlists; Cloudflare has not been enabled in production. Production network policy remains unchanged. This work detects Microsoft Learn's current incompatibility; it does not claim to fix session-bound tools by ignoring schema constraints.

## Reproduce

Normal CI uses deterministic SDK/HTTP/PostgreSQL fixtures and makes no public-provider calls. For explicitly chosen public checks:

```sh
RUN_EXTERNAL_MCP_COMPATIBILITY=1 \
  EXTERNAL_COMPATIBILITY_EVIDENCE=/absolute/path/public-compatibility.json \
  go test -race -count=1 -v ./tests/integration \
  -run '^TestExternalPublicMCPCompatibility$'

RUN_EXTERNAL_MCP_TEST=1 \
  TEST_DATABASE_URL='postgres://user@127.0.0.1:5432/dedicated_test?sslmode=disable' \
  EXTERNAL_MCP_EVIDENCE=/absolute/path/cloudflare-sdk.json \
  go test -race -count=1 -v ./tests/integration \
  -run '^TestExternalCloudflareDocs$'
```

Evidence paths are absolute because Go test runs in the package directory. The SDK acceptance requires a loopback test database, creates an isolated schema and removes it afterward; it never uses a cloud account or production database. The public query is fixed in source, and evidence excludes raw returned documents and credentials. Provider availability is not a CI release gate. These checks are not a load benchmark.

## Next compatibility work

Real OAuth consent and additional client/vendor profiles remain separate acceptance items. Session-bound contracts need an explicit binding and reviewed logical contract before they can be supported; generic connection reuse alone does not resolve persisted schema identity. Future pooling and catalog caching must preserve live permission/credential changes, schema checks and the prohibition on uncertain-write replay.
