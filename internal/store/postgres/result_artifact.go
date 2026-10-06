package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/jackc/pgx/v5"
)

const workspaceArtifactBytes = 100 << 20

var errArtifactQuota = errors.New("result artifact workspace quota exceeded")

// persistResultArtifact runs in the operation completion transaction. The
// validated/projected result is never duplicated into operation/event rows.
func persistResultArtifact(ctx context.Context, tx pgx.Tx, op core.Operation, tool core.Tool, in core.FinishInput) (json.RawMessage, error) {
	if in.State != core.StateSucceeded || tool.MCP == nil || tool.ResponsePolicy == nil || tool.ResponsePolicy.Artifact == nil {
		return in.Result, nil
	}
	policy, err := core.NormalizeResponsePolicy(tool.ResponsePolicy)
	if err != nil {
		return nil, err
	}
	// Defense in depth: even trusted Finish callers cannot persist an unprojected
	// envelope. Connector results were already checked before their queue storage.
	projected, err := core.ApplyMCPResponsePolicy(in.Result, policy)
	if err != nil {
		return nil, err
	}
	if len(projected) <= policy.MaxBytes {
		return projected, nil
	}
	var envelope struct {
		Structured json.RawMessage `json:"structuredContent"`
	}
	if err = json.Unmarshal(projected, &envelope); err != nil || len(envelope.Structured) == 0 {
		return nil, core.ErrInvalid
	}
	raw := envelope.Structured
	// Serialize quota accounting per workspace, without holding a network call.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('result-artifacts:' || $1,0))`, op.WorkspaceID); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM gateway_result_artifacts WHERE workspace_id=$1 AND expires_at<=clock_timestamp()`, op.WorkspaceID); err != nil {
		return nil, err
	}
	var used, count int64
	if err = tx.QueryRow(ctx, `SELECT COALESCE(sum(octet_length(payload)),0),count(*) FROM gateway_result_artifacts WHERE workspace_id=$1`, op.WorkspaceID).Scan(&used, &count); err != nil {
		return nil, err
	}
	if used+int64(len(raw)) > workspaceArtifactBytes || count >= 1000 {
		return nil, errArtifactQuota
	}
	sum := sha256.Sum256(raw)
	ref := core.ResultReference{OperationID: op.ID, Bytes: len(raw), SHA256: hex.EncodeToString(sum[:]), Format: "json_utf8"}
	err = tx.QueryRow(ctx, `INSERT INTO gateway_result_artifacts(workspace_id,operation_id,payload,sha256,expires_at) VALUES($1,$2,$3,$4,clock_timestamp()+($5*interval '1 second')) RETURNING expires_at`, op.WorkspaceID, op.ID, []byte(raw), ref.SHA256, policy.Artifact.TTLSeconds).Scan(&ref.ExpiresAt)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"gateway_result_ref": ref, "message": "Read the projected structured JSON with read_result using operation_id; concatenate chunks before parsing. The reference is not the tool output schema."})
}

func (r *Repository) ReadResult(ctx context.Context, actor core.Actor, in core.ResultReadInput) (core.ResultPage, error) {
	// Perform authority checks and payload read in one database statement. Current
	// grants, key rotation, disablement, connector status, ownership and expiry are
	// observed on each page. Snapshot server access remains required after rebinding.
	var raw []byte
	ref := core.ResultReference{OperationID: in.OperationID, Format: "json_utf8"}
	err := r.pool.QueryRow(ctx, `SELECT a.payload,a.sha256,a.expires_at FROM gateway_result_artifacts a
 JOIN operations ON operations.workspace_id=a.workspace_id AND operations.id=a.operation_id
 WHERE a.workspace_id=$1 AND a.operation_id=$2 AND a.expires_at>clock_timestamp()
 AND operations.state='SUCCEEDED' AND (operations.actor_id=$3 OR $4 IN ('admin','approver'))
 AND `+clientOperationAccessSQL("$5", "$6")+`
 AND EXISTS(SELECT 1 FROM tools WHERE tools.workspace_id=a.workspace_id AND tools.id=operations.tool_id AND enabled AND status='published' AND `+serverAvailable+`)
 AND (operations.tool_snapshot->'mcp'->>'server_id' IS NULL OR EXISTS(SELECT 1 FROM mcp_servers ms WHERE ms.workspace_id=a.workspace_id AND ms.id=operations.tool_snapshot->'mcp'->>'server_id' AND ms.enabled AND `+connectorAvailable+`))`, actor.WorkspaceID, in.OperationID, actor.ID, actor.Role, actor.ClientID, actor.ClientKeyID).Scan(&raw, &ref.SHA256, &ref.ExpiresAt)
	if err != nil {
		return core.ResultPage{}, dbError(err)
	}
	ref.Bytes = len(raw)
	return core.ResultChunk(ref, raw, in)
}

// CleanupResultArtifacts deletes at most 500 expired payloads per maintenance
// tick. Reads reject expired references immediately, regardless of cleanup lag.
func (r *Repository) CleanupResultArtifacts(ctx context.Context) (int64, error) {
	tag, err := r.pool.Exec(ctx, `WITH expired AS (SELECT workspace_id,operation_id FROM gateway_result_artifacts WHERE expires_at<=clock_timestamp() ORDER BY expires_at LIMIT 500 FOR UPDATE SKIP LOCKED) DELETE FROM gateway_result_artifacts a USING expired e WHERE a.workspace_id=e.workspace_id AND a.operation_id=e.operation_id`)
	return tag.RowsAffected(), err
}
