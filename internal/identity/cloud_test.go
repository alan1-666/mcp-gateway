package identity

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/alan1-666/mcp-gateway/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/time/rate"
)

func TestCloudInvitationSessionAndRevocation(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	ctx := context.Background()
	base, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	schema := "identity_" + strings.ReplaceAll(core.NewID(), "-", "")
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = base.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	defer base.Exec(ctx, "DROP SCHEMA "+quoted+" CASCADE")
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err = migrations.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	bootstrap := randomToken()
	const origin = "https://console.example"
	auth, err := NewCloud(ctx, pool, origin, bootstrap)
	if err != nil {
		t.Fatal(err)
	}
	auth.cloud.limiter = rate.NewLimiter(rate.Inf, 1000)
	mux := http.NewServeMux()
	mux.Handle("/api/v1/auth/", auth.CloudHandler())
	protected := auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, Actor(r.Context())) }))
	mux.Handle("/api/v1/me", protected)
	mux.Handle("/mcp", protected)
	type client struct {
		cookie      *http.Cookie
		csrf, token string
	}
	call := func(cl client, method, path string, body any, want int) *httptest.ResponseRecorder {
		t.Helper()
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest(method, path, bytes.NewReader(raw))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", origin)
		if cl.cookie != nil {
			r.AddCookie(cl.cookie)
		}
		if cl.csrf != "" {
			r.Header.Set("X-CSRF-Token", cl.csrf)
		}
		if cl.token != "" {
			r.Header.Set("Authorization", "Bearer "+cl.token)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: expected %d, got %d", method, path, want, w.Code)
		}
		return w
	}
	session := func(w *httptest.ResponseRecorder) client {
		t.Helper()
		cookies := w.Result().Cookies()
		if len(cookies) != 1 || !cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode || cookies[0].Path != "/" {
			t.Fatal("unsafe session cookie")
		}
		cl := client{cookie: cookies[0]}
		var s struct {
			CSRF string `json:"csrf_token"`
		}
		json.Unmarshal(call(cl, "GET", "/api/v1/auth/session", nil, 200).Body.Bytes(), &s)
		cl.csrf = s.CSRF
		if len(cl.csrf) < 32 {
			t.Fatal("missing CSRF token")
		}
		return cl
	}
	accept := func(token, name string) *httptest.ResponseRecorder {
		return call(client{}, "POST", "/api/v1/auth/accept", map[string]string{"token": token, "username": name, "password": "a-secure-test-password"}, 200)
	}
	var status struct {
		Mode    string `json:"mode"`
		Present bool   `json:"session_present"`
	}
	json.Unmarshal(call(client{}, "GET", "/api/v1/auth/status", nil, 200).Body.Bytes(), &status)
	if status.Mode != "cloud" || status.Present {
		t.Fatal("incorrect anonymous session hint")
	}
	owner := session(accept(bootstrap, "owner"))
	json.Unmarshal(call(owner, "GET", "/api/v1/auth/status", nil, 200).Body.Bytes(), &status)
	if !status.Present {
		t.Fatal("missing session hint")
	}
	call(client{}, "POST", "/api/v1/auth/accept", map[string]string{"token": bootstrap, "username": "duplicate", "password": "a-secure-test-password"}, 400)
	call(client{cookie: owner.cookie}, "POST", "/api/v1/auth/invites", map[string]string{"role": "admin"}, 403)
	call(owner, "POST", "/mcp", map[string]string{}, 401)
	var invitation struct{ ID, URL string }
	json.Unmarshal(call(owner, "POST", "/api/v1/auth/invites", map[string]string{"role": "operator"}, 200).Body.Bytes(), &invitation)
	operator := session(accept(strings.Split(invitation.URL, "#invite=")[1], "operator"))
	call(operator, "GET", "/api/v1/auth/members", nil, 403)
	var op core.Actor
	json.Unmarshal(call(operator, "GET", "/api/v1/me", nil, 200).Body.Bytes(), &op)
	var key struct{ ID, Token string }
	json.Unmarshal(call(operator, "POST", "/api/v1/auth/keys", map[string]string{"name": "client"}, 200).Body.Bytes(), &key)
	call(client{token: key.Token}, "GET", "/api/v1/me", nil, 200)
	call(client{token: key.Token}, "POST", "/mcp", map[string]string{}, 200)
	call(client{token: key.Token}, "POST", "/api/v1/auth/keys", map[string]string{"name": "escalate"}, 403)
	call(owner, "POST", "/api/v1/auth/members/"+op.ID, map[string]any{"role": "operator", "disabled": true}, 200)
	call(operator, "GET", "/api/v1/me", nil, 401)
	call(client{token: key.Token}, "GET", "/api/v1/me", nil, 401)
	call(owner, "POST", "/api/v1/auth/members/"+op.ID, map[string]any{"role": "approver", "disabled": false}, 200)
	call(operator, "GET", "/api/v1/me", nil, 401)
	call(client{token: key.Token}, "GET", "/api/v1/me", nil, 401)
	operator = session(call(client{}, "POST", "/api/v1/auth/login", map[string]string{"username": "operator", "password": "a-secure-test-password"}, 200))
	json.Unmarshal(call(operator, "GET", "/api/v1/me", nil, 200).Body.Bytes(), &op)
	if op.Role != core.RoleApprover {
		t.Fatal("role was not refreshed")
	}
	var ownerActor core.Actor
	json.Unmarshal(call(owner, "GET", "/api/v1/me", nil, 200).Body.Bytes(), &ownerActor)
	call(owner, "POST", "/api/v1/auth/members/"+ownerActor.ID, map[string]any{"role": "viewer", "disabled": true}, 409)
	call(owner, "POST", "/api/v1/auth/members/not-in-workspace", map[string]any{"role": "viewer", "disabled": true}, 404)
	json.Unmarshal(call(owner, "POST", "/api/v1/auth/invites", map[string]string{"role": "viewer"}, 200).Body.Bytes(), &invitation)
	call(owner, "POST", "/api/v1/auth/invites/"+invitation.ID+"/revoke", map[string]string{}, 200)
	call(client{}, "POST", "/api/v1/auth/accept", map[string]string{"token": strings.Split(invitation.URL, "#invite=")[1], "username": "revoked", "password": "a-secure-test-password"}, 400)
	json.Unmarshal(call(operator, "POST", "/api/v1/auth/keys", map[string]string{"name": "revocable"}, 200).Body.Bytes(), &key)
	call(operator, "POST", "/api/v1/auth/keys/"+key.ID+"/revoke", map[string]string{}, 200)
	call(client{token: key.Token}, "GET", "/api/v1/me", nil, 401)
	json.Unmarshal(call(operator, "POST", "/api/v1/auth/keys", map[string]string{"name": "password-bound"}, 200).Body.Bytes(), &key)
	call(operator, "POST", "/api/v1/auth/password", map[string]string{"current": "a-secure-test-password", "password": "new-secure-test-password"}, 200)
	call(operator, "GET", "/api/v1/me", nil, 401)
	call(client{token: key.Token}, "GET", "/api/v1/me", nil, 401)
	call(client{}, "POST", "/api/v1/auth/login", map[string]string{"username": "operator", "password": "a-secure-test-password"}, 401)
	operator = session(call(client{}, "POST", "/api/v1/auth/login", map[string]string{"username": "operator", "password": "new-secure-test-password"}, 200))
	call(operator, "POST", "/api/v1/auth/logout", map[string]string{}, 200)
	call(operator, "GET", "/api/v1/me", nil, 401)
	// Bootstrap is one-time even after process restart and configuration rotation.
	if _, err = NewCloud(ctx, pool, origin, randomToken()); err != nil {
		t.Fatal(err)
	}
	var n int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM gateway_invites WHERE id='bootstrap' AND consumed_at IS NOT NULL`).Scan(&n); err != nil || n != 1 {
		t.Fatal("bootstrap was reopened")
	}
	r := httptest.NewRequest("POST", "/api/v1/auth/login", strings.NewReader(`{}`))
	r.Header.Set("Origin", "https://attacker.example")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("cross-origin login accepted")
	}
	auth.cloud.limiter = rate.NewLimiter(0, 0)
	call(client{}, "POST", "/api/v1/auth/login", map[string]string{}, 429)
}
