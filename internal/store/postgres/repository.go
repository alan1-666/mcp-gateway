package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// DB is implemented by both pgxpool.Pool and pgx.Tx. A transaction-backed
// repository uses pgx savepoints, keeping composed state changes atomic and
// avoiding a second connection while holding database locks.
type DB interface {
	Begin(context.Context) (pgx.Tx, error)
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}
type Repository struct{ pool DB }

func New(pool DB) *Repository { return &Repository{pool: pool} }

var _ core.Repository = (*Repository)(nil)

const toolColumns = `id, workspace_id, name, risk, status, enabled, version, definition, created_at`
const operationColumns = `id, workspace_id, tool_id, tool_name, tool_version, risk, actor_id, arguments, arguments_hash, idempotency_key, state, result, error, approved_by, approval_expires_at, created_at, updated_at, tool_snapshot`

type scanner interface{ Scan(...any) error }

func scanTool(row scanner) (core.Tool, error) {
	var tool core.Tool
	var raw []byte
	if err := row.Scan(&tool.ID, &tool.WorkspaceID, &tool.Name, &tool.Risk, &tool.Status, &tool.Enabled, &tool.Version, &raw, &tool.CreatedAt); err != nil {
		return tool, dbError(err)
	}
	var input core.ToolInput
	if err := json.Unmarshal(raw, &input); err != nil {
		return tool, fmt.Errorf("decode stored tool: %w", err)
	}
	tool.Description, tool.InputSchema, tool.OutputSchema, tool.HTTP = input.Description, input.InputSchema, input.OutputSchema, input.HTTP
	return tool, nil
}
func scanOperation(row scanner) (core.Operation, core.Tool, error) {
	var op core.Operation
	var raw []byte
	if err := row.Scan(&op.ID, &op.WorkspaceID, &op.ToolID, &op.ToolName, &op.ToolVersion, &op.Risk, &op.ActorID, &op.Arguments, &op.ArgumentsHash, &op.IdempotencyKey, &op.State, &op.Result, &op.Error, &op.ApprovedBy, &op.ApprovalExpiresAt, &op.CreatedAt, &op.UpdatedAt, &raw); err != nil {
		return op, core.Tool{}, dbError(err)
	}
	var tool core.Tool
	if err := json.Unmarshal(raw, &tool); err != nil {
		return op, tool, fmt.Errorf("decode stored tool snapshot: %w", err)
	}
	return op, tool, nil
}
func dbError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return core.ErrNotFound
	}
	var pgerr *pgconn.PgError
	if errors.As(err, &pgerr) && pgerr.Code == "23505" {
		return core.ErrConflict
	}
	return err
}
func transaction[T any](ctx context.Context, pool DB, fn func(pgx.Tx) (T, error)) (T, error) {
	var empty T
	tx, err := pool.Begin(ctx)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback(context.Background())
	result, err := fn(tx)
	if err != nil {
		return empty, dbError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return empty, dbError(err)
	}
	return result, nil
}
func appendEvent(ctx context.Context, tx pgx.Tx, op core.Operation, kind, actor string, data any) error {
	body, err := json.Marshal(data)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO operation_events(workspace_id,operation_id,type,actor_id,data) VALUES($1,$2,$3,$4,$5)`, op.WorkspaceID, op.ID, kind, actor, body)
	return err
}
func appendAudit(ctx context.Context, tx pgx.Tx, actor core.Actor, action, id string, data any) error {
	body, err := json.Marshal(data)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO audit_events(workspace_id,actor_id,action,resource_id,data) VALUES($1,$2,$3,$4,$5)`, actor.WorkspaceID, actor.ID, action, id, body)
	return err
}

func (r *Repository) CreateTool(ctx context.Context, actor core.Actor, input core.ToolInput) (core.Tool, error) {
	return transaction(ctx, r.pool, func(tx pgx.Tx) (core.Tool, error) {
		body, err := json.Marshal(input)
		if err != nil {
			return core.Tool{}, err
		}
		tool, err := scanTool(tx.QueryRow(ctx, `INSERT INTO tools(workspace_id,id,name,risk,definition) VALUES($1,$2,$3,$4,$5) RETURNING `+toolColumns, actor.WorkspaceID, core.NewID(), input.Name, input.Risk, body))
		if err != nil {
			return core.Tool{}, err
		}
		err = appendAudit(ctx, tx, actor, "TOOL_CREATED", tool.ID, map[string]any{"name": tool.Name, "version": tool.Version})
		return tool, err
	})
}
func (r *Repository) ListTools(ctx context.Context, actor core.Actor) ([]core.Tool, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+toolColumns+` FROM tools WHERE workspace_id=$1 AND ($2 OR (status='published' AND enabled)) ORDER BY created_at DESC,id DESC LIMIT 500`, actor.WorkspaceID, actor.Role == core.RoleAdmin)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []core.Tool{}
	for rows.Next() {
		tool, err := scanTool(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, tool)
	}
	return items, rows.Err()
}
func (r *Repository) GetTool(ctx context.Context, actor core.Actor, id string) (core.Tool, error) {
	return scanTool(r.pool.QueryRow(ctx, `SELECT `+toolColumns+` FROM tools WHERE workspace_id=$1 AND id=$2 AND ($3 OR (status='published' AND enabled))`, actor.WorkspaceID, id, actor.Role == core.RoleAdmin))
}
func (r *Repository) PublishTool(ctx context.Context, actor core.Actor, id string) (core.Tool, error) {
	return transaction(ctx, r.pool, func(tx pgx.Tx) (core.Tool, error) {
		tool, err := scanTool(tx.QueryRow(ctx, `SELECT `+toolColumns+` FROM tools WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, actor.WorkspaceID, id))
		if err != nil {
			return tool, err
		}
		if tool.Status == "published" {
			return tool, nil
		}
		tool, err = scanTool(tx.QueryRow(ctx, `UPDATE tools SET status='published',enabled=true WHERE workspace_id=$1 AND id=$2 RETURNING `+toolColumns, actor.WorkspaceID, id))
		if err != nil {
			return tool, err
		}
		return tool, appendAudit(ctx, tx, actor, "TOOL_PUBLISHED", id, map[string]any{"version": tool.Version})
	})
}
func (r *Repository) SetToolEnabled(ctx context.Context, actor core.Actor, id string, enabled bool) (core.Tool, error) {
	return transaction(ctx, r.pool, func(tx pgx.Tx) (core.Tool, error) {
		tool, err := scanTool(tx.QueryRow(ctx, `SELECT `+toolColumns+` FROM tools WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, actor.WorkspaceID, id))
		if err != nil {
			return tool, err
		}
		if tool.Status != "published" {
			return tool, fmt.Errorf("%w: only published tools can be enabled or disabled", core.ErrConflict)
		}
		if tool.Enabled == enabled {
			return tool, nil
		}
		tool, err = scanTool(tx.QueryRow(ctx, `UPDATE tools SET enabled=$3 WHERE workspace_id=$1 AND id=$2 RETURNING `+toolColumns, actor.WorkspaceID, id, enabled))
		if err != nil {
			return tool, err
		}
		return tool, appendAudit(ctx, tx, actor, "TOOL_ENABLED_CHANGED", id, map[string]any{"enabled": enabled})
	})
}

func (r *Repository) Prepare(ctx context.Context, actor core.Actor, in core.PrepareInput) (core.Operation, error) {
	return transaction(ctx, r.pool, func(tx pgx.Tx) (core.Operation, error) {
		canonical, hash, err := core.CanonicalArguments(in.Arguments)
		if err != nil {
			return core.Operation{}, err
		}
		// Serialize a workspace/key pair before checking existence. The unique
		// constraint remains the final guard and hash collisions only serialize.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, actor.WorkspaceID+"\x1f"+in.IdempotencyKey); err != nil {
			return core.Operation{}, err
		}
		existing, _, err := scanOperation(tx.QueryRow(ctx, `SELECT `+operationColumns+` FROM operations WHERE workspace_id=$1 AND idempotency_key=$2`, actor.WorkspaceID, in.IdempotencyKey))
		if err == nil {
			if existing.ActorID != actor.ID || existing.ToolID != in.ToolID || existing.ArgumentsHash != hash {
				return core.Operation{}, fmt.Errorf("%w: idempotency key is bound to another intent", core.ErrConflict)
			}
			return existing, nil
		}
		if !errors.Is(err, core.ErrNotFound) {
			return core.Operation{}, err
		}
		tool, err := scanTool(tx.QueryRow(ctx, `SELECT `+toolColumns+` FROM tools WHERE workspace_id=$1 AND id=$2 AND status='published' AND enabled FOR SHARE`, actor.WorkspaceID, in.ToolID))
		if err != nil {
			return core.Operation{}, err
		}
		if err := core.ValidateArguments(tool.InputSchema, canonical); err != nil {
			return core.Operation{}, err
		}
		state := core.StateReady
		if tool.Risk == core.RiskWrite {
			state = core.StateWaitingApproval
		}
		snapshot, err := json.Marshal(tool)
		if err != nil {
			return core.Operation{}, err
		}
		op, _, err := scanOperation(tx.QueryRow(ctx, `INSERT INTO operations(workspace_id,id,tool_id,tool_name,tool_version,risk,actor_id,arguments,arguments_hash,idempotency_key,tool_snapshot,state) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) RETURNING `+operationColumns, actor.WorkspaceID, core.NewID(), tool.ID, tool.Name, tool.Version, tool.Risk, actor.ID, canonical, hash, in.IdempotencyKey, snapshot, state))
		if err != nil {
			return core.Operation{}, err
		}
		return op, appendEvent(ctx, tx, op, "OPERATION_PREPARED", actor.ID, map[string]any{"state": state, "tool_version": tool.Version, "arguments_hash": hash})
	})
}

func (r *Repository) Approve(ctx context.Context, actor core.Actor, id string) (core.Operation, error) {
	return transaction(ctx, r.pool, func(tx pgx.Tx) (core.Operation, error) {
		op, _, err := scanOperation(tx.QueryRow(ctx, `SELECT `+operationColumns+` FROM operations WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, actor.WorkspaceID, id))
		if err != nil {
			return op, err
		}
		if op.ActorID == actor.ID {
			return core.Operation{}, fmt.Errorf("%w: a different person must approve the operation", core.ErrForbidden)
		}
		if op.State == core.StateReady && op.ApprovedBy != "" {
			return op, nil
		}
		if op.State != core.StateWaitingApproval {
			return core.Operation{}, fmt.Errorf("%w: operation is not awaiting approval", core.ErrConflict)
		}
		// Lock the current tool while granting approval so a committed disable is
		// honored. Dispatch will independently repeat this check.
		var enabled bool
		if err := tx.QueryRow(ctx, `SELECT enabled FROM tools WHERE workspace_id=$1 AND id=$2 AND status='published' FOR SHARE`, actor.WorkspaceID, op.ToolID).Scan(&enabled); err != nil {
			return core.Operation{}, dbError(err)
		}
		if !enabled {
			return core.Operation{}, fmt.Errorf("%w: tool is disabled", core.ErrConflict)
		}
		op, _, err = scanOperation(tx.QueryRow(ctx, `UPDATE operations SET state='READY',approved_by=$3,approval_expires_at=clock_timestamp()+interval '30 minutes',updated_at=clock_timestamp() WHERE workspace_id=$1 AND id=$2 RETURNING `+operationColumns, actor.WorkspaceID, id, actor.ID))
		if err != nil {
			return op, err
		}
		return op, appendEvent(ctx, tx, op, "OPERATION_APPROVED", actor.ID, map[string]any{"expires_at": op.ApprovalExpiresAt, "arguments_hash": op.ArgumentsHash, "tool_version": op.ToolVersion})
	})
}
func (r *Repository) Reject(ctx context.Context, actor core.Actor, id string) (core.Operation, error) {
	return transaction(ctx, r.pool, func(tx pgx.Tx) (core.Operation, error) {
		op, _, err := scanOperation(tx.QueryRow(ctx, `SELECT `+operationColumns+` FROM operations WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, actor.WorkspaceID, id))
		if err != nil {
			return op, err
		}
		if op.ActorID == actor.ID {
			return core.Operation{}, fmt.Errorf("%w: a different person must review the operation", core.ErrForbidden)
		}
		if op.State == core.StateRejected {
			return op, nil
		}
		if op.State != core.StateWaitingApproval {
			return core.Operation{}, fmt.Errorf("%w: operation is not awaiting approval", core.ErrConflict)
		}
		op, _, err = scanOperation(tx.QueryRow(ctx, `UPDATE operations SET state='REJECTED',error='approval rejected',updated_at=clock_timestamp() WHERE workspace_id=$1 AND id=$2 RETURNING `+operationColumns, actor.WorkspaceID, id))
		if err != nil {
			return op, err
		}
		return op, appendEvent(ctx, tx, op, "OPERATION_REJECTED", actor.ID, map[string]any{})
	})
}

func (r *Repository) GetOperation(ctx context.Context, actor core.Actor, id string) (core.Operation, error) {
	op, _, err := scanOperation(r.pool.QueryRow(ctx, `SELECT `+operationColumns+` FROM operations WHERE workspace_id=$1 AND id=$2 AND (actor_id=$3 OR $4)`, actor.WorkspaceID, id, actor.ID, actor.Role == core.RoleAdmin || actor.Role == core.RoleApprover))
	return op, err
}
func (r *Repository) ListOperations(ctx context.Context, actor core.Actor) ([]core.Operation, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+operationColumns+` FROM operations WHERE workspace_id=$1 AND (actor_id=$2 OR $3) ORDER BY created_at DESC,id DESC LIMIT 200`, actor.WorkspaceID, actor.ID, actor.Role == core.RoleAdmin || actor.Role == core.RoleApprover)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []core.Operation{}
	for rows.Next() {
		op, _, err := scanOperation(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, op)
	}
	return items, rows.Err()
}
func (r *Repository) ListEvents(ctx context.Context, actor core.Actor, id string, after int64) ([]core.Event, error) {
	if _, err := r.GetOperation(ctx, actor, id); err != nil {
		return nil, err
	}
	rows, err := r.pool.Query(ctx, `SELECT id,operation_id,type,actor_id,created_at,data FROM operation_events WHERE workspace_id=$1 AND operation_id=$2 AND id>$3 ORDER BY id LIMIT 500`, actor.WorkspaceID, id, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []core.Event{}
	for rows.Next() {
		var event core.Event
		if err := rows.Scan(&event.ID, &event.OperationID, &event.Type, &event.ActorID, &event.CreatedAt, &event.Data); err != nil {
			return nil, err
		}
		items = append(items, event)
	}
	return items, rows.Err()
}

type claimResult struct {
	op      core.Operation
	tool    core.Tool
	granted bool
	expired bool
}

func (r *Repository) Claim(ctx context.Context, actor core.Actor, id string) (core.Operation, core.Tool, bool, error) {
	result, err := transaction(ctx, r.pool, func(tx pgx.Tx) (claimResult, error) {
		op, tool, err := scanOperation(tx.QueryRow(ctx, `SELECT `+operationColumns+` FROM operations WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, actor.WorkspaceID, id))
		if err != nil {
			return claimResult{}, err
		}
		if !core.CanExecuteOperation(actor, op) {
			return claimResult{}, core.ErrNotFound
		}
		result := claimResult{op: op, tool: tool}
		// Replays still require the same currently authorized actor. They do not
		// dispatch even when a previous process died after sending the request.
		if op.State == core.StateDispatching || op.State == core.StateSucceeded || op.State == core.StateFailed || op.State == core.StateUnknown || op.State == core.StateRejected {
			return result, nil
		}
		if op.State != core.StateReady {
			return result, fmt.Errorf("%w: approval is required before dispatch", core.ErrConflict)
		}
		var enabled bool
		if err := tx.QueryRow(ctx, `SELECT enabled FROM tools WHERE workspace_id=$1 AND id=$2 AND status='published' FOR SHARE`, actor.WorkspaceID, op.ToolID).Scan(&enabled); err != nil {
			return result, dbError(err)
		}
		if !enabled {
			return result, fmt.Errorf("%w: tool is disabled", core.ErrConflict)
		}
		if op.Risk == core.RiskWrite {
			var approvalValid bool
			if err := tx.QueryRow(ctx, `SELECT approved_by<>'' AND approved_by<>actor_id AND approval_expires_at>clock_timestamp() FROM operations WHERE workspace_id=$1 AND id=$2`, actor.WorkspaceID, id).Scan(&approvalValid); err != nil {
				return result, err
			}
			if !approvalValid {
				result.op, _, err = scanOperation(tx.QueryRow(ctx, `UPDATE operations SET state='REJECTED',error='approval expired',updated_at=clock_timestamp() WHERE workspace_id=$1 AND id=$2 RETURNING `+operationColumns, actor.WorkspaceID, id))
				if err != nil {
					return result, err
				}
				result.expired = true
				return result, appendEvent(ctx, tx, result.op, "APPROVAL_EXPIRED", "system", map[string]any{})
			}
		}
		result.op, _, err = scanOperation(tx.QueryRow(ctx, `UPDATE operations SET state='DISPATCHING',updated_at=clock_timestamp() WHERE workspace_id=$1 AND id=$2 RETURNING `+operationColumns, actor.WorkspaceID, id))
		if err != nil {
			return result, err
		}
		result.granted = true
		return result, appendEvent(ctx, tx, result.op, "OPERATION_DISPATCHING", actor.ID, map[string]any{"arguments_hash": op.ArgumentsHash, "tool_version": op.ToolVersion})
	})
	if err == nil && result.expired {
		err = core.ErrApprovalExpired
	}
	return result.op, result.tool, result.granted, err
}

func (r *Repository) Finish(ctx context.Context, actor core.Actor, id string, in core.FinishInput) (core.Operation, error) {
	return transaction(ctx, r.pool, func(tx pgx.Tx) (core.Operation, error) {
		op, _, err := scanOperation(tx.QueryRow(ctx, `SELECT `+operationColumns+` FROM operations WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, actor.WorkspaceID, id))
		if err != nil {
			return op, err
		}
		if !core.CanExecuteOperation(actor, op) {
			return core.Operation{}, core.ErrNotFound
		}
		if op.State != core.StateDispatching {
			return core.Operation{}, fmt.Errorf("%w: operation is not dispatching", core.ErrConflict)
		}
		var result any
		if len(in.Result) > 0 {
			result = in.Result
		}
		op, _, err = scanOperation(tx.QueryRow(ctx, `UPDATE operations SET state=$3,result=$4,error=$5,updated_at=clock_timestamp() WHERE workspace_id=$1 AND id=$2 RETURNING `+operationColumns, actor.WorkspaceID, id, in.State, result, in.Error))
		if err != nil {
			return op, err
		}
		return op, appendEvent(ctx, tx, op, "OPERATION_"+string(in.State), actor.ID, map[string]any{"state": in.State})
	})
}

func (r *Repository) Recover(ctx context.Context, olderThan time.Duration) (int, error) {
	// Recovery is an internal cross-workspace maintenance capability. It only
	// marks uncertainty and cannot grant execution or resend an external action.
	return transaction(ctx, r.pool, func(tx pgx.Tx) (int, error) {
		rows, err := tx.Query(ctx, `SELECT `+operationColumns+` FROM operations WHERE state='DISPATCHING' AND updated_at<clock_timestamp()-($1*interval '1 millisecond') ORDER BY updated_at LIMIT 100 FOR UPDATE SKIP LOCKED`, olderThan.Milliseconds())
		if err != nil {
			return 0, err
		}
		var stale []core.Operation
		for rows.Next() {
			op, _, err := scanOperation(rows)
			if err != nil {
				rows.Close()
				return 0, err
			}
			stale = append(stale, op)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return 0, err
		}
		for _, op := range stale {
			if _, err := tx.Exec(ctx, `UPDATE operations SET state='UNKNOWN',error='executor interrupted; verify the downstream result before taking further action',updated_at=clock_timestamp() WHERE workspace_id=$1 AND id=$2`, op.WorkspaceID, op.ID); err != nil {
				return 0, err
			}
			if err := appendEvent(ctx, tx, op, "OPERATION_UNKNOWN", "system", map[string]any{"reason": "stale_dispatch"}); err != nil {
				return 0, err
			}
		}
		return len(stale), nil
	})
}
