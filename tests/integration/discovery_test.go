package integration

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/alan1-666/mcp-gateway/internal/adapters/httpadapter"
	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/alan1-666/mcp-gateway/internal/execution"
	"github.com/alan1-666/mcp-gateway/internal/identity"
	"github.com/alan1-666/mcp-gateway/internal/store/postgres"
	"github.com/alan1-666/mcp-gateway/internal/transport/httpapi"
	"github.com/alan1-666/mcp-gateway/internal/transport/mcpserver"
	"github.com/alan1-666/mcp-gateway/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestDiscoveryAcrossRESTMCPAndLiveCloudIdentity(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required for PostgreSQL discovery integration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	base, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	schema := "discovery_" + strings.ReplaceAll(core.NewID(), "-", "")
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
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err = migrations.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}

	tokens := map[string]string{}
	for _, name := range []string{"admin", "operator", "foreign"} {
		role, workspace := name, "discovery-team"
		if name == "foreign" {
			role, workspace = "admin", "another-team"
		}
		token := core.NewID() + core.NewID()
		hash := sha256.Sum256([]byte(token))
		if _, err = pool.Exec(ctx, `INSERT INTO gateway_users(id,workspace_id,username,password_hash,role) VALUES($1,$2,$1,$3,$4)`, name, workspace, []byte("unused-test-password"), role); err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, `INSERT INTO gateway_api_keys(id,user_id,token_hash,name,expires_at) VALUES($1,$1,$2,'discovery test',clock_timestamp()+interval '1 hour')`, name, hash[:]); err != nil {
			t.Fatal(err)
		}
		tokens[name] = token
	}
	definition, _ := json.Marshal(core.ToolInput{Name: "fixture", Description: strings.Repeat("<>&\x01", 1000), Risk: core.RiskRead, InputSchema: json.RawMessage(`{"type":"object"}`), OutputSchema: json.RawMessage(`{"type":"object"}`), HTTP: core.HTTPConfig{URL: "https://private-service.invalid/check", Method: "GET", CredentialRef: "PRIVATE_REFERENCE", TimeoutMS: 1000}})
	if _, err = pool.Exec(ctx, `INSERT INTO tools(workspace_id,id,name,risk,status,enabled,definition,created_at)
  SELECT 'discovery-team',md5(n::text)::uuid::text,'catalog_' || lpad(n::text,4,'0'),'read','published',true,$1::jsonb,timestamptz '2025-01-01' + n * interval '1 second'
  FROM generate_series(1,520) n`, definition); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE tools SET definition=jsonb_set(definition,'{description}','"Ancient NEEDLE 中文 100%_literal"'),version=version+1 WHERE name='catalog_0001'`); err != nil {
		t.Fatal(err)
	}
	for _, record := range []struct {
		name, workspace, status string
		enabled                 bool
	}{{"hidden_draft", "discovery-team", "draft", false}, {"hidden_disabled", "discovery-team", "published", false}, {"foreign_tool", "another-team", "published", true}} {
		if _, err = pool.Exec(ctx, `INSERT INTO tools(workspace_id,id,name,risk,status,enabled,definition) VALUES($1,$2,$3,'read',$4,$5,$6::jsonb)`, record.workspace, core.NewID(), record.name, record.status, record.enabled, definition); err != nil {
			t.Fatal(err)
		}
	}
	service := core.NewService(postgres.New(pool))
	adapter, err := httpadapter.New(nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	executor := &execution.Executor{Service: service, Adapter: adapter}
	auth, err := identity.NewCloud(ctx, pool, "https://discovery.example", "")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/api/", (&httpapi.API{Service: service, Executor: executor, Adapter: adapter}).Handler(auth))
	mux.Handle("/mcp", mcpserver.Handler(service, executor, auth))
	server := httptest.NewServer(mux)
	defer server.Close()
	request := func(path, actor string, want int) []byte {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, "GET", server.URL+"/api/v1"+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+tokens[actor])
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != want {
			t.Fatalf("%s (%s): expected %d, got %d: %s", path, actor, want, response.StatusCode, body)
		}
		return body
	}
	decodePage := func(raw []byte) core.ToolDiscoveryPage {
		t.Helper()
		if len(raw) > 256<<10 {
			t.Fatalf("discovery response exceeds Pi budget: %d bytes", len(raw))
		}
		var wire struct {
			Items []map[string]any `json:"items"`
		}
		if err := json.Unmarshal(raw, &wire); err != nil {
			t.Fatal(err)
		}
		for _, item := range wire.Items {
			for key := range item {
				switch key {
				case "id", "name", "description", "risk", "version", "match", "server_id":
				default:
					t.Fatalf("discovery leaked extra field: %s", key)
				}
			}
			for _, key := range []string{"id", "name", "description", "risk", "version"} {
				if _, ok := item[key]; !ok {
					t.Fatalf("missing summary field %s", key)
				}
			}
		}
		var page core.ToolDiscoveryPage
		if err := json.Unmarshal(raw, &page); err != nil {
			t.Fatal(err)
		}
		return page
	}
	first := decodePage(request("/catalog/tools?limit=17", "operator", 200))
	if first.Total != 520 || len(first.Items) != 17 || first.NextCursor == "" {
		t.Fatalf("first page: %+v", first)
	}
	admin := decodePage(request("/catalog/tools?limit=17", "admin", 200))
	if !reflect.DeepEqual(first.Items, admin.Items) || admin.Total != 520 {
		t.Fatal("admin discovery bypassed publication visibility")
	}
	var registry core.ToolPage
	if err = json.Unmarshal(request("/tools?limit=1", "admin", 200), &registry); err != nil || registry.Total != 522 {
		t.Fatalf("registry total: %+v %v", registry, err)
	}
	request("/catalog/tools?cursor="+url.QueryEscape(registry.NextCursor), "admin", 400)
	request("/catalog/tools?cursor="+url.QueryEscape(first.NextCursor), "foreign", 400)
	request("/catalog/tools?query=different&cursor="+url.QueryEscape(first.NextCursor), "operator", 400)
	for _, query := range []string{"needle", "NEEDLE", "中文", "100%", "%_"} {
		page := decodePage(request("/catalog/tools?query="+url.QueryEscape(query), "operator", 200))
		if page.Total != 1 || len(page.Items) != 1 || page.Items[0].Name != "catalog_0001" {
			t.Fatalf("old literal match %q: %+v", query, page)
		}
	}
	for _, query := range []string{"limit=0", "limit=51", "limit=1&limit=2", "unknown=1", "cursor=invalid", "query=" + strings.Repeat("x", 201)} {
		request("/catalog/tools?"+query, "operator", 400)
	}

	client := mcp.NewClient(&mcp.Implementation{Name: "discovery-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: server.URL + "/mcp", HTTPClient: &http.Client{Transport: bearerTransport{tokens["operator"]}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	searchMCP := func(args map[string]any, wantError bool) core.ToolDiscoveryPage {
		t.Helper()
		reply, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "search_tools", Arguments: args})
		if err != nil || reply == nil || reply.IsError != wantError {
			t.Fatalf("MCP search result: %+v %v", reply, err)
		}
		if wantError {
			return core.ToolDiscoveryPage{}
		}
		return decodePage([]byte(reply.Content[0].(*mcp.TextContent).Text))
	}
	secondPath := "/catalog/tools?limit=17&cursor=" + url.QueryEscape(first.NextCursor)
	second := decodePage(request(secondPath, "operator", 200))
	fromMCP := searchMCP(map[string]any{"limit": 17, "cursor": first.NextCursor}, false)
	if !reflect.DeepEqual(second, fromMCP) {
		t.Fatalf("REST/MCP page mismatch: REST %+v MCP %+v", second, fromMCP)
	}

	rankedREST := decodePage(request("/catalog/tools?query=NEEDLE", "operator", 200))
	rankedMCP := searchMCP(map[string]any{"query": "NEEDLE"}, false)
	if !reflect.DeepEqual(rankedREST, rankedMCP) || rankedMCP.Items[0].Match == nil {
		t.Fatalf("ranked REST/MCP mismatch: %+v %+v", rankedREST, rankedMCP)
	}
	filteredREST := decodePage(request("/catalog/tools?query=NEEDLE&server_id=absent-service", "operator", 200))
	filteredMCP := searchMCP(map[string]any{"query": "NEEDLE", "server_id": "absent-service"}, false)
	if !reflect.DeepEqual(filteredREST, filteredMCP) || filteredMCP.Total != 0 {
		t.Fatalf("server filter REST/MCP mismatch: %+v %+v", filteredREST, filteredMCP)
	}
	searchMCP(map[string]any{"query": "NEEDLE", "server_id": "absent-service", "cursor": first.NextCursor}, true)
	searchMCP(map[string]any{"cursor": "invalid"}, true)
	// The protocol advertises and enforces the same page-size bound.
	invalidResult, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "search_tools", Arguments: map[string]any{"limit": 0}})
	if err == nil && (invalidResult == nil || !invalidResult.IsError) {
		t.Fatal("MCP accepted explicit zero page size")
	}

	seen := map[string]bool{}
	page := first
	for pageNumber := 0; ; pageNumber++ {
		if pageNumber > 40 {
			t.Fatal("cursor loop")
		}
		for _, item := range page.Items {
			if seen[item.ID] {
				t.Fatal("duplicate tool in pagination")
			}
			seen[item.ID] = true
		}
		if page.NextCursor == "" {
			break
		}
		page = decodePage(request("/catalog/tools?limit=17&cursor="+url.QueryEscape(page.NextCursor), "operator", 200))
	}
	if len(seen) != 520 {
		t.Fatalf("complete traversal returned %d tools", len(seen))
	}
	// Visibility is evaluated again, including when the caller retains an older cursor.
	disabled := second.Items[0]
	if _, err = pool.Exec(ctx, `UPDATE tools SET enabled=false WHERE workspace_id='discovery-team' AND id=$1`, disabled.ID); err != nil {
		t.Fatal(err)
	}
	live := decodePage(request(secondPath, "operator", 200))
	if live.Total != 519 {
		t.Fatalf("live visible count: %d", live.Total)
	}
	for _, item := range live.Items {
		if item.ID == disabled.ID {
			t.Fatal("disabled tool remained discoverable")
		}
	}
	if _, err = pool.Exec(ctx, `UPDATE gateway_users SET role='viewer' WHERE id='operator'`); err != nil {
		t.Fatal(err)
	}
	request(secondPath, "operator", 400)
	if _, err = pool.Exec(ctx, `UPDATE gateway_users SET disabled=true WHERE id='operator'`); err != nil {
		t.Fatal(err)
	}
	request("/catalog/tools", "operator", 401)
	if _, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "search_tools", Arguments: map[string]any{}}); err == nil {
		t.Fatal("MCP accepted a revoked identity")
	}
	t.Log(fmt.Sprintf("Verified 520 visible tools across REST/MCP, literal search, cursor binding, summary isolation and live identity revocation"))
}
