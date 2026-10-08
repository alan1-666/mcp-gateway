package upstreams

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/alan1-666/mcp-gateway/internal/store/postgres"
)

func TestObservedHealthAndNearestRankPercentiles(t *testing.T) {
	now := time.Now().UTC()
	server := core.MCPServer{Enabled: true}
	samples := []executionSample{}
	for i := 1; i <= 20; i++ {
		samples = append(samples, executionSample{now.Add(time.Duration(i-20) * time.Second), core.StateSucceeded, core.MCPObservation{Code: "ok", TotalMS: int64(i * 10), CallAttempted: true, PhasesMS: core.MCPPhaseDurations{Call: int64(i * 10)}}})
	}
	out := ExecutionMetrics{ObservedAt: now, States: map[core.State]int{}, Errors: map[string]int{}}
	aggregateExecutions(&out, server, samples)
	if out.Calls != 20 || out.Attempted != 20 || out.P50MS != 100 || out.P95MS != 190 || out.MeanPhasesMS.Call != 105 || out.Health != "healthy" {
		t.Fatalf("metrics %+v", out)
	}
	for _, tc := range []struct{ code, health string }{{"timeout", "degraded"}, {"connection_failed", "degraded"}, {"upstream_unavailable", "degraded"}, {"rate_limited", "degraded"}, {"call_unconfirmed", "degraded"}, {"authentication_rejected", "attention"}, {"cancelled", "attention"}} {
		out = ExecutionMetrics{ObservedAt: now, States: map[core.State]int{}, Errors: map[string]int{}}
		aggregateExecutions(&out, server, []executionSample{{now, core.StateUnknown, core.MCPObservation{Code: tc.code}}})
		if out.Health != tc.health || out.Errors[tc.code] != 1 || out.States[core.StateUnknown] != 1 {
			t.Fatal(out)
		}
	}
	for _, tc := range []struct {
		enabled     bool
		at, updated time.Time
		health      string
	}{
		{false, now, time.Time{}, "disabled"}, {true, time.Time{}, time.Time{}, "unobserved"}, {true, now.Add(-6 * time.Minute), time.Time{}, "stale"}, {true, now.Add(-time.Second), now, "stale"},
	} {
		out = ExecutionMetrics{ObservedAt: now, States: map[core.State]int{}, Errors: map[string]int{}}
		var sample []executionSample
		if !tc.at.IsZero() {
			sample = []executionSample{{tc.at, core.StateSucceeded, core.MCPObservation{Code: "ok"}}}
		}
		aggregateExecutions(&out, core.MCPServer{Enabled: tc.enabled, UpdatedAt: tc.updated}, sample)
		if out.Health != tc.health {
			t.Fatal(out.Health, tc.health)
		}
	}
}

func TestExecutionObservationPersistenceScopeReplayAndSampleBound(t *testing.T) {
	ctx, pool, svc, _, actor := fixture(t)
	server := server(t, ctx, svc, actor)
	page, err := svc.Discover(ctx, actor, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	tool, err := svc.Import(ctx, actor, server.ID, ImportInput{ToolName: page.Items[0].Name, SchemaHash: page.Items[0].SchemaHash, Risk: core.RiskRead})
	if err != nil {
		t.Fatal(err)
	}
	runtime := core.NewService(postgres.New(pool))
	tool, err = runtime.PublishTool(ctx, actor, tool.ID)
	if err != nil {
		t.Fatal(err)
	}
	op, err := runtime.Prepare(ctx, actor, core.PrepareInput{ToolID: tool.ID, Arguments: json.RawMessage(`{}`), IdempotencyKey: "observed"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, claimed, err := runtime.Claim(ctx, actor, op.ID); err != nil || !claimed {
		t.Fatal(err, claimed)
	}
	bad := &core.MCPObservation{Code: "PRIVATE", Phase: "call"}
	if _, err := runtime.Finish(ctx, actor, op.ID, core.FinishInput{State: core.StateFailed, MCPObservation: bad}); !errors.Is(err, core.ErrInvalid) {
		t.Fatal("invalid telemetry accepted", err)
	}
	current, err := runtime.GetOperation(ctx, actor, op.ID)
	if err != nil || current.State != core.StateDispatching {
		t.Fatal("invalid trace modified state")
	}
	good := &core.MCPObservation{Code: "ok", Phase: "result", TotalMS: 40, PhasesMS: core.MCPPhaseDurations{Session: 10, Catalog: 10, Call: 20}, CallAttempted: true, HTTPStatus: 200}
	finish := core.FinishInput{State: core.StateSucceeded, Result: json.RawMessage(`{"content":[],"isError":false}`), MCPObservation: good}
	if _, err := runtime.Finish(ctx, actor, op.ID, finish); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Finish(ctx, actor, op.ID, finish); !errors.Is(err, core.ErrConflict) {
		t.Fatal("completion replay", err)
	}
	if _, _, claimed, err := runtime.Claim(ctx, actor, op.ID); err != nil || claimed {
		t.Fatal("claimed finished operation", err)
	}
	metrics, err := svc.ExecutionMetrics(ctx, actor, server.ID)
	if err != nil || metrics.Calls != 1 || metrics.Attempted != 1 || metrics.States[core.StateSucceeded] != 1 || metrics.Health != "healthy" || metrics.P95MS != 40 || metrics.OperationsScanned != 1 {
		t.Fatalf("metrics %+v err=%v", metrics, err)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM operation_events WHERE workspace_id=$1 AND operation_id=$2 AND data ? 'mcp_execution'`, actor.WorkspaceID, op.ID).Scan(&n); err != nil || n != 1 {
		t.Fatal("trace duplicated", n, err)
	}
	for _, role := range []core.Role{core.RoleOperator, core.RoleApprover, core.RoleViewer} {
		a := actor
		a.Role = role
		if _, err := svc.ExecutionMetrics(ctx, a, server.ID); !errors.Is(err, core.ErrForbidden) {
			t.Fatal("role leak", err)
		}
	}
	client := actor
	client.ClientID = "machine"
	if _, err := svc.ExecutionMetrics(ctx, client, server.ID); !errors.Is(err, core.ErrForbidden) {
		t.Fatal("client leaked metrics", err)
	}
	empty := actor
	empty.ID = ""
	if _, err := svc.ExecutionMetrics(ctx, empty, server.ID); !errors.Is(err, core.ErrUnauthorized) {
		t.Fatal(err)
	}
	foreign := actor
	foreign.WorkspaceID = core.NewID()
	if _, err := svc.ExecutionMetrics(ctx, foreign, server.ID); !errors.Is(err, core.ErrNotFound) {
		t.Fatal("workspace leak", err)
	}
	if _, err := svc.SetEnabled(ctx, actor, server.ID, false); err != nil {
		t.Fatal(err)
	}
	metrics, err = svc.ExecutionMetrics(ctx, actor, server.ID)
	if err != nil || metrics.Health != "disabled" {
		t.Fatal(metrics, err)
	}
	if _, err := svc.SetEnabled(ctx, actor, server.ID, true); err != nil {
		t.Fatal(err)
	}
	metrics, err = svc.ExecutionMetrics(ctx, actor, server.ID)
	if err != nil || metrics.Health != "stale" {
		t.Fatal("old configuration treated as current health", metrics, err)
	}
	// Historical operation creation and observations after the sample cutoff
	// cannot shift a supposedly bounded point-in-time report.
	_, err = pool.Exec(ctx, `UPDATE operation_events SET created_at=clock_timestamp()+interval '1 hour' WHERE workspace_id=$1 AND operation_id=$2 AND data ? 'mcp_execution'`, actor.WorkspaceID, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	metrics, err = svc.ExecutionMetrics(ctx, actor, server.ID)
	if err != nil || metrics.Calls != 0 {
		t.Fatal("future observation included", metrics, err)
	}
	_, err = pool.Exec(ctx, `UPDATE operation_events SET created_at=clock_timestamp() WHERE workspace_id=$1 AND operation_id=$2 AND data ? 'mcp_execution'`, actor.WorkspaceID, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `UPDATE operations SET created_at=clock_timestamp()-interval '25 hours' WHERE workspace_id=$1 AND id=$2`, actor.WorkspaceID, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	metrics, err = svc.ExecutionMetrics(ctx, actor, server.ID)
	if err != nil || metrics.Calls != 0 || metrics.OperationsScanned != 0 {
		t.Fatal("old operation included", metrics, err)
	}
	// Newer noncompleted operations consume the global bounded sample, proving
	// that a quiet server does not claim complete coverage or current health.
	_, err = pool.Exec(ctx, `INSERT INTO operations(workspace_id,id,tool_id,tool_name,tool_version,risk,actor_id,arguments,arguments_hash,idempotency_key,tool_snapshot,state)
 SELECT workspace_id,id||'-sample-'||g,tool_id,tool_name,tool_version,risk,actor_id,arguments,arguments_hash,idempotency_key||'-sample-'||g,tool_snapshot,'READY'
 FROM operations CROSS JOIN generate_series(1,1001) g WHERE workspace_id=$1 AND id=$2`, actor.WorkspaceID, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	metrics, err = svc.ExecutionMetrics(ctx, actor, server.ID)
	if err != nil || metrics.OperationsScanned != 1000 || !metrics.Truncated || metrics.Calls != 0 || metrics.Health != "unobserved" {
		t.Fatalf("unbounded sample %+v err=%v", metrics, err)
	}
}
