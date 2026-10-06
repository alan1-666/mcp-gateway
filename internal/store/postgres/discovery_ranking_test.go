package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/alan1-666/mcp-gateway/internal/core"
)

func seedRankedTool(t *testing.T, f fixture, id, name, description, server string, age int) {
	t.Helper()
	definition := map[string]any{"description": description, "input_schema": map[string]any{"type": "object", "description": "Fixed contract for repeatable discovery evaluation", "properties": map[string]any{"query": map[string]any{"type": "string"}}}}
	if server != "" {
		definition["mcp"] = map[string]string{"server_id": server, "tool_name": name, "schema_hash": "fixture"}
	}
	raw, err := json.Marshal(definition)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.pool.Exec(context.Background(), `INSERT INTO tools(workspace_id,id,name,risk,status,enabled,version,definition,created_at) VALUES($1,$2,$3,'read','published',true,1,$4,timestamptz '2025-01-01' + $5 * interval '1 second')`, f.admin.WorkspaceID, id, name, raw, age)
	if err != nil {
		t.Fatal(err)
	}
}

func seedDiscoveryServer(t *testing.T, f fixture, id string) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO mcp_servers(workspace_id,id,name,namespace,url,timeout_ms) VALUES($1,$2,$2,$2,'https://example.invalid/mcp',1000)`, f.admin.WorkspaceID, id); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := f.pool.Exec(context.Background(), `DELETE FROM mcp_servers WHERE workspace_id=$1 AND id=$2`, f.admin.WorkspaceID, id); err != nil {
			t.Error(err)
		}
	})
}

func TestDiscoveryRankingServerFilterAndCursor(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	seedDiscoveryServer(t, f, "service-a")
	seedDiscoveryServer(t, f, "service-b")
	for i, row := range []struct{ id, name, description, server string }{
		{"exact", "orders", "Retrieve purchase history", "service-a"},
		{"prefix", "orders_latest", "Recent purchases", "service-a"},
		{"fragment", "list_orders", "Query purchases", "service-a"},
		{"phrase", "purchase_history", "List orders by account", "service-a"},
		{"other", "orders_other", "Different service", "service-b"},
		{"http", "query_orders", "Standalone HTTP", ""},
	} {
		seedRankedTool(t, f, row.id, row.name, row.description, row.server, i)
	}
	input := core.ToolSearchInput{Query: "ORDERS", ServerID: "service-a", Limit: 1}
	var got []string
	for {
		page, err := f.svc.DiscoverTools(ctx, f.admin, input)
		if err != nil {
			t.Fatal(err)
		}
		if page.Total != 4 {
			t.Fatalf("filtered count %+v", page)
		}
		for _, item := range page.Items {
			got = append(got, item.ID)
			if item.ServerID != "service-a" || item.Match == nil {
				t.Fatalf("summary metadata %+v", item)
			}
		}
		if page.NextCursor == "" {
			break
		}
		input.Cursor = page.NextCursor
		if len(got) > 4 {
			t.Fatal("cursor did not advance")
		}
	}
	want := []string{"exact", "prefix", "fragment", "phrase"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ranked page traversal %v", got)
		}
	}
	first, err := f.svc.DiscoverTools(ctx, f.admin, core.ToolSearchInput{Query: "orders", ServerID: "service-a", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if first.Items[0].Match.Score != 500 || first.Items[0].Match.Reason != "exact_name" {
		t.Fatalf("exact reason %+v", first.Items[0])
	}
	for _, server := range []string{"", "service-b"} {
		_, err = f.svc.DiscoverTools(ctx, f.admin, core.ToolSearchInput{Query: "orders", ServerID: server, Cursor: first.NextCursor})
		if !errors.Is(err, core.ErrInvalid) {
			t.Fatalf("cursor reused with server %q: %v", server, err)
		}
	}
	for _, q := range []struct {
		query, id, reason string
		score             int
	}{{"orders_", "prefix", "name_prefix", 400}, {"st_orders", "fragment", "name_fragment", 300}, {"by account", "phrase", "phrase", 200}, {"account List", "phrase", "all_terms", 100}} {
		p, err := f.svc.DiscoverTools(ctx, f.admin, core.ToolSearchInput{Query: q.query, ServerID: "service-a"})
		if err != nil || len(p.Items) == 0 {
			t.Fatalf("%q %+v %v", q.query, p, err)
		}
		item := p.Items[0]
		if item.ID != q.id || item.Match.Reason != q.reason || item.Match.Score != q.score {
			t.Fatalf("rank %q %+v", q.query, item)
		}
	}
	if _, err = f.pool.Exec(ctx, `UPDATE mcp_servers SET enabled=false WHERE workspace_id=$1 AND id='service-a'`, f.admin.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	page, err := f.svc.DiscoverTools(ctx, f.admin, core.ToolSearchInput{Query: "orders", ServerID: "service-a", Cursor: first.NextCursor})
	if err != nil || page.Total != 0 || len(page.Items) != 0 {
		t.Fatalf("disabled server still visible %+v %v", page, err)
	}
	page, err = f.svc.DiscoverTools(ctx, f.admin, core.ToolSearchInput{ServerID: "missing"})
	if err != nil || page.Total != 0 {
		t.Fatalf("missing server %+v %v", page, err)
	}
}

func TestRankedDiscoveryFiltersPermissionsBeforeLimitAndRevocation(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	seedDiscoveryServer(t, f, "authorized-service")
	seedRankedTool(t, f, "hidden-exact", "status", "Status query", "", 1)
	seedRankedTool(t, f, "visible-one", "alpha.status", "Service status", "authorized-service", 2)
	seedRankedTool(t, f, "visible-two", "beta.status", "Service status", "authorized-service", 3)
	clientID := core.NewID()
	keyID := core.NewID()
	_, err := f.pool.Exec(ctx, `INSERT INTO gateway_clients(id,workspace_id,name,scopes,key_id,token_hash,key_expires_at) VALUES($1,$2,$1,ARRAY['tools:read'],$3,decode(md5($1)||md5($3),'hex'),clock_timestamp()+interval '1 hour')`, clientID, f.admin.WorkspaceID, keyID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := f.pool.Exec(context.Background(), `DELETE FROM gateway_clients WHERE id=$1`, clientID); err != nil {
			t.Error(err)
		}
	})
	_, err = f.pool.Exec(ctx, `INSERT INTO gateway_client_server_grants(workspace_id,client_id,server_id) VALUES($1,$2,'authorized-service')`, f.admin.WorkspaceID, clientID)
	if err != nil {
		t.Fatal(err)
	}
	actor := core.Actor{ID: clientID, ClientID: clientID, ClientKeyID: keyID, WorkspaceID: f.admin.WorkspaceID, Role: core.RoleOperator}
	first, err := f.svc.DiscoverTools(ctx, actor, core.ToolSearchInput{Query: "status", Limit: 1})
	if err != nil || first.Total != 2 || len(first.Items) != 1 || first.Items[0].ID != "visible-two" || first.NextCursor == "" {
		t.Fatalf("unauthorized exact match must not consume page %+v %v", first, err)
	}
	_, err = f.pool.Exec(ctx, `DELETE FROM gateway_client_server_grants WHERE client_id=$1`, clientID)
	if err != nil {
		t.Fatal(err)
	}
	next, err := f.svc.DiscoverTools(ctx, actor, core.ToolSearchInput{Query: "status", Cursor: first.NextCursor})
	if err != nil || next.Total != 0 || len(next.Items) != 0 {
		t.Fatalf("revoked grant remains visible %+v %v", next, err)
	}
}

// A deterministic synthetic catalog and labelled bilingual queries. This is a
// regression benchmark, not a claim about semantic retrieval or production load.
func TestDiscoveryFixedCatalogEvaluation(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	// 992 identical, recent distractors intentionally exercise old recency bias.
	_, err := f.pool.Exec(ctx, `INSERT INTO tools(workspace_id,id,name,risk,status,enabled,version,definition,created_at)
 SELECT $1,'noise-'||n,'utility_'||lpad(n::text,4,'0'),'read','published',true,1,
 jsonb_build_object('description','Related to orders and tasks and logs and metrics; 请使用专用订单查询和任务状态工具', 'input_schema',jsonb_build_object('type','object','description',repeat('schema field documentation ',20))),
 timestamptz '2025-02-01' + n * interval '1 second' FROM generate_series(1,992) n`, f.admin.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	targets := []struct{ name, description string }{
		{"orders", "Retrieve customer purchase history"}, {"tasks", "Read scheduled job state"}, {"logs", "Read error records"}, {"metrics", "Read service health"},
		{"lookup_purchase", "客户 历史 明细 查询"}, {"job_health", "查看 任务 状态"}, {"error_inspect", "定位 错误 日志"}, {"service_health", "查询 服务 指标"},
	}
	for i, row := range targets {
		seedRankedTool(t, f, "target-"+row.name, row.name, row.description, "", i)
	}
	cases := []struct{ query, target string }{
		{"orders", "target-orders"}, {"tasks", "target-tasks"}, {"logs", "target-logs"}, {"metrics", "target-metrics"},
		{"客户 查询", "target-lookup_purchase"}, {"状态 任务", "target-job_health"}, {"错误 日志", "target-error_inspect"}, {"服务 指标", "target-service_health"},
	}
	var oldLatencies, newLatencies []float64
	oldHits, newHits, summaryBytes := 0, 0, 0
	for _, tc := range cases {
		var baseline []byte
		var page core.ToolDiscoveryPage
		for i := 0; i < 20; i++ {
			start := time.Now()
			err = f.pool.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(item ORDER BY created_at DESC,id DESC),'[]') FROM (
 SELECT id,created_at,jsonb_build_object('id',id,'name',name,'description',definition->>'description','risk',risk,'version',version) item FROM tools
 WHERE workspace_id=$1 AND enabled AND status='published' AND strpos(lower(name||' '||COALESCE(definition->>'description','')),lower($2))>0
 ORDER BY created_at DESC,id DESC LIMIT 5) t`, f.admin.WorkspaceID, tc.query).Scan(&baseline)
			if err != nil {
				t.Fatal(err)
			}
			oldLatencies = append(oldLatencies, float64(time.Since(start).Microseconds())/1000)
			start = time.Now()
			page, err = f.svc.DiscoverTools(ctx, f.admin, core.ToolSearchInput{Query: tc.query, Limit: 5})
			if err != nil {
				t.Fatal(err)
			}
			newLatencies = append(newLatencies, float64(time.Since(start).Microseconds())/1000)
		}
		var old []core.ToolSummary
		if err = json.Unmarshal(baseline, &old); err != nil {
			t.Fatal(err)
		}
		oldHit, newHit := false, false
		for _, item := range old {
			if item.ID == tc.target {
				oldHit = true
			}
		}
		for _, item := range page.Items {
			if item.ID == tc.target {
				newHit = true
			}
		}
		if oldHit {
			oldHits++
		}
		if newHit {
			newHits++
		}
		if !newHit {
			t.Logf("labelled target outside top 5 for %q: lexical ties remain a known limitation", tc.query)
		}
		raw, _ := json.Marshal(page)
		summaryBytes += len(raw)
		t.Logf("query=%q baseline_hit@5=%t ranked_hit@5=%t ranked_page_bytes=%d", tc.query, oldHit, newHit, len(raw))
	}
	if oldHits != 2 || newHits != 7 {
		t.Fatalf("fixed relevance baseline changed: substring=%d ranked=%d; inspect rankings before updating labels", oldHits, newHits)
	}
	var catalogCount, fullBytes int
	err = f.pool.QueryRow(ctx, `SELECT count(*),octet_length(jsonb_agg(definition||jsonb_build_object('id',id,'name',name,'risk',risk,'version',version))::text) FROM tools WHERE workspace_id=$1`, f.admin.WorkspaceID).Scan(&catalogCount, &fullBytes)
	if err != nil {
		t.Fatal(err)
	}
	if catalogCount != 1000 {
		t.Fatalf("fixed catalog count=%d", catalogCount)
	}
	for _, list := range [][]float64{oldLatencies, newLatencies} {
		sort.Float64s(list)
	}
	percentile := func(list []float64, p int) float64 { return list[(len(list)-1)*p/100] }
	t.Logf("EVALUATION catalog=%d queries=%d samples_per_query=20 baseline_hit@5=%d/%d ranked_hit@5=%d/%d baseline_p50_ms=%.3f baseline_p95_ms=%.3f ranked_p50_ms=%.3f ranked_p95_ms=%.3f full_catalog_json_bytes=%d mean_ranked_page_bytes=%d", catalogCount, len(cases), oldHits, len(cases), newHits, len(cases), percentile(oldLatencies, 50), percentile(oldLatencies, 95), percentile(newLatencies, 50), percentile(newLatencies, 95), fullBytes, summaryBytes/len(cases))
}
