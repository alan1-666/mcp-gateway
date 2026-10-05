// Package mcpadapter executes reviewed remote MCP tools through the gateway's
// egress policy. Connections are isolated per discovery or operation: no user's
// session, credential, server notification, or model capability is shared.
package mcpadapter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/alan1-666/mcp-gateway/internal/adapters/httpadapter"
	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	maxTools        = 1000
	maxPages        = 100
	maxCatalogBytes = 4 << 20
)

type Resolver interface {
	GetServer(context.Context, string, string) (core.MCPServer, error)
}

type Adapter struct {
	egress   *httpadapter.Adapter
	resolver Resolver
}

// A gateway's incoming MCP context contains SDK-private protocol/session
// values. Passing them to a nested client leaks the caller's negotiated
// protocol into the upstream handshake (including legacy fallback). The
// outbound trust boundary keeps cancellation/deadlines but no inbound values;
// actor and credential scope are passed explicitly instead.
type outboundContext struct{ context.Context }

func (outboundContext) Value(any) any { return nil }

func New(egress *httpadapter.Adapter, resolver Resolver) *Adapter {
	return &Adapter{egress: egress, resolver: resolver}
}

func (a *Adapter) ValidateServer(actor core.Actor, server core.MCPServer) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return a.ValidateServerContext(ctx, actor, server)
}
func (a *Adapter) ValidateServerContext(ctx context.Context, actor core.Actor, server core.MCPServer) error {
	if a.egress == nil || server.WorkspaceID != actor.WorkspaceID || server.TimeoutMS < 100 || server.TimeoutMS > 120000 {
		return fmt.Errorf("%w: invalid MCP server policy or timeout", core.ErrInvalid)
	}
	_, closeClient, err := a.egress.NewClientContext(ctx, actor.WorkspaceID, server.URL, server.CredentialRef, time.Duration(server.TimeoutMS)*time.Millisecond)
	if err != nil {
		return err
	}
	closeClient()
	return nil
}

func (a *Adapter) connect(ctx context.Context, actor core.Actor, server core.MCPServer) (*mcp.ClientSession, *protocolTransport, func(), error) {
	if err := a.ValidateServerContext(ctx, actor, server); err != nil {
		return nil, nil, nil, err
	}
	client, closeClient, err := a.egress.NewClientContext(ctx, actor.WorkspaceID, server.URL, server.CredentialRef, time.Duration(server.TimeoutMS)*time.Millisecond)
	if err != nil {
		return nil, nil, nil, err
	}
	transport := &protocolTransport{base: client.Transport, ctx: ctx, responses: make(map[string]*responseCapture)}
	client.Transport = transport
	sdk := mcp.NewClient(&mcp.Implementation{Name: "mcp-gateway", Version: "1.0.0"}, &mcp.ClientOptions{
		Capabilities: &mcp.ClientCapabilities{},
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	session, err := sdk.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: server.URL, HTTPClient: client, MaxRetries: -1, DisableStandaloneSSE: true}, nil)
	if err != nil {
		closeClient()
		return nil, transport, nil, fmt.Errorf("MCP server connection failed")
	}
	return session, transport, func() { _ = session.Close(); closeClient() }, nil
}

func (a *Adapter) Discover(ctx context.Context, actor core.Actor, server core.MCPServer) ([]core.RemoteTool, error) {
	if !server.Enabled {
		return nil, fmt.Errorf("%w: MCP server is disabled", core.ErrInvalid)
	}
	ctx, cancel := context.WithTimeout(outboundContext{ctx}, time.Duration(server.TimeoutMS)*time.Millisecond)
	defer cancel()
	session, transport, closeSession, err := a.connect(ctx, actor, server)
	if err != nil {
		return nil, err
	}
	defer closeSession()
	return discover(ctx, session, transport)
}

func discover(ctx context.Context, session *mcp.ClientSession, transport *protocolTransport) ([]core.RemoteTool, error) {
	tools := make([]core.RemoteTool, 0)
	seenNames, seenCursors := map[string]bool{}, map[string]bool{}
	cursor, totalBytes := "", 0
	for page := 0; page < maxPages; page++ {
		if _, err := session.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor}); err != nil {
			return nil, fmt.Errorf("MCP tool discovery failed")
		}
		raw, err := transport.result("tools/list")
		if err != nil {
			return nil, fmt.Errorf("MCP tool discovery returned an invalid response")
		}
		totalBytes += len(raw)
		if totalBytes > maxCatalogBytes {
			return nil, fmt.Errorf("MCP tool catalog exceeds the size limit")
		}
		if _, err := core.DecodeResult(raw); err != nil {
			return nil, fmt.Errorf("MCP tool catalog contains unsupported JSON")
		}
		var result struct {
			Tools []struct {
				Name         string          `json:"name"`
				Description  string          `json:"description"`
				InputSchema  json.RawMessage `json:"inputSchema"`
				OutputSchema json.RawMessage `json:"outputSchema"`
				Annotations  struct {
					ReadOnlyHint bool `json:"readOnlyHint"`
				} `json:"annotations"`
			} `json:"tools"`
			NextCursor string `json:"nextCursor"`
		}
		if err := json.Unmarshal(raw, &result); err != nil || result.Tools == nil {
			return nil, fmt.Errorf("MCP tool catalog is malformed")
		}
		for _, tool := range result.Tools {
			if len(tools) >= maxTools {
				return nil, fmt.Errorf("MCP tool catalog exceeds the tool limit")
			}
			if tool.Name == "" || len(tool.Name) > 128 || !utf8.ValidString(tool.Name) || strings.IndexFunc(tool.Name, unicode.IsControl) >= 0 || seenNames[tool.Name] || len(tool.Description) > 4000 {
				return nil, fmt.Errorf("MCP tool catalog has an invalid or duplicate tool")
			}
			seenNames[tool.Name] = true
			input, err := canonicalSchema(tool.InputSchema, true)
			if err != nil {
				return nil, fmt.Errorf("MCP tool input schema is not supported")
			}
			var output json.RawMessage
			if len(tool.OutputSchema) > 0 {
				output, err = canonicalSchema(tool.OutputSchema, false)
				if err != nil {
					return nil, fmt.Errorf("MCP tool output schema is not supported")
				}
			}
			description := tool.Description
			if strings.TrimSpace(description) == "" {
				description = "Remote MCP tool: " + tool.Name
			}
			tools = append(tools, core.RemoteTool{Name: tool.Name, Description: description, InputSchema: input, OutputSchema: output, ReadOnlyHint: tool.Annotations.ReadOnlyHint, SchemaHash: schemaHash(tool.Name, input, output)})
		}
		if result.NextCursor == "" {
			return tools, nil
		}
		if len(result.NextCursor) > 2048 || seenCursors[result.NextCursor] {
			return nil, fmt.Errorf("MCP tool discovery returned an invalid or repeated cursor")
		}
		seenCursors[result.NextCursor] = true
		cursor = result.NextCursor
	}
	return nil, fmt.Errorf("MCP tool catalog exceeds the page limit")
}

func canonicalSchema(raw json.RawMessage, object bool) (json.RawMessage, error) {
	if _, err := core.CompileSchema(raw, object); err != nil {
		return nil, err
	}
	value, err := core.DecodeResult(raw)
	if err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

func schemaHash(name string, input, output json.RawMessage) string {
	// PostgreSQL JSONB may change key order and whitespace. Contract identity is
	// based on normalized JSON values, never their serialization formatting.
	i, _ := core.DecodeResult(input)
	var o any
	if len(output) > 0 {
		o, _ = core.DecodeResult(output)
	}
	raw, _ := json.Marshal(struct {
		Name          string
		Input, Output any
	}{name, i, o})
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func (a *Adapter) Execute(ctx context.Context, actor core.Actor, tool core.Tool, op core.Operation) core.FinishInput {
	fail := func(message string, uncertain bool) core.FinishInput {
		state := core.StateFailed
		if uncertain && tool.Risk == core.RiskWrite {
			state = core.StateUnknown
		}
		return core.FinishInput{State: state, Error: message}
	}
	if a.resolver == nil || tool.MCP == nil || tool.WorkspaceID != actor.WorkspaceID || tool.MCP.ServerID == "" || tool.MCP.ToolName == "" {
		return fail("invalid MCP tool configuration", false)
	}
	server, err := a.resolver.GetServer(ctx, actor.WorkspaceID, tool.MCP.ServerID)
	if err != nil || server.ID != tool.MCP.ServerID || !server.Enabled || server.WorkspaceID != actor.WorkspaceID {
		return fail("MCP server is unavailable or disabled", false)
	}
	ctx, cancel := context.WithTimeout(outboundContext{ctx}, time.Duration(server.TimeoutMS)*time.Millisecond)
	defer cancel()
	session, transport, closeSession, err := a.connect(ctx, actor, server)
	if err != nil {
		return fail("MCP server is blocked or could not be connected", false)
	}
	defer closeSession()
	tools, err := discover(ctx, session, transport)
	if err != nil {
		return fail("MCP tool contract could not be verified", false)
	}
	matched := false
	for _, remote := range tools {
		if remote.Name == tool.MCP.ToolName {
			matched = remote.SchemaHash == tool.MCP.SchemaHash && remote.SchemaHash == schemaHash(tool.MCP.ToolName, tool.InputSchema, tool.OutputSchema)
			break
		}
	}
	if !matched {
		return fail("MCP tool schema changed or the tool was removed; rediscover and review before executing", false)
	}
	if err := core.ValidateArguments(tool.InputSchema, op.Arguments); err != nil {
		return fail("persisted arguments do not match the MCP tool contract", false)
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: tool.MCP.ToolName, Arguments: json.RawMessage(op.Arguments)})
	if err != nil || result == nil {
		return fail("MCP tool did not produce a confirmed result; verify write outcome before another action", true)
	}
	if result.IsError || result.NeedsInput() {
		return fail("MCP tool reported an error or unsupported interaction; verify write outcome before another action", true)
	}
	raw, err := transport.result("tools/call")
	if err != nil {
		return fail("MCP tool returned an invalid result", true)
	}
	envelope, err := sanitizeResult(raw, tool.OutputSchema)
	if err != nil {
		return fail(err.Error(), true)
	}
	return core.FinishInput{State: core.StateSucceeded, Result: envelope}
}

func sanitizeResult(raw, outputSchema json.RawMessage) (json.RawMessage, error) {
	if _, err := core.DecodeResult(raw); err != nil {
		return nil, fmt.Errorf("MCP tool returned malformed or oversized JSON")
	}
	var result struct {
		Content           []json.RawMessage `json:"content"`
		StructuredContent json.RawMessage   `json:"structuredContent"`
		IsError           bool              `json:"isError"`
	}
	if json.Unmarshal(raw, &result) != nil || result.IsError {
		return nil, fmt.Errorf("MCP tool reported an invalid result")
	}
	type textContent struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	content := make([]textContent, 0, len(result.Content))
	for _, item := range result.Content {
		var value struct {
			Type string  `json:"type"`
			Text *string `json:"text"`
		}
		if json.Unmarshal(item, &value) != nil || value.Type != "text" || value.Text == nil {
			return nil, fmt.Errorf("MCP content type is unsupported; only text and structured JSON are supported")
		}
		content = append(content, textContent{Type: "text", Text: *value.Text})
	}
	if len(outputSchema) > 0 {
		value, err := core.DecodeResult(result.StructuredContent)
		if err != nil {
			return nil, fmt.Errorf("MCP result is missing valid structured content required by its output schema")
		}
		schema, err := core.CompileSchema(outputSchema, false)
		if err != nil || schema.Validate(value) != nil {
			return nil, fmt.Errorf("MCP structured content failed the output contract")
		}
	}
	envelope, err := json.Marshal(struct {
		Content           []textContent   `json:"content"`
		StructuredContent json.RawMessage `json:"structuredContent,omitempty"`
		IsError           bool            `json:"isError"`
	}{content, result.StructuredContent, false})
	if err != nil || len(envelope) > core.MaxResultBytes {
		return nil, fmt.Errorf("MCP result exceeds the storage size limit")
	}
	return envelope, nil
}
