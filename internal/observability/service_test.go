package observability_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/alan1-666/mcp-gateway/internal/clients"
	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/alan1-666/mcp-gateway/internal/observability"
	"github.com/alan1-666/mcp-gateway/internal/store/postgres"
	"github.com/alan1-666/mcp-gateway/migrations"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fixture struct {
	db                        *pgxpool.Pool
	s                         *observability.Service
	core                      *core.Service
	admin, operator, approver core.Actor
	tool                      core.Tool
}

func setup(t *testing.T) fixture {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required")
	}
	ctx := context.Background()
	db, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if err = migrations.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	f := fixture{db: db, s: observability.New(db), core: core.NewService(postgres.New(db)), admin: core.Actor{ID: core.NewID(), WorkspaceID: core.NewID(), Role: core.RoleAdmin}}
	f.operator = core.Actor{ID: core.NewID(), WorkspaceID: f.admin.WorkspaceID, Role: core.RoleOperator}
	f.approver = core.Actor{ID: core.NewID(), WorkspaceID: f.admin.WorkspaceID, Role: core.RoleApprover}
	t.Cleanup(func() {
		for _, table := range []string{"operation_reconciliations", "operation_events", "audit_events", "operations", "gateway_clients", "tools"} {
			if _, err := db.Exec(context.Background(), "DELETE FROM "+table+" WHERE workspace_id=$1", f.admin.WorkspaceID); err != nil {
				t.Error(err)
			}
		}
	})
	f.tool, err = f.core.CreateTool(ctx, f.admin, core.ToolInput{Name: "observe", Description: "test tool", Risk: core.RiskRead, InputSchema: json.RawMessage(`{"type":"object"}`), HTTP: core.HTTPConfig{URL: "https://example.test/read", Method: "GET"}})
	if err != nil {
		t.Fatal(err)
	}
	f.tool, err = f.core.PublishTool(ctx, f.admin, f.tool.ID)
	if err != nil {
		t.Fatal(err)
	}
	return f
}
func unknown(t *testing.T, f fixture, actor, dispatcher core.Actor) core.Operation {
	t.Helper()
	ctx := context.Background()
	op, err := f.core.Prepare(ctx, actor, core.PrepareInput{ToolID: f.tool.ID, Arguments: json.RawMessage(`{}`), IdempotencyKey: core.NewID()})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, claimed, err := f.core.Claim(ctx, dispatcher, op.ID); err != nil || !claimed {
		t.Fatalf("claim: %v", err)
	}
	op, err = f.core.Finish(ctx, dispatcher, op.ID, core.FinishInput{State: core.StateUnknown, Error: "unconfirmed outcome"})
	if err != nil {
		t.Fatal(err)
	}
	return op
}
func TestReconciliationIsAppendOnlyIndependentAndNeverDispatches(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	op := unknown(t, f, f.operator, f.operator)
	in := observability.ReconcileInput{ExpectedLastID: "0", Outcome: "confirmed_success", EvidenceRef: "INC-123", Note: "fixture-private-note"}
	self := f.operator
	self.Role = core.RoleApprover
	if _, err := f.s.Reconcile(ctx, self, op.ID, in); !errors.Is(err, core.ErrForbidden) {
		t.Fatal("requester self-reconciled")
	}
	for _, a := range []core.Actor{f.operator, {ID: core.NewID(), WorkspaceID: core.NewID(), Role: core.RoleAdmin}} {
		_, err := f.s.Reconcile(ctx, a, op.ID, in)
		if err == nil {
			t.Fatal("unauthorized reconciliation succeeded")
		}
	}
	var wins, conflicts atomic.Int32
	var group sync.WaitGroup
	for range 10 {
		group.Go(func() {
			_, err := f.s.Reconcile(ctx, f.admin, op.ID, in)
			if err == nil {
				wins.Add(1)
			} else if errors.Is(err, core.ErrConflict) {
				conflicts.Add(1)
			} else {
				t.Error(err)
			}
		})
	}
	group.Wait()
	if wins.Load() != 1 || conflicts.Load() != 9 {
		t.Fatalf("concurrent append wins=%d conflicts=%d", wins.Load(), conflicts.Load())
	}
	page, err := f.s.Reconciliations(ctx, f.operator, op.ID, observability.Filter{Limit: 1})
	if err != nil || len(page.Items) != 1 || page.Items[0].Outcome != "confirmed_success" {
		t.Fatalf("owner history %+v %v", page, err)
	}
	first := page.Items[0]
	in.ExpectedLastID = first.ID
	in.Outcome = "inconclusive"
	in.Note = "Further evidence supersedes the earlier observation"
	second, err := f.s.Reconcile(ctx, f.approver, op.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	page, err = f.s.Reconciliations(ctx, f.admin, op.ID, observability.Filter{Limit: 1})
	if err != nil || page.Total != 2 || page.Items[0].ID != second.ID || page.NextCursor == "" {
		t.Fatalf("latest %+v %v", page, err)
	}
	older, err := f.s.Reconciliations(ctx, f.admin, op.ID, observability.Filter{Limit: 1, Cursor: page.NextCursor})
	if err != nil || len(older.Items) != 1 || older.Items[0].ID != first.ID || older.Items[0].Outcome != "confirmed_success" {
		t.Fatalf("history was overwritten %+v %v", older, err)
	}
	actual, err := f.core.GetOperation(ctx, f.admin, op.ID)
	if err != nil || actual.State != core.StateUnknown || actual.Error != op.Error {
		t.Fatal("reconciliation rewrote operation outcome")
	}
	var dispatches int
	if err = f.db.QueryRow(ctx, `SELECT count(*) FROM operation_events WHERE workspace_id=$1 AND operation_id=$2 AND type='OPERATION_DISPATCHING'`, f.admin.WorkspaceID, op.ID).Scan(&dispatches); err != nil || dispatches != 1 {
		t.Fatal("reconciliation dispatched business action")
	}
	var audit []byte
	if err = f.db.QueryRow(ctx, `SELECT jsonb_agg(data) FROM audit_events WHERE workspace_id=$1 AND action='OPERATION_RECONCILIATION_ADDED'`, f.admin.WorkspaceID).Scan(&audit); err != nil || strings.Contains(string(audit), "fixture-private-note") || strings.Contains(string(audit), "INC-123") {
		t.Fatal("raw evidence leaked into audit")
	}
	if _, err = f.db.Exec(ctx, `UPDATE operation_reconciliations SET outcome='confirmed_failure' WHERE id=$1`, first.ID); err == nil {
		t.Fatal("evidence row was mutable")
	}
	otherOp := unknown(t, f, f.operator, f.admin)
	if _, err = f.s.Reconcile(ctx, f.admin, otherOp.ID, observability.ReconcileInput{ExpectedLastID: "0", Outcome: "inconclusive", EvidenceRef: "INC-2", Note: "check"}); !errors.Is(err, core.ErrForbidden) {
		t.Fatal("actual dispatching actor self-reconciled")
	}
}
func TestOperationAndAuditPaginationScopeFiltersAndStableInsertion(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	snapshot, _ := json.Marshal(f.tool)
	_, err := f.db.Exec(ctx, `INSERT INTO operations(workspace_id,id,tool_id,tool_name,tool_version,risk,actor_id,arguments,arguments_hash,idempotency_key,tool_snapshot,state,created_at)
 SELECT $1,'seed-'||lpad(n::text,4,'0'),$2,'observe',1,'read',$3,'{}'::jsonb,'hash','seed-key-'||n,$4::jsonb,'READY',clock_timestamp()-interval '1 day' FROM generate_series(1,205)n`, f.admin.WorkspaceID, f.tool.ID, f.operator.ID, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	in := observability.Filter{Limit: 73, State: core.StateReady, ActorID: f.operator.ID, ToolID: f.tool.ID}
	page, err := f.s.Operations(ctx, f.operator, in)
	if err != nil || len(page.Items) != 73 || page.Total != 205 {
		t.Fatalf("first page count=%d total=%d err=%v", len(page.Items), page.Total, err)
	}
	seen := map[string]bool{}
	for _, op := range page.Items {
		seen[op.ID] = true
	}
	if _, err = f.core.Prepare(ctx, f.operator, core.PrepareInput{ToolID: f.tool.ID, Arguments: json.RawMessage(`{}`), IdempotencyKey: core.NewID()}); err != nil {
		t.Fatal(err)
	}
	cursor := page.NextCursor
	for cursor != "" {
		in.Cursor = cursor
		page, err = f.s.Operations(ctx, f.operator, in)
		if err != nil || page.Total != 205 {
			t.Fatalf("unstable insert total=%d err=%v", page.Total, err)
		}
		for _, op := range page.Items {
			if seen[op.ID] {
				t.Fatal("duplicate page item")
			}
			seen[op.ID] = true
		}
		cursor = page.NextCursor
	}
	if len(seen) != 205 {
		t.Fatalf("lost old operations %d", len(seen))
	}
	in.Cursor = ""
	first, err := f.s.Operations(ctx, f.admin, in)
	if err != nil {
		t.Fatal(err)
	}
	changed := in
	changed.Cursor = first.NextCursor
	changed.State = core.StateFailed
	if _, err = f.s.Operations(ctx, f.admin, changed); !errors.Is(err, core.ErrInvalid) {
		t.Fatal("cursor accepted changed filter")
	}
	outsider := f.operator
	outsider.ID = core.NewID()
	hidden, err := f.s.Operations(ctx, outsider, observability.Filter{})
	if err != nil || hidden.Total != 0 {
		t.Fatal("another member saw operation history")
	}
	outside := f.admin
	outside.WorkspaceID = core.NewID()
	hidden, err = f.s.Operations(ctx, outside, observability.Filter{})
	if err != nil || hidden.Total != 0 {
		t.Fatal("foreign workspace leaked history")
	}
	for range 3 {
		if _, err = f.db.Exec(ctx, `INSERT INTO audit_events(workspace_id,actor_id,action,resource_id,data) VALUES($1,$2,'FILTER_ME','resource','{}')`, f.admin.WorkspaceID, f.admin.ID); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	start := now.Add(-time.Hour)
	filter := observability.Filter{ActorID: f.admin.ID, Action: "FILTER_ME", ResourceID: "resource", From: &start, To: &now, Limit: 1}
	audit, err := f.s.Audit(ctx, f.admin, filter)
	if err != nil || audit.Total != 3 || len(audit.Items) != 1 || audit.NextCursor == "" {
		t.Fatalf("audit %+v %v", audit, err)
	}
	filter.Cursor = audit.NextCursor
	audit2, err := f.s.Audit(ctx, f.admin, filter)
	if err != nil || len(audit2.Items) != 1 || audit2.Items[0].ID == audit.Items[0].ID {
		t.Fatal("audit pagination failed")
	}
	if _, err = f.s.Audit(ctx, f.approver, observability.Filter{}); !errors.Is(err, core.ErrForbidden) {
		t.Fatal("nonadmin read configuration audit")
	}
}
func TestScopedClientHistoryHonorsLiveGrantRevocation(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	svc := clients.New(f.db)
	issued, err := svc.Create(ctx, f.admin, clients.CreateInput{Name: "observer", Scopes: []string{clients.ScopeRead, clients.ScopeInvoke}, ToolIDs: []string{f.tool.ID}})
	if err != nil {
		t.Fatal(err)
	}
	actor := core.Actor{ID: issued.Client.ID, WorkspaceID: f.admin.WorkspaceID, Role: core.RoleOperator, ClientID: issued.Client.ID, ClientKeyID: issued.Client.KeyID}
	op := unknown(t, f, actor, actor)
	if _, err = f.s.Reconcile(ctx, f.admin, op.ID, observability.ReconcileInput{ExpectedLastID: "0", Outcome: "inconclusive", EvidenceRef: "INC-3", Note: "review"}); err != nil {
		t.Fatal(err)
	}
	page, err := f.s.Operations(ctx, actor, observability.Filter{})
	if err != nil || page.Total != 1 {
		t.Fatalf("client page %+v %v", page, err)
	}
	if _, err = f.s.Reconciliations(ctx, actor, op.ID, observability.Filter{}); err != nil {
		t.Fatal(err)
	}
	empty := []string{}
	if _, err = svc.Update(ctx, f.admin, issued.Client.ID, clients.UpdateInput{ExpectedVersion: 1, ToolIDs: &empty}); err != nil {
		t.Fatal(err)
	}
	page, err = f.s.Operations(ctx, actor, observability.Filter{})
	if err != nil || page.Total != 0 {
		t.Fatal("revoked client grant leaked operations")
	}
	if _, err = f.s.Reconciliations(ctx, actor, op.ID, observability.Filter{}); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("revoked grant leaked evidence %v", err)
	}
}
func TestReconciliationInputValidation(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	op := unknown(t, f, f.operator, f.operator)
	for i, bad := range []observability.ReconcileInput{{ExpectedLastID: "", Outcome: "inconclusive", EvidenceRef: "INC-1", Note: "ok"}, {ExpectedLastID: "00", Outcome: "inconclusive", EvidenceRef: "INC-1", Note: "ok"}, {ExpectedLastID: "0", Outcome: "bad", EvidenceRef: "INC-1", Note: "ok"}, {ExpectedLastID: "0", Outcome: "inconclusive", EvidenceRef: "https://user:secret@example.com/evidence", Note: "ok"}, {ExpectedLastID: "0", Outcome: "inconclusive", EvidenceRef: "https://example.com/evidence?token=secret", Note: "ok"}, {ExpectedLastID: "0", Outcome: "inconclusive", EvidenceRef: "http://example.com/evidence", Note: "ok"}, {ExpectedLastID: "0", Outcome: "inconclusive", EvidenceRef: "INC-1", Note: strings.Repeat("x", 2001)}} {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			if _, err := f.s.Reconcile(ctx, f.admin, op.ID, bad); !errors.Is(err, core.ErrInvalid) {
				t.Fatalf("accepted invalid evidence %v", err)
			}
		})
	}
	ready, err := f.core.Prepare(ctx, f.operator, core.PrepareInput{ToolID: f.tool.ID, Arguments: json.RawMessage(`{}`), IdempotencyKey: core.NewID()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Reconcile(ctx, f.admin, ready.ID, observability.ReconcileInput{ExpectedLastID: "0", Outcome: "inconclusive", EvidenceRef: "INC-1", Note: "ok"}); !errors.Is(err, core.ErrConflict) {
		t.Fatal("non-UNKNOWN operation reconciled")
	}
}
