CREATE TABLE tools (
    workspace_id text NOT NULL,
    id text NOT NULL,
    name text NOT NULL,
    risk text NOT NULL CHECK (risk IN ('read','write')),
    status text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','published')),
    enabled boolean NOT NULL DEFAULT false,
    version integer NOT NULL DEFAULT 1 CHECK (version > 0),
    definition jsonb NOT NULL CHECK (jsonb_typeof(definition) = 'object'),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (workspace_id,id),
    UNIQUE (workspace_id,name),
    CHECK (NOT enabled OR status = 'published')
);

CREATE TABLE operations (
    workspace_id text NOT NULL,
    id text NOT NULL,
    tool_id text NOT NULL,
    tool_name text NOT NULL,
    tool_version integer NOT NULL CHECK (tool_version > 0),
    risk text NOT NULL CHECK (risk IN ('read','write')),
    actor_id text NOT NULL,
    arguments jsonb NOT NULL CHECK (jsonb_typeof(arguments) = 'object'),
    arguments_hash text NOT NULL,
    idempotency_key text NOT NULL,
    tool_snapshot jsonb NOT NULL CHECK (jsonb_typeof(tool_snapshot) = 'object'),
    state text NOT NULL CHECK (state IN ('WAITING_APPROVAL','READY','DISPATCHING','SUCCEEDED','FAILED','UNKNOWN','REJECTED')),
    result jsonb,
    error text NOT NULL DEFAULT '',
    approved_by text NOT NULL DEFAULT '',
    approval_expires_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (workspace_id,id),
    UNIQUE (workspace_id,idempotency_key),
    FOREIGN KEY (workspace_id,tool_id) REFERENCES tools (workspace_id,id),
    CHECK (approved_by = '' OR approved_by <> actor_id),
    CHECK ((approved_by = '') = (approval_expires_at IS NULL)),
    CHECK (risk <> 'write' OR state NOT IN ('READY','DISPATCHING','SUCCEEDED','FAILED','UNKNOWN') OR approved_by <> '')
);
CREATE INDEX operations_workspace_created_idx ON operations (workspace_id,created_at DESC,id DESC);
CREATE INDEX operations_workspace_actor_created_idx ON operations (workspace_id,actor_id,created_at DESC,id DESC);
CREATE INDEX operations_dispatching_idx ON operations (updated_at) WHERE state = 'DISPATCHING';

CREATE TABLE operation_events (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    workspace_id text NOT NULL,
    operation_id text NOT NULL,
    type text NOT NULL,
    actor_id text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    data jsonb NOT NULL DEFAULT '{}'::jsonb,
    FOREIGN KEY (workspace_id,operation_id) REFERENCES operations (workspace_id,id)
);
CREATE INDEX operation_events_cursor_idx ON operation_events (workspace_id,operation_id,id);

CREATE TABLE audit_events (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    workspace_id text NOT NULL,
    actor_id text NOT NULL,
    action text NOT NULL,
    resource_id text NOT NULL,
    data jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX audit_events_workspace_created_idx ON audit_events (workspace_id,created_at DESC,id DESC);
