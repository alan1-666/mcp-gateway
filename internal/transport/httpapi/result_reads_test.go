package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/alan1-666/mcp-gateway/internal/identity"
)

type resultFixture struct {
	core.Repository
	input core.ResultReadInput
	fail  bool
}

func (r *resultFixture) ReadResult(_ context.Context, _ core.Actor, in core.ResultReadInput) (core.ResultPage, error) {
	r.input = in
	if r.fail {
		return core.ResultPage{}, core.ErrNotFound
	}
	return core.ResultPage{Chunk: "{}"}, nil
}
func TestResultReadQueryContract(t *testing.T) {
	in, err := parseResultRead("operation", "cursor=part%2Btwo&limit_bytes=1024")
	if err != nil || in.OperationID != "operation" || in.Cursor != "part+two" || in.LimitBytes != 1024 {
		t.Fatalf("invalid parsed query %+v %v", in, err)
	}
	for _, query := range []string{"cursor=%zz", "cursor=a&cursor=b", "limit_bytes=0", "limit_bytes=bad", "limit_bytes=16385", "offset=10"} {
		if _, err := parseResultRead("op", query); !errors.Is(err, core.ErrInvalid) {
			t.Fatalf("accepted %s", query)
		}
	}
}
func TestResultReadHTTPBoundary(t *testing.T) {
	repo := &resultFixture{}
	api := &API{Service: core.NewService(repo)}
	mux := http.NewServeMux()
	api.registerResultReads(mux)
	auth, err := identity.New([]identity.Token{{Token: "fixture-key-with-at-least-thirty-two-characters", Actor: core.Actor{ID: "reader", WorkspaceID: "team", Role: core.RoleOperator}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := auth.Middleware(mux)
	for _, tc := range []struct {
		query, token string
		fail         bool
		status       int
	}{
		{"", "fixture-key-with-at-least-thirty-two-characters", false, 200}, {"?limit_bytes=1024&cursor=next", "fixture-key-with-at-least-thirty-two-characters", false, 200},
		{"?unknown=1", "fixture-key-with-at-least-thirty-two-characters", false, 400}, {"", "fixture-key-with-at-least-thirty-two-characters", true, 404}, {"", "", false, 401},
	} {
		repo.fail = tc.fail
		r := httptest.NewRequest("GET", "/api/v1/operations/op/result"+tc.query, nil)
		if tc.token != "" {
			r.Header.Set("Authorization", "Bearer "+tc.token)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("status=%d want=%d", w.Code, tc.status)
		}
	}
	if repo.input.OperationID != "op" {
		t.Fatal("operation id lost")
	}
}
