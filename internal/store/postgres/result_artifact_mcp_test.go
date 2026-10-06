package postgres_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/alan1-666/mcp-gateway/internal/clients"
	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/alan1-666/mcp-gateway/internal/identity"
	"github.com/alan1-666/mcp-gateway/internal/transport/mcpserver"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type artifactBearerTransport struct{ token string }

func (b artifactBearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	copy := r.Clone(r.Context())
	copy.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(copy)
}

// Exercise the official SDK over Streamable HTTP and the real database-backed
// client identity/grant boundary. Artifact creation uses the same completion
// service as execution; no fabricated reference/payload table rows are inserted.
func TestResultArtifactMCPTransportAcceptance(t *testing.T) {
	f := database(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tool, _ := artifactTool(t, f)
	cs := clients.New(f.pool)
	owner, err := cs.Create(ctx, f.admin, clients.CreateInput{Name: "Artifact SDK owner", Scopes: []string{clients.ScopeRead, clients.ScopeInvoke}, ToolIDs: []string{tool.ID}})
	if err != nil {
		t.Fatal(err)
	}
	outsider, err := cs.Create(ctx, f.admin, clients.CreateInput{Name: "Artifact SDK other client", Scopes: []string{clients.ScopeRead, clients.ScopeInvoke}, ToolIDs: []string{tool.ID}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := f.pool.Exec(context.Background(), `DELETE FROM gateway_clients WHERE workspace_id=$1`, f.admin.WorkspaceID); err != nil {
			t.Error(err)
		}
	})
	actor := core.Actor{ID: owner.Client.ID, ClientID: owner.Client.ID, ClientKeyID: owner.Client.KeyID, WorkspaceID: f.admin.WorkspaceID, Role: core.RoleOperator}
	op := artifactOperation(t, f, actor, tool)
	var reference struct {
		Ref core.ResultReference `json:"gateway_result_ref"`
	}
	if err = json.Unmarshal(op.Result, &reference); err != nil {
		t.Fatal(err)
	}
	auth, err := identity.NewCloud(ctx, f.pool, "https://artifact-sdk.example", "")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(mcpserver.Handler(f.svc, nil, auth))
	defer server.Close()
	connect := func(token string) *mcp.ClientSession {
		t.Helper()
		client := mcp.NewClient(&mcp.Implementation{Name: "artifact-acceptance", Version: "1"}, nil)
		session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: server.URL, HTTPClient: &http.Client{Transport: artifactBearerTransport{token: token}}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { session.Close() })
		return session
	}
	session := connect(owner.APIKey)
	deniedSession := connect(outsider.APIKey)
	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tool := range listed.Tools {
		if tool.Name == "read_result" {
			found = true
		}
	}
	if !found {
		t.Fatal("read_result missing from SDK tool discovery")
	}
	call := func(s *mcp.ClientSession, args map[string]any) (*mcp.CallToolResult, error) {
		return s.CallTool(ctx, &mcp.CallToolParams{Name: "read_result", Arguments: args})
	}
	reject := func(s *mcp.ClientSession, args map[string]any) {
		t.Helper()
		result, err := call(s, args)
		if err == nil && (result == nil || !result.IsError) {
			t.Fatalf("unexpected successful result for %#v: %+v", args, result)
		}
		if result != nil {
			for _, c := range result.Content {
				if text, ok := c.(*mcp.TextContent); ok && (strings.Contains(text.Text, "90071992547409931234") || strings.Contains(text.Text, "中文")) {
					t.Fatal("denied response leaked payload")
				}
			}
		}
	}
	var joined strings.Builder
	cursor := ""
	pages := 0
	for {
		result, err := call(session, map[string]any{"operation_id": op.ID, "cursor": cursor, "limit_bytes": 1024})
		if err != nil || result.IsError {
			t.Fatalf("read_result failed: %+v %v", result, err)
		}
		if len(result.Content) != 1 {
			t.Fatal("unexpected MCP content count")
		}
		text, ok := result.Content[0].(*mcp.TextContent)
		if !ok {
			t.Fatal("result is not MCP text")
		}
		var page core.ResultPage
		if err = json.Unmarshal([]byte(text.Text), &page); err != nil {
			t.Fatal(err)
		}
		if page.Offset != joined.Len() || len(page.Chunk) > 1024 || !utf8.ValidString(page.Chunk) || page.Format != "json_utf8" || page.SHA256 != reference.Ref.SHA256 || page.Bytes != reference.Ref.Bytes || page.OperationID != op.ID {
			t.Fatal("invalid page metadata", page.Offset)
		}
		joined.WriteString(page.Chunk)
		pages++
		cursor = page.NextCursor
		if cursor == "" {
			break
		}
		if pages > 30 {
			t.Fatal("unbounded pagination")
		}
	}
	if pages < 2 {
		t.Fatal("fixture did not exercise chunking")
	}
	sum := sha256.Sum256([]byte(joined.String()))
	if joined.Len() != reference.Ref.Bytes || hex.EncodeToString(sum[:]) != reference.Ref.SHA256 {
		t.Fatal("reconstructed bytes differ from immutable result")
	}
	decoded, err := core.DecodeResult(json.RawMessage(joined.String()))
	if err != nil {
		t.Fatal(err)
	}
	object := decoded.(map[string]any)
	if object["id"] != json.Number("90071992547409931234") || object["password"] != "[REDACTED]" || object["excluded"] != nil {
		t.Fatal("precision/redaction/projection changed")
	}
	for _, args := range []map[string]any{{"operation_id": op.ID}, {"operation_id": op.ID, "limit_bytes": 16384}} {
		got, err := call(session, args)
		if err != nil || got.IsError {
			t.Fatalf("valid default/maximum limit rejected: %+v %v", got, err)
		}
	}
	reject(deniedSession, map[string]any{"operation_id": op.ID})
	for _, limit := range []any{0, -1, 1023, 16385, 1.5, "1024", nil} {
		reject(session, map[string]any{"operation_id": op.ID, "limit_bytes": limit})
	}
	reject(session, map[string]any{"operation_id": op.ID, "unexpected": "ignored?"})
	reject(session, map[string]any{"operation_id": op.ID, "cursor": "malformed"})
	reject(session, map[string]any{})
	empty := []string{}
	updated, err := cs.Update(ctx, f.admin, owner.Client.ID, clients.UpdateInput{ExpectedVersion: owner.Client.Version, ToolIDs: &empty})
	if err != nil {
		t.Fatal(err)
	}
	reject(session, map[string]any{"operation_id": op.ID})
	grants := []string{tool.ID}
	if _, err = cs.Update(ctx, f.admin, owner.Client.ID, clients.UpdateInput{ExpectedVersion: updated.Version, ToolIDs: &grants}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pool.Exec(ctx, `UPDATE gateway_result_artifacts SET expires_at=clock_timestamp()-interval '1 second' WHERE workspace_id=$1 AND operation_id=$2`, f.admin.WorkspaceID, op.ID); err != nil {
		t.Fatal(err)
	}
	reject(session, map[string]any{"operation_id": op.ID})
}
