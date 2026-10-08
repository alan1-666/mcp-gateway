package mcpadapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alan1-666/mcp-gateway/internal/adapters/httpadapter"
	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/alan1-666/mcp-gateway/internal/upstreams"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Catalog changes are driven by actual protocol requests, not an injected
// comparison result. The fixture counts business calls and second connections.
func TestFreshSessionDiagnosticsObserveContractChanges(t *testing.T) {
	for _, mode := range []string{"stable", "format", "description", "input", "output", "removed", "added", "renamed", "empty"} {
		t.Run(mode, func(t *testing.T) {
			sdk := mcp.NewServer(&mcp.Implementation{Name: "fresh-session", Version: "1"}, nil)
			var lists, calls, connects atomic.Int32
			sdk.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
				return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
					if method == "server/discover" || method == "initialize" {
						connects.Add(1)
					}
					if method == "tools/call" {
						calls.Add(1)
					}
					if method != "tools/list" {
						return next(ctx, method, req)
					}
					page := lists.Add(1)
					tool := basicTool("lookup")
					if mode == "empty" {
						return &mcp.ListToolsResult{Tools: []*mcp.Tool{}}, nil
					}
					if page == 2 {
						switch mode {
						case "format":
							tool.InputSchema = json.RawMessage(`{ "additionalProperties": false, "properties": {}, "type": "object" }`)
						case "description":
							tool.Description = "updated documentation"
						case "input":
							tool.InputSchema = json.RawMessage(`{"type":"object","properties":{"SessionId":{"type":"string","const":"private-session-2","default":"private-session-2"}}}`)
						case "output":
							tool.OutputSchema = json.RawMessage(`{"type":"object","required":["newField"]}`)
						case "removed":
							return &mcp.ListToolsResult{Tools: []*mcp.Tool{}}, nil
						case "added":
							return &mcp.ListToolsResult{Tools: []*mcp.Tool{tool, basicTool("new_tool")}}, nil
						case "renamed":
							tool.Name = "renamed"
						}
					}
					return &mcp.ListToolsResult{Tools: []*mcp.Tool{tool}}, nil
				}
			})
			host := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return sdk }, &mcp.StreamableHTTPOptions{JSONResponse: true, Stateless: true}))
			defer host.Close()
			egress, err := httpadapter.New([]string{host.URL}, []string{"127.0.0.0/8"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			config := serverConfig(host.URL, "fresh", "workspace")
			report := New(egress, nil).Check(context.Background(), core.Actor{WorkspaceID: "workspace"}, config)
			stable := mode == "stable" || mode == "format" || mode == "description" || mode == "empty"
			if stable && (report.Status != "ok" || report.SessionContractStatus != "stable") {
				t.Fatalf("stable observation %+v", report)
			}
			if !stable && (report.Status != "degraded" || report.Code != "session_catalog_changed" || report.SessionContractStatus != "changed") {
				t.Fatalf("changing catalog was not identified: %+v", report)
			}
			if !stable && mode != "added" && (report.IncompatibleCount != 1 || report.Tools[0].Code != "session_contract_changed") {
				t.Fatalf("changed tool not identified: %+v", report)
			}
			if mode == "added" && (report.CompatibleCount != 1 || report.IncompatibleCount != 0) {
				t.Fatalf("unchanged original tool incorrectly rejected: %+v", report)
			}
			if connects.Load() != 2 || lists.Load() != 2 || calls.Load() != 0 {
				t.Fatalf("expected independent handshakes and no execution: connections=%d lists=%d calls=%d", connects.Load(), lists.Load(), calls.Load())
			}
			raw, _ := json.Marshal(report)
			if strings.Contains(string(raw), "private-session") || strings.Contains(string(raw), "SessionId") {
				t.Fatal("session-bound schema leaked into report")
			}
		})
	}
}

func TestFreshSessionFailureIsNotReportedAsStable(t *testing.T) {
	for _, phase := range []string{"connect", "discovery", "invalid-schema"} {
		t.Run(phase, func(t *testing.T) {
			sdk, host, calls := sdkServer(true, 1, "", basicTool("lookup"))
			defer host.Close()
			var connects, lists atomic.Int32
			sdk.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
				return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
					if method == "server/discover" || method == "initialize" {
						if connects.Add(1) >= 2 && phase == "connect" {
							return nil, errors.New("private upstream connection error")
						}
					}
					if method == "tools/list" && lists.Add(1) == 2 {
						if phase == "discovery" {
							return nil, errors.New("private upstream discovery error")
						}
						if phase == "invalid-schema" {
							return &mcp.ListToolsResult{Tools: []*mcp.Tool{{Name: "lookup", InputSchema: json.RawMessage(`{"type":"object","properties":{"x":{"$ref":"https://private.invalid/schema"}}}`)}}}, nil
						}
					}
					return next(ctx, method, req)
				}
			})
			egress, _ := httpadapter.New([]string{host.URL}, []string{"127.0.0.0/8"}, nil)
			report := New(egress, nil).Check(context.Background(), core.Actor{WorkspaceID: "workspace"}, serverConfig(host.URL, "fresh", "workspace"))
			if report.Status != "failed" || report.SessionContractStatus != "unverified" || report.Code != "session_verification_failed" || calls.Load() != 0 {
				t.Fatalf("fresh session failure %+v calls=%d", report, calls.Load())
			}
			raw, _ := json.Marshal(report)
			if strings.Contains(string(raw), "private") {
				t.Fatal("upstream details exposed")
			}
		})
	}
}

func TestDiagnosticFailureClassification(t *testing.T) {
	for _, tc := range []struct {
		status      int
		code, stage string
	}{
		{401, "authentication_rejected", "authentication"}, {403, "authentication_rejected", "authentication"},
		{429, "upstream_rate_limited", "connect"}, {500, "upstream_unavailable", "connect"}, {503, "upstream_unavailable", "connect"},
		{404, "endpoint_incompatible", "connect"}, {405, "endpoint_incompatible", "connect"}, {400, "connection_failed", "connect"},
	} {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "private-token-and-error", tc.status) }))
			defer host.Close()
			egress, _ := httpadapter.New([]string{host.URL}, []string{"127.0.0.0/8"}, nil)
			report := New(egress, nil).Check(context.Background(), core.Actor{WorkspaceID: "workspace"}, serverConfig(host.URL, "failure", "workspace"))
			if report.Code != tc.code || report.Stage != tc.stage || report.Status != "failed" {
				t.Fatalf("classification %+v", report)
			}
			if strings.Contains(report.Message, "private") {
				t.Fatal("error body exposed")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if report := diagnosticFailure(ctx, nil, upstreams.CheckReport{}, "discovery", "fallback", "fallback"); report.Code != "check_cancelled" {
		t.Fatal(report)
	}
	timed, done := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer done()
	if report := diagnosticFailure(timed, nil, upstreams.CheckReport{}, "compatibility", "fallback", "fallback"); report.Code != "check_timeout" {
		t.Fatal(report)
	}
	if report := diagnosticFailure(context.Background(), nil, upstreams.CheckReport{}, "connect", "fallback", "fallback"); report.Code != "fallback" {
		t.Fatal(report)
	}
}

func TestFreshSessionCheckRetainsOriginalDeadlineAndClosesClients(t *testing.T) {
	_, host, calls := sdkServer(true, 1, "", basicTool("lookup"))
	defer host.Close()
	var created, closed atomic.Int32
	adapter := New(nil, nil)
	adapter.SetTransportFactory(func(ctx context.Context, _ core.MCPServer) (*http.Client, func(), error) {
		if created.Add(1) == 2 {
			<-ctx.Done()
			return nil, nil, ctx.Err()
		}
		return &http.Client{Transport: http.DefaultTransport}, func() { closed.Add(1) }, nil
	})
	config := serverConfig(host.URL, "deadline", "workspace")
	config.TimeoutMS = 100
	start := time.Now()
	report := adapter.Check(context.Background(), core.Actor{WorkspaceID: "workspace"}, config)
	if report.Code != "check_timeout" || report.SessionContractStatus != "unverified" || created.Load() != 2 || closed.Load() != 1 || calls.Load() != 0 {
		t.Fatalf("deadline/cleanup %+v created=%d closed=%d", report, created.Load(), closed.Load())
	}
	if time.Since(start) > time.Second {
		t.Fatal("fresh session reset the check deadline")
	}
}

func TestDiscoveryAuthenticationFailureUsesFixedClassification(t *testing.T) {
	_, target, _ := sdkServer(true, 1, "", basicTool("lookup"))
	defer target.Close()
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body.Close()
		r.Body = io.NopCloser(bytes.NewReader(body))
		var message struct {
			Method string `json:"method"`
		}
		_ = json.Unmarshal(body, &message)
		if message.Method == "tools/list" {
			http.Error(w, "private-denial", 401)
			return
		}
		// Forward only this test's fixed requests to its local SDK handler.
		target.Config.Handler.ServeHTTP(w, r)
	}))
	defer host.Close()
	egress, _ := httpadapter.New([]string{host.URL}, []string{"127.0.0.0/8"}, nil)
	report := New(egress, nil).Check(context.Background(), core.Actor{WorkspaceID: "workspace"}, serverConfig(host.URL, "auth", "workspace"))
	if report.Code != "authentication_rejected" || report.Stage != "authentication" || report.SessionContractStatus != "not_checked" {
		t.Fatal(report)
	}
}
