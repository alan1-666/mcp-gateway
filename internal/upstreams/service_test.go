package upstreams

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/alan1-666/mcp-gateway/internal/store/postgres"
	"github.com/alan1-666/mcp-gateway/migrations"
	"github.com/jackc/pgx/v5/pgxpool"
)

type fakeRemote struct {
	items []core.RemoteTool
	calls int
	err   error
}

func (f *fakeRemote) ValidateServer(core.Actor, core.MCPServer) error { return f.err }
func (f *fakeRemote) Discover(context.Context, core.Actor, core.MCPServer) ([]core.RemoteTool, error) {
	f.calls++
	return append([]core.RemoteTool(nil), f.items...), f.err
}
func fixture(t *testing.T) (context.Context, *pgxpool.Pool, *Service, *fakeRemote, core.Actor) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := migrations.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	a := core.Actor{ID: core.NewID(), WorkspaceID: core.NewID(), Role: core.RoleAdmin}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for _, table := range []string{"operation_events", "audit_events", "operations", "tools", "mcp_servers"} {
			if _, err := pool.Exec(ctx, "DELETE FROM "+table+" WHERE workspace_id=$1", a.WorkspaceID); err != nil {
				t.Errorf("cleanup %s: %v", table, err)
			}
		}
	})
	remote := &fakeRemote{items: []core.RemoteTool{{Name: "get.weather", Description: "Weather query", InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`), ReadOnlyHint: true, SchemaHash: strings.Repeat("a", 64)}}}
	return ctx, pool, New(NewStore(pool), remote), remote, a
}
func server(t *testing.T, ctx context.Context, s *Service, a core.Actor) core.MCPServer {
	t.Helper()
	v, err := s.Create(ctx, a, core.MCPServerInput{Name: "Weather", Namespace: "weather", URL: "https://mcp.example.com/mcp"})
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func TestValidationAndAliases(t *testing.T) {
	valid := core.MCPServerInput{Name: "天气", Namespace: "weather_2", URL: "https://mcp.example.com/mcp"}
	normalized, err := normalize(valid)
	if err != nil || normalized.TimeoutMS != 10000 {
		t.Fatalf("normalize %+v %v", normalized, err)
	}
	cases := []core.MCPServerInput{{Name: "", Namespace: "weather", URL: valid.URL}, {Name: "bad\nname", Namespace: "weather", URL: valid.URL}, {Name: "天气", Namespace: "Upper", URL: valid.URL}, {Name: "name", Namespace: "x", URL: "https://a:b@mcp.example.com/mcp"}, {Name: "name", Namespace: "x", URL: "https://mcp.example.com/mcp?token=secret"}, {Name: "name", Namespace: "x", URL: "https://mcp.example.com/mcp#fragment"}, {Name: "name", Namespace: "x", URL: valid.URL, TimeoutMS: 99}, {Name: "name", Namespace: "x", URL: valid.URL, CredentialRef: "raw-token"}}
	for _, in := range cases {
		if _, err := normalize(in); !errors.Is(err, core.ErrInvalid) {
			t.Fatalf("accepted %+v %v", in, err)
		}
	}
	seen := map[string]bool{}
	for _, name := range []string{"get:weather", "get/weather", "天气", strings.Repeat("long.name", 50)} {
		alias := GatewayName(strings.Repeat("a", 24), name)
		if len(alias) > 64 || seen[alias] {
			t.Fatalf("bad alias %s", alias)
		}
		seen[alias] = true
		if alias != GatewayName(strings.Repeat("a", 24), name) {
			t.Fatal("unstable alias")
		}
	}
}
func TestImportIsReviewedIdempotentAndWorkspaceScoped(t *testing.T) {
	ctx, pool, s, remote, a := fixture(t)
	v := server(t, ctx, s, a)
	operator := a
	operator.Role = core.RoleOperator
	for _, role := range []core.Role{core.RoleOperator, core.RoleApprover, core.RoleViewer} {
		u := a
		u.Role = role
		if _, err := s.List(ctx, u); !errors.Is(err, core.ErrForbidden) {
			t.Fatal("non-admin listed server", err)
		}
		if _, err := s.Discover(ctx, u, v.ID); !errors.Is(err, core.ErrForbidden) {
			t.Fatal("non-admin discovered", err)
		}
		if _, err := s.Create(ctx, u, v.MCPServerInput); !errors.Is(err, core.ErrForbidden) {
			t.Fatal("non-admin create", err)
		}
		if _, err := s.SetEnabled(ctx, u, v.ID, false); !errors.Is(err, core.ErrForbidden) {
			t.Fatal("non-admin toggle", err)
		}
		if _, err := s.Import(ctx, u, v.ID, ImportInput{}); !errors.Is(err, core.ErrForbidden) {
			t.Fatal("non-admin import", err)
		}
	}
	other := a
	other.WorkspaceID = core.NewID()
	if _, err := s.Discover(ctx, other, v.ID); !errors.Is(err, core.ErrNotFound) {
		t.Fatal("cross-workspace discovery", err)
	}
	if _, err := s.SetEnabled(ctx, other, v.ID, false); !errors.Is(err, core.ErrNotFound) {
		t.Fatal("cross-workspace toggle", err)
	}
	page, err := s.List(ctx, other)
	if err != nil || page.Total != 0 {
		t.Fatalf("cross-workspace list %+v %v", page, err)
	}
	if _, err := s.Create(ctx, a, v.MCPServerInput); !errors.Is(err, core.ErrConflict) {
		t.Fatal("duplicate namespace", err)
	}
	discovered, err := s.Discover(ctx, a, v.ID)
	if err != nil || discovered.Total != 1 || discovered.Items[0].GatewayName == "" || discovered.Items[0].ImportedToolID != "" {
		t.Fatalf("discovery %+v %v", discovered, err)
	}
	input := ImportInput{ToolName: remote.items[0].Name, SchemaHash: remote.items[0].SchemaHash, Risk: core.RiskWrite, ResponsePolicy: &core.ResponsePolicy{Include: []string{"/weather"}, MaxBytes: 4096}}
	tool, err := s.Import(ctx, a, v.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if tool.Enabled || tool.Status != "draft" || tool.Risk != core.RiskWrite || tool.MCP == nil || tool.ResponsePolicy == nil {
		t.Fatalf("unreviewed import %+v", tool)
	}
	svc := core.NewService(postgres.New(pool))
	if _, err := svc.GetTool(ctx, operator, tool.ID); !errors.Is(err, core.ErrNotFound) {
		t.Fatal("draft visible to operator", err)
	}
	again, err := s.Import(ctx, a, v.ID, input)
	if err != nil || again.ID != tool.ID {
		t.Fatalf("not idempotent %+v %v", again, err)
	}
	discovered, err = s.Discover(ctx, a, v.ID)
	if err != nil || discovered.Items[0].ImportedToolID != tool.ID {
		t.Fatalf("no import linkage %+v %v", discovered, err)
	}
	changed := input
	changed.Risk = core.RiskRead
	if _, err := s.Import(ctx, a, v.ID, changed); !errors.Is(err, core.ErrConflict) {
		t.Fatal("silently changed risk", err)
	}
	changed = input
	changed.ResponsePolicy = &core.ResponsePolicy{MaxBytes: 8192}
	if _, err := s.Import(ctx, a, v.ID, changed); !errors.Is(err, core.ErrConflict) {
		t.Fatal("silently changed projection", err)
	}
	remote.items[0].SchemaHash = strings.Repeat("b", 64)
	if _, err := s.Import(ctx, a, v.ID, input); !errors.Is(err, core.ErrConflict) {
		t.Fatal("stale schema accepted", err)
	}
	changed = input
	changed.SchemaHash = remote.items[0].SchemaHash
	if _, err := s.Import(ctx, a, v.ID, changed); !errors.Is(err, core.ErrConflict) {
		t.Fatal("import overwrote contract", err)
	}
	stored, err := svc.GetTool(ctx, a, tool.ID)
	if err != nil || stored.MCP.SchemaHash != input.SchemaHash {
		t.Fatal("old contract mutated", err)
	}
	remote.items = nil
	if _, err := s.Import(ctx, a, v.ID, input); !errors.Is(err, core.ErrConflict) {
		t.Fatal("missing remote tool accepted", err)
	}
	if remote.calls < 7 {
		t.Fatal("import did not rediscover current contract")
	}
}
func TestServerDisableFencesCatalogApprovalAndDispatch(t *testing.T) {
	ctx, pool, s, remote, a := fixture(t)
	v := server(t, ctx, s, a)
	svc := core.NewService(postgres.New(pool))
	operator := a
	operator.ID = core.NewID()
	operator.Role = core.RoleOperator
	approver := a
	approver.ID = core.NewID()
	approver.Role = core.RoleApprover
	in := ImportInput{ToolName: remote.items[0].Name, SchemaHash: remote.items[0].SchemaHash, Risk: core.RiskWrite}
	tool, err := s.Import(ctx, a, v.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	tool, err = svc.PublishTool(ctx, a, tool.ID)
	if err != nil {
		t.Fatal(err)
	}
	prepare := func() core.Operation {
		op, err := svc.Prepare(ctx, operator, core.PrepareInput{ToolID: tool.ID, Arguments: json.RawMessage(`{}`), IdempotencyKey: core.NewID()})
		if err != nil {
			t.Fatal(err)
		}
		return op
	}
	ready := prepare()
	if _, err := svc.Approve(ctx, approver, ready.ID); err != nil {
		t.Fatal(err)
	}
	waiting := prepare()
	if _, err := s.SetEnabled(ctx, a, v.ID, false); err != nil {
		t.Fatal(err)
	}
	for _, actor := range []core.Actor{a, operator} {
		p, err := svc.DiscoverTools(ctx, actor, core.ToolSearchInput{})
		if err != nil || p.Total != 0 {
			t.Fatalf("disabled upstream in catalog %+v %v", p, err)
		}
	}
	p, err := svc.SearchTools(ctx, operator, core.ToolSearchInput{})
	if err != nil || p.Total != 0 {
		t.Fatalf("disabled upstream in nonadmin registry %+v %v", p, err)
	}
	p, err = svc.SearchTools(ctx, a, core.ToolSearchInput{})
	if err != nil || p.Total != 1 || p.Items[0].Enabled {
		t.Fatalf("admin metadata not effective %+v %v", p, err)
	}
	got, err := svc.GetTool(ctx, a, tool.ID)
	if err != nil || got.Enabled {
		t.Fatal("admin get effective enabled", err)
	}
	if _, err := svc.GetTool(ctx, operator, tool.ID); !errors.Is(err, core.ErrNotFound) {
		t.Fatal("disabled tool visible", err)
	}
	if _, err := svc.Prepare(ctx, operator, core.PrepareInput{ToolID: tool.ID, Arguments: json.RawMessage(`{}`), IdempotencyKey: core.NewID()}); !errors.Is(err, core.ErrConflict) {
		t.Fatal("disabled prepare", err)
	}
	if _, err := svc.Approve(ctx, approver, waiting.ID); !errors.Is(err, core.ErrConflict) {
		t.Fatal("disabled approval", err)
	}
	if _, _, granted, err := svc.Claim(ctx, operator, ready.ID); granted || !errors.Is(err, core.ErrConflict) {
		t.Fatalf("disabled dispatch %v %v", granted, err)
	}
	if _, err := svc.PublishTool(ctx, a, tool.ID); !errors.Is(err, core.ErrConflict) {
		t.Fatal("disabled publish", err)
	}
	if _, err := svc.SetToolEnabled(ctx, a, tool.ID, true); !errors.Is(err, core.ErrConflict) {
		t.Fatal("disabled enable", err)
	}
	if _, err := s.Discover(ctx, a, v.ID); !errors.Is(err, core.ErrConflict) {
		t.Fatal("disabled discover", err)
	}
	if _, err := s.Import(ctx, a, v.ID, in); !errors.Is(err, core.ErrConflict) {
		t.Fatal("disabled import", err)
	}
	if _, err := s.SetEnabled(ctx, a, v.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, snapshot, granted, err := svc.Claim(ctx, operator, ready.ID); err != nil || !granted || snapshot.MCP == nil || snapshot.MCP.SchemaHash != in.SchemaHash || snapshot.ResponsePolicy == nil {
		t.Fatalf("reactivation with snapshot %v %+v %v", granted, snapshot, err)
	}
	// A tool-specific disable must survive a server cycle.
	if _, err := svc.SetToolEnabled(ctx, a, tool.ID, false); err != nil {
		t.Fatal(err)
	}
	_, _ = s.SetEnabled(ctx, a, v.ID, false)
	_, _ = s.SetEnabled(ctx, a, v.ID, true)
	got, err = svc.GetTool(ctx, a, tool.ID)
	if err != nil || got.Enabled {
		t.Fatal("tool disable lost", err)
	}
}
func TestLimitsDescriptionAndConcurrentImport(t *testing.T) {
	ctx, pool, s, remote, a := fixture(t)
	v := server(t, ctx, s, a)
	remote.items[0].Description = strings.Repeat("测", 1500)
	in := ImportInput{ToolName: remote.items[0].Name, SchemaHash: remote.items[0].SchemaHash, Risk: core.RiskRead}
	tool, err := s.Import(ctx, a, v.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(tool.Description) > 4000 || !utf8.ValidString(tool.Description) {
		t.Fatal("description not safely bounded")
	}
	// Test store contention independently of the intentionally simple fake remote.
	normalized := core.ToolInput{Name: tool.Name, Description: tool.Description, Risk: tool.Risk, InputSchema: tool.InputSchema, MCP: tool.MCP, ResponsePolicy: tool.ResponsePolicy}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := s.store.importTool(ctx, a, normalized)
			if err != nil || got.ID != tool.ID {
				t.Errorf("concurrent retry %+v %v", got, err)
			}
		}()
	}
	wg.Wait()
	_, err = pool.Exec(ctx, `INSERT INTO mcp_servers(workspace_id,id,name,namespace,url,timeout_ms) SELECT $1, 'capacity-'||g,'Capacity','capacity_'||g,'https://mcp.example.com/mcp',10000 FROM generate_series(1,99)g`, a.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	page, err := s.List(ctx, a)
	if err != nil || page.Total != 100 {
		t.Fatalf("bounded list %+v %v", page, err)
	}
	if _, err := s.Create(ctx, a, core.MCPServerInput{Name: "Overflow", Namespace: "overflow", URL: "https://mcp.example.com/mcp"}); !errors.Is(err, core.ErrConflict) {
		t.Fatal("capacity overflow accepted", err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO mcp_servers(workspace_id,id,name,namespace,url,timeout_ms) VALUES($1,$2,'Overflow','overflow','https://mcp.example.com/mcp',10000)`, a.WorkspaceID, core.NewID())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.List(ctx, a); !errors.Is(err, core.ErrConflict) {
		t.Fatal("silently truncated server list", err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM tools WHERE workspace_id=$1`, a.WorkspaceID).Scan(&count); err != nil || count != 1 {
		t.Fatal(fmt.Sprintf("duplicate imports %d %v", count, err))
	}
}

func TestCatalogPaginationAppliesServerVisibilityAndBoundaries(t *testing.T) {
	ctx, pool, s, remote, a := fixture(t)
	first := server(t, ctx, s, a)
	second, err := s.Create(ctx, a, core.MCPServerInput{Name: "Other", Namespace: "other", URL: "https://other.example.com/mcp"})
	if err != nil {
		t.Fatal(err)
	}
	svc := core.NewService(postgres.New(pool))
	operator := a
	operator.ID = core.NewID()
	operator.Role = core.RoleOperator
	var firstTool core.Tool
	for _, serverID := range []string{first.ID, second.ID} {
		tool, err := s.Import(ctx, a, serverID, ImportInput{ToolName: remote.items[0].Name, SchemaHash: remote.items[0].SchemaHash, Risk: core.RiskRead})
		if err != nil {
			t.Fatal(err)
		}
		tool, err = svc.PublishTool(ctx, a, tool.ID)
		if err != nil {
			t.Fatal(err)
		}
		if serverID == first.ID {
			firstTool = tool
		}
	}
	page, err := svc.DiscoverTools(ctx, operator, core.ToolSearchInput{Limit: 1})
	if err != nil || page.Total != 2 || page.NextCursor == "" {
		t.Fatalf("first page %+v %v", page, err)
	}
	if _, err := s.SetEnabled(ctx, a, first.ID, false); err != nil {
		t.Fatal(err)
	}
	next, err := svc.DiscoverTools(ctx, operator, core.ToolSearchInput{Limit: 1, Cursor: page.NextCursor})
	if err != nil || next.Total != 1 || len(next.Items) != 0 || next.NextCursor != "" {
		t.Fatalf("stale cursor after disable %+v %v", next, err)
	}
	// Cannot attach tools from another workspace, even through the repository.
	other := a
	other.WorkspaceID = core.NewID()
	_, err = postgres.New(pool).CreateTool(ctx, other, core.ToolInput{Name: firstTool.Name, Description: "foreign", Risk: core.RiskRead, InputSchema: firstTool.InputSchema, MCP: firstTool.MCP})
	if !errors.Is(err, core.ErrNotFound) {
		t.Fatal("cross-workspace MCP import accepted", err)
	}
}
