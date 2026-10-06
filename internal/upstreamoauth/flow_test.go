package upstreamoauth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/alan1-666/mcp-gateway/internal/upstreams"
)

func TestAuthorizationLifecyclePKCEAndSessionBinding(t *testing.T) {
	f := newOAuthFixture(t)
	if status := f.status(); status.Status != "unconfigured" || status.Version != 0 {
		t.Fatal(status)
	}
	configured := f.configure("client_secret_basic")
	if configured.Status != "disconnected" || configured.Version != 1 {
		t.Fatal(configured)
	}
	q := f.start(configured.Version)
	if q.Get("redirect_uri") != "https://console.example"+CallbackPath || q.Get("resource") != f.server.URL || q.Get("client_id") != "registered-client" || q.Get("response_type") != "code" || q.Get("scope") != "tools:read" || q.Get("code_challenge_method") != "S256" || len(q.Get("state")) != 43 {
		t.Fatalf("incorrect authorization parameters: %v", q)
	}
	v, err := read(f.ctx, f.pool, f.actor.WorkspaceID, f.server.ID)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := f.s.open(f.actor.WorkspaceID, f.server.ID, "verifier", v.Verifier)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(verifier)
	if q.Get("code_challenge") != base64.RawURLEncoding.EncodeToString(hash[:]) || v.State == nil || *v.State == q.Get("state") || *v.State != digest(q.Get("state")) {
		t.Fatal("state or PKCE persistence is not protected")
	}
	if bytes.Contains(v.Verifier, verifier) || bytes.Contains(v.Secret, []byte("registered-client-secret")) {
		t.Fatal("plaintext persisted")
	}
	if _, err := f.s.open("other-workspace", f.server.ID, "verifier", v.Verifier); err == nil {
		t.Fatal("encrypted verifier not workspace bound")
	}
	if _, err := f.s.open(f.actor.WorkspaceID, "other-server", "verifier", v.Verifier); err == nil {
		t.Fatal("encrypted verifier not server bound")
	}
	for _, tc := range []struct{ binding, state, issuer string }{
		{"another-session", q.Get("state"), f.p.server.URL + "/issuer"},
		{"opaque-browser-binding", strings.Repeat("x", 43), f.p.server.URL + "/issuer"},
		{"opaque-browser-binding", q.Get("state"), "https://other-issuer.example"},
		{"opaque-browser-binding", q.Get("state"), ""},
	} {
		if err := f.s.Complete(f.ctx, f.actor, tc.binding, tc.state, "provider-code", "", tc.issuer); err == nil {
			t.Fatal("unbound callback accepted")
		}
	}
	if f.status().Status != "pending" || len(f.p.observed("POST", "/token")) != 0 {
		t.Fatal("invalid callback consumed legitimate attempt")
	}
	if err := f.s.Complete(f.ctx, f.actor, "opaque-browser-binding", q.Get("state"), "provider-code", "", f.p.server.URL+"/issuer"); err != nil {
		t.Fatal(err)
	}
	if status := f.status(); status.Status != "connected" || status.Version != 4 || status.ExpiresAt == nil {
		t.Fatal(status)
	}
	if err := f.s.Complete(f.ctx, f.actor, "opaque-browser-binding", q.Get("state"), "provider-code", "", f.p.server.URL+"/issuer"); err == nil {
		t.Fatal("callback state reused")
	}
	requests := f.p.observed("POST", "/token")
	if len(requests) != 1 || requests[0].Values.Get("code_verifier") != string(verifier) || requests[0].Values.Get("resource") != f.server.URL || requests[0].Values.Get("redirect_uri") != f.s.redirect {
		t.Fatalf("bad code exchange: %+v", requests)
	}
	r := &http.Request{Header: http.Header{"Authorization": []string{requests[0].Authorization}}}
	user, password, ok := r.BasicAuth()
	if !ok || user != url.QueryEscape("registered-client") || password != url.QueryEscape("registered-client-secret") {
		t.Fatal("registered confidential client not authenticated")
	}
	v, err = read(f.ctx, f.pool, f.actor.WorkspaceID, f.server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if v.State != nil || v.Session != nil || v.Actor != nil || v.Verifier != nil {
		t.Fatal("completed attempt retained replay material")
	}
	statusJSON, _ := json.Marshal(f.status())
	var audits string
	if err := f.pool.QueryRow(f.ctx, `SELECT string_agg(data::text,'') FROM audit_events`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"access-secret-1", "refresh-secret-1", "registered-client-secret", string(verifier), q.Get("state"), "provider-code"} {
		if bytes.Contains(v.Token, []byte(secret)) || bytes.Contains(statusJSON, []byte(secret)) || strings.Contains(audits, secret) {
			t.Fatal("OAuth secret leaked into storage/status/audit")
		}
	}
}

func TestManagementAuthorizationVersionsAndInvalidConfiguration(t *testing.T) {
	f := newOAuthFixture(t)
	for _, role := range []core.Role{core.RoleViewer, core.RoleOperator, core.RoleApprover} {
		a := f.actor
		a.Role = role
		if _, err := f.s.Status(f.ctx, a, f.server.ID); !errors.Is(err, core.ErrForbidden) {
			t.Fatal("status permission", err)
		}
		if _, err := f.s.Discover(f.ctx, a, f.server.ID, ""); !errors.Is(err, core.ErrForbidden) {
			t.Fatal("discovery permission", err)
		}
		if _, err := f.s.Configure(f.ctx, a, f.server.ID, f.input("none")); !errors.Is(err, core.ErrForbidden) {
			t.Fatal("configuration permission", err)
		}
		if _, err := f.s.Connect(f.ctx, a, f.server.ID, VersionInput{revision(0)}, "binding"); !errors.Is(err, core.ErrForbidden) {
			t.Fatal("connect permission", err)
		}
		if _, err := f.s.Disconnect(f.ctx, a, f.server.ID, VersionInput{revision(0)}); !errors.Is(err, core.ErrForbidden) {
			t.Fatal("disconnect permission", err)
		}
	}
	foreign := f.actor
	foreign.WorkspaceID = core.NewID()
	if _, err := f.s.Status(f.ctx, foreign, f.server.ID); !errors.Is(err, core.ErrNotFound) {
		t.Fatal("cross-workspace status", err)
	}
	if _, err := f.s.Configure(f.ctx, foreign, f.server.ID, f.input("none")); !errors.Is(err, core.ErrNotFound) {
		t.Fatal("cross-workspace configure", err)
	}
	machine := f.actor
	machine.ClientID = "gateway-client"
	if _, err := f.s.Configure(f.ctx, machine, f.server.ID, f.input("none")); !errors.Is(err, core.ErrForbidden) {
		t.Fatal("machine configured OAuth", err)
	}
	for _, mutate := range []func(*ConfigInput){
		func(in *ConfigInput) { in.ExpectedVersion = nil }, func(in *ConfigInput) { in.ExpectedVersion = revision(-1) },
		func(in *ConfigInput) { in.ClientID = "bad\nclient" }, func(in *ConfigInput) { in.ClientSecret = "public-client-secret" },
		func(in *ConfigInput) { in.AuthMethod = "client_secret_basic" }, func(in *ConfigInput) { in.AuthMethod = "client_secret_post" },
		func(in *ConfigInput) { in.Scopes = []string{"read write"} }, func(in *ConfigInput) { in.Scopes = []string{"read", "read"} },
	} {
		in := f.input("none")
		mutate(&in)
		if _, err := f.s.Configure(f.ctx, f.actor, f.server.ID, in); !errors.Is(err, core.ErrInvalid) {
			t.Fatal("invalid configuration accepted", err)
		}
	}
	f.configure("none")
	before := len(f.p.observed("POST", "/mcp"))
	if _, err := f.s.Configure(f.ctx, f.actor, f.server.ID, f.input("none")); !errors.Is(err, core.ErrConflict) {
		t.Fatal("stale version accepted", err)
	}
	if len(f.p.observed("POST", "/mcp")) != before {
		t.Fatal("stale configuration made outbound requests")
	}
	if _, err := f.s.Connect(f.ctx, f.actor, f.server.ID, VersionInput{revision(1)}, ""); !errors.Is(err, core.ErrForbidden) {
		t.Fatal("missing session accepted", err)
	}
	if _, err := f.s.Connect(f.ctx, f.actor, f.server.ID, VersionInput{revision(0)}, "binding"); !errors.Is(err, core.ErrConflict) {
		t.Fatal("stale connect accepted", err)
	}
	if _, err := f.s.Disconnect(f.ctx, f.actor, f.server.ID, VersionInput{revision(0)}); !errors.Is(err, core.ErrConflict) {
		t.Fatal("stale disconnect accepted", err)
	}
	if _, err := f.pool.Exec(f.ctx, `UPDATE mcp_servers SET enabled=false`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Connect(f.ctx, f.actor, f.server.ID, VersionInput{revision(1)}, "binding"); !errors.Is(err, core.ErrConflict) {
		t.Fatal("disabled connect accepted", err)
	}
	if _, err := f.s.Discover(f.ctx, f.actor, f.server.ID, ""); !errors.Is(err, core.ErrConflict) {
		t.Fatal("disabled discovery accepted", err)
	}
	if _, err := f.pool.Exec(f.ctx, `UPDATE mcp_servers SET enabled=true,credential_ref='STATIC_KEY'`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Discover(f.ctx, f.actor, f.server.ID, ""); !errors.Is(err, core.ErrConflict) {
		t.Fatal("static and OAuth combined", err)
	}
}

func TestRefreshClaimSharedAcrossProcessesAndRotatesExactlyOnce(t *testing.T) {
	f := newOAuthFixture(t)
	f.connect("none")
	f.expire()
	second, err := New(f.pool, []byte(strings.Repeat("k", 32)), f.p.policy, "https://console.example")
	if err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	var count atomic.Int32
	f.p.set("POST /token", func(w http.ResponseWriter, r *http.Request) {
		if count.Add(1) == 1 {
			close(started)
		}
		if r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("refresh_token") != "refresh-secret-1" || r.Form.Get("resource") != f.server.URL {
			t.Errorf("bad refresh binding %v", r.Form)
		}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		writeJSON(w, grant{AccessToken: "access-secret-2", RefreshToken: "refresh-secret-2", TokenType: "Bearer", ExpiresIn: revision(3600)})
	})
	var wg sync.WaitGroup
	for n := range 8 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			s := f.s
			if n%2 == 1 {
				s = second
			}
			token, version, configured, err := s.access(f.ctx, f.actor, f.server.ID)
			if err != nil || !configured || token != "access-secret-2" || version != 6 {
				t.Errorf("refresh caller: token=%q version=%d configured=%v error=%v", token, version, configured, err)
			}
		}(n)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("refresh did not start")
	}
	if status := f.status(); status.Status != "refreshing" {
		t.Error(status)
	}
	waitContext, cancel := context.WithTimeout(f.ctx, 50*time.Millisecond)
	_, _, _, waitErr := second.access(waitContext, f.actor, f.server.ID)
	cancel()
	if !errors.Is(waitErr, context.DeadlineExceeded) {
		t.Error("concurrent refresh waiter ignored operation deadline", waitErr)
	}
	close(release)
	wg.Wait()
	if count.Load() != 1 || len(f.p.observed("POST", "/token")) != 2 {
		t.Fatal("refresh token replayed")
	}
	v, err := read(f.ctx, f.pool, f.actor.WorkspaceID, f.server.ID)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := f.s.open(f.actor.WorkspaceID, f.server.ID, "token", v.Token)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(plain, []byte("refresh-secret-2")) || bytes.Contains(plain, []byte("refresh-secret-1")) {
		t.Fatal("rotated token not saved")
	}
}

func TestRefreshFailuresNeverReplaySingleUseToken(t *testing.T) {
	for _, mode := range []string{"rejected", "ambiguous-connection-loss", "missing-rotation", "same-rotation", "unexpected-scope", "redirect", "oversize"} {
		t.Run(mode, func(t *testing.T) {
			f := newOAuthFixture(t)
			f.connect("none")
			f.expire()
			f.p.set("POST /token", func(w http.ResponseWriter, r *http.Request) {
				g := grant{AccessToken: "access-secret-2", RefreshToken: "refresh-secret-2", TokenType: "Bearer", ExpiresIn: revision(3600)}
				switch mode {
				case "rejected":
					http.Error(w, "provider-sensitive-error refresh-secret-1", http.StatusBadRequest)
					return
				case "ambiguous-connection-loss":
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					_ = conn.Close()
					return
				case "missing-rotation":
					g.RefreshToken = ""
				case "same-rotation":
					g.RefreshToken = "refresh-secret-1"
				case "unexpected-scope":
					g.Scope = "admin:write"
				case "redirect":
					http.Redirect(w, r, f.p.server.URL+"/other-token", http.StatusTemporaryRedirect)
					return
				case "oversize":
					_, _ = w.Write([]byte(`{"access_token":"` + strings.Repeat("x", maxJSON) + `"}`))
					return
				}
				writeJSON(w, g)
			})
			for range 2 {
				_, _, configured, err := f.s.access(f.ctx, f.actor, f.server.ID)
				if err == nil || !configured {
					t.Fatal("failed grant allowed runtime access")
				}
				if strings.Contains(err.Error(), "provider-sensitive-error") || strings.Contains(err.Error(), "refresh-secret-1") {
					t.Fatal("provider error leaked")
				}
			}
			if len(f.p.observed("POST", "/token")) != 2 || len(f.p.observed("POST", "/other-token")) != 0 {
				t.Fatal("failed refresh retried or redirected")
			}
			v, err := read(f.ctx, f.pool, f.actor.WorkspaceID, f.server.ID)
			if err != nil || v.Status != "reconnect_required" || v.Token != nil {
				t.Fatal("uncertain token retained", v.Status, err)
			}
		})
	}
}

func TestDisconnectFencesInflightExchanges(t *testing.T) {
	for _, phase := range []string{"authorization", "refresh"} {
		t.Run(phase, func(t *testing.T) {
			f := newOAuthFixture(t)
			var q url.Values
			if phase == "refresh" {
				f.connect("none")
				f.expire()
			} else {
				f.configure("none")
				q = f.start(1)
			}
			started, release := make(chan struct{}), make(chan struct{})
			f.p.set("POST /token", func(w http.ResponseWriter, r *http.Request) {
				close(started)
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
				writeJSON(w, grant{AccessToken: "late-access", RefreshToken: "late-refresh", TokenType: "Bearer", ExpiresIn: revision(3600)})
			})
			done := make(chan error, 1)
			go func() {
				if phase == "refresh" {
					_, _, _, err := f.s.access(f.ctx, f.actor, f.server.ID)
					done <- err
				} else {
					done <- f.s.Complete(f.ctx, f.actor, "opaque-browser-binding", q.Get("state"), "provider-code", "", f.p.server.URL+"/issuer")
				}
			}()
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				close(release)
				t.Fatal("exchange not started")
			}
			current := f.status()
			_, err := f.s.Disconnect(f.ctx, f.actor, f.server.ID, VersionInput{revision(current.Version)})
			close(release)
			if err != nil {
				t.Fatal(err)
			}
			if err := <-done; !errors.Is(err, core.ErrConflict) {
				t.Fatal("old exchange resurrected grant", err)
			}
			if status := f.status(); status.Status != "disconnected" {
				t.Fatal(status)
			}
			if _, _, configured, err := f.s.access(f.ctx, f.actor, f.server.ID); err == nil || !configured {
				t.Fatal("disconnected grant fell back to anonymous")
			}
		})
	}
}

func TestTransportExactDestinationRevocationAndNo401Replay(t *testing.T) {
	f := newOAuthFixture(t)
	f.connect("none")
	for _, mutate := range []func(*core.MCPServer){func(s *core.MCPServer) { s.URL = f.p.server.URL + "/attacker" }, func(s *core.MCPServer) { s.WorkspaceID = "other-workspace" }} {
		server := f.server
		mutate(&server)
		if _, err := f.s.WrapTransport(f.ctx, f.actor, server, f.p.policy.base); err == nil {
			t.Fatal("forged transport binding accepted")
		}
	}
	transport, err := f.s.WrapTransport(f.ctx, f.actor, f.server, f.p.policy.base)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{f.p.server.URL + "/other", f.server.URL + "?x=1"} {
		req, _ := http.NewRequestWithContext(f.ctx, http.MethodPost, target, strings.NewReader(`{}`))
		if _, err := transport.RoundTrip(req); err == nil {
			t.Fatal("token forwarded to another resource")
		}
	}
	req, _ := http.NewRequestWithContext(f.ctx, http.MethodPost, f.server.URL, strings.NewReader(`{"jsonrpc":"2.0","method":"tools/call","id":1}`))
	req.Header.Set("Authorization", "Bearer gateway-caller-key")
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	seen := f.p.mcpCalls()
	if len(seen) != 1 || seen[0].Authorization != "Bearer access-secret-1" || req.Header.Get("Authorization") != "Bearer gateway-caller-key" {
		t.Fatal("incorrect token injection or mutated caller")
	}
	f.p.set("POST /mcp", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) })
	req, _ = http.NewRequestWithContext(f.ctx, http.MethodPost, f.server.URL, strings.NewReader(`{"jsonrpc":"2.0","method":"tools/call","id":2}`))
	resp, err = transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized || len(f.p.mcpCalls()) != 2 || len(f.p.observed("POST", "/token")) != 1 {
		t.Fatal("401 refreshed or replayed MCP call")
	}
	if _, err := transport.RoundTrip(req); err == nil {
		t.Fatal("rejected grant remained usable")
	}
	if len(f.p.mcpCalls()) != 2 || f.status().Status != "reconnect_required" {
		t.Fatal("revocation ignored")
	}
}

func TestExpiredAttemptsAndAbandonedClaimsDoNotResume(t *testing.T) {
	for _, phase := range []string{"pending", "exchanging", "refreshing"} {
		t.Run(phase, func(t *testing.T) {
			f := newOAuthFixture(t)
			f.configure("none")
			q := f.start(1)
			if _, err := f.pool.Exec(f.ctx, `UPDATE mcp_upstream_oauth SET status=$1,deadline=clock_timestamp()-interval '1 second'`, phase); err != nil {
				t.Fatal(err)
			}
			if f.status().Status != "reconnect_required" {
				t.Fatal("abandoned claim not visible")
			}
			if err := f.s.Complete(f.ctx, f.actor, "opaque-browser-binding", q.Get("state"), "provider-code", "", f.p.server.URL+"/issuer"); err == nil {
				t.Fatal("expired attempt resumed")
			}
			if _, _, configured, err := f.s.access(f.ctx, f.actor, f.server.ID); err == nil || !configured {
				t.Fatal("abandoned exchange retried")
			}
			if len(f.p.observed("POST", "/token")) != 0 {
				t.Fatal("expired claim exchanged token")
			}
		})
	}
}

func TestConcurrentCodeCallbacksExchangeOnce(t *testing.T) {
	f := newOAuthFixture(t)
	f.configure("none")
	q := f.start(1)
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	f.p.set("POST /token", func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			close(started)
		}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		writeJSON(w, grant{AccessToken: "access-secret-1", RefreshToken: "refresh-secret-1", TokenType: "Bearer", ExpiresIn: revision(3600)})
	})
	done := make(chan error, 1)
	go func() {
		done <- f.s.Complete(f.ctx, f.actor, "opaque-browser-binding", q.Get("state"), "provider-code", "", f.p.server.URL+"/issuer")
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("code exchange not started")
	}
	err := f.s.Complete(f.ctx, f.actor, "opaque-browser-binding", q.Get("state"), "provider-code", "", f.p.server.URL+"/issuer")
	close(release)
	if err == nil {
		t.Fatal("concurrent callback accepted")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || f.status().Status != "connected" {
		t.Fatal("authorization code replayed")
	}
}

func TestTokenExpiryValidationAndDefaultScopes(t *testing.T) {
	for _, expiry := range []*int64{nil, revision(0), revision(-1), revision(31536001)} {
		name := "missing-expiry"
		if expiry != nil {
			name = time.Duration(*expiry).String()
		}
		t.Run(name, func(t *testing.T) {
			f := newOAuthFixture(t)
			f.configure("none")
			q := f.start(1)
			f.p.set("POST /token", func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(w, grant{AccessToken: "access", TokenType: "Bearer", ExpiresIn: expiry})
			})
			err := f.s.Complete(f.ctx, f.actor, "opaque-browser-binding", q.Get("state"), "provider-code", "", f.p.server.URL+"/issuer")
			if expiry == nil {
				if err != nil || f.status().ExpiresAt != nil || f.status().Status != "connected" {
					t.Fatal("missing lifetime rejected or guessed", err)
				}
				if _, _, _, err := f.s.access(f.ctx, f.actor, f.server.ID); err != nil {
					t.Fatal(err)
				}
				if len(f.p.observed("POST", "/token")) != 1 {
					t.Fatal("unknown expiry triggered speculative refresh")
				}
			} else if err == nil || f.status().Status != "reconnect_required" {
				t.Fatal("invalid explicit expiry accepted", err)
			}
		})
	}
	t.Run("provider-default-scope-is-pinned-for-refresh", func(t *testing.T) {
		f := newOAuthFixture(t)
		in := f.input("none")
		in.Scopes = nil
		if _, err := f.s.Configure(f.ctx, f.actor, f.server.ID, in); err != nil {
			t.Fatal(err)
		}
		q := f.start(1)
		if q.Has("scope") {
			t.Fatal("default scopes overridden")
		}
		if err := f.s.Complete(f.ctx, f.actor, "opaque-browser-binding", q.Get("state"), "provider-code", "", f.p.server.URL+"/issuer"); err != nil {
			t.Fatal("provider default rejected", err)
		}
		f.expire()
		f.p.set("POST /token", func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, grant{AccessToken: "access-2", RefreshToken: "refresh-2", TokenType: "Bearer", ExpiresIn: revision(3600), Scope: "admin:write"})
		})
		if _, _, _, err := f.s.access(f.ctx, f.actor, f.server.ID); err == nil {
			t.Fatal("refresh widened provider default scopes")
		}
	})
}

func TestServerDisableRevokesGrantAndPendingAttempt(t *testing.T) {
	for _, phase := range []string{"pending", "connected"} {
		t.Run(phase, func(t *testing.T) {
			f := newOAuthFixture(t)
			var q url.Values
			var transport http.RoundTripper
			if phase == "pending" {
				f.configure("none")
				q = f.start(1)
			} else {
				f.connect("none")
				var err error
				transport, err = f.s.WrapTransport(f.ctx, f.actor, f.server, f.p.policy.base)
				if err != nil {
					t.Fatal(err)
				}
			}
			upstream := upstreams.New(upstreams.NewStore(f.pool), nil)
			if _, err := upstream.SetEnabled(f.ctx, f.actor, f.server.ID, false); err != nil {
				t.Fatal(err)
			}
			if _, err := upstream.SetEnabled(f.ctx, f.actor, f.server.ID, true); err != nil {
				t.Fatal(err)
			}
			if f.status().Status != "reconnect_required" {
				t.Fatal("server re-enabled prior grant")
			}
			if phase == "pending" {
				if err := f.s.Complete(f.ctx, f.actor, "opaque-browser-binding", q.Get("state"), "provider-code", "", f.p.server.URL+"/issuer"); err == nil {
					t.Fatal("disabled attempt recovered")
				}
				if len(f.p.observed("POST", "/token")) != 0 {
					t.Fatal("disabled callback sent code")
				}
			} else {
				req, _ := http.NewRequestWithContext(f.ctx, http.MethodPost, f.server.URL, strings.NewReader(`{}`))
				if _, err := transport.RoundTrip(req); err == nil {
					t.Fatal("disabled session transport remained usable")
				}
				if len(f.p.mcpCalls()) != 0 {
					t.Fatal("old token dispatched")
				}
			}
			v, err := read(f.ctx, f.pool, f.actor.WorkspaceID, f.server.ID)
			if err != nil || v.Token != nil || v.State != nil || v.Verifier != nil {
				t.Fatal("disable retained old grant", err)
			}
		})
	}
}

func TestReconfigurationAndNewConnectInvalidateOldAttempts(t *testing.T) {
	f := newOAuthFixture(t)
	f.configure("none")
	first := f.start(1)
	second := f.start(2)
	if first.Get("state") == second.Get("state") {
		t.Fatal("authorization reused state")
	}
	if err := f.s.Complete(f.ctx, f.actor, "opaque-browser-binding", first.Get("state"), "provider-code", "", f.p.server.URL+"/issuer"); err == nil {
		t.Fatal("replaced authorization attempt accepted")
	}
	in := f.input("none")
	in.ExpectedVersion = revision(3)
	if _, err := f.s.Configure(f.ctx, f.actor, f.server.ID, in); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Complete(f.ctx, f.actor, "opaque-browser-binding", second.Get("state"), "provider-code", "", f.p.server.URL+"/issuer"); err == nil {
		t.Fatal("reconfigured authorization attempt accepted")
	}
	if len(f.p.observed("POST", "/token")) != 0 || f.status().Status != "disconnected" {
		t.Fatal("old authorization exchanged code")
	}
}

func TestDeclinedConsentConsumesAttemptWithoutTokenRequest(t *testing.T) {
	f := newOAuthFixture(t)
	f.configure("none")
	q := f.start(1)
	providerError := "sensitive-provider-denial"
	err := f.s.Complete(f.ctx, f.actor, "opaque-browser-binding", q.Get("state"), "", providerError, f.p.server.URL+"/issuer")
	if err == nil || strings.Contains(err.Error(), providerError) {
		t.Fatal("denial not sanitized", err)
	}
	if len(f.p.observed("POST", "/token")) != 0 || f.status().Status != "reconnect_required" {
		t.Fatal("declined consent exchanged code")
	}
	if err := f.s.Complete(f.ctx, f.actor, "opaque-browser-binding", q.Get("state"), "provider-code", "", f.p.server.URL+"/issuer"); err == nil {
		t.Fatal("declined attempt replayed")
	}
}
