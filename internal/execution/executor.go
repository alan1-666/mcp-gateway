package execution

import (
	"context"
	"time"

	"github.com/alan1-666/mcp-gateway/internal/adapters/httpadapter"
	"github.com/alan1-666/mcp-gateway/internal/core"
)

type Executor struct {
	Service *core.Service
	Adapter *httpadapter.Adapter
}

func (e *Executor) Execute(ctx context.Context, actor core.Actor, id string) (core.Operation, error) {
	op, tool, claimed, err := e.Service.Claim(ctx, actor, id)
	if err != nil || !claimed {
		return op, err
	}
	// Once claimed, a browser disconnect must not discard the operation result.
	// Both downstream execution and final persistence have finite deadlines.
	callCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Duration(tool.HTTP.TimeoutMS)*time.Millisecond+time.Second)
	result := e.Adapter.Execute(callCtx, actor, tool, op)
	cancel()
	finishCtx, finishCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer finishCancel()
	return e.Service.Finish(finishCtx, actor, id, result)
}
