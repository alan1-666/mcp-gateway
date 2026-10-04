# Pi Agent Runner

A user-hosted command-line agent that connects an existing Pi subscription login to MCP Gateway. It exposes five controlled tools: `search_tools`, `get_tool_schema`, `prepare_action`, `invoke_tool`, and `get_operation`.

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

## Runtime boundary

This package implements a standalone local CLI and persisted Pi conversation resume. The gateway remains authoritative for permissions, approvals and external operation outcomes. It does not implement the architecture's distributed runner leases, automatic checkpoint reconciliation, model budget accounting, multi-host scheduling or guaranteed recovery of incomplete model turns. Conversation resume alone does not establish external side-effect completion.

## Verification

```sh
npm run check --workspace @mcp-gateway/agent-runner
npm test --workspace @mcp-gateway/agent-runner
```

Tests use loopback HTTP servers and temporary journals. They exercise authorization headers, redirect rejection, response limits, approval pauses, idempotent preparation and the lost-execution-response window. Tests do not contact a model provider or consume subscription quota.
