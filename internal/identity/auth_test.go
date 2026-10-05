package identity

import (
	"github.com/alan1-666/mcp-gateway/internal/core"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTrustedIdentityAndOrigin(t *testing.T) {
	token := strings.Repeat("a", 32)
	a, err := New([]Token{{Token: token, Actor: core.Actor{ID: "alice", WorkspaceID: "one", Role: core.RoleOperator}}}, []string{"https://console.example"})
	if err != nil {
		t.Fatal(err)
	}
	h := a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actor := Actor(r.Context())
		if actor.WorkspaceID != "one" || actor.ID != "alice" {
			t.Fatal("untrusted identity override")
		}
		w.WriteHeader(204)
	}))
	for _, tc := range []struct {
		auth, origin string
		status       int
	}{{"", "", 401}, {"Bearer wrong", "", 401}, {"Bearer " + token, "https://attacker.example", 403}, {"Bearer " + token, "https://console.example", 204}} {
		r := httptest.NewRequest("GET", "/api/v1/me", nil)
		r.Header.Set("Authorization", tc.auth)
		r.Header.Set("Origin", tc.origin)
		r.Header.Set("X-Workspace-ID", "other")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Errorf("expected%d got%d", tc.status, w.Code)
		}
	}
}
