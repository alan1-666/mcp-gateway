package platform

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alan1-666/mcp-gateway/internal/adapters/httpadapter"
	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/alan1-666/mcp-gateway/internal/execution"
	"github.com/alan1-666/mcp-gateway/internal/identity"
	"github.com/alan1-666/mcp-gateway/internal/runs"
	"github.com/alan1-666/mcp-gateway/internal/store/postgres"
	"github.com/alan1-666/mcp-gateway/internal/transport/httpapi"
	"github.com/alan1-666/mcp-gateway/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// This boundary test uses the production Node worker/client and Go HTTP/SQL
// implementation. Only model inference and the business HTTP service are fakes.
func TestCloudWorkerIntegration(t *testing.T) {
	if os.Getenv("RUN_CLOUD_WORKER_INTEGRATION") != "1" {
		t.Skip("set RUN_CLOUD_WORKER_INTEGRATION=1 after npm ci to include the Node worker")
	}
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Fatal("TEST_DATABASE_URL is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	base, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	schema := "worker_" + strings.ReplaceAll(core.NewID(), "-", "")
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = base.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	defer base.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE")
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	config.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err = migrations.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	service := core.NewService(postgres.New(pool))
	workspace := core.NewID()
	admin := core.Actor{ID: "owner", WorkspaceID: workspace, Role: core.RoleAdmin}
	tokens := map[string]string{}
	for _, role := range []string{"operator", "approver"} {
		token := core.NewID() + core.NewID()
		hash := sha256.Sum256([]byte(token))
		_, err = pool.Exec(ctx, `INSERT INTO gateway_users(id,workspace_id,username,password_hash,role) VALUES($1,$2,$1,$3,$1)`, role, workspace, []byte("unused-test-password"))
		if err != nil {
			t.Fatal(err)
		}
		_, err = pool.Exec(ctx, `INSERT INTO gateway_api_keys(id,user_id,token_hash,name,expires_at) VALUES($1,$1,$2,'test',clock_timestamp()+interval '1 hour')`, role, hash[:])
		if err != nil {
			t.Fatal(err)
		}
		tokens[role] = token
	}
	var writes atomic.Int32
	downstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			writes.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"healthy"}`))
	}))
	defer downstream.Close()
	adapter, err := httpadapter.New([]string{downstream.URL}, []string{"127.0.0.0/8"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	toolIDs := map[string]string{}
	for _, risk := range []core.Risk{core.RiskRead, core.RiskWrite} {
		method := "GET"
		if risk == core.RiskWrite {
			method = "POST"
		}
		tool, err := service.CreateTool(ctx, admin, core.ToolInput{Name: "integration_" + string(risk), Description: "Isolated worker boundary check", Risk: risk,
			InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`), HTTP: core.HTTPConfig{URL: downstream.URL, Method: method, TimeoutMS: 2000}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = service.PublishTool(ctx, admin, tool.ID); err != nil {
			t.Fatal(err)
		}
		toolIDs[string(risk)] = tool.ID
	}
	// Older relevant tools must remain discoverable beyond the old 500-row cap.
	definition, _ := json.Marshal(core.ToolInput{Name: "fixture", Description: strings.Repeat("<>&\x01", 1000), Risk: core.RiskRead,
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`), HTTP: core.HTTPConfig{URL: downstream.URL, Method: "GET", TimeoutMS: 2000}})
	if _, err = pool.Exec(ctx, `INSERT INTO tools(workspace_id,id,name,risk,status,enabled,definition)
		SELECT $1,md5($1 || n::text)::uuid::text,'filler_' || n::text,'read','published',true,$2::jsonb
		FROM generate_series(1,510) n`, workspace, definition); err != nil {
		t.Fatal(err)
	}
	auth, err := identity.NewCloud(ctx, pool, "https://integration.example", "")
	if err != nil {
		t.Fatal(err)
	}
	executor := &execution.Executor{Service: service, Adapter: adapter}
	secret := core.NewID() + core.NewID()
	runAPI, err := runs.New(pool, service, executor, runs.Config{WorkspaceID: workspace, SharedSecret: secret})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/api/v1/runs", runAPI.PublicHandler(auth))
	mux.Handle("/api/v1/runs/", runAPI.PublicHandler(auth))
	mux.Handle("/internal/runner/", runAPI.InternalHandler())
	mux.Handle("/api/", (&httpapi.API{Service: service, Executor: executor, Adapter: adapter}).Handler(auth))
	server := httptest.NewServer(requests(mux))
	defer server.Close()
	input, _ := json.Marshal(map[string]string{"baseURL": server.URL, "secret": secret, "token": tokens["operator"], "approverToken": tokens["approver"], "readToolID": toolIDs["read"], "writeToolID": toolIDs["write"], "stateDir": t.TempDir()})
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, "node", "--import", "tsx", "test/cloud-worker-integration.mjs")
	cmd.Dir = root
	cmd.Stdin = bytes.NewReader(input)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Node worker boundary test: %v\n%s", err, output)
	}
	t.Log(string(output))
	if writes.Load() != 1 {
		t.Fatalf("expected exactly one approved business write, got %d", writes.Load())
	}
}
