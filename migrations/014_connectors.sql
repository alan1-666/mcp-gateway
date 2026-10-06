CREATE TABLE gateway_connectors (
 workspace_id text NOT NULL,
 id text NOT NULL,
 name text NOT NULL,
 token_hash text NOT NULL UNIQUE,
 enabled boolean NOT NULL DEFAULT true,
 targets jsonb,
 last_seen_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY (workspace_id,id),
 CHECK (targets IS NULL OR jsonb_typeof(targets)='array')
);
CREATE INDEX gateway_connectors_workspace_idx ON gateway_connectors(workspace_id,created_at DESC,id);

ALTER TABLE mcp_servers ADD COLUMN connector_id text;
ALTER TABLE mcp_servers ADD COLUMN target_name text NOT NULL DEFAULT '';
ALTER TABLE mcp_servers ADD CONSTRAINT mcp_servers_connector_fk FOREIGN KEY (workspace_id,connector_id) REFERENCES gateway_connectors(workspace_id,id);
ALTER TABLE mcp_servers ADD CONSTRAINT mcp_servers_transport_binding CHECK (
 (connector_id IS NULL AND target_name='' AND url<>'') OR
 (connector_id IS NOT NULL AND target_name<>'' AND url='' AND credential_ref='')
);

CREATE TABLE gateway_connector_jobs (
 workspace_id text NOT NULL,
 id text NOT NULL,
 connector_id text NOT NULL,
 server_id text NOT NULL,
 operation_id text,
 kind text NOT NULL CHECK (kind IN ('discover','execute')),
 state text NOT NULL DEFAULT 'queued' CHECK (state IN ('queued','claimed','started','completed','expired')),
 target_name text NOT NULL,
 target_fingerprint text NOT NULL,
 payload jsonb,
 actor jsonb NOT NULL,
 client_key_id text NOT NULL DEFAULT '',
 result jsonb,
 result_hash text,
 deadline timestamptz NOT NULL,
 started_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY (workspace_id,id),
 FOREIGN KEY (workspace_id,connector_id) REFERENCES gateway_connectors(workspace_id,id),
 FOREIGN KEY (workspace_id,server_id) REFERENCES mcp_servers(workspace_id,id),
 FOREIGN KEY (workspace_id,operation_id) REFERENCES operations(workspace_id,id),
 UNIQUE (workspace_id,operation_id),
 CHECK ((kind='execute')=(operation_id IS NOT NULL))
);
CREATE INDEX gateway_connector_jobs_queue_idx ON gateway_connector_jobs(workspace_id,connector_id,created_at,id) WHERE state='queued';
CREATE INDEX gateway_connector_jobs_deadline_idx ON gateway_connector_jobs(deadline) WHERE state IN ('queued','claimed','started');
CREATE INDEX gateway_connector_jobs_retention_idx ON gateway_connector_jobs(updated_at) WHERE payload IS NOT NULL;
