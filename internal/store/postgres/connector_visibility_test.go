package postgres_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/alan1-666/mcp-gateway/internal/connectors"
	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/alan1-666/mcp-gateway/internal/store/postgres"
)

func TestConnectorRevocationHidesToolsAndRejectsNewIntents(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	service := connectors.New(f.pool)
	created, err := service.Create(ctx, f.admin, "Visibility test")
	if err != nil {
		t.Fatal(err)
	}
	serverID := core.NewID()
	clientID := "client_" + core.NewID()
	t.Cleanup(func() {
		for _, table := range []string{"gateway_clients", "mcp_servers", "gateway_connectors"} {
			if _, err := f.pool.Exec(context.Background(), "DELETE FROM "+table+" WHERE workspace_id=$1", f.admin.WorkspaceID); err != nil {
				t.Error(err)
			}
		}
	})
	if _, err = f.pool.Exec(ctx, `INSERT INTO mcp_servers(workspace_id,id,name,namespace,url,connector_id,target_name,timeout_ms) VALUES($1,$2,'Private docs','private','',$3,'docs',2000)`, f.admin.WorkspaceID, serverID, created.Connector.ID); err != nil {
		t.Fatal(err)
	}
	tool, err := f.svc.CreateTool(ctx, f.admin, core.ToolInput{Name: "private.search", Description: "Read private documentation", Risk: core.RiskRead, InputSchema: json.RawMessage(`{"type":"object"}`), MCP: &core.MCPConfig{ServerID: serverID, ToolName: "search", SchemaHash: strings.Repeat("b", 64)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.PublishTool(ctx, f.admin, tool.ID); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte(core.NewID()))
	if _, err = f.pool.Exec(ctx, `INSERT INTO gateway_clients(id,workspace_id,name,scopes,key_id,token_hash,key_expires_at) VALUES($1,$2,'Visibility test',ARRAY['tools:read','tools:invoke'],'key_'||$1,$3,clock_timestamp()+interval '1 hour')`, clientID, f.admin.WorkspaceID, hash[:]); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pool.Exec(ctx, `INSERT INTO gateway_client_server_grants(workspace_id,client_id,server_id) VALUES($1,$2,$3)`, f.admin.WorkspaceID, clientID, serverID); err != nil {
		t.Fatal(err)
	}
	client := core.Actor{ID: clientID, ClientID: clientID, ClientKeyID: "key_" + clientID, WorkspaceID: f.admin.WorkspaceID, Role: core.RoleOperator}
	actors := []core.Actor{f.operator, client}
	ready := []core.Operation{}
	for _, a := range actors {
		page, err := f.svc.DiscoverTools(ctx, a, core.ToolSearchInput{})
		if err != nil || page.Total != 1 || len(page.Items) != 1 {
			t.Fatal(page, err)
		}
		op, err := f.svc.Prepare(ctx, a, core.PrepareInput{ToolID: tool.ID, Arguments: json.RawMessage(`{}`), IdempotencyKey: core.NewID()})
		if err != nil {
			t.Fatal(err)
		}
		ready = append(ready, op)
	}
	if _, err = service.Revoke(ctx, f.admin, created.Connector.ID); err != nil {
		t.Fatal(err)
	}
	for index, a := range actors {
		page, err := f.svc.DiscoverTools(ctx, a, core.ToolSearchInput{})
		if err != nil || page.Total != 0 || len(page.Items) != 0 || page.NextCursor != "" {
			t.Fatal("revoked connector catalog visible", page, err)
		}
		registry, err := f.svc.SearchTools(ctx, a, core.ToolSearchInput{})
		if err != nil || registry.Total != 0 || len(registry.Items) != 0 {
			t.Fatal("revoked connector registry visible", registry, err)
		}
		list, err := f.svc.ListTools(ctx, a)
		if err != nil || len(list) != 0 {
			t.Fatal("revoked connector list visible", list, err)
		}
		if _, err = f.svc.GetTool(ctx, a, tool.ID); !errors.Is(err, core.ErrNotFound) {
			t.Fatal("revoked connector schema visible", err)
		}
		if _, err = f.svc.Prepare(ctx, a, core.PrepareInput{ToolID: tool.ID, Arguments: json.RawMessage(`{}`), IdempotencyKey: core.NewID()}); err == nil {
			t.Fatal("prepared after revocation")
		}
		if _, _, claimed, err := f.svc.Claim(ctx, a, ready[index].ID); claimed || !errors.Is(err, core.ErrConflict) {
			t.Fatal("existing intent dispatched after revocation", claimed, err)
		}
	}
	adminTool, err := f.svc.GetTool(ctx, f.admin, tool.ID)
	if err != nil || adminTool.Enabled {
		t.Fatal("admin effective enabled state incorrect", adminTool, err)
	}
	registry, err := f.svc.SearchTools(ctx, f.admin, core.ToolSearchInput{})
	if err != nil || registry.Total != 1 || len(registry.Items) != 1 || registry.Items[0].Enabled {
		t.Fatal(registry, err)
	}
	catalog, err := f.svc.DiscoverTools(ctx, f.admin, core.ToolSearchInput{})
	if err != nil || catalog.Total != 0 || len(catalog.Items) != 0 {
		t.Fatal("admin consumer catalog exposed unavailable tool", catalog, err)
	}
	if _, err = f.svc.Prepare(ctx, f.admin, core.PrepareInput{ToolID: tool.ID, Arguments: json.RawMessage(`{}`), IdempotencyKey: core.NewID()}); err == nil {
		t.Fatal("admin prepared revoked connector intent")
	}
	var storedEnabled bool
	if err = f.pool.QueryRow(ctx, `SELECT enabled FROM tools WHERE workspace_id=$1 AND id=$2`, f.admin.WorkspaceID, tool.ID).Scan(&storedEnabled); err != nil || !storedEnabled {
		t.Fatal("revocation changed stored tool flag", err)
	}
	// Registration/publishing helpers use the same effective availability check.
	if _, err = postgres.New(f.pool).CreateTool(ctx, f.admin, core.ToolInput{Name: "another", Risk: core.RiskRead, InputSchema: json.RawMessage(`{"type":"object"}`), MCP: tool.MCP}); !errors.Is(err, core.ErrConflict) {
		t.Fatal("registration accepted revoked connector", err)
	}
}
