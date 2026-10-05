package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/alan1-666/mcp-gateway/internal/execution"
	"github.com/alan1-666/mcp-gateway/internal/identity"
	"github.com/alan1-666/mcp-gateway/internal/transport/httpapi"
)

func (f fixture) policyTool(t *testing.T, risk core.Risk) (core.Tool, string) {
	t.Helper()
	ctx := context.Background()
	serverID := core.NewID()
	_, err := f.pool.Exec(ctx, `INSERT INTO mcp_servers(workspace_id,id,name,namespace,url,timeout_ms) VALUES($1,$2,'Policy fixture',$2,'https://example.com/mcp',10000)`, f.admin.WorkspaceID, serverID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := f.pool.Exec(context.Background(), `DELETE FROM mcp_servers WHERE workspace_id=$1 AND id=$2`, f.admin.WorkspaceID, serverID); err != nil {
			t.Error(err)
		}
	})
	tool, err := f.svc.CreateTool(ctx, f.admin, core.ToolInput{Name: "policy_" + core.NewID(), Description: "Policy version fixture", Risk: risk, InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`), OutputSchema: json.RawMessage(`{"type":"object","required":["id","name"],"properties":{"id":{"type":"integer"},"name":{"type":"string"}}}`), MCP: &core.MCPConfig{ServerID: serverID, ToolName: "get_report", SchemaHash: strings.Repeat("a", 64)}, ResponsePolicy: &core.ResponsePolicy{Include: []string{"/id"}, MaxBytes: 4096}})
	if err != nil {
		t.Fatal(err)
	}
	return tool, serverID
}
func TestPolicyUpdatePreservesToolStateAndRejectsStaleNoop(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	tool, serverID := f.policyTool(t, core.RiskRead)
	input := core.ResponsePolicyUpdateInput{ExpectedVersion: 1, ResponsePolicy: &core.ResponsePolicy{Include: []string{"/name", "/id"}, MaxBytes: 8192}}
	updated, err := f.svc.UpdateToolResponsePolicy(ctx, f.admin, tool.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Version != 2 || updated.Enabled || updated.Status != "draft" || updated.MCP.SchemaHash != tool.MCP.SchemaHash || updated.Name != tool.Name || updated.CreatedAt != tool.CreatedAt {
		t.Fatalf("tool changed unexpectedly %+v", updated)
	}
	if len(updated.ResponsePolicy.Include) != 2 || updated.ResponsePolicy.Include[0] != "/id" {
		t.Fatalf("policy not normalized %+v", updated.ResponsePolicy)
	}
	if _, err := f.svc.UpdateToolResponsePolicy(ctx, f.admin, tool.ID, input); !errors.Is(err, core.ErrConflict) {
		t.Fatal("stale same-policy update accepted", err)
	}
	input.ExpectedVersion = 2
	noop, err := f.svc.UpdateToolResponsePolicy(ctx, f.admin, tool.ID, input)
	if err != nil || noop.Version != 2 {
		t.Fatal("no-op version increment", noop.Version, err)
	}
	if _, err := f.svc.PublishTool(ctx, f.admin, tool.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE mcp_servers SET enabled=false WHERE workspace_id=$1 AND id=$2`, f.admin.WorkspaceID, serverID); err != nil {
		t.Fatal(err)
	}
	input.ResponsePolicy = &core.ResponsePolicy{Include: []string{"/name"}, MaxBytes: 8192}
	disabled, err := f.svc.UpdateToolResponsePolicy(ctx, f.admin, tool.ID, input)
	if err != nil || disabled.Version != 3 || disabled.Enabled || disabled.Status != "published" {
		t.Fatalf("disabled repair %+v %v", disabled, err)
	}
	var ownEnabled bool
	if err := f.pool.QueryRow(ctx, `SELECT enabled FROM tools WHERE workspace_id=$1 AND id=$2`, f.admin.WorkspaceID, tool.ID).Scan(&ownEnabled); err != nil || !ownEnabled {
		t.Fatal("own enabled flag altered", err)
	}
	var count int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE workspace_id=$1 AND resource_id=$2 AND action='TOOL_RESPONSE_POLICY_UPDATED'`, f.admin.WorkspaceID, tool.ID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("audit count %d %v", count, err)
	}
	var audit []byte
	if err := f.pool.QueryRow(ctx, `SELECT data FROM audit_events WHERE workspace_id=$1 AND resource_id=$2 AND action='TOOL_RESPONSE_POLICY_UPDATED' AND data->>'new_version'='3'`, f.admin.WorkspaceID, tool.ID).Scan(&audit); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(audit), `"old_version": 2`) || !strings.Contains(string(audit), `"new_version": 3`) || !strings.Contains(string(audit), `"/name"`) {
		t.Fatalf("incomplete audit %s", audit)
	}
	var policyAudit struct {
		Old *core.ResponsePolicy `json:"old_response_policy"`
		New *core.ResponsePolicy `json:"response_policy"`
	}
	if err := json.Unmarshal(audit, &policyAudit); err != nil || policyAudit.Old == nil || policyAudit.New == nil || len(policyAudit.Old.Include) != 2 || len(policyAudit.New.Include) != 1 {
		t.Fatalf("audit lacks before/after policy %s %v", audit, err)
	}
	foreign := f.admin
	foreign.WorkspaceID = core.NewID()
	if _, err := f.svc.UpdateToolResponsePolicy(ctx, foreign, tool.ID, core.ResponsePolicyUpdateInput{ExpectedVersion: 3, ResponsePolicy: &core.ResponsePolicy{}}); !errors.Is(err, core.ErrNotFound) {
		t.Fatal("foreign workspace updated", err)
	}
	httpTool := f.tool(t, core.RiskRead)
	if _, err := f.svc.UpdateToolResponsePolicy(ctx, f.admin, httpTool.ID, core.ResponsePolicyUpdateInput{ExpectedVersion: 1, ResponsePolicy: &core.ResponsePolicy{}}); !errors.Is(err, core.ErrInvalid) {
		t.Fatal("HTTP tool policy accepted", err)
	}
}
func TestPolicyConcurrentUpdateAndPublication(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	tool, _ := f.policyTool(t, core.RiskRead)
	var winners, conflicts atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := f.svc.UpdateToolResponsePolicy(ctx, f.admin, tool.ID, core.ResponsePolicyUpdateInput{ExpectedVersion: 1, ResponsePolicy: &core.ResponsePolicy{MaxBytes: 8192 + i}})
			if err == nil {
				winners.Add(1)
			} else if errors.Is(err, core.ErrConflict) {
				conflicts.Add(1)
			} else {
				t.Error(err)
			}
		}(i)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		if _, err := f.svc.PublishTool(ctx, f.admin, tool.ID); err != nil {
			t.Error(err)
		}
	}()
	wg.Wait()
	got, err := f.svc.GetTool(ctx, f.admin, tool.ID)
	if err != nil || winners.Load() != 1 || conflicts.Load() != 11 || got.Version != 2 || !got.Enabled || got.Status != "published" {
		t.Fatalf("concurrent state %+v winners=%d conflicts=%d err=%v", got, winners.Load(), conflicts.Load(), err)
	}
}

type policyFixtureAdapter struct{}

func (policyFixtureAdapter) Execute(context.Context, core.Actor, core.Tool, core.Operation) core.FinishInput {
	return core.FinishInput{State: core.StateSucceeded, Result: json.RawMessage(`{"isError":false,"content":[],"structuredContent":{"id":9007199254740993123,"name":"latest","private":"unselected"}}`)}
}
func TestPolicyUpdateLeavesApprovedSnapshotsImmutable(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	tool, _ := f.policyTool(t, core.RiskWrite)
	if _, err := f.svc.PublishTool(ctx, f.admin, tool.ID); err != nil {
		t.Fatal(err)
	}
	prepare := func() core.Operation {
		op, err := f.svc.Prepare(ctx, f.operator, core.PrepareInput{ToolID: tool.ID, Arguments: json.RawMessage(`{}`), IdempotencyKey: core.NewID()})
		if err != nil {
			t.Fatal(err)
		}
		return op
	}
	old := prepare()
	approved, err := f.svc.Approve(ctx, f.approver, old.ID)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := f.svc.UpdateToolResponsePolicy(ctx, f.admin, tool.ID, core.ResponsePolicyUpdateInput{ExpectedVersion: 1, ResponsePolicy: &core.ResponsePolicy{Include: []string{"/name"}, MaxBytes: 4096}})
	if err != nil || updated.Version != 2 {
		t.Fatal(err)
	}
	oldAfter, err := f.svc.GetOperation(ctx, f.operator, old.ID)
	if err != nil || oldAfter.State != core.StateReady || oldAfter.ToolVersion != 1 || oldAfter.ApprovedBy != approved.ApprovedBy || !oldAfter.ApprovalExpiresAt.Equal(*approved.ApprovalExpiresAt) || oldAfter.ArgumentsHash != old.ArgumentsHash {
		t.Fatalf("old approval changed %+v %v", oldAfter, err)
	}
	newer := prepare()
	if newer.ToolVersion != 2 || newer.State != core.StateWaitingApproval {
		t.Fatalf("new prepare %+v", newer)
	}
	if _, err := f.svc.Approve(ctx, f.approver, newer.ID); err != nil {
		t.Fatal(err)
	}
	executor := execution.Executor{Service: f.svc, Adapter: policyFixtureAdapter{}}
	oldResult, err := executor.Execute(ctx, f.operator, old.ID)
	if err != nil || oldResult.State != core.StateSucceeded || !strings.Contains(string(oldResult.Result), "9007199254740993123") || strings.Contains(string(oldResult.Result), "latest") {
		t.Fatalf("old policy not used %+v %v", oldResult, err)
	}
	newResult, err := executor.Execute(ctx, f.operator, newer.ID)
	if err != nil || newResult.State != core.StateSucceeded || !strings.Contains(string(newResult.Result), "latest") || strings.Contains(string(newResult.Result), "9007199254740993123") {
		t.Fatalf("new policy not used %+v %v", newResult, err)
	}
}
func TestResponsePolicyHTTPContractAndPreviewPrivacy(t *testing.T) {
	f := database(t)
	tool, _ := f.policyTool(t, core.RiskRead)
	adminToken, operatorToken, foreignToken := core.NewID(), core.NewID(), core.NewID()
	foreign := f.admin
	foreign.WorkspaceID = core.NewID()
	auth, err := identity.New([]identity.Token{{Token: adminToken, Actor: f.admin}, {Token: operatorToken, Actor: f.operator}, {Token: foreignToken, Actor: foreign}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := (&httpapi.API{Service: f.svc}).Handler(auth)
	call := func(token, suffix, body string, status int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/tools/"+tool.ID+"/response-policy"+suffix, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != status {
			t.Fatalf("status %d want %d body %s", rec.Code, status, rec.Body.String())
		}
		return rec
	}
	preview := `{"expected_version":1,"response_policy":{"include":["/id"],"max_bytes":4096},"sample":{"isError":false,"content":[{"type":"text","text":"SAMPLE_NEVER_PERSIST_89"}],"structuredContent":{"id":9007199254740993123,"name":"SAMPLE_NEVER_PERSIST_89"}}}`
	var before int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_events WHERE workspace_id=$1`, f.admin.WorkspaceID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	rec := call(adminToken, "/preview", preview, 200)
	var result core.ResponsePolicyPreview
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil || result.ToolVersion != 1 || result.ProjectedBytes != len(result.Result) || strings.Contains(string(result.Result), "SAMPLE_NEVER_PERSIST_89") {
		t.Fatalf("bad preview %s %v", rec.Body.String(), err)
	}
	for _, suffix := range []string{"", "/preview"} {
		call("", suffix, preview, 401)
		body := `{"expected_version":1,"response_policy":{}}`
		if suffix != "" {
			body = preview
		}
		call(operatorToken, suffix, body, 403)
		call(foreignToken, suffix, body, 404)
	}
	for _, body := range []string{`{}`, `null`, `{"expected_version":0,"response_policy":{}}`, `{"expected_version":1,"response_policy":null}`, `{"expected_version":1,"response_policy":{},"sample":null}`, `{"expected_version":1,"response_policy":{},"sample":{}}`, `{"expected_version":1,"response_policy":{},"sample":{"isError":false,"content":[{"type":"image","data":"abc"}]}}`} {
		call(adminToken, "/preview", body, 400)
	}
	call(adminToken, "/preview", `{"expected_version":1,"response_policy":{},"sample":{"isError":false,"content":[{"type":"text","text":"`+strings.Repeat("a", 256<<10)+`"}]}}`, 400)
	call(adminToken, "/preview", strings.Replace(preview, `"expected_version":1`, `"expected_version":9`, 1), 409)
	var after int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_events WHERE workspace_id=$1`, f.admin.WorkspaceID).Scan(&after); err != nil || after != before {
		t.Fatalf("preview persisted audit %d vs %d %v", after, before, err)
	}
	var ops int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM operations WHERE workspace_id=$1`, f.admin.WorkspaceID).Scan(&ops); err != nil || ops != 0 {
		t.Fatal("preview created operation", err)
	}
	for _, body := range []string{`{}`, `{"expected_version":1}`, `{"expected_version":1,"response_policy":null}`, `{"expected_version":1.5,"response_policy":{}}`, `{"expected_version":1,"response_policy":{},"unknown":true}`} {
		call(adminToken, "", body, 400)
	}
	rec = call(adminToken, "", `{"expected_version":1,"response_policy":{"include":["/name"],"max_bytes":8192}}`, 200)
	var updated core.Tool
	if err := json.Unmarshal(rec.Body.Bytes(), &updated); err != nil || updated.Version != 2 {
		t.Fatal(fmt.Sprintf("update response %s %v", rec.Body.String(), err))
	}
	call(adminToken, "", `{"expected_version":1,"response_policy":{"include":["/name"],"max_bytes":8192}}`, 409)
	call(adminToken, "/preview", preview, 409)
}
