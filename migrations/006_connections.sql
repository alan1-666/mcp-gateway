CREATE TABLE gateway_credentials (
 workspace_id text NOT NULL,
 ref text NOT NULL,
 origin text NOT NULL,
 version integer NOT NULL DEFAULT 1 CHECK (version > 0),
 enabled boolean NOT NULL DEFAULT true,
 header_names text[] NOT NULL,
 encrypted_headers bytea NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY (workspace_id,ref)
);

CREATE TABLE mcp_server_checks (
 id bigserial PRIMARY KEY,
 workspace_id text NOT NULL,
 server_id text NOT NULL,
 checked_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 report jsonb NOT NULL,
 FOREIGN KEY (workspace_id,server_id) REFERENCES mcp_servers(workspace_id,id) ON DELETE CASCADE
);
CREATE INDEX mcp_server_checks_history_idx ON mcp_server_checks(workspace_id,server_id,id DESC);
