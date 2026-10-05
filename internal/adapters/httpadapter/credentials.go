package httpadapter

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/alan1-666/mcp-gateway/internal/core"
)

// CredentialResolver distinguishes an absent managed reference from lookup or
// decryption failure. Only absence permits a configured static credential.
type CredentialResolver interface {
	Resolve(context.Context, string, string, string) (Credential, bool, error)
}

// SetCredentialResolver is startup configuration; call before serving requests.
func (a *Adapter) SetCredentialResolver(resolver CredentialResolver) { a.resolver = resolver }
func (a *Adapter) HasStaticCredential(workspace, ref string) bool {
	_, ok := a.credentials[workspace+"/"+ref]
	return ok
}
func (a *Adapter) ValidateContext(ctx context.Context, workspace string, cfg core.HTTPConfig) error {
	_, err := a.resolveCredential(ctx, workspace, cfg)
	return err
}
func (a *Adapter) resolveCredential(ctx context.Context, workspace string, cfg core.HTTPConfig) (Credential, error) {
	u, err := url.Parse(cfg.URL)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" || u.ForceQuery || !a.origins[u.Scheme+"://"+u.Host] {
		return Credential{}, fmt.Errorf("%w: tool URL requires an explicitly allowed origin and must not contain credentials, query or fragment", core.ErrInvalid)
	}
	if cfg.CredentialRef == "" {
		return Credential{}, nil
	}
	origin := u.Scheme + "://" + u.Host
	static, exists := a.credentials[workspace+"/"+cfg.CredentialRef]
	if a.resolver != nil {
		managed, found, err := a.resolver.Resolve(ctx, workspace, origin, cfg.CredentialRef)
		if err != nil {
			return Credential{}, fmt.Errorf("%w: credential resolution failed", core.ErrInvalid)
		}
		if found {
			if exists {
				return Credential{}, fmt.Errorf("%w: credential reference has ambiguous sources", core.ErrInvalid)
			}
			if managed.WorkspaceID != workspace || managed.Ref != cfg.CredentialRef || managed.Origin != origin {
				return Credential{}, fmt.Errorf("%w: credential scope mismatch", core.ErrInvalid)
			}
			return managed, nil
		}
	}
	if !exists || static.Origin != origin {
		return Credential{}, fmt.Errorf("%w: credential reference is not bound to this workspace and origin", core.ErrInvalid)
	}
	return static, nil
}

// Compatibility entry points are bounded even when their caller has no context.
func validationContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 5*time.Second)
}
