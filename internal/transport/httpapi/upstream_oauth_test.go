package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/alan1-666/mcp-gateway/internal/adapters/httpadapter"
	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/alan1-666/mcp-gateway/internal/identity"
	"github.com/alan1-666/mcp-gateway/internal/upstreamoauth"
	"github.com/alan1-666/mcp-gateway/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestOAuthCallbackQuery(t *testing.T) {
	for _, bad := range []string{"state=a&state=b", "code=a&code=b", "state=%GG", "state=a;code=b", strings.Repeat("x", 16385)} {
		if _, err := oauthCallbackQuery(bad); err == nil {
			t.Fatalf("accepted malformed callback")
		}
	}
	q, err := oauthCallbackQuery("state=abc&code=def&iss=https%3A%2F%2Fissuer.example&scope=files&authuser=0&redirect_uri=https://evil.example")
	if err != nil || q.Get("iss") != "https://issuer.example" || q.Has("redirect_uri") || q.Has("authuser") {
		t.Fatal("valid issuer callback rejected", err)
	}
}
func TestCloudOAuthRequiresBoundAdminSession(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required")
	}
	ctx := context.Background()
	base, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	schema := pgx.Identifier{"oauth_api_" + strings.ReplaceAll(core.NewID(), "-", "")}.Sanitize()
	if _, err = base.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer base.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
	config, _ := pgxpool.ParseConfig(dsn)
	config.ConnConfig.RuntimeParams["search_path"] = strings.Trim(schema, `"`)
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err = migrations.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	const origin = "https://gateway.example"
	auth, err := identity.NewCloud(ctx, pool, origin, "")
	if err != nil {
		t.Fatal(err)
	}
	policy, _ := httpadapter.New(nil, nil, nil)
	oauth, err := upstreamoauth.New(pool, bytes.Repeat([]byte{1}, 32), policy, origin)
	if err != nil {
		t.Fatal(err)
	}
	handler := (&API{UpstreamOAuth: oauth}).Handler(auth)
	id := core.NewID()
	if _, err = pool.Exec(ctx, `INSERT INTO mcp_servers(workspace_id,id,name,namespace,url,timeout_ms) VALUES('workspace',$1,'fixture','fixture','https://upstream.example/mcp',2000)`, id); err != nil {
		t.Fatal(err)
	}
	session := func(role, workspace string) string {
		t.Helper()
		uid, token := core.NewID(), strings.ReplaceAll(core.NewID()+core.NewID(), "-", "")
		h := sha256.Sum256([]byte(token))
		_, err := pool.Exec(ctx, `INSERT INTO gateway_users(id,workspace_id,username,password_hash,role) VALUES($1,$2,$1,'unused',$3)`, uid, workspace, role)
		if err != nil {
			t.Fatal(err)
		}
		_, err = pool.Exec(ctx, `INSERT INTO gateway_sessions(token_hash,user_id,csrf_token,expires_at) VALUES($1,$2,'csrf-fixture',clock_timestamp()+interval '1 hour')`, h[:], uid)
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	admin, viewer, foreign := session("admin", "workspace"), session("viewer", "workspace"), session("admin", "elsewhere")
	call := func(method, path, token, body string, csrf bool, want int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if token != "" {
			req.AddCookie(&http.Cookie{Name: "__Host-gateway-session", Value: token})
		}
		req.Header.Set("Content-Type", "application/json")
		if csrf {
			req.Header.Set("Origin", origin)
			req.Header.Set("X-CSRF-Token", "csrf-fixture")
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("%s %s got %d want %d: %s", method, path, rec.Code, want, rec.Body)
		}
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("OAuth response must not be cached")
		}
		return rec
	}
	path := "/api/v1/mcp/servers/" + id + "/oauth"
	call("GET", path, "", "", false, 401)
	call("GET", path, viewer, "", false, 403)
	call("GET", path, foreign, "", false, 404)
	got := call("GET", path, admin, "", false, 200)
	var status upstreamoauth.Status
	if json.Unmarshal(got.Body.Bytes(), &status) != nil || status.Status != "unconfigured" || status.RedirectURI != origin+upstreamoauth.CallbackPath {
		t.Fatal("status contract mismatch")
	}
	call("POST", path+"/connect", admin, `{"expected_version":0}`, false, 403)
	call("POST", path+"/connect", admin, `{}`, true, 400)
	call("POST", path+"/connect", admin, `{"expected_version":0}`, true, 409)
	call("PUT", path, admin, `{"expected_version":0,"unexpected":true}`, true, 400)
	// Even a privileged personal API key cannot start/bind a browser grant.
	bearer := strings.Repeat("k", 40)
	h := sha256.Sum256([]byte(bearer))
	_, err = pool.Exec(ctx, `INSERT INTO gateway_api_keys(id,user_id,token_hash,name,expires_at) SELECT $1,user_id,$2,'fixture',clock_timestamp()+interval '1 hour' FROM gateway_sessions WHERE token_hash=$3`, core.NewID(), h[:], func() []byte { v := sha256.Sum256([]byte(admin)); return v[:] }())
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", path, nil)
	req.Header.Set("Authorization", "Bearer "+bearer)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatal("personal key accepted for browser management")
	}
	callback := call("GET", upstreamoauth.CallbackPath+"?state=bad&error=untrusted-provider-secret", admin, "", false, 303)
	if callback.Header().Get("Location") != "/console/?oauth=reconnect_required" || strings.Contains(callback.Body.String(), "untrusted-provider-secret") || callback.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatal("callback reflected provider data")
	}
	sum := sha256.Sum256([]byte(admin))
	if _, err = pool.Exec(ctx, `DELETE FROM gateway_sessions WHERE token_hash=$1`, sum[:]); err != nil {
		t.Fatal(err)
	}
	call("GET", path, admin, "", false, 401)
	expiredCallback := call("GET", upstreamoauth.CallbackPath+"?state=private&code=private", admin, "", false, 303)
	if expiredCallback.Header().Get("Location") != "/console/?oauth=reconnect_required" || strings.Contains(expiredCallback.Body.String(), "private") {
		t.Fatal("expired session retained callback credentials")
	}
}
