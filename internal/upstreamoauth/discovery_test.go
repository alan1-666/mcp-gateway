package upstreamoauth

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestDiscoveryChallengeAndWellKnownFallbacks(t *testing.T) {
	for _, mode := range []string{"challenge", "path-resource", "root-resource", "oidc-prefix", "oidc-suffix"} {
		t.Run(mode, func(t *testing.T) {
			p := newProvider(t)
			if mode == "path-resource" || mode == "root-resource" {
				p.set("POST /mcp", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusMethodNotAllowed) })
				path := "/.well-known/oauth-protected-resource/mcp"
				if mode == "root-resource" {
					path = "/.well-known/oauth-protected-resource"
				}
				p.set("GET "+path, func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, p.resourceMetadata()) })
			}
			if strings.HasPrefix(mode, "oidc-") {
				p.set("GET /.well-known/oauth-authorization-server/issuer", nil)
				path := "/.well-known/openid-configuration/issuer"
				if mode == "oidc-suffix" {
					path = "/issuer/.well-known/openid-configuration"
				}
				p.set("GET "+path, func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, p.issuerMetadata()) })
			}
			s := &Service{policy: p.policy, redirect: "https://console.example" + CallbackPath}
			m, err := s.discover(context.Background(), "workspace", p.server.URL+"/mcp", "")
			if err != nil {
				t.Fatal(err)
			}
			if m.Resource != p.server.URL+"/mcp" || m.Issuer != p.server.URL+"/issuer" || m.TokenEndpoint != p.server.URL+"/token" || m.RedirectURI != s.redirect || !reflect.DeepEqual(m.AuthMethods, []string{"none", "client_secret_basic"}) {
				t.Fatalf("incorrect bound metadata: %+v", m)
			}
			if len(p.observed("POST", "/token")) != 0 || len(p.observed("GET", "/mcp")) != 0 {
				t.Fatal("discovery exchanged a token or depended on GET")
			}
			for _, request := range p.observed("POST", "/mcp") {
				if request.RPCMethod != "initialize" || request.Authorization != "" {
					t.Fatal("discovery executed business method or forwarded credentials")
				}
			}
		})
	}
}

func TestDiscoveryRejectsUntrustedMetadata(t *testing.T) {
	cases := []struct {
		name             string
		resource, issuer func(map[string]any)
		selected         string
	}{
		{name: "different-resource", resource: func(m map[string]any) { m["resource"] = "https://different.example/mcp" }},
		{name: "no-issuers", resource: func(m map[string]any) { m["authorization_servers"] = []string{} }},
		{name: "insecure-issuer", resource: func(m map[string]any) { m["authorization_servers"] = []string{"http://issuer.example"} }},
		{name: "unadvertised-issuer", selected: "https://different.example"},
		{name: "issuer-mismatch", issuer: func(m map[string]any) { m["issuer"] = "https://different.example" }},
		{name: "missing-pkce", issuer: func(m map[string]any) { delete(m, "code_challenge_methods_supported") }},
		{name: "missing-issuer-response-binding", issuer: func(m map[string]any) { delete(m, "authorization_response_iss_parameter_supported") }},
		{name: "unsupported-issuer-response-binding", issuer: func(m map[string]any) { m["authorization_response_iss_parameter_supported"] = false }},
		{name: "plain-pkce", issuer: func(m map[string]any) { m["code_challenge_methods_supported"] = []string{"plain"} }},
		{name: "implicit-only", issuer: func(m map[string]any) { m["response_types_supported"] = []string{"token"} }},
		{name: "unsupported-grant", issuer: func(m map[string]any) { m["grant_types_supported"] = []string{"client_credentials"} }},
		{name: "unsupported-auth", issuer: func(m map[string]any) { m["token_endpoint_auth_methods_supported"] = []string{"private_key_jwt"} }},
		{name: "foreign-token-origin", issuer: func(m map[string]any) { m["token_endpoint"] = "https://attacker.example/token" }},
		{name: "foreign-authorization-origin", issuer: func(m map[string]any) { m["authorization_endpoint"] = "https://attacker.example/authorize" }},
		{name: "token-query", issuer: func(m map[string]any) { m["token_endpoint"] = m["token_endpoint"].(string) + "?redirect=elsewhere" }},
		{name: "invalid-scope", resource: func(m map[string]any) { m["scopes_supported"] = []string{"read write"} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newProvider(t)
			resource, issuer := p.resourceMetadata(), p.issuerMetadata()
			if tc.resource != nil {
				tc.resource(resource)
			}
			if tc.issuer != nil {
				tc.issuer(issuer)
			}
			p.set("GET /resource-metadata", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, resource) })
			p.set("GET /.well-known/oauth-authorization-server/issuer", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, issuer) })
			s := &Service{policy: p.policy}
			if _, err := s.discover(context.Background(), "workspace", p.server.URL+"/mcp", tc.selected); err == nil {
				t.Fatal("unsafe discovery accepted")
			}
		})
	}
}

func TestMetadataRedirectsAndResponseBounds(t *testing.T) {
	for _, mode := range []string{"redirect", "foreign-challenge", "conflicting-challenge", "oversize", "trailing-json", "duplicate-json-key"} {
		t.Run(mode, func(t *testing.T) {
			p := newProvider(t)
			s := &Service{policy: p.policy}
			switch mode {
			case "redirect":
				p.set("GET /resource-metadata", func(w http.ResponseWriter, r *http.Request) {
					http.Redirect(w, r, p.server.URL+"/redirected", http.StatusFound)
				})
				p.set("GET /redirected", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, p.resourceMetadata()) })
			case "foreign-challenge":
				p.set("POST /mcp", func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="https://attacker.example/metadata"`)
					w.WriteHeader(http.StatusUnauthorized)
				})
			case "conflicting-challenge":
				p.set("POST /mcp", func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Add("WWW-Authenticate", `Bearer resource_metadata="`+p.server.URL+`/resource-metadata"`)
					w.Header().Add("WWW-Authenticate", `Bearer resource_metadata="`+p.server.URL+`/other"`)
					w.WriteHeader(http.StatusUnauthorized)
				})
			case "oversize":
				p.set("GET /resource-metadata", func(w http.ResponseWriter, _ *http.Request) {
					_, _ = w.Write([]byte(`{"padding":"` + strings.Repeat("x", maxJSON) + `"}`))
				})
			case "trailing-json":
				p.set("GET /resource-metadata", func(w http.ResponseWriter, _ *http.Request) {
					writeJSON(w, p.resourceMetadata())
					writeJSON(w, map[string]string{"another": "document"})
				})
			case "duplicate-json-key":
				p.set("GET /resource-metadata", func(w http.ResponseWriter, _ *http.Request) {
					raw, _ := json.Marshal(p.resourceMetadata())
					_, _ = w.Write(append([]byte(`{"resource":"https://attacker.example",`), raw[1:]...))
				})
			}
			if _, err := s.discover(context.Background(), "workspace", p.server.URL+"/mcp", ""); err == nil {
				t.Fatal("unsafe metadata accepted")
			}
			if len(p.observed("GET", "/redirected")) != 0 {
				t.Fatal("followed metadata redirect")
			}
		})
	}
}

func TestMetadataFallbackCannotReusePartiallyDecodedSecurityFields(t *testing.T) {
	for _, kind := range []string{"resource", "issuer"} {
		t.Run(kind, func(t *testing.T) {
			p := newProvider(t)
			if kind == "resource" {
				p.set("POST /mcp", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) })
				first := p.resourceMetadata()
				first["authorization_servers"] = 42
				second := p.resourceMetadata()
				delete(second, "resource")
				p.set("GET /.well-known/oauth-protected-resource/mcp", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, first) })
				p.set("GET /.well-known/oauth-protected-resource", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, second) })
			} else {
				first := p.issuerMetadata()
				first["token_endpoint_auth_methods_supported"] = 42
				second := p.issuerMetadata()
				delete(second, "code_challenge_methods_supported")
				p.set("GET /.well-known/oauth-authorization-server/issuer", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, first) })
				p.set("GET /.well-known/openid-configuration/issuer", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, second) })
			}
			s := &Service{policy: p.policy}
			if _, err := s.discover(context.Background(), "workspace", p.server.URL+"/mcp", ""); err == nil {
				t.Fatal("fallback reused fields from rejected metadata")
			}
		})
	}
}

func TestDiscoverySelectsAllowedIssuerWithoutOverridingExplicitChoice(t *testing.T) {
	p := newProvider(t)
	metadata := p.resourceMetadata()
	metadata["authorization_servers"] = []string{"https://blocked.example/issuer", p.server.URL + "/issuer"}
	p.set("GET /resource-metadata", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, metadata) })
	s := &Service{policy: p.policy}
	selected, err := s.discover(context.Background(), "workspace", p.server.URL+"/mcp", "")
	if err != nil || selected.Issuer != p.server.URL+"/issuer" {
		t.Fatal("first blocked issuer prevented usable selection", err)
	}
	if _, err := s.discover(context.Background(), "workspace", p.server.URL+"/mcp", "https://blocked.example/issuer"); err == nil {
		t.Fatal("explicit issuer silently replaced")
	}
}

func TestDiscoveryClosesUnexpectedAnonymousSession(t *testing.T) {
	p := newProvider(t)
	p.set("POST /mcp", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Mcp-Session-Id", "anonymous-probe-session")
		writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": "oauth-discovery", "result": map[string]any{}})
	})
	p.set("DELETE /mcp", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Mcp-Session-Id") != "anonymous-probe-session" || r.Header.Get("Authorization") != "" {
			t.Error("invalid anonymous session teardown")
		}
		w.WriteHeader(http.StatusNoContent)
	})
	p.set("GET /.well-known/oauth-protected-resource/mcp", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, p.resourceMetadata()) })
	s := &Service{policy: p.policy}
	if _, err := s.discover(context.Background(), "workspace", p.server.URL+"/mcp", ""); err != nil {
		t.Fatal(err)
	}
	if len(p.observed("DELETE", "/mcp")) != 1 || len(p.observed("POST", "/mcp")) != 1 {
		t.Fatal("discovery leaked or reinitialized anonymous session")
	}
}
