package execution

import (
	"context"
	"encoding/json"
	"github.com/alan1-666/mcp-gateway/internal/core"
	"testing"
)

type ledger struct {
	core.Repository
	tool   core.Tool
	finish core.FinishInput
}

func (l *ledger) Claim(context.Context, core.Actor, string) (core.Operation, core.Tool, bool, error) {
	return core.Operation{ID: "operation"}, l.tool, true, nil
}
func (l *ledger) Finish(_ context.Context, _ core.Actor, _ string, in core.FinishInput) (core.Operation, error) {
	l.finish = in
	return core.Operation{ID: "operation", State: in.State, Result: in.Result, Error: in.Error}, nil
}

type fixedResult struct {
	calls  int
	result core.FinishInput
}

func (a *fixedResult) Execute(context.Context, core.Actor, core.Tool, core.Operation) core.FinishInput {
	a.calls++
	return a.result
}
func TestProjectionFailurePreservesWriteUncertainty(t *testing.T) {
	for _, risk := range []core.Risk{core.RiskRead, core.RiskWrite} {
		t.Run(string(risk), func(t *testing.T) {
			repo := &ledger{tool: core.Tool{MCP: &core.MCPConfig{ServerID: "server"}, Risk: risk, ResponsePolicy: &core.ResponsePolicy{Include: []string{"/missing"}}}}
			downstream := &fixedResult{result: core.FinishInput{MCPObservation: &core.MCPObservation{Code: "ok", Phase: "result", CallAttempted: true}, State: core.StateSucceeded, Result: json.RawMessage(`{"isError":false,"content":[],"structuredContent":{"id":1,"private":"must not persist"}}`)}}
			executor := Executor{Service: core.NewService(repo), Adapter: downstream}
			result, err := executor.Execute(context.Background(), core.Actor{ID: "user", WorkspaceID: "workspace", Role: core.RoleOperator}, "operation")
			if err != nil {
				t.Fatal(err)
			}
			expected := core.StateFailed
			if risk == core.RiskWrite {
				expected = core.StateUnknown
			}
			if repo.finish.MCPObservation == nil || repo.finish.MCPObservation.Code != "projection_failed" || repo.finish.MCPObservation.Phase != "projection" || !repo.finish.MCPObservation.CallAttempted || repo.finish.MCPObservation.Validate() != nil {
				t.Fatal("projection lost trace", repo.finish.MCPObservation)
			}
			if result.State != expected || len(repo.finish.Result) > 0 || downstream.calls != 1 {
				t.Fatalf("unsafe projection failure: %+v", result)
			}
		})
	}
}
