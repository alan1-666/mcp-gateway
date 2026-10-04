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
| Pi bridge tests | Authentication, controlled tool discovery, local durability, approval pause and ambiguous result handling passed without model calls |
| Real Pi configuration check | Local subscription OAuth and selected model configuration found; no model request sent |
| Browser checks | Login, invalid form input, role controls, empty states, logout/reload credential clearing and 390px layout passed; no console errors or warnings |
| Dependency audit | No known npm vulnerabilities after upgrading Pi to 1.0.2 and Vite to 7.3.6 |
| Docker Compose | Cloud images built and deployed on Ubuntu 24.04; API, Gateway, PostgreSQL and console healthy |
| Cloud browser | Login, session restoration across service replacement, team/account views, logout and 390px layout passed; final guest page has no console errors or warnings |
| Certificate renewal | Staging dry-run renewal succeeded with the final nginx webroot; scheduled renewal service and timer verified |
| Cloud identity | One-time/expired-state handling, CSRF rejection, role checks, member/credential revocation, logout and password rotation covered by PostgreSQL tests |
| Public HTTPS and MCP | Trusted IP certificate; real public login, invitation acceptance, MCP initialize/tool discovery and immediate revocation passed |
| Go vulnerability scan | govulncheck v1.8.0 reports zero affected symbols and zero imported-package findings after Go, pgx and x/text updates; advisory matches remain in unused module packages |
| Backup and recovery | Scheduled dump generated and restored into a separate temporary database; migrations and unclaimed owner invitation verified |

## Explicitly unverified

- The populated approval/execution workflow in the browser: API/MCP end-to-end tests cover it, while browser checks used an empty real workspace with no permitted downstream origins.
- Live model inference and behavior evaluation: subscription usage was not consumed.
- Other MCP client/protocol combinations, Kubernetes, load/SLO targets, high availability, off-host disaster recovery and live team/business-data rollout.

The GitHub CI workflow is present in the working source but has not been executed by GitHub for these uncommitted changes. See [development](development.md) for repeatable commands and [implementation status](implementation-status.md) for pending architecture work.
