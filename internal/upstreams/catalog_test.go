package upstreams

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/alan1-666/mcp-gateway/internal/store/postgres"
)

func TestCatalogComparisonAndBoundaries(t *testing.T) {
	remote := []core.RemoteTool{{Name: "new", SchemaHash: "b"}, {Name: "same", Description: "  same  ", SchemaHash: "a"}, {Name: "schema", SchemaHash: "b"}, {Name: "description", Description: "changed", SchemaHash: "a"}}
	registered := map[string]core.Tool{}
	for _, name := range []string{"same", "schema", "description", "missing"} {
		registered[name] = core.Tool{ID: name, Name: "alias_" + name, Description: "same", Version: 3, Status: "published", Enabled: true, MCP: &core.MCPConfig{SchemaHash: "a"}}
	}
	page, err := compareCatalog("example", remote, registered)
	if err != nil || page.Total != 4 || len(page.Review.Items) != 5 || page.Review.Counts != (CatalogCounts{Unimported: 1, Unchanged: 1, SchemaChanged: 1, DescriptionChanged: 1, Missing: 1}) {
		t.Fatalf("comparison %+v %v", page, err)
	}
	if page.Items[1].GatewayName != "alias_same" || page.Items[1].ImportedToolID != "same" || remote[1].ImportedToolID != "" {
		t.Fatal("comparison lost registered alias or mutated upstream data")
	}
	for i, item := range page.Review.Items {
		if i > 0 && page.Review.Items[i-1].Name >= item.Name {
			t.Fatal("unstable report ordering")
		}
		if item.Name == "missing" && (item.SchemaHash != "" || item.ImportedVersion != 3 || item.ImportedSchemaHash != "a") {
			t.Fatal("missing contract evidence lost")
		}
	}
	if _, err := compareCatalog("x", append(remote, remote[0]), registered); !errors.Is(err, core.ErrInvalid) {
		t.Fatal("duplicate names accepted", err)
	}
	for i := 0; i <= MaxCatalogEntries; i++ {
		registered[fmt.Sprint(i)] = registered["missing"]
	}
	if _, err := compareCatalog("x", remote, registered); !errors.Is(err, core.ErrConflict) {
		t.Fatal("partial oversized comparison accepted", err)
	}
	for _, description := range []string{"", "  ", strings.Repeat("中", 1500)} {
		r := core.RemoteTool{Name: "normalized", Description: description, SchemaHash: "a"}
		base := core.Tool{Description: core.RemoteDescription(r), MCP: &core.MCPConfig{SchemaHash: "a"}}
		p, err := compareCatalog("x", []core.RemoteTool{r}, map[string]core.Tool{r.Name: base})
		if err != nil || p.Review.Counts.Unchanged != 1 {
			t.Fatal("normalization causes false drift", err)
		}
	}
}

func TestCatalogReviewsPersistOnlyCompleteObservations(t *testing.T) {
	ctx, pool, s, remote, a := fixture(t)
	server := server(t, ctx, s, a)
	first, err := s.Discover(ctx, a, server.ID)
	if err != nil || first.Review.Counts.Unimported != 1 || first.Review.ID == 0 || first.Review.CheckedAt.Before(first.Review.StartedAt) {
		t.Fatalf("first %+v %v", first, err)
	}
	tool, err := s.Import(ctx, a, server.ID, ImportInput{ToolName: remote.items[0].Name, SchemaHash: remote.items[0].SchemaHash, Risk: core.RiskWrite})
	if err != nil {
		t.Fatal(err)
	}
	remote.items[0].SchemaHash = strings.Repeat("b", 64)
	changed, err := s.Discover(ctx, a, server.ID)
	if err != nil || changed.Review.Counts.SchemaChanged != 1 || changed.Review.Items[0].ImportedVersion != tool.Version || changed.Review.Items[0].ImportedStatus != "draft" {
		t.Fatalf("changed %+v %v", changed, err)
	}
	stored, err := postgres.New(pool).GetTool(ctx, a, tool.ID)
	if err != nil || stored.Version != 1 || stored.Enabled || stored.MCP.SchemaHash != strings.Repeat("a", 64) {
		t.Fatal("discovery mutated executable definition", err)
	}
	remote.err = errors.New("upstream authorization failed with secret-not-for-storage")
	if _, err := s.Discover(ctx, a, server.ID); err == nil {
		t.Fatal("accepted failed discovery")
	}
	history, err := s.CatalogReviews(ctx, a, server.ID, 1, 0)
	if err != nil || len(history.Items) != 1 || history.Items[0].ID != changed.Review.ID || history.NextCursor == "" {
		t.Fatalf("history %+v %v", history, err)
	}
	older, err := s.CatalogReviews(ctx, a, server.ID, 1, changed.Review.ID)
	if err != nil || len(older.Items) != 1 || older.Items[0].ID != first.Review.ID || older.NextCursor != "" {
		t.Fatal("history cursor", err)
	}
	remote.err, remote.items = nil, nil
	empty, err := s.Discover(ctx, a, server.ID)
	if err != nil || empty.Items == nil || empty.Total != 0 || empty.Review.Counts.Missing != 1 || empty.Review.Items[0].ImportedToolID != tool.ID {
		t.Fatalf("empty complete catalog %+v %v", empty, err)
	}
	for _, role := range []core.Role{core.RoleOperator, core.RoleApprover, core.RoleViewer} {
		other := a
		other.Role = role
		if _, err := s.CatalogReviews(ctx, other, server.ID, 20, 0); !errors.Is(err, core.ErrForbidden) {
			t.Fatal("nonadmin history", err)
		}
	}
	client := a
	client.ClientID = "machine"
	if _, err := s.CatalogReviews(ctx, client, server.ID, 20, 0); !errors.Is(err, core.ErrForbidden) {
		t.Fatal("machine history", err)
	}
	other := a
	other.WorkspaceID = core.NewID()
	if _, err := s.CatalogReviews(ctx, other, server.ID, 20, 0); !errors.Is(err, core.ErrNotFound) {
		t.Fatal("cross workspace history", err)
	}
	if _, err := s.SetEnabled(ctx, a, server.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Discover(ctx, a, server.ID); !errors.Is(err, core.ErrConflict) {
		t.Fatal("disabled discovery", err)
	}
	if p, err := s.CatalogReviews(ctx, a, server.ID, 20, 0); err != nil || len(p.Items) != 3 {
		t.Fatal("disabled server lost history", err)
	}
}

func TestCatalogRetentionAndInFlightServerChange(t *testing.T) {
	ctx, pool, s, remote, a := fixture(t)
	server := server(t, ctx, s, a)
	if _, err := s.SetEnabled(ctx, a, server.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetEnabled(ctx, a, server.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.reviewCatalog(ctx, a, server, time.Now(), remote.items); !errors.Is(err, core.ErrConflict) {
		t.Fatal("stale in-flight server accepted", err)
	}
	for i := 0; i < CatalogHistoryLimit+2; i++ {
		if _, err := s.Discover(ctx, a, server.ID); err != nil {
			t.Fatal(err)
		}
	}
	p, err := s.CatalogReviews(ctx, a, server.ID, 50, 0)
	if err != nil || len(p.Items) != CatalogHistoryLimit || p.NextCursor != "" {
		t.Fatalf("retention %d %v", len(p.Items), err)
	}
	var auditCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE workspace_id=$1 AND action='MCP_CATALOG_REVIEWED'`, a.WorkspaceID).Scan(&auditCount); err != nil || auditCount != CatalogHistoryLimit+2 {
		t.Fatal("audit retention changed", auditCount, err)
	}
}
