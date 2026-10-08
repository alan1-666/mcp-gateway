package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alan1-666/mcp-gateway/internal/adapters/httpadapter"
	"github.com/alan1-666/mcp-gateway/internal/adapters/mcpadapter"
	"github.com/alan1-666/mcp-gateway/internal/capacity"
	"github.com/alan1-666/mcp-gateway/internal/clients"
	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/alan1-666/mcp-gateway/internal/execution"
	"github.com/alan1-666/mcp-gateway/internal/identity"
	"github.com/alan1-666/mcp-gateway/internal/store/postgres"
	"github.com/alan1-666/mcp-gateway/internal/transport/mcpserver"
	"github.com/alan1-666/mcp-gateway/internal/upstreams"
	"github.com/alan1-666/mcp-gateway/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type loadSample struct {
	Index         int     `json:"index"`
	MS            float64 `json:"latency_ms"`
	Outcome       string  `json:"outcome"`
	ResponseBytes int     `json:"response_json_bytes"`
	OperationID   string  `json:"operation_id,omitempty"`
}
type loadMeasurement struct {
	Route               string       `json:"route"`
	Mode                string       `json:"mode"`
	CatalogSize         int          `json:"catalog_size"`
	Concurrency         int          `json:"concurrency"`
	FaultEvery          int          `json:"fault_every"`
	Warmup              int          `json:"warmup"`
	Samples             []loadSample `json:"samples"`
	Succeeded           int          `json:"succeeded"`
	Failed              int          `json:"failed"`
	Rejected            int          `json:"rejected"`
	Unknown             int          `json:"unknown"`
	P50MS               float64      `json:"p50_ms"`
	P95MS               float64      `json:"p95_ms"`
	WallMS              float64      `json:"wall_ms"`
	CompletedPerSecond  float64      `json:"completed_per_second"`
	SuccessfulPerSecond float64      `json:"successful_per_second"`
	Initializes         int64        `json:"upstream_initializes"`
	CatalogRequests     int64        `json:"upstream_catalog_requests"`
	Calls               int64        `json:"upstream_calls"`
	Observations        int          `json:"durable_observations"`
	CatalogMeanMS       float64      `json:"gateway_catalog_mean_ms"`
}

// This explicit opt-in workload uses loopback servers and an isolated schema.
// No public provider, production account, or model request is involved.
func TestMCPLoadEvaluation(t *testing.T) {
	if os.Getenv("RUN_MCP_LOAD_EVALUATION") != "1" {
		t.Skip("set RUN_MCP_LOAD_EVALUATION=1 and a loopback TEST_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cfg, err := pgxpool.ParseConfig(os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal("invalid test database configuration")
	}
	if cfg.ConnConfig.Host != "127.0.0.1" && cfg.ConnConfig.Host != "localhost" && cfg.ConnConfig.Host != "::1" {
		t.Fatal("evaluation requires a loopback database")
	}
	base, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	schema := "mcp_load_" + strings.ReplaceAll(core.NewID(), "-", "")
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = base.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	defer func() {
		c, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if _, e := base.Exec(c, "DROP SCHEMA IF EXISTS "+quoted+" CASCADE"); e != nil {
			t.Errorf("cleanup: %v", e)
		}
	}()
	cfg = cfg.Copy()
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	cfg.MaxConns = 12
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err = migrations.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var rows []loadMeasurement
	for _, size := range []int{1, 100} {
		for _, scenario := range []struct {
			mode               string
			concurrency, fault int
		}{{"cold", 1, 0}, {"warm", 1, 0}, {"warm", 4, 0}, {"warm", 1, 6}} {
			for _, route := range []string{"direct", "gateway"} {
				t.Run(fmt.Sprintf("%s/%d/%s/c%d/f%d", route, size, scenario.mode, scenario.concurrency, scenario.fault), func(t *testing.T) {
					rows = append(rows, runMCPLoadCase(t, ctx, pool, route, size, scenario.mode, scenario.concurrency, scenario.fault))
				})
			}
		}
	}
	report := struct {
		Version        int               `json:"version"`
		RecordedAt     string            `json:"recorded_at"`
		Go             string            `json:"go"`
		OS             string            `json:"os"`
		Arch           string            `json:"arch"`
		CPUs           int               `json:"cpus"`
		GOMAXPROCS     int               `json:"gomaxprocs"`
		FixtureDelayMS int               `json:"fixture_delay_ms"`
		PayloadBytes   int               `json:"payload_bytes"`
		Results        []loadMeasurement `json:"results"`
	}{1, time.Now().UTC().Format(time.RFC3339), runtime.Version(), runtime.GOOS, runtime.GOARCH, runtime.NumCPU(), runtime.GOMAXPROCS(0), 2, 2048, rows}
	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if path := os.Getenv("MCP_LOAD_EVIDENCE"); path != "" && !t.Failed() {
		if err = os.WriteFile(path, append(raw, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range rows {
		t.Logf("%s catalog=%d %s concurrency=%d fault=%d success=%d failed=%d p50=%.2fms p95=%.2fms calls=%d lists=%d", r.Route, r.CatalogSize, r.Mode, r.Concurrency, r.FaultEvery, r.Succeeded, r.Failed, r.P50MS, r.P95MS, r.Calls, r.CatalogRequests)
	}
}

func runMCPLoadCase(t *testing.T, ctx context.Context, pool *pgxpool.Pool, route string, size int, mode string, concurrency, fault int) loadMeasurement {
	t.Helper()
	var initializes, catalogs, calls atomic.Int64
	sdk := mcp.NewServer(&mcp.Implementation{Name: "load-fixture", Version: "1"}, nil)
	payload := strings.Repeat("x", 2048)
	for i := 0; i < size; i++ {
		sdk.AddTool(&mcp.Tool{Name: fmt.Sprintf("fixture_%03d", i), Description: "Read fixed evaluation payload", InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)}, func(ctx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			n := calls.Add(1)
			timer := time.NewTimer(2 * time.Millisecond)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-timer.C:
			}
			if fault > 0 && n%int64(fault) == 0 {
				return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "injected tool failure"}}}, nil
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: payload}}}, nil
		})
	}
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return sdk }, &mcp.StreamableHTTPOptions{JSONResponse: true})
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			body, e := io.ReadAll(io.LimitReader(r.Body, 1<<20))
			if e != nil {
				http.Error(w, "read failure", 400)
				return
			}
			r.Body.Close()
			r.Body = io.NopCloser(bytes.NewReader(body))
			var msg struct {
				Method string `json:"method"`
			}
			if json.Unmarshal(body, &msg) == nil {
				switch msg.Method {
				case "initialize":
					initializes.Add(1)
				case "tools/list":
					catalogs.Add(1)
				}
			}
		}
		handler.ServeHTTP(w, r)
	}))
	defer remote.Close()
	endpoint, token, toolID := remote.URL, "", ""
	svc := core.NewService(postgres.New(pool))
	admin := core.Actor{ID: core.NewID(), WorkspaceID: core.NewID(), Role: core.RoleAdmin}
	if route == "gateway" {
		egress, e := httpadapter.New([]string{remote.URL}, []string{"127.0.0.0/8"}, nil)
		if e != nil {
			t.Fatal(e)
		}
		store := upstreams.NewStore(pool)
		adapter := mcpadapter.New(egress, store)
		if mode == "warm" {
			if e = adapter.EnableSessionPool(mcpadapter.SessionPoolOptions{MaxSessions: 8, IdleTTL: time.Minute, MaxLifetime: 10 * time.Minute}); e != nil {
				t.Fatal(e)
			}
		}
		defer adapter.Close()
		up := upstreams.New(store, adapter)
		server, e := up.Create(ctx, admin, core.MCPServerInput{Name: "Load fixture", Namespace: "load", URL: remote.URL, TimeoutMS: 5000})
		if e != nil {
			t.Fatal(e)
		}
		catalog, e := up.Discover(ctx, admin, server.ID)
		if e != nil {
			t.Fatal(e)
		}
		var selected core.RemoteTool
		for _, v := range catalog.Items {
			if v.Name == "fixture_000" {
				selected = v
			}
		}
		if selected.Name == "" {
			t.Fatal("fixture missing")
		}
		tool, e := up.Import(ctx, admin, server.ID, upstreams.ImportInput{ToolName: selected.Name, SchemaHash: selected.SchemaHash, Risk: core.RiskRead})
		if e != nil {
			t.Fatal(e)
		}
		if _, e = svc.PublishTool(ctx, admin, tool.ID); e != nil {
			t.Fatal(e)
		}
		toolID = tool.ID
		manager := clients.New(pool)
		issued, e := manager.Create(ctx, admin, clients.CreateInput{Name: "Load fixture client", Scopes: []string{clients.ScopeRead, clients.ScopeInvoke}})
		if e != nil {
			t.Fatal(e)
		}
		grants := []string{tool.ID}
		if _, e = manager.Update(ctx, admin, issued.Client.ID, clients.UpdateInput{ExpectedVersion: issued.Client.Version, ToolIDs: &grants}); e != nil {
			t.Fatal(e)
		}
		token = issued.APIKey
		auth, e := identity.NewCloud(ctx, pool, "https://load.example", "")
		if e != nil {
			t.Fatal(e)
		}
		executor := &execution.Executor{Service: svc, Adapter: &execution.Router{HTTP: egress, MCP: adapter}, Capacity: capacity.New(pool)}
		gateway := httptest.NewServer(mcpserver.Handler(svc, executor, auth))
		defer gateway.Close()
		endpoint = gateway.URL
	}
	connect := func() (*mcp.ClientSession, error) {
		httpClient := &http.Client{}
		if token != "" {
			httpClient.Transport = bearerTransport{token}
		}
		client := mcp.NewClient(&mcp.Implementation{Name: "load-client", Version: "1"}, nil)
		session, e := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: endpoint, HTTPClient: httpClient, MaxRetries: -1, DisableStandaloneSSE: true}, nil)
		if e == nil {
			_, e = session.ListTools(ctx, &mcp.ListToolsParams{})
			if e != nil {
				session.Close()
				return nil, e
			}
		}
		return session, e
	}
	invoke := func(session *mcp.ClientSession, index int) loadSample {
		sample := loadSample{Index: index, Outcome: "transport_error"}
		params := &mcp.CallToolParams{Name: "fixture_000", Arguments: map[string]any{}}
		if route == "gateway" {
			params = &mcp.CallToolParams{Name: "call_tool", Arguments: map[string]any{"tool_id": toolID, "arguments": map[string]any{}, "idempotency_key": fmt.Sprintf("evaluation-%s-%d", admin.WorkspaceID, index)}}
		}
		result, e := session.CallTool(ctx, params)
		if e != nil {
			return sample
		}
		raw, e := json.Marshal(result)
		if e != nil {
			sample.Outcome = "invalid_response"
			return sample
		}
		sample.ResponseBytes = len(raw)
		business := result
		if route == "gateway" {
			if result.IsError {
				sample.Outcome = "gateway_rejected"
				return sample
			}
			if len(result.Content) != 1 {
				sample.Outcome = "invalid_response"
				return sample
			}
			text, ok := result.Content[0].(*mcp.TextContent)
			if !ok {
				sample.Outcome = "invalid_response"
				return sample
			}
			var op core.Operation
			if json.Unmarshal([]byte(text.Text), &op) != nil {
				sample.Outcome = "invalid_response"
				return sample
			}
			sample.OperationID = op.ID
			if op.State == core.StateFailed {
				sample.Outcome = "tool_error"
				return sample
			}
			if op.State != core.StateSucceeded {
				sample.Outcome = string(op.State)
				return sample
			}
			business = new(mcp.CallToolResult)
			if json.Unmarshal(op.Result, business) != nil {
				sample.Outcome = "invalid_response"
				return sample
			}
		}
		if business.IsError {
			sample.Outcome = "tool_error"
			return sample
		}
		if len(business.Content) != 1 {
			sample.Outcome = "invalid_response"
			return sample
		}
		text, ok := business.Content[0].(*mcp.TextContent)
		if !ok || text.Text != payload {
			sample.Outcome = "invalid_response"
			return sample
		}
		sample.Outcome = "succeeded"
		return sample
	}
	m := loadMeasurement{Route: route, Mode: mode, CatalogSize: size, Concurrency: concurrency, FaultEvery: fault, Samples: make([]loadSample, 24)}
	sessions := make([]*mcp.ClientSession, concurrency)
	if mode == "warm" {
		for i := range sessions {
			s, e := connect()
			if e != nil {
				t.Fatal(e)
			}
			sessions[i] = s
			defer s.Close()
			sample := invoke(s, -i-1)
			if sample.Outcome != "succeeded" {
				t.Fatalf("warmup failed: %+v", sample)
			}
			m.Warmup++
		}
	}
	beforeInit, beforeLists, beforeCalls := initializes.Load(), catalogs.Load(), calls.Load()
	started := time.Now()
	var wg sync.WaitGroup
	for worker := 0; worker < concurrency; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for index := worker; index < len(m.Samples); index += concurrency {
				start := time.Now()
				session := sessions[worker]
				if mode == "cold" {
					var e error
					session, e = connect()
					if e != nil {
						m.Samples[index] = loadSample{Index: index, MS: float64(time.Since(start).Microseconds()) / 1000, Outcome: "connect_error"}
						continue
					}
				}
				sample := invoke(session, index)
				sample.MS = float64(time.Since(start).Microseconds()) / 1000
				m.Samples[index] = sample
				if mode == "cold" {
					session.Close()
				}
			}
		}(worker)
	}
	wg.Wait()
	m.WallMS = float64(time.Since(started).Microseconds()) / 1000
	m.Initializes = initializes.Load() - beforeInit
	m.CatalogRequests = catalogs.Load() - beforeLists
	m.Calls = calls.Load() - beforeCalls
	var latency []float64
	var catalogSum int64
	for _, sample := range m.Samples {
		latency = append(latency, sample.MS)
		switch sample.Outcome {
		case "succeeded":
			m.Succeeded++
		case "gateway_rejected":
			m.Rejected++
		case string(core.StateUnknown):
			m.Unknown++
		default:
			m.Failed++
		}
		if sample.OperationID != "" {
			events, e := svc.ListEvents(ctx, admin, sample.OperationID, 0)
			if e != nil {
				t.Fatal(e)
			}
			for _, event := range events {
				var data struct {
					MCP *core.MCPObservation `json:"mcp_execution"`
				}
				if e = json.Unmarshal(event.Data, &data); e != nil {
					t.Fatal(e)
				}
				if data.MCP != nil {
					wantCode := "ok"
					if sample.Outcome == "tool_error" {
						wantCode = "tool_error"
					}
					if data.MCP.Code != wantCode {
						t.Errorf("sample %d observation code %s, want %s", sample.Index, data.MCP.Code, wantCode)
					}
					if e = data.MCP.Validate(); e != nil {
						t.Fatal(e)
					}
					m.Observations++
					catalogSum += data.MCP.PhasesMS.Catalog
				}
			}
		}
	}
	slices.Sort(latency)
	m.P50MS = latency[int(math.Ceil(float64(len(latency))*.5))-1]
	m.P95MS = latency[int(math.Ceil(float64(len(latency))*.95))-1]
	m.CompletedPerSecond = float64(len(latency)) * 1000 / m.WallMS
	m.SuccessfulPerSecond = float64(m.Succeeded) * 1000 / m.WallMS
	if m.Observations > 0 {
		m.CatalogMeanMS = float64(catalogSum) / float64(m.Observations)
	}
	expectedFailures := 0
	if fault > 0 {
		expectedFailures = len(m.Samples) / fault
	}
	if m.Succeeded != len(m.Samples)-expectedFailures || m.Failed != expectedFailures || m.Rejected != 0 || m.Unknown != 0 || m.Calls != int64(len(m.Samples)) {
		t.Errorf("unexpected outcome or replay: %+v", m)
	}
	for _, sample := range m.Samples {
		if sample.Outcome != "succeeded" && sample.Outcome != "tool_error" {
			t.Errorf("unexpected sample: %+v", sample)
		}
	}
	if route == "gateway" && m.Observations != len(m.Samples) {
		t.Errorf("missing/duplicate observations: %d", m.Observations)
	}
	if route == "gateway" && m.CatalogRequests != int64(len(m.Samples)) {
		t.Errorf("expected complete discovery per call, got %d", m.CatalogRequests)
	}
	if mode == "warm" && concurrency == 1 && fault == 0 && m.Initializes != 0 {
		t.Errorf("expected connection reuse, got %d initializes", m.Initializes)
	}
	return m
}
