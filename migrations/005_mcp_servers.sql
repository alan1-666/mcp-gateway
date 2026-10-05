CREATE TABLE mcp_servers (
 workspace_id text NOT NULL,
 id text NOT NULL,
 name text NOT NULL,
 namespace text NOT NULL,
 url text NOT NULL,
 credential_ref text NOT NULL DEFAULT '',
 timeout_ms integer NOT NULL CHECK (timeout_ms BETWEEN 100 AND 120000),
 enabled boolean NOT NULL DEFAULT true,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY (workspace_id,id),
 UNIQUE (workspace_id,namespace)
);
CREATE INDEX mcp_servers_workspace_created_idx ON mcp_servers(workspace_id,created_at DESC,id DESC);
CREATE UNIQUE INDEX tools_mcp_upstream_unique ON tools(workspace_id,(definition->'mcp'->>'server_id'),(definition->'mcp'->>'tool_name')) WHERE definition->'mcp' IS NOT NULL AND definition->'mcp' <> 'null'::jsonb;
