# Development and operation

The repository contains executable Go services, a React console and a Pi integration client. This page describes local source development; use the [cloud deployment guide](cloud-deployment.md) for the hosted product. See [implementation status](implementation-status.md) for the boundary between working software and the complete target architecture.

## Requirements

- Go 1.26 or newer.
- Node.js 22.19+ (Node 24 recommended) and npm.
- PostgreSQL 16+ for source development, or Docker with Compose.
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

Tools are immutable after registration apart from publish/enable state. To change a contract, register a distinct tool name. Editing versions, signed releases and rollback are not exposed yet.

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

Tokens are loaded at process startup and hashed in memory for lookup. Rotation or revocation requires restarting both API and gateway. This static-token development mode does not provide live revocation. Cloud mode instead uses PostgreSQL-backed accounts, sessions and API keys; enterprise OIDC and scoped task credentials remain pending.

Downstream credentials are separate. Set `GATEWAY_CREDENTIALS_FILE` on the API and gateway to a private JSON file:

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

## Execution and recovery

- `POST /operations` reserves a workspace-scoped idempotency key and fixed arguments. Reusing that key with a different tool or payload returns `409`.
- `POST /operations/{id}/execute` atomically changes `READY` to `DISPATCHING`. Concurrent calls return the recorded operation and cannot independently send it again.
- Each request carries the gateway operation ID in `Idempotency-Key`. This does not imply that every downstream service honors the header.
- A confirmed valid result is stored with its event in one database transaction.
- Ambiguous write outcomes become `UNKNOWN`. A new request must not be used as a substitute for determining the original outcome.
- The recovery worker scans every 15 seconds and marks `DISPATCHING` operations older than 150 seconds as `UNKNOWN`. It never resends them. The maximum downstream timeout is 120 seconds.
- A late completion cannot replace a recovered `UNKNOWN` state. Human reconciliation and adapter-specific outcome queries are still pending.
- Client disconnects do not abandon a claimed operation; execution and final persistence each retain a finite deadline.

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
```

Use a dedicated empty test database. Integration tests apply migrations and create uniquely scoped workspace records; they do not truncate existing tables. Without `TEST_DATABASE_URL`, database tests are explicitly skipped. CI always provides PostgreSQL and runs them. `RUN_CLOUD_WORKER_INTEGRATION=1` additionally starts the production Node worker client against the real Go API and an isolated PostgreSQL schema; install npm dependencies first. Its model executor is deterministic and makes no provider request.

Coverage includes independent approval, expiry, workspace and actor isolation, schema bounds, duplicate preparations, concurrent execution, interrupted writes, restart persistence, actual MCP SDK client/server calls, credential-bound egress, local Pi intent durability and HTTP failure handling. No automated test sends a real model request.

Architecture SLOs, throughput, full MCP client compatibility, backup restoration and container deployment are separate acceptance gates, not results inferred from unit tests.
