package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/alan1-666/mcp-gateway/internal/execution"
	"github.com/alan1-666/mcp-gateway/internal/identity"
	"github.com/alan1-666/mcp-gateway/internal/store/postgres"
	"github.com/alan1-666/mcp-gateway/internal/transport/httpapi"
	"github.com/alan1-666/mcp-gateway/internal/transport/mcpserver"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func policyUpdate(t *testing.T, f fixture, tool core.Tool, p core.ApprovalPolicy) core.Tool {
	t.Helper()
	got, err := f.svc.UpdateToolApprovalPolicy(context.Background(), f.admin, tool.ID, core.ApprovalPolicyUpdateInput{ExpectedVersion: tool.Version, ApprovalPolicy: p})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

type countingPolicyAdapter struct {
	calls atomic.Int32
	state core.State
}

func (a *countingPolicyAdapter) Execute(context.Context, core.Actor, core.Tool, core.Operation) core.FinishInput {
	a.calls.Add(1)
	return core.FinishInput{State: a.state, Result: json.RawMessage(`{"ok":true}`)}
}

func TestCallApprovalPolicyLifecycle(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	tool := f.tool(t, core.RiskWrite)
	adapter := &countingPolicyAdapter{state: core.StateSucceeded}
	e := execution.Executor{Service: f.svc, Adapter: adapter}
	in := core.PrepareInput{ToolID: tool.ID, Arguments: json.RawMessage(`{"job_id":42}`), IdempotencyKey: "stable-write-key"}
	pending, err := e.Call(ctx, f.operator, in)
	if err != nil || pending.State != core.StateWaitingApproval || adapter.calls.Load() != 0 {
		t.Fatal(pending, err)
	}
	tool = policyUpdate(t, f, tool, core.ApprovalNone)
	same, err := e.Call(ctx, f.operator, in)
	if err != nil || same.ID != pending.ID || same.State != core.StateWaitingApproval {
		t.Fatal("loosening auto executed pending", same, err)
	}
	if _, err = f.svc.Approve(ctx, f.approver, pending.ID); err != nil {
		t.Fatal(err)
	}
	done, err := e.Call(ctx, f.operator, in)
	if err != nil || done.State != core.StateSucceeded || done.ToolVersion != 1 || adapter.calls.Load() != 1 {
		t.Fatal(done, err)
	}
	in.IdempotencyKey = "exempt-write-key"
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := e.Call(ctx, f.operator, in); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	done, err = e.Call(ctx, f.operator, in)
	if err != nil || done.State != core.StateSucceeded || done.ApprovedBy != "" || done.ToolVersion != 2 || adapter.calls.Load() != 2 {
		t.Fatal(done, err, adapter.calls.Load())
	}
	if _, err = e.Call(ctx, f.operator, core.PrepareInput{ToolID: tool.ID, Arguments: json.RawMessage(`{"job_id":43}`), IdempotencyKey: in.IdempotencyKey}); !errors.Is(err, core.ErrConflict) {
		t.Fatal(err)
	}
	if _, err = e.Call(ctx, f.other, in); !errors.Is(err, core.ErrConflict) {
		t.Fatal(err)
	}
	in.IdempotencyKey = "unknown-write-key"
	adapter.state = core.StateUnknown
	unknown, err := e.Call(ctx, f.operator, in)
	if err != nil || unknown.State != core.StateUnknown {
		t.Fatal(unknown, err)
	}
	again, err := e.Call(ctx, f.operator, in)
	if err != nil || again.ID != unknown.ID || adapter.calls.Load() != 3 {
		t.Fatal(again, err)
	}
}

func TestApprovalTighteningPreservesIntentAndRequiresReview(t *testing.T) {
	for _, loosenAgain := range []bool{false, true} {
		t.Run(map[bool]string{false: "tightened", true: "tighten-loosen"}[loosenAgain], func(t *testing.T) {
			f := database(t)
			ctx := context.Background()
			tool := policyUpdate(t, f, f.tool(t, core.RiskWrite), core.ApprovalNone)
			op := f.prepare(t, tool, core.NewID())
			var before []byte
			if err := f.pool.QueryRow(ctx, `SELECT tool_snapshot FROM operations WHERE workspace_id=$1 AND id=$2`, op.WorkspaceID, op.ID).Scan(&before); err != nil {
				t.Fatal(err)
			}
			tool = policyUpdate(t, f, tool, core.ApprovalRequired)
			if loosenAgain {
				tool = policyUpdate(t, f, tool, core.ApprovalNone)
			}
			held, _, claimed, err := f.svc.Claim(ctx, f.operator, op.ID)
			if err != nil || claimed || held.State != core.StateWaitingApproval {
				t.Fatal(held, claimed, err)
			}
			var after []byte
			if err := f.pool.QueryRow(ctx, `SELECT tool_snapshot FROM operations WHERE workspace_id=$1 AND id=$2`, op.WorkspaceID, op.ID).Scan(&after); err != nil {
				t.Fatal(err)
			}
			if string(before) != string(after) || held.ArgumentsHash != op.ArgumentsHash || held.ToolVersion != op.ToolVersion {
				t.Fatal("intent mutated")
			}
			if _, err = f.svc.Approve(ctx, f.approver, op.ID); err != nil {
				t.Fatal(err)
			}
			_, _, claimed, err = f.svc.Claim(ctx, f.operator, op.ID)
			if err != nil || !claimed {
				t.Fatal(claimed, err)
			}
		})
	}
}

func TestApprovalPolicyVersionAuditAndAuthorization(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	tool := f.tool(t, core.RiskWrite)
	repo := postgres.New(f.pool)
	in := core.ApprovalPolicyUpdateInput{ExpectedVersion: 1, ApprovalPolicy: core.ApprovalNone}
	if _, err := repo.UpdateToolApprovalPolicy(ctx, f.operator, tool.ID, in); !errors.Is(err, core.ErrForbidden) {
		t.Fatal(err)
	}
	if _, err := repo.UpdateToolApprovalPolicy(ctx, f.admin, tool.ID, core.ApprovalPolicyUpdateInput{}); !errors.Is(err, core.ErrInvalid) {
		t.Fatal(err)
	}
	foreign := f.admin
	foreign.WorkspaceID = core.NewID()
	if _, err := f.svc.UpdateToolApprovalPolicy(ctx, foreign, tool.ID, in); !errors.Is(err, core.ErrNotFound) {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var wins, conflicts atomic.Int32
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := f.svc.UpdateToolApprovalPolicy(ctx, f.admin, tool.ID, in)
			if err == nil {
				wins.Add(1)
			} else if errors.Is(err, core.ErrConflict) {
				conflicts.Add(1)
			} else {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 || conflicts.Load() != 7 {
		t.Fatal(wins.Load(), conflicts.Load())
	}
	in.ExpectedVersion = 2
	got, err := f.svc.UpdateToolApprovalPolicy(ctx, f.admin, tool.ID, in)
	if err != nil || got.Version != 2 || got.Risk != core.RiskWrite {
		t.Fatal(got, err)
	}
	var audit, versions int
	if err = f.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE workspace_id=$1 AND action='TOOL_APPROVAL_POLICY_UPDATED'`, tool.WorkspaceID).Scan(&audit); err != nil {
		t.Fatal(err)
	}
	if err = f.pool.QueryRow(ctx, `SELECT count(*) FROM tool_versions WHERE workspace_id=$1 AND tool_id=$2`, tool.WorkspaceID, tool.ID).Scan(&versions); err != nil || audit != 1 || versions != 2 {
		t.Fatal(audit, versions, err)
	}
}

func TestApprovalPolicyHTTPContract(t *testing.T) {
	f := database(t)
	tool := f.tool(t, core.RiskWrite)
	adminToken, operatorToken := core.NewID(), core.NewID()
	auth, err := identity.New([]identity.Token{{Token: adminToken, Actor: f.admin}, {Token: operatorToken, Actor: f.operator}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := (&httpapi.API{Service: f.svc}).Handler(auth)
	for _, tc := range []struct {
		token, body string
		status      int
	}{{"", `{}`, 401}, {operatorToken, `{"expected_version":1,"approval_policy":"none"}`, 403}, {adminToken, `{}`, 400}, {adminToken, `{"expected_version":1,"approval_policy":"skip"}`, 400}, {adminToken, `{"expected_version":1,"approval_policy":"none","bypass":true}`, 400}, {adminToken, `{"expected_version":1,"approval_policy":"none"}`, 200}, {adminToken, `{"expected_version":1,"approval_policy":"required"}`, 409}} {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/tools/"+tool.ID+"/approval-policy", strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		if tc.token != "" {
			req.Header.Set("Authorization", "Bearer "+tc.token)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != tc.status {
			t.Fatal(tc, rec.Code, rec.Body.String())
		}
	}
}

type policyFailDB struct {
	postgres.DB
	prefix string
}
type policyFailTx struct {
	pgx.Tx
	prefix string
}

func (d policyFailDB) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := d.DB.Begin(ctx)
	return policyFailTx{Tx: tx, prefix: d.prefix}, err
}
func (tx policyFailTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if strings.HasPrefix(sql, tx.prefix) {
		return pgconn.CommandTag{}, errors.New("injected persistence outage")
	}
	return tx.Tx.Exec(ctx, sql, args...)
}
func TestApprovalPolicyFailureRollsBackVersionAndAudit(t *testing.T) {
	for _, prefix := range []string{"UPDATE tools", "INSERT INTO audit_events"} {
		t.Run(prefix, func(t *testing.T) {
			f := database(t)
			tool := f.tool(t, core.RiskWrite)
			repo := postgres.New(policyFailDB{DB: f.pool, prefix: prefix})
			if _, err := repo.UpdateToolApprovalPolicy(context.Background(), f.admin, tool.ID, core.ApprovalPolicyUpdateInput{ExpectedVersion: 1, ApprovalPolicy: core.ApprovalNone}); err == nil {
				t.Fatal("injected failure ignored")
			}
			unchanged, err := f.svc.GetTool(context.Background(), f.admin, tool.ID)
			if err != nil || unchanged.Version != 1 || unchanged.ApprovalPolicy != core.ApprovalRequired {
				t.Fatal(unchanged, err)
			}
		})
	}
}

func TestPolicyTighteningSerializesWithConcurrentClaim(t *testing.T) {
	f := database(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tool := policyUpdate(t, f, f.tool(t, core.RiskWrite), core.ApprovalNone)
	op := f.prepare(t, tool, core.NewID())
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	// The policy update keeps an exclusive row lock until commit; a racing Claim
	// must observe the committed revision rather than its earlier READY snapshot.
	if _, err = postgres.New(tx).UpdateToolApprovalPolicy(ctx, f.admin, tool.ID, core.ApprovalPolicyUpdateInput{ExpectedVersion: tool.Version, ApprovalPolicy: core.ApprovalRequired}); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() {
		got, _, claimed, err := f.svc.Claim(ctx, f.operator, op.ID)
		if err == nil && (claimed || got.State != core.StateWaitingApproval) {
			err = errors.New("stale exemption dispatched")
		}
		finished <- err
	}()
	select {
	case err := <-finished:
		t.Fatalf("claim escaped lock: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-finished; err != nil {
		t.Fatal(err)
	}
}

type policyBearer struct{ token string }

func (t policyBearer) RoundTrip(r *http.Request) (*http.Response, error) {
	c := r.Clone(r.Context())
	c.Header.Set("Authorization", "Bearer "+t.token)
	return http.DefaultTransport.RoundTrip(c)
}
func TestCallFacadeHTTPAndStandardMCP(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	read := f.tool(t, core.RiskRead)
	write := f.tool(t, core.RiskWrite)
	adapter := &countingPolicyAdapter{state: core.StateSucceeded}
	executor := &execution.Executor{Service: f.svc, Adapter: adapter}
	token := core.NewID()
	auth, err := identity.New([]identity.Token{{Token: token, Actor: f.operator}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/api/", (&httpapi.API{Service: f.svc, Executor: executor}).Handler(auth))
	mux.Handle("/mcp", mcpserver.Handler(f.svc, executor, auth))
	server := httptest.NewServer(mux)
	defer server.Close()
	httpCall := func(body string, want int) core.Operation {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/call", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatal(rec.Code, rec.Body.String())
		}
		var op core.Operation
		if want == 200 {
			if err := json.Unmarshal(rec.Body.Bytes(), &op); err != nil {
				t.Fatal(err)
			}
		}
		return op
	}
	body := `{"tool_id":"` + read.ID + `","arguments":{"job_id":42},"idempotency_key":"http-stable-key"}`
	op := httpCall(body, 200)
	if op.State != core.StateSucceeded {
		t.Fatal(op)
	}
	if again := httpCall(body, 200); again.ID != op.ID || adapter.calls.Load() != 1 {
		t.Fatal(again)
	}
	httpCall(`{"tool_id":"`+read.ID+`","arguments":{"job_id":"bad"},"idempotency_key":"http-invalid-key"}`, 400)
	httpCall(`{"tool_id":"`+read.ID+`","arguments":{},"idempotency_key":"short","approval_policy":"none"}`, 400)
	client := mcp.NewClient(&mcp.Implementation{Name: "policy-contract", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: server.URL + "/mcp", HTTPClient: &http.Client{Transport: policyBearer{token}}, MaxRetries: -1, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	call := func(tool, key string, wantError bool, extra bool) core.Operation {
		t.Helper()
		args := map[string]any{"tool_id": tool, "arguments": map[string]any{"job_id": 42}, "idempotency_key": key}
		if extra {
			args["approval_policy"] = "none"
		}
		r, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "call_tool", Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		if r.IsError != wantError {
			t.Fatalf("error=%t content=%v", r.IsError, r.Content[0].(*mcp.TextContent).Text)
		}
		var op core.Operation
		if !wantError {
			if err = json.Unmarshal([]byte(r.Content[0].(*mcp.TextContent).Text), &op); err != nil {
				t.Fatal(err)
			}
		}
		return op
	}
	pending := call(write.ID, "mcp-write-pending", false, false)
	if pending.State != core.StateWaitingApproval {
		t.Fatal(pending)
	}
	call(write.ID, "mcp-bypass-attempt", true, true)
	write = policyUpdate(t, f, write, core.ApprovalNone)
	done := call(write.ID, "mcp-exempt-write", false, false)
	if done.State != core.StateSucceeded || done.ApprovedBy != "" {
		t.Fatal(done)
	}
	if again := call(write.ID, "mcp-exempt-write", false, false); again.ID != done.ID || adapter.calls.Load() != 2 {
		t.Fatal(again)
	}
	if _, err = f.svc.SetToolEnabled(ctx, f.admin, read.ID, false); err != nil {
		t.Fatal(err)
	}
	call(read.ID, "mcp-disabled-key", true, false)
}
