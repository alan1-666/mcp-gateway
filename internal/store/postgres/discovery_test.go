package postgres_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/alan1-666/mcp-gateway/internal/store/postgres"
)

// Seed bounded search inventories directly; execution/registration are covered
// separately, and this keeps >500-record regression tests fast.
func (f fixture) seedSearchTools(t *testing.T, count int, description string, created time.Time) []string {
	t.Helper()
	ctx := context.Background()
	definition, err := json.Marshal(core.ToolInput{Name: "seed", Description: description, Risk: core.RiskRead, InputSchema: json.RawMessage(`{"type":"object"}`), HTTP: core.HTTPConfig{URL: "https://example.com", Method: "GET", TimeoutMS: 1000}})
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, count)
	for i := range ids {
		ids[i] = core.NewID()
	}
	_, err = f.pool.Exec(ctx, `INSERT INTO tools(workspace_id,id,name,risk,status,enabled,version,definition,created_at)
 SELECT $1,item,'seed_'||item,'read','published',true,1,$3,$4 FROM unnest($2::text[]) item`, f.admin.WorkspaceID, ids, definition, created)
	if err != nil {
		t.Fatal(err)
	}
	return ids
}

func TestCatalogQueryOmitsHeavyToolConfiguration(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	schema, err := json.Marshal(map[string]any{"type": "object", "description": strings.Repeat("schema documentation ", 1500)})
	if err != nil {
		t.Fatal(err)
	}
	tool, err := f.svc.CreateTool(ctx, f.admin, core.ToolInput{Name: "large_contract", Description: "Read the status", Risk: core.RiskRead, InputSchema: schema, OutputSchema: schema, HTTP: core.HTTPConfig{URL: "https://example.com/status", Method: "GET", CredentialRef: "PRIVATE_REF"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.PublishTool(ctx, f.admin, tool.ID); err != nil {
		t.Fatal(err)
	}
	repo := postgres.New(f.pool)
	catalog, err := repo.SearchTools(ctx, f.admin, core.ToolSearchInput{}, core.ToolScopeCatalog)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Items) != 1 {
		t.Fatalf("unexpected catalog %+v", catalog)
	}
	item := catalog.Items[0]
	if len(item.InputSchema) != 0 || len(item.OutputSchema) != 0 || item.HTTP != (core.HTTPConfig{}) {
		t.Fatal("catalog query fetched schemas or HTTP configuration")
	}
	if item.ID != tool.ID || item.Description != tool.Description || item.Name != tool.Name || item.Version != 1 {
		t.Fatalf("catalog summary metadata missing: %+v", item)
	}
	registry, err := repo.SearchTools(ctx, f.admin, core.ToolSearchInput{}, core.ToolScopeRegistry)
	if err != nil {
		t.Fatal(err)
	}
	if len(registry.Items) != 1 || len(registry.Items[0].InputSchema) < 30000 || len(registry.Items[0].OutputSchema) < 30000 || registry.Items[0].HTTP.CredentialRef != "PRIVATE_REF" {
		t.Fatal("registry lost full configuration")
	}
}

func TestDiscoverySearchesBeyondFiveHundredAndTraversesTies(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	oldTime := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Microsecond)
	old := f.seedSearchTools(t, 1, "Older 中文订单 MiXeDCaSe unique-needle", oldTime)[0]
	tied := f.seedSearchTools(t, 523, "Newer inventory", oldTime.Add(time.Hour))
	page, err := f.svc.DiscoverTools(ctx, f.admin, core.ToolSearchInput{Query: "UNIQUE-NEEDLE"})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != old {
		t.Fatalf("old match missing: %+v", page)
	}
	for _, query := range []string{"中文订单", "mixedcase"} {
		p, err := f.svc.DiscoverTools(ctx, f.operator, core.ToolSearchInput{Query: query})
		if err != nil || p.Total != 1 || p.Items[0].ID != old {
			t.Fatalf("query %q: %+v %v", query, p, err)
		}
	}
	var found []string
	cursor := ""
	for {
		p, err := f.svc.DiscoverTools(ctx, f.admin, core.ToolSearchInput{Limit: 50, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		if p.Total != 524 {
			t.Fatalf("page total %d", p.Total)
		}
		if len(p.Items) > 50 {
			t.Fatal("page exceeds limit")
		}
		for _, item := range p.Items {
			found = append(found, item.ID)
		}
		cursor = p.NextCursor
		if cursor == "" {
			break
		}
		if len(found) > 524 {
			t.Fatal("cursor failed to advance")
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(tied)))
	expected := append(tied, old)
	if len(found) != len(expected) {
		t.Fatalf("traversed %d of %d", len(found), len(expected))
	}
	for i := range found {
		if found[i] != expected[i] {
			t.Fatalf("unstable order or duplicate at %d: got %s want %s", i, found[i], expected[i])
		}
	}
}

func TestDiscoveryVisibilityLiteralMatchingAndBoundary(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	created := time.Now().Add(-time.Hour).UTC().Truncate(time.Microsecond)
	visible := f.seedSearchTools(t, 4, "metrics 50% exact_under path\\segment 中文", created)
	draft := f.seedSearchTools(t, 1, "draft should be hidden", created)[0]
	disabled := f.seedSearchTools(t, 1, "disabled should be hidden", created)[0]
	if _, err := f.pool.Exec(ctx, `UPDATE tools SET status='draft',enabled=false WHERE workspace_id=$1 AND id=$2`, f.admin.WorkspaceID, draft); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE tools SET enabled=false WHERE workspace_id=$1 AND id=$2`, f.admin.WorkspaceID, disabled); err != nil {
		t.Fatal(err)
	}
	for _, actor := range []core.Actor{f.admin, f.operator, f.approver, {ID: "viewer", WorkspaceID: f.admin.WorkspaceID, Role: core.RoleViewer}} {
		page, err := f.svc.DiscoverTools(ctx, actor, core.ToolSearchInput{})
		if err != nil || page.Total != 4 {
			t.Fatalf("catalog visibility %+v %+v %v", actor, page, err)
		}
	}
	registry, err := f.svc.SearchTools(ctx, f.admin, core.ToolSearchInput{})
	if err != nil || registry.Total != 6 {
		t.Fatalf("admin registry %+v %v", registry, err)
	}
	registry, err = f.svc.SearchTools(ctx, f.operator, core.ToolSearchInput{})
	if err != nil || registry.Total != 4 {
		t.Fatalf("operator registry %+v %v", registry, err)
	}
	foreign := f.admin
	foreign.WorkspaceID = core.NewID()
	page, err := f.svc.DiscoverTools(ctx, foreign, core.ToolSearchInput{})
	if err != nil || page.Total != 0 || len(page.Items) != 0 {
		t.Fatalf("foreign visibility %+v %v", page, err)
	}
	for _, query := range []string{"50%", "exact_under", `path\segment`} {
		page, err := f.svc.DiscoverTools(ctx, f.admin, core.ToolSearchInput{Query: query})
		if err != nil || page.Total != 4 {
			t.Fatalf("literal query %q %+v %v", query, page, err)
		}
	}
	for _, query := range []string{"50_", "exact%under", `path_segment`, "absent"} {
		page, err := f.svc.DiscoverTools(ctx, f.admin, core.ToolSearchInput{Query: query})
		if err != nil || page.Total != 0 || len(page.Items) != 0 || page.NextCursor != "" {
			t.Fatalf("literal nonmatch %q %+v %v", query, page, err)
		}
	}
	first, err := f.svc.DiscoverTools(ctx, f.admin, core.ToolSearchInput{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if first.NextCursor == "" {
		t.Fatal("missing cursor")
	}
	// Insert after the first statement's time boundary. It must be visible to a
	// fresh query, but cannot slide into the old traversal or its total.
	newID := f.seedSearchTools(t, 1, "new record", time.Now().UTC())[0]
	var disableID string
	for _, id := range visible {
		if id != first.Items[0].ID {
			disableID = id
			break
		}
	}
	if _, err := f.svc.SetToolEnabled(ctx, f.admin, disableID, false); err != nil {
		t.Fatal(err)
	}
	next, err := f.svc.DiscoverTools(ctx, f.admin, core.ToolSearchInput{Limit: 50, Cursor: first.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	if next.Total != 3 || len(next.Items) != 2 {
		t.Fatalf("boundary/live disable %+v", next)
	}
	for _, item := range next.Items {
		if item.ID == newID || item.ID == disableID || item.ID == first.Items[0].ID {
			t.Fatalf("unexpected later item %+v", item)
		}
	}
	fresh, err := f.svc.DiscoverTools(ctx, f.admin, core.ToolSearchInput{})
	if err != nil || fresh.Total != 4 {
		t.Fatalf("fresh boundary %+v %v", fresh, err)
	}
}

func TestDiscoveryCursorContextAndBounds(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	f.seedSearchTools(t, 3, "match", time.Now().Add(-time.Hour))
	first, err := f.svc.DiscoverTools(ctx, f.admin, core.ToolSearchInput{Query: " match ", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		actor    core.Actor
		input    core.ToolSearchInput
		registry bool
	}{
		{"workspace", core.Actor{ID: f.admin.ID, WorkspaceID: "elsewhere", Role: core.RoleAdmin}, core.ToolSearchInput{Query: "match", Cursor: first.NextCursor}, false},
		{"actor", core.Actor{ID: "someone-else", WorkspaceID: f.admin.WorkspaceID, Role: core.RoleAdmin}, core.ToolSearchInput{Query: "match", Cursor: first.NextCursor}, false},
		{"role", core.Actor{ID: f.admin.ID, WorkspaceID: f.admin.WorkspaceID, Role: core.RoleOperator}, core.ToolSearchInput{Query: "match", Cursor: first.NextCursor}, false},
		{"query", f.admin, core.ToolSearchInput{Query: "changed", Cursor: first.NextCursor}, false},
		{"scope", f.admin, core.ToolSearchInput{Query: "match", Cursor: first.NextCursor}, true},
		{"malformed", f.admin, core.ToolSearchInput{Query: "match", Cursor: "not-base64!"}, false},
		{"oversized", f.admin, core.ToolSearchInput{Query: "match", Cursor: strings.Repeat("x", 2049)}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var err error
			if tc.registry {
				_, err = f.svc.SearchTools(ctx, tc.actor, tc.input)
			} else {
				_, err = f.svc.DiscoverTools(ctx, tc.actor, tc.input)
			}
			if !errors.Is(err, core.ErrInvalid) {
				t.Fatalf("cursor accepted: %v", err)
			}
		})
	}
	// Limit is deliberately not bound, and normalized query whitespace is stable.
	second, err := f.svc.DiscoverTools(ctx, f.admin, core.ToolSearchInput{Query: "  match", Cursor: first.NextCursor, Limit: 2})
	if err != nil || len(second.Items) != 2 || second.NextCursor != "" {
		t.Fatalf("changed limit %+v %v", second, err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(first.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	var original map[string]any
	if err := json.Unmarshal(raw, &original); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(map[string]any){
		func(c map[string]any) { c["v"] = 3 },
		func(c map[string]any) { c["v"] = 1 },
		func(c map[string]any) { c["after_score"] = -1 },
		func(c map[string]any) { c["after_score"] = 501 },
		func(c map[string]any) { c["after_score"] = 123 },
		func(c map[string]any) { c["after_score"] = 0 },
		func(c map[string]any) { c["client_id"] = "other" },
		func(c map[string]any) { c["client_key_id"] = "other" },
		func(c map[string]any) { c["after_id"] = "" },
		func(c map[string]any) { c["after_time"] = "2099-01-01T00:00:00Z" },
		func(c map[string]any) { c["upper"] = "2099-01-01T00:00:00Z" },
		func(c map[string]any) { c["unexpected"] = true },
	} {
		copy := map[string]any{}
		for key, value := range original {
			copy[key] = value
		}
		change(copy)
		body, err := json.Marshal(copy)
		if err != nil {
			t.Fatal(err)
		}
		_, err = f.svc.DiscoverTools(ctx, f.admin, core.ToolSearchInput{Query: "match", Cursor: base64.RawURLEncoding.EncodeToString(body)})
		if !errors.Is(err, core.ErrInvalid) {
			t.Fatalf("invalid cursor structure accepted: %s (%v)", fmt.Sprint(copy), err)
		}
	}
}
