# Rillgate implementation status

The [product design](product-design.md) and [system architecture](architecture.md) describe the intended production system. This page describes implemented source and the separate acceptance required before a release is considered ready. Historical cloud evidence remains in [verification](verification.md).

## Implemented in source

| Area | Working behavior |
| --- | --- |
| Services and persistence | Separate Go API, MCP gateway, recovery worker and migrations; PostgreSQL immutable operation snapshots, transactional states/events and checked migrations |
| Accounts and team | Invitation-only cloud accounts, hashed passwords, CSRF-protected sessions, personal keys, member access/revocation and independent approving users |
| Machine clients | Separate client identities and expiring one-time-issued keys; explicit read/invoke scopes and tool/server grants; version-checked edits/rotation, live revocation and snapshot-aware dispatch authorization |
| Registry and releases | Manual HTTP registration and reviewed MCP draft imports; candidate diffs, immutable definition history, optimistic publication, retirement and compatible rollback through a new version |
| Execution | `call_tool` / `POST /api/v1/call` wraps preparation and READY execution; existing two-step interfaces remain; administrator-managed, versioned `required` / `none` policy, conservative legacy defaults, live rechecks and canonical idempotency |
| Uncertainty | Interrupted or ambiguous writes remain `UNKNOWN`; independent reviewers append immutable outcome evidence with concurrent-update protection, without replaying the call or rewriting its state |
| Network and secrets | Deployment origin/CIDR allowlists, checked DNS dialing, blocked redirects/metadata addresses; static-file or AES-GCM-encrypted workspace/origin-bound credentials with dynamic rotation/disablement |
| Upstream OAuth | Pre-registered public/confidential clients, protected-resource and issuer discovery, PKCE S256 + RFC 9207, browser-bound callbacks, encrypted grants, cross-process refresh claims, revocation fences and reconnect controls; see [supported profile](upstream-oauth.md) |
| Private Connectors | Outbound HTTPS polling, independently revocable hashed credentials, frozen local target fingerprints, durable claim/start/result jobs, private HTTP and digest-pinned rootless Podman stdio, local journal, no side-effect replay, cloud result validation/projection before storage; see [supported profile](private-connectors.md) |
| Connection diagnostics | Safe policy/authentication/connection/discovery/compatibility stages, per-tool reports and persisted bounded check history; no business tool calls during a check |
| Catalog change review | Complete discovery compared with registered versions; schema/description drift, unimported/missing tools, last 50 successful reports and audit; hash/version-guarded refresh candidates with explicit review/publication |
| Scheduled catalog checks | Opt-in per-server cadence, two bounded worker consumers, durable fenced leases, failure backoff, safe outcomes, admin revision checks and source-labeled history; see [scheduling](catalog-scheduling.md) |
| Discovery | Service-filtered lexical ranking with exact/prefix/fragment/phrase/all-term reasons, rank-aware cursor v2 and separate summaries/schema reads across REST, MCP and Pi; client grants applied before count/limit; fixed 1000-tool bilingual evaluation |
| MCP transport | Official Go SDK v1.7.0, seven governed client-facing tools (`search_tools`, `get_tool_schema`, `call_tool`, `get_operation`, `read_result`, `prepare_action`, `invoke_tool`); remote Streamable HTTP, bounded complete discovery, aliases, explicit-risk imports, schema-drift checks and server disable gates |
| MCP responses | Original structuredContent schema checks, object/array projection, common-secret filtering, text regeneration, string/null cursor preservation and final byte limits; pure sample preview and versioned edits; opt-in MCP structured-result artifacts with expiry, quota and authorized UTF-8 chunk reads |
| HTTP reliability | At most two attempts for read-only GET transient connection/502/503/504 failures within the original deadline; bounded Retry-After and process-local circuit breaker; no write/MCP/Connector replay |
| Operations and audit | Filtered operation/audit keyset pages with live counts; role/workspace/client visibility; independent append-only reconciliation and secret-free configuration audit metadata |
| Capacity | Database-backed workspace/client/upstream concurrency and per-minute admission budgets, 429/Retry-After or MCP tool errors, aggregate outcome/duration/rejection metrics |
| Public website | English and Simplified Chinese static pages at `/en/` and `/zh/`, remembered language selection, localized metadata, routing diagram, interactive execution/outcome illustration and responsive layout; independently bundled workspace at `/console/` and legacy invitation forwarding |
| Console | Real-API views for connections, credentials, clients, releases, response policy, approvals, operations, audit, UNKNOWN evidence and capacity; gateway-focused navigation, access-key connection checks/configuration, service search filter, invocation policy and large-result reader; separate detail tabs with retained drafts |
| Pi and tasks | Restricted subscription-backed CLI/cloud worker, durable platform Run leases, persistent sessions/intents, creator isolation, cancellation and explicit approval/uncertainty pauses |
| Release tooling | Committed-source packaging, source/migration checksums, recorded image IDs, explicit deployment/compatible rollback and interrupted metadata repair; separately versioned operations bundles preserve backup/monitor tooling across application rollback |
| Recovery tooling | Encrypted database/configuration/secret bundles, independent backup key, optional checksum-verified SSH off-host transfer and isolated PostgreSQL restore verification; no automatic pruning |
| Health collection | Host collector for execution/admission/UNKNOWN signals and backup/restore receipts; optional generic, Feishu or WeCom webhook delivery with cooldown, recovery messages and safe failure handling |

## Current delivery — 2026-10-06

**Source versus deployment:** the new call facade, optional approval policy, ranked service discovery, result artifacts and HTTP GET reliability are present in the working source. They require exact-source CI and release acceptance before being described as deployed. The following cloud descriptions summarize earlier recorded releases, not acceptance of these new changes.

The product brand is **Rillgate**; MCP gateway describes its function. The public website, workspace, browser identity, CLI help and current product documentation use this name. The public site is [rillgate.cn](https://rillgate.cn/), with the workspace at [/console/](https://rillgate.cn/console/) and MCP endpoint at `https://rillgate.cn/mcp`. Domain TLS, canonical redirects and certificate renewal are configured independently of application releases.

The core Gateway flow is working in the cloud: connect an upstream → discover and review tools → publish a version → authorize a client → execute through policy → inspect the outcome. The public website explains tool onboarding, on-demand discovery, scoped access, independent write approval and recorded outcomes. A local illustration shows successful execution and uncertain writes requiring human evidence; response projection is a supporting feature. The example does not call a real business service or claim a preinstalled ticket integration.

English and Simplified Chinese pages are available at `/en/` and `/zh/`; `/` remembers a selected language. The authenticated team workspace remains at `/console/`. The Rillgate brand was introduced in cloud.16 and language support in cloud.17; later releases refine the website's copy and product story. Application release IDs, source commits and bounded acceptance evidence are recorded in [verification](verification.md) and the corresponding release pull requests. Both existing upstreams retain hourly catalog checks. The full production architecture is not complete.

| Workstream | Delivered | Next gap |
| --- | --- | --- |
| Third-party access | Remote Streamable HTTP, managed credentials, pre-registered OAuth, discovery/import and connection diagnostics; public documentation and an authorized private test integration have each supplied three published tools | Additional OAuth registration profiles, legacy transports and more private-target compatibility evidence |
| Tool governance | Explicit client grants, immutable versions, candidate diffs, approval, retirement and reviewed rollback | Staged publication and broader access policies |
| Response control | Source includes schema checks, projection and opt-in expiring PostgreSQL result artifacts; prior cloud evidence covers inline projection | Release acceptance of artifact retrieval; measured production workloads and larger object storage remain separate |
| Operational visibility | Recorded operations, audit, scoped admission, catalog comparison, retained review history and scheduled checks | Telemetry/SLO dashboards and scheduler-specific alerting |
| Pi compatibility | Existing subscription-backed runner and original five-tool interface remain available | Workbench, model-account and orchestration expansion are outside gateway-core delivery |
| Cloud delivery | Known-source releases, encrypted local backups, isolated restore verification and local health collection | Independent off-host backup destination, external alert webhook, actual application rollback and HA |
| Product website | Public English homepage, independent `/console/` entry, responsive layout, local sample interaction and invitation compatibility; released with cloud route/session/browser acceptance | Product feedback and broader usability acceptance |
| Console redesign | Grouped navigation, compact overview, server and tool detail tabs, lazy retained panels, mobile navigation and a shared light theme implemented and locally accepted | Broader usability feedback and future feature views |

## Development order

The original five packages are implemented: connections/credentials, client access, tool publication, diagnostics/audit, and capacity/recovery. Their cloud.8 acceptance is historical evidence, not the current deployment. Subsequent proxy recovery, private integration and catalog-review releases are recorded chronologically in [verification](verification.md).

The console redesign is deployed and cloud-verified. Scheduled catalog checks are deployed and cloud-verified. The public website is deployed and cloud-verified, with its own workspace route and backward-compatible invitation entry. Upstream OAuth is implemented for the documented pre-registered client profile; real third-party consent acceptance is still pending. Private Connector implementation includes HTTP targets and isolated stdio; deployment and acceptance scope is recorded per release in verification. Gateway product capabilities take priority over enterprise account expansion. Off-host recovery and external alert delivery still need real destinations before they can be commissioned. Use the [delivery workflow](development.md#delivery-workflow) for every package; completion of one package does not establish readiness for all production scenarios.

### Gateway product priorities

The [Gateway roadmap](gateway-roadmap.md) defines the core release. The corresponding source changes are implemented; exact-source CI, cloud deployment and named real-service checks remain separate acceptance gates:

1. Existing-client onboarding: copyable endpoint configuration, supplied-key identity/MCP discovery check, and first-call instructions
2. Service-filtered, explainably ranked discovery and a fixed 1000-tool evaluation with documented misses
3. `call_tool`, compatible existing invocation tools, explicit administrator-managed approval policies and immutable intents
4. Structured MCP result projection plus bounded, expiring, permission-checked result retrieval
5. Eligible HTTP GET retry/circuit breaking, durable outcome metrics and repeatable reliability tests

Each package includes its own negative, revocation, compatibility and recovery checks. Pi remains an optional evaluation client. Standalone client products, Agent workbench expansion, multi-agent orchestration and model-account management are outside the first complete Gateway release. Existing task code remains maintained; these decisions do not imply removal or new deployment.

Enterprise SSO/MFA, multi-organization membership and account expansion are deferred. Existing permissions and approval gates continue to protect gateway operations.

## Remaining production architecture

- Enterprise OIDC/OAuth interoperability, MFA, self-service account recovery and short-lived Runner identity.
- Multiple organization/workspace membership, database RLS and separate service database accounts.
- Signed application/tool releases, staged rollout, instance acknowledgements and broader version-compatibility policies.
- OpenAPI/Protobuf import, gRPC, additional upstream OAuth registration profiles, streaming Connector transport and broader stdio sandbox profiles.
- Semantic ranking, verified environment metadata/filters and actor/resource/field-level access policies; exact MCP service filtering is implemented in source.
- Distributed Runner control, multi-host checkpoint storage and organization-wide model budgets.
- Adapter-specific business outcome queries, compensation and upstream idempotency guarantees.
- General object/file storage, distributed cache, durable outbox consumers and management-event streaming; current result artifacts are bounded PostgreSQL structured JSON.
- Structured evaluation datasets, telemetry dashboards, load/SLO measurements and live model quality evaluation.
- Kubernetes/high availability, point-in-time recovery, automated retention/deletion and independently exercised disaster recovery.

## Current constraints

- Cloud accounts belong to one workspace. Invitations are bearer links; email ownership is not verified. Client keys are separate identities, not enterprise SSO or unrestricted delegated user access.
- Cloud.16 is deployed from `9dc23b702d613a7d45e54241caa5dcebf85f610d`, applying the Rillgate name to the public website, workspace, browser identity and CLI help. Exact-source push/PR checks and bounded cloud brand/routing/auth/UI acceptance passed. The preceding cloud.11 acceptance includes a projected external MCP read; cloud.14 includes actual scheduled discovery with no business calls or definition mutations; cloud.15 includes account login and invitation-route acceptance. Brand acceptance changed no business tools or client grants and created no new accounts. The release sequence remains in [verification](verification.md). Full cloud administrative-mutation coverage and a real application rollback are not claimed.
- The host topology is single-server. An encrypted local snapshot shares that host's failure domain. An authorized second backup destination and an external alert webhook are still pending; no snapshot or backup-key copy to another host has completed. Automatic off-host recovery protection and external notification are not yet commissioned.
- Backups contain PostgreSQL, cloud configuration and the secret directory including the vault master key. Pi configuration/session volumes, nginx and certificate state need their own coordinated recovery handling. The independent backup key must be escrowed separately. Snapshots are not automatically pruned; production retention/deletion remains future work.
- HTTP tools require bounded JSON and static paths. Remote MCP supports Streamable HTTP JSON/SSE responses to the original POST and text/structured results; no legacy SSE or standalone streams/resumption; isolated stdio is available only through the documented Linux rootless-Podman Connector profile. OAuth requires the documented pre-registered client, PKCE and issuer-response profile. See [remote MCP integration](remote-mcp-contract.md).
- Server registration is admin-only and capped at 100 per workspace. Credentials cannot expand the deployment's origin/CIDR allowlist. Managed credential changes resolve on new attempts without restart; static file/network-policy changes still need service replacement.
- Normal discovery is complete or fails: 1000 tools, 100 pages, 4 MiB aggregate, 1 MiB per protocol response, 64 KiB per schema. Diagnostics can explain incompatible definitions, but do not authorize partial import. Every execution repeats discovery; no pool, cache or background synchronization is implemented.
- Legacy write tools require independent approval with a 30-minute expiry. The new source supports explicit versioned administrator exemptions and optional approval for reads; unknown policy values fail closed, and existing pending approvals remain bound. Deployment determines when these policy endpoints become available. Human reconciliation retains the operation's `UNKNOWN` state; a confirmed human finding is separate evidence, not a new adapter result.
- Tool pages contain at most 50 records; operation/audit/reconciliation pages at most 100. Event polling uses batches of 500 and an exclusive `after` cursor. Counts and permission filters remain live. See [pagination](tool-discovery-contract.md).
- Reviewed candidates can update schema/risk/bindings and roll back definitions. They cannot restore an upstream schema that no longer exists, rewrite old operation snapshots, provide gradual traffic rollout or bypass disabled-server/client gates.
- MCP output_schema validates original structuredContent. Without a business postcondition, `SUCCEEDED` verifies the adapter/result contract rather than independently proving a downstream business effect.
- Projection permits up to 32 object/array selectors and a 1–128 KiB inline envelope (64 KiB default). Opt-in MCP artifacts allow the complete projected envelope up to 1 MiB, retain only structured JSON for 60–86400 seconds, and enforce 100 MiB / 1000 retained artifacts per workspace. Reads recheck permission and expiry; there is no arbitrary file storage or model-generated compaction. Projection reduces stored/model-facing data, not upstream transfer. Secret-name filtering is not comprehensive DLP or per-user field authorization; arbitrary text may contain sensitive values.
- Capacity metrics are aggregate counts and duration totals, not latency percentiles or a full telemetry backend. Configurable webhook delivery is a host-operated alert facility, not a general event delivery platform.
- Browser Agent tasks use an operator-configured server Pi subscription. Per-user provider accounts, self-service model onboarding, live task-quality evaluation and multi-host Pi durability remain pending. Gateway access itself does not need a model account.

## Scoped quality gates

CI collects Go coverage with `-coverpkg=./...` across unit/integration execution and fails when a declared file in `scripts/go-coverage-targets.json` has less than 90% statement coverage. This is not Go branch coverage or a whole-repository percentage. Frontend TypeScript logic has aggregate thresholds of 95% lines and 90% branches/functions; `.tsx` UI components are excluded and require separate interaction acceptance. Local test evidence and cloud acceptance are recorded independently; adding a gate does not establish that an untested release passed it.
