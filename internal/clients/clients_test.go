package clients_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alan1-666/mcp-gateway/internal/clients"
	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/alan1-666/mcp-gateway/internal/identity"
	"github.com/alan1-666/mcp-gateway/internal/store/postgres"
	"github.com/alan1-666/mcp-gateway/internal/transport/httpapi"
	"github.com/alan1-666/mcp-gateway/internal/transport/mcpserver"
	"github.com/alan1-666/mcp-gateway/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type fixture struct {
	pool    *pgxpool.Pool
	clients *clients.Service
	core    *core.Service
	admin   core.Actor
}

func database(t *testing.T) fixture {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	ctx := context.Background()
	base, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "client_" + strings.ReplaceAll(core.NewID(), "-", "")
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = base.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		base.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { base.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE"); base.Close() })
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err = migrations.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	return fixture{pool, clients.New(pool), core.NewService(postgres.New(pool)), core.Actor{ID: "admin", WorkspaceID: "workspace", Role: core.RoleAdmin}}
}
func (f fixture) tool(t *testing.T, risk core.Risk) core.Tool {
	t.Helper()
	method := "GET"
	if risk == core.RiskWrite {
		method = "POST"
	}
	tool, err := f.core.CreateTool(context.Background(), f.admin, core.ToolInput{Name: "tool_" + core.NewID(), Description: "Client access test", Risk: risk, InputSchema: json.RawMessage(`{"type":"object"}`), HTTP: core.HTTPConfig{URL: "https://example.test/status", Method: method}})
	if err != nil {
		t.Fatal(err)
	}
	tool, err = f.core.PublishTool(context.Background(), f.admin, tool.ID)
	if err != nil {
		t.Fatal(err)
	}
	return tool
}
func (f fixture) issue(t *testing.T, ids ...string) clients.IssuedKey {
	t.Helper()
	v, err := f.clients.Create(context.Background(), f.admin, clients.CreateInput{Name: core.NewID(), Scopes: []string{clients.ScopeRead, clients.ScopeInvoke}, ToolIDs: ids})
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func actor(c clients.Client) core.Actor {
	return core.Actor{ID: c.ID, WorkspaceID: c.WorkspaceID, Role: core.RoleOperator, ClientID: c.ID, ClientKeyID: c.KeyID}
}
func (f fixture) prepare(t *testing.T, a core.Actor, tool core.Tool) core.Operation {
	t.Helper()
	op, err := f.core.Prepare(context.Background(), a, core.PrepareInput{ToolID: tool.ID, Arguments: json.RawMessage(`{}`), IdempotencyKey: core.NewID()})
	if err != nil {
		t.Fatal(err)
	}
	return op
}
func TestClientsDefaultDenyAndTwoClientIsolation(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	one, two := f.tool(t, core.RiskRead), f.tool(t, core.RiskWrite)
	denied, err := f.clients.Create(ctx, f.admin, clients.CreateInput{Name: "default-deny"})
	if err != nil {
		t.Fatal(err)
	}
	if len(denied.Client.Scopes) != 0 || len(denied.Client.ToolIDs) != 0 || len(denied.Client.ServerIDs) != 0 {
		t.Fatal("default client has permissions")
	}
	page, err := f.core.DiscoverTools(ctx, actor(denied.Client), core.ToolSearchInput{})
	if err != nil || page.Total != 0 {
		t.Fatalf("default discovery total=%d err=%v", page.Total, err)
	}
	if _, err = f.core.GetTool(ctx, actor(denied.Client), one.ID); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("default schema access: %v", err)
	}
	a, b := f.issue(t, one.ID), f.issue(t, two.ID)
	for _, v := range []struct {
		client          clients.Client
		allowed, hidden core.Tool
	}{{a.Client, one, two}, {b.Client, two, one}} {
		ident := actor(v.client)
		for _, scope := range []core.ToolVisibilityScope{core.ToolScopeRegistry, core.ToolScopeCatalog} {
			p, err := postgres.New(f.pool).SearchTools(ctx, ident, core.ToolSearchInput{}, scope)
			if err != nil || p.Total != 1 || len(p.Items) != 1 || p.Items[0].ID != v.allowed.ID {
				t.Fatalf("discovery scope=%s total=%d err=%v", scope, p.Total, err)
			}
		}
		list, err := f.core.ListTools(ctx, ident)
		if err != nil || len(list) != 1 {
			t.Fatalf("legacy catalog count=%d err=%v", len(list), err)
		}
		if _, err = f.core.GetTool(ctx, ident, v.hidden.ID); !errors.Is(err, core.ErrNotFound) {
			t.Fatalf("guessed schema visible: %v", err)
		}
		if _, err = f.core.Prepare(ctx, ident, core.PrepareInput{ToolID: v.hidden.ID, Arguments: json.RawMessage(`{}`), IdempotencyKey: core.NewID()}); !errors.Is(err, core.ErrNotFound) {
			t.Fatalf("guessed prepare permitted: %v", err)
		}
	}
	op := f.prepare(t, actor(a.Client), one)
	if op.ActorID == f.admin.ID || op.ActorID != a.Client.ID {
		t.Fatal("operation did not retain independent client identity")
	}
	if _, err = f.core.GetOperation(ctx, actor(b.Client), op.ID); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("other client's operation visible: %v", err)
	}
	if _, err = f.core.Approve(ctx, actor(a.Client), op.ID); !errors.Is(err, core.ErrForbidden) {
		t.Fatalf("client can approve: %v", err)
	}
	if _, err = f.clients.Create(ctx, actor(a.Client), clients.CreateInput{Name: "escalate"}); !errors.Is(err, core.ErrForbidden) {
		t.Fatalf("client can configure clients: %v", err)
	}
	foreign := f.admin
	foreign.WorkspaceID = "foreign"
	if _, err = f.clients.Create(ctx, foreign, clients.CreateInput{Name: "cross", Scopes: []string{clients.ScopeRead}, ToolIDs: []string{one.ID}}); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("cross-workspace grant accepted: %v", err)
	}
}
func TestReadScopeAndRevocationFencePrepareApproveAndClaim(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	read, write := f.tool(t, core.RiskRead), f.tool(t, core.RiskWrite)
	issued := f.issue(t, read.ID, write.ID)
	a := actor(issued.Client)
	ready := f.prepare(t, a, read)
	waiting := f.prepare(t, a, write)
	empty := []string{}
	updated, err := f.clients.Update(ctx, f.admin, a.ID, clients.UpdateInput{ExpectedVersion: 1, ToolIDs: &empty})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, granted, err := f.core.Claim(ctx, a, ready.ID); granted || !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("revoked client dispatch granted=%v err=%v", granted, err)
	}
	if _, _, granted, err := f.core.Claim(ctx, f.admin, ready.ID); granted || !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("admin revived revoked client intent granted=%v err=%v", granted, err)
	}
	reviewer := core.Actor{ID: "reviewer", WorkspaceID: a.WorkspaceID, Role: core.RoleApprover}
	if _, err = f.core.Approve(ctx, reviewer, waiting.ID); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("revoked client intent approved: %v", err)
	}
	tools := []string{read.ID, write.ID}
	scopes := []string{clients.ScopeRead}
	updated, err = f.clients.Update(ctx, f.admin, a.ID, clients.UpdateInput{ExpectedVersion: updated.Version, ToolIDs: &tools, Scopes: &scopes})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.core.GetTool(ctx, a, read.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.core.Prepare(ctx, a, core.PrepareInput{ToolID: read.ID, Arguments: json.RawMessage(`{}`), IdempotencyKey: core.NewID()}); !errors.Is(err, core.ErrForbidden) {
		t.Fatalf("read-only scope invoked: %v", err)
	}
	if _, _, granted, err := f.core.Claim(ctx, a, ready.ID); granted || !errors.Is(err, core.ErrForbidden) {
		t.Fatalf("scope revocation ignored: %v", err)
	}
	if updated.Version != 3 {
		t.Fatalf("expected monotonic version, got %d", updated.Version)
	}
}
func TestClientKeyRotationExpiryDisableAndAudit(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	tool := f.tool(t, core.RiskRead)
	issued := f.issue(t, tool.ID)
	a := actor(issued.Client)
	ready := f.prepare(t, a, tool)
	rotated, err := f.clients.Rotate(ctx, f.admin, a.ID, clients.RotateInput{ExpectedVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	if rotated.APIKey == issued.APIKey || rotated.Client.KeyID == issued.Client.KeyID || rotated.Client.Version != 2 {
		t.Fatal("rotation reused old credentials")
	}
	if _, _, granted, err := f.core.Claim(ctx, a, ready.ID); granted || !errors.Is(err, core.ErrForbidden) {
		t.Fatalf("old authenticated key claimed after rotation: %v", err)
	}
	if _, err = f.core.GetTool(ctx, a, tool.ID); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("old key fetched schema: %v", err)
	}
	current := actor(rotated.Client)
	if _, _, granted, err := f.core.Claim(ctx, current, ready.ID); !granted || err != nil {
		t.Fatalf("new key could not claim own intent: %v", err)
	}
	disabled := false
	c, err := f.clients.Update(ctx, f.admin, a.ID, clients.UpdateInput{ExpectedVersion: 2, Enabled: &disabled})
	if err != nil {
		t.Fatal(err)
	}
	if p, err := f.core.DiscoverTools(ctx, current, core.ToolSearchInput{}); err != nil || p.Total != 0 {
		t.Fatalf("disabled catalog visible: total=%d err=%v", p.Total, err)
	}
	enabled := true
	if _, err = f.clients.Update(ctx, f.admin, a.ID, clients.UpdateInput{ExpectedVersion: c.Version, Enabled: &enabled}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pool.Exec(ctx, `UPDATE gateway_clients SET key_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, granted, err := f.core.Claim(ctx, current, ready.ID); granted || !errors.Is(err, core.ErrForbidden) {
		t.Fatalf("expired key replay accepted: %v", err)
	}
	var hash []byte
	if err = f.pool.QueryRow(ctx, `SELECT token_hash FROM gateway_clients WHERE id=$1`, a.ID).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256([]byte(rotated.APIKey))
	if !bytes.Equal(hash, want[:]) {
		t.Fatal("key digest does not match")
	}
	var audits []byte
	if err = f.pool.QueryRow(ctx, `SELECT jsonb_agg(data) FROM audit_events WHERE resource_id=$1`, a.ID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(audits, []byte(issued.APIKey)) || bytes.Contains(audits, []byte(rotated.APIKey)) || bytes.Contains(audits, []byte("token_hash")) {
		t.Fatal("secret material appeared in audit")
	}
}
func TestClientConcurrentVersionChangesAndTransactionalRevocation(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	tool := f.tool(t, core.RiskRead)
	issued := f.issue(t, tool.ID)
	a := actor(issued.Client)
	ready := f.prepare(t, a, tool)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := fmt.Sprintf("renamed-%d", i)
			_, err := f.clients.Update(ctx, f.admin, a.ID, clients.UpdateInput{ExpectedVersion: 1, Name: &name})
			results <- err
		}(i)
	}
	wg.Wait()
	close(results)
	succeeded, conflicts := 0, 0
	for err := range results {
		if err == nil {
			succeeded++
		} else if errors.Is(err, core.ErrConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if succeeded != 1 || conflicts != 1 {
		t.Fatalf("updates success=%d conflict=%d", succeeded, conflicts)
	}
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `UPDATE gateway_clients SET enabled=false WHERE id=$1`, a.ID); err != nil {
		t.Fatal(err)
	}
	claimed := make(chan error, 1)
	go func() {
		_, _, granted, err := f.core.Claim(ctx, a, ready.ID)
		if granted {
			err = errors.New("dispatch admitted during revocation")
		}
		claimed <- err
	}()
	select {
	case err := <-claimed:
		t.Fatalf("claim bypassed client row lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-claimed:
		if !errors.Is(err, core.ErrForbidden) {
			t.Fatalf("committed disable ignored: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("claim did not release after revocation commit")
	}
	var state string
	if err = f.pool.QueryRow(ctx, `SELECT state FROM operations WHERE id=$1`, ready.ID).Scan(&state); err != nil || state != "READY" {
		t.Fatalf("denied claim changed operation state: %s %v", state, err)
	}
}

type bearerTransport struct{ token string }

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}
func TestClientRESTAndActualMCPSDKHaveIdenticalIsolation(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	one, two := f.tool(t, core.RiskRead), f.tool(t, core.RiskRead)
	a, b := f.issue(t, one.ID), f.issue(t, two.ID)
	auth, err := identity.New([]identity.Token{{Token: a.APIKey, Actor: actor(a.Client)}, {Token: b.APIKey, Actor: actor(b.Client)}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	rest := (&httpapi.API{Service: f.core}).Handler(auth)
	callREST := func(token, path string, want int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		rest.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("REST %s status=%d want=%d", path, w.Code, want)
		}
		return w
	}
	server := httptest.NewServer(mcpserver.Handler(f.core, nil, auth))
	defer server.Close()
	for _, item := range []struct {
		key             clients.IssuedKey
		allowed, hidden core.Tool
	}{{a, one, two}, {b, two, one}} {
		var restPage core.ToolDiscoveryPage
		w := callREST(item.key.APIKey, "/api/v1/catalog/tools", 200)
		if err = json.Unmarshal(w.Body.Bytes(), &restPage); err != nil {
			t.Fatal(err)
		}
		if restPage.Total != 1 || len(restPage.Items) != 1 || restPage.Items[0].ID != item.allowed.ID {
			t.Fatal("REST catalog leaked another tool")
		}
		callREST(item.key.APIKey, "/api/v1/tools/"+item.hidden.ID, 404)
		client := mcp.NewClient(&mcp.Implementation{Name: "client-grant-test", Version: "1"}, nil)
		session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: server.URL, HTTPClient: &http.Client{Transport: bearerTransport{item.key.APIKey}}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "search_tools", Arguments: map[string]any{}})
		if err != nil || result.IsError {
			t.Fatalf("MCP search failed: %v", err)
		}
		var sdkPage core.ToolDiscoveryPage
		if err = json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &sdkPage); err != nil {
			t.Fatal(err)
		}
		if sdkPage.Total != restPage.Total || len(sdkPage.Items) != 1 || sdkPage.Items[0].ID != item.allowed.ID {
			t.Fatal("REST and SDK permissions disagree")
		}
		result, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "get_tool_schema", Arguments: map[string]any{"tool_id": item.hidden.ID}})
		if err != nil || !result.IsError {
			t.Fatalf("MCP guessed schema was allowed: %v", err)
		}
		result, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "prepare_action", Arguments: map[string]any{"tool_id": item.hidden.ID, "arguments": map[string]any{}, "idempotency_key": core.NewID()}})
		if err != nil || !result.IsError {
			t.Fatalf("MCP guessed prepare was allowed: %v", err)
		}
		session.Close()
	}
}

func TestServerGrantsFollowWorkspaceAndLiveDisablement(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	serverID := core.NewID()
	if _, err := f.pool.Exec(ctx, `INSERT INTO mcp_servers(workspace_id,id,name,namespace,url,timeout_ms) VALUES($1,$2,'server','remote','https://example.test/mcp',1000)`, f.admin.WorkspaceID, serverID); err != nil {
		t.Fatal(err)
	}
	tool, err := f.core.CreateTool(ctx, f.admin, core.ToolInput{Name: "remote.status", Description: "Remote status", Risk: core.RiskRead, InputSchema: json.RawMessage(`{"type":"object"}`), MCP: &core.MCPConfig{ServerID: serverID, ToolName: "status", SchemaHash: strings.Repeat("a", 64)}})
	if err != nil {
		t.Fatal(err)
	}
	tool, err = f.core.PublishTool(ctx, f.admin, tool.ID)
	if err != nil {
		t.Fatal(err)
	}
	unrelated := f.tool(t, core.RiskRead)
	issued, err := f.clients.Create(ctx, f.admin, clients.CreateInput{Name: "server-client", Scopes: []string{clients.ScopeRead, clients.ScopeInvoke}, ServerIDs: []string{serverID}})
	if err != nil {
		t.Fatal(err)
	}
	a := actor(issued.Client)
	page, err := f.core.DiscoverTools(ctx, a, core.ToolSearchInput{})
	if err != nil || page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != tool.ID {
		t.Fatalf("server grant discovery total=%d err=%v", page.Total, err)
	}
	if _, err = f.core.GetTool(ctx, a, unrelated.ID); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("server grant leaked HTTP tool: %v", err)
	}
	op := f.prepare(t, a, tool)
	if _, err = f.pool.Exec(ctx, `UPDATE mcp_servers SET enabled=false WHERE id=$1`, serverID); err != nil {
		t.Fatal(err)
	}
	if page, err = f.core.DiscoverTools(ctx, a, core.ToolSearchInput{}); err != nil || page.Total != 0 {
		t.Fatalf("disabled upstream remains visible: %v", err)
	}
	if _, _, granted, err := f.core.Claim(ctx, a, op.ID); granted || !errors.Is(err, core.ErrConflict) {
		t.Fatalf("disabled upstream admitted: %v", err)
	}
	if _, err = f.pool.Exec(ctx, `UPDATE mcp_servers SET enabled=true WHERE id=$1`, serverID); err != nil {
		t.Fatal(err)
	}
	empty := []string{}
	if _, err = f.clients.Update(ctx, f.admin, a.ID, clients.UpdateInput{ExpectedVersion: 1, ServerIDs: &empty}); err != nil {
		t.Fatal(err)
	}
	if _, _, granted, err := f.core.Claim(ctx, a, op.ID); granted || !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("revoked server grant admitted: %v", err)
	}
}

func TestCloudClientKeysOneTimeReturnAndBrowserOnlyManagement(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	const origin = "https://console.test"
	auth, err := identity.NewCloud(ctx, f.pool, origin, "")
	if err != nil {
		t.Fatal(err)
	}
	sessionToken, csrf, personalToken := core.NewID()+core.NewID(), core.NewID(), core.NewID()+core.NewID()
	sessionHash := sha256.Sum256([]byte(sessionToken))
	personalHash := sha256.Sum256([]byte(personalToken))
	if _, err = f.pool.Exec(ctx, `INSERT INTO gateway_users(id,workspace_id,username,password_hash,role) VALUES($1,$2,'owner',$3,'admin')`, f.admin.ID, f.admin.WorkspaceID, []byte("not-a-login-fixture")); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pool.Exec(ctx, `INSERT INTO gateway_sessions(token_hash,user_id,csrf_token,expires_at) VALUES($1,$2,$3,clock_timestamp()+interval '1 hour')`, sessionHash[:], f.admin.ID, csrf); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pool.Exec(ctx, `INSERT INTO gateway_api_keys(id,user_id,token_hash,name,expires_at) VALUES($1,$2,$3,'existing personal key',clock_timestamp()+interval '1 hour')`, core.NewID(), f.admin.ID, personalHash[:]); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/api/v1/clients", f.clients.Handler(auth))
	mux.Handle("/api/v1/clients/", f.clients.Handler(auth))
	mux.Handle("/api/v1/auth/", auth.CloudHandler())
	mux.Handle("/api/v1/", (&httpapi.API{Service: f.core}).Handler(auth))
	call := func(browser bool, token, method, path string, body any, want int) *httptest.ResponseRecorder {
		t.Helper()
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest(method, path, bytes.NewReader(raw))
		r.Header.Set("Content-Type", "application/json")
		if browser {
			r.AddCookie(&http.Cookie{Name: "__Host-gateway-session", Value: sessionToken})
			r.Header.Set("Origin", origin)
			r.Header.Set("X-CSRF-Token", csrf)
		}
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s returned %d want %d", method, path, w.Code, want)
		}
		return w
	}
	call(false, personalToken, "POST", "/api/v1/clients", map[string]any{"name": "denied bearer"}, 403)
	call(false, personalToken, "GET", "/api/v1/catalog/tools", nil, 200)
	created := call(true, "", "POST", "/api/v1/clients", map[string]any{"name": "cloud application"}, 201)
	var issued clients.IssuedKey
	if err = json.Unmarshal(created.Body.Bytes(), &issued); err != nil {
		t.Fatal(err)
	}
	if issued.APIKey == "" {
		t.Fatal("initial key was not returned")
	}
	var gotActor core.Actor
	if err = json.Unmarshal(call(false, issued.APIKey, "GET", "/api/v1/me", nil, 200).Body.Bytes(), &gotActor); err != nil {
		t.Fatal(err)
	}
	if gotActor.ID != issued.Client.ID || gotActor.ClientID != issued.Client.ID || gotActor.Role != core.RoleOperator || gotActor.ClientKeyID != "" {
		t.Fatal("client key did not use independent operator identity")
	}
	call(false, issued.APIKey, "GET", "/api/v1/clients", nil, 403)
	call(false, issued.APIKey, "GET", "/api/v1/auth/keys", nil, 403)
	call(false, issued.APIKey, "POST", "/api/v1/tools", map[string]any{"name": "escalate"}, 403)
	list := call(true, "", "GET", "/api/v1/clients", nil, 200).Body.Bytes()
	if bytes.Contains(list, []byte(issued.APIKey)) || bytes.Contains(list, []byte("api_key")) || bytes.Contains(list, []byte("token_hash")) {
		t.Fatal("list returned secret material")
	}
	call(true, "", "POST", "/api/v1/clients/"+issued.Client.ID, map[string]any{"enabled": false}, 400)
	call(true, "", "POST", "/api/v1/clients/"+issued.Client.ID, map[string]any{"expected_version": 9, "enabled": false}, 409)
	rotated := call(true, "", "POST", "/api/v1/clients/"+issued.Client.ID+"/rotate", map[string]any{"expected_version": 1}, 200)
	var current clients.IssuedKey
	if err = json.Unmarshal(rotated.Body.Bytes(), &current); err != nil {
		t.Fatal(err)
	}
	call(false, issued.APIKey, "GET", "/api/v1/me", nil, 401)
	call(false, current.APIKey, "GET", "/api/v1/me", nil, 200)
	call(true, "", "POST", "/api/v1/clients/"+current.Client.ID, map[string]any{"expected_version": 2, "enabled": false}, 200)
	call(false, current.APIKey, "GET", "/api/v1/me", nil, 401)
	call(true, "", "POST", "/api/v1/clients/"+current.Client.ID, map[string]any{"expected_version": 3, "enabled": true}, 200)
	if _, err = f.pool.Exec(ctx, `UPDATE gateway_clients SET key_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, current.Client.ID); err != nil {
		t.Fatal(err)
	}
	call(false, current.APIKey, "GET", "/api/v1/me", nil, 401)
	call(true, "", "POST", "/api/v1/clients", map[string]any{"name": "past expiry", "expires_at": "2020-01-01T00:00:00Z"}, 400)
	call(true, "", "POST", "/api/v1/clients", map[string]any{"name": "unsupported scope", "scopes": []string{"admin"}}, 400)
}
