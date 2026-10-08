package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/alan1-666/mcp-gateway/internal/adapters/httpadapter"
	"github.com/alan1-666/mcp-gateway/internal/adapters/mcpadapter"
	"github.com/alan1-666/mcp-gateway/internal/clients"
	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/alan1-666/mcp-gateway/internal/execution"
	"github.com/alan1-666/mcp-gateway/internal/identity"
	"github.com/alan1-666/mcp-gateway/internal/store/postgres"
	"github.com/alan1-666/mcp-gateway/internal/transport/mcpserver"
	"github.com/alan1-666/mcp-gateway/internal/upstreams"
	"github.com/alan1-666/mcp-gateway/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestExternalCloudflareDocs is an opt-in compatibility acceptance, not a CI
// availability gate or production deployment. The only remote target is the
// public endpoint documented by Cloudflare at:
// https://developers.cloudflare.com/agents/model-context-protocol/guides/connect-mcp-client/
// It runs one fixed, read-only public documentation query through an ephemeral
// local Gateway and PostgreSQL schema. No account or existing configuration is used.
func TestExternalCloudflareDocs(t *testing.T) {
	if os.Getenv("RUN_EXTERNAL_MCP_TEST") != "1" {
		t.Skip("set RUN_EXTERNAL_MCP_TEST=1 for the fixed public Cloudflare docs endpoint")
	}
	const endpoint = "https://docs.mcp.cloudflare.com/mcp"
	const remoteTool = "search_cloudflare_documentation"
	const query = "Cloudflare Workers fetch handler"
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	cfg, err := pgxpool.ParseConfig(os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal("invalid local test database configuration")
	}
	if cfg.ConnConfig.Host != "127.0.0.1" && cfg.ConnConfig.Host != "localhost" && cfg.ConnConfig.Host != "::1" {
		t.Fatal("external-provider acceptance requires a loopback PostgreSQL test database")
	}
	base, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	schema := "external_cf_" + strings.ReplaceAll(core.NewID(), "-", "")
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = base.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanCtx, cleanCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanCancel()
		if _, err := base.Exec(cleanCtx, "DROP SCHEMA IF EXISTS "+quoted+" CASCADE"); err != nil {
			t.Errorf("ephemeral schema cleanup failed: %v", err)
		}
	}()
	cfg = cfg.Copy()
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err = migrations.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	admin := core.Actor{ID: core.NewID(), WorkspaceID: core.NewID(), Role: core.RoleAdmin}
	// This fixed allowlist exists only in this test process, never on the cloud host.
	egress, err := httpadapter.New([]string{"https://docs.mcp.cloudflare.com"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	store := upstreams.NewStore(pool)
	adapter := mcpadapter.New(egress, store)
	upstream := upstreams.New(store, adapter)
	svc := core.NewService(postgres.New(pool))
	started := time.Now()
	server, err := upstream.Create(ctx, admin, core.MCPServerInput{Name: "Cloudflare public documentation acceptance", Namespace: "cfdocs", URL: endpoint, TimeoutMS: 30000})
	if err != nil {
		t.Fatalf("register fixed public endpoint: %v", err)
	}
	check, err := upstream.Check(ctx, admin, server.ID)
	if err != nil || check.Status != "ok" || check.SessionContractStatus != "stable" {
		t.Fatalf("public endpoint fresh-session compatibility: err=%v status=%s code=%s", err, check.Status, check.Code)
	}
	history, err := upstream.Checks(ctx, admin, server.ID, 1, 0)
	if err != nil || len(history.Items) != 1 || history.Items[0].SessionContractStatus != "stable" {
		t.Fatal("public compatibility receipt was not persisted")
	}
	discoveryStarted := time.Now()
	catalog, err := upstream.Discover(ctx, admin, server.ID)
	if err != nil {
		t.Fatalf("discover public endpoint: %v", err)
	}
	discoveryMS := time.Since(discoveryStarted).Milliseconds()
	var selected core.RemoteTool
	for _, tool := range catalog.Items {
		if tool.Name == remoteTool {
			selected = tool
			break
		}
	}
	if selected.Name == "" {
		names := make([]string, 0, len(catalog.Items))
		for _, tool := range catalog.Items {
			names = append(names, tool.Name)
		}
		t.Fatalf("fixed read-only search tool unavailable; public tool names=%v", names)
	}
	arguments := json.RawMessage(`{"query":"` + query + `"}`)
	if err = core.ValidateArguments(selected.InputSchema, arguments); err != nil {
		t.Fatal("public search schema changed; review the fixed query contract")
	}
	tool, err := upstream.Import(ctx, admin, server.ID, upstreams.ImportInput{ToolName: selected.Name, SchemaHash: selected.SchemaHash, Risk: core.RiskRead, ResponsePolicy: &core.ResponsePolicy{MaxBytes: 131072}})
	if err != nil {
		t.Fatalf("import reviewed read-only tool: %v", err)
	}
	if _, err = svc.PublishTool(ctx, admin, tool.ID); err != nil {
		t.Fatal(err)
	}
	manager := clients.New(pool)
	issued, err := manager.Create(ctx, admin, clients.CreateInput{Name: "Ephemeral public-provider acceptance", Scopes: []string{clients.ScopeRead, clients.ScopeInvoke}})
	if err != nil {
		t.Fatal(err)
	}
	auth, err := identity.NewCloud(ctx, pool, "https://external-fixture.example", "")
	if err != nil {
		t.Fatal(err)
	}
	executor := &execution.Executor{Service: svc, Adapter: &execution.Router{HTTP: egress, MCP: adapter}}
	gateway := httptest.NewServer(mcpserver.Handler(svc, executor, auth))
	defer gateway.Close()
	sdk := mcp.NewClient(&mcp.Implementation{Name: "rillgate-public-provider-acceptance", Version: "1"}, nil)
	session, err := sdk.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: gateway.URL, HTTPClient: &http.Client{Transport: bearerTransport{issued.APIKey}}, MaxRetries: -1, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal("local SDK handshake failed")
	}
	defer session.Close()
	call := func(name string, args map[string]any, wantError bool) json.RawMessage {
		t.Helper()
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("local MCP %s transport failed", name)
		}
		if result.IsError != wantError || len(result.Content) != 1 {
			t.Fatalf("local MCP %s error=%t expected=%t", name, result.IsError, wantError)
		}
		content, ok := result.Content[0].(*mcp.TextContent)
		if !ok {
			t.Fatal("unexpected local MCP envelope")
		}
		return json.RawMessage(content.Text)
	}
	var denied core.ToolDiscoveryPage
	if err = json.Unmarshal(call("search_tools", map[string]any{"server_id": server.ID}, false), &denied); err != nil || len(denied.Items) != 0 {
		t.Fatal("ungranted public tool became discoverable")
	}
	call("get_tool_schema", map[string]any{"tool_id": tool.ID}, true)
	call("call_tool", map[string]any{"tool_id": tool.ID, "arguments": map[string]any{"query": query}, "idempotency_key": "ungranted-query"}, true)
	grants := []string{tool.ID}
	client, err := manager.Update(ctx, admin, issued.Client.ID, clients.UpdateInput{ExpectedVersion: issued.Client.Version, ToolIDs: &grants})
	if err != nil {
		t.Fatal(err)
	}
	var visible core.ToolDiscoveryPage
	if err = json.Unmarshal(call("search_tools", map[string]any{"server_id": server.ID, "query": "search"}, false), &visible); err != nil || len(visible.Items) != 1 || visible.Items[0].ID != tool.ID {
		t.Fatal("granted tool not discovered through SDK")
	}
	var contract struct {
		ID          string          `json:"id"`
		InputSchema json.RawMessage `json:"input_schema"`
	}
	if err = json.Unmarshal(call("get_tool_schema", map[string]any{"tool_id": tool.ID}, false), &contract); err != nil || contract.ID != tool.ID || len(contract.InputSchema) == 0 {
		t.Fatal("SDK contract retrieval failed")
	}
	intent := map[string]any{"tool_id": tool.ID, "arguments": map[string]any{"query": query}, "idempotency_key": "public-cloudflare-docs-query"}
	callStarted := time.Now()
	raw := call("call_tool", intent, false)
	callMS := time.Since(callStarted).Milliseconds()
	var op core.Operation
	if err = json.Unmarshal(raw, &op); err != nil || op.State != core.StateSucceeded {
		t.Fatalf("public read operation not successful: state=%s error=%s", op.State, op.Error)
	}
	var envelope struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err = json.Unmarshal(op.Result, &envelope); err != nil || envelope.IsError || len(envelope.Content) == 0 {
		t.Fatal("empty/invalid public documentation result")
	}
	hasDocs := false
	for _, item := range envelope.Content {
		if item.Type == "text" && strings.Contains(strings.ToLower(item.Text), "workers") {
			hasDocs = true
		}
	}
	if !hasDocs {
		t.Fatal("public search result did not contain the requested documentation topic")
	}
	var replay core.Operation
	if err = json.Unmarshal(call("call_tool", intent, false), &replay); err != nil || replay.ID != op.ID || replay.State != op.State {
		t.Fatal("idempotent facade replay changed outcome")
	}
	var dispatches int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM operation_events WHERE workspace_id=$1 AND operation_id=$2 AND type='OPERATION_DISPATCHING'`, admin.WorkspaceID, op.ID).Scan(&dispatches); err != nil || dispatches != 1 {
		t.Fatal("repeated call dispatched more than once")
	}
	empty := []string{}
	if _, err = manager.Update(ctx, admin, client.ID, clients.UpdateInput{ExpectedVersion: client.Version, ToolIDs: &empty}); err != nil {
		t.Fatal(err)
	}
	call("get_tool_schema", map[string]any{"tool_id": tool.ID}, true)
	call("call_tool", intent, true)
	evidence := map[string]any{"verified_at": time.Now().UTC().Format(time.RFC3339), "provider": "Cloudflare public documentation MCP", "endpoint": endpoint, "official_reference": "https://developers.cloudflare.com/agents/model-context-protocol/guides/connect-mcp-client/", "transport": "Streamable HTTP", "environment": "ephemeral local Gateway and PostgreSQL schema", "production_configuration_changed": false, "provider_authentication": "public; no account", "tool": remoteTool, "schema_sha256": selected.SchemaHash, "query": query, "catalog_tools": catalog.Total, "discovery_ms": discoveryMS, "call_ms": callMS, "setup_to_first_call_ms": callStarted.Sub(started).Milliseconds() + callMS, "result_bytes": len(op.Result), "operation_state": op.State, "dispatches_after_replay": dispatches, "checks": []string{"discover", "reviewed_import", "publish", "ungranted_hidden", "ungranted_call_denied", "tool_grant", "sdk_schema", "sdk_call", "public_topic_present", "same_key_replay", "revoked_hidden", "revoked_replay_denied"}, "limitations": []string{"one public query, not a load benchmark", "not enabled in production", "OAuth and authenticated Cloudflare APIs not exercised", "no raw documentation result or credential retained in evidence"}}
	summary, err := json.Marshal(evidence)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("PUBLIC_PROVIDER_EVIDENCE %s", summary)
	if path := os.Getenv("EXTERNAL_MCP_EVIDENCE"); path != "" {
		summary, err = json.MarshalIndent(evidence, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, append(summary, '\n'), 0644); err != nil {
			t.Fatal(err)
		}
	}
}
