package execution

import (
	"context"

	"github.com/alan1-666/mcp-gateway/internal/core"
)

// Call hides the prepare/execute choreography while preserving the operation
// ledger. Retrying the same intent resumes an approved operation or returns the
// existing terminal/unknown/pending state; it never allocates a fresh key.
func (e *Executor) Call(ctx context.Context, actor core.Actor, input core.PrepareInput) (core.Operation, error) {
	op, err := e.Service.Prepare(ctx, actor, input)
	if err != nil || op.State != core.StateReady {
		return op, err
	}
	return e.Execute(ctx, actor, op.ID)
}
