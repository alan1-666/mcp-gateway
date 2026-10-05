# Development and operation

The repository contains executable Go services, a React console and a Pi integration client. This page describes local source development; use the [cloud deployment guide](cloud-deployment.md) for the hosted product. See [implementation status](implementation-status.md) for the boundary between working software and the complete target architecture.

## Delivery workflow

Delivery prioritizes the Gateway: connect an upstream, review its tools, grant a client access, discover the required schema, execute through policy, return a bounded result and inspect the outcome. Pi remains a client and a regression path through those same controls.

The implemented work packages and their remaining release/acceptance gates are tracked in [implementation status](implementation-status.md#development-order). The complete product and architecture remain the target; a deployed feature does not establish production readiness for the whole system.

### One work package, one reviewable change

| Step | Required output |
| --- | --- |
| 1. Define the user outcome | A short task stating the user/role, action, expected result, failure cases, boundaries and dependencies |
| 2. Agree the contract | HTTP/MCP shapes, errors, authorization, persistence, version/concurrency behavior and migration compatibility; update OpenAPI or the relevant contract document |
| 3. Divide implementation | Assign file ownership and a single integrator for shared types, migrations and platform wiring; implement backend and console against the same agreed contract |
| 4. Verify the complete flow | Focused unit tests, actual PostgreSQL and MCP integration, affected browser journeys and negative/recovery cases; deterministic fixtures precede a bounded real-service check |
| 5. Review on GitHub | One feature branch/PR per work package after the current baseline is integrated; include behavior, tests, migration/rollback conditions and remaining limits. Verify CI on the exact commit being merged |
| 6. Release a known commit | Build a versioned artifact from the reviewed commit, back up persistent data, run compatible migrations, replace services and wait for health checks |
| 7. Verify and record | Check HTTPS, identity, discovery and affected execution/UI paths; record commit SHA, release ID, test evidence and known limits, then update the implementation status |

A work item can use this compact template in its issue or PR:

```text
User outcome:
Scope and dependencies:
API/data/permission changes:
Success and failure acceptance cases:
Migration and rollback conditions:
Evidence and remaining limits:
```

### Parallel development

- **Integrator:** owns the task boundary, shared contracts/types, migration numbering, dependency changes, integration review and release decision.
- **Backend owner:** implements persistence, permission checks and runtime behavior, with focused tests.
- **Console owner:** implements the real API journey, including loading, failure, permission and version-conflict states.
- **Verification owner:** independently exercises API/MCP/database boundaries, concurrency, workspace isolation and affected browser flows; reports concrete failures for the responsible owner to fix.

Agree endpoint shapes before splitting work. Assign shared-file changes to one owner, communicate contract changes before applying them, and integrate before running the final regression suite. With four concurrent agents, the integrator plus the three owners above fill the available roles. Contributors do not independently publish releases. Commit and push only with user authorization, and use GitHub as the repository host.

### Completion criteria

A functional package is complete when the user journey works through actual APIs and persistence, the affected error/permission paths are verified, documentation matches code, and the agreed cloud acceptance has evidence. Backend-only work, a static screen, passing unit tests, and a successful deployment are separate milestones within that completion.

For implementation changes, the integrator runs `make check` and `RUN_CLOUD_WORKER_INTEGRATION=1 TEST_DATABASE_URL=... make test` with a dedicated database. A run that skips database tests is not an integration pass. CI additionally performs the pinned vulnerability scan and npm audit; inspect the remote result, rather than inferring it from local checks. Use meaningful tests for changed behavior; documentation-only changes need link/contract consistency checks rather than a new application regression suite.

Exercise relevant failures: wrong identity/workspace, stale version, malformed/oversized data, repeated request, concurrent action, upstream rejection/timeout and interruption after dispatch. For write tools, demonstrate independent approval and preservation of an uncertain outcome without automatic replay. Tests use isolated fixtures; a real-service check has a named target and permitted operation. Never substitute a synthetic result for evidence of actual compatibility.

Each PR links the evidence in [verification](verification.md). Update delivered behavior in [implementation status](implementation-status.md), implementation details in the appropriate contract, and setup/operations steps only when changed. SLOs, model quality and token savings require their own fixed workloads and measurements.

### GitHub and cloud release baseline

The prior feature baseline is integrated. After the initial race-test failure on `59b8f53`, the corrected baseline `e4e382cf574916b15793f27e4998149ffbecd70e` passed all four remote checks: Verify and release-tooling for both push and PR. [PR 1](https://github.com/alan1-666/mcp-gateway/pull/1) merged into `main` as `1163b157fbff6e2e278ee6c12b9be08b3a70b0fb`. The exact merge commit also passed its subsequent main CI run. See the dated [verification record](verification.md) for run links. The five-package feature commit `05d1706780ce7f5c8a085a8801eb144be15c9631` subsequently passed push/PR checks, merged through PR 2 as `da74dc4518c32cc9528667227274863f8967e368`, and passed that merge commit's main CI. Cloud.8 deployed the feature commit, with bounded cloud acceptance recorded in verification. PR 4 subsequently merged the proxy DNS recovery fix, passed main CI at `be62bfaec89bfefb8c632af605aa2515c59e969a`, and cloud.9 deployed its feature commit `9d35bf36087603aa8bb86be4f48ee31cab8fc735`. The catalog-review package in PR 5 subsequently passed source CI and cloud.11 acceptance at `f96f2b646a6487958126ee5707b71b1e8e8d6858`; it adds migration 011. Later changes still require their own review/checks; the monitor-unit correction is a separate operations follow-up and does not replace the cloud.8 application images. Branch-protection enforcement remains a separate configuration check.

`scripts/cloud-release.py` packages committed Git content only, excluding uncommitted working-tree changes and rejecting tracked private/generated material. Its manifest records the full commit and source/migration hashes. Deployment verifies this manifest, retains image IDs, takes an encrypted pre-release backup, applies compatible migrations, waits for service/public HTTPS health and then updates the environment release ID and `current` symlink. Services are explicitly stopped for replacement; no zero-downtime guarantee is implied.

```sh
python3 scripts/cloud-release.py package --source . --commit REVIEWED_COMMIT \
  --release RELEASE_ID --output /tmp/mcp-gateway-RELEASE_ID.tar.gz
```

On an existing cloud host, use `adopt --artifact ...` for a read-only comparison of a legacy current directory with its committed artifact, then `adopt --record` only after the match and health checks pass. Adoption records observed image IDs; it cannot retroactively attest how an old image was built. Use `deploy --artifact ... --base /opt/mcp-gateway` for the new package and `rollback --release PREVIOUS_ID --base /opt/mcp-gateway` for retained artifacts/images. Run each subcommand's `--help` before operation and follow the [cloud guide](cloud-deployment.md#host-preparation-and-release).

Install the operations bundle separately with `scripts/cloud-install-ops.py`; `/opt/mcp-gateway/ops` selects a retained version independently of application `current`. Host release/backup/monitor commands then use that bundle, so rolling back the application does not downgrade backup encryption or remove alert collection. Installation does not enable units or copy credentials; see [operations installation](cloud-deployment.md#install-operations-tools-independently).

Keep **commit SHA, release directory, image IDs/tags, `RELEASE_ID` and `current` symlink** consistent. `cloud-bootstrap.py` preserves an existing `cloud.env`; its `--release` does not change that file. Release tooling refuses unknown source/image/migration state. A rollback with extra database migrations needs a reviewed compatibility JSON naming the exact target release/commit and allowed migration hashes. It never reverses migrations or restores the database.

If a crash interrupts only the pointer/environment transition after the target release was verified, `repair-metadata --base /opt/mcp-gateway` rechecks source, schema, image IDs and health before completing metadata. It does not restart services or repair an unverified deployment. Failed replacement can leave admission stopped: inspect actual state and use an explicit compatible rollback. Never replace this procedure with deletion of volumes or credentials.

Actual cloud.8 deployment, release-tuple checks and encrypted restore drills for both the pre-upgrade schema 005 and post-upgrade schema 010 are recorded in [verification](verification.md). An actual production application rollback has not been exercised. Snapshot/backup-key off-host copying and external alert delivery remain unperformed pending an authorized second destination and a webhook; successful local recovery does not establish either external capability.

## Requirements

- Go 1.26 or newer.
- Node.js 22.19+ (Node 24 recommended) and npm.
- PostgreSQL 16+ for source development, or Docker with Compose.
- Python 3 for release/backup/monitor tools; GnuPG 2 for encrypted recovery snapshots; OpenSSH for an explicitly configured off-host destination.
- A separately configured Pi subscription login for model-assisted work. The gateway itself does not require a model account.

## Docker Compose

```sh
node scripts/bootstrap.mjs
docker compose --env-file .local/compose.env -f deploy/compose/compose.yaml up --build
```

Open [the console](http://127.0.0.1:4782). The bootstrap command creates four independent identities in `.local/identities.json`. Open that private file and use the `admin` token to register tools, the `operator` token to request and execute actions, and the `approver` token to approve another user's action. Tokens stay in browser memory and are cleared on disconnect or refresh.

Bootstrap never overwrites existing identities. The private `.local/compose.env` contains a randomly generated database password and local user/group IDs. Both files are ignored by Git and excluded from images. Compose mounts the identity file read-only; backend processes run with the matching local UID to read its restrictive file permissions.

The Compose deployment binds only loopback ports. It is a single-host installation and does not provide high availability. The separate cloud recipe provides TLS, persisted accounts and scheduled backups. This development recipe retains static tokens and must not be exposed publicly.

### Permit a downstream service

The default egress policy permits no downstream origin. Set explicit origins when starting services:

```sh
HTTP_ALLOWED_ORIGINS=https://api.example.org \
  docker compose --env-file .local/compose.env -f deploy/compose/compose.yaml up --build
```

An origin includes scheme, hostname and optional port. Tool URLs cannot contain embedded credentials, query strings or fragments. Private and loopback destinations additionally require an explicit `HTTP_ALLOWED_CIDRS` entry. Cloud metadata/link-local destinations are blocked; redirects are never followed. DNS answers are checked and the connection uses the checked address. Proxy environment variables are not inherited by the downstream HTTP client.

For Docker-hosted internal services, use a reachable service hostname and an explicitly authorized network. `127.0.0.1` inside a container refers to that container, not the Docker host.

## Run from source

Create an empty, dedicated PostgreSQL database, then:

```sh
npm ci --ignore-scripts
node scripts/bootstrap.mjs
cp .env.example .env
# Edit DATABASE_URL and the downstream allowlist in .env.
make migrate
make dev
```

`make dev` builds and starts the API, gateway, recovery worker and Vite console. Interrupt it to stop its children. Database migrations are explicit; application startup does not silently change schema. The migration runner uses an advisory lock, a transaction and checksum verification.

| Component | Default address | Purpose |
| --- | --- | --- |
| Console | `http://127.0.0.1:4782` | Management and operations |
| API | `http://127.0.0.1:8090/api/v1` | Authenticated management and execution |
| MCP | `http://127.0.0.1:8091/mcp` | Authenticated Streamable HTTP |
| Health | `/healthz`, `/readyz` on each Go HTTP service | Process and database availability |

Vite forwards `/api` and `/mcp` to the appropriate local services. Production console assets use the same paths through nginx. Bearer tokens must never be included in URL query parameters.

## Register and execute a tool

1. Connect to the console with an administrator identity.
2. Register a tool with a business name, description, risk, input JSON Schema and an allowed HTTP URL.
3. Publish it. Drafts are not discoverable by operators or external MCP clients.
4. Connect as an operator, select the tool, inspect its schema and prepare exact JSON arguments.
5. A read operation becomes `READY`. A write becomes `WAITING_APPROVAL` without contacting the downstream service.
6. A different administrator or approver approves the write. Approval expires after 30 minutes.
7. The requesting operator executes the fixed operation ID. Review its result and durable event history.

Administrators review HTTP or MCP contract changes as candidates, inspect field differences and publish a new immutable version using `expected_version`. A historical definition can be copied into a rollback candidate only while its upstream contract remains compatible. Retiring a tool disables admission; restoration requires a newly reviewed candidate. The separate MCP response-policy editor provides sample preview and version-checked changes. Existing prepared operations retain their full original snapshot. See the [remote MCP contract](remote-mcp-contract.md#reviewed-versions-and-retirement).

`GET` is the only permitted method for a read tool. Arguments become query parameters; strings are encoded directly and other JSON values use their JSON representation. Other methods send the fixed arguments as JSON. Paths are static. The operator cannot override the URL, headers, method or credential reference.

Successful HTTP calls must return a single bounded JSON value. The API also accepts an optional `output_schema`; use it to express business success requirements, including `const` values. Without that contract, `SUCCEEDED` means a 2xx response and valid JSON, not an independently verified business effect. Empty responses and non-JSON responses require a future explicit adapter contract.

Known secret field names are redacted recursively before results are stored. This is a baseline filter, not comprehensive data classification or DLP. Do not register sensitive payloads until their output policy is adequately defined; arbitrary strings are not guaranteed to be secret-free. Arguments and redacted results are currently retained in PostgreSQL without an automated retention policy.

Represent integer identifiers outside JavaScript's safe range as strings in tool contracts. Go preserves JSON number precision; the console and Pi bridge reject unsafe numeric arguments rather than silently changing them. A console operation containing an integer it cannot accurately represent cannot be approved or executed from that view.

## Authentication and credentials

Identity configuration is a private JSON array:

```json
[
  {
    "token": "<generate-at-least-32-random-characters>",
    "id": "service-operator",
    "workspace_id": "workspace-a",
    "role": "operator"
  }
]
```

Roles are `admin`, `operator`, `approver`, and `viewer`. Workspace and role are derived exclusively from the authenticated identity. Request headers cannot override them. Operators/viewers see their own operation records; administrators/approvers can inspect operations in their workspace. Approvers cannot execute tools. The Pi runner requires an operator identity.

Tokens are loaded at process startup and hashed in memory for lookup. Rotation or revocation requires restarting both API and gateway. This static-token development mode does not provide live revocation. Cloud mode instead uses PostgreSQL-backed accounts, sessions, personal API keys and scoped machine-client keys. Cloud client administration requires an administrator browser session; development administrator tokens can exercise those routes locally. Client grants/scopes, expiry and rotation are checked live, including before dispatch of old prepared operations. Enterprise OIDC and short-lived task credentials remain pending.

Downstream credentials are separate. For a deployment-managed static reference, set `GATEWAY_CREDENTIALS_FILE` on the API and gateway to a private JSON file:

```json
[
  {
    "workspace_id": "workspace-a",
    "ref": "ORDERS_API",
    "origin": "https://api.example.org",
    "headers": { "Authorization": "Bearer <downstream-secret>" }
  }
]
```

The matching tool contains only `credential_ref: "ORDERS_API"`. The credential must belong to the same workspace and exact allowed origin. Reserved transport and idempotency headers cannot be overridden. Add a read-only mount and the environment variable explicitly if using Compose. Do not put this file into source control or send it to the model.

For admin-managed credentials, configure `GATEWAY_MASTER_KEY_FILE` with a private file containing exactly 32 raw random bytes, then use the console or `/api/v1/credentials`. This key is required in cloud mode and optional in token development mode; without it the managed credential routes are unavailable. Keep it outside source control and preserve it with encrypted database recovery material. Create/rotate accepts header values but every response contains metadata only. Rotation and disablement affect new attempts without restart, while the same workspace/exact-origin and reserved-header rules remain in force. Static and managed references cannot shadow each other. Do not replace or re-encode an existing master key.

A machine-client key authenticates an agent **to the Gateway**; a managed downstream credential authenticates the Gateway **to an upstream**. A Pi subscription is a third, independent model-provider connection. See the [credential/client contract](remote-mcp-contract.md#network-and-credential-controls) for bounds and one-time key behavior.

## Execution and recovery

- `POST /operations` reserves a workspace-scoped idempotency key and fixed arguments. Reusing that key with a different tool or payload returns `409`.
- `POST /operations/{id}/execute` atomically changes `READY` to `DISPATCHING`. Concurrent calls return the recorded operation and cannot independently send it again.
- Each request carries the gateway operation ID in `Idempotency-Key`. This does not imply that every downstream service honors the header.
- A confirmed valid result is stored with its event in one database transaction.
- Ambiguous write outcomes become `UNKNOWN`. A new request must not be used as a substitute for determining the original outcome.
- The recovery worker scans every 15 seconds and marks `DISPATCHING` operations older than 150 seconds as `UNKNOWN`. It never resends them. The maximum downstream timeout is 120 seconds.
- A late completion cannot replace a recovered `UNKNOWN` state. An independent admin/approver can append human evidence with `expected_last_id`; the original state/result remain unchanged and no tool is invoked. Requesters and recorded dispatchers cannot reconcile their own action. Adapter-specific outcome queries remain future work.
- Client disconnects do not abandon a claimed operation; execution and final persistence each retain a finite deadline.

Operation/audit history now has server-side filters, live totals and keyset pages (default 50, maximum 100). Capacity admission runs before a business dispatch and reports REST `429` plus `Retry-After`, or an MCP `isError` result with retry delay/scope. Inspect/retry the same operation after backoff rather than inventing a new key.

This provides single dispatch of a recorded operation and conservative uncertainty handling. It does not promise exactly-once business effects in arbitrary external systems.

## Pi integration

See [the runner guide](../apps/agent-runner/README.md) for login, selection, execution and resume commands. `--check` verifies the gateway identity and local Pi configuration without sending a model request. It does not guarantee that the provider will accept a future login refresh.

Only `search_tools`, `get_tool_schema`, `prepare_action`, `invoke_tool` and `get_operation` are available to the model. Built-in shell/file tools, repository resources and arbitrary extensions are disabled. The host writes an intent journal before preparing or dispatching actions. Pending approval and uncertain execution pause the runner.

The CLI is user-hosted. The separate cloud worker uses server-side subscriptions, durable platform Runs and fenced leases; see the [cloud task contract](cloud-run-contract.md). Distributed multi-host scheduling and organization-wide model budgets remain architecture work.

## Verification

```sh
npm ci --ignore-scripts
make check
RUN_CLOUD_WORKER_INTEGRATION=1 TEST_DATABASE_URL='postgres://user:password@127.0.0.1:5432/gateway_test?sslmode=disable' make test
python3 -m unittest discover -s scripts/tests -v
```

Use a dedicated empty test database. Integration tests apply migrations and create uniquely scoped workspace records; they do not truncate existing tables. Without `TEST_DATABASE_URL`, database tests are explicitly skipped. CI always provides PostgreSQL and runs them. `RUN_CLOUD_WORKER_INTEGRATION=1` additionally starts the production Node worker client against the real Go API and an isolated PostgreSQL schema; install npm dependencies first. Its model executor is deterministic and makes no provider request.

Coverage includes independent approval, workspace/client/snapshot isolation, encrypted credential rotation/revocation, candidate concurrency, paginated audit/operations, append-only UNKNOWN evidence, scoped admission budgets, schema/projection bounds, duplicate preparation/dispatch, interrupted writes, actual MCP SDK calls and the Node worker boundary. Python tests cover release manifests/rollback/metadata transitions, encrypted bundles and monitoring/backup failure handling. Controlled fixtures do not establish a working external destination. No automated test sends a real model request.

Architecture SLOs, throughput, full MCP client compatibility, backup restoration and container deployment are separate acceptance gates, not results inferred from unit tests.
