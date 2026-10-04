package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/alan1-666/mcp-gateway/internal/execution"
	"github.com/alan1-666/mcp-gateway/internal/identity"
	"github.com/alan1-666/mcp-gateway/internal/transport/httpapi"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func Handler(service *core.Service, executor *execution.Executor, auth *identity.Auth) http.Handler {
	h := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		actor := identity.Actor(r.Context())
		server := mcp.NewServer(&mcp.Implementation{Name: "mcp-gateway", Version: "0.1.0"}, nil)
		type Search struct {
			Query  string `json:"query,omitempty"`
			Cursor string `json:"cursor,omitempty"`
			Limit  *int   `json:"limit,omitempty"`
		}
		type ToolID struct {
			ToolID string `json:"tool_id"`
		}
		type OperationID struct {
			OperationID string `json:"operation_id"`
		}
		mcp.AddTool(server, &mcp.Tool{Name: "search_tools", Description: "Search authorized enabled, published tool summaries by a literal name or description fragment. Returns one page and next_cursor; use the same query and cursor to request another page only when needed. Fetch a selected tool schema before preparing an operation.", InputSchema: map[string]any{
			"type": "object", "properties": map[string]any{
				"query":  map[string]any{"type": "string", "maxLength": 200, "description": "Optional literal search text; maximum 200 UTF-8 bytes."},
				"cursor": map[string]any{"type": "string", "maxLength": 2048},
				"limit":  map[string]any{"type": "integer", "minimum": 1, "maximum": 50},
			}, "additionalProperties": false,
		}}, func(ctx context.Context, _ *mcp.CallToolRequest, in Search) (*mcp.CallToolResult, any, error) {
			input := core.ToolSearchInput{Query: in.Query, Cursor: in.Cursor}
			if in.Limit != nil {
				if *in.Limit < 1 || *in.Limit > 50 {
					return result(nil, fmt.Errorf("%w: limit must be an integer from 1 to 50", core.ErrInvalid))
				}
				input.Limit = *in.Limit
			}
			page, err := service.DiscoverTools(ctx, actor, input)
			return result(page, err)
		})
		mcp.AddTool(server, &mcp.Tool{Name: "get_tool_schema", Description: "Read an authorized published tool's input and output contracts."}, func(ctx context.Context, _ *mcp.CallToolRequest, in ToolID) (*mcp.CallToolResult, any, error) {
			t, err := service.GetTool(ctx, actor, in.ToolID)
			if err != nil {
				return result(nil, err)
			}
			if t.Status != "published" || !t.Enabled {
				return result(nil, core.ErrNotFound)
			}
			return result(map[string]any{"id": t.ID, "name": t.Name, "description": t.Description, "risk": t.Risk, "version": t.Version, "input_schema": t.InputSchema, "output_schema": t.OutputSchema}, nil)
		})
		mcp.AddTool(server, &mcp.Tool{Name: "prepare_action", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"tool_id": map[string]any{"type": "string"}, "arguments": map[string]any{"type": "object"}, "idempotency_key": map[string]any{"type": "string", "minLength": 8, "maxLength": 128}}, "required": []string{"tool_id", "arguments", "idempotency_key"}, "additionalProperties": false}, Description: "Persist fixed arguments and a stable client idempotency key before execution. Writes wait for independent approval. Reuse the same key on an uncertain preparation response."}, func(ctx context.Context, _ *mcp.CallToolRequest, in core.PrepareInput) (*mcp.CallToolResult, any, error) {
			op, err := service.Prepare(ctx, actor, in)
			return result(op, err)
		})
		mcp.AddTool(server, &mcp.Tool{Name: "invoke_tool", Description: "Execute an existing READY operation by ID. Repeated calls return its recorded state. UNKNOWN does not mean failed and must not be replayed with a new operation."}, func(ctx context.Context, _ *mcp.CallToolRequest, in OperationID) (*mcp.CallToolResult, any, error) {
			op, err := executor.Execute(ctx, actor, in.OperationID)
			return result(op, err)
		})
		mcp.AddTool(server, &mcp.Tool{Name: "get_operation", Description: "Read durable execution state, result, approval or unknown outcome for an authorized operation."}, func(ctx context.Context, _ *mcp.CallToolRequest, in OperationID) (*mcp.CallToolResult, any, error) {
			op, err := service.GetOperation(ctx, actor, in.OperationID)
			return result(op, err)
		})
		return server
	}, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true, MaxRequestBodyBytes: 256 << 10})
	return auth.Middleware(h)
}

func result(value any, err error) (*mcp.CallToolResult, any, error) {
	if err != nil {
		_, code, message := httpapi.ErrorDetails(err)
		value = map[string]any{"error": map[string]string{"code": code, "message": message}}
	}
	data, marshalErr := json.Marshal(value)
	if marshalErr != nil {
		return nil, nil, marshalErr
	}
	// Business failures remain MCP tool results, distinguishable from transport errors.
	return &mcp.CallToolResult{IsError: err != nil, Content: []mcp.Content{&mcp.TextContent{Text: string(data)}}}, nil, nil
}
