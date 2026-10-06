package execution

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/alan1-666/mcp-gateway/internal/core"
)

type callLedger struct {
	core.Repository
	state  core.State
	err    error
	claims int
}

func (r *callLedger) Prepare(context.Context, core.Actor, core.PrepareInput) (core.Operation, error) {
	return core.Operation{ID: "operation", State: r.state}, r.err
}
func (r *callLedger) Claim(context.Context, core.Actor, string) (core.Operation, core.Tool, bool, error) {
	r.claims++
	return core.Operation{ID: "operation", State: r.state}, core.Tool{}, false, nil
}
func TestCallReturnsPendingAndTerminalWithoutReplaying(t *testing.T) {
	a := core.Actor{ID: "a", WorkspaceID: "w", Role: core.RoleOperator}
	input := core.PrepareInput{ToolID: "tool", Arguments: json.RawMessage(`{}`), IdempotencyKey: "stable-key"}
	for _, state := range []core.State{core.StateWaitingApproval, core.StateDispatching, core.StateSucceeded, core.StateFailed, core.StateRejected, core.StateUnknown, core.StateReady} {
		r := &callLedger{state: state}
		e := Executor{Service: core.NewService(r)}
		got, err := e.Call(context.Background(), a, input)
		want := 0
		if state == core.StateReady {
			want = 1
		}
		if err != nil || got.State != state || r.claims != want {
			t.Fatal(state, got, err, r.claims)
		}
	}
	r := &callLedger{err: core.ErrForbidden}
	e := Executor{Service: core.NewService(r)}
	if _, err := e.Call(context.Background(), a, input); !errors.Is(err, core.ErrForbidden) || r.claims != 0 {
		t.Fatal(err)
	}
}
