# MCP execution observations

Rillgate records bounded, gateway-owned execution metadata for remote HTTP MCP
operations and configuration rejections. It answers which phase took time, where an execution stopped and
whether the protocol dispatch fence allowed a business-call attempt.

## Durable record

`OPERATION_SUCCEEDED`, `OPERATION_FAILED` and `OPERATION_UNKNOWN` events may contain
`data.mcp_execution`. The event and terminal operation state commit in the same
PostgreSQL transaction. Reusing an idempotency key or completing an operation twice
cannot append another observation. Existing operation visibility and retention
apply. Failed final persistence leaves a dispatching operation; recovery marks it
UNKNOWN without inventing a completed observation.

The record contains six integer millisecond durations: configuration validation,
session acquisition, catalog verification, tool round trip, result validation and
response projection. Each phase is disjoint; millisecond rounding can make their
sum smaller than the total. A permitted expired-session reconnect during catalog
inspection belongs to catalog timing. Admission, human approval, persistence and
session teardown are excluded. Artifact storage failures retain the observation
and classify workspace quota exhaustion as `artifact_quota`.

`code` is a fixed gateway enum. Network errors are classified using context and
HTTP status, never string matching an upstream message. Schema drift, catalog
changes, tool errors, unsupported interactions, invalid responses and projection
failures have separate codes. `phase` identifies the last execution phase.
`call_attempted` means the request passed the gateway protocol dispatch fence;
it is not proof that the upstream received or executed it. The observed HTTP
status belongs to tools/call when attempted, otherwise the last connection/catalog
response; no response means the field is omitted. Existing write failures still
remain UNKNOWN whenever the result is uncertain; telemetry never authorizes replay.

There are no credentials, URLs, arguments, tool response bodies, free-text errors
or user-chosen labels in the metadata. Neither HTTP execution requests nor Connector
results may supply it. Delegated Connector executions are not instrumented as remote HTTP
phases; a configuration rejection before resolving the server can still be observed, and old operations have no synthetic observations.

## Administrator view

`GET /api/v1/mcp/servers/{id}/execution-metrics` accepts no query parameters.
Administrator identities are scoped to their workspace; machine clients are
excluded. The endpoint reads at most the newest 1,000 workspace operations created
within 24 hours, then selects committed observations belonging to the server in
its immutable operation snapshot. The existing workspace creation-time index and
operation event cursor index bound retrieval. `operations_scanned`, `sample_limit`
and `truncated` describe sampling before server filtering. Quiet servers may have
no sample when other workspace traffic consumes the limit. Pending and legacy
operations consume the sampling budget but are not counted as observed calls.

Metrics include operation states, fixed error counts, attempted count, nearest-rank
P50/P95 total duration and per-phase integer means. The server diagnostics panel
supports manual refresh and English/Chinese labels. No periodic business calls,
background probes or new subscriptions are started by opening it.

Observed health describes the most recent included execution:

| Value | Meaning |
| --- | --- |
| healthy | Most recent sampled call succeeded |
| degraded | Transport failure, rate limit, timeout or unconfirmed call |
| attention | Other failure requiring inspection, including auth and contract issues |
| stale | Latest sample is older than five minutes or predates server configuration |
| disabled | Current server is disabled |
| unobserved | No observations in the bounded sample |

This is historical execution evidence, not an uptime guarantee, a complete time
series, active health probe or SLA. Connection checks remain a separate capability.
