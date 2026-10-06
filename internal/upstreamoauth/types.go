// Package upstreamoauth owns upstream grants. These grants never authenticate
// incoming gateway callers and never pass through the public API as tokens.
package upstreamoauth

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/alan1-666/mcp-gateway/internal/store/postgres"
)

const CallbackPath = "/api/v1/mcp/oauth/callback"
const exchangeTimeout = 20 * time.Second

type Policy interface {
	NewClientContext(context.Context, string, string, string, time.Duration) (*http.Client, func(), error)
}
type Configuration struct {
	Issuer     string   `json:"issuer"`
	ClientID   string   `json:"client_id"`
	AuthMethod string   `json:"auth_method"`
	Scopes     []string `json:"scopes"`
}
type Metadata struct {
	Resource              string   `json:"resource"`
	Issuers               []string `json:"issuers"`
	Issuer                string   `json:"issuer"`
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	ScopesSupported       []string `json:"scopes_supported"`
	AuthMethods           []string `json:"auth_methods"`
	RedirectURI           string   `json:"redirect_uri"`
}
type ConfigInput struct {
	Configuration
	ExpectedVersion *int64 `json:"expected_version"`
	ClientSecret    string `json:"client_secret,omitempty"`
}
type VersionInput struct {
	ExpectedVersion *int64 `json:"expected_version"`
}
type Status struct {
	ServerID      string         `json:"server_id"`
	Version       int64          `json:"version"`
	Status        string         `json:"status"`
	RedirectURI   string         `json:"redirect_uri"`
	Configuration *Configuration `json:"configuration,omitempty"`
	ExpiresAt     *time.Time     `json:"expires_at,omitempty"`
	UpdatedAt     *time.Time     `json:"updated_at,omitempty"`
}
type Authorization struct {
	AuthorizationURL string    `json:"authorization_url"`
	ExpiresAt        time.Time `json:"expires_at"`
}
type storedConfig struct {
	Configuration
	Resource              string `json:"resource"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
}
type grant struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	TokenType    string `json:"token_type"`
	ExpiresIn    *int64 `json:"expires_in,omitempty"`
	Scope        string `json:"scope,omitempty"`
}
type Service struct {
	db       postgres.DB
	policy   Policy
	aead     cipher.AEAD
	redirect string
}

func New(db postgres.DB, key []byte, policy Policy, origin string) (*Service, error) {
	u, err := httpsURL(origin)
	if err != nil || u.Path != "" || len(key) != 32 || db == nil || policy == nil {
		return nil, fmt.Errorf("OAuth requires a database, encryption key, egress policy and HTTPS PUBLIC_ORIGIN")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Service{db: db, policy: policy, aead: aead, redirect: origin + CallbackPath}, nil
}
func httpsURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || len(raw) > 2048 || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.ContainsAny(raw, "\r\n\\") {
		return nil, fmt.Errorf("%w: OAuth endpoints must be HTTPS URLs without credentials, query or fragment", core.ErrInvalid)
	}
	return u, nil
}
func administrator(ctx context.Context, a core.Actor) error {
	if a.ID == "" || a.WorkspaceID == "" {
		return core.ErrUnauthorized
	}
	if a.Role != core.RoleAdmin || a.ClientID != "" {
		return core.ErrForbidden
	}
	return nil
}
func randomValue() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func digest(value string) string { h := sha256.Sum256([]byte(value)); return hex.EncodeToString(h[:]) }
func (s *Service) seal(workspace, id, purpose string, value []byte) []byte {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		panic(err)
	}
	aad, _ := json.Marshal([]string{"rillgate-upstream-oauth-v1", workspace, id, purpose})
	return s.aead.Seal(nonce, nonce, value, aad)
}
func (s *Service) open(workspace, id, purpose string, value []byte) ([]byte, error) {
	if len(value) < s.aead.NonceSize() {
		return nil, fmt.Errorf("OAuth encrypted data is unavailable")
	}
	aad, _ := json.Marshal([]string{"rillgate-upstream-oauth-v1", workspace, id, purpose})
	n := s.aead.NonceSize()
	plain, err := s.aead.Open(nil, value[:n], value[n:], aad)
	if err != nil {
		return nil, fmt.Errorf("OAuth encrypted data is unavailable")
	}
	return plain, nil
}
