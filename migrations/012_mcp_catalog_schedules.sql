-- Opt-in metadata polling. Executable tool versions remain separately reviewed.
CREATE TABLE mcp_catalog_schedules (
 workspace_id text NOT NULL,
 server_id text NOT NULL,
 enabled boolean NOT NULL DEFAULT false,
 interval_seconds integer NOT NULL CHECK (interval_seconds BETWEEN 300 AND 86400),
 revision integer NOT NULL DEFAULT 1 CHECK (revision > 0),
 next_check_at timestamptz,
 lease_id text NOT NULL DEFAULT '',
 lease_until timestamptz,
 last_started_at timestamptz,
 last_finished_at timestamptz,
 last_success_at timestamptz,
 consecutive_failures integer NOT NULL DEFAULT 0 CHECK (consecutive_failures BETWEEN 0 AND 1000),
 last_error_code text NOT NULL DEFAULT '',
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY (workspace_id, server_id),
 FOREIGN KEY (workspace_id, server_id) REFERENCES mcp_servers(workspace_id, id) ON DELETE CASCADE,
 CHECK ((lease_id = '') = (lease_until IS NULL)),
 CHECK (enabled = (next_check_at IS NOT NULL))
);
CREATE INDEX mcp_catalog_schedules_due ON mcp_catalog_schedules(next_check_at) WHERE enabled;
