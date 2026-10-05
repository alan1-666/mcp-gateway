package postgres

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/alan1-666/mcp-gateway/internal/core"
)

// A cursor is an unsigned pagination position. Its context binding prevents
// accidental reuse; all visibility predicates are independently recomputed.
type toolCursor struct {
	Version   int                      `json:"v"`
	Workspace string                   `json:"workspace"`
	Actor     string                   `json:"actor"`
	Role      core.Role                `json:"role"`
	Query     string                   `json:"query"`
	Scope     core.ToolVisibilityScope `json:"scope"`
	Upper     time.Time                `json:"upper"`
	AfterTime time.Time                `json:"after_time"`
	AfterID   string                   `json:"after_id"`
}

func decodeToolCursor(actor core.Actor, input core.ToolSearchInput, scope core.ToolVisibilityScope) (toolCursor, error) {
	invalid := func() (toolCursor, error) {
		return toolCursor{}, fmt.Errorf("%w: malformed or mismatched tool cursor", core.ErrInvalid)
	}
	if len(input.Cursor) == 0 || len(input.Cursor) > 2048 {
		return invalid()
	}
	data, err := base64.RawURLEncoding.Strict().DecodeString(input.Cursor)
	if err != nil {
		return invalid()
	}
	var cursor toolCursor
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cursor); err != nil {
		return invalid()
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return invalid()
	}
	if cursor.Version != 1 || cursor.Workspace != actor.WorkspaceID || cursor.Actor != actor.ID || cursor.Role != actor.Role || cursor.Query != input.Query || cursor.Scope != scope {
		return invalid()
	}
	if cursor.Upper.IsZero() || cursor.AfterTime.IsZero() || cursor.Upper.Year() < 1970 || cursor.AfterTime.Year() < 1970 || cursor.AfterTime.After(cursor.Upper) || cursor.Upper.After(time.Now().Add(5*time.Minute)) {
		return invalid()
	}
	if cursor.AfterID == "" || len(cursor.AfterID) > 128 || !utf8.ValidString(cursor.AfterID) || strings.ContainsAny(cursor.AfterID, "\x00\r\n") {
		return invalid()
	}
	return cursor, nil
}

func encodeToolCursor(actor core.Actor, input core.ToolSearchInput, scope core.ToolVisibilityScope, upper time.Time, last core.Tool) (string, error) {
	cursor := toolCursor{Version: 1, Workspace: actor.WorkspaceID, Actor: actor.ID, Role: actor.Role, Query: input.Query, Scope: scope, Upper: upper.UTC(), AfterTime: last.CreatedAt.UTC(), AfterID: last.ID}
	data, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(data)
	if len(encoded) > 2048 {
		return "", fmt.Errorf("%w: identity context exceeds tool cursor capacity", core.ErrInvalid)
	}
	return encoded, nil
}

func (r *Repository) SearchTools(ctx context.Context, actor core.Actor, input core.ToolSearchInput, scope core.ToolVisibilityScope) (core.ToolPage, error) {
	input, err := core.NormalizeToolSearch(input, scope)
	if err != nil {
		return core.ToolPage{}, err
	}
	var upperParam, afterParam any
	var afterID string
	if input.Cursor != "" {
		cursor, err := decodeToolCursor(actor, input, scope)
		if err != nil {
			return core.ToolPage{}, err
		}
		upperParam, afterParam, afterID = cursor.Upper, cursor.AfterTime, cursor.AfterID
	}
	// Materialize only matching IDs and timestamps; large schemas are fetched
	// after pagination for registry pages only. Count, boundary and page share
	// a single statement snapshot.
	// strpos provides literal substring matching without SQL wildcard escaping.
	query := `
WITH boundary AS (
 SELECT COALESCE($4::timestamptz,statement_timestamp()) AS upper_bound
), filtered AS MATERIALIZED (
 SELECT t.id,t.created_at
 FROM tools t CROSS JOIN boundary b
 WHERE t.workspace_id=$1
   AND ($2 OR (t.status='published' AND t.enabled AND (t.definition->'mcp'->>'server_id' IS NULL OR EXISTS (SELECT 1 FROM mcp_servers ms WHERE ms.workspace_id=t.workspace_id AND ms.id=t.definition->'mcp'->>'server_id' AND ms.enabled))))
   AND ($3='' OR strpos(lower(t.name || ' ' || COALESCE(t.definition->>'description','')),lower($3))>0)
   AND t.created_at<=b.upper_bound
   AND ` + clientToolAccessSQL("t", "$9", "$10") + `
), page AS (
 SELECT id,created_at FROM filtered
 WHERE $5::timestamptz IS NULL OR (created_at,id)<($5::timestamptz,$6::text)
 ORDER BY created_at DESC,id DESC LIMIT $7
)
SELECT (SELECT upper_bound FROM boundary),
       (SELECT count(*) FROM filtered),
       COALESCE((
         SELECT jsonb_agg((CASE WHEN $8 THEN t.definition
           ELSE jsonb_build_object('description',t.definition->>'description') END) || jsonb_build_object(
           'id',t.id,'workspace_id',t.workspace_id,'name',t.name,'risk',t.risk,
           'status',t.status,'enabled',(t.enabled AND (t.definition->'mcp'->>'server_id' IS NULL OR EXISTS (SELECT 1 FROM mcp_servers ms WHERE ms.workspace_id=t.workspace_id AND ms.id=t.definition->'mcp'->>'server_id' AND ms.enabled))),'version',t.version,'created_at',t.created_at
         ) ORDER BY p.created_at DESC,p.id DESC)
         FROM page p JOIN tools t ON t.workspace_id=$1 AND t.id=p.id
       ),'[]'::jsonb)`
	showUnpublished := scope == core.ToolScopeRegistry && actor.Role == core.RoleAdmin
	var upper time.Time
	var payload []byte
	page := core.ToolPage{Items: []core.Tool{}}
	if err := r.pool.QueryRow(ctx, query, actor.WorkspaceID, showUnpublished, input.Query, upperParam, afterParam, afterID, input.Limit+1, scope == core.ToolScopeRegistry, actor.ClientID, actor.ClientKeyID).Scan(&upper, &page.Total, &payload); err != nil {
		return page, dbError(err)
	}
	if err := json.Unmarshal(payload, &page.Items); err != nil {
		return core.ToolPage{}, fmt.Errorf("decode tool search page: %w", err)
	}
	if len(page.Items) > input.Limit {
		page.Items = page.Items[:input.Limit]
		page.NextCursor, err = encodeToolCursor(actor, input, scope, upper, page.Items[len(page.Items)-1])
		if err != nil {
			return core.ToolPage{}, err
		}
	}
	return page, nil
}
