# Pi Agent Runner

A user-hosted command-line agent that connects an existing Pi subscription login to MCP Gateway. It exposes five controlled tools: `search_tools`, `get_tool_schema`, `prepare_action`, `invoke_tool`, and `get_operation`.

`search_tools` performs literal name/description search and returns one page of authorized, published tool summaries: `{ items, next_cursor?, total }`. Queries are limited to 200 UTF-8 bytes; optional `limit` accepts 1–50 (default 25), and `cursor` accepts the unchanged cursor from the previous page (at most 2048 UTF-8 bytes). Keep the same query when continuing. No further pages or schemas are fetched automatically; request `get_tool_schema` only for selected tools. `total` counts visible matches and can change as publication changes. Tool descriptions remain untrusted data.

The Pi SDK packages are pinned to **1.0.2**. This maintenance release includes the upstream `brace-expansion` security fix and removes the older published shrinkwrap that prevented npm overrides from applying. The repository lockfile pins the complete dependency tree; use `npm ci` for repeatable installation.

## Run

Install the repository's npm workspaces from the repository root. Log in and select a subscription model using the Pi CLI on the same machine. The runner delegates credential access to the Pi SDK; credentials are never sent to the gateway or model context.

Set `GATEWAY_TOKEN` to an **operator** token through your local environment or secret manager. Set `GATEWAY_URL` if the gateway is not at `http://127.0.0.1:8090`. Remote gateways require HTTPS. Admin and approver tokens are rejected by this client.

```sh
npm run run --workspace @mcp-gateway/agent-runner -- --check
npm run run --workspace @mcp-gateway/agent-runner -- --prompt "Find the service status tool, inspect its schema, and check the service status."
npm run run --workspace @mcp-gateway/agent-runner -- --session <session-id> --prompt "Check the operation state and continue if it has been approved."
```

`--check` checks the authenticated gateway identity and locally configured Pi model/login without sending an inference request. It does not prove that a subscription token is still valid. Explicit model selection uses `PI_PROVIDER` and `PI_MODEL`. The runner requires subscription OAuth, rejects virtual models, and never falls back to API-key billing.

The runner does not load repository instructions, extensions, skills, shell tools or filesystem tools. It disables automatic model retries, cache warming and context compaction. Each invocation is bounded by a five-minute deadline and 40 tool calls. An operator must deliberately run a prompt to consume their subscription quota.

## Approval and recovery

1. `prepare_action` writes an intent to a local journal and then asks the gateway to prepare the operation. Preparing an operation never dispatches the business action.
2. `WAITING_APPROVAL` pauses the agent. Approve the exact operation using a separate authorized console identity, then resume the same session.
3. `invoke_tool` checks the gateway's current state and writes a dispatch marker before sending an execution request. Arguments cannot change during execution.
4. If an execution response is lost, the runner reports an unknown outcome. It can read the operation state, but will not repeat that execution request. Inspect the gateway audit trail before deciding any further action.

Pi sessions and intent journals live in `.gateway/sessions` and `.gateway/intents` at the repository root. Set `GATEWAY_STATE_DIR` for a different location. Journals store hashes and identifiers, not tool arguments or credentials. **Pi session transcripts can contain business inputs and tool results**; keep this directory private and exclude it from version control. The parent directory is created with mode `0700`; journals use mode `0600`.

Identical write-tool IDs and arguments reuse one operation throughout a session, even if a new model tool-call ID is generated. This intentionally prevents a model from creating duplicate actions after a timeout. The runner reads the tool's current risk classification from the gateway before preparing it. A new read-only call gets a fresh operation so status can be checked again; replaying the same call ID retains its key. Intent journals are bounded to 500 entries and 1 MiB per session. Retain journals for as long as their sessions may be resumed; deleting them removes duplicate protection. Resume is restricted to this runner's session directory and binds the journal to the gateway origin, workspace and actor.

A lock prevents concurrent processes from running the same session. A process crash leaves its `<session-id>.json.lock` file in the intents directory. Stop any remaining runner process and inspect the matching journal and gateway operations before removing that single stale lock. Then resume the same session. Never remove a lock while its runner is active.

Numeric arguments must be finite. Integers outside JavaScript's safe integer range are rejected before creating an intent or sending a request; pass their original decimal representation as a string using a compatible tool schema. Converting an already-rounded JavaScript number to a string does not recover its precision. A failed operation-state lookup reports an unknown outcome, never that the action has not executed.

## Cloud worker

The daemon runs tasks created in the web console. It authenticates through a separate server-bound daemon secret and a short-lived lease for each Run. It never uses a human administrator's API key. Tool access is scoped by the Go API to the Run, workspace and creator's current permissions.

```sh
npm run worker --workspace @mcp-gateway/agent-runner -- --check
npm run worker --workspace @mcp-gateway/agent-runner -- --once
npm run worker --workspace @mcp-gateway/agent-runner
```

| Configuration | Meaning |
| --- | --- |
| `RUNNER_API_URL` | Required internal API origin, for example `http://api:8090` |
| `RUNNER_SHARED_SECRET_FILE` | Required path to a dedicated high-entropy daemon secret, matching the Go API |
| `RUNNER_WORKER_ID` | Stable worker identity; generated and persisted if omitted, cannot change for an existing state volume |
| `GATEWAY_STATE_DIR` | Dedicated persistent worker directory; defaults to `<repository>/.gateway-worker` |
| `RUNNER_HTTP_ALLOWED_HOSTS` | Explicit comma-separated internal hostnames allowed over HTTP, for example `api`; loopback is already allowed |
| `PI_CODING_AGENT_DIR` | Pi-supported configuration/login directory; deploy this on a separate persistent volume |
| `PI_PROVIDER`, `PI_MODEL` | Optional explicit subscription model, with the same no-fallback policy as the CLI |

`--check` publishes sanitized runtime status and exits without claiming a task or calling a model. `model_ready` means a subscription model/login is locally configured; it does not prove token validity or available quota. The daemon re-reads local login/model settings on each polling cycle, so completing Pi login does not require restarting the daemon. A Run already in `WAITING_CREDENTIALS` requires an explicit console resume.

For server-side login, use an administrator terminal inside the runner container and run `node node_modules/@earendil-works/pi-coding-agent/dist/cli.js`. The container's `PI_CODING_AGENT_DIR` directs Pi to its dedicated volume. Do not copy a laptop's authentication files into the deployment or paste credentials into the console.

### Cloud persistence and recovery

- One task executes at a time. The worker heartbeats its 45-second lease every 10 seconds. Cancellation, creator revocation, lease loss or uncertain heartbeat delivery aborts the model and fences all subsequent tool requests through the API. No stale finish is submitted.
- Per-Run Pi sessions, intent journals and queued events are persisted atomically under `runs/<run-id>`. A started Run with a missing session or intent journal enters `NEEDS_REVIEW`; the worker never replaces its identity or quietly starts over.
- Events use stable keys and an ordered durable queue. Lost acknowledgements can retry the same immutable event within an attempt. Text is persisted locally and batched at 512 bytes or 200 milliseconds. Event bodies are capped at 16 KiB, each attempt at 1,000 events / 1 MiB, and output at 64 KiB. The five-minute attempt deadline and 40 governed calls per Run are enforced locally; the API separately enforces its admission budget.
- On an explicit resume, pending events and unconfirmed text from the old attempt are archived as `attempt-<n>-unconfirmed.json`. They are not replayed as new output. A `RECOVERY_CHECKPOINT` event records the archived counts. The business operation IDs and dispatch markers remain unchanged.
- `WAITING_APPROVAL` requires a separate approver in the console. Unknown operations require review and reconciliation before resume. Cancellation never implies that a previously admitted external action was rolled back.
- A daemon lock records hostname, PID, and Linux boot/process-start identifiers. Dead owners and verified PID reuse can be recovered; live or unverifiable owners fail closed. Use a stable container hostname and a single worker process per state volume. On a lock error, stop every process using the volume, inspect `worker.lock` and its recorded process identity, and only then remove a stale lock or `worker-lock-recovery` directory. Preserve Run state, Pi sessions, intent journals and archives.

State files and Pi transcripts contain private business data. Keep both persistent volumes access restricted, back them up together with the gateway database, and retain Run journals while their tasks may be resumed. The worker logs only IDs and sanitized status codes; it does not log arbitrary prompts, outputs or provider exception bodies.

## Runtime boundary

This package implements a standalone local CLI and a single-host cloud worker with authenticated HTTP control, durable conversation resume and lease fencing. The gateway remains authoritative for permissions, approvals and external operation outcomes. Distributed gRPC scheduling, multi-host session migration, model-cost accounting and automatic recovery of incomplete model turns remain outside this implementation. Conversation resume alone does not establish external side-effect completion.

## Verification

```sh
npm run check --workspace @mcp-gateway/agent-runner
npm test --workspace @mcp-gateway/agent-runner
```

Tests use loopback HTTP servers, temporary journals and injected execution/probe functions. They cover credentials, approval/resume, lost lease, cancellation, event retries/order/limits, old-attempt archives, missing checkpoints, numeric precision and unknown operation outcomes. Tests do not contact a model provider or consume subscription quota. `CloudWorker.runOnce()` and `executeLease()` accept injected `WorkerAPI`, `probeModel` and `execute` implementations for integration tests against the real Go API.
