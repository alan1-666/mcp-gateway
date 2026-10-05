package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/jackc/pgx/v5"
)

func (r *Repository) UpdateToolResponsePolicy(ctx context.Context, actor core.Actor, id string, in core.ResponsePolicyUpdateInput) (core.Tool, error) {
	if in.ExpectedVersion < 1 || in.ResponsePolicy == nil {
		return core.Tool{}, fmt.Errorf("%w: expected_version and response_policy are required", core.ErrInvalid)
	}
	policy, err := core.NormalizeResponsePolicy(in.ResponsePolicy)
	if err != nil {
		return core.Tool{}, err
	}
	return transaction(ctx, r.pool, func(tx pgx.Tx) (core.Tool, error) {
		tool, err := scanTool(tx.QueryRow(ctx, `SELECT `+toolColumns+` FROM tools WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, actor.WorkspaceID, id))
		if err != nil {
			return tool, err
		}
		if tool.MCP == nil {
			return core.Tool{}, fmt.Errorf("%w: response policies require an MCP tool", core.ErrInvalid)
		}
		if tool.Version != in.ExpectedVersion {
			return core.Tool{}, fmt.Errorf("%w: tool version changed; reload before saving", core.ErrConflict)
		}
		raw, err := json.Marshal(policy)
		if err != nil {
			return core.Tool{}, err
		}
		previous, previousErr := core.NormalizeResponsePolicy(tool.ResponsePolicy)
		previousRaw, _ := json.Marshal(previous)
		// A no-op still requires the current expected version. Existing operation
		// snapshots remain untouched, even when a real update increments the tool.
		if previousErr == nil && bytes.Equal(previousRaw, raw) {
			return New(tx).GetTool(ctx, actor, id)
		}
		oldVersion := tool.Version
		if _, err := tx.Exec(ctx, `UPDATE tools SET definition=jsonb_set(definition,'{response_policy}',$3::jsonb,true),version=version+1 WHERE workspace_id=$1 AND id=$2`, actor.WorkspaceID, id, raw); err != nil {
			return core.Tool{}, err
		}
		if err := appendAudit(ctx, tx, actor, "TOOL_RESPONSE_POLICY_UPDATED", id, map[string]any{"old_version": oldVersion, "new_version": oldVersion + 1, "old_response_policy": tool.ResponsePolicy, "response_policy": policy}); err != nil {
			return core.Tool{}, err
		}
		// Reading effective visibility also handles configuration repairs while the
		// upstream server is disabled, without changing the tool's own enabled flag.
		return New(tx).GetTool(ctx, actor, id)
	})
}
