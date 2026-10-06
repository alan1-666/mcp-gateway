package upstreamoauth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

const maxJSON = 64 << 10

type resourceMetadata struct {
	Resource string   `json:"resource"`
	Issuers  []string `json:"authorization_servers"`
	Scopes   []string `json:"scopes_supported"`
}
type issuerMetadata struct {
	Issuer         string   `json:"issuer"`
	Authorization  string   `json:"authorization_endpoint"`
	Token          string   `json:"token_endpoint"`
	PKCE           []string `json:"code_challenge_methods_supported"`
	Methods        []string `json:"token_endpoint_auth_methods_supported"`
	Responses      []string `json:"response_types_supported"`
	Grants         []string `json:"grant_types_supported"`
	IssuerResponse bool     `json:"authorization_response_iss_parameter_supported"`
}

// Every metadata URL uses the same checked dialer as MCP traffic. A provider's
// document cannot authorize another origin or make redirects bypass that policy.
func (s *Service) client(ctx context.Context, workspace, endpoint string) (*http.Client, func(), error) {
	if _, err := httpsURL(endpoint); err != nil {
		return nil, nil, err
	}
	return s.policy.NewClientContext(ctx, workspace, endpoint, "", exchangeTimeout)
}
func (s *Service) fetchJSON(ctx context.Context, workspace, endpoint string, out any) error {
	client, closeClient, err := s.client(ctx, workspace, endpoint)
	if err != nil {
		return err
	}
	defer closeClient()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return core.ErrInvalid
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: OAuth metadata request failed", core.ErrInvalid)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: OAuth metadata is unavailable", core.ErrInvalid)
	}
	return decodeJSON(resp.Body, out)
}
func decodeJSON(body io.Reader, out any) error {
	raw, err := io.ReadAll(io.LimitReader(body, maxJSON+1))
	if err != nil || len(raw) > maxJSON {
		return fmt.Errorf("%w: OAuth response exceeds its supported size", core.ErrInvalid)
	}
	if _, err = core.DecodeResult(raw); err != nil {
		return fmt.Errorf("%w: OAuth response is not supported JSON", core.ErrInvalid)
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return fmt.Errorf("%w: OAuth response must be a JSON object", core.ErrInvalid)
	}
	if uniqueJSON(raw) != nil || json.Unmarshal(raw, out) != nil {
		return fmt.Errorf("%w: OAuth response is malformed", core.ErrInvalid)
	}
	return nil
}
func (s *Service) Discover(ctx context.Context, a core.Actor, id, issuer string) (Metadata, error) {
	if err := administrator(ctx, a); err != nil {
		return Metadata{}, err
	}
	server, err := s.server(ctx, a.WorkspaceID, id)
	if err != nil {
		return Metadata{}, err
	}
	if !server.Enabled || server.CredentialRef != "" {
		return Metadata{}, fmt.Errorf("%w: OAuth requires an enabled server without a static credential", core.ErrConflict)
	}
	ctx, cancel := context.WithTimeout(ctx, exchangeTimeout)
	defer cancel()
	return s.discover(ctx, a.WorkspaceID, server.URL, issuer)
}
func (s *Service) discover(ctx context.Context, workspace, resource, issuer string) (Metadata, error) {
	result := Metadata{Resource: resource, RedirectURI: s.redirect, ScopesSupported: []string{}, AuthMethods: []string{}}
	u, err := httpsURL(resource)
	if err != nil {
		return result, err
	}
	client, closeClient, err := s.client(ctx, workspace, resource)
	if err != nil {
		return result, err
	}
	// An unauthenticated initialize probes only protocol authentication. It also
	// works for servers that reject GET because they do not expose standalone SSE.
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, resource, strings.NewReader(`{"jsonrpc":"2.0","id":"oauth-discovery","method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"rillgate-oauth-discovery","version":"1"}}}`))
	req.GetBody = nil
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("MCP-Protocol-Version", "2025-11-25")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := client.Do(req)
	metadataURL := ""
	if err != nil {
		closeClient()
		return result, fmt.Errorf("%w: could not inspect the MCP authentication challenge", core.ErrInvalid)
	}
	if resp.StatusCode == http.StatusUnauthorized {
		challenges, e := oauthex.ParseWWWAuthenticate(resp.Header.Values("WWW-Authenticate"))
		if e != nil {
			resp.Body.Close()
			closeClient()
			return result, fmt.Errorf("%w: invalid MCP authentication challenge", core.ErrInvalid)
		}
		for _, c := range challenges {
			if strings.EqualFold(c.Scheme, "Bearer") {
				if candidate := c.Params["resource_metadata"]; candidate != "" {
					if metadataURL != "" && metadataURL != candidate {
						resp.Body.Close()
						closeClient()
						return result, core.ErrInvalid
					}
					metadataURL = candidate
				}
			}
		}
	}
	resp.Body.Close()
	// If an anonymous endpoint unexpectedly created a session, release it once.
	if session := resp.Header.Get("Mcp-Session-Id"); session != "" && len(session) <= 256 {
		cleanup, _ := http.NewRequestWithContext(ctx, http.MethodDelete, resource, nil)
		cleanup.Header.Set("Mcp-Session-Id", session)
		cleanup.Header.Set("MCP-Protocol-Version", "2025-11-25")
		if response, e := client.Do(cleanup); e == nil {
			response.Body.Close()
		}
	}
	closeClient()
	candidates := []string{metadataURL}
	if metadataURL == "" {
		origin := u.Scheme + "://" + u.Host
		candidates = []string{origin + "/.well-known/oauth-protected-resource" + u.EscapedPath()}
		if u.Path != "" {
			candidates = append(candidates, origin+"/.well-known/oauth-protected-resource")
		}
	}
	var resourceMeta resourceMetadata
	found := false
	for _, candidate := range candidates {
		var fetched resourceMetadata
		if e := s.fetchJSON(ctx, workspace, candidate, &fetched); e == nil {
			resourceMeta = fetched
			found = true
			break
		}
	}
	if !found {
		return result, fmt.Errorf("%w: protected resource metadata is unavailable or blocked by egress policy", core.ErrInvalid)
	}
	if resourceMeta.Resource != resource || len(resourceMeta.Issuers) == 0 || len(resourceMeta.Issuers) > 16 {
		return result, fmt.Errorf("%w: metadata does not identify this exact MCP resource", core.ErrInvalid)
	}
	for _, v := range resourceMeta.Issuers {
		if _, err := httpsURL(v); err != nil {
			return result, err
		}
	}
	result.Issuers = resourceMeta.Issuers
	if len(resourceMeta.Scopes) > 64 {
		return result, core.ErrInvalid
	}
	for _, scope := range resourceMeta.Scopes {
		if !validScope(scope) {
			return result, core.ErrInvalid
		}
	}
	if resourceMeta.Scopes != nil {
		result.ScopesSupported = resourceMeta.Scopes
	}
	if issuer != "" {
		if !slices.Contains(result.Issuers, issuer) {
			return result, fmt.Errorf("%w: selected issuer is not advertised by this resource", core.ErrInvalid)
		}
		return s.discoverIssuer(ctx, workspace, result, issuer)
	}
	for _, candidate := range result.Issuers {
		if discovered, e := s.discoverIssuer(ctx, workspace, result, candidate); e == nil {
			return discovered, nil
		}
	}
	return result, fmt.Errorf("%w: no allowed issuer supports authorization code, PKCE S256 and RFC 9207 issuer responses", core.ErrInvalid)
}
func (s *Service) discoverIssuer(ctx context.Context, workspace string, result Metadata, issuer string) (Metadata, error) {
	iu, _ := httpsURL(issuer)
	origin := iu.Scheme + "://" + iu.Host
	candidates := []string{origin + "/.well-known/oauth-authorization-server" + iu.EscapedPath(), origin + "/.well-known/openid-configuration" + iu.EscapedPath()}
	if iu.Path != "" {
		candidates = append(candidates, strings.TrimSuffix(issuer, "/")+"/.well-known/openid-configuration")
	}
	var meta issuerMetadata
	found := false
	for _, candidate := range candidates {
		var fetched issuerMetadata
		if e := s.fetchJSON(ctx, workspace, candidate, &fetched); e == nil {
			meta = fetched
			found = true
			break
		}
	}
	if !found || meta.Issuer != issuer || !meta.IssuerResponse || !slices.Contains(meta.PKCE, "S256") || !slices.Contains(meta.Responses, "code") || (len(meta.Grants) > 0 && !slices.Contains(meta.Grants, "authorization_code")) {
		return result, fmt.Errorf("%w: issuer metadata must match and support authorization code, PKCE S256 and RFC 9207 issuer responses", core.ErrInvalid)
	}
	for _, endpoint := range []string{meta.Authorization, meta.Token} {
		_, closeEndpoint, e := s.client(ctx, workspace, endpoint)
		if e != nil {
			return result, fmt.Errorf("%w: OAuth endpoint is invalid or its origin is not allowed", core.ErrInvalid)
		}
		closeEndpoint()
	}
	if len(meta.Methods) == 0 {
		meta.Methods = []string{"client_secret_basic"}
	}
	for _, method := range []string{"none", "client_secret_basic"} {
		if slices.Contains(meta.Methods, method) {
			result.AuthMethods = append(result.AuthMethods, method)
		}
	}
	if len(result.AuthMethods) == 0 {
		return result, fmt.Errorf("%w: issuer requires unsupported client authentication", core.ErrInvalid)
	}
	result.Issuer, result.AuthorizationEndpoint, result.TokenEndpoint = issuer, meta.Authorization, meta.Token
	return result, nil
}
func validScope(scope string) bool {
	if scope == "" || len(scope) > 256 {
		return false
	}
	for _, ch := range []byte(scope) {
		if ch < 0x21 || ch > 0x7e || ch == '"' || ch == '\\' {
			return false
		}
	}
	return true
}

func (s *Service) exchange(ctx context.Context, workspace string, c storedConfig, secret []byte, values url.Values) (grant, error) {
	client, closeClient, err := s.client(ctx, workspace, c.TokenEndpoint)
	if err != nil {
		return grant{}, err
	}
	defer closeClient()
	values.Set("client_id", c.ClientID)
	values.Set("resource", c.Resource)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, c.TokenEndpoint, strings.NewReader(values.Encode()))
	req.GetBody = nil // The code and rotated refresh token are single use, even on transport failure.
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if c.AuthMethod == "client_secret_basic" {
		req.SetBasicAuth(url.QueryEscape(c.ClientID), url.QueryEscape(string(secret)))
	}
	resp, err := client.Do(req)
	if err != nil {
		return grant{}, fmt.Errorf("OAuth token exchange failed; reconnect is required")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return grant{}, fmt.Errorf("OAuth token exchange was rejected; reconnect is required")
	}
	var token grant
	if err = decodeJSON(resp.Body, &token); err != nil {
		return grant{}, fmt.Errorf("OAuth token response is invalid; reconnect is required")
	}
	if !strings.EqualFold(token.TokenType, "Bearer") || !validToken(token.AccessToken) || len(token.RefreshToken) > 8192 || strings.ContainsAny(token.RefreshToken, "\r\n\x00") || (token.ExpiresIn != nil && (*token.ExpiresIn <= 0 || *token.ExpiresIn > 31536000)) {
		return grant{}, fmt.Errorf("OAuth token response is unsupported; reconnect is required")
	}
	if token.Scope != "" {
		for _, scope := range strings.Fields(token.Scope) {
			if !validScope(scope) || (len(c.Scopes) > 0 && !slices.Contains(c.Scopes, scope)) {
				return grant{}, fmt.Errorf("OAuth granted unexpected scopes; reconnect is required")
			}
		}
	}
	return token, nil
}
func validToken(v string) bool {
	if v == "" || len(v) > 8192 {
		return false
	}
	for _, ch := range []byte(v) {
		if !((ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || strings.ContainsRune("-._~+/=", rune(ch))) {
			return false
		}
	}
	return true
}
