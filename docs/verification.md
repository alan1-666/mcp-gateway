# Verification record

Verified locally on 2026-10-04 using macOS arm64, Go 1.26.8, Node.js 24.4.1 and PostgreSQL 16.9. Tests used a dedicated local database and temporary HTTP endpoints, not company systems.

| Check | Outcome |
| --- | --- |
| Go unit and real PostgreSQL integration tests with the race detector | Passed |
| Concurrent preparation and dispatch | Repeated preparation shares one operation; 12 simultaneous execute requests produce one downstream write |
| Approval and access checks | Independent approval, expiry, disabled tools, actor/workspace isolation and schema validation passed |
| Interrupted write | Lost downstream response becomes `UNKNOWN`; subsequent execution does not send the write again |
| Persistence and recovery | A reconstructed service reads committed results; stale dispatch recovers to `UNKNOWN`; late completion is rejected |
| MCP interoperability | Official SDK client connects to the actual HTTP handler, discovers five tools, prepares an operation and executes it |
| Go static analysis | `go vet ./...` passed |
| TypeScript and console production build | Passed |
| Pi worker/client tests | 22 checks passed: authentication, durable intents/events, missing credentials, heartbeat loss/cancellation, approval resume, missing checkpoint refusal, stale-attempt archival, limits and transport failures; no model calls |
| Console state tests | 14 checks passed, including string cursors, deduplication, attempt isolation, controls and persistent creation idempotency |
| Cloud task persistence | Real PostgreSQL race tests passed for concurrent admission, atomic operation binding rollback, lease fencing, creator revocation, budgets, cancellation and expiry |
| Cross-language worker integration | Production Node worker/client → Go HTTP/cloud auth → isolated PostgreSQL passed: read execution, independent write approval/resume with one downstream write, credential gating and cancel fencing |
| Real Pi configuration check | Local subscription OAuth and selected model configuration found; no model request sent |
| Browser checks | Login, invalid form input, role controls, empty states, logout/reload credential clearing and 390px layout passed; no console errors or warnings |
| Dependency audit | No known npm vulnerabilities after upgrading Pi to 1.0.2 and Vite to 7.3.6 |
| Docker Compose | Cloud backend, console and Pi worker images built and deployed on Ubuntu 24.04; API, Gateway, PostgreSQL and console health checks passed; Pi worker runtime heartbeat verified |
| Deployed cloud worker | Actual Pi daemon with an empty dedicated login volume reports model configuration missing; public HTTPS task creation/idempotency, WAITING_CREDENTIALS, explicit resume, cancellation and CSRF rejection passed; internal runner path returns 404 publicly |
| Cloud browser | Login, session restoration across service replacement, team/account views and Agent task creation → waiting credentials → cancellation passed. Real event history, cleared draft, logout/401 and 390px layout verified; zero browser errors/warnings. No model or business tool call |
| Certificate renewal | Staging dry-run renewal succeeded with the final nginx webroot; scheduled renewal service and timer verified |
| Cloud identity | One-time/expired-state handling, CSRF rejection, role checks, member/credential revocation, logout and password rotation covered by PostgreSQL tests |
| Public HTTPS and MCP | Trusted IP certificate; real public login, invitation acceptance, MCP initialize/tool discovery and immediate revocation passed |
| Go vulnerability scan | govulncheck v1.8.0 reports zero affected symbols and zero imported-package findings after Go, pgx and x/text updates; advisory matches remain in unused module packages |
| Backup and recovery | Scheduled dump generated and restored into a separate temporary database; migrations and unclaimed owner invitation verified |

## Cloud subscription verification — 2026-10-05

- Completed a fresh Pi `openai` OAuth authorization on the cloud host. The dedicated Pi configuration volume retains the login across worker replacement.
- Applied explicit `PI_PROVIDER=openai` and `PI_MODEL=gpt-5.5` overrides. After replacement, the worker heartbeat reported online and model ready with no configuration error.
- Made one live request from the deployed Pi container using the project's `loadConfiguredModel` and Pi `completeSimple`. It returned exactly `CLOUD_PI_OK`, with `stopReason=stop`, 16 input tokens and 19 output tokens. No tools or project data were supplied, no API key was used, and automatic retries were disabled.
- Closed the temporary loopback SSH callback tunnel after authorization. Credentials were not printed, exported from the laptop, or added to the repository.
- This checks subscription authentication and live inference. It does not establish production task quality, tool-selection accuracy, remaining quota, or load capacity.

## Paginated tool discovery — 2026-10-05

Implemented and deployed as `20261005-cloud.5`, following the on-demand discovery direction in [Uber Engineering's MCP Gateway article](https://www.uber.com/jp/en/blog/designing-mcp-gateway/). See the [transport contract](tool-discovery-contract.md).

| Check | Outcome |
| --- | --- |
| Full Go suite with race detector and real Node cloud worker | Passed against the dedicated PostgreSQL test database; affected packages rerun after the final response-size fix |
| Go static analysis | `go vet ./...` passed |
| PostgreSQL discovery | 524-record regression: old matches beyond 500, tied timestamps, literal wildcard/Chinese matching, cursor validation and live visibility passed |
| REST and official MCP client | 520 published tools plus hidden/foreign records: equivalent pages, complete traversal without repeats/omissions, role/workspace isolation, revocation and invalid-input handling passed |
| Response limits | Maximum 50-item discovery pages with 4,000-byte descriptions containing JSON/HTML escape characters fit under 256 KiB; summaries retain at most 512 UTF-8 bytes and complete schemas remain separate |
| Production Pi clients and leased Node worker | Public and lease-scoped discovery agreed for older tools and a 50-item escaped-description page; approval/resume/cancellation integration remained passing |
| TypeScript | 31 Pi runner tests and 27 console tests passed; type checking and console production build passed |
| Browser, isolated local test database | 520 published + 1 draft + 1 disabled: overview totals, registry 50→100 and invocation 25→50 pagination, old-tool search/details/selection, rapid query replacement, hidden-tool empty state, and disabling the selected tool passed |
| Browser layout and logs | Registry and invocation had no document overflow at 390px. No application errors/warnings; unrelated wallet-extension warnings/errors were present in Chrome |
| OpenAPI | YAML parsed with duplicate-key checks; all 125 local references resolved |
| Cloud rollout | Pre-release database backup completed; migration 004 applied; API, Gateway, database and console healthy. Existing Pi OAuth volume preserved and worker reported online/ready with `openai / gpt-5.5` |

The populated browser checks used a disposable local schema, which was removed afterwards. No new cloud administrator was created and no synthetic inventory was inserted into the cloud workspace. Public HTTPS served the new console. Cloud authentication and tool discovery share the integration-tested handlers, but a populated cloud browser workflow was not repeated for this release. No live model or downstream business requests were made for this feature.

## Remote MCP aggregation and response projection — 2026-10-05

See the [remote MCP contract](remote-mcp-contract.md) for supported transports, credentials, limits and remaining compatibility boundaries.

| Check | Outcome |
| --- | --- |
| Complete Go suite | `RUN_CLOUD_WORKER_INTEGRATION=1 go test -race ./...` passed with real PostgreSQL and the Node worker; `go vet ./...` passed |
| Upstream protocol | Real official SDK servers covered paginated discovery, independent sessions, two-server routing, static credential isolation, JSON/SSE responses, oversized/malformed results and uncertain writes without replay |
| Nested MCP handshake | A real public MCP request through Gateway to an upstream server exposed inherited protocol context; fixed by isolating outbound context values while retaining cancellation/deadlines, with a regression test |
| Registry and admission | Workspace/admin isolation, draft imports, advisory read-only hints, duplicate imports, schema changes, server limits and server disablement gates passed |
| Projection | Nested object selection, escaping, common-secret redaction, raw-text replacement, preserved string/null cursors, numeric precision, missing fields and final byte limits passed; rejected write results remain `UNKNOWN` |
| End-to-end MCP and Pi | Official MCP client → Gateway → remote MCP → recorded projected result passed. Production Node Pi bridge → leased Go API → remote MCP passed alongside approval/resume/cancellation checks |
| TypeScript | 31 Pi runner tests and 39 console tests passed; both type checks and console production build passed |
| Browser with isolated local data | Two loopback SDK fixtures discovered both pages, imported drafts, published and routed same-named inventory tools to different sources. Selected result fields and cursor remained; internal note and original text were absent |
| Browser approval and disablement | Operator could not manage servers; write remained waiting until another identity approved. Fixture write count was zero before approval and one after execution. Server disablement removed its tools from the operator catalog; re-enable and rediscovery worked |
| Browser layout and logs | MCP server list and registration form had no document overflow at 390px. No application errors/warnings; unrelated wallet-extension messages were present |
| OpenAPI | Strict duplicate-key YAML parsing, all 179 local references and 19 unique operation IDs passed |
| Cloud rollout | `20261005-cloud.6` built and deployed after a database backup. Migration 005 applied; API, Gateway, database and console healthy; public trusted HTTPS served the new asset bundle. Pi remained online/model-ready with `openai / gpt-5.5` and preserved login/state volumes |

Browser acceptance for that release used a disposable local PostgreSQL schema and loopback-only synthetic MCP services; the schema, temporary identities and processes were removed afterwards. That acceptance created no cloud identities, contacted no company service and made no model calls. At the initial `20261005-cloud.6` rollout, the deployed MCP registry was empty and the existing egress policy was unchanged. The subsequent Microsoft Learn onboarding below records the first independently operated third-party integration.

## Microsoft Learn cloud integration — 2026-10-05

Verified against the deployed workspace at [the public HTTPS entry point](https://76.13.220.236/) and Microsoft's public Streamable HTTP endpoint `https://learn.microsoft.com/api/mcp`. Initial administrator setup used the normal invitation-acceptance flow; subsequent calls used normal HTTPS authentication and application APIs. No direct tool/operation database insertion was used for this acceptance.

| Check | Outcome |
| --- | --- |
| Upstream registration | Registered namespace `mslearn`, timeout 30000 ms, no credential reference. The exact `https://learn.microsoft.com` outbound origin was configured and loaded |
| Discovery and publication | Three tools imported from the currently discovered contracts with explicit risk `read`, then published using the normal management endpoints |
| Response policies | Both search tools select the whole `/results` field with `max_bytes:131072`; fetch uses the same byte limit without `include`, matching its text-only response |
| Documentation search | `microsoft_docs_search` with query `Azure Container Apps health probes` reached `SUCCEEDED` and returned 10 official documentation results |
| Code sample search | `microsoft_code_sample_search` with query `Azure Container Apps create container app` and `language:azurecli` reached `SUCCEEDED` and returned 10 results |
| Document fetch | `microsoft_docs_fetch` fetched the first search result, `https://learn.microsoft.com/azure/container-apps/health-probes`, and reached `SUCCEEDED` with document text |
| Governed execution | Each call went through the normal authenticated HTTPS prepare → execute flow and the Gateway's remote MCP adapter; all three completed successfully |
| Preparation idempotency | Repeating preparation with the same tool, arguments and idempotency key returned the same operation ID for each tool |
| Execution replay | Repeating execution of each operation returned the same recorded result; its event history contained exactly one `OPERATION_DISPATCHING` event |
| Cloud browser | Administrator sign-in succeeded; the live MCP Servers page showed the server enabled. Running Discover returned all three tools, each marked Imported. This confirms the populated cloud discovery view; execution was verified through the HTTPS API |

This is a real public-document integration, using no company data and making no model request. Selecting `/results` retains the complete result array; it is not evidence of per-item field reduction or a measured token saving. The operation ledger and replay checks establish one recorded Gateway dispatch per operation, not exactly-once effects inside the upstream service. Final wire-result byte sizes were not separately recorded.

## Versioned response policies and array projection — 2026-10-05

| Check | Outcome |
| --- | --- |
| Backend regression | Full `RUN_CLOUD_WORKER_INTEGRATION=1 go test -race ./...` passed against PostgreSQL; `go vet ./...` passed |
| Array projection | Multiple fields merge into each original element; nested/empty arrays, escaped names, large JSON integers and null leaves passed. Missing fields, inconsistent element types, overlapping paths and array/object conflicts fail the whole projection |
| Policy transactions | Concurrent updates have one winner; stale expected versions return 409 even for identical policies. Current identical policies do not increment versions. Publication and tool/server disablement remain intact |
| Operation snapshots | Real HTTP and official MCP SDK tests prepare and approve before an edit, then execute old/new policies at their recorded versions. Original output schema is checked before projection and successful/uncertain operations are not replayed |
| Pure preview | Validates a supplied MCP envelope and original output schema; no upstream request, operation write or audit write. Supported-envelope byte accounting, text metadata removal, bounded input and large integer precision passed |
| Console regression | 51 console tests and 31 Pi runner tests passed; both type checks and the production console build passed |
| Browser acceptance | Isolated local MCP fixture: preview, array field removal, cursor preservation, save/catalog version refresh, stale-save rejection, explicit reload retaining draft/sample, missing-field rejection and decimal precision rejection passed; no browser warnings or errors |
| Browser numeric safety | Rejects unsafe integers, decimal rounding such as 9007199254740991.1 and underflow such as 1e-400; ordinary decimal values and equivalent exponent notation remain accepted |
| API documentation | Strict duplicate-key YAML parsing, 200 local references and 21 unique operation IDs passed |
| Cloud rollout | Deployed `20261005-cloud.7` after a database backup; no schema changes. API, Gateway, PostgreSQL and console healthy. Existing account session and Pi configuration/state volumes retained |
| Cloud sample preview | Used the prior successful Microsoft Learn search result as the sample. Selecting `/results/*/title` and `/results/*/contentUrl` preserved all 10 entries; normalized supported-envelope size changed from 49,932 to 3,633 bytes (92.7% smaller) |
| Cloud policy editing | The signed-in administrator previewed and saved the policy in the deployed console, creating version 2; the catalog and detail view refreshed to the new version. Repeated preview showed the same byte sizes and no browser errors/warnings |
| Live projected execution | A fresh public documentation search through normal HTTPS prepare/execute reached `SUCCEEDED`, returned 10 title/link entries and matching regenerated text, and recorded version 2. Repeated preparation/execution returned the same operation/result with one recorded dispatch |

The disposable local browser schema, identities and service processes were removed after acceptance. The cloud check used the existing administrator account and public documentation only, with no model call. Code-sample search and document fetch policies were not changed.

Preview counts are normalized supported-envelope bytes, not raw network size or model tokens. The backend preserves JSON numbers; the browser rejects sample numbers it cannot submit without changing their decimal value.

## Team Gateway governance and recovery packages — 2026-10-05

**Source, isolated tests, feature/merge CI, cloud deployment and bounded cloud smoke checks passed. Remaining external and broader operational acceptance are tracked below.** The five packages add managed credentials/diagnostics, machine clients, reviewed tool releases, operational evidence and capacity/recovery controls. Historical cloud releases above retain their original scope; the cloud.8 evidence below records deployment of these additions.

| Check | Outcome |
| --- | --- |
| Backend regression | Final complete Go test rerun with race detector passed using `RUN_CLOUD_WORKER_INTEGRATION=1`; real PostgreSQL and the Node cloud-worker boundary were included. Type/static checks and production build passed |
| Credentials and diagnostics | Real PostgreSQL and authenticated official MCP fixtures covered encrypted storage, wrong key/authentication, rotation, disablement, workspace/origin isolation, fail-closed resolution and bounded compatibility/history reports; checks invoke no business tools |
| Machine clients | Default-deny scopes/grants, one-time issuance, rotation/expiry/revocation, guessed-ID/workspace isolation and current/snapshot binding authorization covered by backend regression |
| Tool lifecycle | Candidate creation/diffs, immutable definition history, concurrent publication, retirement, compatible rollback and preservation of old operation snapshots covered by backend regression |
| Operational evidence | More than 200 operations traverse without truncation; stable creation-time cursors, live filters/grants, audit visibility, concurrent reconciliation, independent reviewer checks and unchanged UNKNOWN/dispatch state passed |
| Capacity | Workspace/client/upstream admission bounds, explicit rejection semantics, isolation, terminal aggregates and generic failure HTTP 503 handling passed |
| Console and runner | 62 console tests and 31 Pi runner tests passed, including the added error-code mapping cases; type checks and console production build passed |
| Python delivery/recovery tools | All 39 tests passed, including release manifest/image/schema checks, rollback/metadata transition handling, encrypted backup validation, collector/webhook behavior and eight independent-operations installer cases. Shell syntax and diff checks passed |
| Independent operations bundle | Actual repository scripts installed into a temporary base; installed release `--help`, repeated-install no-op and restore passed. Tests also exercised installed `Host.backup` dynamic import plus GPG bundle validation, retained/legacy bundles, damaged-current recovery, locking/interruption and exclusion of secrets/configuration. This local installer test performed no cloud or systemd mutation |
| Collector database integration | Fixed read-only bounded collector SQL executed successfully against a dedicated PostgreSQL database, including unresolved UNKNOWN/evidence handling |
| Isolated browser: connections | Wrong credential → failed check → rotate → successful check and persisted check history passed using controlled fixtures |
| Isolated browser: clients | Default-deny behavior, one-time key display and key clearing passed |
| Isolated browser: versions | Reviewed schema diff and publication v1 → v2 passed; incompatible rollback returned 409; a compatible rollback published v3; retirement produced v4 and disabled the tool |
| Isolated browser: UNKNOWN | Appended independent evidence while retaining UNKNOWN and the existing four events; no business redispatch occurred |
| Isolated browser: capacity and audit | Saved workspace `*` limits of 2 concurrent/120 per minute; exact credential-rotation audit action filter returned its one matching record |
| Isolated browser: operator permissions | Administrative navigation was hidden; the operator could read visible UNKNOWN evidence but had no reconciliation submission form |
| Isolated browser: layout and logs | Audit and capacity views had no horizontal overflow at 390px; no console errors |
| Actual-host encrypted recovery | An encrypted snapshot from the existing cloud database through migration 005 restored successfully into an isolated PostgreSQL container with no network. Receipt: `/opt/mcp-gateway/backups/pre-team-controls-restore.json`. This verifies the old cloud snapshot, not a backup containing the new 006–010 tables |
| API contract | Strict YAML duplicate-key check, 462 resolved local references, 44 unique operation IDs and all new handler route/path-parameter coverage passed |
| Feature-commit remote CI | Commit `05d1706780ce7f5c8a085a8801eb144be15c9631` passed all four push/PR checks (Verify and release-tooling) across [run 37277646150](https://github.com/alan1-666/mcp-gateway/actions/runs/37277646150) and [run 37277682573](https://github.com/alan1-666/mcp-gateway/actions/runs/37277682573) |
| Merge/main CI | [PR 2](https://github.com/alan1-666/mcp-gateway/pull/2) merged as `da74dc4518c32cc9528667227274863f8967e368`; [main run 37277904657](https://github.com/alan1-666/mcp-gateway/actions/runs/37277904657) passed |

### Cloud.8 deployment and acceptance — 2026-10-05

| Check | Outcome |
| --- | --- |
| Application release | `20261005-cloud.8` completed checked deployment from source commit `05d1706780ce7f5c8a085a8801eb144be15c9631`. This is the feature commit, not merge commit `da74dc4518c32cc9528667227274863f8967e368`. Ten migrations are applied; current symlink, environment release ID and running image identities agree |
| Public management APIs | Eight new authenticated GET endpoints responded successfully. Microsoft Learn connection check ID `1` returned `status:ok`; the cloud check-history view showed stage `complete` and 1216 ms |
| Live governed MCP call | Operation `c0189b7f-4854-4801-8d1a-ddd55c8b9071` reached `SUCCEEDED` against Microsoft Learn and returned 10 entries containing only `title` and `contentUrl`. Repeated prepare/execute returned the recorded operation/result with one dispatch |
| Capacity metrics | Recorded one successful call with `duration_ms_total:1860` and zero active leases. This is one observed call, not a latency/SLO benchmark |
| Cloud console | Existing owner session cookie remained valid. Credentials and clients displayed their real empty states, audit displayed 11 records, capacity displayed real aggregates and the existing tool version v2 loaded; no browser console errors were observed |
| Cloud Pi continuity | Browser runtime showed ready with `openai / gpt-5.5` and a current worker heartbeat (15:35 as displayed). This verifies retained configuration/runtime continuity, not a new model inference or full model-driven task |
| Independent operations installation | Bundle `20261005T073115330026Z-46a0c1485e45` is installed outside the application current pointer. Backup and monitor timers are active |
| Scheduled backup and post-upgrade restore | Actual backup service succeeded. `/opt/mcp-gateway/backups/last-restore.json` records `2026-10-05T07:32:15.854240Z`, 10 migrations and 5 operations from a network-isolated PostgreSQL restore. Snapshot SHA-256: `5da937ec93983e7de00b07ef999c3b3364b0c10fd3603422921420b58821d4c4` |
| Collector signal | Real collector reported only `offhost_backup_missing`; delivery remained `local_only` with no external send. The missing off-host condition remained visible with `sent:false` |
| Monitor unit correction | The host unit passed `systemd-analyze verify`. After adding `SuccessExitStatus=2`, `systemctl start` returned 0 and systemd reported `Result=success`, `ExecMainStatus=2`; journald still contained `offhost_backup_missing`, `delivery:local_only`, `sent:false`. The correction changes classification of an evaluated alert, not the alert itself |

These cloud checks exercise real authenticated APIs, a public read-only upstream and the listed browser views. Credential/client creation and rotation, tool candidate mutation/retirement and UNKNOWN evidence submission were comprehensively exercised against isolated fixtures above; they were not all repeated as cloud mutations. The restore drill restored the post-upgrade data in isolation and did not roll back the production application or database. No real cloud application rollback is claimed. The live monitor-unit correction is a separate operations-unit follow-up for PR 3; it does not change the cloud.8 application images or their source commit.

### Remaining acceptance

| Gate | Current evidence status |
| --- | --- |
| Actual cloud application rollback | Tooling and isolated rollback tests passed; no actual production application rollback has been performed |
| Automatic off-host backup | Awaiting an authorized independent destination. No snapshot or backup-key copy to another host has been completed, and no remote-transfer receipt or independent disaster-recovery exercise is claimed |
| External alert delivery | Awaiting the real webhook configuration; neither tests nor the live collector sent an external notification |

The encrypted bundle includes database, cloud configuration and secrets including the vault master key, protected by a separate backup key. Pi volumes and host TLS/nginx state are outside that bundle. Local backup, remote transfer and restore verification have distinct receipts and must be reported separately. Monitoring without a webhook explicitly reports local-only; a completed fixture test is not a sent production alert. No live model request or company business-data call was required for this package regression.

## Explicitly unverified

- The complete Microsoft Learn cloud browser import/publication/execution interaction sequence. Cloud sign-in, enabled-server visibility and live discovery of all three Imported tools passed; all three tools also passed authenticated HTTPS API execution. Local browser approval/execution passed separately.
- Upstream OAuth, stdio and compatibility with other independently operated MCP servers have not been validated; the implemented transport/authentication limits remain in effect.
- Live model behavior evaluation and a complete cloud task using the real model and downstream business tools; the live inference check above sent only a fixed connectivity prompt.
- Cloud mutation coverage beyond the explicit cloud.8 checks and an actual production application rollback. Feature/merge CI and cloud deployment passed; a configured external backup destination and alert webhook remain pending.
- Other MCP client/protocol combinations, Kubernetes, load/SLO targets, high availability, independently exercised off-host disaster recovery and live team/business-data rollout.

## GitHub baseline integration — 2026-10-05

The GitHub CI workflow includes the database-backed Node/Go boundary test. The first Verify run for `59b8f53` failed at `go test -race ./...`; dependency installation, Go vet and govulncheck passed, and subsequent TypeScript/build/audit steps were skipped. That historical failure is recorded in [run 37271631614](https://github.com/alan1-666/mcp-gateway/actions/runs/37271631614).

After correcting the catalog-limit fixture deadline/assertions, baseline commit `e4e382cf574916b15793f27e4998149ffbecd70e` passed Verify and release-tooling on both push and PR: four successful checks across [run 37273876618](https://github.com/alan1-666/mcp-gateway/actions/runs/37273876618) and [run 37273830693](https://github.com/alan1-666/mcp-gateway/actions/runs/37273830693). [PR 1](https://github.com/alan1-666/mcp-gateway/pull/1) merged into `main` as `1163b157fbff6e2e278ee6c12b9be08b3a70b0fb`. The subsequent [main CI run 37274534904](https://github.com/alan1-666/mcp-gateway/actions/runs/37274534904) for that exact merge commit also passed.

These runs close the earlier baseline failure. The subsequent five-package feature commit now has its own successful push/PR checks recorded above; the separately recorded cloud.8 application deployment and bounded acceptance passed, with the remaining external/operational limits listed above. See [development](development.md) for repeatable commands and [implementation status](implementation-status.md) for the remaining release and architecture gates.

## 2026-10-05 — proxy recovery and authenticated private upstream

- PR 4: <https://github.com/alan1-666/mcp-gateway/pull/4>. Reviewed source `9d35bf36087603aa8bb86be4f48ee31cab8fc735`; PR CI <https://github.com/alan1-666/mcp-gateway/actions/runs/37289787571>, push CI <https://github.com/alan1-666/mcp-gateway/actions/runs/37289781033> and merge CI <https://github.com/alan1-666/mcp-gateway/actions/runs/37290160621> passed.
- Release `20261005-cloud.9` deployed that source with artifact SHA-256 `1541fae892da2deb3e2b7dd7f1186533e595297ad507c3aa7bb3f126f26c25e2`. API, gateway, console and PostgreSQL were healthy; worker, runner and operational timers remained active.
- The proxy regression replaces both backends at different IPs on an isolated Docker network. API and MCP traffic recover through Docker DNS without replacing/reloading the console. The explicit network subnet makes the static-IP fixture portable to GitHub Actions.
- An authorized private test integration passed 11 source checks and 13 gateway acceptance cases: authenticated discovery, three read tools, explicit client grants, denied scopes/tools, bounded arguments, projection/version checks, idempotent dispatch, expected not-found failure and audit evidence. A further governed MCP read after cloud.9 succeeded with one dispatch, followed by temporary-client revocation. Company identifiers, endpoints, adapters, credentials and raw business results remain outside this public repository.
- One bounded normalized result sample decreased from 14,062 to 5,174 bytes (63.2%). This measures the stored/model-facing MCP envelope, not tokens or upstream transfer. These are compatibility samples, not throughput/SLO evidence.

## 2026-10-05 — catalog change review source acceptance

- Discovery now compares a complete MCP catalog with registered definitions and atomically records the observation plus audit. Coverage includes all five comparison states, canonical description matching, duplicate/oversized comparison rejection, history pagination/retention, role/workspace isolation, and disable/re-enable fencing.
- `TestCatalogReviewLifecycleWithRealMCP` uses an actual MCP SDK HTTP server and PostgreSQL through the management API. It verifies discovery → import → explicit publication → schema drift → hash/version-guarded candidate → live revalidation → explicit v2 publication → successful projected invocation. No business call occurs during discovery/review/publication. Prepared operation snapshots remain v1. A failed upstream leaves the last successful report intact; a complete empty catalog marks missing without retiring the tool.
- Local `make check` and `RUN_CLOUD_WORKER_INTEGRATION=1 TEST_DATABASE_URL=... make test` passed with PostgreSQL 16.9 and Go race detection. Console: 64 tests; Pi runner: 31 tests. All 39 Python release/backup/monitor tests passed. OpenAPI parsed with all 468 local references resolved.
- Browser acceptance used an isolated local workspace, real API/database and a controllable MCP fixture. It verified five comparison states, the changes filter, a missing-tool registry link, candidate creation, reopening the saved candidate after reload, real field diffs, explicit v2 publication, a subsequent in-sync result, retained historical observations and an upstream-failure state. This found and fixed a previous console issue: selecting a saved candidate had used a list summary with no field diff. The console now fetches and validates candidate detail before offering publication.
- Migration 011 is additive. Earlier application binaries ignore its table; release rollback still requires the explicit compatibility declaration for the retained migration. Reports retain 50 successful observations per server and never automatically publish, retire or invoke tools. Background synchronization is not included.
- GitHub checks and cloud activation are recorded in the release sequence below, separately from local acceptance.

### Cloud release sequence

The feature source `8d183fc6d12dc70eeb7961de008488ac4efc032f` passed [PR CI](https://github.com/alan1-666/mcp-gateway/actions/runs/37294074206) and [push CI](https://github.com/alan1-666/mcp-gateway/actions/runs/37294065731), including verify, release-tooling and the container proxy regression. `20261005-cloud.10` deployed successfully, but post-release catalog acceptance found different timestamp precision in the creation response (nanoseconds) and PostgreSQL history (microseconds). This prevented claiming the full acceptance gate even though services remained healthy. The failure occurred before creating a temporary client or invoking a business tool.

Fix `f96f2b646a6487958126ee5707b71b1e8e8d6858` returns the database-stored start timestamp from the insert. A PostgreSQL regression now asserts that both timestamps survive the discovery/history round trip without change; focused upstream and HTTP/MCP race tests passed.

The fixed source passed [PR CI](https://github.com/alan1-666/mcp-gateway/actions/runs/37295003671) and [push CI](https://github.com/alan1-666/mcp-gateway/actions/runs/37294998002), all six job results successful. `20261005-cloud.11` deployed `f96f2b646a6487958126ee5707b71b1e8e8d6858` from artifact SHA-256 `e21b1495c883647603785b626a301dbfe3f9af41f9049032423a47fd15a0749e`. This adds migration 011 to the previous 10 migrations. A reviewed cloud.9 rollback-compatibility file for this additive migration was retained on the same host; actual application rollback was not exercised.

Nine bounded cloud acceptance checks then passed: both existing upstreams discovered, stored report round trip, unchanged registered versions, unauthenticated denial, invalid query rejection, machine-client history denial, external MCP read with projection, single dispatch, and catalog audit presence. Both catalogs contained three registered tools in sync. One authorized private-test read returned three projected rows at its existing tool version 2, with one dispatch; the temporary client was disabled afterward and its key returned 401. No upstream business write, automatic tool refresh or existing publication change was performed. The cloud browser retained its owner session and exposed the new catalog history/compare workflow. Private IDs and response contents remain only in ignored local evidence.

[PR 5](https://github.com/alan1-666/mcp-gateway/pull/5) includes the feature, timestamp regression fix and this evidence. Its final documentation commit does not change deployed application source; the cloud release remains pinned to the fixed source SHA above. Off-host backup/webhook destinations, background synchronization and the broader architecture gaps remain open.


## 2026-10-05 — console simplification

The visual reference is the public [Raft product page](https://raft.build/): a light workspace, compact navigation, bounded sections and distinct task tabs. The Gateway keeps its own identity and workflows. No source, images or assets were copied from Raft.

- Navigation is grouped into Gateway, Activity and Manage. Overview shows real metrics, direct actions and recent operations. New invocation is available through Overview, Operations and each callable tool.
- Server detail separates Tools, Activity and Settings. Tool detail separates Contract, Versions & changes and MCP Response policy; upstream names improve scanning while canonical aliases remain visible. Creating a change candidate links directly to its version-review tab.
- Tabs mount on first selection and retain mounted panels afterward, preserving drafts and pending actions. Arrow keys, Home/End and ARIA tab/panel relationships are supported. The narrow-screen navigation supports normal keyboard order, Escape, focus return and a closed state after navigation; hidden content is not interactive.
- Local `make check` and PostgreSQL-backed `RUN_CLOUD_WORKER_INTEGRATION=1 TEST_DATABASE_URL=... make test` passed: Go race suite, 64 console tests and 31 runner tests. Subsequent console edits passed their final TypeScript/production build; GitHub exact-source checks are a separate gate.
- Browser checks used the actual API/PostgreSQL and synthetic MCP fixture: discover all comparison states; switch server tabs; disable/enable the isolated server; inspect diagnostics/history; create a candidate and land directly in Versions; fetch field differences and publish v2; retain a response-policy draft across tab changes; preview selected sample fields; register an HTTP draft and verify it has no MCP-only Response policy tab. The initial HTTP fixture lacked its adapter; the fixture was corrected and the HTTP journey rerun successfully. No real upstream business operation was dispatched for these UI checks.
- A viewer session exposed only permitted navigation and contract inspection, without administration, invocation or publication controls. Desktop and 390px viewport checks passed with no document horizontal overflow, along with keyboard tab navigation, mobile menu/Escape and focus restoration.
- Cloud deployment and bounded real-workspace acceptance are recorded after the exact-source release below. Authentication, API contracts, schemas and database migrations are unchanged by this package.

### Console cloud release

`20261005-cloud.12` deployed source `92d574a7a3c321f9675132e3d7d055d564c16d73` with artifact SHA-256 `634496b649ba5a6e64b4ef016204f02ddad33cf821a98771403d74dd54e80c89`. All six checks passed across [push CI](https://github.com/alan1-666/mcp-gateway/actions/runs/37303856420) and [PR CI](https://github.com/alan1-666/mcp-gateway/actions/runs/37303927640) before deployment. The release script verified the source/migration/image tuple, backup and service health. This package adds no database migration and retains the existing 11.

The existing owner cookie session remained valid. Cloud browser verification loaded the redesigned overview with six registered/available tools and 15 existing operations; both existing upstreams discovered three in-sync tools each. Server activity/settings, stored catalog history, existing diagnostic records, the published tool v2 and its response-policy fields loaded correctly. Clients, operations, approvals, audit, credentials, capacity and team/account views completed loading with no visible alerts or document overflow. Browser error logs were empty. Pi showed Runtime ready with its existing configured model and a current worker heartbeat; this checks runtime continuity, not new model inference.

No production publication, policy/credential/client mutation, business-tool dispatch or model task was needed for this UI release. The discovery actions did append normal catalog observations. The screenshot and private workspace evidence are retained locally, outside the public repository. A later documentation-only commit records this acceptance; the deployed application remains pinned to the source above. [PR 6](https://github.com/alan1-666/mcp-gateway/pull/6) contains the complete change and evidence. Off-host backup, webhook commissioning and a real application rollback remain open.


## 2026-10-05 — scheduled catalog check source acceptance

- PostgreSQL race tests cover opt-in defaults, bounds, admin/client/workspace isolation, revision conflicts and idempotent saves; eight concurrent claimers obtain one lease. Expired-lease takeover, late completion, pause/edit fencing and disable/re-enable fencing are verified.
- Successful observations, failure backoff, bounded delays, safe error storage, preserved history, empty catalogs, comparison failure and worker shutdown are covered. No raw upstream exception is persisted.
- The existing real SDK + HTTP management + PostgreSQL lifecycle test now enables scheduling, runs an actual scheduled discovery, checks failure/pause, and verifies the published tool version and business-call count remain unchanged.
- `make test` passed with PostgreSQL and the Node cloud worker boundary enabled: Go race tests, 64 console tests and 31 Pi runner tests. `make check` passed (Go vet, TypeScript and production builds).
- Browser acceptance uses an isolated workspace and synthetic MCP server: default off; enable/save; draft preservation across tabs; successful scheduled history; stale concurrent edit returns conflict and blocks retry until reload; upstream outage preserves last success and shows retry time; server disable displays paused; schedule pause persists; catalog history reload works. At 390 px the settings form has no horizontal document overflow; browser error logs are empty. All 39 release-tooling tests passed. Private fixture IDs, logs and screenshots stay outside source control.

Cloud release acceptance follows separately; source tests do not establish deployed behavior.


## 2026-10-06 — scheduled catalog check cloud acceptance

The application artifact `20261005-cloud.13` pins source `3a90344294ef7908ce0147fd08c7edafd5932971`, SHA256 `815fe8620cda50482a071d5d20b73675c255285f3a91af5c573a2fdc1c0b1d89`. Both [push CI](https://github.com/alan1-666/mcp-gateway/actions/runs/37337162314) and [PR CI](https://github.com/alan1-666/mcp-gateway/actions/runs/37337304714) passed verify, release-tooling and console-proxy before deployment. Source/migration verification, same-host encrypted backup, migration and container health gates passed. Migration 012 is additive; the exact cloud.12 rollback compatibility declaration is retained on the host. No application rollback was exercised.

Eight bounded checks passed: opt-in defaults, unauthenticated denial, enabling saved schedules, stale-revision denial, actual worker completion against both existing upstreams, source-labeled history, internal audit identity, and unchanged full tool definitions. Each upstream reported three registered tools in sync. Both schedules remain enabled at 3600 seconds; the first successful checks occurred at 16:04:33 and 16:04:34 UTC on October 5 (00:04 local on October 6). No business tools, model tasks, policy edits or new client keys were used.

The existing browser owner session remained valid. Cloud settings showed the saved hourly interval, last success and next check; Activity displayed the scheduled observation alongside previous manual history. Browser error logs were empty. Overview retained six tools and 15 existing operations. The cloud screenshot and detailed private acceptance evidence remain in ignored local files. This documentation follow-up does not change the pinned application source. The full change is in [PR 7](https://github.com/alan1-666/mcp-gateway/pull/7); upstream OAuth, off-host recovery, external webhook commissioning and broader production architecture remain open.


### Completion timestamp consistency

The documentation-head push run [37338386983](https://github.com/alan1-666/mcp-gateway/actions/runs/37338386983) exposed a one-microsecond discrepancy in the scheduled-backoff assertion; the parallel PR run passed. Separate `clock_timestamp()` expressions in one update can be evaluated in different column order. Completion, last success and next due now share `statement_timestamp()`, and the test requires the exact configured delay. This is a runtime timestamp consistency fix, not a relaxed assertion. The exact-delay PostgreSQL regression passed 20 consecutive race-enabled runs. The cloud.13 acceptance above remains historical; the corrected source must pass CI and be released separately.


### Cloud.14 verified deployment

The corrected application release `20261006-cloud.14` pins source `e1e2f14317575319b09d2255cab598359daadd7b`, artifact SHA256 `78ea9825ad2d07a35d4b869b657d38a234a32648fea0cade290b2b47d2167cab`. Its [push CI](https://github.com/alan1-666/mcp-gateway/actions/runs/37339110600) and [PR CI](https://github.com/alan1-666/mcp-gateway/actions/runs/37339119207) passed all six gates before deployment. Same-host backup, source/migration verification and service health checks passed; the schema remains at 12 migrations.

Nine cloud checks passed after this deployment. Each existing schedule was paused and resumed through the revision-checked API, then completed a fresh real worker check. Next due minus completion was exactly 3600 seconds for both. Successful observations again showed three unchanged tools each, and their exact review IDs appeared under the internal scheduler audit identity. Full tool definitions were unchanged; zero business calls and zero model tasks were created. The existing hourly schedules remain enabled. The cloud UI showed the new last-success and next-check timestamps with no browser errors. Detailed evidence and the final screenshot remain in ignored local files. The later documentation commit records these results without altering the pinned deployed application.


## Public product website — local acceptance, 2026-10-06

- Added an English product homepage with a routing illustration, a keyboard-accessible synthetic response projection comparison, current capability explanations and links to actual repository documentation. No customer claims, live operation data or model requests are used in the marketing examples.
- Split Vite entries: `/` serves static product HTML with a small interaction module; `/console/` loads the existing React workspace. Landing JS has no React or API dependency. Both use self-hosted fonts and the existing origin.
- Existing root invitation fragments forward to the fixed workspace path, including same-document fragment changes. Newly generated member/bootstrap invitations use `/console/#invite=...`. Tokens remain fragments and are cleared by the existing login flow. Console HTML is `noindex`.
- nginx keeps `/api`, `/mcp`, internal-route blocking and dynamic Docker DNS behavior. A relative canonical redirect preserves the external HTTPS origin. Missing public pages and asset files return 404; workspace deep links use their own fallback. The real-container proxy regression now checks these static routes alongside backend IP replacement.
- Local regression: `make test` passed with PostgreSQL and cloud-worker integration enabled (Go race tests, 66 console tests, 31 runner tests); `make check` passed; 39 Python release tests passed. Repository documentation link targets were checked against local files.
- Browser checks: desktop composition, 390px full page and 320px small-screen layout without horizontal overflow, mouse/keyboard response toggles, and a built-page workspace transition followed by successful authentication against an isolated real Go API. Existing workspace navigation and tool counts rendered without browser errors. Cloud routing, session and invitation acceptance remain release gates.


## Public product website — cloud.15 acceptance, 2026-10-06

- Published committed source `fcfcf8348b206825a42ad45f435a9ac1e61f74d1` as `20261006-cloud.15`. Artifact SHA256: `c692e61d14604b506d006528d7c3854ed4fc0fff4e37271502f9938ce93a78d1`. No database migration was added. Standard same-host encrypted backup and release health verification completed; no off-host backup transfer was performed.
- All six exact-source checks passed: [push CI](https://github.com/alan1-666/mcp-gateway/actions/runs/37354497517), [PR CI](https://github.com/alan1-666/mcp-gateway/actions/runs/37354548151). Real-container acceptance includes public/workspace routes, a relative 308 redirect with query preservation, missing-resource 404s, security headers and API/MCP recovery after backend IP changes.
- Thirteen cloud checks passed: public HTML/security headers; independent workspace entry; HTTPS-safe canonical redirect; workspace refresh fallback; missing asset/public/internal routes; JSON account status; anonymous account denial; emitted asset presence and successful serving for each entry.
- Browser acceptance: signed in with an existing cloud account, visited the website and returned to the authenticated workspace without another login. Existing registry counts and operations rendered. The pre-existing page had required login on reload; session preservation was verified with the newly authenticated session rather than inferred from the old in-memory view.
- Browser invitation acceptance used synthetic fragments only: old root links, same-document root fragment changes and new workspace links reached the join form and cleared the fragment. No invitation was accepted or account created in this check; real invitation creation/acceptance is covered by the PostgreSQL integration test.
- Mouse and keyboard controls switched the cloud website's synthetic response example correctly. Local desktop, 390px and 320px layout checks passed; cloud browser inspection confirmed the deployed design and mobile layout. No private upstream data is used by the website. Browser evidence and the bounded route report are retained privately under `.local/verification/`.
- PostgreSQL, API, MCP gateway and console reported healthy. The recovery/catalog worker and Pi runner were running; the existing integration link, backup timer and monitor timer remained active. No business calls, model calls, client grants or tool-definition changes were made for website acceptance.


## Rillgate branding — cloud.16 acceptance, 2026-10-06

- Adopted the approved Rillgate name across the public website, workspace, browser metadata, R monogram/favicon, CLI help, API title and current product documentation. MCP gateway remains the functional descriptor; existing repository, module and deployment identifiers remain compatible. The owner is registering a custom domain; the current HTTPS origin is still in use.
- Published source `9dc23b702d613a7d45e54241caa5dcebf85f610d` as `20261006-cloud.16`, artifact SHA256 `ce2607bb8c138edd98cef66a2fabbc77c416868707e6f459fce8c5fd9381e48a`. No migration or protocol change was added. Same-host encrypted backup and source/migration/service health verification passed.
- TypeScript checks, production builds, 66 console tests and 31 runner tests passed locally, with PostgreSQL and cloud-worker integration enabled. All six exact-source checks passed: [push CI](https://github.com/alan1-666/mcp-gateway/actions/runs/37410815380) and [PR CI](https://github.com/alan1-666/mcp-gateway/actions/runs/37410820067). CI includes Go race/database checks and the real-container proxy regression.
- Sixteen bounded cloud checks passed: homepage/workspace branding, R favicon, public security headers, independent workspace entry, canonical redirect, deep-link fallback, missing-resource/internal 404s, JSON authentication status, anonymous denial and emitted assets for both entries.
- Browser acceptance confirmed the rendered public Rillgate name and monogram, workspace title and brand, restoration of the existing authenticated session, and return to the public website. Browser error logs were empty. Screenshot and detailed route evidence remain privately in `.local/verification/`.
- PostgreSQL, API, gateway and console were healthy; recovery/catalog worker and Pi runner were running. Existing integration, backup and monitor units stayed active. No business calls, model calls, accounts, client grants or tool definitions were changed for branding acceptance. This evidence does not establish a custom domain or any additional production capability.


## Canonical domain — host configuration acceptance, 2026-10-06

- The owner registered `rillgate.cn` and configured apex/www A records to the existing cloud host. Authoritative and independent public resolvers returned the expected address. The canonical website is [https://rillgate.cn/](https://rillgate.cn/), workspace is [https://rillgate.cn/console/](https://rillgate.cn/console/) and MCP endpoint is `https://rillgate.cn/mcp`.
- A trusted `mcp-gateway-domain` certificate covers the apex and www names and initially expires on 2027-01-04. The existing IP certificate remains for HTTPS redirects. Certbot's domain renewal dry-run passed. The existing twice-daily systemd timer now invokes an independently installed script for these two certificate names only; a manual invocation succeeded and both expiry checks passed. This does not establish independent external expiry alerting.
- The domain nginx template preserves existing request limits, safe logging, internal-route denial and streaming proxy behavior. HTTP, www HTTPS and legacy IP HTTPS return fixed-origin 308 redirects preserving path/query. Acceptance found that IP clients omit TLS SNI: the IP certificate is now the explicit default on both listen families, and strict certificate-verified IP requests passed after nginx reload. No browser certificate warning was bypassed.
- Changed only `PUBLIC_ORIGIN` in the private application environment and recreated API, gateway and worker using their existing images. No running/queued Agent task or dispatch was observed at the pre-switch check. An encrypted same-host pre-domain snapshot and private configuration copies were retained. Application release stays `20261006-cloud.16` at source `9dc23b702d613a7d45e54241caa5dcebf85f610d`; image/source/migration versions, database volumes, tool policies and model configuration were not changed.
- Twenty-three external route checks passed with normal DNS and certificate validation: website/workspace branding and assets, missing/internal routes, canonical redirects including encoded queries, anonymous account/MCP denial and rejection of the former IP browser Origin. Eight additional checks passed using an existing account: domain login, host-only Secure/HttpOnly/SameSite cookie, authenticated session, rejection of cookie-only MCP access even with valid CSRF, invalid-CSRF denial, preserved session after rejection, test-session logout and subsequent denial.
- Browser acceptance on the canonical domain passed: real account login, session restoration after refresh, public website navigation and a synthetic old-IP invitation link reaching the join form with its fragment cleared. No invitation was accepted or account created. Browser errors were empty on the accepted domain journey. Detailed reports and the screenshot remain private under `.local/verification/`.
- The host's primary recursive DNS retained a negative response from before registration while public resolvers and external clients already worked. Its already-configured fallback resolvers were prioritized at runtime; no hosts-file entry or certificate-validation exception was added. This is separate from the domain's public DNS configuration.
- PostgreSQL, API, gateway and console were healthy; worker and Pi runner were running. Integration, backup, monitor and certificate timers/services remained active. The host nginx configuration SHA256 is `5ba7d6d26134b0287c6f0935d4841ba91a204dc84dea8cbc4c2469097d93d742`; the installed renewal script SHA256 is `15910c0bb7c1eb4d7b06bf73a7b6a6d22b07de601e6c3e8dfd26ffde71f5c0b5`. Changes are tracked in [PR 10](https://github.com/alan1-666/mcp-gateway/pull/10).

## English and Chinese public website — 2026-10-06

- Shared, escaped HTML template generates complete English and Simplified Chinese pages at `/en/` and `/cn/`; localized content and metadata are present without JavaScript. Native language links remain usable if preference storage is unavailable.
- Explicit language URLs override the saved choice. Root visits reuse a valid preference; language switching retains the query and section anchor. Legacy invitation forwarding takes priority on every public entry.
- Local acceptance passed: Go race tests with real PostgreSQL and the Node cloud-worker integration, 70 console tests, 31 Pi tests, `go vet`, TypeScript checks and production builds. Focused language regressions cover complete dictionaries, escaped rendering, metadata, explicit-route precedence, restricted storage and invitation priority.
- Browser acceptance passed at desktop, 390px and 320px: translated content, keyboard switching, English/Chinese preference restoration, query/anchor preservation, localized response example and synthetic invitation forwarding. Both languages have no horizontal document overflow at 320px. The workspace remains independently bundled and English.
- Real nginx CI fixtures now check both language entries, query-preserving slash redirects and missing-page 404s in addition to API/MCP proxy recovery.

Deployed as `20261006-cloud.17`, source `8e6affd0986929b8762cd928e2402cb1d90dd87f`, artifact SHA256 `20f3659df5d707e1279af647a53c39ec068c68cf1656486e03f6d8af52180233`, after all six exact-source GitHub push/PR checks passed. The release tool verified deployment and service health. Twenty-one strict-TLS checks passed for full translated HTML, language metadata, assets, query-preserving redirects, missing-page handling, the independent workspace and API/MCP authentication boundaries.

Deployed browser acceptance confirmed both languages, keyboard switching with section preservation, English/Chinese preference restoration, the translated response example, the existing authenticated workspace session and 390px layout without document overflow. No browser errors were recorded during these journeys. Host domain nginx configuration retained its recorded checksum; backup, monitor and certificate timers remained active. No live model or business tool calls were made. Private evidence is retained under `.local/verification/website-languages-*`; it is excluded from Git.


## Governed execution website — 2026-10-06

- Reframed both public languages around existing tools, authorized discovery, independent approval and recorded business operations. Field projection is now a supporting feature instead of the main demonstration.
- Replaced the synthetic JSON comparison with an explicitly labeled, local five-step operation illustration. Keyboard-operable buttons switch between a confirmed result and an uncertain write: UNKNOWN is retained, no automatic write replay is represented, and a reviewer can append evidence. No preinstalled ticket connector or downstream exactly-once guarantee is claimed.
- Production TypeScript/build checks and all 70 existing console tests passed. Browser checks covered both scenario directions, keyboard activation, language switching with the execution anchor, and the previous response anchor. Chinese remains free of full-width stops. English at 320px and Chinese at 390px have no horizontal document overflow; no browser errors were recorded.
- Complete translated content is rendered into static HTML. Without JavaScript, the completed sequence remains readable and unavailable scenario controls remain hidden. Source and cloud deployment receipts are recorded on the corresponding feature pull request.

## Upstream OAuth capability — 2026-10-06

Implemented the profile in [upstream OAuth](upstream-oauth.md): pre-registered
clients, protected-resource/issuer metadata, PKCE S256, mandatory RFC 9207 issuer
responses, encrypted grants, browser-session-bound attempts and persisted refresh
claims. Console Settings now contains configuration, authorization, status and
reconnection controls. Enterprise account expansion is deferred.

Local acceptance:

- `make check`: Go vet, TypeScript checks and production bundles passed
- `RUN_CLOUD_WORKER_INTEGRATION=1 TEST_DATABASE_URL=... make test`: all Go race
  and PostgreSQL integration suites passed; console 79/79 and Pi runner 31/31 passed
- 20 OAuth test groups cover metadata/egress bounds, PKCE, state/session/issuer
  binding, exact resource fencing, encrypted persistence, optimistic edits,
  concurrent refresh across two service instances, failed/ambiguous exchanges,
  disabled servers, expired grants, and disconnect/configuration races
- An official MCP SDK client/server over trusted fixture TLS exercised initialize,
  tools/list and tools/call; a 401 was sent once, without refresh or replay, and
  revoked/previously anonymous transports were fenced
- Cloud HTTP contract tests verify administrator/browser-only management, CSRF,
  workspace isolation, personal-key rejection and safe callback redirects
- Release/backup/monitor Python suite: 39 tests passed

These checks use isolated PostgreSQL schemas and deterministic provider fixtures.
They do not claim completed consent or compatibility testing with a real external
OAuth provider. Cloud release and browser acceptance are recorded in the release
pull request after exact-commit CI. The TLS edge and console proxy must both
suppress access and error logging for the callback URI before enabling OAuth.

## Private Connector implementation — 2026-10-06

See [private MCP Connectors](private-connectors.md) for the supported execution profile and installation steps. Cloud deployment acceptance is recorded on the release pull request after exact-source CI succeeds.

- Complete Go race suite with real PostgreSQL and the Node cloud-worker integration passed; `make check` passed. Console has 88 passing tests; Pi has 31. The 39 Python operations tests passed.
- Durable queue tests cover hashed credentials, workspace isolation, frozen target fingerprints, one-use claim/start, exact completion retries, current grants/key/tool/server/approval checks, deadlines under lock contention, bounded capacity, retention and UNKNOWN write outcomes. Restarted services never requeue claimed work.
- Results are schema-validated and projected before Connector queue persistence; regressions check that dropped fields, raw text and arbitrary error bodies are not stored. Exact integer values survive the transport.
- Runtime tests exercise real subprocess stdio, local TLS HTTP control requests, fixed private target credentials, journal fsync/restart protection, failed-start no-retry, result-only retries, corruption/capacity fail-stop, output limits, process reaping and unconfirmed-cleanup shutdown.
- The rootless container test runs separately on the Ubuntu 24.04 cloud host under a dedicated non-root acceptance account. It uses only a locally built synthetic fixture image and checks MCP discovery/call, timeout, cancellation and final container absence. Kernel cgroup files confirm 256 MiB memory, 64 processes and a 1 CPU quota. Container logs and inherited proxy environment are disabled.
- Revoked Connector tools disappear from consuming catalogs and cannot be prepared or claimed. Administrators retain a visible disabled record. The target configuration and existing tool flag are not rewritten by revocation.

These checks use synthetic targets and do not establish compatibility with every private MCP server, arbitrary stdio package or large-scale workload. The Connector transport is bounded HTTPS polling rather than the architecture's future bidirectional gRPC stream. No private company data or real business write was used for this package.
