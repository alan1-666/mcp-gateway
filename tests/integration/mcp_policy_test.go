package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alan1-666/mcp-gateway/internal/adapters/httpadapter"
	"github.com/alan1-666/mcp-gateway/internal/adapters/mcpadapter"
	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/alan1-666/mcp-gateway/internal/execution"
	"github.com/alan1-666/mcp-gateway/internal/identity"
	"github.com/alan1-666/mcp-gateway/internal/store/postgres"
	"github.com/alan1-666/mcp-gateway/internal/transport/httpapi"
	"github.com/alan1-666/mcp-gateway/internal/transport/mcpserver"
	"github.com/alan1-666/mcp-gateway/internal/upstreams"
	"github.com/alan1-666/mcp-gateway/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPResponsePolicyPreviewVersionsAndPinnedExecution(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required for PostgreSQL policy integration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	base, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	schema := "mcp_policy_" + strings.ReplaceAll(core.NewID(), "-", "")
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = base.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	defer base.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE")
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err = migrations.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}

	validStructured := json.RawMessage(`{"rows":[{"id":9007199254740993123,"customer":{"name":"Third","email":"private-three@example.test"},"private":"POLICY_V1_DETAILS_A"},{"id":1,"customer":{"name":"First","email":"private-one@example.test"},"private":"POLICY_V1_DETAILS_B"}],"next_cursor":"cursor-next","raw_required":"validated-before-projection"}`)
	invalidStructured := json.RawMessage(strings.Replace(string(validStructured), `,"private":"POLICY_V1_DETAILS_A"`, "", 1))
	originalText := strings.Repeat("RAW_TEXT_BYPASS ", 100)
	var calls, upstreamRequests atomic.Int32
	var invalidOutput atomic.Bool
	sdk := mcp.NewServer(&mcp.Implementation{Name: "policy-upstream", Version: "1"}, nil)
	sdk.AddTool(&mcp.Tool{
		Name: "orders", Description: "Return ordered customer records", InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
		OutputSchema: json.RawMessage(`{"type":"object","required":["rows","next_cursor","raw_required"],"properties":{"rows":{"type":"array","items":{"type":"object","required":["id","customer","private"],"properties":{"id":{"type":"integer"},"customer":{"type":"object","required":["name","email"],"properties":{"name":{"type":"string"},"email":{"type":"string"}}},"private":{"type":"string"}}}},"next_cursor":{"type":"string"},"raw_required":{"type":"string"}}}`),
	}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		calls.Add(1)
		structured := validStructured
		if invalidOutput.Load() {
			structured = invalidStructured
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: originalText, Meta: mcp.Meta{"hidden": "WIRE_META_BYPASS"}}}, StructuredContent: structured, Meta: mcp.Meta{"hidden": "WIRE_META_BYPASS"}}, nil
	})
	remoteHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return sdk }, &mcp.StreamableHTTPOptions{JSONResponse: true, Stateless: true})
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { upstreamRequests.Add(1); remoteHandler.ServeHTTP(w, r) }))
	defer remote.Close()
	egress, err := httpadapter.New([]string{remote.URL}, []string{"127.0.0.0/8"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	store := upstreams.NewStore(pool)
	adapter := mcpadapter.New(egress, store)
	service := core.NewService(postgres.New(pool))
	executor := &execution.Executor{Service: service, Adapter: &execution.Router{HTTP: egress, MCP: adapter}}
	token := func(who string) string { return strings.Repeat("policy-"+who+"-", 24) }
	auth, err := identity.New([]identity.Token{
		{Token: token("admin"), Actor: core.Actor{ID: "admin", WorkspaceID: "one", Role: core.RoleAdmin}},
		{Token: token("operator"), Actor: core.Actor{ID: "operator", WorkspaceID: "one", Role: core.RoleOperator}},
		{Token: token("foreign"), Actor: core.Actor{ID: "foreign", WorkspaceID: "two", Role: core.RoleAdmin}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/api/", (&httpapi.API{Service: service, Executor: executor, Adapter: egress, Upstreams: upstreams.New(store, adapter)}).Handler(auth))
	mux.Handle("/mcp", mcpserver.Handler(service, executor, auth))
	gateway := httptest.NewServer(mux)
	defer gateway.Close()
	decode := func(raw []byte, out any) {
		t.Helper()
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("decode response: %v", err)
		}
	}
	request := func(who, path string, body any, want int) []byte {
		t.Helper()
		method := "GET"
		var reader io.Reader
		if body != nil {
			method = "POST"
			raw, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			reader = bytes.NewReader(raw)
		}
		req, err := http.NewRequestWithContext(ctx, method, gateway.URL+"/api/v1"+path, reader)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token(who))
		req.Header.Set("Content-Type", "application/json")
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		raw, err := io.ReadAll(io.LimitReader(response.Body, core.MaxResultBytes))
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != want {
			t.Fatalf("%s %s: status=%d want=%d body=%s", method, path, response.StatusCode, want, raw)
		}
		return raw
	}
	var server core.MCPServer
	decode(request("admin", "/mcp/servers", core.MCPServerInput{Name: "Orders", Namespace: "orders", URL: remote.URL, TimeoutMS: 3000}, 200), &server)
	var discovered upstreams.DiscoveryPage
	decode(request("admin", "/mcp/servers/"+server.ID+"/discover", map[string]any{}, 200), &discovered)
	if len(discovered.Items) != 1 {
		t.Fatal("expected one upstream tool")
	}
	v1Policy := &core.ResponsePolicy{Include: []string{"/rows"}, MaxBytes: 8192}
	v2Policy := &core.ResponsePolicy{Include: []string{"/rows/*/id", "/rows/*/customer/name"}, MaxBytes: 8192}
	var tool core.Tool
	decode(request("admin", "/mcp/servers/"+server.ID+"/import", upstreams.ImportInput{ToolName: "orders", SchemaHash: discovered.Items[0].SchemaHash, Risk: core.RiskWrite, ResponsePolicy: v1Policy}, 200), &tool)
	decode(request("admin", "/tools/"+tool.ID+"/publish", map[string]any{}, 200), &tool)
	if tool.Version != 1 || !tool.Enabled || tool.Status != "published" {
		t.Fatalf("unexpected initial tool: %+v", tool)
	}

	client := mcp.NewClient(&mcp.Implementation{Name: "policy-agent", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: gateway.URL + "/mcp", HTTPClient: &http.Client{Transport: bearerTransport{token("operator")}}, MaxRetries: -1, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	call := func(name string, args map[string]any) []byte {
		t.Helper()
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		if result.IsError || len(result.Content) != 1 {
			t.Fatalf("MCP %s failed: %+v", name, result)
		}
		text, ok := result.Content[0].(*mcp.TextContent)
		if !ok {
			t.Fatalf("MCP %s did not return text", name)
		}
		return []byte(text.Text)
	}
	prepare := func(key string, wantVersion int) core.Operation {
		t.Helper()
		var op core.Operation
		decode(call("prepare_action", map[string]any{"tool_id": tool.ID, "arguments": map[string]any{}, "idempotency_key": key}), &op)
		if op.State != core.StateWaitingApproval || op.ToolVersion != wantVersion {
			t.Fatalf("wrong prepared snapshot: %+v", op)
		}
		decode(request("admin", "/operations/"+op.ID+"/approve", map[string]any{}, 200), &op)
		if op.State != core.StateReady {
			t.Fatalf("approval failed: %+v", op)
		}
		return op
	}
	oldOp := prepare("policy-v1-approved", 1)
	if calls.Load() != 0 {
		t.Fatal("prepare/approval sent a business call")
	}

	makeSample := func(structured json.RawMessage) json.RawMessage {
		raw, err := json.Marshal(map[string]any{"isError": false, "content": []any{map[string]any{"type": "text", "text": originalText}}, "structuredContent": structured, "_meta": map[string]any{"private": "PREVIEW_ONLY_DO_NOT_STORE"}})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	sample := makeSample(validStructured)
	previewInput := core.ResponsePolicyPreviewInput{ExpectedVersion: 1, ResponsePolicy: v2Policy, Sample: sample}
	policyPath := "/tools/" + tool.ID + "/response-policy"
	beforeRequests := upstreamRequests.Load()
	var beforeAudits, beforeOperations int
	if err = pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM audit_events),(SELECT count(*) FROM operations)`).Scan(&beforeAudits, &beforeOperations); err != nil {
		t.Fatal(err)
	}
	request("operator", policyPath+"/preview", previewInput, 403)
	request("foreign", policyPath+"/preview", previewInput, 404)
	var preview core.ResponsePolicyPreview
	decode(request("admin", policyPath+"/preview", previewInput, 200), &preview)
	canonicalSample, err := core.DecodeResult(sample)
	if err != nil {
		t.Fatal(err)
	}
	// The reported baseline matches the supported live MCP envelope, so ignored
	// metadata cannot inflate the apparent saving.
	delete(canonicalSample.(map[string]any), "_meta")
	canonicalBytes, err := json.Marshal(canonicalSample)
	if err != nil {
		t.Fatal(err)
	}
	if preview.ToolVersion != 1 || preview.OriginalBytes != len(canonicalBytes) || preview.ProjectedBytes != len(preview.Result) || preview.ProjectedBytes >= preview.OriginalBytes {
		t.Fatalf("incorrect preview counters: version=%d original=%d want_original=%d projected=%d result_bytes=%d", preview.ToolVersion, preview.OriginalBytes, len(canonicalBytes), preview.ProjectedBytes, len(preview.Result))
	}
	invalidPreview := previewInput
	invalidPreview.Sample = makeSample(invalidStructured)
	// The missing field would be discarded by v2. Preview still validates it
	// against the original output schema before projection.
	request("admin", policyPath+"/preview", invalidPreview, 400)
	var afterAudits, afterOperations int
	if err = pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM audit_events),(SELECT count(*) FROM operations)`).Scan(&afterAudits, &afterOperations); err != nil {
		t.Fatal(err)
	}
	if upstreamRequests.Load() != beforeRequests || calls.Load() != 0 || beforeAudits != afterAudits || beforeOperations != afterOperations {
		t.Fatal("preview called upstream or changed durable records")
	}
	var unchanged core.Tool
	decode(request("admin", "/tools/"+tool.ID, nil, 200), &unchanged)
	if unchanged.Version != 1 || !reflect.DeepEqual(unchanged.ResponsePolicy, v1Policy) {
		t.Fatal("preview mutated live policy")
	}

	updateInput := core.ResponsePolicyUpdateInput{ExpectedVersion: 1, ResponsePolicy: v2Policy}
	request("operator", policyPath, updateInput, 403)
	request("foreign", policyPath, updateInput, 404)
	var updated core.Tool
	decode(request("admin", policyPath, updateInput, 200), &updated)
	if updated.Version != 2 || !updated.Enabled || updated.Status != "published" {
		t.Fatalf("save failed or altered visibility: %+v", updated)
	}
	request("admin", policyPath, updateInput, 409)
	request("admin", policyPath+"/preview", previewInput, 409)
	if upstreamRequests.Load() != beforeRequests || calls.Load() != 0 {
		t.Fatal("policy update contacted upstream")
	}
	var contract struct {
		Version           int                  `json:"version"`
		ResponsePolicy    *core.ResponsePolicy `json:"response_policy"`
		OutputSchemaScope string               `json:"output_schema_scope"`
	}
	decode(call("get_tool_schema", map[string]any{"tool_id": tool.ID}), &contract)
	wantPolicy, err := core.NormalizeResponsePolicy(v2Policy)
	if err != nil {
		t.Fatal(err)
	}
	if contract.Version != 2 || !reflect.DeepEqual(contract.ResponsePolicy, wantPolicy) || contract.OutputSchemaScope != "structuredContent_before_projection" {
		t.Fatalf("stale MCP contract: %+v", contract)
	}

	decode(call("invoke_tool", map[string]any{"operation_id": oldOp.ID}), &oldOp)
	if oldOp.State != core.StateSucceeded || oldOp.ToolVersion != 1 || calls.Load() != 1 {
		t.Fatalf("old approved snapshot failed: %+v", oldOp)
	}
	if !bytes.Contains(oldOp.Result, []byte("POLICY_V1_DETAILS_A")) || !bytes.Contains(oldOp.Result, []byte("private-three@example.test")) || bytes.Contains(oldOp.Result, []byte("RAW_TEXT_BYPASS")) {
		t.Fatalf("old operation did not retain v1 policy: %s", oldOp.Result)
	}
	newOp := prepare("policy-v2-approved", 2)
	decode(call("invoke_tool", map[string]any{"operation_id": newOp.ID}), &newOp)
	if newOp.State != core.StateSucceeded || newOp.ToolVersion != 2 || calls.Load() != 2 {
		t.Fatalf("new policy execution failed: %+v", newOp)
	}
	value, err := core.DecodeResult(newOp.Result)
	if err != nil {
		t.Fatal(err)
	}
	envelope := value.(map[string]any)
	expected, err := core.DecodeResult(json.RawMessage(`{"rows":[{"id":9007199254740993123,"customer":{"name":"Third"}},{"id":1,"customer":{"name":"First"}}],"next_cursor":"cursor-next"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(envelope["structuredContent"], expected) {
		t.Fatalf("array order, precision, fields or cursor changed: %s", newOp.Result)
	}
	text := envelope["content"].([]any)[0].(map[string]any)["text"].(string)
	textValue, err := core.DecodeResult(json.RawMessage(text))
	if err != nil || !reflect.DeepEqual(textValue, expected) {
		t.Fatal("text bypassed projection")
	}
	for _, marker := range []string{"POLICY_V1_DETAILS", "private-three", "private-one", "RAW_TEXT_BYPASS", "WIRE_META_BYPASS", "PREVIEW_ONLY_DO_NOT_STORE", "validated-before-projection"} {
		if bytes.Contains(newOp.Result, []byte(marker)) {
			t.Fatalf("unselected field leaked: %s", marker)
		}
	}
	previewValue, err := core.DecodeResult(preview.Result)
	if err != nil || !reflect.DeepEqual(previewValue, value) {
		t.Fatal("preview differs from actual execution of same schema and policy")
	}
	call("invoke_tool", map[string]any{"operation_id": oldOp.ID})
	call("invoke_tool", map[string]any{"operation_id": newOp.ID})
	if calls.Load() != 2 {
		t.Fatal("replayed operation sent another upstream request")
	}
	var storedResult, oldSnapshot, newSnapshot json.RawMessage
	if err = pool.QueryRow(ctx, `SELECT result,tool_snapshot FROM operations WHERE workspace_id='one' AND id=$1`, newOp.ID).Scan(&storedResult, &newSnapshot); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT tool_snapshot FROM operations WHERE workspace_id='one' AND id=$1`, oldOp.ID).Scan(&oldSnapshot); err != nil {
		t.Fatal(err)
	}
	storedValue, err := core.DecodeResult(storedResult)
	if err != nil || !reflect.DeepEqual(storedValue, value) {
		t.Fatal("database persisted an unprojected result")
	}
	var oldTool, newTool core.Tool
	decode(oldSnapshot, &oldTool)
	decode(newSnapshot, &newTool)
	if oldTool.Version != 1 || !reflect.DeepEqual(oldTool.ResponsePolicy, v1Policy) || newTool.Version != 2 || !reflect.DeepEqual(newTool.ResponsePolicy, wantPolicy) {
		t.Fatal("operation snapshots changed with current policy")
	}

	// Now return a payload missing the required, unselected private field. A
	// projection-first implementation would incorrectly accept this response.
	invalidOutput.Store(true)
	badOp := prepare("policy-invalid-output", 2)
	decode(call("invoke_tool", map[string]any{"operation_id": badOp.ID}), &badOp)
	if badOp.State != core.StateUnknown || len(badOp.Result) != 0 || calls.Load() != 3 || !strings.Contains(badOp.Error, "output contract") {
		t.Fatalf("raw output schema was bypassed: %+v", badOp)
	}
	call("invoke_tool", map[string]any{"operation_id": badOp.ID})
	if calls.Load() != 3 {
		t.Fatal("uncertain write was resent")
	}
}
