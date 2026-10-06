# HTTP reliability profile

The gateway automatically retries only reviewed **read-only HTTP GET tools**. Remote MCP and Connector calls do not inherit this retry behavior. Writes, uncertain write outcomes and non-GET requests are never automatically replayed by this profile.

## Retry boundary

- At most two HTTP attempts share one tool timeout and the caller's deadline, including credential resolution and backoff
- Retryable conditions: HTTP 502/503/504, EOF before a response, connection reset/refused/broken pipe, and temporary or timed-out network errors when the operation deadline has not expired
- No retries for authentication failures, redirects, 429/other status codes, blocked egress, invalid JSON, schema errors, oversized/incomplete response bodies, or canceled/expired contexts
- Default backoff is 100 ms; valid `Retry-After` seconds or HTTP-date values replace it. A hint over one second, malformed hint, or insufficient remaining time suppresses retry. The gateway never shortens an upstream hint to squeeze in another call
- Each attempt resolves current workspace/origin-bound credentials. Removed headers do not survive into the next attempt. The existing transport disables connection reuse and rechecks DNS addresses against the egress policy for every connection

A service owner must classify GET tools correctly. HTTP method alone does not prove a business action has no side effects. GET tools labeled `write` do not retry.

## Circuit breaker

Three consecutive transient logical-call failures open the circuit for 15 seconds. The two attempts belonging to one logical call count as one failure. The next eligible call after cooldown is the only half-open probe; concurrent callers remain blocked. Successful/nontransient communication closes the circuit. Cancellation releases a probe without treating it as recovery. Generation checks stop earlier in-flight completions from closing a circuit opened by newer failures.

Circuit state is in memory per adapter process, keyed by workspace, origin and credential reference. It is not a distributed availability guarantee. Restarting a process resets its circuit state. The map tracks at most 1024 keys; stale entries older than five minutes can be evicted. When tracking is saturated, unrelated destinations retain bounded retries without circuit tracking rather than growing memory or being denied globally.

Operation IDs remain stable across retry attempts and are sent as the existing `Idempotency-Key` header. Operators can correlate that ID with gateway operation/event records and upstream logs that record the header. This is correlation support, not an OpenTelemetry implementation. No credential values or upstream response bodies are added to retry logs.

## Reproducible verification

```sh
go test -race ./internal/adapters/httpadapter
go test -count=1 -v ./internal/adapters/httpadapter -run '^TestHTTPReliabilityEvaluation$'
```

Unit and isolated HTTP tests cover concurrent half-open admission, stale completions, recovery, cancellation, keyspace saturation/eviction, workspace isolation, exact attempt counts, credential changes/revocation, Retry-After/deadline boundaries, schema/auth failures, and writes remaining UNKNOWN after an unconfirmed response. New `reliability.go` statements reached 100% coverage in the recorded local race run; this is not a whole-package coverage claim or a Go branch-coverage measurement.

### Fixed local evaluation, 2026-10-06

Environment: macOS arm64, 10 logical CPUs, Go 1.26.8. Each scenario performs 20 **sequential** operations against an isolated loopback HTTP fixture with a 2 ms server delay, `Retry-After: 0`, and a one-second overall tool timeout. Percentiles use observed adapter call durations; they include connection setup and retry overhead. No LLM, internet, database, gateway HTTP frontend or cloud server is involved.

| Scenario | Upstream attempts | Success / failed / unknown | Circuit rejections | p50 ms | p95 ms |
| --- | ---: | --- | ---: | ---: | ---: |
| Healthy GET | 20 | 20 / 0 / 0 | 0 | 2.726 | 2.854 |
| First attempt 503, then success | 40 | 20 / 0 / 0 | 0 | 5.392 | 5.480 |
| Persistent 503 GET | 6 | 0 / 20 / 0 | 17 | 0.001 | 5.383 |
| Unauthorized GET | 20 | 0 / 20 / 0 | 0 | 2.706 | 2.792 |
| Persistent 503 write | 20 | 0 / 0 / 20 | 0 | 2.717 | 2.770 |

The near-zero median for persistent failures measures local rejection, not fast successful requests. The unavailable and unauthorized scenarios intentionally have a 100% non-success rate. Twenty sequential samples verify behavior under a fixed workload; they do not establish peak throughput, production latency targets, or a statistically representative failure rate.

Machine-readable observations: [HTTP evaluation](evidence/http-reliability-2026-10-06.json). Existing database-backed operation/capacity records continue to provide deployed execution counts and latency observations; this change does not introduce a new histogram or distributed circuit service.
