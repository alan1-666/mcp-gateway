package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/jackc/pgx/v5"
)

func (r *Repository) UpdateToolApprovalPolicy(ctx context.Context, actor core.Actor, id string, in core.ApprovalPolicyUpdateInput) (core.Tool, error) {
	if actor.Role != core.RoleAdmin || actor.ClientID != "" {
		return core.Tool{}, core.ErrForbidden
	}
	if in.ExpectedVersion < 1 || (in.ApprovalPolicy != core.ApprovalRequired && in.ApprovalPolicy != core.ApprovalNone) {
		return core.Tool{}, core.ErrInvalid
	}
	return transaction(ctx, r.pool, func(tx pgx.Tx) (core.Tool, error) {
		tool, err := scanTool(tx.QueryRow(ctx, `SELECT `+toolColumns+` FROM tools WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, actor.WorkspaceID, id))
		if err != nil {
			return tool, err
		}
		if tool.Version != in.ExpectedVersion {
			return core.Tool{}, fmt.Errorf("%w: tool version changed; reload before saving", core.ErrConflict)
		}
		if tool.ApprovalPolicy == in.ApprovalPolicy {
			return New(tx).GetTool(ctx, actor, id)
		}
		raw, _ := json.Marshal(in.ApprovalPolicy)
		if _, err := tx.Exec(ctx, `UPDATE tools SET definition=jsonb_set(definition,'{approval_policy}',$3::jsonb,true),version=version+1 WHERE workspace_id=$1 AND id=$2`, actor.WorkspaceID, id, raw); err != nil {
			return core.Tool{}, err
		}
		if err := appendAudit(ctx, tx, actor, "TOOL_APPROVAL_POLICY_UPDATED", id, map[string]any{"old_version": tool.Version, "new_version": tool.Version + 1, "old_approval_policy": tool.ApprovalPolicy, "approval_policy": in.ApprovalPolicy}); err != nil {
			return core.Tool{}, err
		}
		return New(tx).GetTool(ctx, actor, id)
	})
}
