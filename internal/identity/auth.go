package identity

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/alan1-666/mcp-gateway/internal/core"
)

type Token struct {
	Token string `json:"token"`
	core.Actor
}
type Auth struct {
	actors  map[[32]byte]core.Actor
	origins map[string]bool
	cloud   *Cloud
}
type actorKey struct{}

func New(tokens []Token, origins []string) (*Auth, error) {
	a := &Auth{actors: map[[32]byte]core.Actor{}, origins: map[string]bool{}}
	for _, t := range tokens {
		if len(t.Token) < 32 || t.ID == "" || t.WorkspaceID == "" {
			return nil, fmt.Errorf("identity must have a token of at least 32 characters, id and workspace_id")
		}
		switch t.Role {
		case core.RoleAdmin, core.RoleOperator, core.RoleApprover, core.RoleViewer:
		default:
			return nil, fmt.Errorf("invalid identity role")
		}
		h := sha256.Sum256([]byte(t.Token))
		if _, exists := a.actors[h]; exists {
			return nil, fmt.Errorf("duplicate identity token")
		}
		a.actors[h] = t.Actor
	}
	if len(a.actors) == 0 {
		return nil, fmt.Errorf("at least one identity is required")
	}
	for _, origin := range origins {
		if origin != "" {
			a.origins[origin] = true
		}
	}
	return a, nil
}

func FromFile(path string, origins []string) (*Auth, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read identity file: %w", err)
	}
	var tokens []Token
	if err := json.Unmarshal(data, &tokens); err != nil {
		return nil, fmt.Errorf("identity file must contain a JSON array of identities")
	}
	return New(tokens, origins)
}

func Actor(ctx context.Context) core.Actor { a, _ := ctx.Value(actorKey{}).(core.Actor); return a }

func (a *Auth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if origin := r.Header.Get("Origin"); origin != "" && !a.origins[origin] {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"code":"forbidden","message":"origin is not allowed"}}`))
			return
		}
		if a.cloud != nil {
			a.cloud.authenticate(next, w, r)
			return
		}
		parts := strings.Fields(r.Header.Get("Authorization"))
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			unauthorized(w)
			return
		}
		actor, ok := a.actors[sha256.Sum256([]byte(parts[1]))]
		if !ok {
			unauthorized(w)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), actorKey{}, actor)))
	})
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("WWW-Authenticate", `Bearer realm="mcp-gateway"`)
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`{"error":{"code":"unauthorized","message":"a valid bearer token is required"}}`))
}
