CREATE TABLE gateway_clients (
    id text PRIMARY KEY,
    workspace_id text NOT NULL,
    name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 120),
    enabled boolean NOT NULL DEFAULT true,
    version integer NOT NULL DEFAULT 1 CHECK (version > 0),
    scopes text[] NOT NULL DEFAULT '{}' CHECK (scopes <@ ARRAY['tools:read','tools:invoke']::text[]),
    key_id text NOT NULL UNIQUE,
    token_hash bytea NOT NULL UNIQUE CHECK (octet_length(token_hash)=32),
    key_created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    key_expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (workspace_id,id),
    UNIQUE (workspace_id,name)
);
CREATE INDEX gateway_clients_workspace_created_idx ON gateway_clients(workspace_id,created_at DESC,id DESC);

CREATE TABLE gateway_client_tool_grants (
    workspace_id text NOT NULL,
    client_id text NOT NULL,
    tool_id text NOT NULL,
    PRIMARY KEY (workspace_id,client_id,tool_id),
    FOREIGN KEY (workspace_id,client_id) REFERENCES gateway_clients(workspace_id,id) ON DELETE CASCADE,
    FOREIGN KEY (workspace_id,tool_id) REFERENCES tools(workspace_id,id) ON DELETE CASCADE
);
CREATE TABLE gateway_client_server_grants (
    workspace_id text NOT NULL,
    client_id text NOT NULL,
    server_id text NOT NULL,
    PRIMARY KEY (workspace_id,client_id,server_id),
    FOREIGN KEY (workspace_id,client_id) REFERENCES gateway_clients(workspace_id,id) ON DELETE CASCADE,
    FOREIGN KEY (workspace_id,server_id) REFERENCES mcp_servers(workspace_id,id) ON DELETE CASCADE
);
