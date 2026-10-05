package httpapi

import (
	"context"
	"encoding/json"
	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/alan1-666/mcp-gateway/internal/identity"
	"github.com/alan1-666/mcp-gateway/internal/observability"
	"github.com/alan1-666/mcp-gateway/internal/store/postgres"
	"github.com/alan1-666/mcp-gateway/migrations"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestHistoryQueryValidation(t *testing.T) {
	for _, tc := range []struct{ kind, query string }{{"operations", "limit=0"}, {"audit", "limit=101"}, {"audit", "actor_id=a&actor_id=b"}, {"audit", "from=yesterday"}, {"operations", "action=CREATED"}, {"audit", "tool_id=abc"}, {"reconciliations", "actor_id=a"}, {"reconciliations", "from=2026-10-05T00:00:00Z"}, {"operations", "offset=10"}, {"audit", "cursor=%GG"}, {"audit", "limit=1;x=2"}} {
		if _, err := historyQuery(httptest.NewRequest("GET", "/history?"+tc.query, nil), tc.kind); err == nil {
			t.Fatalf("accepted %s %s", tc.kind, tc.query)
		}
	}
	in, err := historyQuery(httptest.NewRequest("GET", "/history?state=UNKNOWN&actor_id=alice&tool_id=one&from=2026-10-01T00:00:00Z&to=2026-10-05T00:00:00Z&limit=10", nil), "operations")
	if err != nil || in.Limit != 10 || in.State != core.StateUnknown || in.From == nil || in.To == nil {
		t.Fatalf("valid history query %+v %v", in, err)
	}
}
func TestObservabilityHTTPPermissionsAndNoStateRewrite(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required")
	}
	ctx := context.Background()
	db, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = migrations.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	admin := core.Actor{ID: core.NewID(), WorkspaceID: core.NewID(), Role: core.RoleAdmin}
	operator := core.Actor{ID: core.NewID(), WorkspaceID: admin.WorkspaceID, Role: core.RoleOperator}
	foreign := admin
	foreign.WorkspaceID = core.NewID()
	defer func() {
		for _, table := range []string{"operation_reconciliations", "operation_events", "audit_events", "operations", "tools"} {
			if _, err := db.Exec(ctx, "DELETE FROM "+table+" WHERE workspace_id=$1", admin.WorkspaceID); err != nil {
				t.Error(err)
			}
		}
	}()
	svc := core.NewService(postgres.New(db))
	tool, err := svc.CreateTool(ctx, admin, core.ToolInput{Name: "http_observe", Description: "test", Risk: core.RiskRead, InputSchema: json.RawMessage(`{"type":"object"}`), HTTP: core.HTTPConfig{URL: "https://example.test/query", Method: "GET"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.PublishTool(ctx, admin, tool.ID); err != nil {
		t.Fatal(err)
	}
	op, err := svc.Prepare(ctx, operator, core.PrepareInput{ToolID: tool.ID, Arguments: json.RawMessage(`{}`), IdempotencyKey: core.NewID()})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = svc.Claim(ctx, operator, op.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Finish(ctx, operator, op.ID, core.FinishInput{State: core.StateUnknown}); err != nil {
		t.Fatal(err)
	}
	tokens := []identity.Token{{Token: strings.Repeat("a", 32), Actor: admin}, {Token: strings.Repeat("o", 32), Actor: operator}, {Token: strings.Repeat("f", 32), Actor: foreign}}
	auth, err := identity.New(tokens, nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := (&API{Service: svc, Observability: observability.New(db)}).Handler(auth)
	call := func(method, path, token, body string, want int) []byte {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != want {
			t.Fatalf("%s %s: %d want%d: %s", method, path, res.Code, want, res.Body.String())
		}
		return res.Body.Bytes()
	}
	a, o, f := tokens[0].Token, tokens[1].Token, tokens[2].Token
	call("GET", "/api/v1/audit", "", "", 401)
	call("GET", "/api/v1/audit", o, "", 403)
	call("GET", "/api/v1/audit?action=TOOL_CREATED&limit=1", a, "", 200)
	payload := call("GET", "/api/v1/operations?state=UNKNOWN&limit=1", o, "", 200)
	var page observability.Page[core.Operation]
	if json.Unmarshal(payload, &page) != nil || page.Total != 1 || page.Items[0].ID != op.ID {
		t.Fatal("operation page contract mismatch")
	}
	call("GET", "/api/v1/operations?state=invalid", a, "", 400)
	path := "/api/v1/operations/" + op.ID + "/reconciliations"
	body := `{"expected_last_id":"0","outcome":"confirmed_success","evidence_ref":"https://example.test/evidence/1","note":"Business receipt verified"}`
	call("POST", path, o, body, 403)
	call("POST", path, f, body, 404)
	call("POST", path, a, strings.Replace(body, `"0"`, `0`, 1), 400)
	call("POST", path, a, body, 200)
	call("POST", path, a, body, 409)
	call("GET", path, o, "", 200)
	call("GET", path, f, "", 404)
	actual, err := svc.GetOperation(ctx, admin, op.ID)
	if err != nil || actual.State != core.StateUnknown {
		t.Fatal("HTTP reconciliation changed original state")
	}
}
