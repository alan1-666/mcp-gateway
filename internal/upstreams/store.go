package upstreams

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/alan1-666/mcp-gateway/internal/store/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type Store struct{ db postgres.DB }

func NewStore(db postgres.DB) *Store { return &Store{db: db} }

const columns = `id,workspace_id,name,namespace,url,credential_ref,timeout_ms,enabled,created_at,updated_at`

func scan(row interface{ Scan(...any) error }) (core.MCPServer, error) {
	var v core.MCPServer
	err := row.Scan(&v.ID, &v.WorkspaceID, &v.Name, &v.Namespace, &v.URL, &v.CredentialRef, &v.TimeoutMS, &v.Enabled, &v.CreatedAt, &v.UpdatedAt)
	return v, mapError(err)
}
func mapError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return core.ErrNotFound
	}
	var e *pgconn.PgError
	if errors.As(err, &e) && e.Code == "23505" {
		return core.ErrConflict
	}
	return err
}
func withTx[T any](ctx context.Context, db postgres.DB, fn func(pgx.Tx) (T, error)) (T, error) {
	var zero T
	tx, err := db.Begin(ctx)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback(context.Background())
	value, err := fn(tx)
	if err != nil {
		return zero, mapError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return zero, mapError(err)
	}
	return value, nil
}
func audit(ctx context.Context, tx pgx.Tx, actor core.Actor, action, id string, data any) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO audit_events(workspace_id,actor_id,action,resource_id,data) VALUES($1,$2,$3,$4,$5)`, actor.WorkspaceID, actor.ID, action, id, raw)
	return err
}
func (s *Store) GetServer(ctx context.Context, workspaceID, id string) (core.MCPServer, error) {
	return scan(s.db.QueryRow(ctx, `SELECT `+columns+` FROM mcp_servers WHERE workspace_id=$1 AND id=$2`, workspaceID, id))
}
func (s *Store) create(ctx context.Context, actor core.Actor, in core.MCPServerInput) (core.MCPServer, error) {
	return withTx(ctx, s.db, func(tx pgx.Tx) (core.MCPServer, error) {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, actor.WorkspaceID+"\x1fmcp-servers"); err != nil {
			return core.MCPServer{}, err
		}
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM mcp_servers WHERE workspace_id=$1`, actor.WorkspaceID).Scan(&count); err != nil {
			return core.MCPServer{}, err
		}
		if count >= MaxServers {
			return core.MCPServer{}, fmt.Errorf("%w: workspace supports at most %d MCP servers", core.ErrConflict, MaxServers)
		}
		v, err := scan(tx.QueryRow(ctx, `INSERT INTO mcp_servers(workspace_id,id,name,namespace,url,credential_ref,timeout_ms) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING `+columns, actor.WorkspaceID, core.NewID(), in.Name, in.Namespace, in.URL, in.CredentialRef, in.TimeoutMS))
		if err != nil {
			return v, err
		}
		return v, audit(ctx, tx, actor, "MCP_SERVER_CREATED", v.ID, map[string]any{"namespace": v.Namespace})
	})
}
func (s *Store) list(ctx context.Context, workspaceID string) ([]core.MCPServer, error) {
	rows, err := s.db.Query(ctx, `SELECT `+columns+` FROM mcp_servers WHERE workspace_id=$1 ORDER BY created_at DESC,id DESC LIMIT $2`, workspaceID, MaxServers+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []core.MCPServer{}
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(values) > MaxServers {
		return nil, fmt.Errorf("%w: MCP server count exceeds supported capacity", core.ErrConflict)
	}
	return values, nil
}
func (s *Store) setEnabled(ctx context.Context, actor core.Actor, id string, enabled bool) (core.MCPServer, error) {
	return withTx(ctx, s.db, func(tx pgx.Tx) (core.MCPServer, error) {
		v, err := scan(tx.QueryRow(ctx, `SELECT `+columns+` FROM mcp_servers WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, actor.WorkspaceID, id))
		if err != nil {
			return v, err
		}
		if v.Enabled == enabled {
			return v, nil
		}
		v, err = scan(tx.QueryRow(ctx, `UPDATE mcp_servers SET enabled=$3,updated_at=clock_timestamp() WHERE workspace_id=$1 AND id=$2 RETURNING `+columns, actor.WorkspaceID, id, enabled))
		if err != nil {
			return v, err
		}
		if !enabled {
			_, err = tx.Exec(ctx, `UPDATE mcp_upstream_oauth SET version=version+1,status='reconnect_required',token=NULL,expires_at=NULL,state_hash=NULL,session_hash=NULL,actor_id=NULL,verifier=NULL,deadline=NULL,updated_at=clock_timestamp() WHERE workspace_id=$1 AND server_id=$2`, actor.WorkspaceID, id)
			if err != nil {
				return v, err
			}
		}
		return v, audit(ctx, tx, actor, "MCP_SERVER_ENABLED_CHANGED", id, map[string]any{"enabled": enabled})
	})
}
func (s *Store) importTool(ctx context.Context, actor core.Actor, in core.ToolInput) (core.Tool, error) {
	return withTx(ctx, s.db, func(tx pgx.Tx) (core.Tool, error) {
		// A share lock fences concurrent disable; the advisory lock makes retries
		// idempotent without exposing an aborted unique-constraint transaction.
		var enabled bool
		if err := tx.QueryRow(ctx, `SELECT enabled FROM mcp_servers WHERE workspace_id=$1 AND id=$2 FOR SHARE`, actor.WorkspaceID, in.MCP.ServerID).Scan(&enabled); err != nil {
			return core.Tool{}, err
		}
		if !enabled {
			return core.Tool{}, fmt.Errorf("%w: MCP server is disabled", core.ErrConflict)
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, actor.WorkspaceID+"\x1fmcp-import\x1f"+in.MCP.ServerID+"\x1f"+in.MCP.ToolName); err != nil {
			return core.Tool{}, err
		}
		var id string
		err := tx.QueryRow(ctx, `SELECT id FROM tools WHERE workspace_id=$1 AND definition->'mcp'->>'server_id'=$2 AND definition->'mcp'->>'tool_name'=$3`, actor.WorkspaceID, in.MCP.ServerID, in.MCP.ToolName).Scan(&id)
		repo := postgres.New(tx)
		if err == nil {
			existing, err := repo.GetTool(ctx, actor, id)
			if err != nil {
				return core.Tool{}, err
			}
			oldPolicy, _ := json.Marshal(existing.ResponsePolicy)
			newPolicy, _ := json.Marshal(in.ResponsePolicy)
			if existing.MCP == nil || existing.MCP.SchemaHash != in.MCP.SchemaHash || existing.Risk != in.Risk || !bytes.Equal(oldPolicy, newPolicy) {
				return core.Tool{}, fmt.Errorf("%w: upstream tool is already imported with a different contract; existing tool was preserved", core.ErrConflict)
			}
			return existing, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return core.Tool{}, err
		}
		tool, err := repo.CreateTool(ctx, actor, in)
		if err != nil {
			return tool, err
		}
		return tool, audit(ctx, tx, actor, "MCP_TOOL_IMPORTED", tool.ID, map[string]any{"server_id": in.MCP.ServerID, "schema_hash": in.MCP.SchemaHash})
	})
}
