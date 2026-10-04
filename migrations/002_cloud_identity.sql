CREATE TABLE gateway_users (
    id text PRIMARY KEY,
    workspace_id text NOT NULL,
    username text NOT NULL UNIQUE,
    password_hash bytea NOT NULL,
    role text NOT NULL CHECK (role IN ('admin','operator','approver','viewer')),
    disabled boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE TABLE gateway_invites (
    id text PRIMARY KEY,
    workspace_id text NOT NULL,
    token_hash bytea NOT NULL UNIQUE,
    role text NOT NULL CHECK (role IN ('admin','operator','approver','viewer')),
    created_by text,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    revoked_at timestamptz
);
CREATE TABLE gateway_sessions (
    token_hash bytea PRIMARY KEY,
    user_id text NOT NULL REFERENCES gateway_users(id),
    csrf_token text NOT NULL,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX gateway_sessions_user ON gateway_sessions(user_id);
CREATE TABLE gateway_api_keys (
    id text PRIMARY KEY,
    user_id text NOT NULL REFERENCES gateway_users(id),
    token_hash bytea NOT NULL UNIQUE,
    name text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz
);
CREATE INDEX gateway_api_keys_user ON gateway_api_keys(user_id);
CREATE TABLE gateway_identity_events (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    workspace_id text NOT NULL,
    actor_id text NOT NULL,
    action text NOT NULL,
    subject_id text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
