package upstreamoauth

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestSDKSessionUsesOAuthForInitializeListAndCallWithout401Replay(t *testing.T) {
	f := newOAuthFixture(t)
	f.connect("none")
	sdk := mcp.NewServer(&mcp.Implementation{Name: "oauth-protected-fixture", Version: "1"}, nil)
	var executed, callRequests atomic.Int32
	var reject atomic.Bool
	sdk.AddTool(&mcp.Tool{Name: "lookup", Description: "Read the fixture record", InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		executed.Add(1)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "fixture record"}}}, nil
	})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return sdk }, &mcp.StreamableHTTPOptions{JSONResponse: true, SessionTimeout: time.Second})
	protected := func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer access-secret-1" {
			t.Error("SDK request missing bound OAuth token")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodPost {
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				return
			}
			_ = r.Body.Close()
			r.Body = io.NopCloser(bytes.NewReader(raw))
			var request struct {
				Method string `json:"method"`
			}
			if err := json.Unmarshal(raw, &request); err != nil {
				t.Error(err)
				return
			}
			// Exercise initialize negotiation, including SDK clients that probe the
			// newer optional server/discover method before falling back.
			if request.Method == "server/discover" {
				http.NotFound(w, r)
				return
			}
			if request.Method == "tools/call" {
				callRequests.Add(1)
				if reject.Load() {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
			}
		}
		handler.ServeHTTP(w, r)
	}
	f.p.set("POST /mcp", protected)
	f.p.set("DELETE /mcp", protected)
	f.p.set("GET /mcp", protected)
	client, closeClient, err := f.p.policy.NewClientContext(f.ctx, f.actor.WorkspaceID, f.server.URL, "", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer closeClient()
	client.Transport, err = f.s.WrapTransport(f.ctx, f.actor, f.server, client.Transport)
	if err != nil {
		t.Fatal(err)
	}
	agent := mcp.NewClient(&mcp.Implementation{Name: "oauth-agent-fixture", Version: "1"}, nil)
	session, err := agent.Connect(f.ctx, &mcp.StreamableClientTransport{Endpoint: f.server.URL, HTTPClient: client, MaxRetries: -1, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	catalog, err := session.ListTools(f.ctx, nil)
	if err != nil || len(catalog.Tools) != 1 || catalog.Tools[0].Name != "lookup" {
		t.Fatal("authenticated catalog failed", err)
	}
	result, err := session.CallTool(f.ctx, &mcp.CallToolParams{Name: "lookup", Arguments: map[string]any{}})
	if err != nil || result.IsError || len(result.Content) != 1 || executed.Load() != 1 {
		t.Fatal("authenticated call failed", err)
	}
	seen := map[string]bool{}
	for _, request := range f.p.mcpCalls() {
		seen[request.RPCMethod] = true
		if request.Authorization != "Bearer access-secret-1" {
			t.Fatal("token missing on SDK message")
		}
	}
	for _, method := range []string{"initialize", "tools/list", "tools/call"} {
		if !seen[method] {
			t.Fatal("SDK method not exercised", method)
		}
	}
	reject.Store(true)
	if _, err := session.CallTool(f.ctx, &mcp.CallToolParams{Name: "lookup", Arguments: map[string]any{}}); err == nil {
		t.Fatal("401 hidden from SDK caller")
	}
	if callRequests.Load() != 2 || executed.Load() != 1 || len(f.p.observed("POST", "/token")) != 1 || f.status().Status != "reconnect_required" {
		t.Fatal("authentication failure refreshed or replayed business call")
	}
	before := len(f.p.mcpCalls())
	if _, err := session.ListTools(f.ctx, nil); err == nil {
		t.Fatal("revoked SDK session remained usable")
	}
	if len(f.p.mcpCalls()) != before {
		t.Fatal("revocation dispatched another SDK request")
	}
}

func TestConfiguringOAuthFencesPreviouslyOpenedAnonymousTransport(t *testing.T) {
	f := newOAuthFixture(t)
	old, err := f.s.WrapTransport(f.ctx, f.actor, f.server, f.p.policy.base)
	if err != nil {
		t.Fatal(err)
	}
	f.configure("none")
	before := len(f.p.observed("POST", "/mcp"))
	request, _ := http.NewRequestWithContext(f.ctx, http.MethodPost, f.server.URL, strings.NewReader(`{"jsonrpc":"2.0","method":"initialize","id":1}`))
	if _, err := old.RoundTrip(request); err == nil {
		t.Fatal("anonymous session survived OAuth configuration")
	}
	if len(f.p.observed("POST", "/mcp")) != before {
		t.Fatal("anonymous request reached protected resource after configuration")
	}
}
