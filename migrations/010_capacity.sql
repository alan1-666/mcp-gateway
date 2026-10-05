CREATE TABLE capacity_limits (
    workspace_id text NOT NULL,
    scope text NOT NULL CHECK (scope IN ('workspace','client','upstream')),
    scope_id text NOT NULL,
    max_concurrent integer NOT NULL CHECK (max_concurrent BETWEEN 1 AND 256),
    requests_per_minute integer NOT NULL CHECK (requests_per_minute BETWEEN 1 AND 60000),
    version integer NOT NULL DEFAULT 1,
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY(workspace_id,scope,scope_id)
);
CREATE TABLE capacity_leases (
    workspace_id text NOT NULL,
    operation_id text NOT NULL,
    token text NOT NULL,
    client_id text NOT NULL,
    upstream_id text NOT NULL,
    expires_at timestamptz NOT NULL,
    PRIMARY KEY(workspace_id,operation_id)
);
CREATE INDEX capacity_leases_expiry ON capacity_leases(expires_at);
CREATE TABLE capacity_windows (
    workspace_id text NOT NULL,
    scope text NOT NULL,
    scope_id text NOT NULL,
    minute timestamptz NOT NULL,
    requests integer NOT NULL,
    PRIMARY KEY(workspace_id,scope,scope_id,minute)
);
CREATE INDEX capacity_windows_expiry ON capacity_windows(minute);
-- Only finite, server-selected dimensions. No tool/client/request/user labels.
CREATE TABLE capacity_call_metrics (
    workspace_id text NOT NULL,
    transport text NOT NULL CHECK (transport IN ('http','mcp')),
    state text NOT NULL CHECK (state IN ('SUCCEEDED','FAILED','UNKNOWN')),
    count bigint NOT NULL DEFAULT 0,
    duration_ms_total bigint NOT NULL DEFAULT 0,
    PRIMARY KEY(workspace_id,transport,state)
);
CREATE TABLE capacity_rejection_metrics (
    workspace_id text NOT NULL,
    scope text NOT NULL CHECK (scope IN ('workspace','client','upstream')),
    reason text NOT NULL CHECK (reason IN ('concurrency','rate')),
    count bigint NOT NULL DEFAULT 0,
    PRIMARY KEY(workspace_id,scope,reason)
);
CREATE INDEX operations_retention ON operations(updated_at) WHERE state IN ('SUCCEEDED','FAILED','REJECTED');
CREATE INDEX agent_runs_retention ON agent_runs(updated_at) WHERE state IN ('SUCCEEDED','FAILED','CANCELLED');
CREATE INDEX audit_events_retention ON audit_events(created_at);
