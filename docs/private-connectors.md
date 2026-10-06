# Private MCP Connectors

A Connector runs beside private services and makes authenticated outbound HTTPS requests to Rillgate. It has no public listening port and no database access. The cloud binds an MCP server to a Connector ID and an operator-defined target name. It cannot supply a command, destination URL, image, environment variable or filesystem mount.

## Supported targets

| Target | Execution policy |
| --- | --- |
| HTTP MCP | Fixed local URL, exact origin, explicit private CIDRs, no redirects or inherited proxy, optional headers in a local private file; existing Streamable HTTP protocol and result limits apply |
| stdio MCP | Linux, a dedicated non-root user and local rootless Podman; preloaded image pinned by SHA-256 digest, one container per job, no network, read-only root filesystem, no mounts, no capabilities, no new privileges, bounded CPU/memory/processes and a 16 MiB temporary filesystem |

HTTP targets support macOS and Linux. stdio requires Linux; a container socket is never passed into the Connector. stdio programs must be usable with the declared restrictions. Automatic package downloads, arbitrary host commands, networking and host-directory mounts are not supported. Private-target credentials stay in local files; cloud upstream OAuth applies to direct HTTP servers.

## Register and run

1. Open **MCP Servers → Connectors** in the workspace and register a Connector. Save the one-time token in a file owned by the Connector user with mode `0600`. The cloud stores its hash. Treat it as an execution credential.
2. Build the matching release with `go build -o connector ./cmd/connector`. Prepare a private state directory and a local configuration. Secret/state paths must be absolute, have no symlink components, and be owned by this user; the state directory must use mode `0700`.
3. Run `./connector --config /absolute/path/connector.json`. The first heartbeat registers the target names, transport types and configuration fingerprints.
4. Add an MCP server using **Private connector**, choose an advertised target and a namespace. Run a connection check, discover tools, review their contracts and risk classifications, then publish. Client grants and write approvals apply exactly as they do to direct servers.

```json
{
  "gateway_url": "https://rillgate.cn",
  "token_file": "/home/connector/config/token",
  "state_dir": "/home/connector/state",
  "targets": [
    {
      "name": "internal-docs",
      "transport": "http",
      "url": "http://10.20.0.8:8080/mcp",
      "allowed_cidrs": ["10.20.0.8/32"],
      "headers_file": "/home/connector/config/docs-headers.json"
    }
  ]
}
```

The optional headers file is a JSON object, for example `{"Authorization":"Bearer <private-service-token>"}`, with mode `0600`. Omit it for services without authentication. Credentials are resolved for each job. The Connector token is used only for its configured HTTPS gateway origin and is never forwarded to an MCP target.

For stdio, replace a target with:

```json
{
  "name": "offline-docs",
  "transport": "stdio",
  "image": "registry.example/team/docs-mcp@sha256:<64-lowercase-hex-digest>",
  "args": []
}
```

Preload the image into that user's rootless Podman store. The runtime uses `--pull=never`, checks that Podman is local and rootless, and applies resource limits. The host must permit unprivileged user namespaces and provide delegated cgroup support for these limits. An unsupported host fails closed. Do not run stdio as root or remove limits to make a failing host pass.

Target fingerprints cover the local destination, allowed networks, image/arguments and credential-file path. They do not contain header values. Changing a target's configuration requires registering a new Connector and reviewing a new server binding. Rotating the contents of its existing local credential file does not change the binding. The online indicator reflects a heartbeat in the last 30 seconds; it is not a health check of the target.

## Durable execution and failure behavior

```text
Gateway operation / catalog request
  → persisted Connector job
  → claim once
  → local journal fsync
  → separate cloud execution authorization
  → fixed local target
  → bounded result report
  → cloud schema/result validation and existing response policy
```

Jobs follow `queued → claimed → started → completed`, or expire. Claiming and execution authorization are separate commits. Start rechecks the deadline, Connector/server state, approved operation snapshot, requesting client's current grants and write approval. A repeated start is rejected. Identical result reports can be retried without executing the tool again.

Claimed work is never requeued after a lost response, restart or network failure. A journal entry is persisted before requesting start; a duplicate job ID cannot execute again. A write with an uncertain outcome after start remains `UNKNOWN` and requires reconciliation through the existing operation workflow. This deliberately favors avoiding duplicate side effects over automatically completing every interrupted task.

Revocation prevents subsequent authorization and result acceptance, and the runtime stops when its heartbeat reports revocation. An authorization already delivered to a Connector cannot recall a downstream action. Cancellation and container termination are best effort; neither undoes a business operation. A lost completion can therefore require human reconciliation even if the target completed successfully.

The gateway still validates imported schema hashes, supported result content and output schemas. Response projection/size rules are applied before the final operation result is stored. No automatic business-call retry or upstream tool replay is introduced.

## Limits and retention

- 100 registered Connectors and 100 active jobs per workspace; 32 targets per Connector
- One execution at a time per runtime process; idle polling every 2 seconds and heartbeats every 5 seconds
- Server deadlines between 100 ms and 120 seconds; leave enough time for polling, container startup and reporting
- 1 MiB MCP message/result limit, 1,000 tools and 100 catalog pages; Connector result envelopes at most 4 MiB
- Local journal: 10,000 retained attempts, expired entries pruned after 24 hours; a corrupt/full journal blocks execution
- Cloud cleanup expires abandoned jobs, deletes completed discovery payloads after 24 hours and erases execution payloads/results after 24 hours; minimal operation tombstones remain to prevent replay
- Configuration/token replacement is performed by registering a new Connector and revoking the previous one; there is no token retrieval or re-enable action

Use a service manager to restart the Connector with its persistent state directory. Never share that directory between processes or delete its journal to retry a write. Local journal entries include container names for startup cleanup. Unconfirmed cleanup must be investigated before further stdio execution.

## Protocol

Administration uses the authenticated workspace API. Machine requests use a separate bearer credential and reject browser cookies/Origin headers:

| Endpoint | Meaning |
| --- | --- |
| `GET /api/v1/connectors` | List workspace registrations, target metadata and heartbeat timestamps |
| `POST /api/v1/connectors` | Register and return a one-time token |
| `POST /api/v1/connectors/{id}/revoke` | Permanently revoke the registration |
| `POST /connector/v1/poll` | Freeze/check targets and heartbeat; optionally claim one job |
| `POST /connector/v1/jobs/{id}/start` | Authorize one attempt against current policy |
| `POST /connector/v1/jobs/{id}/result` | Report its bounded outcome |

This implementation uses bounded HTTPS polling with durable PostgreSQL jobs. Bidirectional gRPC streams remain a possible transport optimization, not a dependency or an implemented claim. See [OpenAPI](../api/openapi.yaml) for exact schemas and [verification](verification.md) for tested acceptance boundaries.
