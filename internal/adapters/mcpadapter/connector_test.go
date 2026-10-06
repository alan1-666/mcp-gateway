package mcpadapter

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/alan1-666/mcp-gateway/internal/core"
)

type privateDelegate struct {
	tools  []core.RemoteTool
	result core.FinishInput
	calls  int
}

func (d *privateDelegate) ValidateServer(core.Actor, core.MCPServer) error { return nil }
func (d *privateDelegate) Discover(context.Context, core.Actor, core.MCPServer) ([]core.RemoteTool, error) {
	return d.tools, nil
}
func (d *privateDelegate) Execute(context.Context, core.Actor, core.Tool, core.Operation) core.FinishInput {
	d.calls++
	return d.result
}

func TestConnectorBoundaryValidatesCatalogAndOutput(t *testing.T) {
	server := serverConfig("", "private", "one")
	server.ConnectorID, server.TargetName = "connector", "docs"
	remote := core.RemoteTool{Name: "search", InputSchema: objectSchema, OutputSchema: json.RawMessage(`{"type":"object","required":["id"],"properties":{"id":{"type":"integer"}}}`)}
	remote.SchemaHash = schemaHash(remote.Name, remote.InputSchema, remote.OutputSchema)
	d := &privateDelegate{tools: []core.RemoteTool{remote}}
	adapter := New(nil, servers{server.ID: server})
	adapter.SetDelegate(d)
	actor := core.Actor{WorkspaceID: "one"}
	items, err := adapter.Discover(context.Background(), actor, server)
	if err != nil || len(items) != 1 {
		t.Fatalf("discover: %v", err)
	}
	d.tools[0].SchemaHash = "forged"
	if _, err = adapter.Discover(context.Background(), actor, server); err == nil {
		t.Fatal("accepted forged schema hash")
	}
	d.tools = []core.RemoteTool{remote, remote}
	if _, err = adapter.Discover(context.Background(), actor, server); err == nil {
		t.Fatal("accepted duplicate tools")
	}
	tool := imported(server, remote, core.RiskWrite)
	op := core.Operation{Arguments: json.RawMessage(`{}`)}
	for _, tc := range []struct {
		name   string
		result core.FinishInput
		want   core.State
	}{
		{"valid", core.FinishInput{State: core.StateSucceeded, Result: json.RawMessage(`{"content":[],"structuredContent":{"id":9007199254740993},"_meta":{"secret":"redact"}}`)}, core.StateSucceeded},
		{"schema failure", core.FinishInput{State: core.StateSucceeded, Result: json.RawMessage(`{"content":[],"structuredContent":{"id":"wrong"}}`)}, core.StateUnknown},
		{"unsupported image", core.FinishInput{State: core.StateSucceeded, Result: json.RawMessage(`{"content":[{"type":"image","data":"x"}],"structuredContent":{"id":1}}`)}, core.StateUnknown},
		{"bad status", core.FinishInput{State: core.StateReady}, core.StateUnknown},
		{"not started", core.FinishInput{State: core.StateFailed}, core.StateFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d.result = tc.result
			out := adapter.Execute(context.Background(), actor, tool, op)
			if out.State != tc.want {
				t.Fatalf("state=%s", out.State)
			}
			if tc.name == "valid" && string(out.Result) != `{"content":[],"structuredContent":{"id":9007199254740993},"isError":false}` {
				t.Fatalf("raw result changed: %s", out.Result)
			}
		})
	}
	calls := d.calls
	op.Arguments = json.RawMessage(`{"extra":1}`)
	if out := adapter.Execute(context.Background(), actor, tool, op); out.State != core.StateFailed || d.calls != calls {
		t.Fatal("invalid arguments were delegated")
	}
	if err := adapter.ValidateServer(core.Actor{WorkspaceID: "another"}, server); err == nil {
		t.Fatal("cross-workspace accepted")
	}
	server.URL = "https://unapproved.example/mcp"
	if err := adapter.ValidateServer(actor, server); err == nil {
		t.Fatal("connector binding accepted cloud destination")
	}
}
