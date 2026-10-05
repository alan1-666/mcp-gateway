// Package releases publishes reviewed, immutable tool definitions. A rollback is
// a new release, and never rewrites the snapshot pinned by an existing operation.
package releases

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/alan1-666/mcp-gateway/internal/adapters/httpadapter"
	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/alan1-666/mcp-gateway/internal/store/postgres"
	"github.com/alan1-666/mcp-gateway/internal/upstreams"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type Service struct {
	db     postgres.DB
	remote upstreams.Remote
	http   *httpadapter.Adapter
}

func New(db postgres.DB, remote upstreams.Remote, http *httpadapter.Adapter) *Service {
	return &Service{db: db, remote: remote, http: http}
}

type Version struct {
	Version    int            `json:"version"`
	Definition core.ToolInput `json:"definition"`
	CreatedAt  time.Time      `json:"created_at"`
}
type VersionPage struct {
	Items      []Version `json:"items"`
	NextCursor int       `json:"next_cursor,omitempty"`
}
type Change struct {
	Field  string `json:"field"`
	Before any    `json:"before"`
	After  any    `json:"after"`
}
type Candidate struct {
	ID               string         `json:"id"`
	ToolID           string         `json:"tool_id"`
	BaseVersion      int            `json:"base_version"`
	SourceVersion    *int           `json:"source_version,omitempty"`
	Definition       core.ToolInput `json:"definition"`
	Changes          []Change       `json:"changes"`
	Reason           string         `json:"reason"`
	CreatedBy        string         `json:"created_by"`
	CreatedAt        time.Time      `json:"created_at"`
	PublishedVersion *int           `json:"published_version,omitempty"`
}
type CandidateInput struct {
	ExpectedVersion    int                  `json:"expected_version"`
	ExpectedSchemaHash string               `json:"expected_schema_hash,omitempty"`
	SourceVersion      int                  `json:"source_version,omitempty"`
	ServerID           string               `json:"server_id,omitempty"`
	ToolName           string               `json:"tool_name,omitempty"`
	Risk               core.Risk            `json:"risk,omitempty"`
	ResponsePolicy     *core.ResponsePolicy `json:"response_policy,omitempty"`
	Definition         *core.ToolInput      `json:"definition,omitempty"`
	Reason             string               `json:"reason"`
}

func admin(a core.Actor) error {
	if a.ID == "" || a.WorkspaceID == "" {
		return core.ErrUnauthorized
	}
	if a.Role != core.RoleAdmin || a.ClientID != "" {
		return core.ErrForbidden
	}
	return nil
}
func mapped(err error) error {
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
	result, err := fn(tx)
	if err != nil {
		return zero, mapped(err)
	}
	if err = tx.Commit(ctx); err != nil {
		return zero, mapped(err)
	}
	return result, nil
}
func audit(ctx context.Context, tx pgx.Tx, a core.Actor, action, id string, data any) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO audit_events(workspace_id,actor_id,action,resource_id,data) VALUES($1,$2,$3,$4,$5)`, a.WorkspaceID, a.ID, action, id, raw)
	return err
}
func definition(t core.Tool) core.ToolInput {
	return core.ToolInput{Name: t.Name, Description: t.Description, Risk: t.Risk, InputSchema: t.InputSchema, OutputSchema: t.OutputSchema, HTTP: t.HTTP, MCP: t.MCP, ResponsePolicy: t.ResponsePolicy}
}
func differences(before, after core.ToolInput) []Change {
	// Decode with UseNumber so large schema numbers are not silently rounded.
	toMap := func(v core.ToolInput) map[string]any {
		raw, _ := json.Marshal(v)
		d := json.NewDecoder(strings.NewReader(string(raw)))
		d.UseNumber()
		var m map[string]any
		_ = d.Decode(&m)
		return m
	}
	b, a := toMap(before), toMap(after)
	result := []Change{}
	for _, k := range []string{"name", "description", "risk", "input_schema", "output_schema", "http", "mcp", "response_policy"} {
		if !reflect.DeepEqual(b[k], a[k]) {
			result = append(result, Change{k, b[k], a[k]})
		}
	}
	return result
}
func (s *Service) Versions(ctx context.Context, a core.Actor, id string, before, limit int) (VersionPage, error) {
	page := VersionPage{Items: []Version{}}
	if err := admin(a); err != nil {
		return page, err
	}
	if before < 0 || limit < 1 || limit > 100 {
		return page, core.ErrInvalid
	}
	if _, err := postgres.New(s.db).GetTool(ctx, a, id); err != nil {
		return page, err
	}
	rows, err := s.db.Query(ctx, `SELECT version,definition,created_at FROM tool_versions WHERE workspace_id=$1 AND tool_id=$2 AND ($3=0 OR version<$3) ORDER BY version DESC LIMIT $4`, a.WorkspaceID, id, before, limit+1)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		var v Version
		var raw []byte
		if err := rows.Scan(&v.Version, &raw, &v.CreatedAt); err != nil {
			return page, err
		}
		if err := json.Unmarshal(raw, &v.Definition); err != nil {
			return page, err
		}
		page.Items = append(page.Items, v)
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.NextCursor = page.Items[limit-1].Version
	}
	return page, rows.Err()
}
func (s *Service) source(ctx context.Context, a core.Actor, base core.Tool, in CandidateInput) (core.ToolInput, error) {
	if in.SourceVersion != 0 {
		if in.SourceVersion < 1 || in.ServerID != "" || in.ToolName != "" || in.Risk != "" || in.ResponsePolicy != nil || in.Definition != nil || in.ExpectedSchemaHash != "" {
			return core.ToolInput{}, core.ErrInvalid
		}
		var raw []byte
		err := s.db.QueryRow(ctx, `SELECT definition FROM tool_versions WHERE workspace_id=$1 AND tool_id=$2 AND version=$3`, a.WorkspaceID, base.ID, in.SourceVersion).Scan(&raw)
		if err != nil {
			return core.ToolInput{}, mapped(err)
		}
		var d core.ToolInput
		err = json.Unmarshal(raw, &d)
		return d, err
	}
	if in.Definition != nil {
		if base.MCP != nil || in.Definition.MCP != nil || in.ServerID != "" || in.ToolName != "" || in.Risk != "" || in.ResponsePolicy != nil || in.ExpectedSchemaHash != "" {
			return core.ToolInput{}, core.ErrInvalid
		}
		if in.Definition.Name != base.Name {
			return core.ToolInput{}, fmt.Errorf("%w: gateway tool name is immutable", core.ErrInvalid)
		}
		return *in.Definition, nil
	}
	if base.MCP == nil {
		return core.ToolInput{}, fmt.Errorf("%w: an HTTP candidate requires its complete definition", core.ErrInvalid)
	}
	serverID, name := in.ServerID, in.ToolName
	if serverID == "" {
		serverID = base.MCP.ServerID
	}
	if name == "" {
		name = base.MCP.ToolName
	}
	server, err := upstreams.NewStore(s.db).GetServer(ctx, a.WorkspaceID, serverID)
	if err != nil {
		return core.ToolInput{}, err
	}
	if !server.Enabled {
		return core.ToolInput{}, core.ErrConflict
	}
	items, err := s.remote.Discover(ctx, a, server)
	if err != nil {
		return core.ToolInput{}, err
	}
	for _, item := range items {
		if item.Name == name {
			if in.ExpectedSchemaHash != "" && item.SchemaHash != in.ExpectedSchemaHash {
				return core.ToolInput{}, fmt.Errorf("%w: upstream schema changed since discovery; discover and review again", core.ErrConflict)
			}
			d := definition(base)
			d.InputSchema = item.InputSchema
			d.OutputSchema = item.OutputSchema
			d.MCP = &core.MCPConfig{ServerID: serverID, ToolName: name, SchemaHash: item.SchemaHash}
			d.Description = core.RemoteDescription(item)
			if in.Risk != "" {
				d.Risk = in.Risk
			}
			if in.ResponsePolicy != nil {
				d.ResponsePolicy = in.ResponsePolicy
			}
			return d, nil
		}
	}
	return core.ToolInput{}, fmt.Errorf("%w: upstream tool no longer exists", core.ErrConflict)
}
func (s *Service) validateLive(ctx context.Context, a core.Actor, d core.ToolInput) error {
	if d.MCP == nil {
		if s.http == nil {
			return core.ErrInvalid
		}
		return s.http.ValidateContext(ctx, a.WorkspaceID, d.HTTP)
	}
	server, err := upstreams.NewStore(s.db).GetServer(ctx, a.WorkspaceID, d.MCP.ServerID)
	if err != nil {
		return err
	}
	if !server.Enabled {
		return core.ErrConflict
	}
	items, err := s.remote.Discover(ctx, a, server)
	if err != nil {
		return err
	}
	for _, item := range items {
		if item.Name == d.MCP.ToolName && item.SchemaHash == d.MCP.SchemaHash {
			return nil
		}
	}
	return fmt.Errorf("%w: upstream contract changed; create and review a fresh candidate", core.ErrConflict)
}
func (s *Service) Create(ctx context.Context, a core.Actor, id string, in CandidateInput) (Candidate, error) {
	if err := admin(a); err != nil {
		return Candidate{}, err
	}
	if in.ExpectedSchemaHash != "" {
		if len(in.ExpectedSchemaHash) != 64 || strings.ToLower(in.ExpectedSchemaHash) != in.ExpectedSchemaHash {
			return Candidate{}, fmt.Errorf("%w: expected_schema_hash must be a lowercase SHA-256 hash", core.ErrInvalid)
		}
		if _, err := hex.DecodeString(in.ExpectedSchemaHash); err != nil {
			return Candidate{}, core.ErrInvalid
		}
	}
	in.Reason = strings.TrimSpace(in.Reason)
	if in.ExpectedVersion < 1 || len(in.Reason) < 1 || len(in.Reason) > 1000 || !utf8.ValidString(in.Reason) || strings.ContainsRune(in.Reason, 0) {
		return Candidate{}, fmt.Errorf("%w: expected_version and a 1-1000 byte reason are required", core.ErrInvalid)
	}
	base, err := postgres.New(s.db).GetTool(ctx, a, id)
	if err != nil {
		return Candidate{}, err
	}
	if base.Version != in.ExpectedVersion {
		return Candidate{}, core.ErrConflict
	}
	d, err := s.source(ctx, a, base, in)
	if err != nil {
		return Candidate{}, err
	}
	d, err = core.NormalizeTool(d)
	if err != nil {
		return Candidate{}, err
	}
	if err = s.validateLive(ctx, a, d); err != nil {
		return Candidate{}, err
	}
	changes := differences(definition(base), d)
	if len(changes) == 0 && base.Status != "retired" {
		return Candidate{}, fmt.Errorf("%w: candidate has no changes", core.ErrConflict)
	}
	return transaction(ctx, s.db, func(tx pgx.Tx) (Candidate, error) {
		var version int
		if err := tx.QueryRow(ctx, `SELECT version FROM tools WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, a.WorkspaceID, id).Scan(&version); err != nil {
			return Candidate{}, err
		}
		if version != in.ExpectedVersion {
			return Candidate{}, core.ErrConflict
		}
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM tool_candidates WHERE workspace_id=$1 AND tool_id=$2 AND published_version IS NULL`, a.WorkspaceID, id).Scan(&count); err != nil {
			return Candidate{}, err
		}
		if count >= 100 {
			return Candidate{}, fmt.Errorf("%w: discard stale candidates before creating more", core.ErrConflict)
		}
		c := Candidate{ID: core.NewID(), ToolID: id, BaseVersion: version, Definition: d, Changes: changes, Reason: in.Reason, CreatedBy: a.ID}
		if in.SourceVersion != 0 {
			c.SourceVersion = &in.SourceVersion
		}
		raw, _ := json.Marshal(d)
		err := tx.QueryRow(ctx, `INSERT INTO tool_candidates(workspace_id,id,tool_id,base_version,source_version,definition,reason,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING created_at`, a.WorkspaceID, c.ID, id, version, c.SourceVersion, raw, c.Reason, a.ID).Scan(&c.CreatedAt)
		if err != nil {
			return c, err
		}
		return c, audit(ctx, tx, a, "TOOL_CANDIDATE_CREATED", id, map[string]any{"candidate_id": c.ID, "base_version": version, "source_version": c.SourceVersion})
	})
}

const candidateColumns = `id,tool_id,base_version,source_version,definition,reason,created_by,created_at,published_version`

func scanCandidate(row interface{ Scan(...any) error }) (Candidate, error) {
	var c Candidate
	var raw []byte
	err := row.Scan(&c.ID, &c.ToolID, &c.BaseVersion, &c.SourceVersion, &raw, &c.Reason, &c.CreatedBy, &c.CreatedAt, &c.PublishedVersion)
	if err == nil {
		err = json.Unmarshal(raw, &c.Definition)
	}
	return c, mapped(err)
}
func (s *Service) Candidate(ctx context.Context, a core.Actor, id, candidateID string) (Candidate, error) {
	if err := admin(a); err != nil {
		return Candidate{}, err
	}
	c, err := scanCandidate(s.db.QueryRow(ctx, `SELECT `+candidateColumns+` FROM tool_candidates WHERE workspace_id=$1 AND tool_id=$2 AND id=$3`, a.WorkspaceID, id, candidateID))
	if err != nil {
		return c, err
	}
	var raw []byte
	err = s.db.QueryRow(ctx, `SELECT definition FROM tool_versions WHERE workspace_id=$1 AND tool_id=$2 AND version=$3`, a.WorkspaceID, id, c.BaseVersion).Scan(&raw)
	if err != nil {
		return c, mapped(err)
	}
	var d core.ToolInput
	if err = json.Unmarshal(raw, &d); err != nil {
		return c, err
	}
	c.Changes = differences(d, c.Definition)
	return c, nil
}
func (s *Service) Candidates(ctx context.Context, a core.Actor, id string) ([]Candidate, error) {
	if err := admin(a); err != nil {
		return nil, err
	}
	if _, err := postgres.New(s.db).GetTool(ctx, a, id); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `SELECT `+candidateColumns+` FROM tool_candidates WHERE workspace_id=$1 AND tool_id=$2 AND published_version IS NULL ORDER BY created_at DESC,id DESC LIMIT 100`, a.WorkspaceID, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Candidate{}
	for rows.Next() {
		c, err := scanCandidate(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, c)
	}
	return items, rows.Err()
}
func (s *Service) Discard(ctx context.Context, a core.Actor, id, candidateID string) error {
	if err := admin(a); err != nil {
		return err
	}
	_, err := transaction(ctx, s.db, func(tx pgx.Tx) (bool, error) {
		tag, err := tx.Exec(ctx, `DELETE FROM tool_candidates WHERE workspace_id=$1 AND tool_id=$2 AND id=$3 AND published_version IS NULL`, a.WorkspaceID, id, candidateID)
		if err != nil {
			return false, err
		}
		if tag.RowsAffected() != 1 {
			return false, core.ErrConflict
		}
		return true, audit(ctx, tx, a, "TOOL_CANDIDATE_DISCARDED", id, map[string]any{"candidate_id": candidateID})
	})
	return err
}
func (s *Service) Publish(ctx context.Context, a core.Actor, id, candidateID string, expected int) (core.Tool, error) {
	if expected < 1 {
		return core.Tool{}, core.ErrInvalid
	}
	c, err := s.Candidate(ctx, a, id, candidateID)
	if err != nil {
		return core.Tool{}, err
	}
	if c.BaseVersion != expected {
		return core.Tool{}, core.ErrConflict
	}
	if err = s.validateLive(ctx, a, c.Definition); err != nil {
		return core.Tool{}, err
	}
	return transaction(ctx, s.db, func(tx pgx.Tx) (core.Tool, error) {
		var version int
		if err := tx.QueryRow(ctx, `SELECT version FROM tools WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, a.WorkspaceID, id).Scan(&version); err != nil {
			return core.Tool{}, err
		}
		c, err := scanCandidate(tx.QueryRow(ctx, `SELECT `+candidateColumns+` FROM tool_candidates WHERE workspace_id=$1 AND tool_id=$2 AND id=$3 FOR UPDATE`, a.WorkspaceID, id, candidateID))
		if err != nil {
			return core.Tool{}, err
		}
		if c.PublishedVersion != nil {
			if version == *c.PublishedVersion {
				return postgres.New(tx).GetTool(ctx, a, id)
			}
			return core.Tool{}, core.ErrConflict
		}
		if version != expected {
			return core.Tool{}, fmt.Errorf("%w: tool changed while this candidate was under review", core.ErrConflict)
		}
		if c.Definition.MCP != nil {
			var enabled bool
			err = tx.QueryRow(ctx, `SELECT enabled FROM mcp_servers WHERE workspace_id=$1 AND id=$2 FOR SHARE`, a.WorkspaceID, c.Definition.MCP.ServerID).Scan(&enabled)
			if err != nil {
				return core.Tool{}, err
			}
			if !enabled {
				return core.Tool{}, core.ErrConflict
			}
		}
		raw, _ := json.Marshal(c.Definition)
		_, err = tx.Exec(ctx, `UPDATE tools SET definition=$3,risk=$4,version=version+1,status='published',enabled=true WHERE workspace_id=$1 AND id=$2`, a.WorkspaceID, id, raw, c.Definition.Risk)
		if err != nil {
			return core.Tool{}, err
		}
		_, err = tx.Exec(ctx, `UPDATE tool_candidates SET published_version=$3,published_at=clock_timestamp() WHERE workspace_id=$1 AND id=$2`, a.WorkspaceID, c.ID, version+1)
		if err != nil {
			return core.Tool{}, err
		}
		err = audit(ctx, tx, a, "TOOL_VERSION_PUBLISHED", id, map[string]any{"candidate_id": c.ID, "from_version": version, "to_version": version + 1, "source_version": c.SourceVersion})
		if err != nil {
			return core.Tool{}, err
		}
		return postgres.New(tx).GetTool(ctx, a, id)
	})
}
func (s *Service) Retire(ctx context.Context, a core.Actor, id string, expected int) (core.Tool, error) {
	if err := admin(a); err != nil {
		return core.Tool{}, err
	}
	if expected < 1 {
		return core.Tool{}, core.ErrInvalid
	}
	return transaction(ctx, s.db, func(tx pgx.Tx) (core.Tool, error) {
		var version int
		if err := tx.QueryRow(ctx, `SELECT version FROM tools WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, a.WorkspaceID, id).Scan(&version); err != nil {
			return core.Tool{}, err
		}
		if version != expected {
			return core.Tool{}, core.ErrConflict
		}
		_, err := tx.Exec(ctx, `UPDATE tools SET status='retired',enabled=false,version=version+1 WHERE workspace_id=$1 AND id=$2`, a.WorkspaceID, id)
		if err != nil {
			return core.Tool{}, err
		}
		if err = audit(ctx, tx, a, "TOOL_RETIRED", id, map[string]any{"from_version": version, "to_version": version + 1}); err != nil {
			return core.Tool{}, err
		}
		return postgres.New(tx).GetTool(ctx, a, id)
	})
}
