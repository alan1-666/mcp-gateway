package runs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/alan1-666/mcp-gateway/internal/execution"
	"github.com/alan1-666/mcp-gateway/internal/store/postgres"
	"github.com/jackc/pgx/v5"
)

var ErrBudget = errors.New("run tool budget exhausted")
var ErrLease = errors.New("lease is no longer active")

func budget(ctx context.Context, tx pgx.Tx, workspace, id string) error {
	tag, err := tx.Exec(ctx, `UPDATE agent_runs SET tool_calls=tool_calls+1 WHERE workspace_id=$1 AND id=$2 AND tool_calls<$3`, workspace, id, MaxToolCalls)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrBudget
	}
	return nil
}

// GatewayActor authorizes every leased read, without exposing any administrator
// capabilities even when a Run was created by a workspace administrator.
func (r *Repository) GatewayActor(ctx context.Context, workspace, id, token string) (core.Actor, error) {
	return transact(ctx, r.pool, func(tx pgx.Tx) (core.Actor, error) {
		_, a, err := r.lease(ctx, tx, workspace, id, token)
		return a, err
	})
}
func bound(ctx context.Context, tx pgx.Tx, workspace, id, operation string) error {
	var exists bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_run_operations WHERE workspace_id=$1 AND run_id=$2 AND operation_id=$3)`, workspace, id, operation).Scan(&exists)
	if err != nil {
		return err
	}
	if !exists {
		return core.ErrNotFound
	}
	return nil
}
func (r *Repository) Operation(ctx context.Context, service *core.Service, workspace, id, token, operation string) (core.Operation, error) {
	return transact(ctx, r.pool, func(tx pgx.Tx) (core.Operation, error) {
		_, actor, err := r.lease(ctx, tx, workspace, id, token)
		if err != nil {
			return core.Operation{}, err
		}
		if err = bound(ctx, tx, workspace, id, operation); err != nil {
			return core.Operation{}, err
		}
		return core.NewService(postgres.New(tx)).GetOperation(ctx, actor, operation)
	})
}
func digest(value string) string { h := sha256.Sum256([]byte(value)); return hex.EncodeToString(h[:]) }

// Prepare composes the core operation transaction as a savepoint. The operation,
// Run binding, idempotency intent and budget commit atomically on one connection.
func (r *Repository) Prepare(ctx context.Context, service *core.Service, workspace, id, token string, in core.PrepareInput) (core.Operation, error) {
	if !keyOK(in.IdempotencyKey, 128) || in.ToolID == "" {
		return core.Operation{}, invalid("tool_id and idempotency_key are required")
	}
	originalKey := in.IdempotencyKey
	canonical, argHash, err := core.CanonicalArguments(in.Arguments)
	if err != nil {
		return core.Operation{}, err
	}
	in.Arguments = canonical
	intentHash := digest(in.ToolID + ":" + argHash)
	return transact(ctx, r.pool, func(tx pgx.Tx) (core.Operation, error) {
		_, actor, err := r.lease(ctx, tx, workspace, id, token)
		if err != nil {
			return core.Operation{}, err
		}
		service := core.NewService(postgres.New(tx))
		var oldHash, operation string
		err = tx.QueryRow(ctx, `SELECT intent_hash,operation_id FROM agent_run_intents WHERE workspace_id=$1 AND run_id=$2 AND idempotency_key=$3`, workspace, id, in.IdempotencyKey).Scan(&oldHash, &operation)
		if err == nil {
			if oldHash != intentHash {
				return core.Operation{}, conflict("idempotency key is bound to a different intent")
			}
			return core.NewService(postgres.New(tx)).GetOperation(ctx, actor, operation)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return core.Operation{}, err
		}
		tool, err := service.GetTool(ctx, actor, in.ToolID)
		if err != nil {
			return core.Operation{}, err
		}
		if !tool.Enabled || tool.Status != "published" {
			return core.Operation{}, core.ErrNotFound
		}
		// Write intents are unique by tool+canonical arguments across every attempt.
		// Read requests may intentionally refresh data with a new caller key.
		discriminator := in.IdempotencyKey
		if tool.Risk == core.RiskWrite {
			discriminator = intentHash
		}
		namespaced := fmt.Sprintf("run:%s:%s", id, digest(string(tool.Risk)+":"+discriminator))
		in.IdempotencyKey = namespaced
		// Reserve a budget slot while holding the Run lock, rolling back on errors.
		if err = budget(ctx, tx, workspace, id); err != nil {
			return core.Operation{}, err
		}
		op, err := service.Prepare(ctx, actor, in)
		if err != nil {
			return core.Operation{}, err
		}
		_, err = tx.Exec(ctx, `INSERT INTO agent_run_operations(workspace_id,run_id,operation_id) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, workspace, id, op.ID)
		if err != nil {
			return core.Operation{}, err
		}
		_, err = tx.Exec(ctx, `INSERT INTO agent_run_intents(workspace_id,run_id,idempotency_key,intent_hash,operation_id) VALUES($1,$2,$3,$4,$5)`, workspace, id, originalKey, intentHash, op.ID)
		return op, err
	})
}

// leaseRepository replaces only execution admission. Once a downstream call was
// admitted, Finish always persists its eventual outcome even after cancellation.
// This keeps the existing operation ledger authoritative for uncertainty.
type leaseRepository struct {
	core.Repository
	runs                 *Repository
	workspace, id, token string
}

func (g *leaseRepository) Claim(ctx context.Context, _ core.Actor, operation string) (core.Operation, core.Tool, bool, error) {
	type result struct {
		op              core.Operation
		tool            core.Tool
		claimed         bool
		completionError error
	}
	v, err := transact(ctx, g.runs.pool, func(tx pgx.Tx) (result, error) {
		_, actor, err := g.runs.lease(ctx, tx, g.workspace, g.id, g.token)
		if err != nil {
			return result{}, err
		}
		if err = bound(ctx, tx, g.workspace, g.id, operation); err != nil {
			return result{}, err
		}
		scoped := core.NewService(postgres.New(tx))
		op, err := scoped.GetOperation(ctx, actor, operation)
		if err != nil {
			return result{}, err
		}
		if op.State == core.StateReady {
			if err = budget(ctx, tx, g.workspace, g.id); err != nil {
				return result{}, err
			}
		}
		op, tool, claimed, err := scoped.Claim(ctx, actor, operation)
		if errors.Is(err, core.ErrApprovalExpired) {
			return result{op: op, tool: tool, completionError: err}, nil
		}
		return result{op: op, tool: tool, claimed: claimed}, err
	})
	if err == nil {
		err = v.completionError
	}
	return v.op, v.tool, v.claimed, err
}
func (r *Repository) Execute(ctx context.Context, executor *execution.Executor, workspace, id, token, operation string) (core.Operation, error) {
	actor, err := r.GatewayActor(ctx, workspace, id, token)
	if err != nil {
		return core.Operation{}, err
	}
	guarded := &leaseRepository{Repository: postgres.New(r.pool), runs: r, workspace: workspace, id: id, token: token}
	e := execution.Executor{Service: core.NewService(guarded), Adapter: executor.Adapter}
	return e.Execute(ctx, actor, operation)
}
