package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/time/rate"
)

const sessionCookie = "__Host-gateway-session"

var usernamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{2,63}$`)

type Cloud struct {
	pool      *pgxpool.Pool
	origin    string
	limiter   *rate.Limiter
	hashing   chan struct{}
	dummyHash []byte
}
type sessionInfo struct{ token, csrf, username string }
type sessionKey struct{}
type cloudKey struct{}

// BrowserSessionBinding is a one-way binding for browser-initiated OAuth flows.
// It is deliberately unavailable to personal API keys and machine clients.
func BrowserSessionBinding(ctx context.Context) string {
	s, ok := ctx.Value(sessionKey{}).(sessionInfo)
	if !ok || s.token == "" {
		return ""
	}
	return fmt.Sprintf("%x", digest(s.token))
}

// CanManageClients permits local administrators and authenticated cloud browser
// administrators. Personal and client API keys cannot mint machine identities.
func CanManageClients(ctx context.Context) bool {
	a := Actor(ctx)
	if a.Role != core.RoleAdmin || a.ClientID != "" {
		return false
	}
	if cloud, _ := ctx.Value(cloudKey{}).(bool); !cloud {
		return true
	}
	_, ok := ctx.Value(sessionKey{}).(sessionInfo)
	return ok
}

// LockClientAdministrator revalidates the browser session inside a management
// transaction. Revoking the session or administrator role serializes with this
// lock instead of relying on a middleware snapshot.
func LockClientAdministrator(ctx context.Context, tx pgx.Tx) error {
	if cloud, _ := ctx.Value(cloudKey{}).(bool); !cloud {
		return nil
	}
	s, ok := ctx.Value(sessionKey{}).(sessionInfo)
	if !ok {
		return core.ErrForbidden
	}
	a := Actor(ctx)
	var id string
	err := tx.QueryRow(ctx, `SELECT u.id FROM gateway_users u JOIN gateway_sessions s ON s.user_id=u.id WHERE u.id=$1 AND u.workspace_id=$2 AND u.role='admin' AND NOT u.disabled AND s.token_hash=$3 AND s.expires_at>clock_timestamp() FOR SHARE OF u,s`, a.ID, a.WorkspaceID, digest(s.token)).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return core.ErrForbidden
	}
	return err
}

// NewCloud uses persisted identities. Static development tokens are never accepted.
// bootstrapToken creates one durable, expiring admin invitation, once per database.
func NewCloud(ctx context.Context, pool *pgxpool.Pool, origin, bootstrapToken string) (*Auth, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("PUBLIC_ORIGIN must be an HTTPS origin without a path")
	}
	dummy, err := bcrypt.GenerateFromPassword([]byte(randomToken()), 12)
	if err != nil {
		return nil, err
	}
	c := &Cloud{pool: pool, origin: origin, limiter: rate.NewLimiter(rate.Every(2*time.Second), 10), hashing: make(chan struct{}, 2), dummyHash: dummy}
	if bootstrapToken != "" {
		if len(bootstrapToken) < 40 {
			return nil, fmt.Errorf("bootstrap token must contain at least 40 characters")
		}
		_, err = pool.Exec(ctx, `INSERT INTO gateway_invites(id,workspace_id,token_hash,role,expires_at) VALUES('bootstrap','team',$1,'admin',clock_timestamp()+interval '7 days') ON CONFLICT(id) DO NOTHING`, digest(bootstrapToken))
		if err != nil {
			return nil, err
		}
	}
	return &Auth{origins: map[string]bool{origin: true}, cloud: c}, nil
}

func randomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func digest(s string) []byte { h := sha256.Sum256([]byte(s)); return h[:] }
func validRole(s string) bool {
	return s == "admin" || s == "operator" || s == "approver" || s == "viewer"
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func fail(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": http.StatusText(status), "message": message}})
}
func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		fail(w, 400, "A JSON request is required.")
		return false
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		fail(w, 400, "Invalid request fields.")
		return false
	}
	if d.Decode(new(any)) != io.EOF {
		fail(w, 400, "Expected one JSON object.")
		return false
	}
	return true
}

func (c *Cloud) authenticate(next http.Handler, w http.ResponseWriter, r *http.Request) {
	var actor core.Actor
	var err error
	ctx := context.WithValue(r.Context(), cloudKey{}, true)
	if header := r.Header.Get("Authorization"); header != "" {
		parts := strings.Fields(header)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || len(parts[1]) > 256 {
			fail(w, 401, "Authentication required.")
			return
		}
		err = c.pool.QueryRow(ctx, `SELECT u.id,u.workspace_id,u.role FROM gateway_api_keys k JOIN gateway_users u ON u.id=k.user_id WHERE k.token_hash=$1 AND k.revoked_at IS NULL AND k.expires_at>clock_timestamp() AND NOT u.disabled`, digest(parts[1])).Scan(&actor.ID, &actor.WorkspaceID, &actor.Role)
		if errors.Is(err, pgx.ErrNoRows) {
			err = c.pool.QueryRow(ctx, `SELECT id,workspace_id,key_id FROM gateway_clients WHERE token_hash=$1 AND enabled AND key_expires_at>clock_timestamp()`, digest(parts[1])).Scan(&actor.ID, &actor.WorkspaceID, &actor.ClientKeyID)
			if err == nil {
				actor.ClientID, actor.Role = actor.ID, core.RoleOperator
			}
		}
	} else {
		cookie, e := r.Cookie(sessionCookie)
		if e != nil || len(cookie.Value) > 256 {
			fail(w, 401, "Sign in to continue.")
			return
		}
		s := sessionInfo{token: cookie.Value}
		err = c.pool.QueryRow(ctx, `SELECT u.id,u.workspace_id,u.role,u.username,s.csrf_token FROM gateway_sessions s JOIN gateway_users u ON u.id=s.user_id WHERE s.token_hash=$1 AND s.expires_at>clock_timestamp() AND NOT u.disabled`, digest(cookie.Value)).Scan(&actor.ID, &actor.WorkspaceID, &actor.Role, &s.username, &s.csrf)
		if err == nil && r.Method != "GET" && r.Method != "HEAD" {
			if r.Header.Get("Origin") != c.origin || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(s.csrf)) != 1 {
				fail(w, 403, "Refresh your session before making changes.")
				return
			}
		}
		// MCP clients authenticate with revocable API keys, never ambient browser cookies.
		if r.URL.Path == "/mcp" {
			fail(w, 401, "An API key is required for MCP.")
			return
		}
		ctx = context.WithValue(ctx, sessionKey{}, s)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		fail(w, 401, "Authentication required.")
		return
	}
	if err != nil {
		fail(w, 503, "Identity service is unavailable.")
		return
	}
	next.ServeHTTP(w, r.WithContext(context.WithValue(ctx, actorKey{}, actor)))
}

// Handler contains both public sign-in routes and authenticated account administration.
func (a *Auth) CloudHandler() http.Handler {
	if a.cloud == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "GET" && r.URL.Path == "/api/v1/auth/status" {
				writeJSON(w, 200, map[string]string{"mode": "token"})
				return
			}
			fail(w, 404, "Cloud identity is not enabled.")
		})
	}
	c := a.cloud
	public := http.NewServeMux()
	public.HandleFunc("GET /api/v1/auth/status", func(w http.ResponseWriter, r *http.Request) {
		_, err := r.Cookie(sessionCookie)
		// This is only a cookie-presence hint; /session still authenticates it.
		writeJSON(w, 200, map[string]any{"mode": "cloud", "session_present": err == nil})
	})
	public.HandleFunc("POST /api/v1/auth/login", c.login)
	public.HandleFunc("POST /api/v1/auth/accept", c.accept)
	private := http.NewServeMux()
	private.HandleFunc("GET /api/v1/auth/session", c.session)
	private.HandleFunc("POST /api/v1/auth/logout", c.logout)
	private.HandleFunc("POST /api/v1/auth/password", c.password)
	private.HandleFunc("GET /api/v1/auth/members", c.members)
	private.HandleFunc("POST /api/v1/auth/members/{id}", c.updateMember)
	private.HandleFunc("GET /api/v1/auth/invites", c.invites)
	private.HandleFunc("POST /api/v1/auth/invites", c.invite)
	private.HandleFunc("POST /api/v1/auth/invites/{id}/revoke", c.revokeInvite)
	private.HandleFunc("GET /api/v1/auth/keys", c.keys)
	private.HandleFunc("POST /api/v1/auth/keys", c.createKey)
	private.HandleFunc("POST /api/v1/auth/keys/{id}/revoke", c.revokeKey)
	private.HandleFunc("GET /api/v1/auth/events", c.events)
	public.Handle("/", a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := r.Context().Value(sessionKey{}).(sessionInfo); !ok {
			fail(w, 403, "Use a browser session for account administration.")
			return
		}
		private.ServeHTTP(w, r)
	})))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" && origin != c.origin {
			fail(w, 403, "Origin is not allowed.")
			return
		}
		if r.Method == "POST" && (r.URL.Path == "/api/v1/auth/login" || r.URL.Path == "/api/v1/auth/accept") {
			if r.Header.Get("Origin") != c.origin {
				fail(w, 403, "Open the workspace to sign in.")
				return
			}
			if !c.limiter.Allow() {
				w.Header().Set("Retry-After", "30")
				fail(w, 429, "Too many attempts. Try again shortly.")
				return
			}
			select {
			case c.hashing <- struct{}{}:
				defer func() { <-c.hashing }()
			default:
				fail(w, 429, "Too many attempts. Try again shortly.")
				return
			}
		}
		public.ServeHTTP(w, r)
	})
}

func (c *Cloud) issueSession(ctx context.Context, tx pgx.Tx, userID string) (string, string, error) {
	token, csrf := randomToken(), randomToken()
	_, err := tx.Exec(ctx, `INSERT INTO gateway_sessions(token_hash,user_id,csrf_token,expires_at) VALUES($1,$2,$3,clock_timestamp()+interval '12 hours')`, digest(token), userID, csrf)
	return token, csrf, err
}
func setSession(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: token, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 43200})
}
func audit(ctx context.Context, tx pgx.Tx, actor core.Actor, action, subject string) error {
	_, err := tx.Exec(ctx, `INSERT INTO gateway_identity_events(workspace_id,actor_id,action,subject_id) VALUES($1,$2,$3,$4)`, actor.WorkspaceID, actor.ID, action, subject)
	return err
}
func finish(w http.ResponseWriter, r *http.Request, tx pgx.Tx, err error, value any) bool {
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		fail(w, 503, "The change could not be saved.")
		return false
	}
	writeJSON(w, 200, value)
	return true
}

func (c *Cloud) login(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	in.Username = strings.ToLower(strings.TrimSpace(in.Username))
	if len(in.Password) > 72 || !usernamePattern.MatchString(in.Username) {
		fail(w, 401, "Invalid username or password.")
		return
	}
	var actor core.Actor
	var hash []byte
	var disabled bool
	err := c.pool.QueryRow(r.Context(), `SELECT id,workspace_id,role,password_hash,disabled FROM gateway_users WHERE username=$1`, in.Username).Scan(&actor.ID, &actor.WorkspaceID, &actor.Role, &hash, &disabled)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		fail(w, 503, "Identity service is unavailable.")
		return
	}
	if hash == nil {
		hash = c.dummyHash
	}
	match := bcrypt.CompareHashAndPassword(hash, []byte(in.Password)) == nil
	if !match || actor.ID == "" || disabled {
		fail(w, 401, "Invalid username or password.")
		return
	}
	tx, err := c.pool.Begin(r.Context())
	if err != nil {
		fail(w, 503, "Identity service is unavailable.")
		return
	}
	defer tx.Rollback(context.Background())
	// Lock and recheck the credential so password changes/revocation cannot race sign-in.
	var currentHash []byte
	err = tx.QueryRow(r.Context(), `SELECT password_hash FROM gateway_users WHERE id=$1 AND NOT disabled FOR UPDATE`, actor.ID).Scan(&currentHash)
	if err != nil || subtle.ConstantTimeCompare(hash, currentHash) != 1 {
		fail(w, 401, "Invalid username or password.")
		return
	}
	token, _, err := c.issueSession(r.Context(), tx, actor.ID)
	if err == nil {
		err = audit(r.Context(), tx, actor, "LOGIN", actor.ID)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		fail(w, 503, "Sign-in could not be completed.")
		return
	}
	setSession(w, token)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (c *Cloud) accept(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Token    string `json:"token"`
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	in.Username = strings.ToLower(strings.TrimSpace(in.Username))
	if !usernamePattern.MatchString(in.Username) || len(in.Password) < 12 || len(in.Password) > 72 || len(in.Token) < 40 || len(in.Token) > 256 {
		fail(w, 400, "Use a 3–64 character username and a password of 12–72 bytes with a valid invitation.")
		return
	}
	tx, err := c.pool.Begin(r.Context())
	if err != nil {
		fail(w, 503, "Identity service is unavailable.")
		return
	}
	defer tx.Rollback(context.Background())
	var inviteID string
	actor := core.Actor{ID: core.NewID()}
	err = tx.QueryRow(r.Context(), `SELECT id,workspace_id,role FROM gateway_invites WHERE token_hash=$1 AND consumed_at IS NULL AND revoked_at IS NULL AND expires_at>clock_timestamp() FOR UPDATE`, digest(in.Token)).Scan(&inviteID, &actor.WorkspaceID, &actor.Role)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			fail(w, 400, "Invitation is invalid, expired, or already used.")
		} else {
			fail(w, 503, "Identity service is unavailable.")
		}
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(in.Password), 12)
	if err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO gateway_users(id,workspace_id,username,password_hash,role) VALUES($1,$2,$3,$4,$5)`, actor.ID, actor.WorkspaceID, in.Username, hash, actor.Role)
	}
	if err != nil {
		fail(w, 409, "The account could not be created. Choose another username.")
		return
	}
	_, err = tx.Exec(r.Context(), `UPDATE gateway_invites SET consumed_at=clock_timestamp() WHERE id=$1`, inviteID)
	token := ""
	if err == nil {
		token, _, err = c.issueSession(r.Context(), tx, actor.ID)
	}
	if err == nil {
		err = audit(r.Context(), tx, actor, "INVITE_ACCEPTED", inviteID)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		fail(w, 503, "Account creation could not be completed.")
		return
	}
	setSession(w, token)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (c *Cloud) session(w http.ResponseWriter, r *http.Request) {
	s := r.Context().Value(sessionKey{}).(sessionInfo)
	writeJSON(w, 200, map[string]any{"identity": Actor(r.Context()), "username": s.username, "csrf_token": s.csrf})
}
func (c *Cloud) logout(w http.ResponseWriter, r *http.Request) {
	s := r.Context().Value(sessionKey{}).(sessionInfo)
	_, err := c.pool.Exec(r.Context(), `DELETE FROM gateway_sessions WHERE token_hash=$1`, digest(s.token))
	if err != nil {
		fail(w, 503, "Sign-out could not be completed.")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: -1})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (c *Cloud) password(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Current  string `json:"current"`
		Password string `json:"password"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if len(in.Password) < 12 || len(in.Password) > 72 || len(in.Current) > 72 {
		fail(w, 400, "Password must be 12–72 bytes.")
		return
	}
	if !c.limiter.Allow() {
		fail(w, 429, "Too many attempts. Try again shortly.")
		return
	}
	select {
	case c.hashing <- struct{}{}:
		defer func() { <-c.hashing }()
	default:
		fail(w, 429, "Try again shortly.")
		return
	}
	actor := Actor(r.Context())
	tx, err := c.pool.Begin(r.Context())
	if err != nil {
		fail(w, 503, "Identity service is unavailable.")
		return
	}
	defer tx.Rollback(context.Background())
	var hash []byte
	err = tx.QueryRow(r.Context(), `SELECT password_hash FROM gateway_users WHERE id=$1 AND NOT disabled FOR UPDATE`, actor.ID).Scan(&hash)
	if err != nil || bcrypt.CompareHashAndPassword(hash, []byte(in.Current)) != nil {
		fail(w, 401, "Current password is incorrect.")
		return
	}
	hash, err = bcrypt.GenerateFromPassword([]byte(in.Password), 12)
	if err == nil {
		_, err = tx.Exec(r.Context(), `UPDATE gateway_users SET password_hash=$2 WHERE id=$1`, actor.ID, hash)
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `DELETE FROM gateway_sessions WHERE user_id=$1`, actor.ID)
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `UPDATE gateway_api_keys SET revoked_at=clock_timestamp() WHERE user_id=$1 AND revoked_at IS NULL`, actor.ID)
	}
	if err == nil {
		err = audit(r.Context(), tx, actor, "PASSWORD_CHANGED", actor.ID)
	}
	if finish(w, r, tx, err, map[string]bool{"ok": true}) { /* Client signs in again; all previous sessions are revoked. */
	}
}

func admin(w http.ResponseWriter, r *http.Request) bool {
	if Actor(r.Context()).Role != core.RoleAdmin {
		fail(w, 403, "Administrator access is required.")
		return false
	}
	return true
}
func (c *Cloud) members(w http.ResponseWriter, r *http.Request) {
	if !admin(w, r) {
		return
	}
	c.list(w, r, `SELECT jsonb_build_object('id',id,'username',username,'role',role,'disabled',disabled,'created_at',created_at) FROM gateway_users WHERE workspace_id=$1 ORDER BY created_at LIMIT 500`, Actor(r.Context()).WorkspaceID)
}
func (c *Cloud) updateMember(w http.ResponseWriter, r *http.Request) {
	if !admin(w, r) {
		return
	}
	var in struct {
		Role     string `json:"role"`
		Disabled *bool  `json:"disabled"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if !validRole(in.Role) || in.Disabled == nil {
		fail(w, 400, "A valid role and disabled flag are required.")
		return
	}
	actor := Actor(r.Context())
	target := r.PathValue("id")
	if target == actor.ID {
		fail(w, 409, "Ask another administrator to change your own access.")
		return
	}
	tx, err := c.pool.Begin(r.Context())
	if err != nil {
		fail(w, 503, "Identity service is unavailable.")
		return
	}
	defer tx.Rollback(context.Background())
	// A workspace lock prevents two administrators from concurrently removing each other.
	_, err = tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, actor.WorkspaceID)
	var currentRole string
	if err == nil {
		err = tx.QueryRow(r.Context(), `SELECT role FROM gateway_users WHERE id=$1 AND workspace_id=$2 AND NOT disabled FOR UPDATE`, actor.ID, actor.WorkspaceID).Scan(&currentRole)
	}
	if err != nil || currentRole != "admin" {
		fail(w, 403, "Administrator access is required.")
		return
	}
	tag, err := tx.Exec(r.Context(), `UPDATE gateway_users SET role=$3,disabled=$4 WHERE id=$1 AND workspace_id=$2`, target, actor.WorkspaceID, in.Role, *in.Disabled)
	if err == nil && tag.RowsAffected() == 0 {
		fail(w, 404, "Member not found.")
		return
	}
	// Role changes revoke credentials too, so an old key never silently gains new privileges.
	if err == nil {
		_, err = tx.Exec(r.Context(), `DELETE FROM gateway_sessions WHERE user_id=$1`, target)
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `UPDATE gateway_api_keys SET revoked_at=clock_timestamp() WHERE user_id=$1 AND revoked_at IS NULL`, target)
	}
	if err == nil {
		err = audit(r.Context(), tx, actor, "MEMBER_ACCESS_CHANGED", target)
	}
	finish(w, r, tx, err, map[string]bool{"ok": true})
}
func (c *Cloud) invites(w http.ResponseWriter, r *http.Request) {
	if !admin(w, r) {
		return
	}
	c.list(w, r, `SELECT jsonb_build_object('id',id,'role',role,'expires_at',expires_at,'consumed_at',consumed_at,'revoked_at',revoked_at) FROM gateway_invites WHERE workspace_id=$1 ORDER BY created_at DESC LIMIT 100`, Actor(r.Context()).WorkspaceID)
}
func (c *Cloud) invite(w http.ResponseWriter, r *http.Request) {
	if !admin(w, r) {
		return
	}
	var in struct {
		Role string `json:"role"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if !validRole(in.Role) {
		fail(w, 400, "Invalid role.")
		return
	}
	actor := Actor(r.Context())
	id, token := core.NewID(), randomToken()
	tx, err := c.pool.Begin(r.Context())
	if err != nil {
		fail(w, 503, "Identity service is unavailable.")
		return
	}
	defer tx.Rollback(context.Background())
	_, err = tx.Exec(r.Context(), `INSERT INTO gateway_invites(id,workspace_id,token_hash,role,created_by,expires_at) VALUES($1,$2,$3,$4,$5,clock_timestamp()+interval '48 hours')`, id, actor.WorkspaceID, digest(token), in.Role, actor.ID)
	if err == nil {
		err = audit(r.Context(), tx, actor, "INVITE_CREATED", id)
	}
	finish(w, r, tx, err, map[string]string{"id": id, "url": c.origin + "/console/#invite=" + token})
}
func (c *Cloud) revokeInvite(w http.ResponseWriter, r *http.Request) {
	if !admin(w, r) {
		return
	}
	actor := Actor(r.Context())
	tx, err := c.pool.Begin(r.Context())
	if err != nil {
		fail(w, 503, "Identity service is unavailable.")
		return
	}
	defer tx.Rollback(context.Background())
	tag, err := tx.Exec(r.Context(), `UPDATE gateway_invites SET revoked_at=COALESCE(revoked_at,clock_timestamp()) WHERE id=$1 AND workspace_id=$2 AND consumed_at IS NULL`, r.PathValue("id"), actor.WorkspaceID)
	if err == nil && tag.RowsAffected() == 0 {
		fail(w, 404, "Pending invitation not found.")
		return
	}
	if err == nil {
		err = audit(r.Context(), tx, actor, "INVITE_REVOKED", r.PathValue("id"))
	}
	finish(w, r, tx, err, map[string]bool{"ok": true})
}
func (c *Cloud) keys(w http.ResponseWriter, r *http.Request) {
	c.list(w, r, `SELECT jsonb_build_object('id',id,'name',name,'expires_at',expires_at,'revoked_at',revoked_at) FROM gateway_api_keys WHERE user_id=$1 ORDER BY created_at DESC LIMIT 100`, Actor(r.Context()).ID)
}
func (c *Cloud) createKey(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if len(in.Name) < 1 || len(in.Name) > 80 {
		fail(w, 400, "Name must be 1–80 bytes.")
		return
	}
	actor := Actor(r.Context())
	id, token := core.NewID(), "mgw_"+randomToken()
	tx, err := c.pool.Begin(r.Context())
	if err != nil {
		fail(w, 503, "Identity service is unavailable.")
		return
	}
	defer tx.Rollback(context.Background())
	_, err = tx.Exec(r.Context(), `INSERT INTO gateway_api_keys(id,user_id,token_hash,name,expires_at) VALUES($1,$2,$3,$4,clock_timestamp()+interval '30 days')`, id, actor.ID, digest(token), in.Name)
	if err == nil {
		err = audit(r.Context(), tx, actor, "KEY_CREATED", id)
	}
	finish(w, r, tx, err, map[string]string{"id": id, "token": token})
}
func (c *Cloud) revokeKey(w http.ResponseWriter, r *http.Request) {
	actor := Actor(r.Context())
	tx, err := c.pool.Begin(r.Context())
	if err != nil {
		fail(w, 503, "Identity service is unavailable.")
		return
	}
	defer tx.Rollback(context.Background())
	tag, err := tx.Exec(r.Context(), `UPDATE gateway_api_keys SET revoked_at=COALESCE(revoked_at,clock_timestamp()) WHERE id=$1 AND user_id=$2`, r.PathValue("id"), actor.ID)
	if err == nil && tag.RowsAffected() == 0 {
		fail(w, 404, "Key not found.")
		return
	}
	if err == nil {
		err = audit(r.Context(), tx, actor, "KEY_REVOKED", r.PathValue("id"))
	}
	finish(w, r, tx, err, map[string]bool{"ok": true})
}
func (c *Cloud) events(w http.ResponseWriter, r *http.Request) {
	if !admin(w, r) {
		return
	}
	c.list(w, r, `SELECT jsonb_build_object('id',id::text,'actor_id',actor_id,'action',action,'subject_id',subject_id,'created_at',created_at) FROM gateway_identity_events WHERE workspace_id=$1 ORDER BY id DESC LIMIT 100`, Actor(r.Context()).WorkspaceID)
}
func (c *Cloud) list(w http.ResponseWriter, r *http.Request, query string, arg string) {
	rows, err := c.pool.Query(r.Context(), query, arg)
	if err != nil {
		fail(w, 503, "Identity service is unavailable.")
		return
	}
	defer rows.Close()
	items := []json.RawMessage{}
	for rows.Next() {
		var b json.RawMessage
		if err = rows.Scan(&b); err != nil {
			fail(w, 503, "Identity service is unavailable.")
			return
		}
		items = append(items, b)
	}
	if rows.Err() != nil {
		fail(w, 503, "Identity service is unavailable.")
		return
	}
	writeJSON(w, 200, map[string]any{"items": items})
}
