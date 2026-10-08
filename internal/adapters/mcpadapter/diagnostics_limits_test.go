package mcpadapter

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/alan1-666/mcp-gateway/internal/adapters/httpadapter"
	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestDiagnosticsRetainBoundedCompleteCatalogRules(t *testing.T) {
	for _, tc := range []struct{ mode, code string }{
		{"invalid-name", "unsupported_definitions"}, {"duplicate", "unsupported_definitions"},
		{"nul-name", "invalid_response"},
		{"bad-output", "unsupported_definitions"}, {"tool-count", "catalog_limit"},
		{"catalog-bytes", "catalog_limit"}, {"page-count", "catalog_limit"},
		{"repeated-cursor", "repeated_cursor"}, {"long-cursor", "repeated_cursor"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			sdk := mcp.NewServer(&mcp.Implementation{Name: "bounded-check", Version: "1"}, nil)
			var pages, calls atomic.Int32
			sdk.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
				return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
					if method == "tools/call" {
						calls.Add(1)
					}
					if method != "tools/list" {
						return next(ctx, method, req)
					}
					page := pages.Add(1)
					out := &mcp.ListToolsResult{Tools: []*mcp.Tool{basicTool(fmt.Sprintf("tool_%d", page))}}
					switch tc.mode {
					case "invalid-name":
						out.Tools[0].Name = ""
					case "nul-name":
						out.Tools[0].Name = "invalid\x00name"
					case "duplicate":
						out.Tools = append(out.Tools, out.Tools[0])
					case "bad-output":
						out.Tools[0].OutputSchema = json.RawMessage(`{"$ref":"https://private.invalid/schema"}`)
					case "tool-count":
						out.Tools = nil
						for i := 0; i < maxTools+1; i++ {
							out.Tools = append(out.Tools, basicTool(fmt.Sprintf("tool_%d", i)))
						}
					case "catalog-bytes":
						out.Tools = nil
						for i := 0; i < 100; i++ {
							tool := basicTool(fmt.Sprintf("tool_%d_%d", page, i))
							tool.Description = strings.Repeat("x", 3900)
							out.Tools = append(out.Tools, tool)
						}
						out.NextCursor = fmt.Sprint(page)
					case "page-count":
						out.NextCursor = fmt.Sprint(page)
					case "repeated-cursor":
						out.NextCursor = "same"
					case "long-cursor":
						out.NextCursor = strings.Repeat("x", 2049)
					}
					return out, nil
				}
			})
			host := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return sdk }, &mcp.StreamableHTTPOptions{JSONResponse: true, Stateless: true}))
			defer host.Close()
			egress, _ := httpadapter.New([]string{host.URL}, []string{"127.0.0.0/8"}, nil)
			config := serverConfig(host.URL, "bounds", "workspace")
			config.TimeoutMS = 10000
			report := New(egress, nil).Check(context.Background(), core.Actor{WorkspaceID: "workspace"}, config)
			if report.Code != tc.code || report.SessionContractStatus != "not_checked" || calls.Load() != 0 {
				t.Fatalf("bounded diagnostic %+v calls=%d", report, calls.Load())
			}
			if tc.mode == "page-count" && pages.Load() != maxPages {
				t.Fatalf("page limit %d", pages.Load())
			}
			if len(report.Tools) > maxTools {
				t.Fatal("report exceeded tool limit")
			}
		})
	}
}

func TestDiagnosticsDoNotAssumeConnectorFreshSessionSupport(t *testing.T) {
	server := serverConfig("", "private", "workspace")
	server.ConnectorID = "connector"
	server.TargetName = "docs"
	remote := core.RemoteTool{Name: "lookup", InputSchema: objectSchema}
	remote.SchemaHash = schemaHash(remote.Name, remote.InputSchema, nil)
	delegate := &privateDelegate{tools: []core.RemoteTool{remote}}
	adapter := New(nil, nil)
	adapter.SetDelegate(delegate)
	report := adapter.Check(context.Background(), core.Actor{WorkspaceID: "workspace"}, server)
	if report.Status != "ok" || report.SessionContractStatus != "not_checked" || delegate.calls != 0 {
		t.Fatal(report)
	}
	adapter.SetDelegate(nil)
	report = adapter.Check(context.Background(), core.Actor{WorkspaceID: "workspace"}, server)
	if report.Code != "connector_unavailable" || report.Status != "failed" {
		t.Fatal(report)
	}
	server.ConnectorID = ""
	server.URL = "https://denied.invalid/mcp"
	if report = adapter.Check(context.Background(), core.Actor{WorkspaceID: "workspace"}, server); report.Code != "configuration_blocked" {
		t.Fatal(report)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	egress, _ := httpadapter.New([]string{"https://denied.invalid"}, nil, nil)
	if report = New(egress, nil).Check(ctx, core.Actor{WorkspaceID: "workspace"}, server); report.Code != "check_cancelled" {
		t.Fatal(report)
	}
}
