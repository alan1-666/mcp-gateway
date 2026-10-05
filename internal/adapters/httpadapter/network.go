package httpadapter

import (
	"context"
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
	client := &http.Client{
		Transport:     &scopedTransport{base: transport, origin: origin, headers: headers},
		Timeout:       timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return client, transport.CloseIdleConnections, nil
}

func (a *Adapter) newTransport(timeout time.Duration) *http.Transport {
	return &http.Transport{DialContext: a.dial, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: timeout, DisableKeepAlives: true, MaxResponseHeaderBytes: 32 << 10}
}

type scopedTransport struct {
	base    http.RoundTripper
	origin  string
	headers http.Header
}

func (t *scopedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.User != nil || req.URL.Fragment != "" || req.URL.Scheme+"://"+req.URL.Host != t.origin {
		return nil, fmt.Errorf("destination is outside the configured origin")
	}
	req = req.Clone(req.Context())
	for name, values := range t.headers {
		req.Header[name] = append([]string(nil), values...)
	}
	return t.base.RoundTrip(req)
}
