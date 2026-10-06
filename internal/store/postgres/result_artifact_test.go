package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/alan1-666/mcp-gateway/internal/clients"
	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/alan1-666/mcp-gateway/internal/store/postgres"
)

func artifactTool(t *testing.T, f fixture) (core.Tool, string) {
	t.Helper()
	ctx := context.Background()
	tool, server := f.policyTool(t, core.RiskRead)
	updated, err := f.svc.UpdateToolResponsePolicy(ctx, f.admin, tool.ID, core.ResponsePolicyUpdateInput{ExpectedVersion: tool.Version, ResponsePolicy: &core.ResponsePolicy{MaxBytes: 1024, Include: []string{"/id", "/name", "/password"}, Artifact: &core.ResultArtifactPolicy{MaxBytes: 32768, TTLSeconds: 60}}})
	if err != nil {
		t.Fatal(err)
	}
	tool, err = f.svc.PublishTool(ctx, f.admin, updated.ID)
	if err != nil {
		t.Fatal(err)
	}
	return tool, server
}
func artifactOperation(t *testing.T, f fixture, actor core.Actor, tool core.Tool) core.Operation {
	t.Helper()
	ctx := context.Background()
	op, err := f.svc.Prepare(ctx, actor, core.PrepareInput{ToolID: tool.ID, Arguments: json.RawMessage(`{}`), IdempotencyKey: core.NewID()})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, claimed, err := f.svc.Claim(ctx, actor, op.ID); err != nil || !claimed {
		t.Fatal("claim", err)
	}
	raw := json.RawMessage(`{"isError":false,"content":[{"type":"text","text":"unprojected-private"}],"structuredContent":{"id":90071992547409931234,"name":"` + strings.Repeat("中文", 1600) + `","password":"sensitive-value","excluded":"must-never-store"}}`)
	finished, err := f.svc.Finish(ctx, actor, op.ID, core.FinishInput{State: core.StateSucceeded, Result: raw})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(finished.Result), "中文") || !strings.Contains(string(finished.Result), "gateway_result_ref") {
		t.Fatal("result was not replaced with reference")
	}
	return finished
}
func TestResultArtifactRoundTripAccessExpiryAndCleanup(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	tool, server := artifactTool(t, f)
	op := artifactOperation(t, f, f.operator, tool)
	input := core.ResultReadInput{OperationID: op.ID, LimitBytes: 1024}
	var all strings.Builder
	for {
		p, err := f.svc.ReadResult(ctx, f.operator, input)
		if err != nil {
			t.Fatal(err)
		}
		if p.Format != "json_utf8" {
			t.Fatal(p)
		}
		all.WriteString(p.Chunk)
		input.Cursor = p.NextCursor
		if input.Cursor == "" {
			break
		}
	}
	text := all.String()
	if !strings.Contains(text, "90071992547409931234") || !strings.Contains(text, "[REDACTED]") || strings.Contains(text, "sensitive-value") || strings.Contains(text, "must-never-store") || strings.Contains(text, "unprojected-private") {
		t.Fatal("unsafe or inaccurate artifact")
	}
	for _, a := range []core.Actor{f.other, {ID: f.operator.ID, WorkspaceID: "other", Role: core.RoleOperator}} {
		if _, err := f.svc.ReadResult(ctx, a, input); !errors.Is(err, core.ErrNotFound) {
			t.Fatal("unauthorized page", err)
		}
	}
	for _, a := range []core.Actor{f.admin, f.approver} {
		if _, err := f.svc.ReadResult(ctx, a, input); err != nil {
			t.Fatal("reviewer cannot inspect", err)
		}
	}
	for _, q := range []string{`UPDATE tools SET enabled=false WHERE workspace_id=$1`, `UPDATE mcp_servers SET enabled=false WHERE workspace_id=$1`} {
		if _, err := f.pool.Exec(ctx, q, f.admin.WorkspaceID); err != nil {
			t.Fatal(err)
		}
		if _, err := f.svc.ReadResult(ctx, f.operator, input); !errors.Is(err, core.ErrNotFound) {
			t.Fatal("disabled result read", err)
		}
		f.pool.Exec(ctx, `UPDATE tools SET enabled=true WHERE workspace_id=$1`, f.admin.WorkspaceID)
		f.pool.Exec(ctx, `UPDATE mcp_servers SET enabled=true WHERE workspace_id=$1 AND id=$2`, f.admin.WorkspaceID, server)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE gateway_result_artifacts SET expires_at=clock_timestamp()-interval '1 second' WHERE workspace_id=$1`, f.admin.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.ReadResult(ctx, f.operator, input); !errors.Is(err, core.ErrNotFound) {
		t.Fatal("expired result read", err)
	}
	count, err := postgres.New(f.pool).CleanupResultArtifacts(ctx)
	if err != nil || count < 1 {
		t.Fatal("cleanup", count, err)
	}
}
func TestResultArtifactClientRevocationAndRotation(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	tool, _ := artifactTool(t, f)
	cs := clients.New(f.pool)
	issued, err := cs.Create(ctx, f.admin, clients.CreateInput{Name: "Artifact reader", Scopes: []string{clients.ScopeRead, clients.ScopeInvoke}, ToolIDs: []string{tool.ID}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		f.pool.Exec(context.Background(), `DELETE FROM gateway_clients WHERE workspace_id=$1`, f.admin.WorkspaceID)
	})
	a := core.Actor{ID: issued.Client.ID, ClientID: issued.Client.ID, ClientKeyID: issued.Client.KeyID, WorkspaceID: f.admin.WorkspaceID, Role: core.RoleOperator}
	op := artifactOperation(t, f, a, tool)
	input := core.ResultReadInput{OperationID: op.ID}
	if _, err := f.svc.ReadResult(ctx, a, input); err != nil {
		t.Fatal(err)
	}
	empty := []string{}
	updated, err := cs.Update(ctx, f.admin, a.ID, clients.UpdateInput{ExpectedVersion: issued.Client.Version, ToolIDs: &empty})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.ReadResult(ctx, a, input); !errors.Is(err, core.ErrNotFound) {
		t.Fatal("revoked grant read", err)
	}
	grants := []string{tool.ID}
	updated, err = cs.Update(ctx, f.admin, a.ID, clients.UpdateInput{ExpectedVersion: updated.Version, ToolIDs: &grants})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.ReadResult(ctx, a, input); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.Rotate(ctx, f.admin, a.ID, clients.RotateInput{ExpectedVersion: updated.Version}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.ReadResult(ctx, a, input); !errors.Is(err, core.ErrNotFound) {
		t.Fatal("rotated key read", err)
	}
}
func TestSmallArtifactResultStaysInline(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	tool, _ := artifactTool(t, f)
	op, err := f.svc.Prepare(ctx, f.operator, core.PrepareInput{ToolID: tool.ID, Arguments: json.RawMessage(`{}`), IdempotencyKey: core.NewID()})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = f.svc.Claim(ctx, f.operator, op.ID); err != nil {
		t.Fatal(err)
	}
	op, err = f.svc.Finish(ctx, f.operator, op.ID, core.FinishInput{State: core.StateSucceeded, Result: json.RawMessage(`{"isError":false,"content":[],"structuredContent":{"id":123,"name":"small","password":"secret"}}`)})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(op.Result), "gateway_result_ref") || strings.Contains(string(op.Result), "secret") {
		t.Fatal("small result wrong")
	}
	if _, err = f.svc.ReadResult(ctx, f.operator, core.ResultReadInput{OperationID: op.ID}); !errors.Is(err, core.ErrNotFound) {
		t.Fatal("unexpected artifact", err)
	}
}

func TestArtifactQuotaFailurePreservesWriteUncertainty(t *testing.T) {
	for _, risk := range []core.Risk{core.RiskRead, core.RiskWrite} {
		t.Run(string(risk), func(t *testing.T) {
			f := database(t)
			ctx := context.Background()
			tool, _ := artifactTool(t, f)
			first := artifactOperation(t, f, f.operator, tool)
			_, err := f.pool.Exec(ctx, `INSERT INTO operations(workspace_id,id,tool_id,tool_name,tool_version,risk,actor_id,arguments,arguments_hash,idempotency_key,tool_snapshot,state) SELECT workspace_id,'quota-'||i,tool_id,tool_name,tool_version,risk,actor_id,arguments,arguments_hash,'quota-'||i,tool_snapshot,'SUCCEEDED' FROM operations CROSS JOIN generate_series(1,999) i WHERE workspace_id=$1 AND id=$2`, f.admin.WorkspaceID, first.ID)
			if err != nil {
				t.Fatal(err)
			}
			_, err = f.pool.Exec(ctx, `INSERT INTO gateway_result_artifacts(workspace_id,operation_id,payload,sha256,expires_at) SELECT workspace_id,id,convert_to('{}','UTF8'),repeat('a',64),clock_timestamp()+interval '1 hour' FROM operations WHERE workspace_id=$1 AND id LIKE 'quota-%'`, f.admin.WorkspaceID)
			if err != nil {
				t.Fatal(err)
			}
			if risk == core.RiskWrite {
				if _, err = f.pool.Exec(ctx, `UPDATE tools SET risk='write',version=version+1,definition=jsonb_set(definition,'{risk}','"write"') WHERE workspace_id=$1 AND id=$2`, f.admin.WorkspaceID, tool.ID); err != nil {
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
			if _, _, _, err = f.svc.Claim(ctx, f.operator, op.ID); err != nil {
				t.Fatal(err)
			}
			raw := json.RawMessage(`{"isError":false,"content":[],"structuredContent":{"id":1,"name":"` + strings.Repeat("x", 2000) + `","password":"secret"}}`)
			out, err := f.svc.Finish(ctx, f.operator, op.ID, core.FinishInput{State: core.StateSucceeded, Result: raw})
			if err != nil {
				t.Fatal(err)
			}
			want := core.StateFailed
			if risk == core.RiskWrite {
				want = core.StateUnknown
			}
			if out.State != want || len(out.Result) != 0 || !strings.Contains(out.Error, "quota") {
				t.Fatal(out)
			}
			var count int
			if err = f.pool.QueryRow(ctx, `SELECT count(*) FROM gateway_result_artifacts WHERE workspace_id=$1`, f.admin.WorkspaceID).Scan(&count); err != nil || count != 1000 {
				t.Fatal("quota overflow", count, err)
			}
		})
	}
}
