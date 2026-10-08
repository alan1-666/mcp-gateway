# Contributing to Rillgate

Rillgate is an MCP gateway. Focus contributions on connecting upstream tools,
permission-aware discovery, controlled execution, bounded results and observable
failures. Existing MCP clients consume the gateway directly; an Agent workbench
or a separate client application is not required.

## Start here

1. Read the [README](README.md) for supported capabilities and boundaries.
2. Read [implementation status](docs/implementation-status.md) and the
   [roadmap](docs/gateway-roadmap.md) before selecting work.
3. Use [development](docs/development.md) for setup and
   [architecture](docs/architecture.md) for component responsibilities.
4. Use [native client setup](docs/mcp-clients.md) to connect Pi, Codex or Claude.
   Client model authentication and Gateway access keys are separate.

## Local setup

For the quickest local environment, install Node.js 22.19+ and Docker Compose:

```sh
git clone https://github.com/alan1-666/mcp-gateway.git
cd mcp-gateway
node scripts/bootstrap.mjs
docker compose --env-file .local/compose.env -f deploy/compose/compose.yaml up --build
```

Open `http://127.0.0.1:4782/console/`. Local development identities are generated
in `.local/identities.json`. Keep that file private. The default downstream
allowlist is empty; follow the development guide to permit a specific upstream.
No production credentials or model subscription is needed for deterministic tests.

For editing Go services directly, use the Go version in `go.mod`, Node.js 24,
and a dedicated PostgreSQL 16+ development database. Follow the source setup in
[development](docs/development.md#run-from-source). Install JavaScript dependencies
with `npm ci --ignore-scripts`; retain the committed lockfile.

## Find the relevant code

| Location | Responsibility |
| --- | --- |
| `cmd/` | API, MCP gateway, worker, Connector and migration entry points |
| `internal/transport/` | HTTP and MCP interfaces |
| `internal/core/`, `internal/execution/` | Contracts, operation lifecycle and dispatch |
| `internal/adapters/`, `internal/upstreams/` | Upstream execution, sessions, catalogs and diagnostics |
| `internal/clients/`, `internal/identity/`, `internal/credentials/` | Access keys, identities and upstream credentials |
| `internal/store/postgres/`, `migrations/` | Persistence and versioned schema |
| `apps/console/` | Website and bilingual management console |
| `apps/agent-runner/` | Optional Pi integration; not a Gateway dependency |
| `tests/integration/`, `scripts/tests/` | Database/protocol and deployment-tooling verification |
| `docs/evidence/` | Dated, sanitized acceptance records |

## Work together

- Agree one bounded issue: expected behavior, affected contracts, failure cases
  and completion evidence. The roadmap lists the remaining Gateway work.
- Start a feature branch from current `origin/main`. Use a pull request for
  review; avoid concurrent direct edits to `main`.
- Assign one owner for shared contracts, migration numbering, dependency changes
  and release tooling. Coordinate these before parallel implementation.
- Keep commits focused. Use `gofmt` for Go, preserve existing TypeScript conventions,
  and update both English and Chinese console messages when behavior changes.
- Never edit an already released migration. Add a new migration with reviewed
  upgrade and rollback compatibility.
- Never weaken live permission checks or replay an uncertain write to make a
  retry test pass. Preserve the distinction between pending, failed and UNKNOWN.

## Validate a change

Use a dedicated test database, never the cloud application database:

```sh
npm ci --ignore-scripts
make check
export TEST_DATABASE_URL='postgres://user:password@127.0.0.1:5432/gateway_test?sslmode=disable'
RUN_CLOUD_WORKER_INTEGRATION=1 RUN_MCP_LOAD_EVALUATION=1 make test
python3 -m unittest discover -s scripts/tests -p 'test_*.py' -v
```

Replace the example database credentials locally. Missing `TEST_DATABASE_URL`
skips database tests and is not an integration pass. The fixed MCP workload uses
loopback fixtures and an isolated schema; see [measurement instructions](docs/mcp-load-evaluation.md).

CI additionally checks vulnerabilities and coverage. Declared Go files require
at least 90% statement coverage; console TypeScript logic requires 95% lines and
90% branches/functions. These are scoped gates, not whole-application coverage.
See [coverage interpretation](docs/development.md#coverage-gates-and-interpretation).
Check CI on the exact commit being merged. Verify affected console interactions
and real upstream behavior separately when the change requires them.

## Documentation and release

In each PR, describe behavior, tests, compatibility/migrations and remaining
limits. Update the relevant contract and implementation status. Record measured
results with the environment, source revision, workload and failure samples.
Do not commit tokens, passwords, `.env`, `.local`, raw business responses or private
client transcripts. Share needed access through a separate private channel.

Merging code does not deploy it. The release owner follows the
[cloud deployment guide](docs/cloud-deployment.md), deploys a known commit and
records health and cloud acceptance. Contributors should use isolated local
fixtures; shared cloud tests need an agreed target, bounded calls and cleanup.
