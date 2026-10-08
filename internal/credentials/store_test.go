package credentials_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alan1-666/mcp-gateway/internal/adapters/httpadapter"
	"github.com/alan1-666/mcp-gateway/internal/adapters/mcpadapter"
	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/alan1-666/mcp-gateway/internal/credentials"
	"github.com/alan1-666/mcp-gateway/internal/upstreams"
	"github.com/alan1-666/mcp-gateway/migrations"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func fixture(t *testing.T, origins []string, static []httpadapter.Credential) (*pgxpool.Pool, *credentials.Store, *httpadapter.Adapter, core.Actor) {
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
	if err = migrations.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	actor := core.Actor{ID: core.NewID(), WorkspaceID: core.NewID(), Role: core.RoleAdmin}
	t.Cleanup(func() {
		for _, table := range []string{"mcp_server_checks", "audit_events", "gateway_credentials", "mcp_servers"} {
			if _, err := pool.Exec(context.Background(), "DELETE FROM "+table+" WHERE workspace_id=$1", actor.WorkspaceID); err != nil {
				t.Error(err)
			}
		}
	})
	if len(static) > 0 {
		static[0].WorkspaceID = actor.WorkspaceID
	}
	egress, err := httpadapter.New(origins, []string{"127.0.0.0/8"}, static)
	if err != nil {
		t.Fatal(err)
	}
	store, err := credentials.New(pool, bytes.Repeat([]byte{7}, 32), egress)
	if err != nil {
		t.Fatal(err)
	}
	egress.SetCredentialResolver(store)
	return pool, store, egress, actor
}
func TestCredentialEncryptionScopeAndConcurrentRotation(t *testing.T) {
	const origin = "https://upstream.example"
	ctx := context.Background()
	pool, store, egress, actor := fixture(t, []string{origin, "https://other.example"}, []httpadapter.Credential{{Ref: "STATIC", Origin: origin, Headers: map[string]string{"Authorization": "Bearer static-fixture"}}})
	secret := "Bearer fixture-secret-no-exposure"
	created, err := store.Create(ctx, actor, credentials.CreateInput{Ref: "BUSINESS", Origin: origin, Headers: map[string]string{"authorization": secret}})
	if err != nil {
		t.Fatal(err)
	}
	if created.Version != 1 || !created.Enabled || len(created.HeaderNames) != 1 || created.HeaderNames[0] != "Authorization" {
		t.Fatalf("metadata %+v", created)
	}
	var encrypted, logs []byte
	if err = pool.QueryRow(ctx, `SELECT encrypted_headers FROM gateway_credentials WHERE workspace_id=$1 AND ref='BUSINESS'`, actor.WorkspaceID).Scan(&encrypted); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encrypted, []byte(secret)) {
		t.Fatal("plaintext in credential storage")
	}
	if err = pool.QueryRow(ctx, `SELECT coalesce(jsonb_agg(data),'[]'::jsonb) FROM audit_events WHERE workspace_id=$1`, actor.WorkspaceID).Scan(&logs); err != nil {
		t.Fatal(err)
	}
	page, err := store.List(ctx, actor)
	raw, _ := json.Marshal(page)
	if err != nil || bytes.Contains(raw, []byte(secret)) || bytes.Contains(logs, []byte(secret)) {
		t.Fatal("secret exposed through metadata or audit")
	}
	if err = egress.ValidateContext(ctx, actor.WorkspaceID, core.HTTPConfig{URL: origin + "/mcp", CredentialRef: "BUSINESS"}); err != nil {
		t.Fatal(err)
	}
	for _, a := range []core.Actor{{ID: actor.ID, WorkspaceID: actor.WorkspaceID, Role: core.RoleOperator}, {ID: "", WorkspaceID: actor.WorkspaceID, Role: core.RoleAdmin}} {
		if _, err = store.List(ctx, a); err == nil {
			t.Fatal("unauthorized metadata access")
		}
	}
	other := actor
	other.WorkspaceID = core.NewID()
	if _, err = store.Rotate(ctx, other, "BUSINESS", credentials.RotateInput{ExpectedVersion: 1, Headers: map[string]string{"Authorization": secret}}); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("workspace mutation %v", err)
	}
	for _, cfg := range []core.HTTPConfig{{URL: "https://other.example/mcp", CredentialRef: "BUSINESS"}, {URL: origin + "/mcp", CredentialRef: "MISSING"}} {
		if egress.ValidateContext(ctx, actor.WorkspaceID, cfg) == nil {
			t.Fatal("credential scope bypass")
		}
	}
	if _, err = store.Create(ctx, actor, credentials.CreateInput{Ref: "STATIC", Origin: origin, Headers: map[string]string{"Authorization": secret}}); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("static override %v", err)
	}
	for _, bad := range []map[string]string{{"Accept": "x"}, {"Authorization": "a", "authorization": "b"}, {"X-Auth": "x\r\nAuthorization: x"}, {"Mcp-Session-Id": "x"}, {"Proxy-Authorization": "x"}, {"X@Auth": "x"}, {}} {
		if _, err = store.Rotate(ctx, actor, "BUSINESS", credentials.RotateInput{ExpectedVersion: 1, Headers: bad}); !errors.Is(err, core.ErrInvalid) {
			t.Fatalf("invalid header accepted: %v", err)
		}
	}
	var wins, conflicts atomic.Int32
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			_, err := store.Rotate(ctx, actor, "BUSINESS", credentials.RotateInput{ExpectedVersion: 1, Headers: map[string]string{"Authorization": "Bearer rotated-fixture"}})
			if err == nil {
				wins.Add(1)
			} else if errors.Is(err, core.ErrConflict) {
				conflicts.Add(1)
			} else {
				t.Errorf("rotate: %v", err)
			}
		})
	}
	workers.Wait()
	if wins.Load() != 1 || conflicts.Load() != 7 {
		t.Fatalf("rotation race wins=%d conflicts=%d", wins.Load(), conflicts.Load())
	}
	value, found, err := store.Resolve(ctx, actor.WorkspaceID, origin, "BUSINESS")
	if err != nil || !found || value.Headers["Authorization"] != "Bearer rotated-fixture" {
		t.Fatal("new credential not resolved")
	}
	wrongKey, _ := credentials.New(pool, bytes.Repeat([]byte{8}, 32), egress)
	if _, found, err = wrongKey.Resolve(ctx, actor.WorkspaceID, origin, "BUSINESS"); err == nil || !found {
		t.Fatal("wrong key did not fail closed")
	}
	if _, err = pool.Exec(ctx, `UPDATE gateway_credentials SET encrypted_headers=set_byte(encrypted_headers,20,get_byte(encrypted_headers,20)#1) WHERE workspace_id=$1 AND ref='BUSINESS'`, actor.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	if _, found, err = store.Resolve(ctx, actor.WorkspaceID, origin, "BUSINESS"); err == nil || !found {
		t.Fatal("tampered ciphertext accepted")
	}
}

func TestAuthenticatedMCPHotRotationDisableDiagnosticsAndHistory(t *testing.T) {
	var expected atomic.Value
	expected.Store("Bearer valid-fixture")
	var calls, requests atomic.Int32
	sdk := mcp.NewServer(&mcp.Implementation{Name: "credential-fixture", Version: "1"}, nil)
	sdk.AddTool(&mcp.Tool{Name: "read", InputSchema: json.RawMessage(`{"type":"object"}`)}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		calls.Add(1)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil
	})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return sdk }, &mcp.StreamableHTTPOptions{JSONResponse: true, Stateless: true})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Authorization") != expected.Load().(string) {
			http.Error(w, "fixture-secret-upstream-body", 401)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	pool, store, egress, actor := fixture(t, []string{server.URL}, nil)
	ctx := context.Background()
	if _, err := store.Create(ctx, actor, credentials.CreateInput{Ref: "MCP", Origin: server.URL, Headers: map[string]string{"Authorization": "Bearer wrong-fixture"}}); err != nil {
		t.Fatal(err)
	}
	resolver := upstreams.NewStore(pool)
	adapter := mcpadapter.New(egress, resolver)
	if err := adapter.EnableSessionPool(mcpadapter.SessionPoolOptions{MaxSessions: 4, IdleTTL: time.Minute, MaxLifetime: 10 * time.Minute}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(adapter.Close)
	service := upstreams.New(resolver, adapter)
	registered, err := service.Create(ctx, actor, core.MCPServerInput{Name: "Authenticated upstream", Namespace: "auth", URL: server.URL, CredentialRef: "MCP", TimeoutMS: 10000})
	if err != nil {
		t.Fatal(err)
	}
	failed, err := service.Check(ctx, actor, registered.ID)
	if err != nil || failed.Status != "failed" || failed.Stage != "authentication" {
		t.Fatalf("wrong auth report %+v %v", failed, err)
	}
	raw, _ := json.Marshal(failed)
	if bytes.Contains(raw, []byte("wrong-fixture")) || bytes.Contains(raw, []byte("fixture-secret-upstream-body")) {
		t.Fatal("diagnostic leaked a secret")
	}
	if _, err = store.Rotate(ctx, actor, "MCP", credentials.RotateInput{ExpectedVersion: 1, Headers: map[string]string{"Authorization": "Bearer valid-fixture"}}); err != nil {
		t.Fatal(err)
	}
	passed, err := service.Check(ctx, actor, registered.ID)
	if err != nil || passed.Status != "ok" || passed.CompatibleCount != 1 || calls.Load() != 0 {
		t.Fatalf("check ran business tool or failed %+v %v", passed, err)
	}
	tools, err := adapter.Discover(ctx, actor, registered)
	if err != nil || len(tools) != 1 {
		t.Fatal("discovery after rotation failed")
	}
	tool := core.Tool{ID: core.NewID(), WorkspaceID: actor.WorkspaceID, Risk: core.RiskRead, InputSchema: tools[0].InputSchema, MCP: &core.MCPConfig{ServerID: registered.ID, ToolName: "read", SchemaHash: tools[0].SchemaHash}}
	if out := adapter.Execute(ctx, actor, tool, core.Operation{Arguments: json.RawMessage(`{}`)}); out.State != core.StateSucceeded || calls.Load() != 1 {
		t.Fatalf("execute after rotation %+v", out)
	}
	if out := adapter.Execute(ctx, actor, tool, core.Operation{Arguments: json.RawMessage(`{}`)}); out.State != core.StateSucceeded || adapter.SessionPoolStats().Hits != 1 {
		t.Fatal("healthy authenticated execution did not reuse its session")
	}
	// Even a rotation to identical header values must establish a fresh session.
	if _, err = store.Rotate(ctx, actor, "MCP", credentials.RotateInput{ExpectedVersion: 2, Headers: map[string]string{"Authorization": "Bearer valid-fixture"}}); err != nil {
		t.Fatal(err)
	}
	if out := adapter.Execute(ctx, actor, tool, core.Operation{Arguments: json.RawMessage(`{}`)}); out.State != core.StateSucceeded || adapter.SessionPoolStats().Hits != 1 || calls.Load() != 3 {
		t.Fatal("credential version change reused a stale session")
	}
	history, err := service.Checks(ctx, actor, registered.ID, 1, 0)
	if err != nil || len(history.Items) != 1 || history.Items[0].ID != passed.ID || history.NextCursor == "" {
		t.Fatalf("history %+v %v", history, err)
	}
	history, err = service.Checks(ctx, actor, registered.ID, 1, passed.ID)
	if err != nil || len(history.Items) != 1 || history.Items[0].ID != failed.ID {
		t.Fatalf("history next %+v %v", history, err)
	}
	other := actor
	other.WorkspaceID = core.NewID()
	if _, err = service.Checks(ctx, other, registered.ID, 10, 0); !errors.Is(err, core.ErrNotFound) {
		t.Fatal("cross-workspace diagnostic history leaked")
	}
	disabled := false
	if _, err = store.SetEnabled(ctx, actor, "MCP", credentials.EnabledInput{ExpectedVersion: 3, Enabled: &disabled}); err != nil {
		t.Fatal(err)
	}
	before := requests.Load()
	if out := adapter.Execute(ctx, actor, tool, core.Operation{Arguments: json.RawMessage(`{}`)}); out.State != core.StateFailed {
		t.Fatal("disabled credential still executed")
	}
	if requests.Load() != before || calls.Load() != 3 {
		t.Fatal("disabled credential reached upstream")
	}
	disabledReport, err := service.Check(ctx, actor, registered.ID)
	if err != nil || disabledReport.Stage != "policy" {
		t.Fatalf("disable diagnostics %+v %v", disabledReport, err)
	}
	if _, err = store.SetEnabled(ctx, actor, "MCP", credentials.EnabledInput{ExpectedVersion: 2, Enabled: &disabled}); !errors.Is(err, core.ErrConflict) {
		t.Fatal("stale disable accepted")
	}
	var historyJSON []byte
	if err = pool.QueryRow(ctx, `SELECT jsonb_agg(report) FROM mcp_server_checks WHERE workspace_id=$1`, actor.WorkspaceID).Scan(&historyJSON); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(historyJSON), "fixture-secret") || strings.Contains(string(historyJSON), "Bearer ") {
		t.Fatal("stored diagnostics contain secret")
	}
}
