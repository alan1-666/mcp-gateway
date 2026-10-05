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
| Catalog change review | Complete discovery compared with registered versions; schema/description drift, unimported/missing tools, last 50 successful reports and audit; hash/version-guarded refresh candidates with explicit review/publication |
| Scheduled catalog checks | Opt-in per-server cadence, two bounded worker consumers, durable fenced leases, failure backoff, safe outcomes, admin revision checks and source-labeled history; see [scheduling](catalog-scheduling.md) |
| Discovery | Full authorized lexical catalog, bounded keyset pages, context-bound cursors and separate summaries/schema reads across REST, MCP and Pi; client grants applied before count/limit |
| MCP transport | Official Go SDK v1.7.0, five governed client-facing tools; remote Streamable HTTP, bounded complete discovery, aliases, explicit-risk imports, schema-drift checks and server disable gates |
| MCP responses | Original structuredContent schema checks, object/array projection, common-secret filtering, text regeneration, string/null cursor preservation and final byte limits; pure sample preview and versioned edits |
| Operations and audit | Filtered operation/audit keyset pages with live counts; role/workspace/client visibility; independent append-only reconciliation and secret-free configuration audit metadata |
| Capacity | Database-backed workspace/client/upstream concurrency and per-minute admission budgets, 429/Retry-After or MCP tool errors, aggregate outcome/duration/rejection metrics |
| Public website | Dedicated static product homepage, routing diagram, local response-projection illustration and real documentation links; independently bundled workspace at `/console/`, legacy invitation forwarding and responsive layout |
| Console | Real-API views for connections, credentials, clients, releases, response policy, approvals, operations, audit, UNKNOWN evidence and capacity; grouped navigation and separate detail tabs with retained drafts |
| Pi and tasks | Restricted subscription-backed CLI/cloud worker, durable platform Run leases, persistent sessions/intents, creator isolation, cancellation and explicit approval/uncertainty pauses |
| Release tooling | Committed-source packaging, source/migration checksums, recorded image IDs, explicit deployment/compatible rollback and interrupted metadata repair; separately versioned operations bundles preserve backup/monitor tooling across application rollback |
| Recovery tooling | Encrypted database/configuration/secret bundles, independent backup key, optional checksum-verified SSH off-host transfer and isolated PostgreSQL restore verification; no automatic pruning |
| Health collection | Host collector for execution/admission/UNKNOWN signals and backup/restore receipts; optional generic, Feishu or WeCom webhook delivery with cooldown, recovery messages and safe failure handling |

## Current delivery — 2026-10-06

The core Gateway flow is working in the cloud: connect an upstream → discover and review tools → publish a version → authorize a client → execute through policy → project the response → inspect the operation. The current application release is `20261006-cloud.14` (source `e1e2f14`), adding scheduled catalog checks in [PR 7](https://github.com/alan1-666/mcp-gateway/pull/7) to the redesigned console. All six exact-source push/PR checks and nine bounded cloud scheduling checks passed, including exact completion cadence after the timestamp consistency fix. Both existing upstreams now have hourly catalog checks; each completed through the actual cloud worker with unchanged tool definitions and zero business calls. The prior console merge also passed its exact [main CI](https://github.com/alan1-666/mcp-gateway/actions/runs/37305209692). Cloud acceptance is bounded to the journeys recorded in [verification](verification.md); the full production architecture is not complete.

| Workstream | Delivered | Next gap |
| --- | --- | --- |
| Third-party access | Remote Streamable HTTP, managed credentials, discovery/import and connection diagnostics; public documentation and an authorized private test integration have each supplied three published tools | Upstream OAuth, outbound private Connector, stdio and additional protocols |
| Tool governance | Explicit client grants, immutable versions, candidate diffs, approval, retirement and reviewed rollback | Staged publication and broader access policies |
| Response control | Schema validation, object/array projection, cursor preservation, bounded results and sample preview | Large-result storage and measured production workloads |
| Operational visibility | Recorded operations, audit, scoped admission, catalog comparison, retained review history and scheduled checks | Telemetry/SLO dashboards and scheduler-specific alerting |
| Pi tasks | Subscription-backed cloud runner, durable task leases, cancellation and approval pauses | Per-user model accounts, distributed runtime and quality evaluation |
| Cloud delivery | Known-source releases, encrypted local backups, isolated restore verification and local health collection | Independent off-host backup destination, external alert webhook, actual application rollback and HA |
| Product website | Public English homepage, independent `/console/` entry, local sample interaction and invitation compatibility implemented; local responsive/browser, invitation, regression and build verification passed | Cloud acceptance and release evidence |
| Console redesign | Grouped navigation, compact overview, server and tool detail tabs, lazy retained panels, mobile navigation and a shared light theme implemented and locally accepted | Broader usability feedback and future feature views |

## Development order

The original five packages are implemented: connections/credentials, client access, tool publication, diagnostics/audit, and capacity/recovery. Their cloud.8 acceptance is historical evidence, not the current deployment. Subsequent proxy recovery, private integration and catalog-review releases are recorded chronologically in [verification](verification.md).

The console redesign is deployed and cloud-verified. Scheduled catalog checks are deployed and cloud-verified. The public website is being accepted separately from the existing gateway runtime. The next access capability is upstream OAuth. Off-host recovery and external alert delivery still need real destinations before they can be commissioned. Use the [delivery workflow](development.md#delivery-workflow) for every package; completion of one package does not establish readiness for all production scenarios.

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
- Cloud.14 is deployed from `e1e2f14317575319b09d2255cab598359daadd7b`, including scheduled catalog checks, the console redesign and all preceding catalog/proxy fixes. Exact-source push/PR checks and bounded cloud scheduling/UI acceptance passed. The preceding cloud.11 acceptance includes a projected external MCP read. Scheduling acceptance added no business calls or production tool-definition mutations. The release sequence and historical findings remain in [verification](verification.md). Full cloud administrative-mutation coverage and a real application rollback are not claimed.
- The host topology is single-server. An encrypted local snapshot shares that host's failure domain. An authorized second backup destination and an external alert webhook are still pending; no snapshot or backup-key copy to another host has completed. Automatic off-host recovery protection and external notification are not yet commissioned.
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
