package execution

import (
	"context"
	"log/slog"
	"time"

	"github.com/alan1-666/mcp-gateway/internal/capacity"
	"github.com/alan1-666/mcp-gateway/internal/core"
)

type Adapter interface {
	Execute(context.Context, core.Actor, core.Tool, core.Operation) core.FinishInput
}

type Executor struct {
	Service  *core.Service
	Adapter  Adapter
	Capacity *capacity.Service
}

func (e *Executor) Execute(ctx context.Context, actor core.Actor, id string) (core.Operation, error) {
	if e.Capacity != nil {
		current, err := e.Service.GetOperation(ctx, actor, id)
		if err != nil {
			return current, err
		}
		if current.State == core.StateWaitingApproval {
			// Approval can race this read. Never enter Claim without admission
			// simply because the operation was waiting a moment earlier.
			return current, core.ErrConflict
		}
		if current.State == core.StateReady {
			admissionCtx, admissionCancel := context.WithTimeout(ctx, 5*time.Second)
			lease, err := e.Capacity.Acquire(admissionCtx, actor, id)
			admissionCancel()
			if err != nil {
				return current, err
			}
			defer func() {
				releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
				defer cancel()
				if err := lease.Release(releaseCtx); err != nil {
					slog.Error("capacity lease release failed", "operation_id", id)
				}
			}()
		}
	}
	claimCtx, claimCancel := context.WithTimeout(ctx, 5*time.Second)
	op, tool, claimed, err := e.Service.Claim(claimCtx, actor, id)
	claimCancel()
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
	started := time.Now()
	result := e.Adapter.Execute(callCtx, actor, tool, op)
	cancel()
	if tool.MCP != nil && result.State == core.StateSucceeded {
		projectionStarted := time.Now()
		projected, err := core.ApplyMCPResponsePolicy(result.Result, tool.ResponsePolicy)
		if err != nil {
			result.State, result.Result, result.Error = core.StateFailed, nil, err.Error()
			if result.MCPObservation != nil {
				result.MCPObservation.Code = "projection_failed"
			}
			if tool.Risk == core.RiskWrite {
				result.State = core.StateUnknown
			}
		} else {
			result.Result = projected
		}
		if result.MCPObservation != nil {
			elapsed := time.Since(projectionStarted).Milliseconds()
			result.MCPObservation.PhasesMS.Projection = elapsed
			result.MCPObservation.TotalMS += elapsed
			if result.MCPObservation.Code == "projection_failed" {
				result.MCPObservation.Phase = "projection"
			}
		}
	}
	finishCtx, finishCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer finishCancel()
	finished, err := e.Service.Finish(finishCtx, actor, id, result)
	if e.Capacity != nil {
		state := finished.State
		if err != nil {
			state = core.StateUnknown
		}
		metricsCtx, metricsCancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		if observeErr := e.Capacity.Observe(metricsCtx, actor, tool, state, time.Since(started)); observeErr != nil {
			slog.Error("execution metrics persistence failed", "operation_id", id)
		}
		metricsCancel()
	}
	return finished, err
}
