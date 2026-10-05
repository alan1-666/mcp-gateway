package capacity

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
	"github.com/alan1-666/mcp-gateway/migrations"
	"github.com/jackc/pgx/v5/pgxpool"
)

func fixture(t *testing.T) (*Service, *pgxpool.Pool, core.Actor) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required for PostgreSQL capacity fixture")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err = migrations.Migrate(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
	actor := core.Actor{ID: "admin", Role: core.RoleAdmin, WorkspaceID: core.NewID()}
	t.Cleanup(func() {
		for _, table := range []string{"capacity_leases", "capacity_windows", "capacity_call_metrics", "capacity_rejection_metrics", "capacity_limits", "agent_run_intents", "agent_run_operations", "agent_run_events", "agent_runs", "operation_events", "audit_events", "operations", "tool_versions", "tools", "gateway_clients"} {
			if _, err := pool.Exec(context.Background(), "DELETE FROM "+table+" WHERE workspace_id=$1", actor.WorkspaceID); err != nil {
				t.Error(table, err)
			}
		}
	})
	return New(pool), pool, actor
}
func operation(t *testing.T, pool *pgxpool.Pool, actor core.Actor, host string) string {
	t.Helper()
	tool := core.Tool{ID: core.NewID(), WorkspaceID: actor.WorkspaceID, Name: core.NewID(), Risk: core.RiskRead, HTTP: core.HTTPConfig{URL: "https://" + host + "/jobs", Method: "GET", TimeoutMS: 1000}}
	raw, _ := json.Marshal(tool)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO tools(workspace_id,id,name,risk,definition)VALUES($1,$2,$3,'read',$4)`, actor.WorkspaceID, tool.ID, tool.Name, raw); err != nil {
		t.Fatal(err)
	}
	id := core.NewID()
	_, err := pool.Exec(ctx, `INSERT INTO operations(workspace_id,id,tool_id,tool_name,tool_version,risk,actor_id,arguments,arguments_hash,idempotency_key,tool_snapshot,state)VALUES($1,$2,$3,$4,1,'read',$5,'{}','hash',$2,$6,'READY')`, actor.WorkspaceID, id, tool.ID, tool.Name, actor.ID, raw)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
func set(t *testing.T, s *Service, a core.Actor, scope, id string, concurrent, rpm int) {
	t.Helper()
	if _, err := s.Update(context.Background(), a, UpdateInput{Scope: scope, ScopeID: id, MaxConcurrent: concurrent, RequestsPerMinute: rpm}); err != nil {
		t.Fatal(err)
	}
}
func TestValidationAndPermissions(t *testing.T) {
	for _, in := range []UpdateInput{{Scope: "workspace", ScopeID: "wrong", MaxConcurrent: 1, RequestsPerMinute: 2}, {Scope: "workspace", ScopeID: "*", MaxConcurrent: 0, RequestsPerMinute: 2}, {Scope: "upstream", ScopeID: "anything", MaxConcurrent: 1, RequestsPerMinute: 2}} {
		if validate(in) == nil {
			t.Fatal("accepted invalid limit")
		}
	}
	for _, a := range []core.Actor{{ID: "x", WorkspaceID: "w", Role: core.RoleViewer}, {ID: "x", WorkspaceID: "w", Role: core.RoleAdmin, ClientID: "x"}} {
		if !errors.Is(authorize(a), core.ErrForbidden) {
			t.Fatal("permission bypass")
		}
	}
}
func TestSharedAdmissionLeaseOwnershipAndExpiry(t *testing.T) {
	s, p, a := fixture(t)
	ctx := context.Background()
	set(t, s, a, "workspace", "*", 3, 600)
	second := New(p)
	ids := []string{}
	for range 16 {
		ids = append(ids, operation(t, p, a, "example.com"))
	}
	var admitted atomic.Int32
	var wg sync.WaitGroup
	leases := make(chan *Lease, 16)
	for i, id := range ids {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			service := s
			if i%2 == 0 {
				service = second
			}
			l, err := service.Acquire(ctx, a, id)
			if err == nil {
				admitted.Add(1)
				leases <- l
			} else {
				var limit *LimitError
				if !errors.As(err, &limit) {
					t.Error(err)
				}
			}
		}(i, id)
	}
	wg.Wait()
	close(leases)
	if admitted.Load() != 3 {
		t.Fatalf("admitted=%d", admitted.Load())
	}
	first := <-leases
	if _, err := second.Acquire(ctx, a, first.operationID); err == nil {
		t.Fatal("duplicate owner acquired")
	}
	// A stale owner's release cannot release another lease for the same operation.
	if _, err := p.Exec(ctx, `UPDATE capacity_leases SET expires_at=clock_timestamp()-interval '1 second' WHERE workspace_id=$1 AND operation_id=$2`, a.WorkspaceID, first.operationID); err != nil {
		t.Fatal(err)
	}
	replacement, err := second.Acquire(ctx, a, first.operationID)
	if err != nil {
		t.Fatal(err)
	}
	if err = first.Release(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = p.QueryRow(ctx, `SELECT count(*) FROM capacity_leases WHERE workspace_id=$1 AND operation_id=$2`, a.WorkspaceID, first.operationID).Scan(&count); err != nil || count != 1 {
		t.Fatal("stale release removed replacement", count, err)
	}
	if err = replacement.Release(ctx); err != nil {
		t.Fatal(err)
	}
}
func TestRateBudgetIndependentScopesAndConflict(t *testing.T) {
	s, p, a := fixture(t)
	ctx := context.Background()
	set(t, s, a, "upstream", "http:https://SLOW.example:443", 1, 1)
	id := operation(t, p, a, "slow.example")
	lease, err := s.Acquire(ctx, a, id)
	if err != nil {
		t.Fatal(err)
	}
	if err = lease.Release(ctx); err != nil {
		t.Fatal(err)
	}
	_, err = s.Acquire(ctx, a, operation(t, p, a, "slow.example"))
	var limit *LimitError
	if !errors.As(err, &limit) || limit.Scope != "upstream" || limit.Reason != "rate" || limit.RetryAfterSeconds < 1 || limit.RetryAfterSeconds > 60 {
		t.Fatal(err)
	}
	if _, err = s.Acquire(ctx, a, operation(t, p, a, "other.example")); err != nil {
		t.Fatal("independent upstream throttled", err)
	}
	if _, err = s.Update(ctx, a, UpdateInput{Scope: "upstream", ScopeID: "http:https://slow.example", ExpectedVersion: 0, MaxConcurrent: 5, RequestsPerMinute: 10}); !errors.Is(err, core.ErrConflict) {
		t.Fatal(err)
	}
	if err = s.Observe(ctx, a, core.Tool{}, core.StateSucceeded, 12*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	metrics, err := s.Metrics(ctx, a)
	if err != nil || len(metrics.Calls) != 1 || metrics.Calls[0].DurationMSTotal != 12 || len(metrics.Rejections) != 1 {
		t.Fatalf("%+v %v", metrics, err)
	}
	other := a
	other.WorkspaceID = core.NewID()
	m, err := s.Metrics(ctx, other)
	if err != nil || len(m.Calls) != 0 {
		t.Fatal("workspace leakage", err)
	}
}
func TestAdminDispatchChargesOriginalClient(t *testing.T) {
	s, p, a := fixture(t)
	ctx := context.Background()
	client := a
	client.ID = core.NewID()
	client.ClientID = client.ID
	client.Role = core.RoleOperator
	_, err := p.Exec(ctx, `INSERT INTO gateway_clients(id,workspace_id,name,key_id,token_hash,key_expires_at)VALUES($1,$2,$1,$1,decode(repeat('ab',32),'hex'),clock_timestamp()+interval '1 day')`, client.ID, a.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	set(t, s, a, "client", client.ID, 1, 120)
	if _, err = s.Acquire(ctx, a, operation(t, p, client, "one.example")); err != nil {
		t.Fatal(err)
	}
	_, err = s.Acquire(ctx, a, operation(t, p, client, "two.example"))
	var limited *LimitError
	if !errors.As(err, &limited) || limited.Scope != "client" {
		t.Fatal("client limit bypassed", err)
	}
}
func TestRetentionProtectsUnresolvedAndSeparatesAudit(t *testing.T) {
	s, p, a := fixture(t)
	ctx := context.Background()
	states := []string{"SUCCEEDED", "FAILED", "REJECTED", "UNKNOWN", "READY", "DISPATCHING", "SUCCEEDED"}
	ids := []string{}
	for _, state := range states {
		id := operation(t, p, a, "example.com")
		ids = append(ids, id)
		if _, err := p.Exec(ctx, `UPDATE operations SET state=$3,updated_at=clock_timestamp()-interval '40 days' WHERE workspace_id=$1 AND id=$2`, a.WorkspaceID, id, state); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := p.Exec(ctx, `INSERT INTO operation_events(workspace_id,operation_id,type,actor_id)VALUES($1,$2,'OPERATION_UNKNOWN','system')`, a.WorkspaceID, ids[6]); err != nil {
		t.Fatal(err)
	}
	for _, days := range []int{40, 200} {
		if _, err := p.Exec(ctx, `INSERT INTO audit_events(workspace_id,actor_id,action,resource_id,created_at)VALUES($1,'a','test','r',clock_timestamp()-($2*interval '1 day'))`, a.WorkspaceID, days); err != nil {
			t.Fatal(err)
		}
	}
	cfg := DefaultRetentionConfig()
	cfg.BatchSize = 2
	out, err := s.Retain(ctx, cfg)
	if err != nil || out.Operations != 2 || out.AuditEvents != 1 {
		t.Fatalf("%+v %v", out, err)
	}
	_, err = s.Retain(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err = p.QueryRow(ctx, `SELECT count(*) FROM operations WHERE workspace_id=$1`, a.WorkspaceID).Scan(&remaining); err != nil || remaining != 4 {
		t.Fatal("unsafe retention", remaining, err)
	}
}

func TestRetentionKeepsUnfinishedRunGraph(t *testing.T) {
	s, p, a := fixture(t)
	ctx := context.Background()
	user := core.NewID()
	if _, err := p.Exec(ctx, `INSERT INTO gateway_users(id,workspace_id,username,password_hash,role)VALUES($1,$2,$1,'fixture','operator')`, user, a.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = p.Exec(ctx, `DELETE FROM gateway_users WHERE id=$1`, user) })
	// Cleanup callbacks are LIFO: delete the graph before its user.
	t.Cleanup(func() {
		for _, table := range []string{"agent_run_intents", "agent_run_operations", "agent_run_events", "agent_runs"} {
			_, _ = p.Exec(ctx, "DELETE FROM "+table+" WHERE workspace_id=$1", a.WorkspaceID)
		}
	})
	for _, state := range []string{"SUCCEEDED", "NEEDS_REVIEW"} {
		operationID := operation(t, p, a, "example.com")
		runID := core.NewID()
		if _, err := p.Exec(ctx, `UPDATE operations SET state='SUCCEEDED',updated_at=clock_timestamp()-interval '40 days' WHERE workspace_id=$1 AND id=$2`, a.WorkspaceID, operationID); err != nil {
			t.Fatal(err)
		}
		if _, err := p.Exec(ctx, `INSERT INTO agent_runs(workspace_id,id,actor_id,prompt,idempotency_key,state,updated_at)VALUES($1,$2,$3,'fixture',$2,$4,clock_timestamp()-interval '40 days')`, a.WorkspaceID, runID, user, state); err != nil {
			t.Fatal(err)
		}
		if _, err := p.Exec(ctx, `INSERT INTO agent_run_operations(workspace_id,run_id,operation_id)VALUES($1,$2,$3)`, a.WorkspaceID, runID, operationID); err != nil {
			t.Fatal(err)
		}
		if _, err := p.Exec(ctx, `INSERT INTO agent_run_intents(workspace_id,run_id,idempotency_key,intent_hash,operation_id)VALUES($1,$2,'fixture','hash',$3)`, a.WorkspaceID, runID, operationID); err != nil {
			t.Fatal(err)
		}
		if _, err := p.Exec(ctx, `INSERT INTO agent_run_events(workspace_id,run_id,type)VALUES($1,$2,'fixture')`, a.WorkspaceID, runID); err != nil {
			t.Fatal(err)
		}
	}
	out, err := s.Retain(ctx, DefaultRetentionConfig())
	if err != nil || out.Runs != 1 || out.Operations != 1 {
		t.Fatalf("%+v %v", out, err)
	}
	for _, table := range []string{"operations", "agent_runs", "agent_run_operations", "agent_run_intents", "agent_run_events"} {
		var count int
		if err = p.QueryRow(ctx, "SELECT count(*) FROM "+table+" WHERE workspace_id=$1", a.WorkspaceID).Scan(&count); err != nil || count != 1 {
			t.Fatal(table, count, err)
		}
	}
}
