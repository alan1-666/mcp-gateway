package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alan1-666/mcp-gateway/internal/adapters/httpadapter"
	"github.com/alan1-666/mcp-gateway/internal/capacity"
	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/alan1-666/mcp-gateway/internal/execution"
	"github.com/alan1-666/mcp-gateway/internal/identity"
	"github.com/alan1-666/mcp-gateway/internal/store/postgres"
	"github.com/alan1-666/mcp-gateway/internal/transport/httpapi"
	"github.com/alan1-666/mcp-gateway/internal/transport/mcpserver"
	"github.com/alan1-666/mcp-gateway/migrations"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestCapacityAcrossRESTMCPExecutorAndReplay(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	base, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	schema := "capacity_http_" + strings.ReplaceAll(core.NewID(), "-", "")
	if _, err = base.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer base.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
	cfg, _ := pgxpool.ParseConfig(dsn)
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err = migrations.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	a := core.Actor{ID: "admin", WorkspaceID: "capacity", Role: core.RoleAdmin}
	token := strings.Repeat("admission", 8)
	auth, _ := identity.New([]identity.Token{{Token: token, Actor: a}}, nil)
	entered, unblock := make(chan struct{}, 1), make(chan struct{})
	var calls atomic.Int32
	downstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			entered <- struct{}{}
			select {
			case <-unblock:
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
	}))
	defer downstream.Close()
	adapter, _ := httpadapter.New([]string{downstream.URL}, []string{"127.0.0.0/8"}, nil)
	svc := core.NewService(postgres.New(pool))
	budgets := capacity.New(pool)
	executor := &execution.Executor{Service: svc, Adapter: adapter, Capacity: budgets}
	tool, err := svc.CreateTool(ctx, a, core.ToolInput{Name: "query", Description: "Blocking fixture", Risk: core.RiskRead, InputSchema: json.RawMessage(`{"type":"object"}`), HTTP: core.HTTPConfig{URL: downstream.URL, Method: "GET", TimeoutMS: 10000}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.PublishTool(ctx, a, tool.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = budgets.Update(ctx, a, capacity.UpdateInput{Scope: "workspace", ScopeID: "*", MaxConcurrent: 1, RequestsPerMinute: 600}); err != nil {
		t.Fatal(err)
	}
	first, err := svc.Prepare(ctx, a, core.PrepareInput{ToolID: tool.ID, Arguments: json.RawMessage(`{}`), IdempotencyKey: "first-capacity"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.Prepare(ctx, a, core.PrepareInput{ToolID: tool.ID, Arguments: json.RawMessage(`{}`), IdempotencyKey: "second-capacity"})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/api/", (&httpapi.API{Service: svc, Executor: executor, Capacity: budgets}).Handler(auth))
	mux.Handle("/mcp", mcpserver.Handler(svc, executor, auth))
	server := httptest.NewServer(mux)
	defer server.Close()
	done := make(chan error, 1)
	go func() {
		op, err := executor.Execute(ctx, a, first.ID)
		if err == nil && op.State != core.StateSucceeded {
			err = core.ErrConflict
		}
		done <- err
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("first invocation never entered")
	}
	request, _ := http.NewRequestWithContext(ctx, "POST", server.URL+"/api/v1/operations/"+second.ID+"/execute", strings.NewReader(`{}`))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	json.NewDecoder(response.Body).Decode(&body)
	response.Body.Close()
	if response.StatusCode != 429 || response.Header.Get("Retry-After") == "" {
		t.Fatal("missing quota response", response.StatusCode, body)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "capacity-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: server.URL + "/mcp", HTTPClient: &http.Client{Transport: bearerTransport{token}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "invoke_tool", Arguments: map[string]any{"operation_id": second.ID}})
	if err != nil || !result.IsError {
		t.Fatal(result, err)
	}
	raw := result.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(raw, `"capacity_exceeded"`) || !strings.Contains(raw, `"retry_after_seconds":1`) {
		t.Fatal("missing MCP quota information", raw)
	}
	waiting, err := svc.GetOperation(ctx, a, second.ID)
	if err != nil || waiting.State != core.StateReady || calls.Load() != 1 {
		t.Fatal("rejection changed state or dispatched", waiting, err, calls.Load())
	}
	events, _ := svc.ListEvents(ctx, a, second.ID, 0)
	for _, event := range events {
		if event.Type == "OPERATION_DISPATCHING" {
			t.Fatal("limited operation was claimed")
		}
	}
	close(unblock)
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	op, err := executor.Execute(ctx, a, second.ID)
	if err != nil || op.State != core.StateSucceeded {
		t.Fatal(op, err)
	}
	if _, err = executor.Execute(ctx, a, second.ID); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatal("terminal replay reached upstream")
	}
	stats, err := budgets.Metrics(ctx, a)
	if err != nil || stats.ActiveLeases != 0 || len(stats.Calls) != 1 || stats.Calls[0].Count != 2 {
		t.Fatal("incorrect dispatch metrics", stats, err)
	}
}
