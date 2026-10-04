# Cloud Agent Run contract

This contract coordinates the Go API, Pi worker and React console implementation. It describes the shared integration boundary; see implementation status for verified delivery.

## Workflow and ownership

1. Agree on this contract and ownership before parallel implementation.
2. Backend owns `internal/runs`, `migrations/003_agent_runs.sql` and backend tests.
3. Pi worker owns `apps/agent-runner`; console owns `apps/console`.
4. Integrator owns platform wiring, deployment, top-level docs, Git commits and integration verification.
5. Changes to shared contracts are communicated before editing. Agents do not commit or push independently.
6. Acceptance requires actual PostgreSQL state transitions, worker cancellation/fencing tests, frontend type/build checks and an integrated simulated worker flow. Live inference is separately verified with an explicitly configured server-side subscription login.

## Public API

Cloud sessions or personal API keys authenticate using the existing identity middleware. Admins/operators can create tasks. Creators and workspace administrators can read, cancel and resume them. Other workspace members cannot read prompts or outputs. Task tools always run with operator-level permissions, even when the creator is an administrator; the agent cannot approve, publish, administer accounts or mint keys.

All paths start with `/api/v1` and JSON requests use the existing error envelope. IDs and event cursors are strings.

| Endpoint | Request / response |
| --- | --- |
| `GET /runs` | `{items: Run[]}`, latest 100 accessible records |
| `POST /runs` | `{prompt, idempotency_key}` → Run; prompt 1–8000 characters, immutable; repeat same key returns the same Run; changed prompt conflicts |
| `GET /runs/runtime` | `{online, model_ready, provider, model_id, error_code, last_seen_at}` for the configured workspace; no credentials |
| `GET /runs/{id}` | Run |
| `GET /runs/{id}/events?after=0` | `{items: RunEvent[]}`, at most 200; exclusive cursor |
| `POST /runs/{id}/cancel` | `{}` → Run; fences the lease and stops new tool admissions |
| `POST /runs/{id}/resume` | `{}` → Run; only waiting/review states, retains task/worker identity and operation references |

`Run`: `id`, `workspace_id`, `actor_id`, `prompt`, `state`, `attempt` (integer), `output` (string), `error_code` (string), `waiting_operation_id` (string), `created_at`, `updated_at`. Empty optional strings may be omitted. States: `QUEUED`, `RUNNING`, `WAITING_APPROVAL`, `WAITING_CREDENTIALS`, `NEEDS_REVIEW`, `SUCCEEDED`, `FAILED`, `CANCELLED`.

`RunEvent`: `id` (decimal string), `run_id`, `type`, `data` (object), `created_at`. Primary types: `RUN_CREATED`, `RUN_CLAIMED`, `TEXT_DELTA` (`{text, attempt}`), `MODEL_STARTED`, `TOOL_STARTED`, `TOOL_COMPLETED`, `RUN_WAITING_APPROVAL`, `RUN_WAITING_CREDENTIALS`, `RUN_NEEDS_REVIEW`, `RUN_SUCCEEDED`, `RUN_FAILED`, `RUN_CANCELLED`, `RUN_RESUMED`. Additional types are rendered as plain event records. Never expose raw credentials/provider exceptions or render model output as HTML.

Cancellation does not undo a downstream action already admitted; its operation ledger must retain the eventual or uncertain outcome. `SUCCEEDED` means the Agent task finished, not that every requested business action succeeded.

## Internal runner protocol

Internal endpoints are installed on the API service but blocked by the public proxy. They use a separate high-entropy daemon secret from `RUNNER_SHARED_SECRET_FILE`, never a human admin key. The server binds this secret to `RUNNER_WORKSPACE_ID` (default `team`). All requests carry `Authorization: Bearer <daemon secret>`; leased routes additionally carry `X-Run-Lease: <lease token>`. The model never sees either credential.

| Endpoint | Request / response |
| --- | --- |
| `POST /internal/runner/status` | `{worker_id, model_ready, provider, model_id, error_code}` → `{ok:true}`; expires after 90 seconds |
| `POST /internal/runner/claim` | `{worker_id}` → `{run:null}` or `{run: Run, lease_token, lease_expires_at}` |
| `POST /internal/runner/runs/{id}/heartbeat` | `{}` → `{ok:true, lease_expires_at}`; stale/cancelled/revoked lease gets 409/403 |
| `POST /internal/runner/runs/{id}/events` | `{event_key, type, data}` → `{ok:true}`; same event key/body deduplicates, changed body conflicts |
| `POST /internal/runner/runs/{id}/finish` | `{state, output, error_code, waiting_operation_id}` → Run; permitted destinations are the waiting/review/success/failure states |

Run tools mirror the existing Gateway client under `/internal/runner/runs/{id}/gateway`: `GET /me`, `GET /tools?query=`, `GET /tools/{tool_id}`, `POST /operations` (`{tool_id,arguments,idempotency_key}`), `GET /operations/{id}`, `POST /operations/{id}/execute` (`{}`). Tools are published and enabled; operation access is restricted to bindings created by this Run. Prepare keys are namespaced by Run ID. Preparing never executes a downstream action; binding must be durable before returning the operation. Avoid duplicate write operations for the same tool/arguments within the Run, including when a preparation response is lost.

## Persistence and failure policy

- Claim queued rows atomically with PostgreSQL row locks; one active lease per Run. Lease lifetime is 45 seconds, heartbeat interval 10 seconds. Record a monotonically increasing attempt and an unpredictable lease token stored as a hash.
- Every internal mutation and tool admission validates workspace, lease, expiry and the creator's current enabled role. Revocation cannot be bypassed by a previously issued lease.
- Expired active leases become `NEEDS_REVIEW` with a durable event. They are not automatically dispatched again. Resume retains the original worker binding and increments the next claim attempt; stale workers remain fenced.
- Resuming is blocked while a bound operation is `UNKNOWN` or `DISPATCHING`, or awaiting approval. A waiting-operation reference must belong to this Run. Late/stale events and finishes cannot overwrite a newer attempt or cancellation.
- Events are stamped with the current fenced attempt by the API. A resumed worker archives any unacknowledged events/text from the prior attempt locally and emits a `RECOVERY_CHECKPOINT` summary; it never retransmits old text as a new attempt. Same-attempt delivery retries retain the exact event key and body.
- Worker uses a durable per-Run session/intent directory and refuses unsafe recovery if a prior session is missing. Restart must not silently create a new task identity or resend an uncertain write.
- Initial concurrency is one Agent task per worker, one active task per user, and at most ten queued tasks per user. Bound prompts to 8000 characters, event data to 16 KiB, accumulated output to 64 KiB and event count to 1000 per attempt. Event bodies total at most 1 MiB per attempt. Bound tool calls and wall time independently. Lifetime server admission budget is forty new prepare intents/ready dispatch admissions; the Pi worker separately caps all model tool calls at forty across resumes. Existing tool execution ledger remains authoritative.
- Logs contain IDs/status/durations, never model credentials, invitation secrets or arbitrary prompt/output bodies.

## Pi and deployment

Worker configuration: `RUNNER_API_URL`, `RUNNER_SHARED_SECRET_FILE`, `RUNNER_WORKER_ID` (stable), `GATEWAY_STATE_DIR`, `PI_PROVIDER`, `PI_MODEL`, `PI_CODING_AGENT_DIR`, and `RUNNER_HTTP_ALLOWED_HOSTS` (explicit comma-separated internal host allowlist). Plain HTTP is allowed only for explicitly configured internal API hosts/loopback, never arbitrary remote destinations. Redirects are rejected.

The worker uses the existing subscription-only model loader, disables paid API-key fallback and built-in file/shell tools, limits runs to five minutes and forty governed tool calls, and publishes only sanitized runtime status when credentials are unavailable. It may complete a claimed run as `WAITING_CREDENTIALS`; an explicit resume continues it after login is configured.

Server-side Pi login is configured separately in a dedicated persistent volume. Deployment does not copy a personal laptop's model credentials. Deterministic test doubles validate orchestration without consuming a subscription; public runtime status must distinguish missing credentials from a ready configured model. A configured login is not a guarantee of provider quota or successful inference.

This single-host runner uses bounded authenticated HTTP control calls. A distributed gRPC Runner control plane remains part of the broader target architecture.
