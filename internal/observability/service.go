// Package observability exposes scoped operational evidence and append-only
// human reconciliation. It has no adapter and cannot dispatch business tools.
package observability

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/alan1-666/mcp-gateway/internal/store/postgres"
)

type Service struct{ db postgres.DB }

func New(db postgres.DB) *Service { return &Service{db: db} }

type Filter struct {
	Limit      int        `json:"-"`
	Cursor     string     `json:"-"`
	ActorID    string     `json:"actor_id,omitempty"`
	Action     string     `json:"action,omitempty"`
	ResourceID string     `json:"resource_id,omitempty"`
	ToolID     string     `json:"tool_id,omitempty"`
	State      core.State `json:"state,omitempty"`
	From       *time.Time `json:"from,omitempty"`
	To         *time.Time `json:"to,omitempty"`
}
type Page[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
	Total      int64  `json:"total"`
}
type AuditEvent struct {
	ID         string          `json:"id"`
	ActorID    string          `json:"actor_id"`
	Action     string          `json:"action"`
	ResourceID string          `json:"resource_id"`
	Data       json.RawMessage `json:"data"`
	CreatedAt  time.Time       `json:"created_at"`
}
type cursor struct {
	Version   int       `json:"v"`
	Workspace string    `json:"workspace"`
	Actor     string    `json:"actor"`
	Role      core.Role `json:"role"`
	Client    string    `json:"client"`
	ClientKey string    `json:"client_key"`
	Kind      string    `json:"kind"`
	Filter    string    `json:"filter"`
	Upper     time.Time `json:"upper"`
	AfterTime time.Time `json:"after_time"`
	AfterID   string    `json:"after_id"`
}

func authorized(a core.Actor) error {
	if a.ID == "" || a.WorkspaceID == "" {
		return core.ErrUnauthorized
	}
	switch a.Role {
	case core.RoleAdmin, core.RoleApprover, core.RoleOperator, core.RoleViewer:
	default:
		return core.ErrForbidden
	}
	if a.ClientID != "" && (a.ClientID != a.ID || a.Role != core.RoleOperator) {
		return core.ErrForbidden
	}
	return nil
}
func invalid(message string) error { return fmt.Errorf("%w: %s", core.ErrInvalid, message) }
func validIdentifier(value string) bool {
	return len(value) <= 128 && utf8.ValidString(value) && !strings.ContainsAny(value, "\x00\r\n")
}
func normalize(in Filter) (Filter, error) {
	if in.Limit == 0 {
		in.Limit = 50
	}
	if in.Limit < 1 || in.Limit > 100 || len(in.Cursor) > 4096 {
		return in, invalid("limit must be 1-100 and cursor bounded")
	}
	for _, value := range []string{in.ActorID, in.Action, in.ResourceID, in.ToolID} {
		if !validIdentifier(value) {
			return in, invalid("invalid exact-match filter")
		}
	}
	if in.State != "" {
		switch in.State {
		case core.StateWaitingApproval, core.StateReady, core.StateDispatching, core.StateSucceeded, core.StateFailed, core.StateUnknown, core.StateRejected:
		default:
			return in, invalid("unsupported operation state")
		}
	}
	for _, value := range []*time.Time{in.From, in.To} {
		if value != nil && (value.Year() < 1970 || value.Year() > 9999) {
			return in, invalid("invalid time filter")
		}
	}
	if in.From != nil && in.To != nil && in.From.After(*in.To) {
		return in, invalid("from must be before or equal to to")
	}
	return in, nil
}
func filterHash(in Filter) string {
	raw, _ := json.Marshal(in)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func readCursor(a core.Actor, in Filter, kind string, numeric bool) (cursor, error) {
	c := cursor{Version: 1, Workspace: a.WorkspaceID, Actor: a.ID, Role: a.Role, Client: a.ClientID, ClientKey: a.ClientKeyID, Kind: kind, Filter: filterHash(in)}
	if in.Cursor == "" {
		return c, nil
	}
	bad := func() (cursor, error) { return cursor{}, invalid("malformed or mismatched history cursor") }
	raw, err := base64.RawURLEncoding.Strict().DecodeString(in.Cursor)
	if err != nil {
		return bad()
	}
	var decoded cursor
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&decoded) != nil || decoder.Decode(new(any)) != io.EOF {
		return bad()
	}
	if decoded.Version != c.Version || decoded.Workspace != c.Workspace || decoded.Actor != c.Actor || decoded.Role != c.Role || decoded.Client != c.Client || decoded.ClientKey != c.ClientKey || decoded.Kind != c.Kind || decoded.Filter != c.Filter {
		return bad()
	}
	if decoded.Upper.IsZero() || decoded.AfterTime.IsZero() || decoded.AfterTime.Year() < 1970 || decoded.AfterTime.After(decoded.Upper) || decoded.Upper.After(time.Now().Add(5*time.Minute)) || decoded.AfterID == "" || !validIdentifier(decoded.AfterID) {
		return bad()
	}
	if numeric {
		v, err := strconv.ParseInt(decoded.AfterID, 10, 64)
		if err != nil || v < 1 {
			return bad()
		}
	}
	return decoded, nil
}
func writeCursor(c cursor, upper, after time.Time, id string) (string, error) {
	c.Upper = upper
	c.AfterTime = after
	c.AfterID = id
	raw, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(raw)
	if len(encoded) > 4096 {
		return "", invalid("identity context exceeds history cursor capacity")
	}
	return encoded, nil
}

func (s *Service) Audit(ctx context.Context, a core.Actor, in Filter) (Page[AuditEvent], error) {
	p := Page[AuditEvent]{Items: []AuditEvent{}}
	if err := authorized(a); err != nil {
		return p, err
	}
	if a.Role != core.RoleAdmin || a.ClientID != "" {
		return p, core.ErrForbidden
	}
	if in.ToolID != "" || in.State != "" {
		return p, invalid("unsupported audit filter")
	}
	in, err := normalize(in)
	if err != nil {
		return p, err
	}
	c, err := readCursor(a, in, "audit", true)
	if err != nil {
		return p, err
	}
	var upperArg, afterArg, afterID any
	if in.Cursor != "" {
		upperArg = c.Upper
		afterArg = c.AfterTime
		afterID = c.AfterID
	}
	var upper time.Time
	var raw []byte
	err = s.db.QueryRow(ctx, `WITH boundary AS(SELECT COALESCE($7::timestamptz,statement_timestamp()) AS upper_bound),
 filtered AS MATERIALIZED(SELECT ae.id,ae.created_at FROM audit_events ae CROSS JOIN boundary b
 WHERE ae.workspace_id=$1 AND ($2='' OR ae.actor_id=$2) AND ($3='' OR ae.action=$3) AND ($4='' OR ae.resource_id=$4)
 AND ($5::timestamptz IS NULL OR ae.created_at >= $5) AND ($6::timestamptz IS NULL OR ae.created_at <= $6) AND ae.created_at<=b.upper_bound),
 page AS(SELECT id,created_at FROM filtered WHERE ($8::timestamptz IS NULL OR (created_at,id)<($8,$9::bigint)) ORDER BY created_at DESC,id DESC LIMIT $10)
 SELECT (SELECT upper_bound FROM boundary),(SELECT count(*) FROM filtered),COALESCE((SELECT jsonb_agg(jsonb_build_object('id',ae.id::text,'actor_id',ae.actor_id,'action',ae.action,'resource_id',ae.resource_id,'data',ae.data,'created_at',ae.created_at) ORDER BY ae.created_at DESC,ae.id DESC) FROM page p JOIN audit_events ae ON ae.id=p.id),'[]'::jsonb)`, a.WorkspaceID, in.ActorID, in.Action, in.ResourceID, in.From, in.To, upperArg, afterArg, afterID, in.Limit+1).Scan(&upper, &p.Total, &raw)
	if err != nil {
		return p, err
	}
	if err = json.Unmarshal(raw, &p.Items); err != nil {
		return p, fmt.Errorf("could not decode audit page")
	}
	if len(p.Items) > in.Limit {
		p.Items = p.Items[:in.Limit]
		last := p.Items[in.Limit-1]
		p.NextCursor, err = writeCursor(c, upper, last.CreatedAt, last.ID)
	}
	return p, err
}

func (s *Service) Operations(ctx context.Context, a core.Actor, in Filter) (Page[core.Operation], error) {
	p := Page[core.Operation]{Items: []core.Operation{}}
	if err := authorized(a); err != nil {
		return p, err
	}
	if in.Action != "" || in.ResourceID != "" {
		return p, invalid("unsupported operation filter")
	}
	in, err := normalize(in)
	if err != nil {
		return p, err
	}
	c, err := readCursor(a, in, "operations", false)
	if err != nil {
		return p, err
	}
	var upperArg, afterArg any
	if in.Cursor != "" {
		upperArg = c.Upper
		afterArg = c.AfterTime
	}
	var upper time.Time
	var raw []byte
	query := `WITH boundary AS(SELECT COALESCE($10::timestamptz,statement_timestamp()) AS upper_bound),
 filtered AS MATERIALIZED(SELECT operations.id,operations.created_at FROM operations CROSS JOIN boundary b
 WHERE operations.workspace_id=$1 AND (operations.actor_id=$2 OR $3) AND ` + postgres.ClientOperationAccessSQL("$4", "$5") + `
 AND ($6='' OR operations.actor_id=$6) AND ($7='' OR operations.tool_id=$7) AND ($8='' OR operations.state=$8)
 AND ($9::timestamptz IS NULL OR operations.created_at >= $9) AND ($11::timestamptz IS NULL OR operations.created_at <= $11) AND operations.created_at<=b.upper_bound),
 page AS(SELECT id,created_at FROM filtered WHERE ($12::timestamptz IS NULL OR (created_at,id)<($12,$13::text)) ORDER BY created_at DESC,id DESC LIMIT $14)
 SELECT (SELECT upper_bound FROM boundary),(SELECT count(*) FROM filtered),COALESCE((SELECT jsonb_agg(to_jsonb(o)-'tool_snapshot' ORDER BY o.created_at DESC,o.id DESC) FROM page p JOIN operations o ON o.workspace_id=$1 AND o.id=p.id),'[]'::jsonb)`
	err = s.db.QueryRow(ctx, query, a.WorkspaceID, a.ID, a.Role == core.RoleAdmin || a.Role == core.RoleApprover, a.ClientID, a.ClientKeyID, in.ActorID, in.ToolID, in.State, in.From, upperArg, in.To, afterArg, c.AfterID, in.Limit+1).Scan(&upper, &p.Total, &raw)
	if err != nil {
		return p, err
	}
	if err = json.Unmarshal(raw, &p.Items); err != nil {
		return p, fmt.Errorf("could not decode operations page")
	}
	if len(p.Items) > in.Limit {
		p.Items = p.Items[:in.Limit]
		last := p.Items[in.Limit-1]
		p.NextCursor, err = writeCursor(c, upper, last.CreatedAt, last.ID)
	}
	return p, err
}
