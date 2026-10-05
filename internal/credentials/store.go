// Package credentials stores workspace-scoped outbound credentials. Secret
// values leave this package only through the internal adapter resolver.
package credentials

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/alan1-666/mcp-gateway/internal/adapters/httpadapter"
	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/alan1-666/mcp-gateway/internal/store/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const MaxCredentials = 1000

var reference = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,127}$`)
var headerName = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")

type Metadata struct {
	Ref         string    `json:"ref"`
	Origin      string    `json:"origin"`
	Version     int       `json:"version"`
	Enabled     bool      `json:"enabled"`
	HeaderNames []string  `json:"header_names"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}
type Page struct {
	Items []Metadata `json:"items"`
	Total int        `json:"total"`
}
type CreateInput struct {
	Ref     string            `json:"ref"`
	Origin  string            `json:"origin"`
	Headers map[string]string `json:"headers"`
}
type RotateInput struct {
	ExpectedVersion int               `json:"expected_version"`
	Headers         map[string]string `json:"headers"`
}
type EnabledInput struct {
	ExpectedVersion int   `json:"expected_version"`
	Enabled         *bool `json:"enabled"`
}
type Store struct {
	db     postgres.DB
	aead   cipher.AEAD
	policy *httpadapter.Adapter
}

func New(db postgres.DB, masterKey []byte, policy *httpadapter.Adapter) (*Store, error) {
	if len(masterKey) != 32 || db == nil || policy == nil {
		return nil, fmt.Errorf("credential store requires a 32-byte encryption key, database and egress policy")
	}
	block, err := aes.NewCipher(masterKey)
	if err != nil {
		return nil, fmt.Errorf("credential encryption initialization failed")
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("credential encryption initialization failed")
	}
	return &Store{db: db, aead: aead, policy: policy}, nil
}
func admin(a core.Actor) error {
	if a.ID == "" || a.WorkspaceID == "" {
		return core.ErrUnauthorized
	}
	if a.Role != core.RoleAdmin {
		return core.ErrForbidden
	}
	return nil
}
func headers(in map[string]string) (map[string]string, []string, error) {
	bad := func() (map[string]string, []string, error) {
		return nil, nil, fmt.Errorf("%w: provide 1-16 nonempty authentication headers, at most 32768 bytes; protocol and transport headers are reserved", core.ErrInvalid)
	}
	if len(in) == 0 || len(in) > 16 {
		return bad()
	}
	normalized := map[string]string{}
	names := []string{}
	size := 0
	for key, value := range in {
		if len(key) > 128 || !headerName.MatchString(key) || value == "" || len(value) > 8192 {
			return bad()
		}
		for _, c := range value {
			if c < 32 || c == 127 {
				return bad()
			}
		}
		key = http.CanonicalHeaderKey(key)
		switch strings.ToLower(key) {
		case "host", "content-length", "transfer-encoding", "connection", "idempotency-key", "accept", "content-type", "proxy-authorization", "proxy-connection", "te", "trailer", "upgrade":
			return bad()
		}
		if strings.HasPrefix(strings.ToLower(key), "mcp-") {
			return bad()
		}
		if _, ok := normalized[key]; ok {
			return bad()
		}
		normalized[key] = value
		names = append(names, key)
		size += len(key) + len(value)
	}
	if size > 32768 {
		return bad()
	}
	sort.Strings(names)
	return normalized, names, nil
}
func aad(workspace, ref, origin string) []byte {
	b, _ := json.Marshal([]string{"mcp-gateway-credential-v1", workspace, ref, origin})
	return b
}
func (s *Store) encrypt(workspace, ref, origin string, value map[string]string) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("credential encoding failed")
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("credential encryption failed")
	}
	return s.aead.Seal(nonce, nonce, raw, aad(workspace, ref, origin)), nil
}

const columns = `ref,origin,version,enabled,header_names,created_at,updated_at`

func scan(row interface{ Scan(...any) error }) (Metadata, error) {
	var m Metadata
	err := row.Scan(&m.Ref, &m.Origin, &m.Version, &m.Enabled, &m.HeaderNames, &m.CreatedAt, &m.UpdatedAt)
	return m, mapError(err)
}
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
func audit(ctx context.Context, tx pgx.Tx, a core.Actor, action string, m Metadata, oldVersion int) error {
	raw, _ := json.Marshal(map[string]any{"ref": m.Ref, "origin": m.Origin, "version": m.Version, "old_version": oldVersion, "enabled": m.Enabled, "header_names": m.HeaderNames})
	_, err := tx.Exec(ctx, `INSERT INTO audit_events(workspace_id,actor_id,action,resource_id,data) VALUES($1,$2,$3,$4,$5)`, a.WorkspaceID, a.ID, action, m.Ref, raw)
	return err
}
func (s *Store) List(ctx context.Context, a core.Actor) (Page, error) {
	p := Page{Items: []Metadata{}}
	if err := admin(a); err != nil {
		return p, err
	}
	rows, err := s.db.Query(ctx, `SELECT `+columns+` FROM gateway_credentials WHERE workspace_id=$1 ORDER BY ref LIMIT $2`, a.WorkspaceID, MaxCredentials+1)
	if err != nil {
		return p, err
	}
	defer rows.Close()
	for rows.Next() {
		m, err := scan(rows)
		if err != nil {
			return p, err
		}
		p.Items = append(p.Items, m)
	}
	if err := rows.Err(); err != nil {
		return p, err
	}
	if len(p.Items) > MaxCredentials {
		return Page{}, fmt.Errorf("%w: credential inventory exceeds supported capacity", core.ErrConflict)
	}
	p.Total = len(p.Items)
	return p, nil
}
func (s *Store) Create(ctx context.Context, a core.Actor, in CreateInput) (Metadata, error) {
	if err := admin(a); err != nil {
		return Metadata{}, err
	}
	if !reference.MatchString(in.Ref) {
		return Metadata{}, fmt.Errorf("%w: invalid credential reference", core.ErrInvalid)
	}
	u, err := url.Parse(in.Origin)
	if err != nil || u.Host == "" || in.Origin != u.Scheme+"://"+u.Host {
		return Metadata{}, fmt.Errorf("%w: origin must be an exact HTTP(S) origin", core.ErrInvalid)
	}
	if err = s.policy.ValidateContext(ctx, a.WorkspaceID, core.HTTPConfig{URL: in.Origin}); err != nil {
		return Metadata{}, err
	}
	if s.policy.HasStaticCredential(a.WorkspaceID, in.Ref) {
		return Metadata{}, fmt.Errorf("%w: reference is reserved by deployment configuration", core.ErrConflict)
	}
	values, names, err := headers(in.Headers)
	if err != nil {
		return Metadata{}, err
	}
	encrypted, err := s.encrypt(a.WorkspaceID, in.Ref, in.Origin, values)
	if err != nil {
		return Metadata{}, err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Metadata{}, err
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, a.WorkspaceID+"\x1fcredentials"); err != nil {
		return Metadata{}, err
	}
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM gateway_credentials WHERE workspace_id=$1`, a.WorkspaceID).Scan(&count); err != nil {
		return Metadata{}, err
	}
	if count >= MaxCredentials {
		return Metadata{}, fmt.Errorf("%w: credential limit reached", core.ErrConflict)
	}
	m, err := scan(tx.QueryRow(ctx, `INSERT INTO gateway_credentials(workspace_id,ref,origin,header_names,encrypted_headers) VALUES($1,$2,$3,$4,$5) RETURNING `+columns, a.WorkspaceID, in.Ref, in.Origin, names, encrypted))
	if err != nil {
		return m, err
	}
	if err = audit(ctx, tx, a, "CREDENTIAL_CREATED", m, 0); err != nil {
		return Metadata{}, err
	}
	return m, tx.Commit(ctx)
}
func (s *Store) Rotate(ctx context.Context, a core.Actor, ref string, in RotateInput) (Metadata, error) {
	if err := admin(a); err != nil {
		return Metadata{}, err
	}
	if in.ExpectedVersion < 1 {
		return Metadata{}, fmt.Errorf("%w: positive expected_version required", core.ErrInvalid)
	}
	values, names, err := headers(in.Headers)
	if err != nil {
		return Metadata{}, err
	}
	return s.update(ctx, a, ref, in.ExpectedVersion, func(tx pgx.Tx, m Metadata) (Metadata, error) {
		encrypted, err := s.encrypt(a.WorkspaceID, ref, m.Origin, values)
		if err != nil {
			return Metadata{}, err
		}
		updated, err := scan(tx.QueryRow(ctx, `UPDATE gateway_credentials SET encrypted_headers=$3,header_names=$4,version=version+1,updated_at=clock_timestamp() WHERE workspace_id=$1 AND ref=$2 RETURNING `+columns, a.WorkspaceID, ref, encrypted, names))
		if err != nil {
			return updated, err
		}
		return updated, audit(ctx, tx, a, "CREDENTIAL_ROTATED", updated, m.Version)
	})
}
func (s *Store) SetEnabled(ctx context.Context, a core.Actor, ref string, in EnabledInput) (Metadata, error) {
	if err := admin(a); err != nil {
		return Metadata{}, err
	}
	if in.ExpectedVersion < 1 || in.Enabled == nil {
		return Metadata{}, fmt.Errorf("%w: expected_version and enabled required", core.ErrInvalid)
	}
	return s.update(ctx, a, ref, in.ExpectedVersion, func(tx pgx.Tx, m Metadata) (Metadata, error) {
		if m.Enabled == *in.Enabled {
			return m, nil
		}
		updated, err := scan(tx.QueryRow(ctx, `UPDATE gateway_credentials SET enabled=$3,version=version+1,updated_at=clock_timestamp() WHERE workspace_id=$1 AND ref=$2 RETURNING `+columns, a.WorkspaceID, ref, *in.Enabled))
		if err != nil {
			return updated, err
		}
		return updated, audit(ctx, tx, a, "CREDENTIAL_ENABLED_CHANGED", updated, m.Version)
	})
}
func (s *Store) update(ctx context.Context, a core.Actor, ref string, version int, fn func(pgx.Tx, Metadata) (Metadata, error)) (Metadata, error) {
	if !reference.MatchString(ref) {
		return Metadata{}, fmt.Errorf("%w: invalid credential reference", core.ErrInvalid)
	}
	if s.policy.HasStaticCredential(a.WorkspaceID, ref) {
		return Metadata{}, fmt.Errorf("%w: reference has ambiguous sources", core.ErrConflict)
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Metadata{}, err
	}
	defer tx.Rollback(context.Background())
	m, err := scan(tx.QueryRow(ctx, `SELECT `+columns+` FROM gateway_credentials WHERE workspace_id=$1 AND ref=$2 FOR UPDATE`, a.WorkspaceID, ref))
	if err != nil {
		return m, err
	}
	if m.Version != version {
		return Metadata{}, fmt.Errorf("%w: credential changed; reload before retrying", core.ErrConflict)
	}
	m, err = fn(tx, m)
	if err != nil {
		return Metadata{}, err
	}
	return m, tx.Commit(ctx)
}

// Resolve never treats a disabled, wrongly scoped, corrupted or undecryptable
// managed credential as absent. Adapters must fail closed on every such error.
func (s *Store) Resolve(ctx context.Context, workspace, origin, ref string) (httpadapter.Credential, bool, error) {
	var storedOrigin string
	var enabled bool
	var encrypted []byte
	err := s.db.QueryRow(ctx, `SELECT origin,enabled,encrypted_headers FROM gateway_credentials WHERE workspace_id=$1 AND ref=$2`, workspace, ref).Scan(&storedOrigin, &enabled, &encrypted)
	if errors.Is(err, pgx.ErrNoRows) {
		return httpadapter.Credential{}, false, nil
	}
	if err != nil {
		return httpadapter.Credential{}, false, fmt.Errorf("credential lookup unavailable")
	}
	failure := func() (httpadapter.Credential, bool, error) {
		return httpadapter.Credential{}, true, fmt.Errorf("credential unavailable for this destination")
	}
	if !enabled || origin != storedOrigin || len(encrypted) < s.aead.NonceSize() {
		return failure()
	}
	raw, err := s.aead.Open(nil, encrypted[:s.aead.NonceSize()], encrypted[s.aead.NonceSize():], aad(workspace, ref, origin))
	if err != nil {
		return failure()
	}
	var values map[string]string
	if json.Unmarshal(raw, &values) != nil {
		return failure()
	}
	values, _, err = headers(values)
	if err != nil {
		return failure()
	}
	return httpadapter.Credential{WorkspaceID: workspace, Ref: ref, Origin: origin, Headers: values}, true, nil
}
