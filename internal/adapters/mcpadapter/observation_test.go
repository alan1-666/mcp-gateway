package mcpadapter

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alan1-666/mcp-gateway/internal/adapters/httpadapter"
	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestExecutionObservationFailureBoundary(t *testing.T) {
	for _, tc := range []struct {
		name, method, code, phase string
		status                    int
		attempted                 bool
		mode                      string
	}{
		{"ok", "", "ok", "result", 0, true, ""},
		{"auth", "initialize", "authentication_rejected", "session", 401, false, ""},
		{"throttle", "initialize", "rate_limited", "session", 429, false, ""},
		{"unavailable", "initialize", "upstream_unavailable", "session", 503, false, ""},
		{"catalog", "tools/list", "upstream_unavailable", "catalog", 502, false, ""},
		{"write-network", "tools/call", "upstream_unavailable", "call", 503, true, ""},
		{"call-throttle", "tools/call", "rate_limited", "call", 429, true, ""},
		{"timeout", "tools/call", "timeout", "call", 0, true, "timeout"},
		{"tool-error", "", "tool_error", "call", 0, true, "tool-error"},
		{"invalid-content", "", "result_invalid", "result", 0, true, "invalid-content"},
		{"schema", "", "schema_changed", "catalog", 0, false, "schema"},
		{"arguments", "", "arguments_invalid", "catalog", 0, false, "arguments"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sdk := mcp.NewServer(&mcp.Implementation{Name: "observation", Version: "1"}, nil)
			var calls atomic.Int32
			sdk.AddTool(basicTool("read"), func(ctx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				calls.Add(1)
				if tc.mode == "tool-error" {
					return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "PRIVATE-UPSTREAM-ERROR"}}}, nil
				}
				if tc.mode == "invalid-content" {
					return &mcp.CallToolResult{Content: []mcp.Content{&mcp.ImageContent{Data: []byte("PRIVATE"), MIMEType: "image/png"}}}, nil
				}
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "PRIVATE-DATA"}}}, nil
			})
			handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return sdk }, &mcp.StreamableHTTPOptions{JSONResponse: true})
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				r.Body = io.NopCloser(bytes.NewReader(raw))
				var message struct{ Method string }
				_ = json.Unmarshal(raw, &message)
				if message.Method == "server/discover" {
					http.Error(w, "unsupported", 404)
					return
				}
				if message.Method == tc.method {
					if tc.mode == "timeout" {
						<-r.Context().Done()
						return
					}
					http.Error(w, "PRIVATE-UPSTREAM-ERROR", tc.status)
					return
				}
				handler.ServeHTTP(w, r)
			}))
			defer upstream.Close()
			config := serverConfig(upstream.URL, "server", "workspace")
			if tc.mode == "timeout" {
				config.TimeoutMS = 100
			}
			egress, err := httpadapter.New([]string{upstream.URL}, []string{"127.0.0.0/8"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			adapter := New(egress, servers{config.ID: config})
			tool := imported(config, core.RemoteTool{Name: "read", InputSchema: objectSchema, SchemaHash: schemaHash("read", objectSchema, nil)}, core.RiskWrite)
			if tc.mode == "schema" {
				tool.MCP.SchemaHash = strings.Repeat("a", 64)
			}
			args := json.RawMessage(`{}`)
			if tc.mode == "arguments" {
				args = json.RawMessage(`{"unexpected":1}`)
			}
			result := adapter.Execute(context.Background(), core.Actor{ID: "actor", WorkspaceID: "workspace"}, tool, core.Operation{Arguments: args})
			o := result.MCPObservation
			if o == nil || o.Validate() != nil || o.Code != tc.code || o.Phase != tc.phase || o.CallAttempted != tc.attempted {
				t.Fatalf("observation %+v", o)
			}
			if tc.code == "ok" && result.State != core.StateSucceeded {
				t.Fatal(result.State)
			}
			if tc.code != "ok" && tc.attempted && result.State != core.StateUnknown {
				t.Fatal("write uncertainty lost", result.State)
			}
			if tc.code != "ok" && !tc.attempted && result.State != core.StateFailed {
				t.Fatal("pre-call failure uncertain", result.State)
			}
			encoded, _ := json.Marshal(o)
			if strings.Contains(string(encoded), "PRIVATE") {
				t.Fatal("telemetry included payload")
			}
			if calls.Load() > 1 {
				t.Fatal("call replayed")
			}
		})
	}
}

func TestExecutionObservationReuseRevocationAndClassification(t *testing.T) {
	f := newPoolFixture(t, 2, time.Second)
	for range 2 {
		result := f.execute(context.Background(), f.actor)
		if result.MCPObservation == nil || result.MCPObservation.Code != "ok" || !result.MCPObservation.CallAttempted || result.MCPObservation.HTTPStatus != 200 {
			t.Fatal("bad retained session trace", result.MCPObservation)
		}
	}
	if f.handshakes.Load() != 1 || f.calls.Load() != 2 {
		t.Fatal("telemetry broke reuse")
	}
	f.resolver.mu.Lock()
	f.resolver.denied = true
	f.resolver.mu.Unlock()
	result := f.execute(context.Background(), f.actor)
	if result.MCPObservation.Code != "configuration_unavailable" || result.MCPObservation.CallAttempted || f.calls.Load() != 2 {
		t.Fatal("revoked server dispatched")
	}
	for _, code := range []string{"cancelled", "timeout", "connection_failed"} {
		ctx := context.Background()
		var cancel context.CancelFunc
		if code == "cancelled" {
			ctx, cancel = context.WithCancel(ctx)
			cancel()
		}
		if code == "timeout" {
			ctx, cancel = context.WithDeadline(ctx, time.Now().Add(-time.Second))
			defer cancel()
		}
		if observationFailure(ctx, nil, "connection_failed") != code {
			t.Fatal("classification", code)
		}
	}
	timer := newExecutionTimer()
	for _, p := range []string{"session", "catalog", "call", "result"} {
		timer.phase(p)
	}
	if timer.finish(nil).Validate() != nil {
		t.Fatal("nil transport timer")
	}
}
