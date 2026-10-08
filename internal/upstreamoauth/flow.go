package upstreamoauth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/jackc/pgx/v5"
)

func (s *Service) Connect(ctx context.Context, a core.Actor, id string, in VersionInput, binding string) (Authorization, error) {
	if binding == "" {
		return Authorization{}, core.ErrForbidden
	}
	var result Authorization
	state, verifier := randomValue(), randomValue()
	err := s.locked(ctx, a, id, true, func(tx pgx.Tx, server core.MCPServer, v row) error {
		if err := versionMatches(in.ExpectedVersion, v); err != nil {
			return err
		}
		if v.Version == 0 || !server.Enabled || server.CredentialRef != "" || server.URL != v.Config.Resource {
			return core.ErrConflict
		}
		for _, endpoint := range []string{v.Config.Resource, v.Config.AuthorizationEndpoint, v.Config.TokenEndpoint} {
			_, closeClient, e := s.client(ctx, a.WorkspaceID, endpoint)
			if e != nil {
				return e
			}
			closeClient()
		}
		encrypted := s.seal(a.WorkspaceID, id, "verifier", []byte(verifier))
		err := tx.QueryRow(ctx, `UPDATE mcp_upstream_oauth SET version=version+1,status='pending',token=NULL,expires_at=NULL,state_hash=$3,session_hash=$4,actor_id=$5,verifier=$6,deadline=clock_timestamp()+interval '10 minutes',updated_at=clock_timestamp() WHERE workspace_id=$1 AND server_id=$2 RETURNING deadline`, a.WorkspaceID, id, digest(state), binding, a.ID, encrypted).Scan(&result.ExpiresAt)
		if err != nil {
			return err
		}
		hash := sha256.Sum256([]byte(verifier))
		u, _ := url.Parse(v.Config.AuthorizationEndpoint)
		q := url.Values{"response_type": {"code"}, "client_id": {v.Config.ClientID}, "redirect_uri": {s.redirect}, "state": {state}, "code_challenge": {base64.RawURLEncoding.EncodeToString(hash[:])}, "code_challenge_method": {"S256"}, "resource": {v.Config.Resource}}
		if len(v.Config.Scopes) > 0 {
			q.Set("scope", strings.Join(v.Config.Scopes, " "))
		}
		u.RawQuery = q.Encode()
		result.AuthorizationURL = u.String()
		return audit(ctx, tx, a, id, "MCP_OAUTH_CONNECT_STARTED", "pending", v.Version+1)
	})
	return result, err
}

// Complete claims the attempt durably BEFORE sending the one-use code. A crash
// or uncertain exchange can only be recovered by starting a new authorization.
func (s *Service) Complete(ctx context.Context, a core.Actor, binding, state, code, providerError, issuer string) error {
	if err := administrator(ctx, a); err != nil {
		return err
	}
	if binding == "" || len(state) != 43 || len(code) > 8192 || len(providerError) > 128 || (code == "" && providerError == "") || (code != "" && providerError != "") {
		return core.ErrInvalid
	}
	var id string
	err := s.db.QueryRow(ctx, `SELECT server_id FROM mcp_upstream_oauth WHERE workspace_id=$1 AND state_hash=$2 AND session_hash=$3 AND actor_id=$4`, a.WorkspaceID, digest(state), binding, a.ID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return core.ErrInvalid
	}
	if err != nil {
		return err
	}
	var claimed row
	err = s.locked(ctx, a, id, true, func(tx pgx.Tx, server core.MCPServer, v row) error {
		if !server.Enabled || v.Status != "pending" || v.State == nil || *v.State != digest(state) || v.Session == nil || *v.Session != binding || v.Actor == nil || *v.Actor != a.ID || v.Deadline == nil || !v.Now.Before(*v.Deadline) {
			return core.ErrConflict
		}
		if issuer != v.Config.Issuer {
			return core.ErrInvalid
		}
		claimed = v
		claimed.Version++
		_, e := tx.Exec(ctx, `UPDATE mcp_upstream_oauth SET version=version+1,status='exchanging',state_hash=NULL,session_hash=NULL,actor_id=NULL,verifier=NULL,deadline=clock_timestamp()+interval '45 seconds',updated_at=clock_timestamp() WHERE workspace_id=$1 AND server_id=$2`, a.WorkspaceID, id)
		return e
	})
	if err != nil {
		return err
	}
	var token grant
	if providerError != "" {
		return s.finish(a, id, claimed.Version, token, fmt.Errorf("OAuth authorization was declined; reconnect is required"))
	}
	verifier, err := s.open(a.WorkspaceID, id, "verifier", claimed.Verifier)
	defer clear(verifier)
	var secret []byte
	if err == nil && claimed.Config.AuthMethod == "client_secret_basic" {
		secret, err = s.open(a.WorkspaceID, id, "client_secret", claimed.Secret)
	}
	defer clear(secret)
	if err == nil {
		exchangeCtx, cancel := context.WithTimeout(ctx, exchangeTimeout)
		defer cancel()
		token, err = s.exchange(exchangeCtx, a.WorkspaceID, claimed.Config, secret, url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {s.redirect}, "code_verifier": {string(verifier)}})
	}
	return s.finish(a, id, claimed.Version, token, err)
}
func (s *Service) finish(a core.Actor, id string, version int64, token grant, exchangeErr error) error {
	// Save even when the calling connection was cancelled. This context never
	// retries the provider exchange, and the version fence prevents resurrection.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := s.locked(ctx, a, id, false, func(tx pgx.Tx, server core.MCPServer, v row) error {
		if v.Version != version || (v.Status != "exchanging" && v.Status != "refreshing") || !server.Enabled {
			return core.ErrConflict
		}
		next := "connected"
		var encrypted []byte
		var expires *time.Time
		if exchangeErr != nil {
			next = "reconnect_required"
		} else {
			raw, _ := json.Marshal(token)
			encrypted = s.seal(a.WorkspaceID, id, "token", raw)
			clear(raw)
			if token.ExpiresIn != nil {
				until := time.Now().Add(time.Duration(*token.ExpiresIn) * time.Second)
				expires = &until
			}
		}
		_, err := tx.Exec(ctx, `UPDATE mcp_upstream_oauth SET version=version+1,status=$3,token=$4,expires_at=$5,deadline=NULL,updated_at=clock_timestamp() WHERE workspace_id=$1 AND server_id=$2`, a.WorkspaceID, id, next, encrypted, expires)
		if err != nil {
			return err
		}
		action := "MCP_OAUTH_AUTHORIZED"
		if v.Status == "refreshing" {
			action = "MCP_OAUTH_REFRESHED"
		}
		if exchangeErr != nil {
			action = "MCP_OAUTH_RECONNECT_REQUIRED"
		}
		return audit(ctx, tx, a, id, action, next, version+1)
	})
	if err != nil {
		return err
	}
	return exchangeErr
}
func (s *Service) invalidate(ctx context.Context, a core.Actor, id string, version int64) error {
	return s.locked(ctx, a, id, false, func(tx pgx.Tx, _ core.MCPServer, v row) error {
		if v.Version != version {
			return nil
		}
		_, err := tx.Exec(ctx, `UPDATE mcp_upstream_oauth SET version=version+1,status='reconnect_required',token=NULL,expires_at=NULL,state_hash=NULL,session_hash=NULL,actor_id=NULL,verifier=NULL,deadline=NULL,updated_at=clock_timestamp() WHERE workspace_id=$1 AND server_id=$2`, a.WorkspaceID, id)
		if err != nil {
			return err
		}
		return audit(ctx, tx, a, id, "MCP_OAUTH_RECONNECT_REQUIRED", "reconnect_required", version+1)
	})
}

func (s *Service) access(ctx context.Context, a core.Actor, id string) (string, int64, bool, error) {
	for {
		var token grant
		var claimed row
		configured, wait, renew, unavailable := false, false, false, false
		err := s.locked(ctx, a, id, false, func(tx pgx.Tx, server core.MCPServer, v row) error {
			if v.Version == 0 {
				return nil
			}
			configured = true
			if !server.Enabled || server.CredentialRef != "" || server.URL != v.Config.Resource {
				return core.ErrConflict
			}
			claimed = v
			if v.Status == "refreshing" && v.Deadline != nil && v.Now.Before(*v.Deadline) {
				wait = true
				return nil
			}
			if v.Status != "connected" {
				return fmt.Errorf("%w: upstream OAuth requires reconnection", core.ErrConflict)
			}
			raw, e := s.open(a.WorkspaceID, id, "token", v.Token)
			if e != nil {
				return e
			}
			defer clear(raw)
			if json.Unmarshal(raw, &token) != nil || !validToken(token.AccessToken) {
				return fmt.Errorf("OAuth token is unreadable")
			}
			if v.Expires == nil || v.Expires.After(v.Now.Add(30*time.Second)) {
				return nil
			}
			if token.RefreshToken == "" {
				unavailable = true
				_, e = tx.Exec(ctx, `UPDATE mcp_upstream_oauth SET version=version+1,status='reconnect_required',token=NULL,expires_at=NULL,updated_at=clock_timestamp() WHERE workspace_id=$1 AND server_id=$2`, a.WorkspaceID, id)
				if e != nil {
					return e
				}
				return audit(ctx, tx, a, id, "MCP_OAUTH_RECONNECT_REQUIRED", "reconnect_required", v.Version+1)
			}
			renew = true
			claimed.Version++
			_, e = tx.Exec(ctx, `UPDATE mcp_upstream_oauth SET version=version+1,status='refreshing',deadline=clock_timestamp()+interval '45 seconds',updated_at=clock_timestamp() WHERE workspace_id=$1 AND server_id=$2`, a.WorkspaceID, id)
			return e
		})
		if err != nil {
			return "", 0, configured, err
		}
		if !configured {
			return "", 0, false, nil
		}
		if unavailable {
			return "", 0, true, fmt.Errorf("%w: upstream OAuth token expired; reconnect", core.ErrConflict)
		}
		if wait {
			timer := time.NewTimer(100 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return "", 0, true, ctx.Err()
			case <-timer.C:
				continue
			}
		}
		if !renew {
			return token.AccessToken, claimed.Version, true, nil
		}
		var secret []byte
		if claimed.Config.AuthMethod == "client_secret_basic" {
			secret, err = s.open(a.WorkspaceID, id, "client_secret", claimed.Secret)
		}
		var refreshed grant
		if err == nil {
			exchangeCtx, cancel := context.WithTimeout(ctx, exchangeTimeout)
			refreshConfig := claimed.Config
			if token.Scope != "" {
				refreshConfig.Scopes = strings.Fields(token.Scope)
			}
			refreshed, err = s.exchange(exchangeCtx, a.WorkspaceID, refreshConfig, secret, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {token.RefreshToken}})
			cancel()
			if refreshed.Scope == "" {
				refreshed.Scope = token.Scope
			}
			// Confidential clients may keep the old refresh token when none is returned.
			// Public clients require rotation to avoid silently reusing a one-use token.
			if err == nil && refreshed.RefreshToken == "" {
				if claimed.Config.AuthMethod == "none" {
					err = fmt.Errorf("OAuth refresh token rotation is required; reconnect")
				} else {
					refreshed.RefreshToken = token.RefreshToken
				}
			}
			if err == nil && claimed.Config.AuthMethod == "none" && refreshed.RefreshToken == token.RefreshToken {
				err = fmt.Errorf("OAuth refresh token was not rotated; reconnect")
			}
		}
		clear(secret)
		if err = s.finish(a, id, claimed.Version, refreshed, err); err != nil {
			return "", 0, true, err
		}
		// Do not keep refreshing an unusually short-lived token in the same call.
		return refreshed.AccessToken, claimed.Version + 1, true, nil
	}
}

// WrapTransport injects the grant only at this exact resource endpoint. It
// never forwards incoming gateway Authorization headers, refreshes on a 401,
// follows a redirect or replays a tools/call.
func (s *Service) WrapTransport(ctx context.Context, a core.Actor, server core.MCPServer, base http.RoundTripper) (http.RoundTripper, error) {
	stored, err := s.server(ctx, a.WorkspaceID, server.ID)
	if err != nil {
		return nil, err
	}
	if server.WorkspaceID != a.WorkspaceID || stored.URL != server.URL || !stored.Enabled || base == nil {
		return nil, fmt.Errorf("%w: OAuth resource destination changed", core.ErrInvalid)
	}
	value, version, configured, err := s.access(ctx, a, server.ID)
	if err != nil {
		return nil, err
	}
	if !configured {
		version = 0
	}
	return &grantTransport{service: s, actor: a, server: server, base: base, value: value, version: version}, nil
}

type grantTransport struct {
	service *Service
	actor   core.Actor
	server  core.MCPServer
	base    http.RoundTripper
	value   string
	version int64
}

func (t *grantTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Session IDs are headers; the negotiated MCP endpoint must stay exact.
	if req.URL.String() != t.server.URL {
		return nil, fmt.Errorf("OAuth resource destination changed")
	}
	var active bool
	err := t.service.db.QueryRow(req.Context(), `SELECT EXISTS(SELECT 1 FROM mcp_servers s WHERE s.workspace_id=$1 AND s.id=$2 AND s.enabled AND s.url=$4 AND (($3::bigint=0 AND NOT EXISTS(SELECT 1 FROM mcp_upstream_oauth o WHERE o.workspace_id=s.workspace_id AND o.server_id=s.id)) OR EXISTS(SELECT 1 FROM mcp_upstream_oauth o WHERE o.workspace_id=s.workspace_id AND o.server_id=s.id AND o.version=$3 AND o.status='connected' AND (o.expires_at IS NULL OR o.expires_at>clock_timestamp()))))`, t.actor.WorkspaceID, t.server.ID, t.version, t.server.URL).Scan(&active)
	if err != nil || !active {
		return nil, fmt.Errorf("OAuth connection is unavailable or changed; reconnect")
	}
	clone := req.Clone(req.Context())
	if t.version > 0 {
		clone.Header.Set("Authorization", "Bearer "+t.value)
	}
	clone.GetBody = nil
	resp, err := t.base.RoundTrip(clone)
	if err == nil && t.version > 0 && (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if e := t.service.invalidate(ctx, t.actor, t.server.ID, t.version); e != nil {
			resp.Body.Close()
			return nil, fmt.Errorf("could not record upstream OAuth rejection")
		}
	}
	return resp, err
}

// GrantSessionIdentity fences pooled sessions across grant rotation/reconnection.
// This is a version marker, never an access token. RoundTrip still checks the
// current grant status and expiry before each request.
func (t *grantTransport) GrantSessionIdentity() string { return fmt.Sprintf("oauth:%d", t.version) }
