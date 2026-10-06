CREATE TABLE mcp_upstream_oauth (
 workspace_id text NOT NULL,
 server_id text NOT NULL,
 version bigint NOT NULL CHECK (version > 0),
 status text NOT NULL CHECK (status IN ('disconnected','pending','exchanging','connected','refreshing','reconnect_required')),
 configuration jsonb NOT NULL,
 client_secret bytea,
 token bytea,
 expires_at timestamptz,
 state_hash text,
 session_hash text,
 actor_id text,
 verifier bytea,
 deadline timestamptz,
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY (workspace_id,server_id),
 FOREIGN KEY (workspace_id,server_id) REFERENCES mcp_servers(workspace_id,id)
);
CREATE UNIQUE INDEX mcp_upstream_oauth_state_idx ON mcp_upstream_oauth(state_hash) WHERE state_hash IS NOT NULL;
