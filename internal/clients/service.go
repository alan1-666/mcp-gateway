// Package clients manages independent machine identities. Tokens are returned
// only by create/rotate; all durable records contain a SHA-256 digest instead.
package clients

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/alan1-666/mcp-gateway/internal/identity"
	"github.com/alan1-666/mcp-gateway/internal/store/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const MaxClients = 500
const MaxToolGrants = 1000
const MaxServerGrants = 100

const ScopeRead = "tools:read"
const ScopeInvoke = "tools:invoke"

type Client struct {
	ID           string    `json:"id"`
	WorkspaceID  string    `json:"workspace_id"`
	Name         string    `json:"name"`
	Enabled      bool      `json:"enabled"`
	Version      int       `json:"version"`
	Scopes       []string  `json:"scopes"`
	ServerIDs    []string  `json:"server_ids"`
	ToolIDs      []string  `json:"tool_ids"`
	KeyID        string    `json:"key_id"`
	KeyCreatedAt time.Time `json:"key_created_at"`
	KeyExpiresAt time.Time `json:"key_expires_at"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}
type IssuedKey struct {
	Client Client `json:"client"`
	APIKey string `json:"api_key"`
}
type CreateInput struct {
	Name      string     `json:"name"`
	Scopes    []string   `json:"scopes"`
	ServerIDs []string   `json:"server_ids"`
	ToolIDs   []string   `json:"tool_ids"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}
type UpdateInput struct {
	ExpectedVersion int       `json:"expected_version"`
	Name            *string   `json:"name,omitempty"`
	Enabled         *bool     `json:"enabled,omitempty"`
	Scopes          *[]string `json:"scopes,omitempty"`
	ServerIDs       *[]string `json:"server_ids,omitempty"`
	ToolIDs         *[]string `json:"tool_ids,omitempty"`
}
type RotateInput struct {
	ExpectedVersion int        `json:"expected_version"`
	ExpiresAt       *time.Time `json:"expires_at,omitempty"`
}
type Service struct{ db postgres.DB }

func New(db postgres.DB) *Service { return &Service{db: db} }

func authorize(actor core.Actor) error {
	if actor.ID == "" || actor.WorkspaceID == "" {
		return core.ErrUnauthorized
	}
	if actor.Role != core.RoleAdmin || actor.ClientID != "" {
		return core.ErrForbidden
	}
	return nil
}
func invalid(message string) error { return fmt.Errorf("%w: %s", core.ErrInvalid, message) }
func normalize(name string, scopes, serverIDs, toolIDs []string) (string, []string, []string, []string, error) {
	name = strings.TrimSpace(name)
	if !utf8.ValidString(name) || utf8.RuneCountInString(name) < 1 || utf8.RuneCountInString(name) > 120 || strings.ContainsAny(name, "\x00\r\n") {
		return "", nil, nil, nil, invalid("name must contain 1–120 characters without line breaks")
	}
	norm := func(values []string, max int) ([]string, error) {
		if len(values) > max {
			return nil, invalid("too many grants")
		}
		result := make([]string, 0, len(values))
		for _, value := range values {
			if value == "" || len(value) > 128 || value != strings.TrimSpace(value) || !utf8.ValidString(value) || strings.ContainsAny(value, "\x00\r\n") {
				return nil, invalid("invalid scope or grant identifier")
			}
			result = append(result, value)
		}
		slices.Sort(result)
		return slices.Compact(result), nil
	}
	var err error
	if scopes, err = norm(scopes, 2); err != nil {
		return "", nil, nil, nil, err
	}
	for _, scope := range scopes {
		if scope != ScopeRead && scope != ScopeInvoke {
			return "", nil, nil, nil, invalid("supported scopes are tools:read and tools:invoke")
		}
	}
	if slices.Contains(scopes, ScopeInvoke) && !slices.Contains(scopes, ScopeRead) {
		return "", nil, nil, nil, invalid("tools:invoke also requires tools:read")
	}
	if serverIDs, err = norm(serverIDs, MaxServerGrants); err != nil {
		return "", nil, nil, nil, err
	}
	if toolIDs, err = norm(toolIDs, MaxToolGrants); err != nil {
		return "", nil, nil, nil, err
	}
	return name, scopes, serverIDs, toolIDs, nil
}
func expiry(in *time.Time) (time.Time, error) {
	now := time.Now().UTC()
	if in == nil {
		return now.Add(30 * 24 * time.Hour), nil
	}
	if !in.After(now) || in.After(now.Add(366*24*time.Hour)) {
		return time.Time{}, invalid("expires_at must be in the future and within 366 days")
	}
	return in.UTC(), nil
}
func issueToken() (string, []byte, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", nil, err
	}
	token := "mgc_" + base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(token))
	return token, sum[:], nil
}
func mapError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return core.ErrNotFound
	}
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		if pe.Code == "23505" {
			return fmt.Errorf("%w: client name or version conflicts with an existing record", core.ErrConflict)
		}
		if pe.Code == "23503" {
			return invalid("a grant refers to a missing resource")
		}
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
	result, err := fn(tx)
	if err != nil {
		return zero, mapError(err)
	}
	if err = tx.Commit(ctx); err != nil {
		return zero, mapError(err)
	}
	return result, nil
}

const columns = `c.id,c.workspace_id,c.name,c.enabled,c.version,c.scopes,c.key_id,c.key_created_at,c.key_expires_at,c.created_at,c.updated_at,
 ARRAY(SELECT g.server_id FROM gateway_client_server_grants g WHERE g.workspace_id=c.workspace_id AND g.client_id=c.id ORDER BY g.server_id),
 ARRAY(SELECT g.tool_id FROM gateway_client_tool_grants g WHERE g.workspace_id=c.workspace_id AND g.client_id=c.id ORDER BY g.tool_id)`

func scan(row interface{ Scan(...any) error }) (Client, error) {
	var c Client
	err := row.Scan(&c.ID, &c.WorkspaceID, &c.Name, &c.Enabled, &c.Version, &c.Scopes, &c.KeyID, &c.KeyCreatedAt, &c.KeyExpiresAt, &c.CreatedAt, &c.UpdatedAt, &c.ServerIDs, &c.ToolIDs)
	return c, mapError(err)
}
func get(ctx context.Context, db postgres.DB, workspace, id string, lock bool) (Client, error) {
	suffix := ""
	if lock {
		suffix = " FOR UPDATE OF c"
	}
	return scan(db.QueryRow(ctx, `SELECT `+columns+` FROM gateway_clients c WHERE c.workspace_id=$1 AND c.id=$2`+suffix, workspace, id))
}
func audit(ctx context.Context, tx pgx.Tx, actor core.Actor, action string, before *Client, after Client) error {
	data := map[string]any{"after": after}
	if before != nil {
		data["before"] = before
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO audit_events(workspace_id,actor_id,action,resource_id,data) VALUES($1,$2,$3,$4,$5)`, actor.WorkspaceID, actor.ID, action, after.ID, raw)
	return err
}
func replaceGrants(ctx context.Context, tx pgx.Tx, workspace, id string, servers, tools []string) error {
	for _, kind := range []struct {
		table, resource, column string
		ids                     []string
	}{{"gateway_client_server_grants", "mcp_servers", "server_id", servers}, {"gateway_client_tool_grants", "tools", "tool_id", tools}} {
		var found int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM `+kind.resource+` WHERE workspace_id=$1 AND id=ANY($2::text[])`, workspace, kind.ids).Scan(&found); err != nil {
			return err
		}
		if found != len(kind.ids) {
			return invalid("all grants must reference resources in this workspace")
		}
		if _, err := tx.Exec(ctx, `DELETE FROM `+kind.table+` WHERE workspace_id=$1 AND client_id=$2`, workspace, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO `+kind.table+`(workspace_id,client_id,`+kind.column+`) SELECT $1,$2,unnest($3::text[])`, workspace, id, kind.ids); err != nil {
			return err
		}
	}
	return nil
}
func (s *Service) List(ctx context.Context, actor core.Actor) ([]Client, error) {
	if err := authorize(actor); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `SELECT `+columns+` FROM gateway_clients c WHERE c.workspace_id=$1 ORDER BY c.created_at DESC,c.id DESC LIMIT $2`, actor.WorkspaceID, MaxClients+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Client{}
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, v)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(items) > MaxClients {
		return nil, fmt.Errorf("%w: client count exceeds supported capacity", core.ErrConflict)
	}
	return items, nil
}
func (s *Service) Create(ctx context.Context, actor core.Actor, in CreateInput) (IssuedKey, error) {
	if err := authorize(actor); err != nil {
		return IssuedKey{}, err
	}
	name, scopes, servers, tools, err := normalize(in.Name, in.Scopes, in.ServerIDs, in.ToolIDs)
	if err != nil {
		return IssuedKey{}, err
	}
	expires, err := expiry(in.ExpiresAt)
	if err != nil {
		return IssuedKey{}, err
	}
	token, hash, err := issueToken()
	if err != nil {
		return IssuedKey{}, err
	}
	return withTx(ctx, s.db, func(tx pgx.Tx) (IssuedKey, error) {
		if err := identity.LockClientAdministrator(ctx, tx); err != nil {
			return IssuedKey{}, err
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, actor.WorkspaceID+"\x1fclients"); err != nil {
			return IssuedKey{}, err
		}
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM gateway_clients WHERE workspace_id=$1`, actor.WorkspaceID).Scan(&count); err != nil {
			return IssuedKey{}, err
		}
		if count >= MaxClients {
			return IssuedKey{}, fmt.Errorf("%w: at most %d clients per workspace", core.ErrConflict, MaxClients)
		}
		id := "client_" + core.NewID()
		if _, err := tx.Exec(ctx, `INSERT INTO gateway_clients(id,workspace_id,name,scopes,key_id,token_hash,key_expires_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, id, actor.WorkspaceID, name, scopes, core.NewID(), hash, expires); err != nil {
			return IssuedKey{}, err
		}
		if err := replaceGrants(ctx, tx, actor.WorkspaceID, id, servers, tools); err != nil {
			return IssuedKey{}, err
		}
		client, err := get(ctx, tx, actor.WorkspaceID, id, false)
		if err != nil {
			return IssuedKey{}, err
		}
		if err = audit(ctx, tx, actor, "CLIENT_CREATED", nil, client); err != nil {
			return IssuedKey{}, err
		}
		return IssuedKey{Client: client, APIKey: token}, nil
	})
}
func (s *Service) Update(ctx context.Context, actor core.Actor, id string, in UpdateInput) (Client, error) {
	if err := authorize(actor); err != nil {
		return Client{}, err
	}
	if in.ExpectedVersion < 1 {
		return Client{}, invalid("expected_version is required")
	}
	return withTx(ctx, s.db, func(tx pgx.Tx) (Client, error) {
		if err := identity.LockClientAdministrator(ctx, tx); err != nil {
			return Client{}, err
		}
		before, err := get(ctx, tx, actor.WorkspaceID, id, true)
		if err != nil {
			return Client{}, err
		}
		if before.Version != in.ExpectedVersion {
			return Client{}, fmt.Errorf("%w: client was changed; reload before editing", core.ErrConflict)
		}
		next := before
		if in.Name != nil {
			next.Name = *in.Name
		}
		if in.Enabled != nil {
			next.Enabled = *in.Enabled
		}
		if in.Scopes != nil {
			next.Scopes = *in.Scopes
		}
		if in.ServerIDs != nil {
			next.ServerIDs = *in.ServerIDs
		}
		if in.ToolIDs != nil {
			next.ToolIDs = *in.ToolIDs
		}
		name, scopes, servers, tools, err := normalize(next.Name, next.Scopes, next.ServerIDs, next.ToolIDs)
		if err != nil {
			return Client{}, err
		}
		if err = replaceGrants(ctx, tx, actor.WorkspaceID, id, servers, tools); err != nil {
			return Client{}, err
		}
		if _, err = tx.Exec(ctx, `UPDATE gateway_clients SET name=$3,enabled=$4,scopes=$5,version=version+1,updated_at=clock_timestamp() WHERE workspace_id=$1 AND id=$2`, actor.WorkspaceID, id, name, next.Enabled, scopes); err != nil {
			return Client{}, err
		}
		next, err = get(ctx, tx, actor.WorkspaceID, id, false)
		if err != nil {
			return Client{}, err
		}
		return next, audit(ctx, tx, actor, "CLIENT_UPDATED", &before, next)
	})
}
func (s *Service) Rotate(ctx context.Context, actor core.Actor, id string, in RotateInput) (IssuedKey, error) {
	if err := authorize(actor); err != nil {
		return IssuedKey{}, err
	}
	if in.ExpectedVersion < 1 {
		return IssuedKey{}, invalid("expected_version is required")
	}
	expires, err := expiry(in.ExpiresAt)
	if err != nil {
		return IssuedKey{}, err
	}
	token, hash, err := issueToken()
	if err != nil {
		return IssuedKey{}, err
	}
	return withTx(ctx, s.db, func(tx pgx.Tx) (IssuedKey, error) {
		if err := identity.LockClientAdministrator(ctx, tx); err != nil {
			return IssuedKey{}, err
		}
		before, err := get(ctx, tx, actor.WorkspaceID, id, true)
		if err != nil {
			return IssuedKey{}, err
		}
		if before.Version != in.ExpectedVersion {
			return IssuedKey{}, fmt.Errorf("%w: client was changed; reload before rotating", core.ErrConflict)
		}
		if _, err = tx.Exec(ctx, `UPDATE gateway_clients SET key_id=$3,token_hash=$4,key_created_at=clock_timestamp(),key_expires_at=$5,version=version+1,updated_at=clock_timestamp() WHERE workspace_id=$1 AND id=$2`, actor.WorkspaceID, id, core.NewID(), hash, expires); err != nil {
			return IssuedKey{}, err
		}
		next, err := get(ctx, tx, actor.WorkspaceID, id, false)
		if err != nil {
			return IssuedKey{}, err
		}
		if err = audit(ctx, tx, actor, "CLIENT_KEY_ROTATED", &before, next); err != nil {
			return IssuedKey{}, err
		}
		return IssuedKey{Client: next, APIKey: token}, nil
	})
}
