package capacity

import (
	"context"
	"time"
)

// Retention removes only definite terminal operations and fully terminal runs.
// UNKNOWN, NEEDS_REVIEW, active runs, or any historical OPERATION_UNKNOWN event
// hold their operation graph indefinitely for manual reconciliation. Identity
// events, tool versions and credentials are outside this retention policy.
// Expiring idempotency records means keys may be reused after OperationsAge.
// Audit events have a separate, strictly longer horizon.
type RetentionConfig struct {
	OperationsAge time.Duration
	AuditAge      time.Duration
	BatchSize     int
}

func DefaultRetentionConfig() RetentionConfig {
	return RetentionConfig{OperationsAge: 30 * 24 * time.Hour, AuditAge: 180 * 24 * time.Hour, BatchSize: 100}
}

type RetentionResult struct{ Runs, Operations, AuditEvents, ExpiredLeases, ExpiredWindows int64 }

func (s *Service) Retain(ctx context.Context, c RetentionConfig) (RetentionResult, error) {
	var out RetentionResult
	if c.OperationsAge < 24*time.Hour || c.AuditAge <= c.OperationsAge || c.BatchSize < 1 || c.BatchSize > 1000 {
		return out, invalid("retention requires operation age >=24h, longer audit age and batch size 1..1000")
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	// Workers in different processes skip each other's batches using row locks.
	rows, err := tx.Query(ctx, `SELECT r.workspace_id,r.id FROM agent_runs r WHERE r.updated_at<clock_timestamp()-($1 * interval '1 second') AND r.state IN ('SUCCEEDED','FAILED','CANCELLED') AND NOT EXISTS(SELECT 1 FROM agent_run_operations link JOIN operations o ON (o.workspace_id,o.id)=(link.workspace_id,link.operation_id) WHERE (link.workspace_id,link.run_id)=(r.workspace_id,r.id) AND (o.state NOT IN ('SUCCEEDED','FAILED','REJECTED') OR o.updated_at>=clock_timestamp()-($1 * interval '1 second') OR EXISTS(SELECT 1 FROM operation_events e WHERE (e.workspace_id,e.operation_id)=(o.workspace_id,o.id) AND e.type='OPERATION_UNKNOWN'))) ORDER BY r.updated_at,r.id LIMIT $2 FOR UPDATE OF r SKIP LOCKED`, c.OperationsAge.Seconds(), c.BatchSize)
	if err != nil {
		return out, err
	}
	type id struct{ workspace, key string }
	ids := []id{}
	for rows.Next() {
		var v id
		if err = rows.Scan(&v.workspace, &v.key); err != nil {
			rows.Close()
			return out, err
		}
		ids = append(ids, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	for _, v := range ids {
		for _, table := range []string{"agent_run_intents", "agent_run_operations", "agent_run_events", "agent_runs"} {
			column := "run_id"
			if table == "agent_runs" {
				column = "id"
			}
			if _, err = tx.Exec(ctx, "DELETE FROM "+table+" WHERE workspace_id=$1 AND "+column+"=$2", v.workspace, v.key); err != nil {
				return out, err
			}
		}
		out.Runs++
	}
	rows, err = tx.Query(ctx, `SELECT o.workspace_id,o.id FROM operations o WHERE o.updated_at<clock_timestamp()-($1 * interval '1 second') AND o.state IN ('SUCCEEDED','FAILED','REJECTED') AND NOT EXISTS(SELECT 1 FROM agent_run_operations link WHERE (link.workspace_id,link.operation_id)=(o.workspace_id,o.id)) AND NOT EXISTS(SELECT 1 FROM operation_events e WHERE (e.workspace_id,e.operation_id)=(o.workspace_id,o.id) AND e.type='OPERATION_UNKNOWN') ORDER BY o.updated_at,o.id LIMIT $2 FOR UPDATE OF o SKIP LOCKED`, c.OperationsAge.Seconds(), c.BatchSize)
	if err != nil {
		return out, err
	}
	ids = nil
	for rows.Next() {
		var v id
		if err = rows.Scan(&v.workspace, &v.key); err != nil {
			rows.Close()
			return out, err
		}
		ids = append(ids, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	for _, v := range ids {
		if _, err = tx.Exec(ctx, `DELETE FROM operation_events WHERE workspace_id=$1 AND operation_id=$2`, v.workspace, v.key); err != nil {
			return out, err
		}
		if _, err = tx.Exec(ctx, `DELETE FROM operations WHERE workspace_id=$1 AND id=$2`, v.workspace, v.key); err != nil {
			return out, err
		}
		out.Operations++
	}
	tag, err := tx.Exec(ctx, `DELETE FROM audit_events WHERE id IN (SELECT id FROM audit_events WHERE created_at<clock_timestamp()-($1 * interval '1 second') ORDER BY created_at,id LIMIT $2 FOR UPDATE SKIP LOCKED)`, c.AuditAge.Seconds(), c.BatchSize)
	if err != nil {
		return out, err
	}
	out.AuditEvents = tag.RowsAffected()
	tag, err = tx.Exec(ctx, `DELETE FROM capacity_leases WHERE (workspace_id,operation_id) IN (SELECT workspace_id,operation_id FROM capacity_leases WHERE expires_at<clock_timestamp() ORDER BY expires_at LIMIT $1 FOR UPDATE SKIP LOCKED)`, c.BatchSize)
	if err != nil {
		return out, err
	}
	out.ExpiredLeases = tag.RowsAffected()
	tag, err = tx.Exec(ctx, `DELETE FROM capacity_windows WHERE (workspace_id,scope,scope_id,minute) IN (SELECT workspace_id,scope,scope_id,minute FROM capacity_windows WHERE minute<clock_timestamp()-interval '2 minutes' ORDER BY minute LIMIT $1 FOR UPDATE SKIP LOCKED)`, c.BatchSize)
	if err != nil {
		return out, err
	}
	out.ExpiredWindows = tag.RowsAffected()
	return out, tx.Commit(ctx)
}
