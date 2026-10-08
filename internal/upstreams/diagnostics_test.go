package upstreams

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/alan1-666/mcp-gateway/internal/core"
)

type diagnosticRemote struct {
	*fakeRemote
	report CheckReport
	checks int
}

func (r *diagnosticRemote) Check(context.Context, core.Actor, core.MCPServer) CheckReport {
	r.checks++
	return r.report
}

func TestFreshSessionDiagnosticHistoryIsScopedAndBackwardCompatible(t *testing.T) {
	ctx, pool, _, remote, actor := fixture(t)
	r := &diagnosticRemote{fakeRemote: remote, report: CheckReport{
		ID: 999, ServerID: "untrusted", CheckedAt: time.Unix(1, 0),
		Status: "degraded", Stage: "compatibility", Code: "session_catalog_changed", SessionContractStatus: "changed",
		Message:           "The tool catalog changed between independent sessions; review upstream contract stability before publishing.",
		IncompatibleCount: 1, Tools: []ToolCompatibility{{Name: "search", Status: "incompatible", Code: "session_contract_changed"}},
	}}
	svc := New(NewStore(pool), r)
	config := server(t, ctx, svc, actor)
	for _, role := range []core.Role{core.RoleOperator, core.RoleViewer, core.RoleApprover} {
		denied := actor
		denied.Role = role
		if _, err := svc.Check(ctx, denied, config.ID); !errors.Is(err, core.ErrForbidden) {
			t.Fatal("non-admin check", err)
		}
		if _, err := svc.Checks(ctx, denied, config.ID, 0, 0); !errors.Is(err, core.ErrForbidden) {
			t.Fatal("non-admin history", err)
		}
	}
	outsider := actor
	outsider.WorkspaceID = core.NewID()
	if _, err := svc.Check(ctx, outsider, config.ID); !errors.Is(err, core.ErrNotFound) {
		t.Fatal("cross-workspace check", err)
	}
	if r.checks != 0 {
		t.Fatal("denied request reached remote checker")
	}
	checked, err := svc.Check(ctx, actor, config.ID)
	if err != nil {
		t.Fatal(err)
	}
	if checked.ID == 0 || checked.ID == 999 || checked.ServerID != config.ID || checked.CheckedAt.Equal(time.Unix(1, 0)) || checked.SessionContractStatus != "changed" {
		t.Fatalf("persisted report %+v", checked)
	}
	// Existing history rows predate this additive field and still load normally.
	old := r.report
	old.SessionContractStatus = ""
	raw, _ := json.Marshal(old)
	if _, err = pool.Exec(ctx, `INSERT INTO mcp_server_checks(workspace_id,server_id,report) VALUES($1,$2,$3)`, actor.WorkspaceID, config.ID, raw); err != nil {
		t.Fatal(err)
	}
	page, err := svc.Checks(ctx, actor, config.ID, 1, 0)
	if err != nil || len(page.Items) != 1 || page.Items[0].SessionContractStatus != "" || page.NextCursor == "" {
		t.Fatalf("legacy first page %+v err=%v", page, err)
	}
	page, err = svc.Checks(ctx, actor, config.ID, 1, page.Items[0].ID)
	if err != nil || len(page.Items) != 1 || page.Items[0].SessionContractStatus != "changed" || page.Items[0].Tools[0].Code != "session_contract_changed" {
		t.Fatalf("fresh-session history %+v err=%v", page, err)
	}
	if _, err = svc.Checks(ctx, outsider, config.ID, 20, 0); !errors.Is(err, core.ErrNotFound) {
		t.Fatal("cross-workspace history", err)
	}
}
