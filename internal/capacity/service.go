// Package capacity implements shared, database-backed admission budgets.
// Call Acquire before Claim; do not hold an operation row lock during admission.
package capacity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/alan1-666/mcp-gateway/internal/store/postgres"
	"github.com/jackc/pgx/v5"
)

const LeaseDuration = 180 * time.Second

type Limit struct {
	Scope             string    `json:"scope"`
	ScopeID           string    `json:"scope_id"`
	Version           int       `json:"version"`
	MaxConcurrent     int       `json:"max_concurrent"`
	RequestsPerMinute int       `json:"requests_per_minute"`
	UpdatedAt         time.Time `json:"updated_at"`
}
type UpdateInput struct {
	Scope             string `json:"scope"`
	ScopeID           string `json:"scope_id"`
	ExpectedVersion   int    `json:"expected_version"`
	MaxConcurrent     int    `json:"max_concurrent"`
	RequestsPerMinute int    `json:"requests_per_minute"`
}
type LimitError struct {
	Scope, Reason     string
	RetryAfterSeconds int
}

func (e *LimitError) Error() string { return "capacity limit reached; retry later" }

type Service struct{ db postgres.DB }

func New(db postgres.DB) *Service { return &Service{db: db} }
func defaults() []Limit {
	return []Limit{{Scope: "workspace", MaxConcurrent: 32, RequestsPerMinute: 600}, {Scope: "client", MaxConcurrent: 8, RequestsPerMinute: 120}, {Scope: "upstream", MaxConcurrent: 8, RequestsPerMinute: 120}}
}
func authorize(a core.Actor) error {
	if a.ID == "" || a.WorkspaceID == "" {
		return core.ErrUnauthorized
	}
	if a.Role != core.RoleAdmin || a.ClientID != "" {
		return core.ErrForbidden
	}
	return nil
}
func invalid(message string) error { return fmt.Errorf("%w: %s", core.ErrInvalid, message) }
func lock(ctx context.Context, tx pgx.Tx, workspace string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('capacity:' || $1, 0))`, workspace)
	return err
}
func (s *Service) List(ctx context.Context, actor core.Actor) ([]Limit, error) {
	if err := authorize(actor); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `SELECT scope,scope_id,version,max_concurrent,requests_per_minute,updated_at FROM capacity_limits WHERE workspace_id=$1 ORDER BY scope,scope_id`, actor.WorkspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Limit{}
	for rows.Next() {
		var v Limit
		if err = rows.Scan(&v.Scope, &v.ScopeID, &v.Version, &v.MaxConcurrent, &v.RequestsPerMinute, &v.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, v)
	}
	return items, rows.Err()
}
func validate(in UpdateInput) error {
	if in.ExpectedVersion < 0 || in.MaxConcurrent < 1 || in.MaxConcurrent > 256 || in.RequestsPerMinute < 1 || in.RequestsPerMinute > 60000 {
		return invalid("limits require concurrency 1..256, requests per minute 1..60000, and a nonnegative expected_version")
	}
	if len(in.ScopeID) == 0 || len(in.ScopeID) > 512 || in.ScopeID != strings.TrimSpace(in.ScopeID) || strings.ContainsAny(in.ScopeID, "\x00\r\n") {
		return invalid("invalid scope_id")
	}
	switch in.Scope {
	case "workspace":
		if in.ScopeID != "*" {
			return invalid("workspace scope_id must be *")
		}
	case "client":
	case "upstream":
		if !strings.HasPrefix(in.ScopeID, "mcp:") && !strings.HasPrefix(in.ScopeID, "http:") {
			return invalid("upstream scope_id must start with mcp: or http:")
		}
	default:
		return invalid("unsupported capacity scope")
	}
	return nil
}
func (s *Service) Update(ctx context.Context, actor core.Actor, in UpdateInput) (Limit, error) {
	if err := authorize(actor); err != nil {
		return Limit{}, err
	}
	if err := validate(in); err != nil {
		return Limit{}, err
	}
	if in.Scope == "upstream" && strings.HasPrefix(in.ScopeID, "http:") {
		u, err := url.Parse(strings.TrimPrefix(in.ScopeID, "http:"))
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
			return Limit{}, invalid("HTTP upstream scope must be an origin")
		}
		in.ScopeID = "http:" + canonicalOrigin(u)
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Limit{}, err
	}
	defer tx.Rollback(ctx)
	if err = lock(ctx, tx, actor.WorkspaceID); err != nil {
		return Limit{}, err
	}
	var exists bool
	switch in.Scope {
	case "client":
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM gateway_clients WHERE workspace_id=$1 AND id=$2)`, actor.WorkspaceID, in.ScopeID).Scan(&exists)
	case "upstream":
		if strings.HasPrefix(in.ScopeID, "mcp:") {
			err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM mcp_servers WHERE workspace_id=$1 AND id=$2)`, actor.WorkspaceID, strings.TrimPrefix(in.ScopeID, "mcp:")).Scan(&exists)
		} else {
			u, e := url.Parse(strings.TrimPrefix(in.ScopeID, "http:"))
			exists = e == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && u.Path == ""
		}
	default:
		exists = true
	}
	if err != nil {
		return Limit{}, err
	}
	if !exists {
		return Limit{}, invalid("capacity scope does not identify a valid workspace resource or HTTP origin")
	}
	var version int
	err = tx.QueryRow(ctx, `SELECT version FROM capacity_limits WHERE workspace_id=$1 AND scope=$2 AND scope_id=$3`, actor.WorkspaceID, in.Scope, in.ScopeID).Scan(&version)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Limit{}, err
	}
	if version != in.ExpectedVersion {
		return Limit{}, fmt.Errorf("%w: capacity limits changed; reload before saving", core.ErrConflict)
	}
	var out Limit
	err = tx.QueryRow(ctx, `INSERT INTO capacity_limits(workspace_id,scope,scope_id,max_concurrent,requests_per_minute) VALUES($1,$2,$3,$4,$5) ON CONFLICT(workspace_id,scope,scope_id) DO UPDATE SET max_concurrent=excluded.max_concurrent,requests_per_minute=excluded.requests_per_minute,version=capacity_limits.version+1,updated_at=clock_timestamp() RETURNING scope,scope_id,version,max_concurrent,requests_per_minute,updated_at`, actor.WorkspaceID, in.Scope, in.ScopeID, in.MaxConcurrent, in.RequestsPerMinute).Scan(&out.Scope, &out.ScopeID, &out.Version, &out.MaxConcurrent, &out.RequestsPerMinute, &out.UpdatedAt)
	if err != nil {
		return Limit{}, err
	}
	data, _ := json.Marshal(out)
	_, err = tx.Exec(ctx, `INSERT INTO audit_events(workspace_id,actor_id,action,resource_id,data) VALUES($1,$2,'capacity.updated',$3,$4)`, actor.WorkspaceID, actor.ID, in.Scope+":"+in.ScopeID, data)
	if err != nil {
		return Limit{}, err
	}
	return out, tx.Commit(ctx)
}

type Lease struct {
	service                       *Service
	workspace, operationID, token string
}

func (l *Lease) Release(ctx context.Context) error {
	if l == nil {
		return nil
	}
	_, err := l.service.db.Exec(ctx, `DELETE FROM capacity_leases WHERE workspace_id=$1 AND operation_id=$2 AND token=$3`, l.workspace, l.operationID, l.token)
	return err
}
func canonicalOrigin(u *url.URL) string {
	scheme, host, port := strings.ToLower(u.Scheme), strings.ToLower(u.Hostname()), u.Port()
	if (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		port = ""
	}
	if port != "" {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return scheme + "://" + host
}
func upstreamID(tool core.Tool) (string, error) {
	if tool.MCP != nil {
		return "mcp:" + tool.MCP.ServerID, nil
	}
	u, err := url.Parse(tool.HTTP.URL)
	if err != nil || u.Host == "" {
		return "", invalid("invalid operation upstream")
	}
	return "http:" + canonicalOrigin(u), nil
}
func (s *Service) Acquire(ctx context.Context, actor core.Actor, operationID string) (*Lease, error) {
	if actor.ID == "" || actor.WorkspaceID == "" {
		return nil, core.ErrUnauthorized
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if err = lock(ctx, tx, actor.WorkspaceID); err != nil {
		return nil, err
	}
	var raw []byte
	var op core.Operation
	op.ID = operationID
	op.WorkspaceID = actor.WorkspaceID
	err = tx.QueryRow(ctx, `SELECT actor_id,state,tool_snapshot FROM operations WHERE workspace_id=$1 AND id=$2`, actor.WorkspaceID, operationID).Scan(&op.ActorID, &op.State, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, core.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if !core.CanExecuteOperation(actor, op) {
		return nil, core.ErrNotFound
	}
	if op.State != core.StateReady {
		return nil, fmt.Errorf("%w: operation is not ready for admission", core.ErrConflict)
	}
	var tool core.Tool
	if err = json.Unmarshal(raw, &tool); err != nil {
		return nil, err
	}
	upstream, err := upstreamID(tool)
	if err != nil {
		return nil, err
	}
	// Charge the operation owner, even when an administrator dispatches it.
	client := "user:" + op.ActorID
	var machineOwner bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM gateway_clients WHERE workspace_id=$1 AND id=$2)`, actor.WorkspaceID, op.ActorID).Scan(&machineOwner); err != nil {
		return nil, err
	}
	if machineOwner {
		client = op.ActorID
	}
	var now time.Time
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM capacity_leases WHERE workspace_id=$1 AND expires_at<=$2`, actor.WorkspaceID, now); err != nil {
		return nil, err
	}
	var duplicate bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM capacity_leases WHERE workspace_id=$1 AND operation_id=$2)`, actor.WorkspaceID, operationID).Scan(&duplicate); err != nil {
		return nil, err
	}
	reject := func(scope, reason string, retry int) (*Lease, error) {
		_, e := tx.Exec(ctx, `INSERT INTO capacity_rejection_metrics(workspace_id,scope,reason,count) VALUES($1,$2,$3,1) ON CONFLICT(workspace_id,scope,reason) DO UPDATE SET count=capacity_rejection_metrics.count+1`, actor.WorkspaceID, scope, reason)
		if e != nil {
			return nil, e
		}
		if e = tx.Commit(ctx); e != nil {
			return nil, e
		}
		return nil, &LimitError{Scope: scope, Reason: reason, RetryAfterSeconds: retry}
	}
	if duplicate {
		return reject("workspace", "concurrency", 1)
	}
	limits := defaults()
	ids := []string{"*", client, upstream}
	minute := now.Truncate(time.Minute)
	for i := range limits {
		limit := &limits[i]
		limit.ScopeID = ids[i]
		if _, err = tx.Exec(ctx, `DELETE FROM capacity_windows WHERE workspace_id=$1 AND scope=$2 AND scope_id=$3 AND minute<$4`, actor.WorkspaceID, limit.Scope, limit.ScopeID, minute.Add(-2*time.Minute)); err != nil {
			return nil, err
		}
		e := tx.QueryRow(ctx, `SELECT max_concurrent,requests_per_minute FROM capacity_limits WHERE workspace_id=$1 AND scope=$2 AND scope_id=$3`, actor.WorkspaceID, limit.Scope, limit.ScopeID).Scan(&limit.MaxConcurrent, &limit.RequestsPerMinute)
		if e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return nil, e
		}
		var active, requests int
		e = tx.QueryRow(ctx, `SELECT count(*) FROM capacity_leases WHERE workspace_id=$1 AND ($2='workspace' OR ($2='client' AND client_id=$3) OR ($2='upstream' AND upstream_id=$3))`, actor.WorkspaceID, limit.Scope, limit.ScopeID).Scan(&active)
		if e != nil {
			return nil, e
		}
		if active >= limit.MaxConcurrent {
			return reject(limit.Scope, "concurrency", 1)
		}
		e = tx.QueryRow(ctx, `SELECT requests FROM capacity_windows WHERE workspace_id=$1 AND scope=$2 AND scope_id=$3 AND minute=$4`, actor.WorkspaceID, limit.Scope, limit.ScopeID, minute).Scan(&requests)
		if e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return nil, e
		}
		if requests >= limit.RequestsPerMinute {
			return reject(limit.Scope, "rate", max(1, int(minute.Add(time.Minute).Sub(now).Seconds())+1))
		}
	}
	for _, limit := range limits {
		_, err = tx.Exec(ctx, `INSERT INTO capacity_windows(workspace_id,scope,scope_id,minute,requests) VALUES($1,$2,$3,$4,1) ON CONFLICT(workspace_id,scope,scope_id,minute) DO UPDATE SET requests=capacity_windows.requests+1`, actor.WorkspaceID, limit.Scope, limit.ScopeID, minute)
		if err != nil {
			return nil, err
		}
	}
	lease := &Lease{service: s, workspace: actor.WorkspaceID, operationID: operationID, token: core.NewID()}
	_, err = tx.Exec(ctx, `INSERT INTO capacity_leases(workspace_id,operation_id,token,client_id,upstream_id,expires_at) VALUES($1,$2,$3,$4,$5,$6)`, actor.WorkspaceID, operationID, lease.token, client, upstream, now.Add(LeaseDuration))
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return lease, nil
}
