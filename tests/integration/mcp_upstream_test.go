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
	"sync/atomic"
	"testing"
	"time"

	"github.com/alan1-666/mcp-gateway/internal/adapters/httpadapter"
	"github.com/alan1-666/mcp-gateway/internal/adapters/mcpadapter"
	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/alan1-666/mcp-gateway/internal/execution"
	"github.com/alan1-666/mcp-gateway/internal/identity"
	"github.com/alan1-666/mcp-gateway/internal/store/postgres"
	"github.com/alan1-666/mcp-gateway/internal/transport/httpapi"
	"github.com/alan1-666/mcp-gateway/internal/transport/mcpserver"
	"github.com/alan1-666/mcp-gateway/internal/upstreams"
	"github.com/alan1-666/mcp-gateway/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestRemoteMCPGatewayImportApprovalProjectionAndDisable(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	base, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	schema := "mcp_e2e_" + strings.ReplaceAll(core.NewID(), "-", "")
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = base.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	defer base.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE")
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err = migrations.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	sdk := mcp.NewServer(&mcp.Implementation{Name: "integration-upstream", Version: "1"}, nil)
	remoteTool := &mcp.Tool{Name: "inventory", Description: "Read inventory", InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`), OutputSchema: json.RawMessage(`{"type":"object","required":["id","private","next_cursor"],"properties":{"id":{"type":"integer"},"private":{"type":"string"},"next_cursor":{"type":"string"}}}`), Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}
	sdk.AddTool(remoteTool, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		calls.Add(1)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "private original RAW_SECRET"}}, StructuredContent: json.RawMessage(`{"id":9007199254740993,"private":"RAW_SECRET","next_cursor":"next-2","api_key":"SECRET_KEY"}`)}, nil
	})
	remote := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return sdk }, &mcp.StreamableHTTPOptions{JSONResponse: true}))
	defer remote.Close()
	egress, err := httpadapter.New([]string{remote.URL}, []string{"127.0.0.0/8"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	store := upstreams.NewStore(pool)
	adapter := mcpadapter.New(egress, store)
	service := core.NewService(postgres.New(pool))
	executor := &execution.Executor{Service: service, Adapter: &execution.Router{HTTP: egress, MCP: adapter}}
	token := func(name string) string { return strings.Repeat(name+"-", 32) }
	auth, err := identity.New([]identity.Token{
		{Token: token("admin"), Actor: core.Actor{ID: "admin", WorkspaceID: "one", Role: core.RoleAdmin}},
		{Token: token("operator"), Actor: core.Actor{ID: "operator", WorkspaceID: "one", Role: core.RoleOperator}},
		{Token: token("approver"), Actor: core.Actor{ID: "approver", WorkspaceID: "one", Role: core.RoleApprover}},
		{Token: token("foreign"), Actor: core.Actor{ID: "foreign", WorkspaceID: "two", Role: core.RoleAdmin}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/api/", (&httpapi.API{Service: service, Executor: executor, Adapter: egress, Upstreams: upstreams.New(store, adapter)}).Handler(auth))
	mux.Handle("/mcp", mcpserver.Handler(service, executor, auth))
	gateway := httptest.NewServer(mux)
	defer gateway.Close()
	request := func(who, path string, body any, want int) []byte {
		t.Helper()
		method := "GET"
		var reader io.Reader
		if body != nil {
			method = "POST"
			raw, _ := json.Marshal(body)
			reader = bytes.NewReader(raw)
		}
		req, _ := http.NewRequestWithContext(ctx, method, gateway.URL+"/api/v1"+path, reader)
		req.Header.Set("Authorization", "Bearer "+token(who))
		req.Header.Set("Content-Type", "application/json")
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		raw, _ := io.ReadAll(response.Body)
		if response.StatusCode != want {
			t.Fatalf("%s %s: status %d want %d: %s", method, path, response.StatusCode, want, raw)
		}
		return raw
	}
	request("operator", "/mcp/servers", nil, 403)
	input := core.MCPServerInput{Name: "Inventory", Namespace: "inventory", URL: remote.URL, TimeoutMS: 3000}
	var server core.MCPServer
	json.Unmarshal(request("admin", "/mcp/servers", input, 200), &server)
	request("foreign", "/mcp/servers/"+server.ID+"/discover", map[string]any{}, 404)
	var discovered upstreams.DiscoveryPage
	json.Unmarshal(request("admin", "/mcp/servers/"+server.ID+"/discover", map[string]any{}, 200), &discovered)
	if len(discovered.Items) != 1 || !discovered.Items[0].ReadOnlyHint {
		t.Fatal("expected read hint")
	}
	importInput := upstreams.ImportInput{ToolName: "inventory", SchemaHash: discovered.Items[0].SchemaHash, Risk: core.RiskWrite, ResponsePolicy: &core.ResponsePolicy{Include: []string{"/id"}, MaxBytes: 2048}}
	var tool core.Tool
	json.Unmarshal(request("admin", "/mcp/servers/"+server.ID+"/import", importInput, 200), &tool)
	if tool.Status != "draft" || tool.Enabled || tool.Risk != core.RiskWrite || tool.MCP == nil {
		t.Fatalf("invalid imported draft: %+v", tool)
	}
	request("operator", "/tools/"+tool.ID, nil, 404)
	var retried core.Tool
	json.Unmarshal(request("admin", "/mcp/servers/"+server.ID+"/import", importInput, 200), &retried)
	if retried.ID != tool.ID {
		t.Fatal("import retry duplicated tool")
	}
	request("admin", "/tools/"+tool.ID+"/publish", map[string]any{}, 200)
	// Actual external MCP client uses the same approved execution ledger.
	client := mcp.NewClient(&mcp.Implementation{Name: "integration-client", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: gateway.URL + "/mcp", HTTPClient: &http.Client{Transport: bearerTransport{token("operator")}}, MaxRetries: -1, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	call := func(name string, args map[string]any) []byte {
		t.Helper()
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		if result.IsError {
			t.Fatalf("MCP %s error: %+v", name, result.Content)
		}
		return []byte(result.Content[0].(*mcp.TextContent).Text)
	}
	var page core.ToolDiscoveryPage
	json.Unmarshal(call("search_tools", map[string]any{"query": "inventory"}), &page)
	if page.Total != 1 {
		t.Fatal("import missing from discovery")
	}
	contract := call("get_tool_schema", map[string]any{"tool_id": tool.ID})
	if !bytes.Contains(contract, []byte("structuredContent_before_projection")) {
		t.Fatal("projection contract not exposed")
	}
	var op core.Operation
	json.Unmarshal(call("prepare_action", map[string]any{"tool_id": tool.ID, "arguments": map[string]any{}, "idempotency_key": "integration-write-1"}), &op)
	if op.State != core.StateWaitingApproval || calls.Load() != 0 {
		t.Fatal("read annotation bypassed local risk")
	}
	request("operator", "/operations/"+op.ID+"/execute", map[string]any{}, 409)
	request("admin", "/operations/"+op.ID+"/approve", map[string]any{}, 200)
	json.Unmarshal(call("invoke_tool", map[string]any{"operation_id": op.ID}), &op)
	if op.State != core.StateSucceeded || calls.Load() != 1 {
		t.Fatalf("dispatch failed: %+v count %d", op, calls.Load())
	}
	for _, forbidden := range []string{"RAW_SECRET", "SECRET_KEY", "private original"} {
		if bytes.Contains(op.Result, []byte(forbidden)) {
			t.Fatalf("projection leak: %s", op.Result)
		}
	}
	for _, required := range []string{"9007199254740993", "next-2", "gateway_projection"} {
		if !bytes.Contains(op.Result, []byte(required)) {
			t.Fatalf("result lost %q: %s", required, op.Result)
		}
	}
	call("invoke_tool", map[string]any{"operation_id": op.ID})
	if calls.Load() != 1 {
		t.Fatal("duplicate remote write")
	}
	var persisted json.RawMessage
	if err = pool.QueryRow(ctx, `SELECT result FROM operations WHERE workspace_id='one' AND id=$1`, op.ID).Scan(&persisted); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(persisted, []byte("RAW_SECRET")) {
		t.Fatal("raw result was persisted before projection")
	}
	request("foreign", "/operations/"+op.ID, nil, 404)
	var pending core.Operation
	json.Unmarshal(call("prepare_action", map[string]any{"tool_id": tool.ID, "arguments": map[string]any{}, "idempotency_key": "integration-write-2"}), &pending)
	request("admin", "/operations/"+pending.ID+"/approve", map[string]any{}, 200)
	request("admin", "/mcp/servers/"+server.ID+"/enabled", map[string]any{"enabled": false}, 200)
	json.Unmarshal(call("search_tools", map[string]any{"query": "inventory"}), &page)
	if page.Total != 0 {
		t.Fatal("disabled server remains discoverable")
	}
	request("operator", "/operations/"+pending.ID+"/execute", map[string]any{}, 409)
	if calls.Load() != 1 {
		t.Fatal("disabled server invoked")
	}
	request("admin", "/mcp/servers/"+server.ID+"/enabled", map[string]any{"enabled": true}, 200)
	// A changed remote contract cannot execute an already approved snapshot.
	changed := *remoteTool
	changed.InputSchema = json.RawMessage(`{"type":"object","properties":{"new":{"type":"string"}}}`)
	sdk.AddTool(&changed, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		calls.Add(1)
		return nil, fmt.Errorf("must not execute changed contract")
	})
	json.Unmarshal(request("operator", "/operations/"+pending.ID+"/execute", map[string]any{}, 200), &pending)
	if pending.State != core.StateFailed || calls.Load() != 1 {
		t.Fatalf("changed contract sent: %+v", pending)
	}
	request("admin", "/mcp/servers/"+server.ID+"/import", importInput, 409)
}
