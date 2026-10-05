package runs

import (
	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/alan1-666/mcp-gateway/internal/identity"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMachineClientsCannotUseHumanAgentTasks(t *testing.T) {
	token := strings.Repeat("client", 8)
	auth, err := identity.New([]identity.Token{{Token: token, Actor: core.Actor{ID: "app", WorkspaceID: "team", Role: core.RoleOperator, ClientID: "app", ClientKeyID: "key"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := (&API{}).PublicHandler(auth)
	for _, path := range []string{"/api/v1/runs", "/api/v1/runs/runtime", "/api/v1/runs/guessed", "/api/v1/runs/guessed/events", "/api/v1/runs/guessed/cancel"} {
		for _, method := range []string{"GET", "POST"} {
			r := httptest.NewRequest(method, path, strings.NewReader(`{}`))
			r.Header.Set("Authorization", "Bearer "+token)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != 403 {
				t.Fatal(path, method, w.Code)
			}
		}
	}
}
