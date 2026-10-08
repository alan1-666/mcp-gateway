package postgres_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/alan1-666/mcp-gateway/internal/capacity"
	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/alan1-666/mcp-gateway/internal/execution"
)

type artifactMetricsAdapter struct{ calls int }

func (a *artifactMetricsAdapter) Execute(context.Context, core.Actor, core.Tool, core.Operation) core.FinishInput {
	a.calls++
	return core.FinishInput{MCPObservation: &core.MCPObservation{Code: "ok", Phase: "result", CallAttempted: true}, State: core.StateSucceeded, Result: json.RawMessage(`{"isError":false,"content":[],"structuredContent":{"id":1,"name":"` + strings.Repeat("x", 2000) + `","password":"secret"}}`)}
}

// The adapter succeeds, but finishing may fail result retention. Metrics must
// reflect the durable business state and never count replay as another dispatch.
func TestArtifactQuotaMetricsUseDurableOutcome(t *testing.T) {
	for _, risk := range []core.Risk{core.RiskRead, core.RiskWrite} {
		t.Run(string(risk), func(t *testing.T) {
			f := database(t)
			ctx := context.Background()
			tool, _ := artifactTool(t, f)
			seed := artifactOperation(t, f, f.operator, tool)
			_, err := f.pool.Exec(ctx, `INSERT INTO operations(workspace_id,id,tool_id,tool_name,tool_version,risk,actor_id,arguments,arguments_hash,idempotency_key,tool_snapshot,state) SELECT workspace_id,'metrics-quota-'||i,tool_id,tool_name,tool_version,risk,actor_id,arguments,arguments_hash,'metrics-quota-'||i,tool_snapshot,'SUCCEEDED' FROM operations CROSS JOIN generate_series(1,999) i WHERE workspace_id=$1 AND id=$2`, f.admin.WorkspaceID, seed.ID)
			if err != nil {
				t.Fatal(err)
			}
			_, err = f.pool.Exec(ctx, `INSERT INTO gateway_result_artifacts(workspace_id,operation_id,payload,sha256,expires_at) SELECT workspace_id,id,convert_to('{}','UTF8'),repeat('a',64),clock_timestamp()+interval '1 hour' FROM operations WHERE workspace_id=$1 AND id LIKE 'metrics-quota-%'`, f.admin.WorkspaceID)
			if err != nil {
				t.Fatal(err)
			}
			if risk == core.RiskWrite {
				_, err = f.pool.Exec(ctx, `UPDATE tools SET risk='write',version=version+1,definition=jsonb_set(definition,'{risk}','"write"') WHERE workspace_id=$1 AND id=$2`, f.admin.WorkspaceID, tool.ID)
				if err != nil {
					t.Fatal(err)
				}
			}
			op, err := f.svc.Prepare(ctx, f.operator, core.PrepareInput{ToolID: tool.ID, Arguments: json.RawMessage(`{}`), IdempotencyKey: core.NewID()})
			if err != nil {
				t.Fatal(err)
			}
			if risk == core.RiskWrite {
				if _, err = f.svc.Approve(ctx, f.approver, op.ID); err != nil {
					t.Fatal(err)
				}
			}
			budgets := capacity.New(f.pool)
			t.Cleanup(func() {
				for _, table := range []string{"capacity_call_metrics", "capacity_rejection_metrics", "capacity_leases", "capacity_windows", "capacity_limits"} {
					if _, err := f.pool.Exec(context.Background(), "DELETE FROM "+table+" WHERE workspace_id=$1", f.admin.WorkspaceID); err != nil {
						t.Error(err)
					}
				}
			})
			adapter := &artifactMetricsAdapter{}
			executor := execution.Executor{Service: f.svc, Adapter: adapter, Capacity: budgets}
			want := core.StateFailed
			if risk == core.RiskWrite {
				want = core.StateUnknown
			}
			for i := 0; i < 2; i++ {
				out, err := executor.Execute(ctx, f.operator, op.ID)
				if err != nil || out.State != want || !strings.Contains(out.Error, "quota") {
					t.Fatalf("durable quota outcome %+v %v", out, err)
				}
			}
			var observed int
			if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM operation_events WHERE workspace_id=$1 AND operation_id=$2 AND data->'mcp_execution'->>'code'='artifact_quota' AND data->'mcp_execution'->>'call_attempted'='true'`, f.admin.WorkspaceID, op.ID).Scan(&observed); err != nil || observed != 1 {
				t.Fatal("quota trace lost or duplicated", observed, err)
			}
			metrics, err := budgets.Metrics(ctx, f.admin)
			if err != nil {
				t.Fatal(err)
			}
			if adapter.calls != 1 || len(metrics.Calls) != 1 || metrics.Calls[0].State != want || metrics.Calls[0].Count != 1 || metrics.Calls[0].Transport != "mcp" || metrics.ActiveLeases != 0 {
				t.Fatalf("adapter calls=%d metrics=%+v", adapter.calls, metrics)
			}
		})
	}
}
