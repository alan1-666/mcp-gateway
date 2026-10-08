# MCP direct and gateway evaluation

This repeatable workload measures a standard SDK client calling a loopback MCP
server directly and through the complete Gateway execution path. It uses a real
PostgreSQL database in an isolated, automatically removed schema, a managed client
key and explicit tool grant, capacity admission, durable operations, live catalog
verification and response processing. No public upstream or model is called.

## Fixed workload

`TestMCPLoadEvaluation` in `tests/integration/mcp_load_evaluation_test.go` runs
16 cases: two routes × two catalog sizes (1 and 100 tools) × four scenarios.
Each case contains 24 measured requests with identical empty arguments and a
2,048-character ASCII text payload. The fixture waits 2 ms before returning;
scheduler and network overhead can increase that delay.

| Scenario | Client concurrency | Session behavior | Injected failure |
| --- | --- | --- | --- |
| Cold | 1 | New SDK session and tool discovery for each request; Gateway upstream retention disabled | None |
| Warm sequential | 1 | One client session; Gateway upstream retention enabled | None |
| Warm concurrent | 4 | One session per worker; Gateway retains idle upstream sessions, with disposable connections when busy | None |
| Warm tool errors | 1 | Reused client; normal Gateway session disposal on error | Every sixth upstream call returns MCP `isError` |

Cold means a fresh **MCP session**, not a guaranteed fresh TCP/TLS connection or
cold database/cache. HTTP keep-alive follows normal client behavior. Warm cases
perform one successful warm-up per worker before measuring, sequentially; this
does not pre-populate four independent upstream sessions. Counters exclude setup,
import, publication, and warm-up. Direct clients do not incur Gateway discovery,
authorization, persistence or capacity work; those are the added responsibilities
whose aggregate cost this comparison captures.

## Measurement boundaries

- Per-request latency begins before connection and discovery in cold cases, and
  immediately before the tool call in warm cases. It ends after decoding and
  checking the returned result, before closing a cold client session.
- Wall duration covers the worker loop, including cold-session close. Completed
  throughput counts all attempts; successful throughput excludes failures.
- P50/P95 use nearest-rank values over all 24 samples, including injected errors.
  Individual sample latencies and outcomes are retained for separate analysis.
- `response_json_bytes` measures the reserialized SDK response, including the
  Gateway operation envelope. It is neither network bytes nor a tokenizer count.
- Upstream initialize, catalog and business-call counters are measured independently
  of client outcomes. The test requires exactly one business call per request and
  one durable observation per Gateway operation, including tool errors.
- Gateway catalog mean uses the existing integer-millisecond phase observation.
  It excludes authentication, admission, database finalization and client transport.
  Do not subtract it from client percentiles to derive an unmeasured phase.
- Gateway rejections, unknown outcomes, malformed results and transport failures
  are retained in sample outcomes and fail this workload's expected-result checks.
  This is not an overload/rejection benchmark: concurrency four is below the
  default client/upstream concurrency cap of eight.

Tool errors currently retire the retained upstream session. The subsequent request
reconnects; this is reported as initialize traffic, not mistaken for a replay of
the failed action. Other fault classes are covered by existing protocol and
reliability tests, not represented by the timing samples in this workload.

## Reproduce

Set `TEST_DATABASE_URL` to a dedicated loopback PostgreSQL test database. The
runner rejects remote database hosts, creates a unique schema, applies migrations
there and drops that schema afterward. It does not print the connection string or
client key. Use an existing output directory for the optional evidence file.

```sh
RUN_MCP_LOAD_EVALUATION=1 \
MCP_LOAD_EVIDENCE=/absolute/path/mcp-load-evaluation.json \
go test -count=1 -v ./tests/integration -run '^TestMCPLoadEvaluation$'
```

Use `-race` for concurrency correctness. For timing evidence, run the command
without race/coverage instrumentation and without other build/test workloads.
The CI workflow enables this test alongside database integration tests and uploads
its JSON report. CI timings include race/coverage and shared-runner interference;
CI asserts behavior, with no timing threshold or comparison to local latency.
An evidence file is written only after all evaluation cases pass.

## Interpretation and limits

This is a closed-loop, short, single-host workload with fixed ordering (direct
then Gateway), small samples, one measured iteration and no TLS. It establishes
reproducible behavior and an initial overhead measurement. It does not establish
production throughput, saturation capacity, long-run tail latency, resource usage,
real-client compatibility or model-task quality. Repeat on declared deployment
hardware and larger workloads before setting performance objectives.

Only catalog size changes between the two catalog groups; the business payload
and tool are the same. Use catalog-phase measurements to decide whether directory
optimization is material. Any future caching must preserve identity isolation,
live permissions, version binding and catalog invalidation.

## Local evidence — 2026-10-08

The [measured report](evidence/mcp-load-evaluation-2026-10-08.json) contains all
384 samples: 368 successes and 16 expected injected tool errors, with zero
unexpected failures, rejections or UNKNOWN outcomes. Every measured request
caused exactly one upstream business call. All 192 Gateway calls have one durable
observation with the expected error classification. Warm-up adds 24 unmeasured
calls. All temporary evaluation schemas were removed.

Timing was measured separately from the race run on macOS arm64, Go 1.26.8,
10 logical CPUs/GOMAXPROCS, PostgreSQL 16.9, a 12-connection database pool and
loopback HTTP. No other project build or test was running during this measurement;
the host is not dedicated benchmark hardware. Fixture SHA-256 and base commit are
recorded in report provenance; the fixture was an uncommitted addition to that base.

| Catalog / scenario | Direct P50 / P95 ms | Gateway P50 / P95 ms | Gateway catalog mean ms |
| --- | --- | --- | --- |
| 1 / cold serial | 3.991 / 4.899 | 12.938 / 14.428 | <1 (rounded to 0) |
| 1 / warm serial | 2.488 / 2.551 | 9.776 / 11.058 | <1 (rounded to 0) |
| 1 / warm concurrency 4 | 2.423 / 2.555 | 7.593 / 28.604 | <1 (rounded to 0) |
| 100 / cold serial | 3.551 / 3.836 | 16.000 / 17.811 | 2.375 |
| 100 / warm serial | 2.507 / 2.671 | 13.771 / 17.236 | 2.125 |
| 100 / warm concurrency 4 | 2.471 / 2.686 | 13.633 / 17.862 | 3.500 |

Healthy sequential reuse had zero measured upstream initializations. Each Gateway
tool-error scenario had four failures and four subsequent initializations; none
of the failed calls were replayed. Four-worker cases required additional upstream
sessions (two and three respectively) from the pool's exclusive leases.

The one-tool concurrent P95 outlier is retained rather than excluded. These short
samples do not prove scalability or a significant difference between scenarios.
Catalog work is measurable with 100 tools, but the evidence does not establish it
as the dominant cost or justify weakening per-call contract checks.

Validation: the isolated workload passed with `-race`; `make check` and
`RUN_MCP_LOAD_EVALUATION=1 RUN_CLOUD_WORKER_INTEGRATION=1 make test` passed with
the dedicated local test database. The new CI workload/artifact wiring has been
added to source; remote CI acceptance and cloud deployment have not occurred.

## Server validation — 2026-10-08

Two additional runs distinguish deployed-product behavior from a controlled
workload executed on the same server. The release manifest and running images
were verified as `20261008-cloud.31`, source
`a17b5d931aa302a46b4b16987c904de1782a9732`. No application release was performed.

### Deployed HTTPS endpoint

An official Go SDK client ran on the server and connected to
`https://rillgate.cn/mcp` through public DNS and verified TLS. A temporary client
was granted only the existing published, read-only Sport test `list_matches`
tool. Its reviewed policy already allowed direct reads. Twelve distinct calls
used the same bounded query (one page, at most three matches), paced one second
apart: six fresh SDK sessions and six calls on a reused session. Cold timings
include handshake and Gateway tool-list discovery; warm timings cover the call.
The upstream Gateway session pool stayed enabled in both groups.

All twelve calls succeeded. One same-key replay returned the original operation;
each operation had exactly one dispatch and one successful terminal observation.
The temporary client was disabled afterward and its key returned HTTP 401.
Results are retained only as byte counts and hashes, with no returned business
records or credentials in the [HTTPS evidence](evidence/mcp-cloud-measurement-2026-10-08.json).

| Measurement | Result |
| --- | --- |
| Fresh client median / maximum, 6 samples | 221.781 / 675.290 ms |
| Reused client median / maximum, 6 samples | 200.307 / 219.307 ms |
| Internal catalog phase, mean across 12 calls | 78.42 ms |
| Internal upstream tool-call phase, mean across 12 calls | 109.33 ms |

This validates the deployed path under a small paced workload. Six samples per
mode cannot establish a stable tail percentile. The client is on the server,
so these measurements do not include a user's geographic network latency. The
included administrator metrics response contains historical workspace samples;
only the twelve linked operation observations belong to this run.

### Isolated fixed workload on the server

The identical 16-case fixture was cross-compiled for Linux amd64 and executed on
the deployment host with its own temporary PostgreSQL container. The container
had no external network, one CPU quota, 512 MiB memory and 192 MiB database tmpfs.
Application code, module versions and migrations match the deployed source; the
fixture runs a separate Gateway instance rather than sending load to production.
PostgreSQL 16.14 and the test process share this container quota. That differs
from production's separate containers and persistent database storage.

All 384 requests matched expected outcomes (368 successful, 16 injected tool
errors). The 192 Gateway requests each had one terminal observation, with no
business-call replay. See the [server fixture evidence](evidence/mcp-server-load-evaluation-2026-10-08.json).

| 100-tool catalog scenario | Direct P50 / P95 ms | Gateway P50 / P95 ms |
| --- | --- | --- |
| Cold serial | 5.153 / 7.171 | 23.590 / 41.629 |
| Warm serial | 2.648 / 23.837 | 14.941 / 25.050 |
| Warm concurrency 4 | 2.685 / 4.024 | 69.191 / 97.302 |

Outliers remain in the report. Shared CPU quota, tmpfs storage, fixed ordering and
24 samples per case limit interpretation; the concurrent result is not a
production capacity estimate. Schema cleanup returned zero temporary schemas,
the container and uploaded binaries were removed, and the deployed API, Gateway,
console and database remained healthy in the final check. No production data,
network policy or service configuration was changed; normal call audit records
and the disabled temporary client remain for traceability.

The real upstream's catalog phase is materially larger than the loopback fixture's.
This supports investigating catalog round trips and invalidation semantics with
real services before deciding on caching. It does not justify relaxing the
existing schema-drift or live-permission checks.
