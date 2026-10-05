package httpadapter

import (
	"context"
	"errors"
	"github.com/alan1-666/mcp-gateway/internal/core"
	"testing"
)

type resolverFunc func(context.Context, string, string, string) (Credential, bool, error)

func (f resolverFunc) Resolve(ctx context.Context, w, o, r string) (Credential, bool, error) {
	return f(ctx, w, o, r)
}
func TestManagedCredentialNeverFallsBackOnFailureOrAmbiguity(t *testing.T) {
	const origin = "https://example.test"
	static := Credential{WorkspaceID: "w", Ref: "AUTH", Origin: origin, Headers: map[string]string{"Authorization": "Bearer static"}}
	adapter, _ := New([]string{origin}, nil, []Credential{static})
	cfg := core.HTTPConfig{URL: origin + "/mcp", CredentialRef: "AUTH"}
	for _, tc := range []struct {
		name  string
		found bool
		err   error
	}{{"lookup failure", false, errors.New("database secret")}, {"disabled", true, errors.New("disabled")}, {"duplicate", true, nil}} {
		t.Run(tc.name, func(t *testing.T) {
			adapter.SetCredentialResolver(resolverFunc(func(context.Context, string, string, string) (Credential, bool, error) {
				return static, tc.found, tc.err
			}))
			if adapter.ValidateContext(context.Background(), "w", cfg) == nil {
				t.Fatal("managed failure fell back to static credential")
			}
		})
	}
	adapter.SetCredentialResolver(resolverFunc(func(context.Context, string, string, string) (Credential, bool, error) {
		return Credential{}, false, nil
	}))
	if err := adapter.ValidateContext(context.Background(), "w", cfg); err != nil {
		t.Fatal("absent managed reference blocked valid static configuration")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	adapter.SetCredentialResolver(resolverFunc(func(ctx context.Context, _, _, _ string) (Credential, bool, error) {
		return Credential{}, false, ctx.Err()
	}))
	if adapter.ValidateContext(ctx, "w", cfg) == nil {
		t.Fatal("resolver ignored caller cancellation")
	}
}
