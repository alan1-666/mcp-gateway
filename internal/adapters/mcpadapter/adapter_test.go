package mcpadapter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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

var objectSchema = json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)

type servers map[string]core.MCPServer

func (s servers) GetServer(_ context.Context, workspace, id string) (core.MCPServer, error) {
	server, ok := s[id]
	if !ok || workspace != server.WorkspaceID {
		return core.MCPServer{}, core.ErrNotFound
	}
	return server, nil
}

func serverConfig(url, id, workspace string) core.MCPServer {
	return core.MCPServer{MCPServerInput: core.MCPServerInput{Name: id, Namespace: id, URL: url, TimeoutMS: 1500}, ID: id, WorkspaceID: workspace, Enabled: true}
}

func imported(server core.MCPServer, remote core.RemoteTool, risk core.Risk) core.Tool {
	return core.Tool{ID: "tool", WorkspaceID: server.WorkspaceID, Name: server.Namespace + "__" + remote.Name, InputSchema: remote.InputSchema, OutputSchema: remote.OutputSchema, Risk: risk, MCP: &core.MCPConfig{ServerID: server.ID, ToolName: remote.Name, SchemaHash: remote.SchemaHash}}
}

func sdkServer(jsonResponse bool, pageSize int, auth string, tools ...*mcp.Tool) (*mcp.Server, *httptest.Server, *atomic.Int32) {
	sdk := mcp.NewServer(&mcp.Implementation{Name: "test-upstream", Version: "1"}, &mcp.ServerOptions{PageSize: pageSize})
	count := &atomic.Int32{}
	for _, tool := range tools {
		sdk.AddTool(tool, func(_ context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			count.Add(1)
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "done", Meta: mcp.Meta{"secret": "do-not-persist"}}}, StructuredContent: json.RawMessage(`{"id":9007199254740993,"label":"ok"}`), Meta: mcp.Meta{"secret": "do-not-persist"}}, nil
		})
	}
	// Exercise modern sessionless JSON and legacy stateful SSE servers. Legacy
	// servers do not implement server/discover; the SDK falls back to initialize.
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return sdk }, &mcp.StreamableHTTPOptions{JSONResponse: jsonResponse, Stateless: jsonResponse, SessionTimeout: time.Second})
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != auth {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if !jsonResponse && r.Body != nil {
			data, _ := io.ReadAll(r.Body)
			r.Body.Close()
			r.Body = io.NopCloser(bytes.NewReader(data))
			var request struct {
				Method string `json:"method"`
			}
			_ = json.Unmarshal(data, &request)
			if request.Method == "server/discover" {
				http.Error(w, "unsupported method", 404)
				return
			}
		}
		handler.ServeHTTP(w, r)
	}))
	return sdk, httpServer, count
}

func basicTool(name string) *mcp.Tool {
	return &mcp.Tool{Name: name, Description: "Read remote data", InputSchema: objectSchema, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}
}

func TestTwoRealMCPServersDiscoveryExecutionAndCredentialIsolation(t *testing.T) {
	firstSDK, first, firstCalls := sdkServer(true, 1, "Bearer alpha", basicTool("same"), basicTool("second"))
	defer first.Close()
	secondSDK, second, secondCalls := sdkServer(false, 1, "Bearer beta", basicTool("same"))
	defer second.Close()
	aConfig, bConfig := serverConfig(first.URL, "alpha", "one"), serverConfig(second.URL, "beta", "two")
	aConfig.CredentialRef, bConfig.CredentialRef = "AUTH", "AUTH"
	egress, err := httpadapter.New([]string{first.URL, second.URL}, []string{"127.0.0.0/8"}, []httpadapter.Credential{
		{WorkspaceID: "one", Ref: "AUTH", Origin: first.URL, Headers: map[string]string{"Authorization": "Bearer alpha"}},
		{WorkspaceID: "two", Ref: "AUTH", Origin: second.URL, Headers: map[string]string{"Authorization": "Bearer beta"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	adapter := New(egress, servers{"alpha": aConfig, "beta": bConfig})
	for _, tc := range []struct {
		server core.MCPServer
		count  int
	}{{aConfig, 2}, {bConfig, 1}} {
		actor := core.Actor{WorkspaceID: tc.server.WorkspaceID}
		remote, err := adapter.Discover(context.Background(), actor, tc.server)
		if err != nil {
			t.Fatal(err)
		}
		if len(remote) != tc.count || !remote[0].ReadOnlyHint {
			t.Fatalf("unexpected discovery: %+v", remote)
		}
		tool := imported(tc.server, remote[0], core.RiskRead)
		// JSONB formatting must not break a reviewed schema hash.
		tool.InputSchema = json.RawMessage(`{ "additionalProperties":false, "properties":{}, "type":"object" }`)
		out := adapter.Execute(context.Background(), actor, tool, core.Operation{Arguments: json.RawMessage(`{}`)})
		if out.State != core.StateSucceeded {
			t.Fatalf("execution failed: %+v", out)
		}
		if strings.Contains(string(out.Result), "_meta") || strings.Contains(string(out.Result), "do-not-persist") || !bytes.Contains(out.Result, []byte(`9007199254740993`)) {
			t.Fatalf("wire result lost precision or metadata leaked: %s", out.Result)
		}
	}
	if firstCalls.Load() != 1 || secondCalls.Load() != 1 {
		t.Fatal("calls routed to wrong servers")
	}
	// A credential reference cannot cross either workspace or origin.
	wrong := bConfig
	wrong.WorkspaceID = "one"
	if _, err := adapter.Discover(context.Background(), core.Actor{WorkspaceID: "one"}, wrong); err == nil {
		t.Fatal("cross-origin credential accepted")
	}
	if _, err := adapter.Discover(context.Background(), core.Actor{WorkspaceID: "two"}, aConfig); err == nil {
		t.Fatal("cross-workspace server accepted")
	}
	if firstCalls.Load() != 1 || secondCalls.Load() != 1 {
		t.Fatal("denied requests reached tools")
	}
	for _, sdk := range []*mcp.Server{firstSDK, secondSDK} {
		deadline := time.Now().Add(500 * time.Millisecond)
		for {
			count := 0
			for range sdk.Sessions() {
				count++
			}
			if count == 0 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("adapter left a remote session open")
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
}

func TestContractChangesAndDisabledServersBlockCalls(t *testing.T) {
	sdk, host, calls := sdkServer(true, 10, "", basicTool("read"))
	defer host.Close()
	server := serverConfig(host.URL, "server", "w")
	egress, _ := httpadapter.New([]string{host.URL}, []string{"127.0.0.0/8"}, nil)
	registry := servers{server.ID: server}
	adapter := New(egress, registry)
	actor := core.Actor{WorkspaceID: "w"}
	remote, err := adapter.Discover(context.Background(), actor, server)
	if err != nil {
		t.Fatal(err)
	}
	tool := imported(server, remote[0], core.RiskWrite)
	changed := basicTool("read")
	changed.InputSchema = json.RawMessage(`{"type":"object","properties":{"new":{"type":"string"}}}`)
	sdk.AddTool(changed, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		calls.Add(1)
		return &mcp.CallToolResult{}, nil
	})
	out := adapter.Execute(context.Background(), actor, tool, core.Operation{Arguments: json.RawMessage(`{}`)})
	if out.State != core.StateFailed || calls.Load() != 0 || !strings.Contains(out.Error, "schema changed") {
		t.Fatalf("changed schema was sent: %+v", out)
	}
	server.Enabled = false
	registry[server.ID] = server
	out = adapter.Execute(context.Background(), actor, tool, core.Operation{Arguments: json.RawMessage(`{}`)})
	if out.State != core.StateFailed || calls.Load() != 0 {
		t.Fatal("disabled server called")
	}
}

func TestRedirectDNSAndProtocolCredentialPolicy(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
	defer source.Close()
	for _, tc := range []struct {
		name           string
		origins, cidrs []string
		credentials    []httpadapter.Credential
		config         core.MCPServer
	}{
		{"redirect", []string{source.URL, target.URL}, []string{"127.0.0.0/8"}, nil, serverConfig(source.URL, "s", "w")},
		{"loopback-without-cidr", []string{target.URL}, nil, nil, serverConfig(target.URL, "s", "w")},
		{"DNS-loopback-without-cidr", []string{strings.Replace(target.URL, "127.0.0.1", "localhost", 1)}, nil, nil, serverConfig(strings.Replace(target.URL, "127.0.0.1", "localhost", 1), "s", "w")},
		{"unapproved-origin", nil, []string{"127.0.0.0/8"}, nil, serverConfig(target.URL, "s", "w")},
		{"metadata-always-denied", []string{"http://169.254.169.254"}, []string{"169.254.0.0/16"}, nil, serverConfig("http://169.254.169.254", "s", "w")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			egress, _ := httpadapter.New(tc.origins, tc.cidrs, tc.credentials)
			a := New(egress, nil)
			if _, err := a.Discover(context.Background(), core.Actor{WorkspaceID: "w"}, tc.config); err == nil {
				t.Fatal("unsafe destination accepted")
			}
		})
	}
	if hits.Load() != 0 {
		t.Fatal("redirect or DNS policy allowed destination")
	}
	egress, _ := httpadapter.New([]string{target.URL}, []string{"127.0.0.0/8"}, []httpadapter.Credential{{WorkspaceID: "w", Ref: "AUTH", Origin: target.URL, Headers: map[string]string{"Mcp-Session-Id": "injected"}}})
	config := serverConfig(target.URL, "s", "w")
	config.CredentialRef = "AUTH"
	if err := New(egress, nil).ValidateServer(core.Actor{WorkspaceID: "w"}, config); err == nil {
		t.Fatal("credential overwrote MCP session")
	}
}

func TestErrorsTimeoutResponseLossAndSession404NeverRepeatWrites(t *testing.T) {
	for _, mode := range []string{"is-error", "timeout", "response-loss", "session-404", "unsupported-content", "oversized", "bad-output"} {
		t.Run(mode, func(t *testing.T) {
			var effects, attempts atomic.Int32
			sdk := mcp.NewServer(&mcp.Implementation{Name: "failure", Version: "1"}, nil)
			tool := basicTool("write")
			if mode == "bad-output" {
				tool.OutputSchema = json.RawMessage(`{"type":"object","required":["ok"],"properties":{"ok":{"type":"boolean"}}}`)
			}
			sdk.AddTool(tool, func(ctx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				effects.Add(1)
				switch mode {
				case "is-error":
					return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "upstream-secret"}}}, nil
				case "timeout":
					<-ctx.Done()
					return nil, ctx.Err()
				case "unsupported-content":
					return &mcp.CallToolResult{Content: []mcp.Content{&mcp.ImageContent{MIMEType: "image/png", Data: []byte("image")}}}, nil
				case "oversized":
					return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: strings.Repeat("x", maxResponseBytes+1)}}}, nil
				case "bad-output":
					return &mcp.CallToolResult{StructuredContent: map[string]any{"ok": "not a bool"}}, nil
				}
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "done"}}}, nil
			})
			handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return sdk }, &mcp.StreamableHTTPOptions{JSONResponse: true, SessionTimeout: time.Second})
			host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var message struct {
					Method string `json:"method"`
				}
				if r.Body != nil {
					data, _ := io.ReadAll(r.Body)
					r.Body.Close()
					r.Body = io.NopCloser(bytes.NewReader(data))
					_ = json.Unmarshal(data, &message)
				}
				if message.Method == "tools/call" {
					attempts.Add(1)
					if mode == "session-404" {
						http.Error(w, "session absent", 404)
						return
					}
					if mode == "response-loss" {
						handler.ServeHTTP(httptest.NewRecorder(), r)
						conn, _, _ := w.(http.Hijacker).Hijack()
						conn.Close()
						return
					}
				}
				handler.ServeHTTP(w, r)
			}))
			defer host.Close()
			server := serverConfig(host.URL, "s", "w")
			if mode == "timeout" {
				server.TimeoutMS = 200
			}
			egress, _ := httpadapter.New([]string{host.URL}, []string{"127.0.0.0/8"}, nil)
			a := New(egress, servers{"s": server})
			actor := core.Actor{WorkspaceID: "w"}
			remote, err := a.Discover(context.Background(), actor, server)
			if err != nil {
				t.Fatal(err)
			}
			started := time.Now()
			out := a.Execute(context.Background(), actor, imported(server, remote[0], core.RiskWrite), core.Operation{Arguments: json.RawMessage(`{}`)})
			if out.State != core.StateUnknown || attempts.Load() != 1 || effects.Load() > 1 {
				t.Fatalf("unsafe write certainty/retry: %+v attempts=%d effects=%d", out, attempts.Load(), effects.Load())
			}
			if strings.Contains(out.Error, "upstream-secret") || len(out.Result) > 0 {
				t.Fatal("upstream error content leaked")
			}
			if time.Since(started) > 2*time.Second {
				t.Fatal("deadline did not bound session lifetime")
			}
			readOut := a.Execute(context.Background(), actor, imported(server, remote[0], core.RiskRead), core.Operation{Arguments: json.RawMessage(`{}`)})
			if readOut.State != core.StateFailed || attempts.Load() != 2 || effects.Load() > 2 {
				t.Fatalf("read error classification or request count: %+v", readOut)
			}
		})
	}
}

func TestDiscoveryRejectsDuplicateCursorToolsAndLimits(t *testing.T) {
	for _, mode := range []string{"cursor", "duplicate", "schema", "count", "pages", "bytes"} {
		t.Run(mode, func(t *testing.T) {
			sdk := mcp.NewServer(&mcp.Implementation{Name: "catalog", Version: "1"}, nil)
			var pages atomic.Int32
			sdk.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
				return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
					if method != "tools/list" {
						return next(ctx, method, req)
					}
					out := &mcp.ListToolsResult{Tools: []*mcp.Tool{basicTool("test")}}
					page := pages.Add(1)
					switch mode {
					case "cursor":
						out.Tools = []*mcp.Tool{}
						out.NextCursor = "same"
					case "duplicate":
						out.Tools = append(out.Tools, basicTool("test"))
					case "schema":
						out.Tools[0].InputSchema = json.RawMessage(`{"type":"object","properties":{"x":{"$ref":"https://example.invalid/schema"}}}`)
					case "count":
						for i := 0; i < maxTools; i++ {
							out.Tools = append(out.Tools, basicTool(fmt.Sprintf("test%d", i)))
						}
					case "pages":
						out.Tools = []*mcp.Tool{}
						out.NextCursor = fmt.Sprint(page)
					case "bytes":
						out.Tools = []*mcp.Tool{}
						for i := 0; i < 50; i++ {
							tool := basicTool(fmt.Sprintf("page%dtool%d", page, i))
							tool.Description = strings.Repeat("d", 3900)
							tool.InputSchema = map[string]any{"type": "object", "description": strings.Repeat("x", 8000)}
							out.Tools = append(out.Tools, tool)
						}
						out.NextCursor = fmt.Sprint(page)
					}
					return out, nil
				}
			})
			handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return sdk }, &mcp.StreamableHTTPOptions{JSONResponse: true})
			host := httptest.NewServer(handler)
			defer host.Close()
			egress, _ := httpadapter.New([]string{host.URL}, []string{"127.0.0.0/8"}, nil)
			a := New(egress, nil)
			config := serverConfig(host.URL, "s", "w")
			// These cases exercise catalog bounds, not elapsed-time bounds. The
			// race detector on shared CI hosts needs time to validate several MiB
			// of schemas before the aggregate limit is reached. Timeout behavior
			// has its own dedicated test above.
			config.TimeoutMS = 30000
			_, err := a.Discover(context.Background(), core.Actor{WorkspaceID: "w"}, config)
			if err == nil {
				t.Fatal("unsafe catalog accepted")
			}
			if mode == "cursor" && pages.Load() != 2 {
				t.Fatalf("repeated cursor was not detected on second page: %d", pages.Load())
			}
			if mode == "pages" && pages.Load() != maxPages {
				t.Fatalf("pagination bound: %d", pages.Load())
			}
			if mode == "bytes" && (pages.Load() != 7 || err.Error() != "MCP tool catalog exceeds the size limit") {
				t.Fatalf("aggregate byte bound: pages=%d err=%v", pages.Load(), err)
			}
		})
	}
}

func TestStructuredOutputSchemaPreservesLargeInteger(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","required":["id"],"properties":{"id":{"type":"integer","const":9007199254740993}}}`)
	result, err := sanitizeResult(json.RawMessage(`{"content":[],"structuredContent":{"id":9007199254740993},"_meta":{"secret":"omit"}}`), schema)
	if err != nil || !bytes.Contains(result, []byte("9007199254740993")) {
		t.Fatalf("numeric precision was lost: %s %v", result, err)
	}
	if _, err := sanitizeResult(json.RawMessage(`{"content":[]}`), schema); err == nil {
		t.Fatal("required structured content accepted missing")
	}
}

func TestNestedMCPContextDoesNotLeakProtocolIntoLegacyUpstream(t *testing.T) {
	_, upstream, calls := sdkServer(false, 1, "", basicTool("read"))
	defer upstream.Close()
	config := serverConfig(upstream.URL, "legacy", "w")
	egress, _ := httpadapter.New([]string{upstream.URL}, []string{"127.0.0.0/8"}, nil)
	adapter := New(egress, servers{config.ID: config})
	actor := core.Actor{WorkspaceID: "w"}
	remote, err := adapter.Discover(context.Background(), actor, config)
	if err != nil {
		t.Fatal(err)
	}
	tool := imported(config, remote[0], core.RiskRead)
	// Incoming requests negotiate the current protocol, whereas the upstream
	// only speaks legacy MCP. Test both discovery and execution on that context.
	gatewaySDK := mcp.NewServer(&mcp.Implementation{Name: "gateway", Version: "1"}, nil)
	gatewaySDK.AddTool(basicTool("invoke"), func(ctx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		if _, err := adapter.Discover(ctx, actor, config); err != nil {
			return nil, err
		}
		out := adapter.Execute(ctx, actor, tool, core.Operation{Arguments: json.RawMessage(`{}`)})
		if out.State != core.StateSucceeded {
			return nil, fmt.Errorf("nested execution failed: %s", out.Error)
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(out.Result)}}}, nil
	})
	gateway := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return gatewaySDK }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true}))
	defer gateway.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "external-agent", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: gateway.URL, MaxRetries: -1, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "invoke", Arguments: map[string]any{}})
	if err != nil || result.IsError || calls.Load() != 1 {
		t.Fatalf("nested protocols failed: result=%+v error=%v calls=%d", result, err, calls.Load())
	}
}

func TestOutboundContextKeepsCancellationAndDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.WithValue(context.Background(), struct{}{}, "incoming"), time.Second)
	out := outboundContext{ctx}
	if out.Value(struct{}{}) != nil {
		t.Fatal("inbound values leaked")
	}
	want, _ := ctx.Deadline()
	got, ok := out.Deadline()
	if !ok || !got.Equal(want) {
		t.Fatal("deadline was lost")
	}
	cancel()
	if out.Err() != context.Canceled {
		t.Fatal("cancellation was lost")
	}
}
