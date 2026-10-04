CREATE TABLE agent_runs (
    workspace_id text NOT NULL,
    id text NOT NULL,
    actor_id text NOT NULL REFERENCES gateway_users(id),
    prompt text NOT NULL CHECK (char_length(prompt) BETWEEN 1 AND 8000),
    idempotency_key text NOT NULL,
    state text NOT NULL CHECK (state IN ('QUEUED','RUNNING','WAITING_APPROVAL','WAITING_CREDENTIALS','NEEDS_REVIEW','SUCCEEDED','FAILED','CANCELLED')),
    attempt integer NOT NULL DEFAULT 0 CHECK (attempt >= 0),
    worker_id text NOT NULL DEFAULT '',
    lease_hash bytea,
    lease_expires_at timestamptz,
    attempt_deadline timestamptz,
    output text NOT NULL DEFAULT '' CHECK (octet_length(output) <= 65536),
    error_code text NOT NULL DEFAULT '',
    waiting_operation_id text NOT NULL DEFAULT '',
    tool_calls integer NOT NULL DEFAULT 0 CHECK (tool_calls BETWEEN 0 AND 40),
    event_count integer NOT NULL DEFAULT 0,
    event_bytes integer NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (workspace_id,id),
    UNIQUE (workspace_id,actor_id,idempotency_key),
    CHECK ((state = 'RUNNING') = (lease_hash IS NOT NULL AND lease_expires_at IS NOT NULL AND attempt_deadline IS NOT NULL))
);
CREATE INDEX agent_runs_queue ON agent_runs(workspace_id,created_at,id) WHERE state = 'QUEUED';
CREATE INDEX agent_runs_actor ON agent_runs(workspace_id,actor_id,created_at DESC);
CREATE UNIQUE INDEX agent_runs_active_actor ON agent_runs(workspace_id,actor_id) WHERE state = 'RUNNING';
CREATE UNIQUE INDEX agent_runs_active_worker ON agent_runs(workspace_id,worker_id) WHERE state = 'RUNNING';

CREATE TABLE agent_run_events (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    workspace_id text NOT NULL,
    run_id text NOT NULL,
    event_key text,
    type text NOT NULL,
    data jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(data) = 'object'),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    FOREIGN KEY (workspace_id,run_id) REFERENCES agent_runs(workspace_id,id),
    UNIQUE (workspace_id,run_id,event_key)
);
CREATE INDEX agent_run_events_cursor ON agent_run_events(workspace_id,run_id,id);

CREATE TABLE agent_run_operations (
    workspace_id text NOT NULL,
    run_id text NOT NULL,
    operation_id text NOT NULL,
    PRIMARY KEY(workspace_id,run_id,operation_id),
    UNIQUE(workspace_id,operation_id),
    FOREIGN KEY(workspace_id,run_id) REFERENCES agent_runs(workspace_id,id),
    FOREIGN KEY(workspace_id,operation_id) REFERENCES operations(workspace_id,id)
);
CREATE TABLE agent_run_intents (
    workspace_id text NOT NULL,
    run_id text NOT NULL,
    idempotency_key text NOT NULL,
    intent_hash text NOT NULL,
    operation_id text NOT NULL,
    PRIMARY KEY(workspace_id,run_id,idempotency_key),
    FOREIGN KEY(workspace_id,run_id,operation_id) REFERENCES agent_run_operations(workspace_id,run_id,operation_id)
);
CREATE TABLE agent_runner_status (
    workspace_id text PRIMARY KEY,
    worker_id text NOT NULL,
    model_ready boolean NOT NULL,
    provider text NOT NULL,
    model_id text NOT NULL,
    error_code text NOT NULL,
    last_seen_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
