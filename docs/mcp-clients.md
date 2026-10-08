# Native MCP clients

Rillgate exposes a Streamable HTTP endpoint at `https://rillgate.cn/mcp`.
Clients authenticate with a scoped Gateway client key. Configure the server once;
the seven Gateway tools discover and invoke the reviewed business tools available
to that client. A Pi runner process or a Rillgate Agent task is not required.

## Client key and permissions

In the console, create a client with `tools:read` and `tools:invoke` scopes and
explicit grants for the tools it needs. Supply its key as a local environment
variable named `RILLGATE_TOKEN`. Keep the actual key out of configuration committed
to a repository, prompts and screenshots. A model subscription authenticates the
client's model requests; it does not replace this Gateway key.

Use an isolated client key for acceptance tests and revoke it afterward. A cached
tool list is not an access grant: the Gateway checks permissions at execution and
when reading a stored result.

## Pi native MCP

The inspected installation is Pi 1.0.0, with native MCP support. Its installed
`docs/mcp.md` and `pi mcp --help` document this format. Merge a named server into
`~/.pi/agent/mcp.json`, preserving other configured servers:

```json
{
  "mcpServers": {
    "rillgate": {
      "url": "https://rillgate.cn/mcp",
      "headers": {"Authorization": "Bearer ${RILLGATE_TOKEN}"},
      "exposure": "direct"
    }
  }
}
```

`direct` exposes the seven Gateway entry tools to the model. The actual business
tool catalog remains behind `search_tools` and `get_tool_schema`.

```sh
pi mcp list --json
```

This checks connection and tool discovery without a model request. A successful
check alone does not verify business execution. Use a normal Pi session for an
approved read, then inspect its operation record in the Gateway console.

## Claude Code

The inspected installation is Claude Code 2.1.268. Supply a separate configuration
file, for example `rillgate-mcp.json`:

```json
{
  "mcpServers": {
    "rillgate": {
      "type": "http",
      "url": "https://rillgate.cn/mcp",
      "headers": {"Authorization": "Bearer ${RILLGATE_TOKEN}"}
    }
  }
}
```

```sh
claude --strict-mcp-config --mcp-config ./rillgate-mcp.json
```

The acceptance run additionally disables built-in tools and hooks, allows only
`mcp__rillgate__*`, disables session persistence, and bounds execution time and model
budget. Normal users can retain their other clients and tools; acceptance must
show which tool actually handled the request.

## Codex CLI

The inspected installation is Codex CLI 0.161.0. Add a server entry to your Codex
configuration, with the key supplied through the environment:

```toml
[mcp_servers.rillgate]
url = "https://rillgate.cn/mcp"
bearer_token_env_var = "RILLGATE_TOKEN"
required = true
```

Keep normal interactive tool approvals for general use. The bounded, noninteractive
acceptance uses `default_tools_approval_mode = "approve"` only for this server,
with a temporary key restricted to one reviewed read-only business tool. Setting
`approval_policy = "never"` alone does not preapprove MCP tools and can reject a
call before it reaches the Gateway. Shell tools, plugins and other services are
disabled in the acceptance process. This test configuration is not a recommendation
to preapprove general-purpose or write-capable clients.

Configuration reference: [Codex MCP documentation](https://learn.chatgpt.com/docs/extend/mcp?surface=cli).

## Acceptance flow

1. `search_tools`: search within the granted server; verify unrelated tools are absent.
2. `get_tool_schema`: inspect the selected business tool's parameters.
3. `call_tool`: send one approved read with a stable idempotency key.
4. `read_result`: if the operation returns a result reference, follow its cursors;
   reading chunks must not execute the business action again.
5. Repeat `call_tool` with the same arguments and key; verify the same operation ID.
6. `get_operation`: confirm the durable outcome; compare against stored dispatch
   and terminal events, rather than trusting the model's final summary alone.
7. Revoke the test client and verify access denial. A fresh client connection check
   and an existing client's next request are different cases and should be recorded
   separately.

Large-result tests use a dedicated read-only tool definition with an explicitly
small inline limit and bounded artifact lifetime. They must not change a shared
business tool's response policy. Test tools/servers are disabled afterward and
artifacts expire according to their configured lifetime; operation audit records
remain available to administrators.

## Recorded cloud acceptance — 2026-10-08

The deployed runtime is `20261008-cloud.31`. Tests use a dedicated definition
of the already authorized Sport test read tool, with a 1 KiB inline limit and a
10-minute artifact lifetime. Keys grant only that definition. No shared tool
policy or upstream credential is changed.

Pi 1.0.0 completed discovery, schema lookup, invocation, four result chunks,
exact-key replay and operation lookup. The 3,991-byte artifact was reconstructed
and its SHA-256 verified independently of the model response. The durable ledger
contains one operation, one dispatch and one terminal execution observation.

Codex CLI 0.161.0 also completed the same flow: four chunks, independently verified
3,991-byte artifact hash, identical-key/arguments replay to the same operation,
and exactly one durable dispatch. Earlier attempts are retained in its evidence:
a local approval configuration rejection and an incomplete model-driven run.

Claude Code 2.1.268 connected to MCP, but its model OAuth session had expired and
refresh failed. Its business-call acceptance remains incomplete.

Evidence: [Pi and Claude record](evidence/mcp-native-clients-2026-10-08.json),
[Codex record](evidence/mcp-codex-client-2026-10-08.json).
Temporary clients were disabled and their subsequent HTTP identity requests
returned 401. This checks server-side key revocation, not an existing native
client's handling of a revoked session. Test definitions and servers were disabled;
raw transcripts and credentials are excluded from the repository.
