# Discovery evaluation

Measured 2026-10-06 on macOS 26.5 / arm64, Go 1.26.8, local PostgreSQL over loopback, one sequential caller. This is a repeatable synthetic regression evaluation, not a production capacity or semantic retrieval claim. [Recorded metrics and query labels](evidence/discovery-evaluation-2026-10-06.json).

## Reproduce

```sh
TEST_DATABASE_URL='postgres://USER@127.0.0.1:55432/mcp_gateway_test?sslmode=disable' \
  go test ./internal/store/postgres -run '^TestDiscoveryFixedCatalogEvaluation$' -count=1 -v
```

The test creates a unique workspace and deletes its tools afterward. It seeds exactly 1,000 tools: 992 recent generic descriptions plus eight older labelled targets. Four English and four Chinese queries run twenty times each. Rankings and labels are fixed; wall-clock latency is reported without a flaky timing threshold.

## Recorded result

| Measurement | Literal substring + recency | Ranked lexical discovery |
| --- | --- | --- |
| Labelled target in first 5 | 2/8 | 7/8 |
| Local serial p50 | 0.339 ms | 1.075 ms |
| Local serial p95 | 0.385 ms | 1.468 ms |

The baseline deliberately reproduces the old retrieval predicate and recency order using a lightweight SQL projection. It omits count and cursor construction. The ranked measurement includes the service, live count, summary conversion and cursor generation. These latency columns compare measured paths, not identical transport overhead. Neither includes HTTP/TLS, remote network latency, concurrent load or cold-start guarantees. Ranking has additional work and is not claimed to improve latency.

Full synthetic tool catalog with schemas: **782,766 JSON bytes**. Average ranked first page (limit 5, including metadata/cursor where applicable): **993 JSON bytes**. These are UTF-8 JSON sizes, not tokenizer counts. Fetching selected schemas or additional pages adds context. The full-catalog comparison is deliberately different from a bounded old substring page; it demonstrates on-demand discovery rather than ranking alone.

## Failure retained

`状态 任务` matches the intended job-status tool and many generic descriptions at the same `all_terms` tier. Recency ties put the target outside the first five results. The test preserves this miss (7/8 expected) to expose the limits of lexical matching. A service filter or more specific keywords can help; embeddings, translation and automatic Chinese segmentation are not implemented.

## Separate correctness coverage

- Stable rank/creation-time/ID traversal across exact, prefix, fragment and phrase matches; empty query retains recency order
- MCP service filter bound to pagination context; absent services return an empty page
- Permission filtering before count and limit, including an unauthorized exact match above authorized name fragments
- Live server disablement and client grant revocation across pages
- Case-insensitive Chinese/literal punctuation compatibility and all-term matching
- Malformed and cross-context cursors, rank bounds, legacy cursor version rejection and oversized identities
- Equivalent service-filter and ranked-summary contracts over REST, standard MCP SDK, and local/leased runner clients
- Summary allowlists reject schema, credential and connection metadata leakage

Ranked cursors use version 2. Clients with a version 1 cursor must restart discovery after an upgrade. Pages reflect live permissions; changing names/descriptions can reorder records while traversing. Discovery is not a frozen search snapshot.
