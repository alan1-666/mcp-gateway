CREATE TABLE gateway_result_artifacts (
 workspace_id text NOT NULL,
 operation_id text NOT NULL,
 payload bytea NOT NULL CHECK (octet_length(payload) BETWEEN 1 AND 1048576),
 sha256 text NOT NULL CHECK (length(sha256)=64),
 expires_at timestamptz NOT NULL,
 PRIMARY KEY (workspace_id,operation_id),
 FOREIGN KEY (workspace_id,operation_id) REFERENCES operations(workspace_id,id) ON DELETE CASCADE
);
CREATE INDEX gateway_result_artifacts_expiry_idx ON gateway_result_artifacts(expires_at);
