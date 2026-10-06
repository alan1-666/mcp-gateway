// Package connectors implements a durable, non-replaying work queue for outbound
// private-network agents. Connector credentials are separate from client keys.
package connectors

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/alan1-666/mcp-gateway/internal/connectorwire"
	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/alan1-666/mcp-gateway/internal/identity"
	"github.com/alan1-666/mcp-gateway/internal/store/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const MaxConnectors = 100
const MaxActiveJobs = 100
const MaxResultBytes = 4 << 20

type Service struct{ db postgres.DB }

func New(db postgres.DB) *Service { return &Service{db: db} }

func admin(a core.Actor) error {
	if a.ID == "" || a.WorkspaceID == "" || a.Role != core.RoleAdmin || a.ClientID != "" {
		return core.ErrForbidden
	}
	return nil
}
func digest(v string) string { sum := sha256.Sum256([]byte(v)); return hex.EncodeToString(sum[:]) }
func mapError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return core.ErrNotFound
	}
	var p *pgconn.PgError
	if errors.As(err, &p) && p.Code == "23505" {
		return core.ErrConflict
	}
	return err
}
func transaction[T any](ctx context.Context, db postgres.DB, fn func(pgx.Tx) (T, error)) (T, error) {
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
	if err = tx.Commit(ctx); err != nil {
		return zero, mapError(err)
	}
	return value, nil
}

const columns = `id,workspace_id,name,enabled,targets,last_seen_at,created_at`

func scan(row interface{ Scan(...any) error }) (connectorwire.Connector, error) {
	var c connectorwire.Connector
	var raw []byte
	err := row.Scan(&c.ID, &c.WorkspaceID, &c.Name, &c.Enabled, &raw, &c.LastSeenAt, &c.CreatedAt)
	c.Targets = []connectorwire.Target{}
	if err == nil && len(raw) > 0 {
		err = json.Unmarshal(raw, &c.Targets)
	}
	return c, mapError(err)
}
func audit(ctx context.Context, tx pgx.Tx, a core.Actor, id, action string) error {
	_, err := tx.Exec(ctx, `INSERT INTO audit_events(workspace_id,actor_id,action,resource_id) VALUES($1,$2,$3,$4)`, a.WorkspaceID, a.ID, action, id)
	return err
}
func (s *Service) List(ctx context.Context, a core.Actor) ([]connectorwire.Connector, error) {
	if err := admin(a); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `SELECT `+columns+` FROM gateway_connectors WHERE workspace_id=$1 ORDER BY created_at DESC,id LIMIT $2`, a.WorkspaceID, MaxConnectors)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []connectorwire.Connector{}
	for rows.Next() {
		c, e := scan(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
func (s *Service) Create(ctx context.Context, a core.Actor, name string) (connectorwire.Created, error) {
	if err := admin(a); err != nil {
		return connectorwire.Created{}, err
	}
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 120 || !utf8.ValidString(name) || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return connectorwire.Created{}, core.ErrInvalid
	}
	return transaction(ctx, s.db, func(tx pgx.Tx) (connectorwire.Created, error) {
		if err := identity.LockClientAdministrator(ctx, tx); err != nil {
			return connectorwire.Created{}, err
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, a.WorkspaceID+"\x1fconnectors"); err != nil {
			return connectorwire.Created{}, err
		}
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM gateway_connectors WHERE workspace_id=$1`, a.WorkspaceID).Scan(&count); err != nil {
			return connectorwire.Created{}, err
		}
		if count >= MaxConnectors {
			return connectorwire.Created{}, fmt.Errorf("%w: connector limit reached", core.ErrConflict)
		}
		secret := make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			return connectorwire.Created{}, err
		}
		token := "rgc_" + base64.RawURLEncoding.EncodeToString(secret)
		c, err := scan(tx.QueryRow(ctx, `INSERT INTO gateway_connectors(workspace_id,id,name,token_hash) VALUES($1,$2,$3,$4) RETURNING `+columns, a.WorkspaceID, core.NewID(), name, digest(token)))
		if err != nil {
			return connectorwire.Created{}, err
		}
		return connectorwire.Created{Connector: c, Token: token}, audit(ctx, tx, a, c.ID, "CONNECTOR_CREATED")
	})
}
func (s *Service) Revoke(ctx context.Context, a core.Actor, id string) (connectorwire.Connector, error) {
	if err := admin(a); err != nil {
		return connectorwire.Connector{}, err
	}
	return transaction(ctx, s.db, func(tx pgx.Tx) (connectorwire.Connector, error) {
		if err := identity.LockClientAdministrator(ctx, tx); err != nil {
			return connectorwire.Connector{}, err
		}
		c, err := scan(tx.QueryRow(ctx, `SELECT `+columns+` FROM gateway_connectors WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, a.WorkspaceID, id))
		if err != nil {
			return c, err
		}
		if !c.Enabled {
			return c, nil
		}
		if _, err = tx.Exec(ctx, `UPDATE gateway_connectors SET enabled=false WHERE workspace_id=$1 AND id=$2`, a.WorkspaceID, id); err != nil {
			return c, err
		}
		if _, err = tx.Exec(ctx, `UPDATE gateway_connector_jobs SET state='expired',updated_at=clock_timestamp() WHERE workspace_id=$1 AND connector_id=$2 AND state IN ('queued','claimed','started')`, a.WorkspaceID, id); err != nil {
			return c, err
		}
		c.Enabled = false
		return c, audit(ctx, tx, a, id, "CONNECTOR_REVOKED")
	})
}

// A token lookup never returns workspace or identity hints for an invalid token.
// FOR UPDATE serializes poll/claim/start/result and permanent revocation.
func authenticate(ctx context.Context, tx pgx.Tx, token string) (connectorwire.Connector, error) {
	if !strings.HasPrefix(token, "rgc_") || len(token) != 47 {
		return connectorwire.Connector{}, core.ErrUnauthorized
	}
	c, err := scan(tx.QueryRow(ctx, `SELECT `+columns+` FROM gateway_connectors WHERE token_hash=$1 AND enabled FOR UPDATE`, digest(token)))
	if errors.Is(err, core.ErrNotFound) {
		err = core.ErrUnauthorized
	}
	return c, err
}

var targetName = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

func targets(in []connectorwire.Target) ([]connectorwire.Target, error) {
	if len(in) == 0 || len(in) > 32 {
		return nil, core.ErrInvalid
	}
	out := append([]connectorwire.Target(nil), in...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	for i, t := range out {
		raw, e := hex.DecodeString(t.Fingerprint)
		if !targetName.MatchString(t.Name) || e != nil || len(raw) != 32 || t.Fingerprint != strings.ToLower(t.Fingerprint) || (t.Transport != "http" && t.Transport != "stdio") || (i > 0 && t.Name == out[i-1].Name) {
			return nil, core.ErrInvalid
		}
	}
	return out, nil
}
func targetFor(c connectorwire.Connector, name string) (connectorwire.Target, error) {
	for _, t := range c.Targets {
		if t.Name == name {
			return t, nil
		}
	}
	return connectorwire.Target{}, fmt.Errorf("%w: connector target is not registered", core.ErrConflict)
}
func (s *Service) ValidateServer(a core.Actor, server core.MCPServer) error {
	if a.WorkspaceID == "" || server.WorkspaceID != a.WorkspaceID || server.ConnectorID == "" || !targetName.MatchString(server.TargetName) || server.URL != "" || server.CredentialRef != "" || server.TimeoutMS < 100 || server.TimeoutMS > 120000 {
		return core.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := scan(s.db.QueryRow(ctx, `SELECT `+columns+` FROM gateway_connectors WHERE workspace_id=$1 AND id=$2 AND enabled`, a.WorkspaceID, server.ConnectorID))
	if err != nil {
		return err
	}
	_, err = targetFor(c, server.TargetName)
	return err
}

// Cleanup expires abandoned work, erases sensitive task/result payloads after one
// day, and keeps execution tombstones permanently to fence operation replay.
// Each pass touches at most 100 rows per class and is safe across workers.
func (s *Service) Cleanup(ctx context.Context) (int, error) {
	return transaction(ctx, s.db, func(tx pgx.Tx) (int, error) {
		n := 0
		for _, q := range []string{
			`WITH old AS (SELECT workspace_id,id FROM gateway_connector_jobs WHERE state IN ('queued','claimed','started') AND deadline<=clock_timestamp() ORDER BY deadline LIMIT 100 FOR UPDATE SKIP LOCKED) UPDATE gateway_connector_jobs j SET state='expired',updated_at=clock_timestamp() FROM old WHERE j.workspace_id=old.workspace_id AND j.id=old.id`,
			`WITH old AS (SELECT workspace_id,id FROM gateway_connector_jobs WHERE kind='discover' AND state IN ('completed','expired') AND updated_at<clock_timestamp()-interval '1 day' ORDER BY updated_at LIMIT 100 FOR UPDATE SKIP LOCKED) DELETE FROM gateway_connector_jobs j USING old WHERE j.workspace_id=old.workspace_id AND j.id=old.id`,
			`WITH old AS (SELECT workspace_id,id FROM gateway_connector_jobs WHERE kind='execute' AND payload IS NOT NULL AND state IN ('completed','expired') AND updated_at<clock_timestamp()-interval '1 day' ORDER BY updated_at LIMIT 100 FOR UPDATE SKIP LOCKED) UPDATE gateway_connector_jobs j SET payload=NULL,result=NULL,actor='{}'::jsonb,client_key_id='' FROM old WHERE j.workspace_id=old.workspace_id AND j.id=old.id`,
		} {
			tag, err := tx.Exec(ctx, q)
			if err != nil {
				return n, err
			}
			n += int(tag.RowsAffected())
		}
		return n, nil
	})
}
