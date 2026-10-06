-- Legacy snapshots without approval_policy continue to require independent
-- approval for writes. Only an explicit, versioned exemption permits no approver.
ALTER TABLE operations DROP CONSTRAINT operations_check2;
ALTER TABLE operations ADD CONSTRAINT operations_write_approval_check CHECK (
 risk <> 'write'
 OR state NOT IN ('READY','DISPATCHING','SUCCEEDED','FAILED','UNKNOWN')
 OR approved_by <> ''
 OR COALESCE(tool_snapshot->>'approval_policy' = 'none', false)
);
