package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/alan1-666/mcp-gateway/internal/store/postgres"
	"github.com/alan1-666/mcp-gateway/migrations"
	"github.com/jackc/pgx/v5/pgxpool"
)

type fixture struct {
	svc                              *core.Service
	pool                             *pgxpool.Pool
	admin, operator, approver, other core.Actor
}

func database(t *testing.T) fixture {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL integration tests")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := migrations.Migrate(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
	workspace := core.NewID()
	f := fixture{svc: core.NewService(postgres.New(pool)), pool: pool, admin: core.Actor{ID: "admin", WorkspaceID: workspace, Role: core.RoleAdmin}, operator: core.Actor{ID: "operator", WorkspaceID: workspace, Role: core.RoleOperator}, approver: core.Actor{ID: "approver", WorkspaceID: workspace, Role: core.RoleApprover}, other: core.Actor{ID: "another-operator", WorkspaceID: workspace, Role: core.RoleOperator}}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for _, table := range []string{"operation_events", "audit_events", "operations", "tools"} {
			if _, err := pool.Exec(ctx, "DELETE FROM "+table+" WHERE workspace_id=$1", workspace); err != nil {
				t.Errorf("cleanup %s: %v", table, err)
			}
		}
	})
	return f
}
func (f fixture) tool(t *testing.T, risk core.Risk) core.Tool {
	t.Helper()
	method := "GET"
	if risk == core.RiskWrite {
		method = "POST"
	}
	tool, err := f.svc.CreateTool(context.Background(), f.admin, core.ToolInput{Name: "tool_" + core.NewID(), Description: "Integration test service", Risk: risk, InputSchema: json.RawMessage(`{"type":"object","properties":{"job_id":{"type":"integer"}},"required":["job_id"],"additionalProperties":false}`), HTTP: core.HTTPConfig{URL: "https://example.com/jobs", Method: method}})
	if err != nil {
		t.Fatal(err)
	}
	tool, err = f.svc.PublishTool(context.Background(), f.admin, tool.ID)
	if err != nil {
		t.Fatal(err)
	}
	return tool
}
func (f fixture) prepare(t *testing.T, tool core.Tool, key string) core.Operation {
	t.Helper()
	op, err := f.svc.Prepare(context.Background(), f.operator, core.PrepareInput{ToolID: tool.ID, Arguments: json.RawMessage(`{"job_id":42}`), IdempotencyKey: key})
	if err != nil {
		t.Fatal(err)
	}
	return op
}

func TestApprovalAndConcurrentDispatch(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	tool := f.tool(t, core.RiskWrite)
	op := f.prepare(t, tool, "retry-job-42")
	if op.State != core.StateWaitingApproval {
		t.Fatalf("unexpected state %s", op.State)
	}
	selfAdmin := f.operator
	selfAdmin.Role = core.RoleAdmin
	if _, err := f.svc.Approve(ctx, selfAdmin, op.ID); !errors.Is(err, core.ErrForbidden) {
		t.Fatalf("self approval: %v", err)
	}
	if _, _, granted, err := f.svc.Claim(ctx, f.operator, op.ID); granted || !errors.Is(err, core.ErrConflict) {
		t.Fatalf("unapproved dispatch: %t %v", granted, err)
	}
	approved, err := f.svc.Approve(ctx, f.approver, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if approved.ApprovedBy != f.approver.ID || approved.ApprovalExpiresAt == nil || approved.State != core.StateReady {
		t.Fatalf("incomplete approval %+v", approved)
	}
	var claims atomic.Int32
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, snapshot, granted, err := f.svc.Claim(ctx, f.operator, op.ID)
			if err != nil {
				t.Error(err)
				return
			}
			if granted {
				claims.Add(1)
				if snapshot.ID != tool.ID || snapshot.HTTP.URL != tool.HTTP.URL {
					t.Error("snapshot changed")
				}
			}
		}()
	}
	wg.Wait()
	if claims.Load() != 1 {
		t.Fatalf("granted %d dispatches", claims.Load())
	}
	completed, err := f.svc.Finish(ctx, f.operator, op.ID, core.FinishInput{State: core.StateSucceeded, Result: json.RawMessage(`{"job_id":42,"retried":true}`)})
	if err != nil {
		t.Fatal(err)
	}
	if completed.State != core.StateSucceeded {
		t.Fatal(completed.State)
	}
	if _, _, granted, err := f.svc.Claim(ctx, f.operator, op.ID); err != nil || granted {
		t.Fatalf("completed replay dispatched: %t %v", granted, err)
	}
	events, err := f.svc.ListEvents(ctx, f.operator, op.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 4 {
		t.Fatalf("expected prepare, approval, dispatch, completion: %+v", events)
	}
	replayed, err := f.svc.ListEvents(ctx, f.operator, op.ID, events[1].ID)
	if err != nil || len(replayed) != 2 {
		t.Fatalf("event replay %d %v", len(replayed), err)
	}
	// A newly constructed service sees all committed state without process memory.
	restarted := core.NewService(postgres.New(f.pool))
	loaded, err := restarted.GetOperation(ctx, f.operator, op.ID)
	if err != nil || loaded.State != core.StateSucceeded {
		t.Fatalf("restart persistence: %+v %v", loaded, err)
	}
}

func TestIdempotencyAndWorkspaceIsolation(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	tool := f.tool(t, core.RiskRead)
	var wg sync.WaitGroup
	ids := make(chan string, 10)
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			op, err := f.svc.Prepare(ctx, f.operator, core.PrepareInput{ToolID: tool.ID, Arguments: json.RawMessage(`{"job_id":42}`), IdempotencyKey: "stable-job-key"})
			if err != nil {
				t.Error(err)
				return
			}
			ids <- op.ID
		}()
	}
	wg.Wait()
	close(ids)
	var id string
	for current := range ids {
		if id != "" && id != current {
			t.Fatal("duplicate operations for one key")
		}
		id = current
	}
	if id == "" {
		t.Fatal("no operation created")
	}
	if _, err := f.svc.Prepare(ctx, f.operator, core.PrepareInput{ToolID: tool.ID, Arguments: json.RawMessage(`{"job_id":43}`), IdempotencyKey: "stable-job-key"}); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("changed args reused key: %v", err)
	}
	if _, err := f.svc.Prepare(ctx, f.other, core.PrepareInput{ToolID: tool.ID, Arguments: json.RawMessage(`{"job_id":42}`), IdempotencyKey: "stable-job-key"}); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("another user reused key: %v", err)
	}
	if _, err := f.svc.GetOperation(ctx, f.other, id); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("other operator read: %v", err)
	}
	if _, err := f.svc.ListEvents(ctx, f.other, id, 0); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("other operator events: %v", err)
	}
	if _, _, granted, err := f.svc.Claim(ctx, f.other, id); granted || !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("other operator claim: %t %v", granted, err)
	}
	foreign := f.admin
	foreign.WorkspaceID = core.NewID()
	if _, err := f.svc.GetOperation(ctx, foreign, id); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("cross-workspace operation: %v", err)
	}
	if _, err := f.svc.GetTool(ctx, foreign, tool.ID); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("cross-workspace tool: %v", err)
	}
	if _, err := f.svc.Approve(ctx, foreign, id); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("cross-workspace approval: %v", err)
	}
	list, err := f.svc.ListOperations(ctx, foreign)
	if err != nil || len(list) != 0 {
		t.Fatalf("cross-workspace list: %v %v", list, err)
	}
	if _, err := f.svc.Prepare(ctx, f.operator, core.PrepareInput{ToolID: tool.ID, Arguments: json.RawMessage(`{"job_id":"bad"}`), IdempotencyKey: "invalid-args-key"}); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("schema ignored: %v", err)
	}
}

func TestDisableExpiryAndRecovery(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	tool := f.tool(t, core.RiskWrite)
	op := f.prepare(t, tool, "expired-approval-key")
	if _, err := f.svc.Approve(ctx, f.approver, op.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE operations SET approval_expires_at=clock_timestamp()-interval '1 second' WHERE workspace_id=$1 AND id=$2`, f.admin.WorkspaceID, op.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, granted, err := f.svc.Claim(ctx, f.operator, op.ID); granted || !errors.Is(err, core.ErrApprovalExpired) {
		t.Fatalf("expired approval: %t %v", granted, err)
	}
	stored, err := f.svc.GetOperation(ctx, f.operator, op.ID)
	if err != nil || stored.State != core.StateRejected {
		t.Fatalf("expiry was not persisted: %+v %v", stored, err)
	}
	op = f.prepare(t, tool, "disabled-tool-key")
	if _, err := f.svc.Approve(ctx, f.approver, op.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.SetToolEnabled(ctx, f.admin, tool.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, _, granted, err := f.svc.Claim(ctx, f.operator, op.ID); granted || !errors.Is(err, core.ErrConflict) {
		t.Fatalf("disabled tool dispatched: %t %v", granted, err)
	}
	if _, err := f.svc.GetTool(ctx, f.operator, tool.ID); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("disabled tool discoverable: %v", err)
	}
	if _, err := f.svc.SetToolEnabled(ctx, f.admin, tool.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, _, granted, err := f.svc.Claim(ctx, f.operator, op.ID); err != nil || !granted {
		t.Fatalf("claim: %t %v", granted, err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE operations SET updated_at=clock_timestamp()-interval '10 minutes' WHERE workspace_id=$1 AND id=$2`, f.admin.WorkspaceID, op.ID); err != nil {
		t.Fatal(err)
	}
	count, err := f.svc.Recover(ctx, 150*time.Second)
	if err != nil || count < 1 {
		t.Fatalf("recovery: %d %v", count, err)
	}
	stored, err = f.svc.GetOperation(ctx, f.operator, op.ID)
	if err != nil || stored.State != core.StateUnknown {
		t.Fatalf("recovery state: %+v %v", stored, err)
	}
	if _, _, granted, err := f.svc.Claim(ctx, f.operator, op.ID); err != nil || granted {
		t.Fatalf("unknown operation replayed: %t %v", granted, err)
	}
	if _, err := f.svc.Finish(ctx, f.operator, op.ID, core.FinishInput{State: core.StateSucceeded}); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("late executor overwrote unknown result: %v", err)
	}
}
