package mcpadapter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/alan1-666/mcp-gateway/internal/adapters/httpadapter"
	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestDiagnosticsReportsCompatibilityWithoutWeakeningDiscovery(t *testing.T) {
	sdk := mcp.NewServer(&mcp.Implementation{Name: "diagnostics", Version: "1"}, nil)
	var pages, calls atomic.Int32
	sdk.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method == "tools/call" {
				calls.Add(1)
			}
			if method != "tools/list" {
				return next(ctx, method, req)
			}
			pages.Add(1)
			out := &mcp.ListToolsResult{Tools: []*mcp.Tool{basicTool("valid"), {Name: "bad_schema", InputSchema: json.RawMessage(`{"type":"object","properties":{"x":{"$ref":"https://sensitive.invalid/schema-secret"}}}`)}, {Name: "huge_description", Description: strings.Repeat("sensitive-description", 400), InputSchema: objectSchema}}}
			return out, nil
		}
	})
	host := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return sdk }, &mcp.StreamableHTTPOptions{JSONResponse: true, Stateless: true}))
	defer host.Close()
	egress, _ := httpadapter.New([]string{host.URL}, []string{"127.0.0.0/8"}, nil)
	adapter := New(egress, nil)
	server := serverConfig(host.URL, "diag", "workspace")
	server.TimeoutMS = 10000
	actor := core.Actor{WorkspaceID: "workspace"}
	report := adapter.Check(context.Background(), actor, server)
	if report.Status != "degraded" || report.Stage != "compatibility" || report.CompatibleCount != 1 || report.IncompatibleCount != 2 || calls.Load() != 0 {
		t.Fatalf("compatibility report %+v calls=%d", report, calls.Load())
	}
	raw, _ := json.Marshal(report)
	if strings.Contains(string(raw), "schema-secret") || strings.Contains(string(raw), "sensitive-description") {
		t.Fatal("untrusted schema or description in diagnostics")
	}
	if _, err := adapter.Discover(context.Background(), actor, server); err == nil {
		t.Fatal("diagnostics weakened normal discovery")
	}
	if pages.Load() != 2 {
		t.Fatalf("diagnostics/strict discovery unexpected calls=%d", pages.Load())
	}
}

func TestDiagnosticsCompletesPaginatedCatalogAndDoesNotCallTools(t *testing.T) {
	_, host, calls := sdkServer(true, 1, "", basicTool("one"), basicTool("two"))
	defer host.Close()
	egress, _ := httpadapter.New([]string{host.URL}, []string{"127.0.0.0/8"}, nil)
	adapter := New(egress, nil)
	server := serverConfig(host.URL, "diag", "workspace")
	server.TimeoutMS = 10000
	report := adapter.Check(context.Background(), core.Actor{WorkspaceID: "workspace"}, server)
	if report.Status != "ok" || report.CompatibleCount != 2 || calls.Load() != 0 {
		t.Fatalf("pagination diagnostic %+v calls=%d", report, calls.Load())
	}
	server.Enabled = false
	report = adapter.Check(context.Background(), core.Actor{WorkspaceID: "workspace"}, server)
	if report.Stage != "policy" || report.Code != "server_disabled" {
		t.Fatalf("disabled diagnostic %+v", report)
	}
}
