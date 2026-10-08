package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alan1-666/mcp-gateway/internal/adapters/httpadapter"
	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/alan1-666/mcp-gateway/internal/execution"
	"github.com/alan1-666/mcp-gateway/internal/identity"
	"github.com/alan1-666/mcp-gateway/internal/store/postgres"
	"github.com/alan1-666/mcp-gateway/internal/transport/httpapi"
	"github.com/alan1-666/mcp-gateway/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Proves that the console's actual parser produces contracts understood by Go,
// and importing cannot bypass draft publication, schema validation or approval.
func TestOpenAPIImportToApprovedHTTPExecution(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL and Node dependencies required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	base, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	schema := "openapi_" + strings.ReplaceAll(core.NewID(), "-", "")
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
	var calls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Idempotency-Key") == "" {
			t.Error("missing dispatch identity")
		}
		switch r.URL.Path {
		case "/v1/search":
			if r.Method != "GET" || r.URL.Query().Get("q") != "a & 中文" || r.URL.Query().Get("limit") != "2" {
				t.Errorf("query mapping changed: %s %s", r.Method, r.URL.String())
			}
		case "/v1/jobs":
			body, _ := io.ReadAll(r.Body)
			var args map[string]json.RawMessage
			if json.Unmarshal(body, &args) != nil || string(args["job"]) != `"job-1"` || string(args["score"]) != "1" || string(args["note"]) != "null" || r.Method != "POST" || r.URL.RawQuery != "" {
				t.Errorf("body mapping changed: %s %s", r.Method, body)
			}
		default:
			t.Errorf("server base path lost: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer target.Close()
	spec := `{"openapi":"3.0.3","info":{"title":"Import integration","version":"1"},"servers":[{"url":"` + target.URL + `/v1"}],"paths":{
 "/search":{"get":{"operationId":"search_records","summary":"Search records","parameters":[{"name":"q","in":"query","required":true,"schema":{"type":"string"}},{"name":"limit","in":"query","schema":{"type":"integer","minimum":1,"maximum":20}}],"responses":{"200":{"description":"JSON result","content":{"application/json":{"schema":{"type":"object"}}}}}}},
 "/jobs":{"post":{"operationId":"create_job","summary":"Create a job","requestBody":{"required":true,"content":{"application/json":{"schema":{"type":"object","properties":{"job":{"type":"string"},"score":{"type":"number","minimum":0,"exclusiveMinimum":true},"note":{"type":"string","nullable":true}},"required":["job","score","note"],"additionalProperties":false}}}},"responses":{"200":{"description":"JSON result","content":{"application/json":{"schema":{"type":"object"}}}}}}}
 }}`
	command := exec.CommandContext(ctx, "node", "--import", "tsx", "apps/console/test/fixtures/openapi-preview.ts")
	command.Dir = "../.."
	command.Stdin = strings.NewReader(spec)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	payload, err := command.Output()
	if err != nil {
		t.Fatalf("production OpenAPI parser: %v %s", err, stderr.String())
	}
	var imports []core.ToolInput
	if err = json.Unmarshal(payload, &imports); err != nil || len(imports) != 2 {
		t.Fatalf("preview payload: %s %v", payload, err)
	}
	workspace := core.NewID()
	adminToken, operatorToken := strings.Repeat("a", 40), strings.Repeat("o", 40)
	auth, err := identity.New([]identity.Token{
		{Token: adminToken, Actor: core.Actor{ID: "admin", WorkspaceID: workspace, Role: core.RoleAdmin}},
		{Token: operatorToken, Actor: core.Actor{ID: "operator", WorkspaceID: workspace, Role: core.RoleOperator}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := httpadapter.New([]string{target.URL}, []string{"127.0.0.0/8"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	service := core.NewService(postgres.New(pool))
	executor := &execution.Executor{Service: service, Adapter: adapter}
	api := httptest.NewServer((&httpapi.API{Service: service, Executor: executor, Adapter: adapter}).Handler(auth))
	defer api.Close()
	call := func(path, token string, input any, want int) json.RawMessage {
		t.Helper()
		body, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		request, err := http.NewRequestWithContext(ctx, "POST", api.URL+"/api/v1"+path, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Content-Type", "application/json")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, _ := io.ReadAll(response.Body)
		if response.StatusCode != want {
			t.Fatalf("%s: want %d got %d: %s", path, want, response.StatusCode, data)
		}
		return data
	}
	for _, input := range imports {
		if input.Risk != core.RiskWrite {
			t.Fatal("import silently bypasses risk review")
		}
		call("/tools", operatorToken, input, 403)
		var tool core.Tool
		if err = json.Unmarshal(call("/tools", adminToken, input, 200), &tool); err != nil {
			t.Fatal(err)
		}
		if tool.Status != "draft" {
			t.Fatalf("import auto-published: %s", tool.Status)
		}
		arguments := json.RawMessage(`{"job":"job-1","score":1,"note":null}`)
		if input.HTTP.Method == "GET" {
			arguments = json.RawMessage(`{"q":"a & 中文","limit":2}`)
		}
		prepare := core.PrepareInput{ToolID: tool.ID, Arguments: arguments, IdempotencyKey: "import-" + tool.ID}
		call("/operations", operatorToken, prepare, 404)
		call("/tools/"+tool.ID+"/publish", adminToken, struct{}{}, 200)
		bad := prepare
		bad.Arguments = json.RawMessage(`{}`)
		call("/operations", operatorToken, bad, 400)
		if input.HTTP.Method == "POST" {
			bad.Arguments = json.RawMessage(`{"job":"job-1","score":0,"note":null}`)
			call("/operations", operatorToken, bad, 400)
		}
		var operation core.Operation
		if err = json.Unmarshal(call("/operations", operatorToken, prepare, 200), &operation); err != nil {
			t.Fatal(err)
		}
		if operation.State != core.StateWaitingApproval {
			t.Fatalf("import bypassed approval: %s", operation.State)
		}
		call("/operations/"+operation.ID+"/execute", operatorToken, struct{}{}, 409)
		call("/operations/"+operation.ID+"/approve", adminToken, struct{}{}, 200)
		var result core.Operation
		if err = json.Unmarshal(call("/operations/"+operation.ID+"/execute", operatorToken, struct{}{}, 200), &result); err != nil {
			t.Fatal(err)
		}
		if result.State != core.StateSucceeded || !strings.Contains(string(result.Result), "ok") {
			t.Fatalf("imported operation failed: %+v", result)
		}
		call("/operations/"+operation.ID+"/execute", operatorToken, struct{}{}, 200)
	}
	if calls.Load() != 2 {
		t.Fatalf("expected one dispatch per import, got %d", calls.Load())
	}
}
