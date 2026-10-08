package httpadapter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/alan1-666/mcp-gateway/internal/core"
)

// NewClient builds a fresh outbound client scoped to a workspace, an approved
// origin and one credential reference. It never forwards inbound request
// headers, uses the same DNS policy as HTTP tools, and never follows redirects.
// Call closeClient after the entire protocol session has closed.
func (a *Adapter) NewClient(workspace, endpoint, credentialRef string, timeout time.Duration) (*http.Client, func(), error) {
	ctx, cancel := validationContext()
	defer cancel()
	return a.NewClientContext(ctx, workspace, endpoint, credentialRef, timeout)
}
func (a *Adapter) NewClientContext(ctx context.Context, workspace, endpoint, credentialRef string, timeout time.Duration) (*http.Client, func(), error) {
	return a.newClientContext(ctx, workspace, endpoint, credentialRef, timeout, false)
}

// NewSessionClientContext permits TCP reuse only for an exclusively leased MCP
// session. Credential identity is revalidated before every network request.
func (a *Adapter) NewSessionClientContext(ctx context.Context, workspace, endpoint, credentialRef string, timeout time.Duration) (*http.Client, func(), error) {
	return a.newClientContext(ctx, workspace, endpoint, credentialRef, timeout, true)
}

func (a *Adapter) newClientContext(ctx context.Context, workspace, endpoint, credentialRef string, timeout time.Duration, reusable bool) (*http.Client, func(), error) {
	if timeout < 100*time.Millisecond || timeout > 120*time.Second {
		return nil, nil, fmt.Errorf("%w: invalid downstream timeout", core.ErrInvalid)
	}
	credential, err := a.resolveCredential(ctx, workspace, core.HTTPConfig{URL: endpoint, CredentialRef: credentialRef})
	if err != nil {
		return nil, nil, err
	}
	u, _ := url.Parse(endpoint)
	origin := u.Scheme + "://" + u.Host
	headers := make(http.Header)
	if credentialRef != "" {
		for key, value := range credential.Headers {
			// Authentication configuration must not override protocol negotiation,
			// session identity or headers derived from reviewed tool parameters.
			lower := strings.ToLower(key)
			if lower == "accept" || lower == "content-type" || strings.HasPrefix(lower, "mcp-") {
				return nil, nil, fmt.Errorf("%w: credential contains a reserved protocol header", core.ErrInvalid)
			}
			headers.Set(key, value)
		}
	}
	transport := a.newTransport(timeout)
	scoped := &scopedTransport{base: transport, origin: origin, headers: headers}
	if reusable {
		transport.DisableKeepAlives = false
		transport.MaxIdleConns = 1
		transport.MaxIdleConnsPerHost = 1
		transport.IdleConnTimeout = time.Minute
		scoped.identity = credentialIdentity(credential)
		scoped.validate = func(ctx context.Context) error {
			current, err := a.resolveCredential(ctx, workspace, core.HTTPConfig{URL: endpoint, CredentialRef: credentialRef})
			if err != nil || credentialIdentity(current) != scoped.identity {
				return fmt.Errorf("session credential is unavailable or changed")
			}
			return nil
		}
	}
	client := &http.Client{
		Transport:     scoped,
		Timeout:       timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return client, transport.CloseIdleConnections, nil
}

func (a *Adapter) newTransport(timeout time.Duration) *http.Transport {
	return &http.Transport{DialContext: a.dial, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: timeout, DisableKeepAlives: true, MaxResponseHeaderBytes: 32 << 10}
}

type scopedTransport struct {
	base     http.RoundTripper
	origin   string
	headers  http.Header
	identity string
	validate func(context.Context) error
}

func (t *scopedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.User != nil || req.URL.Fragment != "" || req.URL.Scheme+"://"+req.URL.Host != t.origin {
		return nil, fmt.Errorf("destination is outside the configured origin")
	}
	if t.validate != nil {
		if err := t.validate(req.Context()); err != nil {
			return nil, err
		}
	}
	req = req.Clone(req.Context())
	for name, values := range t.headers {
		req.Header[name] = append([]string(nil), values...)
	}
	return t.base.RoundTrip(req)
}

// SessionIdentity is an opaque in-memory key; callers must not log or persist it.
func (t *scopedTransport) SessionIdentity() string { return t.identity }

func credentialIdentity(c Credential) string {
	raw, _ := json.Marshal(struct {
		Workspace, Ref, Origin string
		Version                int
		Headers                map[string]string
	}{c.WorkspaceID, c.Ref, c.Origin, c.Version, c.Headers})
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}
