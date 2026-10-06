package postgres

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/jackc/pgx/v5"
)

// All identifiers passed here are fixed source-code aliases or parameter
// positions, never user input. Current grants are applied before LIMIT/count.
func clientToolAccessSQL(alias, clientParam, keyParam string) string {
	return fmt.Sprintf(`(%[2]s='' OR EXISTS (
 SELECT 1 FROM gateway_clients gc
 WHERE gc.workspace_id=%[1]s.workspace_id AND gc.id=%[2]s
   AND gc.enabled AND gc.key_expires_at>clock_timestamp()
   AND (%[3]s='' OR gc.key_id=%[3]s) AND 'tools:read'=ANY(gc.scopes)
   AND (EXISTS (SELECT 1 FROM gateway_client_tool_grants ctg WHERE ctg.workspace_id=gc.workspace_id AND ctg.client_id=gc.id AND ctg.tool_id=%[1]s.id)
     OR EXISTS (SELECT 1 FROM gateway_client_server_grants csg WHERE csg.workspace_id=gc.workspace_id AND csg.client_id=gc.id AND csg.server_id=%[1]s.definition->'mcp'->>'server_id'))
 ))`, alias, clientParam, keyParam)
}
func clientOperationAccessSQL(clientParam, keyParam string) string {
	return fmt.Sprintf(`(%[1]s='' OR (EXISTS (SELECT 1 FROM tools authorization_tool WHERE authorization_tool.workspace_id=operations.workspace_id AND authorization_tool.id=operations.tool_id AND %[2]s)
 AND (EXISTS(SELECT 1 FROM gateway_client_tool_grants snapshot_grant WHERE snapshot_grant.workspace_id=operations.workspace_id AND snapshot_grant.client_id=%[1]s AND snapshot_grant.tool_id=operations.tool_id)
 OR EXISTS(SELECT 1 FROM gateway_client_server_grants snapshot_grant WHERE snapshot_grant.workspace_id=operations.workspace_id AND snapshot_grant.client_id=%[1]s AND snapshot_grant.server_id=operations.tool_snapshot->'mcp'->>'server_id'))))`, clientParam, clientToolAccessSQL("authorization_tool", clientParam, keyParam))
}

// ClientOperationAccessSQL applies current client grants to an operations query.
// Arguments must be fixed SQL parameter positions chosen by the caller, never
// request input. The outer operation table must use its unaliased table name.
func ClientOperationAccessSQL(clientParam, keyParam string) string {
	return clientOperationAccessSQL(clientParam, keyParam)
}

// Lock the client row before checking scope/grants. Every mutation of grants,
// enablement or keys takes FOR UPDATE on the same row, so a committed revocation
// cannot race a READY -> DISPATCHING transition. No lock is held during I/O.
func lockClientAccess(ctx context.Context, tx pgx.Tx, actor core.Actor, toolID string, invoke bool) error {
	if actor.ClientID == "" {
		return nil
	}
	if actor.ClientID != actor.ID || actor.Role != core.RoleOperator {
		return core.ErrForbidden
	}
	var scopes []string
	var active bool
	var keyID string
	err := tx.QueryRow(ctx, `SELECT scopes,enabled AND key_expires_at>clock_timestamp(),key_id FROM gateway_clients WHERE workspace_id=$1 AND id=$2 FOR SHARE`, actor.WorkspaceID, actor.ClientID).Scan(&scopes, &active, &keyID)
	if errors.Is(err, pgx.ErrNoRows) {
		return core.ErrForbidden
	}
	if err != nil {
		return err
	}
	if !active || (actor.ClientKeyID != "" && keyID != actor.ClientKeyID) || !slices.Contains(scopes, "tools:read") || (invoke && !slices.Contains(scopes, "tools:invoke")) {
		return core.ErrForbidden
	}
	// Keep the binding stable between this authorization and snapshot/claim.
	// A concurrent version publication must not swap the granted server after
	// this check but before the operation captures its execution destination.
	var lockedID string
	if err := tx.QueryRow(ctx, `SELECT id FROM tools WHERE workspace_id=$1 AND id=$2 FOR SHARE`, actor.WorkspaceID, toolID).Scan(&lockedID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return core.ErrNotFound
		}
		return err
	}
	var granted bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM gateway_client_tool_grants WHERE workspace_id=$1 AND client_id=$2 AND tool_id=$3)
 OR EXISTS(SELECT 1 FROM tools t JOIN gateway_client_server_grants g ON g.workspace_id=t.workspace_id AND g.server_id=t.definition->'mcp'->>'server_id' WHERE t.workspace_id=$1 AND g.client_id=$2 AND t.id=$3)`, actor.WorkspaceID, actor.ClientID, toolID).Scan(&granted)
	if err != nil {
		return err
	}
	if !granted {
		return core.ErrNotFound
	}
	return nil
}

// Human reviewers/executors must also honor the requesting client's current
// grants. Changing to an administrator actor never revives a revoked intent.
func lockOperationClient(ctx context.Context, tx pgx.Tx, executor core.Actor, op core.Operation) error {
	if executor.ClientID != "" && executor.ID == op.ActorID {
		if err := lockClientAccess(ctx, tx, executor, op.ToolID, true); err != nil {
			return err
		}
		return lockSnapshotGrant(ctx, tx, executor.ClientID, op)
	}
	var id string
	err := tx.QueryRow(ctx, `SELECT id FROM gateway_clients WHERE workspace_id=$1 AND id=$2`, op.WorkspaceID, op.ActorID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		if strings.HasPrefix(op.ActorID, "client_") {
			return core.ErrForbidden
		}
		return nil
	}
	if err != nil {
		return err
	}
	if err := lockClientAccess(ctx, tx, core.Actor{ID: id, WorkspaceID: op.WorkspaceID, Role: core.RoleOperator, ClientID: id}, op.ToolID, true); err != nil {
		return err
	}
	return lockSnapshotGrant(ctx, tx, id, op)
}

// LockOperationClient rechecks the requesting client's live grants at a deferred
// execution boundary. Callers must keep this transaction through authorization.
func LockOperationClient(ctx context.Context, tx pgx.Tx, executor core.Actor, op core.Operation) error {
	return lockOperationClient(ctx, tx, executor, op)
}

func lockSnapshotGrant(ctx context.Context, tx pgx.Tx, clientID string, op core.Operation) error {
	var granted bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM gateway_client_tool_grants WHERE workspace_id=$1 AND client_id=$2 AND tool_id=$3)
 OR EXISTS(SELECT 1 FROM gateway_client_server_grants g JOIN operations o ON o.workspace_id=g.workspace_id AND o.tool_snapshot->'mcp'->>'server_id'=g.server_id WHERE o.workspace_id=$1 AND g.client_id=$2 AND o.id=$4)`, op.WorkspaceID, clientID, op.ToolID, op.ID).Scan(&granted)
	if err != nil {
		return err
	}
	if !granted {
		return core.ErrNotFound
	}
	return nil
}
