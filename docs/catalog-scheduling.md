# Scheduled MCP catalog checks

Administrators can enable checks in **MCP Servers → Settings → Catalog checks**. Every server starts with scheduling off. The interval is 5 minutes to 24 hours; enabling or changing an enabled schedule makes it due immediately. An unchanged save does not reset its next check. Reload the schedule to see the latest outcome, and use **Activity → Reload catalog history** to inspect new comparisons.

## Execution and persistence

- The existing Go worker runs two catalog consumers alongside operation/agent recovery. Idle consumers poll every 15 seconds. Each worker replica adds two consumers; database leases coordinate them.
- `mcp_catalog_schedules` stores configuration revision, next due time, lease, timestamps and safe failure codes. PostgreSQL time controls scheduling and lease expiry.
- Claiming uses `FOR UPDATE SKIP LOCKED`. A claim has a unique token and a 180-second lease. Network I/O runs outside the transaction and is limited by the server timeout, at most 120 seconds. Result persistence has a separate 10-second deadline.
- Successful comparison, retained history, audit and next due time commit together. A late result is rejected after lease expiry, another worker's takeover, schedule edit/pause or server configuration change. Configuration writes require the last read revision; an identical configuration save is idempotent.
- Process interruption can repeat a read-only discovery after lease expiry. This is not an exactly-once network guarantee. Pausing cannot unsend an HTTP request already in flight, but fences its result.
- Disabling server access excludes its schedule from claims. Re-enabling allows overdue work on the next poll. The interval setting is retained.

## Outcomes and access

Successful checks compare against registered definitions, including drafts and retired tools. Each retained observation is labeled `manual` or `scheduled`; historical pre-scheduler rows are reported as manual. The last 50 successful observations are retained per server. No schedule creates a candidate, publishes, retires or invokes a business tool.

Failed or incomplete discovery preserves previous successful history. Safe codes (`discovery_failed`, `timeout`, `server_changed`, `comparison_failed`) and consecutive failures appear in settings and audit; raw upstream errors are not stored. Retries wait the configured interval, then twice that interval, then four times, capped at 24 hours. Success resets the backoff. Editing configuration does not erase the previous outcome.

Management routes are human-admin-only and workspace scoped. The worker uses an internal `system:catalog-scheduler` audit identity without an administrator role or reusable token. It resolves credentials for the claimed workspace and uses the same origin/CIDR/DNS rules and MCP adapter as manual discovery. Polling bypasses business-call admission because it never executes tools; network work is bounded by the consumer count and per-check limits.

## API

- `GET /api/v1/mcp/servers/{id}/catalog-schedule` returns saved settings and the latest background status. An unconfigured server returns revision `0`, disabled, with a 3600-second interval.
- `PUT /api/v1/mcp/servers/{id}/catalog-schedule` accepts `enabled`, `interval_seconds` and `expected_revision`. A stale revision returns `409`; missing fields, unsupported intervals and unknown fields return `400`.
- Existing catalog history reads include the observation source. The OpenAPI contract documents response fields and permissions.

## Operations and rollout

Migration `012_mcp_catalog_schedules.sql` is additive. The cloud worker now needs the existing egress allowlists and vault configuration (already included in the shared backend deployment settings); its memory limit is 256 MiB. No existing server is scheduled by the migration.

Running processes must be restarted with the new worker image. A stopped or unhealthy worker leaves overdue checks pending; this status is not an upstream health verdict. Multi-host HA, per-workspace polling fairness, push notifications, upstream change subscriptions and scheduler-specific alerting are outside this feature.

If the application is rolled back to pre-scheduler code, saved schedules become dormant and existing tools continue to work. Retain migration 012 and explicitly declare its checksum compatible with the exact rollback target. Re-deploying scheduler code may immediately process overdue enabled schedules. Application rollback does not reverse migrations or automatically remove saved configuration.
