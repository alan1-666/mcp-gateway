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

Browser acceptance used a disposable local PostgreSQL schema and loopback-only synthetic MCP services; the schema, temporary identities and processes were removed afterwards. It created no cloud identities, contacted no company service and made no model calls. The deployed MCP registry is empty and the existing egress policy is unchanged. Compatibility with an independently operated third-party server still requires its actual endpoint and authentication configuration.

## Explicitly unverified

- A populated third-party MCP workflow in the cloud browser. Local browser approval/execution and real-protocol integration passed; the cloud workspace still has no configured downstream MCP origin.
- Live model behavior evaluation and a complete cloud task using the real model and downstream business tools; the live inference check above sent only a fixed connectivity prompt.
- Other MCP client/protocol combinations, Kubernetes, load/SLO targets, high availability, off-host disaster recovery and live team/business-data rollout.

The GitHub CI workflow includes the database-backed Node/Go boundary test. Remote CI has not been executed for these local commits. See [development](development.md) for repeatable commands and [implementation status](implementation-status.md) for pending architecture work.
