package httpadapter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync/atomic"
	"testing"

	"github.com/alan1-666/mcp-gateway/internal/core"
)

func TestEgressBlocksMetadataAndUnapprovedPrivateNetworks(t *testing.T) {
	a, _ := New(nil, nil, nil)
	for _, ip := range []string{"127.0.0.1", "10.0.0.1", "100.100.100.200", "100.64.0.1", "169.254.169.254", "::1", "::ffff:127.0.0.1", "0.0.0.0", "fe80::1"} {
		if a.allowedIP(netip.MustParseAddr(ip)) {
			t.Fatalf("allowed %s", ip)
		}
	}
	a, _ = New(nil, []string{"127.0.0.0/8", "169.254.0.0/16"}, nil)
	if !a.allowedIP(netip.MustParseAddr("127.0.0.1")) || a.allowedIP(netip.MustParseAddr("169.254.169.254")) {
		t.Fatal("explicit loopback or unconditional metadata restriction failed")
	}
}
func TestRedirectDoesNotForwardCredential(t *testing.T) {
	var calls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
	defer source.Close()
	a, _ := New([]string{source.URL, target.URL}, []string{"127.0.0.0/8"}, []Credential{{WorkspaceID: "w", Ref: "SERVICE", Origin: source.URL, Headers: map[string]string{"Authorization": "Bearer downstream-secret"}}})
	r := a.Execute(context.Background(), core.Actor{WorkspaceID: "w"}, core.Tool{Risk: core.RiskWrite, HTTP: core.HTTPConfig{URL: source.URL, Method: "POST", TimeoutMS: 1000, CredentialRef: "SERVICE"}}, core.Operation{ID: "op", Arguments: json.RawMessage(`{}`)})
	if r.State != core.StateUnknown || calls.Load() != 0 {
		t.Fatalf("redirect followed or write certainty overstated: %+v", r)
	}
}
func TestJSONOutputIsBoundedAndRedacted(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("q") != "one & two" {
			t.Error("query encoding")
		}
		_, _ = w.Write([]byte(`{"items":[{"name":"ok","access_token":"secret"}],"password":"secret"}`))
	}))
	defer s.Close()
	a, _ := New([]string{s.URL}, []string{"127.0.0.0/8"}, nil)
	r := a.Execute(context.Background(), core.Actor{}, core.Tool{Risk: core.RiskRead, HTTP: core.HTTPConfig{URL: s.URL, Method: "GET", TimeoutMS: 1000}}, core.Operation{ID: "op", Arguments: json.RawMessage(`{"q":"one & two"}`)})
	if r.State != core.StateSucceeded || string(r.Result) != `{"items":[{"access_token":"[REDACTED]","name":"ok"}],"password":"[REDACTED]"}` {
		t.Fatalf("unexpected sanitized output: %s %s", r.State, r.Result)
	}
}
