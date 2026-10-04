package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alan1-666/mcp-gateway/internal/adapters/httpadapter"
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

type bearerTransport struct{ token string }

func (t bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	copy := r.Clone(r.Context())
	copy.Header.Set("Authorization", "Bearer "+t.token)
	return http.DefaultTransport.RoundTrip(copy)
}

func TestDurableHTTPAndMCPWorkflow(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required for real PostgreSQL integration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err = migrations.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	workspace := core.NewID()
	adminToken := strings.Repeat("a", 40)
	operatorToken := strings.Repeat("o", 40)
	approverToken := strings.Repeat("p", 40)
	otherToken := strings.Repeat("x", 40)
	auth, err := identity.New([]identity.Token{{Token: adminToken, Actor: core.Actor{ID: "admin", WorkspaceID: workspace, Role: core.RoleAdmin}}, {Token: operatorToken, Actor: core.Actor{ID: "operator", WorkspaceID: workspace, Role: core.RoleOperator}}, {Token: approverToken, Actor: core.Actor{ID: "approver", WorkspaceID: workspace, Role: core.RoleApprover}}, {Token: otherToken, Actor: core.Actor{ID: "other", WorkspaceID: core.NewID(), Role: core.RoleAdmin}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var writes atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			writes.Add(1)
		}
		if r.URL.Path == "/lost" {
			conn, _, e := w.(http.Hijacker).Hijack()
			if e == nil {
				_ = conn.Close()
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"accepted","secret":"private"}`))
	}))
	defer target.Close()
	adapter, err := httpadapter.New([]string{target.URL}, []string{"127.0.0.0/8"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	service := core.NewService(postgres.New(pool))
	executor := &execution.Executor{Service: service, Adapter: adapter}
	api := httptest.NewServer((&httpapi.API{Service: service, Executor: executor, Adapter: adapter}).Handler(auth))
	defer api.Close()
	call := func(method, path, token string, input any, want int) json.RawMessage {
		t.Helper()
		b, _ := json.Marshal(input)
		req, e := http.NewRequestWithContext(ctx, method, api.URL+"/api/v1"+path, bytes.NewReader(b))
		if e != nil {
			t.Fatal(e)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		resp, e := http.DefaultClient.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != want {
			t.Fatalf("%s %s: expected%d got%d %s", method, path, want, resp.StatusCode, data)
		}
		return data
	}
	create := func(name, path, method string, risk core.Risk) core.Tool {
		in := core.ToolInput{Name: name, Description: "Integration test business endpoint", Risk: risk, InputSchema: json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"],"additionalProperties":false}`), HTTP: core.HTTPConfig{URL: target.URL + path, Method: method, TimeoutMS: 1000}}
		var tool core.Tool
		_ = json.Unmarshal(call("POST", "/tools", adminToken, in, 200), &tool)
		call("POST", "/tools/"+tool.ID+"/publish", adminToken, struct{}{}, 200)
		return tool
	}
	writeTool := create("retry_job", "/ok", "POST", core.RiskWrite)
	readTool := create("get_job", "/ok", "GET", core.RiskRead)
	prepare := core.PrepareInput{ToolID: writeTool.ID, Arguments: json.RawMessage(`{"id":"job-1"}`), IdempotencyKey: "stable-request-key"}
	var op core.Operation
	_ = json.Unmarshal(call("POST", "/operations", operatorToken, prepare, 200), &op)
	if op.State != core.StateWaitingApproval {
		t.Fatalf("write was not gated: %s", op.State)
	}
	call("POST", "/operations/"+op.ID+"/execute", operatorToken, struct{}{}, 409)
	call("POST", "/operations/"+op.ID+"/approve", operatorToken, struct{}{}, 403)
	call("GET", "/operations/"+op.ID, otherToken, nil, 404)
	prepare.Arguments = json.RawMessage(`{"id":"different"}`)
	call("POST", "/operations", operatorToken, prepare, 409)
	call("POST", "/operations/"+op.ID+"/approve", approverToken, struct{}{}, 200)
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() { defer wg.Done(); call("POST", "/operations/"+op.ID+"/execute", operatorToken, struct{}{}, 200) }()
	}
	wg.Wait()
	if writes.Load() != 1 {
		t.Fatalf("concurrent replay produced%d side effects", writes.Load())
	}
	var finished core.Operation
	_ = json.Unmarshal(call("GET", "/operations/"+op.ID, operatorToken, nil, 200), &finished)
	if finished.State != core.StateSucceeded || strings.Contains(string(finished.Result), "private") {
		t.Fatalf("result contract/redaction: %+v", finished)
	}
	// Reconstruct the service from the same database: execution state is durable.
	restored, err := core.NewService(postgres.New(pool)).GetOperation(ctx, core.Actor{ID: "operator", WorkspaceID: workspace, Role: core.RoleOperator}, op.ID)
	if err != nil || restored.State != core.StateSucceeded {
		t.Fatal("durable restart lookup failed", err)
	}
	lostTool := create("uncertain_write", "/lost", "POST", core.RiskWrite)
	var lost core.Operation
	_ = json.Unmarshal(call("POST", "/operations", operatorToken, core.PrepareInput{ToolID: lostTool.ID, Arguments: json.RawMessage(`{"id":"job-2"}`), IdempotencyKey: "uncertain-request-key"}, 200), &lost)
	call("POST", "/operations/"+lost.ID+"/approve", approverToken, struct{}{}, 200)
	_ = json.Unmarshal(call("POST", "/operations/"+lost.ID+"/execute", operatorToken, struct{}{}, 200), &lost)
	if lost.State != core.StateUnknown {
		t.Fatalf("ambiguous write outcome became %s", lost.State)
	}
	call("POST", "/operations/"+lost.ID+"/execute", operatorToken, struct{}{}, 200)
	if writes.Load() != 2 {
		t.Fatal("unknown write was replayed")
	}
	// Actual official SDK client/server handshake, discovery and execution.
	mcpHTTP := httptest.NewServer(mcpserver.Handler(service, executor, auth))
	defer mcpHTTP.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "gateway-integration", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: mcpHTTP.URL, HTTPClient: &http.Client{Transport: bearerTransport{operatorToken}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	listed, err := session.ListTools(ctx, nil)
	if err != nil || len(listed.Tools) != 5 {
		t.Fatalf("MCP tools: %v %v", listed, err)
	}
	r, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "prepare_action", Arguments: map[string]any{"tool_id": readTool.ID, "arguments": map[string]string{"id": "job-3"}, "idempotency_key": "mcp-read-request"}})
	if err != nil || r.IsError {
		t.Fatalf("MCP preparation: %+v %v content=%+v", r, err, r.Content[0].(*mcp.TextContent).Text)
	}
	var read core.Operation
	if err = json.Unmarshal([]byte(r.Content[0].(*mcp.TextContent).Text), &read); err != nil {
		t.Fatal(err)
	}
	r, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "invoke_tool", Arguments: map[string]string{"operation_id": read.ID}})
	if err != nil || r.IsError {
		t.Fatalf("MCP invocation: %v %v", r, err)
	}
	if !strings.Contains(r.Content[0].(*mcp.TextContent).Text, `"state":"SUCCEEDED"`) {
		t.Fatal(fmt.Sprint(r.Content))
	}
}
