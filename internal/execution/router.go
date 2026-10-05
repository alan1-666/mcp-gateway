package execution

import (
	"context"

	"github.com/alan1-666/mcp-gateway/internal/core"
)

// Router selects the adapter from the immutable tool binding. No user-supplied
// request chooses a downstream URL or bypasses operation admission.
type Router struct {
	HTTP Adapter
	MCP  Adapter
}

func (r *Router) Execute(ctx context.Context, actor core.Actor, tool core.Tool, op core.Operation) core.FinishInput {
	adapter := r.HTTP
	if tool.MCP != nil {
		adapter = r.MCP
	}
	if adapter == nil {
		return core.FinishInput{State: core.StateFailed, Error: "tool transport is not configured"}
	}
	return adapter.Execute(ctx, actor, tool, op)
}
