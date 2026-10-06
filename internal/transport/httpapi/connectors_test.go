package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/alan1-666/mcp-gateway/internal/connectors"
	"github.com/alan1-666/mcp-gateway/internal/connectorwire"
	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/alan1-666/mcp-gateway/internal/identity"
	"github.com/alan1-666/mcp-gateway/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func connectorAPIPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required")
	}
	ctx := context.Background()
	base, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "connector_api_" + strings.ReplaceAll(core.NewID(), "-", "")
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = base.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		base.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := base.Exec(ctx, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error(err)
		}
		base.Close()
	})
	config, _ := pgxpool.ParseConfig(dsn)
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err = migrations.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	return pool
}
func TestConnectorCloudManagementRequiresAdministratorAndCSRF(t *testing.T) {
	pool := connectorAPIPool(t)
	ctx := context.Background()
	origin := "https://gateway.example"
	auth, err := identity.NewCloud(ctx, pool, origin, "")
	if err != nil {
		t.Fatal(err)
	}
	service := connectors.New(pool)
	handler := (&API{Connectors: service}).Handler(auth)
	session := func(role, workspace string) string {
		token := strings.ReplaceAll(core.NewID()+core.NewID(), "-", "")
		uid := core.NewID()
		hash := sha256.Sum256([]byte(token))
		if _, err := pool.Exec(ctx, `INSERT INTO gateway_users(id,workspace_id,username,password_hash,role) VALUES($1,$2,$1,'unused',$3)`, uid, workspace, role); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO gateway_sessions(token_hash,user_id,csrf_token,expires_at) VALUES($1,$2,'csrf-fixture',clock_timestamp()+interval '1 hour')`, hash[:], uid); err != nil {
			t.Fatal(err)
		}
		return token
	}
	admin, viewer, foreign := session("admin", "workspace"), session("viewer", "workspace"), session("admin", "elsewhere")
	call := func(method, path, token, body string, csrf bool, want int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.AddCookie(&http.Cookie{Name: "__Host-gateway-session", Value: token})
		}
		if csrf {
			r.Header.Set("Origin", origin)
			r.Header.Set("X-CSRF-Token", "csrf-fixture")
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s got%d want%d: %s", method, path, w.Code, want, w.Body.String())
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("credentials may be cached")
		}
		return w
	}
	call("GET", "/api/v1/connectors", "", "", false, 401)
	call("GET", "/api/v1/connectors", viewer, "", false, 403)
	call("POST", "/api/v1/connectors", admin, `{"name":"fixture"}`, false, 403)
	created := call("POST", "/api/v1/connectors", admin, `{"name":"fixture"}`, true, 200)
	var value connectorwire.Created
	if err = json.Unmarshal(created.Body.Bytes(), &value); err != nil || value.Token == "" {
		t.Fatal(err)
	}
	listed := call("GET", "/api/v1/connectors", admin, "", false, 200)
	if strings.Contains(listed.Body.String(), value.Token) {
		t.Fatal("one-time token listed")
	}
	call("POST", "/api/v1/connectors/"+value.Connector.ID+"/revoke", foreign, `{}`, true, 404)
	call("POST", "/api/v1/connectors/"+value.Connector.ID+"/revoke", admin, `{}`, false, 403)
	call("POST", "/api/v1/connectors/"+value.Connector.ID+"/revoke", admin, `{}`, true, 200)
	r := httptest.NewRequest("GET", "/api/v1/connectors", nil)
	r.Header.Set("Authorization", "Bearer "+value.Token)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("connector token accessed management")
	}
	bearer := strings.Repeat("k", 40)
	hash := sha256.Sum256([]byte(bearer))
	sessionHash := sha256.Sum256([]byte(admin))
	if _, err = pool.Exec(ctx, `INSERT INTO gateway_api_keys(id,user_id,token_hash,name,expires_at) SELECT $1,user_id,$2,'fixture',clock_timestamp()+interval '1 hour' FROM gateway_sessions WHERE token_hash=$3`, core.NewID(), hash[:], sessionHash[:]); err != nil {
		t.Fatal(err)
	}
	r = httptest.NewRequest("POST", "/api/v1/connectors", strings.NewReader(`{"name":"denied"}`))
	r.Header.Set("Authorization", "Bearer "+bearer)
	r.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("personal key minted machine identity")
	}
}
func TestConnectorMachineProtocolRejectsBrowserCredentialsAndMalformedBodies(t *testing.T) {
	pool := connectorAPIPool(t)
	ctx := context.Background()
	service := connectors.New(pool)
	created, err := service.Create(ctx, core.Actor{ID: "owner", WorkspaceID: "workspace", Role: core.RoleAdmin}, "Fixture")
	if err != nil {
		t.Fatal(err)
	}
	handler := ConnectorHandler(service)
	body := `{"ready":false,"targets":[{"name":"docs","fingerprint":"` + strings.Repeat("a", 64) + `","transport":"stdio"}]}`
	call := func(path, body string, modify func(*http.Request), want int) {
		t.Helper()
		r := httptest.NewRequest("POST", path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+created.Token)
		if modify != nil {
			modify(r)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s got%d want%d: %s", path, w.Code, want, w.Body.String())
		}
		if strings.Contains(w.Body.String(), created.Token) {
			t.Fatal("token echoed")
		}
	}
	call("/connector/v1/poll", body, nil, 204)
	for _, modify := range []func(*http.Request){
		func(r *http.Request) { r.Header.Del("Authorization") },
		func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+strings.Repeat("a", 40)) },
		func(r *http.Request) { r.Header.Add("Authorization", "Bearer "+created.Token) },
		func(r *http.Request) { r.Header.Set("Origin", "https://gateway.example") },
		func(r *http.Request) { r.Header["Origin"] = []string{""} },
		func(r *http.Request) {
			r.AddCookie(&http.Cookie{Name: "__Host-gateway-session", Value: "not-a-machine"})
		},
	} {
		call("/connector/v1/poll", body, modify, 401)
	}
	for _, bad := range []string{`null`, `[]`, `{"ready":true,"ready":false,"targets":[]}`, `{"targets":[],"unknown":true}`, `{} {}`, strings.Repeat(" ", 16385) + `{}`} {
		call("/connector/v1/poll", bad, nil, 400)
	}
	call("/connector/v1/poll", body, func(r *http.Request) { r.Header.Set("Content-Type", "application/jsonp") }, 400)
	call("/connector/v1/jobs/missing/start", `{"unexpected":true}`, nil, 400)
	call("/connector/v1/jobs/missing/start", `{}`, nil, 404)
	call("/connector/v1/jobs/missing/result", `{"state":"SUCCEEDED","state":"UNKNOWN"}`, nil, 400)
	call("/connector/v1/jobs/missing/result", `{"state":"SUCCEEDED","result":{"value":1,"value":2}}`, nil, 400)
	call("/connector/v1/jobs/missing/result", `{"state":"SUCCEEDED","result":{}}`, nil, 404)
	if _, err = service.Revoke(ctx, core.Actor{ID: "owner", WorkspaceID: "workspace", Role: core.RoleAdmin}, created.Connector.ID); err != nil {
		t.Fatal(err)
	}
	call("/connector/v1/poll", body, nil, 401)
}
