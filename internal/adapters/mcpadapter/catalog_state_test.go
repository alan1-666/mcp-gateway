package mcpadapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alan1-666/mcp-gateway/internal/adapters/httpadapter"
	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const catalogNotice = `{"jsonrpc":"2.0","method":"notifications/tools/list_changed"}`

func TestCatalogNotificationFraming(t *testing.T) {
	for _, tc := range []struct {
		name, data string
		count      int
	}{
		{"LF", "data: " + catalogNotice + "\n\n", 1},
		{"CRLF", "data: " + catalogNotice + "\r\n\r\n", 1},
		{"CR", "data: " + catalogNotice + "\r\r", 1},
		{"BOM-comments", "\xef\xbb\xbf: comment\ndata: " + catalogNotice + "\n\n", 1},
		{"message-multiline", "event: message\nid: 4\nretry: 5\ndata: {\"jsonrpc\":\"2.0\",\ndata: \"method\":\"notifications/tools/list_changed\"}\n\n", 1},
		{"reset-event", "event: ignored\ndata: " + catalogNotice + "\n\nevent\ndata: " + catalogNotice + "\n\n", 1},
		{"unknown-event", "event: ignored\ndata: " + catalogNotice + "\n\n", 0},
		{"incomplete", "data: " + catalogNotice + "\n", 0},
		{"invalid-json", "data: {secret-not-json}\n\n", 0},
		{"different-method", "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/resources/list_changed\"}\n\n", 0},
		{"request-id", "data: {\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"notifications/tools/list_changed\"}\n\n", 0},
		{"null-id", "data: {\"jsonrpc\":\"2.0\",\"id\":null,\"method\":\"notifications/tools/list_changed\"}\n\n", 0},
		{"wrong-version", "data: {\"jsonrpc\":\"1.0\",\"method\":\"notifications/tools/list_changed\"}\n\n", 0},
		{"result-is-not-notification", "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/tools/list_changed\",\"result\":{}}\n\n", 0},
		{"error-is-not-notification", "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/tools/list_changed\",\"error\":{}}\n\n", 0},
		{"empty-data", "data\n\n", 0},
		{"several", "data: " + catalogNotice + "\n\ndata: " + catalogNotice + "\n\n", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, chunk := range []int{1, 7, len(tc.data)} {
				count := 0
				scanner := &catalogNotificationScanner{changed: func() { count++ }}
				for remaining := []byte(tc.data); len(remaining) > 0; {
					n := min(chunk, len(remaining))
					scanner.read(remaining[:n])
					remaining = remaining[n:]
				}
				if count != tc.count {
					t.Fatalf("chunk=%d count=%d want=%d", chunk, count, tc.count)
				}
			}
		})
	}
}

func TestCatalogInvalidationPreventsDispatchAndCannotBeResetByLease(t *testing.T) {
	var sent atomic.Int32
	transport := &protocolTransport{ctx: context.Background(), responses: map[string]*responseCapture{}, base: roundTripFunc(func(*http.Request) (*http.Response, error) {
		sent.Add(1)
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})}
	version := transport.catalogVersion()
	if err := transport.verifyCatalog(version); err != nil || !transport.catalogReusable() {
		t.Fatal("fresh complete observation not accepted")
	}
	transport.catalogChanged()
	if !errors.Is(transport.verifyCatalog(version), errCatalogChanged) || transport.catalogReusable() {
		t.Fatal("old observation survived invalidation")
	}
	call := func() error {
		req, _ := http.NewRequest("POST", "http://test.invalid", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"write"}}`))
		resp, err := transport.RoundTrip(req)
		if resp != nil {
			resp.Body.Close()
		}
		return err
	}
	if !errors.Is(call(), errCatalogChanged) || transport.callSent || sent.Load() != 0 {
		t.Fatal("invalidated call was marked dispatched or sent")
	}
	transport.begin(context.Background())
	defer transport.idle()
	if !errors.Is(call(), errCatalogChanged) || sent.Load() != 0 {
		t.Fatal("lease reset authorized stale catalog")
	}
	if err := transport.verifyCatalog(transport.catalogVersion()); err != nil {
		t.Fatal(err)
	}
	if err := call(); err != nil || sent.Load() != 1 {
		t.Fatal("new complete observation did not permit one call", err)
	}
	// Concurrent notices are bounded metadata, never catalog payload storage.
	var workers sync.WaitGroup
	for range 20 {
		workers.Go(func() { transport.catalogChanged() })
	}
	workers.Wait()
	if transport.catalogVersion() != version+21 || transport.catalogReusable() {
		t.Fatal("lost concurrent invalidation")
	}
	if err := call(); err == nil || errors.Is(err, errCatalogChanged) || sent.Load() != 1 {
		t.Fatal("a previously dispatched call was mislabeled as pre-dispatch invalidation", err)
	}
}

type catalogFixture struct {
	adapter      *Adapter
	server       core.MCPServer
	actor        core.Actor
	tool         core.Tool
	mode         atomic.Int32 // 0 stable, 1 list notice, 2 paginated notice, 3 call notice
	lists, calls atomic.Int32
}

func newCatalogFixture(t *testing.T) *catalogFixture {
	t.Helper()
	f := &catalogFixture{actor: core.Actor{ID: "catalog-owner", WorkspaceID: "workspace", Role: core.RoleAdmin}}
	sdk := mcp.NewServer(&mcp.Implementation{Name: "catalog-fixture", Version: "1"}, nil)
	sdk.AddTool(basicTool("read"), func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil
	})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return sdk }, &mcp.StreamableHTTPOptions{JSONResponse: true, Stateless: true})
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Method string          `json:"method"`
			ID     json.RawMessage `json:"id"`
			Params struct {
				Cursor string `json:"cursor"`
			} `json:"params"`
		}
		if r.Body != nil {
			data, _ := io.ReadAll(r.Body)
			r.Body.Close()
			r.Body = io.NopCloser(bytes.NewReader(data))
			_ = json.Unmarshal(data, &request)
		}
		mode := f.mode.Load()
		if request.Method == "tools/list" {
			f.lists.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			tool, cursor := basicTool("read"), ""
			if mode == 2 {
				if request.Params.Cursor == "" {
					cursor = "second"
				} else {
					tool = basicTool("other")
				}
			}
			if mode == 1 || mode == 2 && request.Params.Cursor != "" {
				fmt.Fprintf(w, "data: %s\r\n\r\n", catalogNotice)
			}
			result, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": &mcp.ListToolsResult{Tools: []*mcp.Tool{tool}, NextCursor: cursor}})
			fmt.Fprintf(w, "event: message\r\ndata: %s\r\n\r\n", result)
			return
		}
		if request.Method == "tools/call" {
			f.calls.Add(1)
			if mode == 3 {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(w, "data: %s\n\n", catalogNotice)
				result, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}})
				fmt.Fprintf(w, "data: %s\n\n", result)
				return
			}
		}
		handler.ServeHTTP(w, r)
	}))
	f.server = serverConfig(host.URL, "catalog", "workspace")
	egress, _ := httpadapter.New([]string{host.URL}, []string{"127.0.0.0/8"}, nil)
	f.adapter = New(egress, servers{f.server.ID: f.server})
	if err := f.adapter.EnableSessionPool(SessionPoolOptions{MaxSessions: 2, IdleTTL: time.Second, MaxLifetime: 10 * time.Second}); err != nil {
		t.Fatal(err)
	}
	remote, err := f.adapter.Discover(context.Background(), f.actor, f.server)
	if err != nil {
		t.Fatal(err)
	}
	f.tool = imported(f.server, remote[0], core.RiskRead)
	f.lists.Store(0)
	t.Cleanup(func() { f.adapter.Close(); host.Close() })
	return f
}

func TestActualSDKRejectsNotifiedCatalogAndNeverDispatchesWrites(t *testing.T) {
	for _, mode := range []int32{1, 2} {
		for _, risk := range []core.Risk{core.RiskRead, core.RiskWrite} {
			t.Run(fmt.Sprintf("mode-%d-%s", mode, risk), func(t *testing.T) {
				f := newCatalogFixture(t)
				f.mode.Store(mode)
				f.tool.Risk = risk
				result := f.adapter.Execute(context.Background(), f.actor, f.tool, core.Operation{Arguments: json.RawMessage(`{}`)})
				if result.State != core.StateFailed || f.calls.Load() != 0 || f.adapter.SessionPoolStats().Retained != 0 {
					t.Fatalf("result=%+v calls=%d", result, f.calls.Load())
				}
				items, err := f.adapter.Discover(context.Background(), f.actor, f.server)
				if !errors.Is(err, errCatalogChanged) || items != nil {
					t.Fatalf("partial catalog accepted: %+v %v", items, err)
				}
				report := f.adapter.Check(context.Background(), f.actor, f.server)
				if report.Code != "catalog_changed" || report.Status != "failed" || len(report.Tools) != 0 || report.CompatibleCount != 0 || report.IncompatibleCount != 0 || f.calls.Load() != 0 {
					t.Fatal(report)
				}
				f.mode.Store(0)
				requirePoolSuccess(t, f.adapter.Execute(context.Background(), f.actor, f.tool, core.Operation{Arguments: json.RawMessage(`{}`)}))
				if f.calls.Load() != 1 {
					t.Fatal("recovery replayed an earlier failed intent")
				}
			})
		}
	}
}

func TestPostDispatchNotificationDoesNotRewriteConfirmedOutcome(t *testing.T) {
	f := newCatalogFixture(t)
	f.mode.Store(3)
	f.tool.Risk = core.RiskWrite
	requirePoolSuccess(t, f.adapter.Execute(context.Background(), f.actor, f.tool, core.Operation{Arguments: json.RawMessage(`{}`)}))
	if f.calls.Load() != 1 || f.adapter.SessionPoolStats().Retained != 0 {
		t.Fatal("notified session retained or business call replayed")
	}
	f.mode.Store(0)
	requirePoolSuccess(t, f.adapter.Execute(context.Background(), f.actor, f.tool, core.Operation{Arguments: json.RawMessage(`{}`)}))
	if f.calls.Load() != 2 {
		t.Fatal("independent operation did not call once")
	}
}

func TestSDKPreservesPreDispatchInvalidationError(t *testing.T) {
	f := newCatalogFixture(t)
	session, transport, release, err := f.adapter.executionSession(context.Background(), f.actor, f.server)
	if err != nil {
		t.Fatal(err)
	}
	defer release(false)
	if _, err := discover(context.Background(), session, transport); err != nil {
		t.Fatal(err)
	}
	transport.catalogChanged()
	_, err = session.CallTool(context.Background(), &mcp.CallToolParams{Name: "read", Arguments: json.RawMessage(`{}`)})
	if !errors.Is(err, errCatalogChanged) || f.calls.Load() != 0 || transport.callSent {
		t.Fatalf("pre-dispatch error lost or call sent: %v calls=%d", err, f.calls.Load())
	}
}

func TestNotificationObservationDoesNotParseJSONToolContent(t *testing.T) {
	var count int
	capture := &responseCapture{id: []byte("1"), mediaType: "application/json"}
	body := &boundedBody{body: io.NopCloser(strings.NewReader(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"notifications/tools/list_changed"}]}}`)), capture: capture, remaining: maxResponseBytes, close: func() { count++ }}
	if _, err := io.ReadAll(body); err != nil {
		t.Fatal(err)
	}
	if _, err := capture.result(); err != nil {
		t.Fatal(err)
	}
	if capture.notifications != nil || count != 0 {
		t.Fatal("JSON tool text treated as protocol notification")
	}
	body.Close()
}
