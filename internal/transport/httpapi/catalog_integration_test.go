package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/alan1-666/mcp-gateway/internal/adapters/httpadapter"
	"github.com/alan1-666/mcp-gateway/internal/adapters/mcpadapter"
	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/alan1-666/mcp-gateway/internal/execution"
	"github.com/alan1-666/mcp-gateway/internal/identity"
	"github.com/alan1-666/mcp-gateway/internal/releases"
	"github.com/alan1-666/mcp-gateway/internal/store/postgres"
	"github.com/alan1-666/mcp-gateway/internal/upstreams"
	"github.com/alan1-666/mcp-gateway/migrations"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// This exercises real SDK discovery over HTTP, the public management routes,
// PostgreSQL history/candidates, explicit publication and one business call.
func TestCatalogReviewLifecycleWithRealMCP(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := migrations.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	a := core.Actor{ID: core.NewID(), WorkspaceID: core.NewID(), Role: core.RoleAdmin}
	defer func() {
		for _, table := range []string{"operation_events", "audit_events", "operations", "tools", "mcp_servers"} {
			if _, err := pool.Exec(ctx, "DELETE FROM "+table+" WHERE workspace_id=$1", a.WorkspaceID); err != nil {
				t.Error(err)
			}
		}
	}()
	var active atomic.Pointer[mcp.Server]
	var calls atomic.Int32
	var failed atomic.Bool
	setContract := func(schema string) {
		sdk := mcp.NewServer(&mcp.Implementation{Name: "catalog-fixture", Version: "1"}, nil)
		if schema != "" {
			sdk.AddTool(&mcp.Tool{Name: "lookup", Description: "Find a record", InputSchema: json.RawMessage(schema)}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				calls.Add(1)
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: `{"ok":true,"detail":"extra"}`}}, StructuredContent: json.RawMessage(`{"ok":true,"detail":"extra"}`)}, nil
			})
		}
		active.Store(sdk)
	}
	v1 := `{"type":"object","properties":{},"additionalProperties":false}`
	v2 := `{"type":"object","properties":{"query":{"type":"string"}},"required":["query"],"additionalProperties":false}`
	setContract(v1)
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return active.Load() }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failed.Load() {
			http.Error(w, "unavailable", 503)
			return
		}
		mcpHandler.ServeHTTP(w, r)
	}))
	defer remote.Close()
	egress, err := httpadapter.New([]string{remote.URL}, []string{"127.0.0.0/8"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	adapter := mcpadapter.New(egress, upstreams.NewStore(pool))
	up := upstreams.New(upstreams.NewStore(pool), adapter)
	service := core.NewService(postgres.New(pool))
	server, err := up.Create(ctx, a, core.MCPServerInput{Name: "Catalog lifecycle", Namespace: "catalog", URL: remote.URL, TimeoutMS: 2000})
	if err != nil {
		t.Fatal(err)
	}
	operator := a
	operator.ID, operator.Role = core.NewID(), core.RoleOperator
	foreign := a
	foreign.WorkspaceID = core.NewID()
	at, ot, ft := strings.Repeat("a", 32), strings.Repeat("o", 32), strings.Repeat("f", 32)
	auth, err := identity.New([]identity.Token{{Token: at, Actor: a}, {Token: ot, Actor: operator}, {Token: ft, Actor: foreign}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	api := (&API{Service: service, Upstreams: up, Releases: releases.New(pool, adapter, egress), Executor: &execution.Executor{Service: service, Adapter: adapter}}).Handler(auth)
	call := func(method, path, token string, body any, status int, out any) {
		t.Helper()
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(string(raw)))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		api.ServeHTTP(rec, req)
		if (status != 0 && rec.Code != status) || (status == 0 && rec.Code < 400) {
			t.Fatalf("%s %s: %d want %d: %s", method, path, rec.Code, status, rec.Body)
		}
		if out != nil {
			if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
				t.Fatal(err)
			}
		}
	}
	base := "/mcp/servers/" + server.ID
	var initial upstreams.DiscoveryPage
	call("POST", base+"/discover", at, map[string]any{}, 200, &initial)
	if initial.Review.Counts.Unimported != 1 || calls.Load() != 0 {
		t.Fatal("discovery invoked business tool or missed unimported tool")
	}
	var tool core.Tool
	call("POST", base+"/import", at, upstreams.ImportInput{ToolName: "lookup", SchemaHash: initial.Items[0].SchemaHash, Risk: core.RiskRead, ResponsePolicy: &core.ResponsePolicy{Include: []string{"/ok"}, MaxBytes: 1024}}, 200, &tool)
	toolPath := "/tools/" + tool.ID
	call("POST", toolPath+"/publish", at, map[string]any{}, 200, &tool)
	var pinned core.Operation
	call("POST", "/operations", ot, core.PrepareInput{ToolID: tool.ID, Arguments: json.RawMessage(`{}`), IdempotencyKey: "before-schema-change"}, 200, &pinned)
	setContract(v2)
	var changed upstreams.DiscoveryPage
	call("POST", base+"/discover", at, map[string]any{}, 200, &changed)
	if changed.Review.Counts.SchemaChanged != 1 {
		t.Fatal("schema drift not identified")
	}
	in := releases.CandidateInput{ExpectedVersion: 1, ExpectedSchemaHash: initial.Items[0].SchemaHash, Reason: "Review required query"}
	call("POST", toolPath+"/candidates", at, in, 409, nil)
	in.ExpectedSchemaHash = "invalid"
	call("POST", toolPath+"/candidates", at, in, 400, nil)
	in.ExpectedSchemaHash = changed.Items[0].SchemaHash
	call("POST", toolPath+"/candidates", ot, in, 403, nil)
	call("POST", toolPath+"/candidates", ft, in, 404, nil)
	var candidate releases.Candidate
	call("POST", toolPath+"/candidates", at, in, 200, &candidate)
	if candidate.Definition.Risk != core.RiskRead || candidate.Definition.ResponsePolicy.Include[0] != "/ok" || len(candidate.Changes) != 2 {
		t.Fatal("refresh lost policy or incorrect diff")
	}
	call("GET", toolPath, at, nil, 200, &tool)
	if tool.Version != 1 {
		t.Fatal("candidate automatically published")
	}
	setContract(v1)
	call("POST", toolPath+"/candidates/"+candidate.ID+"/publish", at, map[string]int{"expected_version": 1}, 409, nil)
	setContract(v2)
	call("POST", toolPath+"/candidates/"+candidate.ID+"/publish", at, map[string]int{"expected_version": 1}, 200, &tool)
	if tool.Version != 2 {
		t.Fatal("no new immutable version")
	}
	call("POST", toolPath+"/candidates", at, in, 409, nil)
	var current upstreams.DiscoveryPage
	call("POST", base+"/discover", at, map[string]any{}, 200, &current)
	if current.Review.Counts.Unchanged != 1 {
		t.Fatal("published tool still drifted")
	}
	var snapshot []byte
	if err := pool.QueryRow(ctx, `SELECT tool_snapshot FROM operations WHERE workspace_id=$1 AND id=$2`, a.WorkspaceID, pinned.ID).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	var old core.Tool
	if err := json.Unmarshal(snapshot, &old); err != nil || old.Version != 1 || old.MCP.SchemaHash != initial.Items[0].SchemaHash {
		t.Fatal("prepared snapshot overwritten", err)
	}
	if calls.Load() != 0 {
		t.Fatal("review or publication called a business tool")
	}
	var op core.Operation
	call("POST", "/operations", ot, core.PrepareInput{ToolID: tool.ID, Arguments: json.RawMessage(`{"query":"record"}`), IdempotencyKey: "after-schema-change"}, 200, &op)
	call("POST", "/operations/"+op.ID+"/execute", ot, map[string]any{}, 200, &op)
	if op.State != core.StateSucceeded || calls.Load() != 1 || strings.Contains(string(op.Result), "extra") {
		t.Fatalf("refreshed tool execution %+v calls %d", op, calls.Load())
	}
	call("GET", base+"/catalog-reviews", "", nil, 401, nil)
	call("GET", base+"/catalog-reviews", ot, nil, 403, nil)
	call("GET", base+"/catalog-reviews", ft, nil, 404, nil)
	for _, query := range []string{"limit=0", "limit=51", "limit=1&limit=2", "before=-1", "before=%GG", "unknown=x"} {
		call("GET", base+"/catalog-reviews?"+query, at, nil, 400, nil)
	}
	var history upstreams.CatalogReviewPage
	failed.Store(true)
	call("POST", base+"/discover", at, map[string]any{}, 0, nil)
	call("GET", base+"/catalog-reviews?limit=1", at, nil, 200, &history)
	if len(history.Items) != 1 || history.Items[0].ID != current.Review.ID || history.NextCursor == "" {
		t.Fatal("failed discovery replaced last success")
	}
	failed.Store(false)
	setContract("")
	call("POST", base+"/discover", at, map[string]any{}, 200, &current)
	if current.Review.Counts.Missing != 1 {
		t.Fatal("complete empty catalog did not show missing tool")
	}
	call("GET", toolPath, at, nil, 200, &tool)
	if tool.Status != "published" || tool.Version != 2 {
		t.Fatal("discovery retired tool without review")
	}
}
