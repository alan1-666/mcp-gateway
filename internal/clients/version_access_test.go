package clients_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/alan1-666/mcp-gateway/internal/clients"
	"github.com/alan1-666/mcp-gateway/internal/core"
	"strings"
	"testing"
)

func TestRebindingCannotAuthorizeAnOldSnapshotThroughANewServerGrant(t *testing.T) {
	for _, risk := range []core.Risk{core.RiskRead, core.RiskWrite} {
		t.Run(string(risk), func(t *testing.T) {
			f := database(t)
			ctx := context.Background()
			for _, id := range []string{"original", "replacement"} {
				if _, err := f.pool.Exec(ctx, `INSERT INTO mcp_servers(workspace_id,id,name,namespace,url,timeout_ms) VALUES($1,$2,$2,$2,'https://example.test/mcp',1000)`, f.admin.WorkspaceID, id); err != nil {
					t.Fatal(err)
				}
			}
			tool, err := f.core.CreateTool(ctx, f.admin, core.ToolInput{Name: "rebound", Description: "Rebinding test", Risk: risk, InputSchema: json.RawMessage(`{"type":"object"}`), MCP: &core.MCPConfig{ServerID: "original", ToolName: "status", SchemaHash: strings.Repeat("a", 64)}})
			if err != nil {
				t.Fatal(err)
			}
			tool, err = f.core.PublishTool(ctx, f.admin, tool.ID)
			if err != nil {
				t.Fatal(err)
			}
			issued, err := f.clients.Create(ctx, f.admin, clients.CreateInput{Name: "server-only", Scopes: []string{clients.ScopeRead, clients.ScopeInvoke}, ServerIDs: []string{"original"}})
			if err != nil {
				t.Fatal(err)
			}
			a := actor(issued.Client)
			intent := core.PrepareInput{ToolID: tool.ID, Arguments: json.RawMessage(`{}`), IdempotencyKey: "original-intent"}
			op, err := f.core.Prepare(ctx, a, intent)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.pool.Exec(ctx, `UPDATE tools SET definition=jsonb_set(definition,'{mcp,server_id}','"replacement"'),version=version+1 WHERE workspace_id=$1 AND id=$2`, a.WorkspaceID, tool.ID); err != nil {
				t.Fatal(err)
			}
			servers := []string{"replacement"}
			if _, err = f.clients.Update(ctx, f.admin, a.ID, clients.UpdateInput{ExpectedVersion: 1, ServerIDs: &servers}); err != nil {
				t.Fatal(err)
			}
			if _, err = f.core.GetTool(ctx, a, tool.ID); err != nil {
				t.Fatal("current binding should remain visible", err)
			}
			if _, err = f.core.GetOperation(ctx, a, op.ID); !errors.Is(err, core.ErrNotFound) {
				t.Fatal("old server history became visible", err)
			}
			if _, err = f.core.Prepare(ctx, a, intent); !errors.Is(err, core.ErrNotFound) {
				t.Fatal("idempotency returned revoked snapshot", err)
			}
			if _, _, claimed, err := f.core.Claim(ctx, f.admin, op.ID); claimed || !errors.Is(err, core.ErrNotFound) {
				t.Fatal("admin bypassed original server grant", claimed, err)
			}
			if _, err = f.core.Approve(ctx, f.admin, op.ID); !errors.Is(err, core.ErrNotFound) {
				t.Fatal("approval bypassed snapshot grant", err)
			}
			// An explicit stable tool grant intentionally covers its historical versions,
			// but the actual old destination must still be enabled before dispatch.
			tools := []string{tool.ID}
			if _, err = f.clients.Update(ctx, f.admin, a.ID, clients.UpdateInput{ExpectedVersion: 2, ToolIDs: &tools}); err != nil {
				t.Fatal(err)
			}
			if _, err = f.pool.Exec(ctx, `UPDATE mcp_servers SET enabled=false WHERE workspace_id=$1 AND id='original'`, a.WorkspaceID); err != nil {
				t.Fatal(err)
			}
			if risk == core.RiskRead {
				if _, _, claimed, err := f.core.Claim(ctx, a, op.ID); claimed || !errors.Is(err, core.ErrConflict) {
					t.Fatal("disabled snapshot source claimed", claimed, err)
				}
			} else {
				if _, err = f.core.Approve(ctx, f.admin, op.ID); !errors.Is(err, core.ErrConflict) {
					t.Fatal("disabled snapshot source approved", err)
				}
			}
		})
	}
}
