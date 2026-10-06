# Upstream MCP OAuth

Rillgate acts as an OAuth client of a remote MCP server. This connection is shared
by the tools imported from that server in the current workspace. Gateway client
keys and the operator's model subscription are independent of this connection.

## Supported connection profile

- HTTPS Streamable HTTP servers, authorization code with PKCE S256, RFC 9207
  issuer identification (`authorization_response_iss_parameter_supported=true`), and a
  pre-registered client (`none` or `client_secret_basic` authentication)
- Protected resource discovery through the server's Bearer challenge, then
  path-specific and root well-known metadata; OAuth and OIDC issuer discovery
- Exact resource URL and issuer binding, explicit scopes, bounded JSON responses,
  and the existing origin/CIDR/DNS egress policy for every server-side request
- Encrypted client secrets, PKCE verifier, access and refresh tokens
- A fixed HTTPS callback, ten-minute authorization attempts bound to the initiating
  administrator's browser session, one-time state and code exchange
- Persisted refresh claims shared by the API, gateway and worker; an interrupted
  or rejected exchange requires reconnection instead of reusing a rotated token
- No automatic replay of an MCP call after an authentication error

Register `https://<public-origin>/api/v1/mcp/oauth/callback` at the provider. Allow
the MCP resource, metadata, issuer, authorization and token endpoint origins in
`HTTP_ALLOWED_ORIGINS`. Redirects and implicit HTTP proxying remain disabled.
The gateway does not automatically register clients or request broader scopes.
Client ID metadata documents, dynamic registration, device authorization and
automatic step-up authorization are separate, currently unsupported profiles.

## API contract

All management routes require the existing administrator permission. Cloud
management also requires a browser session and the existing mutation CSRF check.
OAuth cannot be combined with a server's static `credential_ref`.

| Method | Path under `/api/v1` | Input / output |
| --- | --- | --- |
| GET | `/mcp/servers/{id}/oauth` | Status below, `unconfigured` with version 0 when absent |
| POST | `/mcp/servers/{id}/oauth/discover` | `{issuer?: string}` → metadata below |
| PUT | `/mcp/servers/{id}/oauth` | `{expected_version, issuer, client_id, auth_method, client_secret?, scopes: string[]}` → status |
| POST | `/mcp/servers/{id}/oauth/connect` | `{expected_version}` → `{authorization_url, expires_at}` |
| POST | `/mcp/servers/{id}/oauth/disconnect` | `{expected_version}` → status |
| GET | `/mcp/oauth/callback` | Provider's state/code/error and required matching `iss`; fixed redirect to `/console/?oauth=connected` or `oauth=reconnect_required` |

Metadata: `resource`, `issuers`, `issuer`, `authorization_endpoint`,
`token_endpoint`, `scopes_supported`, `auth_methods`, `redirect_uri`

Status: `server_id`, `version`, `status`, `redirect_uri`, optional `configuration`
(`issuer`, `client_id`, `auth_method`, `scopes`), optional `expires_at`, `updated_at`
Statuses: `unconfigured`, `disconnected`, `pending`, `exchanging`, `connected`,
`refreshing`, `reconnect_required`. Expired attempts and abandoned exchanges are
reported as `reconnect_required`. Secrets and upstream error bodies never appear
in status, errors or audit records. A stale version returns 409 and must be read
again before a mutation; disabled servers cannot connect or exchange tokens.

Saving configuration replaces the previous grant and requires a new connection.
Starting a new authorization replaces the previous attempt and grant. Disconnect
erases the locally stored grant; it does not revoke the provider-side consent.
The OAuth configuration remains, preventing a silent fallback to anonymous calls.
Disabling a server invalidates its grant and pending authorization. A request
already dispatched can finish; revocation cannot undo an upstream action.

## Runtime and recovery

Tokens are only attached to the configured MCP endpoint, including its path.
Expiry is checked before opening a new MCP session with a 30-second refresh
margin. Refreshing an unknown-expiry token is not guessed: authentication rejection
requires reconnection. Concurrent callers wait within their existing operation
deadline for the single persisted refresh claim. Tokens without a refresh token
require reconnection on expiry. An authorization or refresh exchange is never
retried after an ambiguous network failure. Completion uses a version fence, so
disconnect, configuration changes and server disable cannot restore an old grant.

Audit records cover configuration, connect, exchange completion, refresh and
disconnect, with status/version only. Reverse proxies must omit callback access
logs to avoid storing authorization codes and state from the query string.

The additive migration can coexist with old tables, but app versions without
OAuth support must not serve configured OAuth upstreams after rollback: they
cannot enforce the fail-closed contract. Disable those servers before rolling
back across this capability boundary.

Protocol references: [MCP authorization specification (2025-11-25)](https://modelcontextprotocol.io/specification/2025-11-25/basic/authorization), [OAuth mix-up defenses (RFC 9700)](https://www.rfc-editor.org/rfc/rfc9700.html#section-4.4.2). Providers without RFC 9207 issuer responses are rejected by this shared-callback profile
