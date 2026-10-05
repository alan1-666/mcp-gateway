CREATE TABLE operation_reconciliations (
 id bigserial PRIMARY KEY,
 workspace_id text NOT NULL,
 operation_id text NOT NULL,
 outcome text NOT NULL CHECK (outcome IN ('confirmed_success','confirmed_failure','inconclusive')),
 evidence_ref text NOT NULL,
 note text NOT NULL,
 actor_id text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY (workspace_id,operation_id) REFERENCES operations(workspace_id,id) ON DELETE CASCADE
);
CREATE INDEX operation_reconciliations_lookup_idx ON operation_reconciliations(workspace_id,operation_id,id DESC);
CREATE INDEX operations_workspace_state_created_idx ON operations(workspace_id,state,created_at DESC,id DESC);
CREATE INDEX operations_workspace_tool_created_idx ON operations(workspace_id,tool_id,created_at DESC,id DESC);
CREATE INDEX audit_events_workspace_action_created_idx ON audit_events(workspace_id,action,created_at DESC,id DESC);

CREATE FUNCTION reject_reconciliation_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 RAISE EXCEPTION 'Reconciliation evidence is append-only';
END;
$$;
CREATE TRIGGER operation_reconciliations_immutable BEFORE UPDATE ON operation_reconciliations
 FOR EACH ROW EXECUTE FUNCTION reject_reconciliation_update();
