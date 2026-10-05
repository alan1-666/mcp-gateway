# Implementation status

The [product design](product-design.md) and [system architecture](architecture.md) describe the intended production system. This page describes implemented source and the separate acceptance required before a release is considered ready. Historical cloud evidence remains in [verification](verification.md).

## Implemented

| Area | Working behavior |
| --- | --- |
| Services and persistence | Separate Go API, MCP gateway, recovery worker and migrations; PostgreSQL immutable operation snapshots, transactional states/events and checked migrations |
| Accounts and team | Invitation-only cloud accounts, hashed passwords, CSRF-protected sessions, personal keys, member access/revocation and independent approving users |
| Machine clients | Separate client identities and expiring one-time-issued keys; explicit read/invoke scopes and tool/server grants; version-checked edits/rotation, live revocation and snapshot-aware dispatch authorization |
| Registry and releases | Manual HTTP registration and reviewed MCP draft imports; candidate diffs, immutable definition history, optimistic publication, retirement and compatible rollback through a new version |
| Execution | Prepare → independent approval for writes → atomic claim → HTTP/MCP dispatch → durable result; canonical idempotency checks and conservative recovery |
| Uncertainty | Interrupted or ambiguous writes remain `UNKNOWN`; independent reviewers append immutable outcome evidence with concurrent-update protection, without replaying the call or rewriting its state |
| Network and secrets | Deployment origin/CIDR allowlists, checked DNS dialing, blocked redirects/metadata addresses; static-file or AES-GCM-encrypted workspace/origin-bound credentials with dynamic rotation/disablement |
| Connection diagnostics | Safe policy/authentication/connection/discovery/compatibility stages, per-tool reports and persisted bounded check history; no business tool calls during a check |
| Discovery | Full authorized lexical catalog, bounded keyset pages, context-bound cursors and separate summaries/schema reads across REST, MCP and Pi; client grants applied before count/limit |
| MCP transport | Official Go SDK v1.7.0, five governed client-facing tools; remote Streamable HTTP, bounded complete discovery, aliases, explicit-risk imports, schema-drift checks and server disable gates |
| MCP responses | Original structuredContent schema checks, object/array projection, common-secret filtering, text regeneration, string/null cursor preservation and final byte limits; pure sample preview and versioned edits |
| Operations and audit | Filtered operation/audit keyset pages with live counts; role/workspace/client visibility; independent append-only reconciliation and secret-free configuration audit metadata |
| Capacity | Database-backed workspace/client/upstream concurrency and per-minute admission budgets, 429/Retry-After or MCP tool errors, aggregate outcome/duration/rejection metrics |
| Console | Connections/credentials, clients, candidates/version history, policy preview/editing, approvals, filtered operations/audit, UNKNOWN evidence and capacity views connected to real APIs |
| Pi and tasks | Restricted subscription-backed CLI/cloud worker, durable platform Run leases, persistent sessions/intents, creator isolation, cancellation and explicit approval/uncertainty pauses |
| Release tooling | Committed-source packaging, source/migration checksums, recorded image IDs, explicit deployment/compatible rollback and interrupted metadata repair; separately versioned operations bundles preserve backup/monitor tooling across application rollback |
| Recovery tooling | Encrypted database/configuration/secret bundles, independent backup key, optional checksum-verified SSH off-host transfer and isolated PostgreSQL restore verification; no automatic pruning |
| Health collection | Host collector for execution/admission/UNKNOWN signals and backup/restore receipts; optional generic, Feishu or WeCom webhook delivery with cooldown, recovery messages and safe failure handling |

## Development order

The five packages below now have implementation and local regression coverage. They are no longer a pending feature list. Exact-commit remote CI, the new cloud release and its affected browser/API journeys remain release gates; external integrations require their own configured destination and evidence.

| Package | Implemented outcome | Acceptance still to close |
| --- | --- | --- |
| Engineering baseline | Reproducible committed-source artifacts, migration/image checks, backup before replacement, explicit rollback and metadata repair | Remote CI for the release commit; exercise adoption/deployment and a compatible rollback against the actual host, then record the release tuple |
| 1. Connections and credentials | Encrypted managed references, live rotation/revocation, safe checks and compatibility history | Cloud onboarding/rotation checks; isolated browser wrong-secret → rotate → successful check already passed |
| 2. Clients and tool access | Default-deny machine keys, scopes and tool/server grants; current and snapshot access fences | Cloud client/grant/revocation acceptance; isolated browser key issuance/clearing and default denial already passed |
| 3. Tool changes and publication | Candidate review, immutable history, publication, retirement and compatible rollback | Cloud candidate publication; isolated browser diff/publication, incompatible rollback rejection, compatible v3 rollback and v4 retirement passed |
| 4. Diagnostics and audit | Paginated filtered history, audit and independent UNKNOWN evidence | Cloud visibility/evidence checks; isolated browser UNKNOWN append without redispatch and audit filtering passed |
| 5. Capacity and recovery | Scoped admission, aggregate metrics, encrypted recovery/restore tooling and configured alert delivery | New-release/monitoring smoke and post-upgrade restore checks; pre-upgrade schema-005 encrypted restore passed. Off-host transfer awaits a second destination; alerts await a webhook |

The earlier baseline passed both push/PR Verify and release-tooling checks and merged through PR 1 into `main` (`1163b157`), whose main CI also passed; the five-package change still needs its own remote checks. The shared delivery process is in [development](development.md#delivery-workflow). Local verification passed the complete Go race suite, 62 console tests, 31 runner tests, type checks/build and all 39 Python tests. The collector's fixed read-only SQL also passed against a real dedicated PostgreSQL database. Isolated browser version/evidence/capacity/audit journeys passed, and a pre-upgrade encrypted cloud snapshot restored into a network-isolated database. These results do not establish deployment of the new packages or delivery to an external destination; no external webhook was called.

## Remaining production architecture

- Enterprise OIDC/OAuth interoperability, MFA, self-service account recovery and short-lived Runner identity.
- Multiple organization/workspace membership, database RLS and separate service database accounts.
- Signed application/tool releases, staged rollout, instance acknowledgements and broader version-compatibility policies.
- OpenAPI/Protobuf import, gRPC, upstream MCP OAuth and an outbound private-network Connector with isolated stdio execution.
- Semantic ranking, service/environment filters and actor/resource/field-level access policies.
- Distributed Runner control, multi-host checkpoint storage and organization-wide model budgets.
- Adapter-specific business outcome queries, compensation and upstream idempotency guarantees.
- Large-result object storage, distributed cache, durable outbox consumers and management-event streaming.
- Structured evaluation datasets, telemetry dashboards, load/SLO measurements and live model quality evaluation.
- Kubernetes/high availability, point-in-time recovery, automated retention/deletion and independently exercised disaster recovery.

## Current constraints

- Cloud accounts belong to one workspace. Invitations are bearer links; email ownership is not verified. Client keys are separate identities, not enterprise SSO or unrestricted delegated user access.
- The five new packages are implemented and locally checked; the last recorded deployed feature set remains the dated cloud release in [verification](verification.md) until a new rollout entry is added.
- The host topology is single-server. An encrypted local snapshot shares that host's failure domain. A second backup destination and an external alert webhook have not been supplied, so automatic off-host recovery protection and external notification are not yet commissioned.
- Backups contain PostgreSQL, cloud configuration and the secret directory including the vault master key. Pi configuration/session volumes, nginx and certificate state need their own coordinated recovery handling. The independent backup key must be escrowed separately. Snapshots are not automatically pruned; production retention/deletion remains future work.
- HTTP tools require bounded JSON and static paths. Remote MCP supports Streamable HTTP JSON/SSE responses to the original POST and text/structured results; no legacy SSE, standalone streams/resumption, upstream OAuth or stdio. See [remote MCP integration](remote-mcp-contract.md).
- Server registration is admin-only and capped at 100 per workspace. Credentials cannot expand the deployment's origin/CIDR allowlist. Managed credential changes resolve on new attempts without restart; static file/network-policy changes still need service replacement.
- Normal discovery is complete or fails: 1000 tools, 100 pages, 4 MiB aggregate, 1 MiB per protocol response, 64 KiB per schema. Diagnostics can explain incompatible definitions, but do not authorize partial import. Every execution repeats discovery; no pool, cache or background synchronization is implemented.
- All write tools require independent approval with a 30-minute expiry. Human reconciliation retains the operation's `UNKNOWN` state; a confirmed human finding is separate evidence, not a new adapter result.
- Tool pages contain at most 50 records; operation/audit/reconciliation pages at most 100. Event polling uses batches of 500 and an exclusive `after` cursor. Counts and permission filters remain live. See [pagination](tool-discovery-contract.md).
- Reviewed candidates can update schema/risk/bindings and roll back definitions. They cannot restore an upstream schema that no longer exists, rewrite old operation snapshots, provide gradual traffic rollout or bypass disabled-server/client gates.
- MCP output_schema validates original structuredContent. Without a business postcondition, `SUCCEEDED` verifies the adapter/result contract rather than independently proving a downstream business effect.
- Projection permits up to 32 object/array selectors and a final 1–128 KiB envelope (64 KiB default). It reduces stored/model-facing data, not upstream transfer. Secret-name filtering is not comprehensive DLP or per-user field authorization; arbitrary text may contain sensitive values.
- Capacity metrics are aggregate counts and duration totals, not latency percentiles or a full telemetry backend. Configurable webhook delivery is a host-operated alert facility, not a general event delivery platform.
- Browser Agent tasks use an operator-configured server Pi subscription. Per-user provider accounts, self-service model onboarding, live task-quality evaluation and multi-host Pi durability remain pending. Gateway access itself does not need a model account.
