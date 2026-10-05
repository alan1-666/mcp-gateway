package execution

import (
	"context"
	"time"

	"github.com/alan1-666/mcp-gateway/internal/core"
)

type Adapter interface {
	Execute(context.Context, core.Actor, core.Tool, core.Operation) core.FinishInput
}

type Executor struct {
	Service *core.Service
	Adapter Adapter
}

func (e *Executor) Execute(ctx context.Context, actor core.Actor, id string) (core.Operation, error) {
	op, tool, claimed, err := e.Service.Claim(ctx, actor, id)
	if err != nil || !claimed {
		return op, err
	}
	// Once claimed, a browser disconnect must not discard the operation result.
	// Both downstream execution and final persistence have finite deadlines.
	timeout := time.Duration(tool.HTTP.TimeoutMS)*time.Millisecond + time.Second
	if tool.MCP != nil {
		timeout = 122 * time.Second
	}
	callCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	result := e.Adapter.Execute(callCtx, actor, tool, op)
	cancel()
	if tool.MCP != nil && result.State == core.StateSucceeded {
		projected, err := core.ApplyMCPResponsePolicy(result.Result, tool.ResponsePolicy)
		if err != nil {
			result = core.FinishInput{State: core.StateFailed, Error: err.Error()}
			if tool.Risk == core.RiskWrite {
				result.State = core.StateUnknown
			}
		} else {
			result.Result = projected
		}
	}
	finishCtx, finishCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer finishCancel()
	return e.Service.Finish(finishCtx, actor, id, result)
}
