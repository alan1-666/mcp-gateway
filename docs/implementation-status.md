# Implementation status

The [product design](product-design.md) and [system architecture](architecture.md) describe the intended production system. This page describes code that exists and identifies the remaining work without treating architecture targets as delivered capabilities.

## Implemented

| Area | Working behavior |
| --- | --- |
| Services | Separate Go API, MCP gateway, operation recovery worker and migration commands |
| Persistence | PostgreSQL tool definitions, immutable operation snapshots, transactional states/events, checked migrations |
| Access | Invitation-only cloud accounts, hashed passwords, CSRF-protected sessions, expiring/revocable API keys, four roles and separate approving users |
| Team | Single-use invitations, member disablement/role changes, credential revocation and identity audit history |
| Registry | Manual HTTP registration and reviewed MCP tool imports; JSON Schema validation, disabled drafts, explicit publication and database-backed tool/server disable checks |
| Execution | Prepare → approve when needed → atomic claim → HTTP or remote MCP execution → durable result; idempotency conflict checks |
| Uncertainty | Ambiguous writes and interrupted dispatches become `UNKNOWN`; no automatic side-effect replay |
| HTTP adapter | Fixed origins and credentials, checked DNS dialing, blocked redirects/metadata addresses, bounded JSON results and basic secret field redaction |
| Discovery | Database-backed literal search over the full visible catalog; bounded keyset pages, context-bound cursors, live visibility and separate summaries/schema reads across REST, MCP and Pi |
| MCP client entry point | Official Go SDK v1.7.0, stateless Streamable HTTP, five governed discovery/execution tools |
| Upstream MCP | Administrator server registry; remote Streamable HTTP, complete bounded tools/list pagination, namespaced aliases, explicit-risk draft imports, schema-drift checks and live server disable gates |
| MCP responses | Original structuredContent schema validation, object/array field selection, common-secret redaction, text regeneration, valid cursor preservation and final byte limits; uncertain writes remain UNKNOWN |
| Console | Version-checked MCP response policy editing and sample preview; MCP server registration/discovery/import/enable controls; server-side catalog search, cursor pagination and schema-on-selection; prepare/execute controls, approval inbox, operation details and event history; cloud sign-in and team/account administration |
| Pi | Subscription-backed CLI and cloud worker with restricted tools, persistent sessions/intents, bounded events and explicit approval/unknown pauses |
| Agent tasks | PostgreSQL Run queue, leases/fencing, creator isolation, live role checks, cancellation, explicit resume and browser task/event views |
| Delivery | Locked dependencies, source startup, Compose configuration, backend/container build definitions, OpenAPI contract and CI checks |

## Development order

The order below is the next delivery plan, based on the current invitation-only team workspace. These are pending work packages with acceptance targets, not implemented features. Each package follows the [delivery workflow](development.md#delivery-workflow) and includes backend, console, failure handling and evidence. Connection diagnostics begin with the first package and continue as later controls are added.

| Order | User outcome and scope | Acceptance target |
| --- | --- | --- |
| Engineering baseline | Resolve the first remote Go test failure, then integrate the existing feature branch through GitHub PR/CI; make release and rollback steps repeatable and tied to one commit | Verify the remote result for the exact merge commit, record the cloud release tuple, and exercise an application rollback with compatible migrations in an isolated environment |
| 1. Connections and credentials | Admin-managed credential references, protected secret storage, rotation/revocation and configuration activation; connection-stage diagnostics and a tool compatibility report | Connect a credential-protected MCP fixture, distinguish authentication from transport/schema failure, rotate its secret, verify activation and absence of secrets in UI/logs; report unsupported definitions explicitly |
| 2. Clients and tool access | Client identities, scoped API keys and explicit Server/Tool grants built on existing roles/workspace isolation | Two clients see/use only their granted tools; guessed IDs cannot bypass checks; revoked access blocks an already prepared operation at dispatch |
| 3. Tool changes and publication | Upstream schema differences, reviewable candidate versions, compatible publication, retirement and controlled rollback | Detect a contract change, review and publish the candidate, preserve prior snapshots and check live upstream compatibility before allowing a rollback |
| 4. Diagnostics and audit | Correlated connection/call stages, health history, searchable paginated operations, configuration audit queries and evidence-based UNKNOWN reconciliation | Distinguish wrong credentials, schema drift, timeout and oversized results; find records older than the current 200-item window; record human findings without rewriting evidence or replaying a write |
| 5. Capacity and recovery | Workspace/client/upstream concurrency and rate budgets, metrics, external alerts, retention and encrypted off-host backup | A slow upstream cannot exhaust another's allocation; limits produce clear results; restore identities, tool versions and operations in an independent environment without replaying writes |

Existing safeguards remain relevant: request IDs and JSON logs, a process-wide concurrency bound, edge IP throttles, bounded payloads, health checks and host-local database backups already exist. The planned work adds operational detail and isolation to those foundations.

Upstream OAuth, private-network Connector/stdio, OpenAPI/gRPC import, richer MCP capabilities, result artifacts and enterprise identity remain in the complete architecture. Schedule a separate package for each against a named integration need and compatibility contract. OAuth may move forward when the selected real upstream requires it; it must not silently change the supported authentication claims. Performance targets and live model quality remain explicit acceptance work, measured on fixed workloads.

## Remaining production architecture

- Enterprise OIDC/OAuth interoperability, MFA, self-service account recovery and short-lived Runner identity.
- Multiple organization/workspace membership, database RLS and per-service database accounts.
- Full tool version lifecycle, signed releases, instance acknowledgements and rollback.
- OpenAPI/Protobuf import, gRPC, upstream MCP OAuth and outbound private-network Connector with isolated stdio execution.
- Semantic ranking, service/environment discovery filters and configurable field-level access policies.
- Distributed gRPC Runner control, short-lived runner identities, multi-host checkpoint storage and organization-wide budget policies.
- Result-query adapters, verified human outcome reconciliation and compensation workflows.
- S3 artifacts, Redis limits/cache, durable outbox consumers, management-event SSE and webhook delivery.
- Structured evaluation datasets, production release gates, telemetry dashboards and performance measurements.
- Kubernetes deployment, off-host disaster recovery, point-in-time recovery, retention/deletion jobs and signed image delivery.

The cloud deployment adds a concrete operational baseline; single-host persisted Pi sessions do not establish the remaining production capabilities. Release readiness must be based on completed acceptance tests against the [architecture's targets](architecture.md).

## Current constraints

- Cloud accounts currently belong to one workspace. Invite possession grants the selected role; email ownership is not verified.
- Cloud Agent tasks require an operator-configured server Pi subscription. Self-service model account onboarding, per-user provider accounts and live model behavior evaluation remain pending. The Gateway can also serve external MCP clients.
- Backups are host-local, and the deployment has no high-availability or external alert delivery.

- HTTP tools require bounded JSON and static paths. Remote MCP supports Streamable HTTP with bounded JSON or SSE responses to the original POST, plus text/structured results. Legacy HTTP+SSE, standalone streams/resumption, upstream OAuth and stdio are not supported. See [remote MCP integration](remote-mcp-contract.md).
- Upstream servers are registered by administrators, capped at 100 per workspace and use static origin/CIDR allowlists plus workspace/origin-bound credential references. These are deployment configuration, not self-service network policy.
- Upstream discovery is complete or fails: maximum 1000 tools, 100 pages, 4 MiB cumulative result bytes, 1 MiB per protocol response and 64 KiB per schema. A single unsupported tool definition rejects the discovery. Each execution repeats live discovery; no connection pool, catalog cache or background synchronization is implemented.
- All write tools require independent approval. Approval lasts 30 minutes.
- Tool registry and discovery page through the full visible inventory with a maximum of 50 records per page. Operations still list the latest 200, and events read in batches of 500 using an `after` cursor. See [tool discovery](tool-discovery-contract.md) for cursor and live-visibility semantics.
- Imported MCP response policies can be revised with optimistic versions, audit history and immutable operation snapshots. General schema/risk/binding edits, version rollout and rollback remain unavailable. Changed MCP schemas block execution; reimporting a different hash, risk or response policy cannot overwrite the existing tool.
- Without an output schema, success verifies the supported transport/result contract rather than independent business postconditions. For MCP, output_schema validates original structuredContent before projection. Business outcome reconciliation still requires an explicit contract and, when appropriate, a future outcome-query adapter.
- MCP projection accepts up to 32 object/array selectors and a final envelope limit of 1–128 KiB (64 KiB default). A full nonfinal `*` traverses all array elements while retaining order and cardinality; missing fields fail the whole result. Policy previews use supplied samples, not live upstream calls. It bounds model-facing/stored data, not upstream transfer, and provides no large-result artifact store.
- Secret field filtering is not comprehensive DLP or actor-specific field authorization. Arbitrary text may contain sensitive values. Argument/result retention requires further policy implementation.
- MCP resource, prompt, sampling and resumable transport features are not advertised.
- The Pi CLI does not claim distributed task durability. It refuses to redispatch uncertain operations and requires the operator to inspect their state.
