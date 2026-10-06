package upstreamoauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/alan1-666/mcp-gateway/internal/identity"
	"github.com/alan1-666/mcp-gateway/internal/store/postgres"
	"github.com/jackc/pgx/v5"
)

type row struct {
	Version                 int64
	Status                  string
	Config                  storedConfig
	Secret, Token, Verifier []byte
	Expires, Deadline       *time.Time
	State, Session, Actor   *string
	Updated, Now            time.Time
}

func (s *Service) server(ctx context.Context, w, id string) (core.MCPServer, error) {
	var v core.MCPServer
	v.ID, v.WorkspaceID = id, w
	err := s.db.QueryRow(ctx, `SELECT url,credential_ref,enabled,COALESCE(connector_id,'') FROM mcp_servers WHERE workspace_id=$1 AND id=$2`, w, id).Scan(&v.URL, &v.CredentialRef, &v.Enabled, &v.ConnectorID)
	if errors.Is(err, pgx.ErrNoRows) {
		err = core.ErrNotFound
	}
	return v, err
}
func read(ctx context.Context, db postgres.DB, w, id string) (row, error) {
	var v row
	var raw []byte
	err := db.QueryRow(ctx, `SELECT version,status,configuration,client_secret,token,expires_at,state_hash,session_hash,actor_id,verifier,deadline,updated_at,clock_timestamp() FROM mcp_upstream_oauth WHERE workspace_id=$1 AND server_id=$2`, w, id).Scan(&v.Version, &v.Status, &raw, &v.Secret, &v.Token, &v.Expires, &v.State, &v.Session, &v.Actor, &v.Verifier, &v.Deadline, &v.Updated, &v.Now)
	if errors.Is(err, pgx.ErrNoRows) {
		return row{}, nil
	}
	if err != nil {
		return v, err
	}
	if json.Unmarshal(raw, &v.Config) != nil {
		return v, fmt.Errorf("OAuth configuration is unreadable")
	}
	return v, nil
}

// Server row locks serialize initial creation as well as edits, disable,
// exchanges and refresh claims across all gateway processes.
func (s *Service) locked(ctx context.Context, a core.Actor, id string, manage bool, fn func(pgx.Tx, core.MCPServer, row) error) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	if manage {
		if err = administrator(ctx, a); err != nil {
			return err
		}
		if err = identity.LockClientAdministrator(ctx, tx); err != nil {
			return err
		}
	}
	server := core.MCPServer{ID: id, WorkspaceID: a.WorkspaceID}
	err = tx.QueryRow(ctx, `SELECT url,credential_ref,enabled,COALESCE(connector_id,'') FROM mcp_servers WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, a.WorkspaceID, id).Scan(&server.URL, &server.CredentialRef, &server.Enabled, &server.ConnectorID)
	if errors.Is(err, pgx.ErrNoRows) {
		return core.ErrNotFound
	}
	if err != nil {
		return err
	}
	v, err := read(ctx, tx, a.WorkspaceID, id)
	if err != nil {
		return err
	}
	if err = fn(tx, server, v); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func audit(ctx context.Context, tx pgx.Tx, a core.Actor, id, action, status string, version int64) error {
	raw, _ := json.Marshal(map[string]any{"status": status, "version": version})
	_, err := tx.Exec(ctx, `INSERT INTO audit_events(workspace_id,actor_id,action,resource_id,data) VALUES($1,$2,$3,$4,$5)`, a.WorkspaceID, a.ID, action, id, raw)
	return err
}
func (s *Service) status(id string, v row) Status {
	result := Status{ServerID: id, Version: v.Version, Status: v.Status, RedirectURI: s.redirect}
	if v.Version == 0 {
		result.Status = "unconfigured"
		return result
	}
	result.Configuration = &v.Config.Configuration
	result.ExpiresAt = v.Expires
	result.UpdatedAt = &v.Updated
	if v.Deadline != nil && !v.Now.Before(*v.Deadline) && (v.Status == "pending" || v.Status == "exchanging" || v.Status == "refreshing") {
		result.Status = "reconnect_required"
	}
	return result
}
func (s *Service) Status(ctx context.Context, a core.Actor, id string) (Status, error) {
	if err := administrator(ctx, a); err != nil {
		return Status{}, err
	}
	if _, err := s.server(ctx, a.WorkspaceID, id); err != nil {
		return Status{}, err
	}
	v, err := read(ctx, s.db, a.WorkspaceID, id)
	result := s.status(id, v)
	if err == nil && v.Status == "connected" && v.Expires != nil && !v.Now.Before(*v.Expires) {
		raw, e := s.open(a.WorkspaceID, id, "token", v.Token)
		defer clear(raw)
		var token grant
		if e != nil || json.Unmarshal(raw, &token) != nil || token.RefreshToken == "" {
			result.Status = "reconnect_required"
		}
	}
	return result, err
}
func versionMatches(expected *int64, v row) error {
	if expected == nil || *expected < 0 {
		return fmt.Errorf("%w: expected_version is required", core.ErrInvalid)
	}
	if *expected != v.Version {
		return fmt.Errorf("%w: OAuth connection changed; reload before retrying", core.ErrConflict)
	}
	return nil
}
func (s *Service) Configure(ctx context.Context, a core.Actor, id string, in ConfigInput) (Status, error) {
	if err := administrator(ctx, a); err != nil {
		return Status{}, err
	}
	if in.ExpectedVersion == nil || *in.ExpectedVersion < 0 || in.Issuer == "" || strings.TrimSpace(in.ClientID) == "" || len(in.ClientID) > 2048 || strings.ContainsAny(in.ClientID, "\x00\r\n") || len(in.ClientSecret) > 8192 || strings.ContainsAny(in.ClientSecret, "\x00\r\n") || len(in.Scopes) > 64 {
		return Status{}, core.ErrInvalid
	}
	if (in.AuthMethod == "none" && in.ClientSecret != "") || (in.AuthMethod == "client_secret_basic" && in.ClientSecret == "") {
		return Status{}, fmt.Errorf("%w: client authentication and secret do not match", core.ErrInvalid)
	}
	seen := map[string]bool{}
	for _, scope := range in.Scopes {
		if !validScope(scope) || seen[scope] {
			return Status{}, core.ErrInvalid
		}
		seen[scope] = true
	}
	if in.Scopes == nil {
		in.Scopes = []string{}
	}
	// Check the local version before any outbound discovery, then again when committing.
	current, err := s.Status(ctx, a, id)
	if err != nil {
		return Status{}, err
	}
	if current.Version != *in.ExpectedVersion {
		return Status{}, fmt.Errorf("%w: OAuth connection changed; reload before retrying", core.ErrConflict)
	}
	metadata, err := s.Discover(ctx, a, id, in.Issuer)
	if err != nil {
		return Status{}, err
	}
	if !slices.Contains(metadata.AuthMethods, in.AuthMethod) {
		return Status{}, fmt.Errorf("%w: client authentication method is unsupported", core.ErrInvalid)
	}
	cfg := storedConfig{Configuration: in.Configuration, Resource: metadata.Resource, AuthorizationEndpoint: metadata.AuthorizationEndpoint, TokenEndpoint: metadata.TokenEndpoint}
	raw, _ := json.Marshal(cfg)
	var encrypted []byte
	if in.ClientSecret != "" {
		encrypted = s.seal(a.WorkspaceID, id, "client_secret", []byte(in.ClientSecret))
	}
	err = s.locked(ctx, a, id, true, func(tx pgx.Tx, server core.MCPServer, v row) error {
		if e := versionMatches(in.ExpectedVersion, v); e != nil {
			return e
		}
		if server.ConnectorID != "" || !server.Enabled || server.CredentialRef != "" || server.URL != metadata.Resource {
			return core.ErrConflict
		}
		_, e := tx.Exec(ctx, `INSERT INTO mcp_upstream_oauth(workspace_id,server_id,version,status,configuration,client_secret) VALUES($1,$2,1,'disconnected',$3,$4) ON CONFLICT(workspace_id,server_id) DO UPDATE SET version=mcp_upstream_oauth.version+1,status='disconnected',configuration=$3,client_secret=$4,token=NULL,expires_at=NULL,state_hash=NULL,session_hash=NULL,actor_id=NULL,verifier=NULL,deadline=NULL,updated_at=clock_timestamp()`, a.WorkspaceID, id, raw, encrypted)
		if e != nil {
			return e
		}
		return audit(ctx, tx, a, id, "MCP_OAUTH_CONFIGURED", "disconnected", v.Version+1)
	})
	if err != nil {
		return Status{}, err
	}
	return s.Status(ctx, a, id)
}
func (s *Service) Disconnect(ctx context.Context, a core.Actor, id string, in VersionInput) (Status, error) {
	err := s.locked(ctx, a, id, true, func(tx pgx.Tx, server core.MCPServer, v row) error {
		if e := versionMatches(in.ExpectedVersion, v); e != nil {
			return e
		}
		if v.Version == 0 {
			return core.ErrNotFound
		}
		_, e := tx.Exec(ctx, `UPDATE mcp_upstream_oauth SET version=version+1,status='disconnected',token=NULL,expires_at=NULL,state_hash=NULL,session_hash=NULL,actor_id=NULL,verifier=NULL,deadline=NULL,updated_at=clock_timestamp() WHERE workspace_id=$1 AND server_id=$2`, a.WorkspaceID, id)
		if e != nil {
			return e
		}
		return audit(ctx, tx, a, id, "MCP_OAUTH_DISCONNECTED", "disconnected", v.Version+1)
	})
	if err != nil {
		return Status{}, err
	}
	return s.Status(ctx, a, id)
}
