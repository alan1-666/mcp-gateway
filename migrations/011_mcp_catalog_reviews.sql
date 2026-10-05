-- Successful, complete catalog observations only. Executable tool definitions
-- remain independent; an observation can never publish or retire a tool.
CREATE TABLE mcp_catalog_reviews (
    id bigserial PRIMARY KEY,
    workspace_id text NOT NULL,
    server_id text NOT NULL,
    started_at timestamptz NOT NULL,
    checked_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    report jsonb NOT NULL,
    FOREIGN KEY (workspace_id, server_id) REFERENCES mcp_servers(workspace_id, id) ON DELETE CASCADE
);
CREATE INDEX mcp_catalog_reviews_history ON mcp_catalog_reviews(workspace_id, server_id, id DESC);
