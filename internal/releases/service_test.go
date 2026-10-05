package releases

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/alan1-666/mcp-gateway/internal/adapters/httpadapter"
	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/alan1-666/mcp-gateway/internal/store/postgres"
	"github.com/alan1-666/mcp-gateway/internal/upstreams"
	"github.com/alan1-666/mcp-gateway/migrations"
	"github.com/jackc/pgx/v5/pgxpool"
)

type remoteFixture struct {
	mu   sync.RWMutex
	tool core.RemoteTool
}

func (r *remoteFixture) ValidateServer(core.Actor, core.MCPServer) error { return nil }
func (r *remoteFixture) Discover(context.Context, core.Actor, core.MCPServer) ([]core.RemoteTool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return []core.RemoteTool{r.tool}, nil
}
func (r *remoteFixture) change() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tool.SchemaHash = strings.Repeat("b", 64)
	r.tool.InputSchema = json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"],"additionalProperties":false}`)
}
func fixture(t *testing.T) (context.Context, *pgxpool.Pool, *Service, *remoteFixture, core.Actor, core.Tool) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err = migrations.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	a := core.Actor{ID: core.NewID(), WorkspaceID: core.NewID(), Role: core.RoleAdmin}
	t.Cleanup(func() {
		for _, table := range []string{"operation_events", "audit_events", "operations", "tools", "mcp_servers"} {
			if _, err := pool.Exec(ctx, "DELETE FROM "+table+" WHERE workspace_id=$1", a.WorkspaceID); err != nil {
				t.Error(err)
			}
		}
	})
	remote := &remoteFixture{tool: core.RemoteTool{Name: "search", Description: "Search documents", SchemaHash: strings.Repeat("a", 64), InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)}}
	up := upstreams.New(upstreams.NewStore(pool), remote)
	server, err := up.Create(ctx, a, core.MCPServerInput{Name: "Docs", Namespace: "docs", URL: "https://docs.example/mcp"})
	if err != nil {
		t.Fatal(err)
	}
	tool, err := up.Import(ctx, a, server.ID, upstreams.ImportInput{ToolName: "search", SchemaHash: remote.tool.SchemaHash, Risk: core.RiskRead})
	if err != nil {
		t.Fatal(err)
	}
	tool, err = core.NewService(postgres.New(pool)).PublishTool(ctx, a, tool.ID)
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := httpadapter.New([]string{"https://docs.example"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return ctx, pool, New(pool, remote, adapter), remote, a, tool
}
func TestCandidatePublicationPinsSnapshotsAndRollbackChecksUpstream(t *testing.T) {
	ctx, pool, s, remote, a, tool := fixture(t)
	coreService := core.NewService(postgres.New(pool))
	op, err := coreService.Prepare(ctx, a, core.PrepareInput{ToolID: tool.ID, Arguments: json.RawMessage(`{}`), IdempotencyKey: "version-snapshot"})
	if err != nil {
		t.Fatal(err)
	}
	remote.change()
	candidate, err := s.Create(ctx, a, tool.ID, CandidateInput{ExpectedVersion: 1, Reason: "Review upstream query requirement"})
	if err != nil {
		t.Fatal(err)
	}
	if len(candidate.Changes) != 2 {
		t.Fatalf("changes %+v", candidate.Changes)
	}
	current, _ := coreService.GetTool(ctx, a, tool.ID)
	if current.Version != 1 {
		t.Fatal("candidate mutated live tool")
	}
	updated, err := s.Publish(ctx, a, tool.ID, candidate.ID, 1)
	if err != nil || updated.Version != 2 {
		t.Fatalf("publish %+v %v", updated, err)
	}
	again, err := s.Publish(ctx, a, tool.ID, candidate.ID, 1)
	if err != nil || again.Version != 2 {
		t.Fatal("publish retry changed release", again, err)
	}
	var snapshot core.Tool
	var raw []byte
	if err = pool.QueryRow(ctx, `SELECT tool_snapshot FROM operations WHERE workspace_id=$1 AND id=$2`, a.WorkspaceID, op.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &snapshot); err != nil || snapshot.Version != 1 || snapshot.MCP.SchemaHash != strings.Repeat("a", 64) {
		t.Fatal("snapshot mutated", snapshot, err)
	}
	versions, err := s.Versions(ctx, a, tool.ID, 0, 1)
	if err != nil || len(versions.Items) != 1 || versions.NextCursor != 2 {
		t.Fatal(versions, err)
	}
	older, err := s.Versions(ctx, a, tool.ID, versions.NextCursor, 1)
	if err != nil || len(older.Items) != 1 || older.Items[0].Version != 1 {
		t.Fatal(older, err)
	}
	if _, err = s.Create(ctx, a, tool.ID, CandidateInput{ExpectedVersion: 2, SourceVersion: 1, Reason: "Rollback"}); !errors.Is(err, core.ErrConflict) {
		t.Fatal("incompatible rollback allowed", err)
	}
	remote.mu.Lock()
	remote.tool.SchemaHash = strings.Repeat("a", 64)
	remote.tool.InputSchema = tool.InputSchema
	remote.mu.Unlock()
	rollback, err := s.Create(ctx, a, tool.ID, CandidateInput{ExpectedVersion: 2, SourceVersion: 1, Reason: "Upstream restored its earlier contract"})
	if err != nil {
		t.Fatal(err)
	}
	restored, err := s.Publish(ctx, a, tool.ID, rollback.ID, 2)
	if err != nil || restored.Version != 3 || restored.MCP.SchemaHash != tool.MCP.SchemaHash {
		t.Fatal(restored, err)
	}
	var count int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE workspace_id=$1 AND action='TOOL_VERSION_PUBLISHED'`, a.WorkspaceID).Scan(&count); err != nil || count != 2 {
		t.Fatal(count, err)
	}
}
func TestStaleReviewRacesAndRetirement(t *testing.T) {
	ctx, pool, s, remote, a, tool := fixture(t)
	remote.change()
	c1, err := s.Create(ctx, a, tool.ID, CandidateInput{ExpectedVersion: 1, Reason: "Schema update"})
	if err != nil {
		t.Fatal(err)
	}
	c2, err := s.Create(ctx, a, tool.ID, CandidateInput{ExpectedVersion: 1, Risk: core.RiskWrite, Reason: "Require approval"})
	if err != nil {
		t.Fatal(err)
	}
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, c := range []Candidate{c1, c2} {
		wg.Add(1)
		go func(c Candidate) { defer wg.Done(); _, err := s.Publish(ctx, a, tool.ID, c.ID, 1); errs <- err }(c)
	}
	wg.Wait()
	close(errs)
	success, conflict := 0, 0
	for err := range errs {
		if err == nil {
			success++
		} else if errors.Is(err, core.ErrConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatal("concurrent publications", success, conflict)
	}
	retired, err := s.Retire(ctx, a, tool.ID, 2)
	if err != nil || retired.Enabled || retired.Status != "retired" || retired.Version != 3 {
		t.Fatal(retired, err)
	}
	cs := core.NewService(postgres.New(pool))
	if _, err = cs.PublishTool(ctx, a, tool.ID); !errors.Is(err, core.ErrConflict) {
		t.Fatal("retired tool directly republished", err)
	}
	if _, err = cs.SetToolEnabled(ctx, a, tool.ID, true); !errors.Is(err, core.ErrConflict) {
		t.Fatal("retired enabled", err)
	}
	if _, err = cs.Prepare(ctx, a, core.PrepareInput{ToolID: tool.ID, Arguments: json.RawMessage(`{"query":"test"}`), IdempotencyKey: "retired-no-dispatch"}); err == nil {
		t.Fatal("retired tool prepared")
	}
	current, _ := cs.GetTool(ctx, a, tool.ID)
	candidate, err := s.Create(ctx, a, tool.ID, CandidateInput{ExpectedVersion: current.Version, Reason: "Reviewed restoration"})
	if err != nil {
		t.Fatal(err)
	}
	restored, err := s.Publish(ctx, a, tool.ID, candidate.ID, current.Version)
	if err != nil || restored.Version != 4 || !restored.Enabled {
		t.Fatal(restored, err)
	}
}
func TestPublicationRejectsDriftAndDisabledSourceAndScopes(t *testing.T) {
	ctx, pool, s, _, a, tool := fixture(t)
	c, err := s.Create(ctx, a, tool.ID, CandidateInput{ExpectedVersion: 1, Risk: core.RiskWrite, Reason: "Require approval"})
	if err != nil {
		t.Fatal(err)
	}
	other := a
	other.WorkspaceID = core.NewID()
	if _, err = s.Candidate(ctx, other, tool.ID, c.ID); !errors.Is(err, core.ErrNotFound) {
		t.Fatal(err)
	}
	viewer := a
	viewer.Role = core.RoleViewer
	if _, err = s.Versions(ctx, viewer, tool.ID, 0, 20); !errors.Is(err, core.ErrForbidden) {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE mcp_servers SET enabled=false WHERE workspace_id=$1 AND id=$2`, a.WorkspaceID, tool.MCP.ServerID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Publish(ctx, a, tool.ID, c.ID, 1); !errors.Is(err, core.ErrConflict) {
		t.Fatal("disabled source published", err)
	}
	if err = s.Discard(ctx, a, tool.ID, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Publish(ctx, a, tool.ID, c.ID, 1); !errors.Is(err, core.ErrNotFound) {
		t.Fatal("discarded candidate published", err)
	}
	_, err = pool.Exec(ctx, `UPDATE tools SET definition=jsonb_set(definition,'{description}','"tampered"') WHERE workspace_id=$1 AND id=$2`, a.WorkspaceID, tool.ID)
	if err == nil {
		t.Fatal("unversioned definition edit allowed")
	}
}
func TestPublicationRejectsUpstreamChangesAfterReview(t *testing.T) {
	ctx, _, s, remote, a, tool := fixture(t)
	c, err := s.Create(ctx, a, tool.ID, CandidateInput{ExpectedVersion: 1, Risk: core.RiskWrite, Reason: "Approval change"})
	if err != nil {
		t.Fatal(err)
	}
	remote.change()
	if _, err = s.Publish(ctx, a, tool.ID, c.ID, 1); !errors.Is(err, core.ErrConflict) {
		t.Fatal("drift published", err)
	}
}
func TestHTTPRevisionAndLargeSchemaDiff(t *testing.T) {
	ctx, pool, s, _, a, _ := fixture(t)
	cs := core.NewService(postgres.New(pool))
	d := core.ToolInput{Name: "health", Description: "Health query", Risk: core.RiskRead, InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`), HTTP: core.HTTPConfig{URL: "https://docs.example/health", Method: "GET", TimeoutMS: 1000}}
	tool, err := cs.CreateTool(ctx, a, d)
	if err != nil {
		t.Fatal(err)
	}
	d.Description = "Updated health query"
	c, err := s.Create(ctx, a, tool.ID, CandidateInput{ExpectedVersion: 1, Definition: &d, Reason: "Review endpoint metadata"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Publish(ctx, a, tool.ID, c.ID, 1); err != nil {
		t.Fatal(err)
	}
	d.HTTP.URL = "https://unapproved.example/health"
	if _, err = s.Create(ctx, a, tool.ID, CandidateInput{ExpectedVersion: 2, Definition: &d, Reason: "Unapproved destination"}); err == nil {
		t.Fatal("unapproved HTTP candidate")
	}
	before, after := definition(tool), definition(tool)
	before.InputSchema = json.RawMessage(`{"minimum":9007199254740992}`)
	after.InputSchema = json.RawMessage(`{"minimum":9007199254740993}`)
	if len(differences(before, after)) != 1 {
		t.Fatal("schema numbers rounded during diff")
	}
}
