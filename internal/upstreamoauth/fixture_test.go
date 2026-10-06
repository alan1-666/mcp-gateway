package upstreamoauth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/alan1-666/mcp-gateway/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// The fake policy trusts only the fixture's TLS certificate and exact origin.
// It deliberately retains the production policy's no-redirect behavior.
type fixturePolicy struct {
	origin string
	base   http.RoundTripper
}

func (p *fixturePolicy) NewClientContext(_ context.Context, _ string, endpoint, credential string, timeout time.Duration) (*http.Client, func(), error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme+"://"+u.Host != p.origin || u.User != nil || u.Fragment != "" || u.RawQuery != "" || credential != "" {
		return nil, nil, fmt.Errorf("blocked fixture origin")
	}
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Scheme+"://"+req.URL.Host != p.origin {
			return nil, fmt.Errorf("blocked fixture destination")
		}
		return p.base.RoundTrip(req)
	})
	return &http.Client{Transport: transport, Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, func() {}, nil
}

type observedRequest struct {
	Method, Path, Authorization, RPCMethod string
	Values                                 url.Values
}

type oauthProvider struct {
	t        *testing.T
	server   *httptest.Server
	policy   *fixturePolicy
	mu       sync.Mutex
	routes   map[string]http.HandlerFunc
	requests []observedRequest
}

func newProvider(t *testing.T) *oauthProvider {
	t.Helper()
	p := &oauthProvider{t: t, routes: make(map[string]http.HandlerFunc)}
	p.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var rpc struct {
			Method string `json:"method"`
		}
		if r.Method == http.MethodPost && r.URL.Path == "/mcp" {
			raw, err := io.ReadAll(io.LimitReader(r.Body, maxJSON))
			if err != nil {
				t.Error(err)
			}
			_ = r.Body.Close()
			r.Body = io.NopCloser(bytes.NewReader(raw))
			if err := json.Unmarshal(raw, &rpc); err != nil {
				t.Error(err)
			}
		}
		if r.Method == http.MethodPost && r.URL.Path == "/token" {
			if err := r.ParseForm(); err != nil {
				t.Error(err)
			}
		}
		p.mu.Lock()
		p.requests = append(p.requests, observedRequest{r.Method, r.URL.Path, r.Header.Get("Authorization"), rpc.Method, r.PostForm})
		handler := p.routes[r.Method+" "+r.URL.Path]
		p.mu.Unlock()
		if handler == nil {
			http.NotFound(w, r)
			return
		}
		handler(w, r)
	}))
	t.Cleanup(p.server.Close)
	p.policy = &fixturePolicy{origin: p.server.URL, base: p.server.Client().Transport}
	p.set("GET /mcp", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusMethodNotAllowed)
	})
	p.set("GET /resource-metadata", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, p.resourceMetadata()) })
	p.set("GET /.well-known/oauth-authorization-server/issuer", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, p.issuerMetadata()) })
	p.set("POST /token", func(w http.ResponseWriter, r *http.Request) {
		if r.Form.Get("resource") != p.server.URL+"/mcp" || r.Form.Get("client_id") != "registered-client" {
			t.Errorf("unbound token request: %v", r.Form)
		}
		if r.Form.Get("grant_type") == "refresh_token" {
			writeJSON(w, grant{AccessToken: "access-secret-2", RefreshToken: "refresh-secret-2", TokenType: "Bearer", ExpiresIn: revision(3600), Scope: "tools:read"})
			return
		}
		writeJSON(w, grant{AccessToken: "access-secret-1", RefreshToken: "refresh-secret-1", TokenType: "Bearer", ExpiresIn: revision(3600), Scope: "tools:read"})
	})
	p.set("POST /mcp", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+p.server.URL+`/resource-metadata"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{}})
	})
	return p
}

func (p *oauthProvider) resourceMetadata() map[string]any {
	return map[string]any{"resource": p.server.URL + "/mcp", "authorization_servers": []string{p.server.URL + "/issuer"}, "scopes_supported": []string{"tools:read", "tools:write"}}
}

func (p *oauthProvider) issuerMetadata() map[string]any {
	return map[string]any{
		"issuer": p.server.URL + "/issuer", "authorization_endpoint": p.server.URL + "/authorize", "token_endpoint": p.server.URL + "/token",
		"authorization_response_iss_parameter_supported": true, "code_challenge_methods_supported": []string{"S256"}, "response_types_supported": []string{"code"}, "grant_types_supported": []string{"authorization_code", "refresh_token"}, "token_endpoint_auth_methods_supported": []string{"none", "client_secret_basic"},
	}
}

func (p *oauthProvider) set(path string, handler http.HandlerFunc) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.routes[path] = handler
}

func (p *oauthProvider) observed(method, path string) []observedRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	var result []observedRequest
	for _, r := range p.requests {
		if r.Method == method && r.Path == path {
			result = append(result, r)
		}
	}
	return result
}

func (p *oauthProvider) mcpCalls() []observedRequest {
	var result []observedRequest
	for _, request := range p.observed(http.MethodPost, "/mcp") {
		if request.Authorization != "" {
			result = append(result, request)
		}
	}
	return result
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

type oauthFixture struct {
	t      *testing.T
	ctx    context.Context
	pool   *pgxpool.Pool
	s      *Service
	p      *oauthProvider
	actor  core.Actor
	server core.MCPServer
}

func newOAuthFixture(t *testing.T) *oauthFixture {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	base, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "oauth_" + strings.ReplaceAll(core.NewID(), "-", "")
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = base.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		base.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := base.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error(err)
		}
		base.Close()
	})
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
	p := newProvider(t)
	s, err := New(pool, []byte(strings.Repeat("k", 32)), p.policy, "https://console.example")
	if err != nil {
		t.Fatal(err)
	}
	a := core.Actor{ID: core.NewID(), WorkspaceID: core.NewID(), Role: core.RoleAdmin}
	server := core.MCPServer{ID: core.NewID(), WorkspaceID: a.WorkspaceID, Enabled: true, MCPServerInput: core.MCPServerInput{Name: "OAuth fixture", Namespace: "oauth", URL: p.server.URL + "/mcp", TimeoutMS: 5000}}
	if _, err = pool.Exec(ctx, `INSERT INTO mcp_servers(workspace_id,id,name,namespace,url,timeout_ms) VALUES($1,$2,$3,$4,$5,$6)`, a.WorkspaceID, server.ID, server.Name, server.Namespace, server.URL, server.TimeoutMS); err != nil {
		t.Fatal(err)
	}
	return &oauthFixture{t: t, ctx: ctx, pool: pool, s: s, p: p, actor: a, server: server}
}

func revision(v int64) *int64 { return &v }

func (f *oauthFixture) input(method string) ConfigInput {
	in := ConfigInput{ExpectedVersion: revision(0), Configuration: Configuration{Issuer: f.p.server.URL + "/issuer", ClientID: "registered-client", AuthMethod: method, Scopes: []string{"tools:read"}}}
	if method == "client_secret_basic" {
		in.ClientSecret = "registered-client-secret"
	}
	return in
}

func (f *oauthFixture) configure(method string) Status {
	f.t.Helper()
	status, err := f.s.Configure(f.ctx, f.actor, f.server.ID, f.input(method))
	if err != nil {
		f.t.Fatal(err)
	}
	return status
}

func (f *oauthFixture) start(version int64) url.Values {
	f.t.Helper()
	result, err := f.s.Connect(f.ctx, f.actor, f.server.ID, VersionInput{revision(version)}, "opaque-browser-binding")
	if err != nil {
		f.t.Fatal(err)
	}
	u, err := url.Parse(result.AuthorizationURL)
	if err != nil {
		f.t.Fatal(err)
	}
	return u.Query()
}

func (f *oauthFixture) connect(method string) {
	f.t.Helper()
	status := f.configure(method)
	query := f.start(status.Version)
	if err := f.s.Complete(f.ctx, f.actor, "opaque-browser-binding", query.Get("state"), "provider-code", "", f.p.server.URL+"/issuer"); err != nil {
		f.t.Fatal(err)
	}
}

func (f *oauthFixture) status() Status {
	f.t.Helper()
	status, err := f.s.Status(f.ctx, f.actor, f.server.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	return status
}

func (f *oauthFixture) expire() {
	f.t.Helper()
	if _, err := f.pool.Exec(f.ctx, `UPDATE mcp_upstream_oauth SET expires_at=clock_timestamp()-interval '1 second'`); err != nil {
		f.t.Fatal(err)
	}
}
