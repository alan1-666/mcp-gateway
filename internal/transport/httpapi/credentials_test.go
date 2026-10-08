package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/alan1-666/mcp-gateway/internal/adapters/httpadapter"
	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/alan1-666/mcp-gateway/internal/credentials"
	"github.com/alan1-666/mcp-gateway/internal/identity"
	"github.com/alan1-666/mcp-gateway/internal/upstreams"
	"github.com/alan1-666/mcp-gateway/migrations"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

type checkRemote struct{}

func (checkRemote) ValidateServer(core.Actor, core.MCPServer) error { return nil }
func (checkRemote) Discover(context.Context, core.Actor, core.MCPServer) ([]core.RemoteTool, error) {
	return []core.RemoteTool{}, nil
}
func (checkRemote) Check(context.Context, core.Actor, core.MCPServer) upstreams.CheckReport {
	return upstreams.CheckReport{Status: "ok", Stage: "complete", Code: "catalog_compatible", Tools: []upstreams.ToolCompatibility{}}
}

func TestCredentialAndDiagnosticHTTPContracts(t *testing.T) {
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
	if err = migrations.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	admin := core.Actor{ID: core.NewID(), WorkspaceID: core.NewID(), Role: core.RoleAdmin}
	operator := admin
	operator.ID = core.NewID()
	operator.Role = core.RoleOperator
	foreign := admin
	foreign.WorkspaceID = core.NewID()
	defer func() {
		for _, table := range []string{"mcp_server_checks", "gateway_credentials", "audit_events", "mcp_servers"} {
			if _, err := pool.Exec(ctx, "DELETE FROM "+table+" WHERE workspace_id=$1", admin.WorkspaceID); err != nil {
				t.Error(err)
			}
		}
	}()
	const origin = "https://credential.example"
	egress, _ := httpadapter.New([]string{origin}, nil, nil)
	vault, err := credentials.New(pool, bytes.Repeat([]byte{42}, 32), egress)
	if err != nil {
		t.Fatal(err)
	}
	upstream := upstreams.New(upstreams.NewStore(pool), checkRemote{})
	server, err := upstream.Create(ctx, admin, core.MCPServerInput{Name: "check", Namespace: "check", URL: origin + "/mcp"})
	if err != nil {
		t.Fatal(err)
	}
	tokens := []identity.Token{{Token: strings.Repeat("a", 32), Actor: admin}, {Token: strings.Repeat("o", 32), Actor: operator}, {Token: strings.Repeat("f", 32), Actor: foreign}}
	auth, err := identity.New(tokens, nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := (&API{Credentials: vault, Upstreams: upstream}).Handler(auth)
	call := func(method, path, token, body string, want int) []byte {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != want {
			t.Fatalf("%s %s: got %d want %d: %s", method, path, res.Code, want, res.Body.String())
		}
		if strings.Contains(res.Body.String(), "fixture-secret-value") {
			t.Fatal("API exposed credential value")
		}
		return res.Body.Bytes()
	}
	base := "/api/v1/credentials"
	a, o, f := tokens[0].Token, tokens[1].Token, tokens[2].Token
	body := `{"ref":"AUTH","origin":"` + origin + `","headers":{"Authorization":"Bearer fixture-secret-value"}}`
	call("GET", base, "", "", 401)
	call("POST", base, o, body, 403)
	call("POST", base, a, body, 200)
	call("POST", base, a, body, 409)
	list := call("GET", base, f, "", 200)
	var page credentials.Page
	if json.Unmarshal(list, &page) != nil || page.Total != 0 {
		t.Fatal("cross-workspace list exposed credential")
	}
	call("POST", base+"/AUTH/rotate", f, `{"expected_version":1,"headers":{"Authorization":"Bearer fixture-secret-value"}}`, 404)
	call("POST", base+"/AUTH/rotate", a, `{"expected_version":1,"headers":null}`, 400)
	call("POST", base+"/AUTH/rotate", a, `{"expected_version":1,"headers":{"Authorization":"Bearer fixture-secret-value"},"workspace_id":"another"}`, 400)
	call("POST", base+"/AUTH/rotate", a, `{"expected_version":1,"headers":{"Authorization":"Bearer fixture-secret-value"}}`, 200)
	call("POST", base+"/AUTH/enabled", a, `{"expected_version":1,"enabled":false}`, 409)
	call("POST", base+"/AUTH/enabled", a, `{"expected_version":2,"enabled":null}`, 400)
	call("POST", base+"/AUTH/enabled", a, `{"expected_version":2,"enabled":false}`, 200)
	prefix := "/api/v1/mcp/servers/" + server.ID
	call("GET", prefix+"/execution-metrics", "", "", 401)
	call("GET", prefix+"/execution-metrics", o, "", 403)
	call("GET", prefix+"/execution-metrics", f, "", 404)
	call("GET", prefix+"/execution-metrics", a, "", 200)
	call("GET", prefix+"/execution-metrics?limit=2000", a, "", 400)
	call("POST", prefix+"/check", o, `{}`, 403)
	call("POST", prefix+"/check", f, `{}`, 404)
	call("POST", prefix+"/check", a, `{}`, 200)
	call("GET", prefix+"/checks", o, "", 403)
	call("GET", prefix+"/checks", f, "", 404)
	call("GET", prefix+"/checks?limit=1", a, "", 200)
	call("GET", prefix+"/checks?limit=0", a, "", 400)
}
