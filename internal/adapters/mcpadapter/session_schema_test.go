package mcpadapter

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/alan1-666/mcp-gateway/internal/adapters/httpadapter"
	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Some upstreams insert a connection-specific default/const into tools/list.
// Such schemas genuinely change: dropping these keywords from the hash would
// incorrectly approve a different contract. Until explicit binding is supported,
// the adapter must fail before a business call rather than reuse stale values.
func TestSessionBoundSchemaConstRemainsPartOfReviewedContract(t *testing.T) {
	schema := func(session string) json.RawMessage {
		return json.RawMessage(`{"type":"object","properties":{"SessionId":{"type":["string","null"],"const":"` + session + `","default":"` + session + `"},"query":{"type":"string"}}}`)
	}
	initial := basicTool("session_search")
	initial.InputSchema = schema("session-one")
	sdk, upstream, calls := sdkServer(true, 1, "", initial)
	defer upstream.Close()
	config := serverConfig(upstream.URL, "session-bound", "workspace")
	egress, err := httpadapter.New([]string{upstream.URL}, []string{"127.0.0.0/8"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	adapter := New(egress, servers{config.ID: config})
	actor := core.Actor{WorkspaceID: config.WorkspaceID}
	first, err := adapter.Discover(context.Background(), actor, config)
	if err != nil {
		t.Fatal(err)
	}
	next := basicTool("session_search")
	next.InputSchema = schema("session-two")
	sdk.AddTool(next, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		calls.Add(1)
		return &mcp.CallToolResult{}, nil
	})
	second, err := adapter.Discover(context.Background(), actor, config)
	if err != nil {
		t.Fatal(err)
	}
	if first[0].SchemaHash == second[0].SchemaHash {
		t.Fatal("session const drift was omitted from contract hash")
	}
	stored := imported(config, first[0], core.RiskRead)
	out := adapter.Execute(context.Background(), actor, stored, core.Operation{Arguments: json.RawMessage(`{"query":"test","SessionId":"session-one"}`)})
	if out.State != core.StateFailed || !strings.Contains(out.Error, "schema changed") || calls.Load() != 0 {
		t.Fatalf("stale session contract reached upstream: %+v calls=%d", out, calls.Load())
	}
}
