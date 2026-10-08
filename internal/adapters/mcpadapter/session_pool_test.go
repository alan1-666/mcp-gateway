package mcpadapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alan1-666/mcp-gateway/internal/adapters/httpadapter"
	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type poolResolver struct {
	mu     sync.Mutex
	server core.MCPServer
	denied bool
}

func (r *poolResolver) GetServer(context.Context, string, string) (core.MCPServer, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.denied {
		return core.MCPServer{}, core.ErrNotFound
	}
	return r.server, nil
}

type poolCredentials struct {
	mu       sync.Mutex
	value    httpadapter.Credential
	disabled bool
}

func (c *poolCredentials) Resolve(context.Context, string, string, string) (httpadapter.Credential, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.disabled {
		return httpadapter.Credential{}, true, errors.New("private credential error")
	}
	return c.value, true, nil
}

type poolFixture struct {
	adapter                           *Adapter
	resolver                          *poolResolver
	credentials                       *poolCredentials
	server                            *httptest.Server
	sdk                               *mcp.Server
	actor                             core.Actor
	tool                              core.Tool
	handshakes, lists, calls, deletes atomic.Int32
	expireList, failCall              atomic.Bool
	mu                                sync.Mutex
	callSessions                      []string
	beforeList                        func()
	beforeInitialize                  func(context.Context)
	handleCall                        func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error)
}

func newPoolFixture(t *testing.T, max int, ttl time.Duration) *poolFixture {
	t.Helper()
	f := &poolFixture{actor: core.Actor{ID: "actor", WorkspaceID: "workspace", Role: core.RoleAdmin}}
	f.sdk = mcp.NewServer(&mcp.Implementation{Name: "pool-fixture", Version: "1"}, nil)
	f.sdk.AddTool(basicTool("read"), func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		f.calls.Add(1)
		if f.handleCall != nil {
			return f.handleCall(ctx, r)
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}, StructuredContent: json.RawMessage(`{"id":9007199254740993}`)}, nil
	})
	// Force legacy stateful initialization, so tests prove reuse of a real
	// negotiated session ID rather than merely reusing a stateless Go client.
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return f.sdk }, &mcp.StreamableHTTPOptions{JSONResponse: true, SessionTimeout: time.Minute})
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var message struct {
			Method string `json:"method"`
		}
		if r.Body != nil {
			data, _ := io.ReadAll(r.Body)
			r.Body.Close()
			r.Body = io.NopCloser(bytes.NewReader(data))
			_ = json.Unmarshal(data, &message)
		}
		if message.Method == "server/discover" {
			http.Error(w, "unsupported", 404)
			return
		}
		if message.Method == "initialize" {
			f.handshakes.Add(1)
			if f.beforeInitialize != nil {
				f.beforeInitialize(r.Context())
			}
		}
		if message.Method == "tools/list" {
			f.lists.Add(1)
			if f.expireList.CompareAndSwap(true, false) {
				http.Error(w, "session missing", 404)
				return
			}
			if f.beforeList != nil {
				f.beforeList()
			}
		}
		if message.Method == "tools/call" {
			f.mu.Lock()
			f.callSessions = append(f.callSessions, r.Header.Get("Mcp-Session-Id"))
			f.mu.Unlock()
			if f.failCall.Load() {
				http.Error(w, "session missing", 404)
				return
			}
		}
		if r.Method == http.MethodDelete {
			f.deletes.Add(1)
		}
		handler.ServeHTTP(w, r)
	}))
	config := serverConfig(f.server.URL, "upstream", f.actor.WorkspaceID)
	config.TimeoutMS = 3000
	config.CredentialRef = "AUTH"
	f.resolver = &poolResolver{server: config}
	f.credentials = &poolCredentials{value: httpadapter.Credential{WorkspaceID: f.actor.WorkspaceID, Ref: "AUTH", Origin: f.server.URL, Version: 1, Headers: map[string]string{"Authorization": "Bearer fixture"}}}
	egress, err := httpadapter.New([]string{f.server.URL}, []string{"127.0.0.0/8"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	egress.SetCredentialResolver(f.credentials)
	f.adapter = New(egress, f.resolver)
	if err = f.adapter.EnableSessionPool(SessionPoolOptions{MaxSessions: max, IdleTTL: ttl, MaxLifetime: 10 * time.Second}); err != nil {
		t.Fatal(err)
	}
	remote, err := f.adapter.Discover(context.Background(), f.actor, config)
	if err != nil {
		t.Fatal(err)
	}
	f.tool = imported(config, remote[0], core.RiskRead)
	f.handshakes.Store(0)
	f.lists.Store(0)
	f.deletes.Store(0)
	t.Cleanup(func() { f.adapter.Close(); f.server.Close() })
	return f
}

func (f *poolFixture) execute(ctx context.Context, actor core.Actor) core.FinishInput {
	return f.adapter.Execute(ctx, actor, f.tool, core.Operation{Arguments: json.RawMessage(`{}`)})
}
func requirePoolSuccess(t *testing.T, r core.FinishInput) {
	t.Helper()
	if r.State != core.StateSucceeded {
		t.Fatalf("operation failed: %+v", r)
	}
}

func TestPoolReusesExclusiveSessionButRechecksEveryContract(t *testing.T) {
	f := newPoolFixture(t, 2, time.Second)
	for range 3 {
		requirePoolSuccess(t, f.execute(context.Background(), f.actor))
	}
	if f.handshakes.Load() != 1 || f.lists.Load() != 3 || f.calls.Load() != 3 {
		t.Fatalf("handshakes/lists/calls = %d/%d/%d", f.handshakes.Load(), f.lists.Load(), f.calls.Load())
	}
	f.mu.Lock()
	sessions := append([]string(nil), f.callSessions...)
	f.mu.Unlock()
	if len(sessions) != 3 || sessions[0] == "" || sessions[0] != sessions[1] || sessions[1] != sessions[2] {
		t.Fatalf("session reuse failed: %d sessions", len(sessions))
	}
	stats := f.adapter.SessionPoolStats()
	if stats.Hits != 2 || stats.Retained != 1 || stats.Idle != 1 || stats.Active != 0 {
		t.Fatalf("stats %+v", stats)
	}
	// Schema changes are never accepted from a cached session.
	f.sdk.AddTool(&mcp.Tool{Name: "read", InputSchema: json.RawMessage(`{"type":"object","required":["new"]}`)}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		f.calls.Add(1)
		return nil, nil
	})
	if r := f.execute(context.Background(), f.actor); r.State != core.StateFailed || f.calls.Load() != 3 {
		t.Fatal("cached contract bypassed live discovery")
	}
	if f.adapter.SessionPoolStats().Retained != 0 {
		t.Fatal("schema failure retained session")
	}
}

func TestPoolSeparatesActorClientKeyRoleCredentialAndServerRevision(t *testing.T) {
	f := newPoolFixture(t, 8, time.Second)
	a := f.actor
	actors := []core.Actor{a, {ID: "other", WorkspaceID: a.WorkspaceID, Role: a.Role}, {ID: a.ID, WorkspaceID: a.WorkspaceID, Role: core.RoleOperator}, {ID: a.ID, WorkspaceID: a.WorkspaceID, Role: a.Role, ClientID: a.ID, ClientKeyID: "key1"}, {ID: a.ID, WorkspaceID: a.WorkspaceID, Role: a.Role, ClientID: a.ID, ClientKeyID: "key2"}}
	for _, actor := range actors {
		requirePoolSuccess(t, f.execute(context.Background(), actor))
	}
	if f.handshakes.Load() != 5 {
		t.Fatal("identity scopes shared a session")
	}
	f.credentials.mu.Lock()
	f.credentials.value.Version++
	f.credentials.mu.Unlock()
	requirePoolSuccess(t, f.execute(context.Background(), a))
	f.resolver.mu.Lock()
	f.resolver.server.UpdatedAt = time.Now()
	f.resolver.mu.Unlock()
	requirePoolSuccess(t, f.execute(context.Background(), a))
	if f.handshakes.Load() != 7 {
		t.Fatal("credential/server revision reused a stale session")
	}
	f.credentials.mu.Lock()
	f.credentials.disabled = true
	f.credentials.mu.Unlock()
	before := f.calls.Load()
	if r := f.execute(context.Background(), a); r.State != core.StateFailed || f.calls.Load() != before {
		t.Fatal("disabled credential reached business call")
	}
	f.credentials.mu.Lock()
	f.credentials.disabled = false
	f.credentials.mu.Unlock()
	f.resolver.mu.Lock()
	f.resolver.server.Enabled = false
	f.resolver.mu.Unlock()
	if r := f.execute(context.Background(), a); r.State != core.StateFailed || f.calls.Load() != before {
		t.Fatal("disabled server reached business call")
	}
}

func TestPoolCredentialAndServerFencesBetweenDiscoveryAndCall(t *testing.T) {
	for _, kind := range []string{"credential", "server"} {
		t.Run(kind, func(t *testing.T) {
			f := newPoolFixture(t, 2, time.Second)
			requirePoolSuccess(t, f.execute(context.Background(), f.actor))
			f.beforeList = func() {
				if kind == "credential" {
					f.credentials.mu.Lock()
					f.credentials.value.Version++
					f.credentials.mu.Unlock()
				} else {
					f.resolver.mu.Lock()
					f.resolver.server.Enabled = false
					f.resolver.mu.Unlock()
				}
			}
			r := f.execute(context.Background(), f.actor)
			if r.State != core.StateFailed || f.calls.Load() != 1 {
				t.Fatal("changed authorization reached business handler")
			}
			if f.adapter.SessionPoolStats().Retained != 0 {
				t.Fatal("invalid lease retained")
			}
		})
	}
}

func TestPoolBusyCapacityUsesIndependentDisposableSession(t *testing.T) {
	f := newPoolFixture(t, 1, time.Second)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	f.handleCall = func(ctx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		first := false
		once.Do(func() { close(entered); first = true })
		if first {
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil
	}
	done := make(chan core.FinishInput, 1)
	go func() { done <- f.execute(context.Background(), f.actor) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("call never entered")
	}
	requirePoolSuccess(t, f.execute(context.Background(), f.actor))
	close(release)
	requirePoolSuccess(t, <-done)
	f.mu.Lock()
	same := len(f.callSessions) == 2 && f.callSessions[0] == f.callSessions[1]
	f.mu.Unlock()
	if same || f.handshakes.Load() != 2 || f.adapter.SessionPoolStats().Fallbacks != 1 {
		t.Fatal("concurrent operations shared a session")
	}
	requirePoolSuccess(t, f.execute(context.Background(), f.actor))
	if f.handshakes.Load() != 2 {
		t.Fatal("healthy retained session lost after fallback")
	}
}

func TestPoolFailureNeverReplaysAndNextOperationRebuilds(t *testing.T) {
	for _, risk := range []core.Risk{core.RiskRead, core.RiskWrite} {
		t.Run(string(risk), func(t *testing.T) {
			f := newPoolFixture(t, 2, time.Second)
			f.tool.Risk = risk
			f.handleCall = func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "failed"}}}, nil
			}
			r := f.execute(context.Background(), f.actor)
			expected := core.StateFailed
			if risk == core.RiskWrite {
				expected = core.StateUnknown
			}
			if r.State != expected || f.calls.Load() != 1 || f.adapter.SessionPoolStats().Retained != 0 {
				t.Fatalf("failure replayed or retained %+v", r)
			}
			f.handleCall = nil
			requirePoolSuccess(t, f.execute(context.Background(), f.actor))
			if f.handshakes.Load() != 2 || f.calls.Load() != 2 {
				t.Fatal("next operation did not rebuild independently")
			}
		})
	}
}

func TestPoolIdleExpiryEvictionAndShutdownSendDelete(t *testing.T) {
	f := newPoolFixture(t, 1, 100*time.Millisecond)
	requirePoolSuccess(t, f.execute(context.Background(), f.actor))
	other := f.actor
	other.ID = "other"
	requirePoolSuccess(t, f.execute(context.Background(), other))
	if f.adapter.SessionPoolStats().Evictions != 1 || f.deletes.Load() != 1 {
		t.Fatal("capacity eviction did not close session")
	}
	deadline := time.Now().Add(2 * time.Second)
	for (f.adapter.SessionPoolStats().Retained != 0 || f.deletes.Load() != 2) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if f.adapter.SessionPoolStats().Retained != 0 || f.deletes.Load() != 2 {
		t.Fatal("idle reaper did not retire session")
	}
	requirePoolSuccess(t, f.execute(context.Background(), f.actor))
	f.adapter.Close()
	f.adapter.Close()
	if f.deletes.Load() != 3 {
		t.Fatal("shutdown did not terminate session exactly once")
	}
	if r := f.execute(context.Background(), f.actor); r.State != core.StateFailed {
		t.Fatal("closed pool admitted operation")
	}
}

func TestPoolInvalidConfigAnonymousAndIndependentDiagnostics(t *testing.T) {
	plain := New(nil, nil)
	if plain.SessionPoolStats() != (SessionPoolStats{}) {
		t.Fatal("disabled stats")
	}
	plain.Close()
	for _, options := range []SessionPoolOptions{{}, {MaxSessions: 257, IdleTTL: time.Second, MaxLifetime: time.Minute}, {MaxSessions: 1, IdleTTL: time.Second, MaxLifetime: time.Millisecond}} {
		if plain.EnableSessionPool(options) == nil {
			t.Fatal("invalid configuration accepted")
		}
	}
	f := newPoolFixture(t, 2, time.Second)
	if f.adapter.EnableSessionPool(SessionPoolOptions{MaxSessions: 1, IdleTTL: time.Second, MaxLifetime: time.Minute}) == nil {
		t.Fatal("pool reconfigured after startup")
	}
	a := f.actor
	a.ID = ""
	for range 2 {
		requirePoolSuccess(t, f.execute(context.Background(), a))
	}
	if f.handshakes.Load() != 2 || f.adapter.SessionPoolStats().Retained != 0 {
		t.Fatal("anonymous actor retained a session")
	}
	requirePoolSuccess(t, f.execute(context.Background(), f.actor))
	before := f.handshakes.Load()
	f.resolver.mu.Lock()
	server := f.resolver.server
	f.resolver.mu.Unlock()
	report := f.adapter.Check(context.Background(), f.actor, server)
	if report.Status != "ok" || f.handshakes.Load() != before+2 || f.adapter.SessionPoolStats().Retained != 1 {
		t.Fatal("diagnostics reused execution session")
	}
}

func TestPoolCancellationRetiresLeaseAndFreshDeadlineWorks(t *testing.T) {
	f := newPoolFixture(t, 2, time.Second)
	requirePoolSuccess(t, f.execute(context.Background(), f.actor))
	cancelCtx, cancel := context.WithCancel(context.Background())
	f.beforeList = cancel
	if r := f.execute(cancelCtx, f.actor); r.State != core.StateFailed || f.calls.Load() != 1 || f.adapter.SessionPoolStats().Retained != 0 {
		t.Fatal("cancelled discovery leaked into dispatch or retained session")
	}
	f.beforeList = nil
	requirePoolSuccess(t, f.execute(context.Background(), f.actor))
	if f.handshakes.Load() != 2 {
		t.Fatal("old cancellation poisoned next independent operation")
	}
}

func TestPoolLifetimeDoesNotRenewAndCloseDuringInitialize(t *testing.T) {
	f := newPoolFixture(t, 2, 100*time.Millisecond)
	// A successful lease that outlives its initial creation must not reset the
	// absolute lifetime. Hold the actual operation across that deadline.
	f.adapter.pool.mu.Lock()
	f.adapter.pool.options.MaxLifetime = 100 * time.Millisecond
	f.adapter.pool.mu.Unlock()
	f.handleCall = func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		time.Sleep(120 * time.Millisecond)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil
	}
	requirePoolSuccess(t, f.execute(context.Background(), f.actor))
	if f.adapter.SessionPoolStats().Retained != 0 {
		t.Fatal("operation renewed absolute session lifetime")
	}
	f.handleCall = nil
	// Close while initialization is reserved but not yet published. The
	// successfully initialized SDK session must still be explicitly destroyed.
	entry, _, _, err := f.adapter.pool.reserve(context.Background(), "reserved", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	f.adapter.Close()
	if f.adapter.pool.attach(entry, nil, nil, func() {}) {
		t.Fatal("session published after pool shutdown")
	}
}

type poolOAuth struct {
	version atomic.Int64
	unknown bool
}
type poolOAuthTransport struct {
	provider *poolOAuth
	version  int64
	base     http.RoundTripper
}

func (p *poolOAuth) WrapTransport(_ context.Context, _ core.Actor, _ core.MCPServer, base http.RoundTripper) (http.RoundTripper, error) {
	if p.unknown {
		return base, nil
	}
	return &poolOAuthTransport{provider: p, version: p.version.Load(), base: base}, nil
}
func (t *poolOAuthTransport) GrantSessionIdentity() string {
	return fmt.Sprintf("fixture:%d", t.version)
}
func (t *poolOAuthTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if t.provider.version.Load() != t.version {
		return nil, errors.New("grant changed")
	}
	return t.base.RoundTrip(r)
}

func TestPoolOAuthRevisionFenceAndUnknownProviderFallsBack(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(fmt.Sprint(unknown), func(t *testing.T) {
			f := newPoolFixture(t, 2, time.Second)
			provider := &poolOAuth{unknown: unknown}
			provider.version.Store(1)
			f.adapter.SetOAuthProvider(provider)
			for range 2 {
				requirePoolSuccess(t, f.execute(context.Background(), f.actor))
			}
			expected := int32(1)
			if unknown {
				expected = 2
			}
			if f.handshakes.Load() != expected {
				t.Fatal("OAuth pool eligibility wrong")
			}
			provider.version.Store(2)
			requirePoolSuccess(t, f.execute(context.Background(), f.actor))
			if f.handshakes.Load() != expected+1 {
				t.Fatal("OAuth revision failed to establish a new session")
			}
			if !unknown {
				f.beforeList = func() { provider.version.Store(3) }
				if r := f.execute(context.Background(), f.actor); r.State != core.StateFailed || f.calls.Load() != 3 {
					t.Fatal("rotated OAuth grant reached call")
				}
			}
		})
	}
}

func TestPoolExpiredCatalogRebuildsOnceButCallIsNeverReplayed(t *testing.T) {
	f := newPoolFixture(t, 2, time.Second)
	requirePoolSuccess(t, f.execute(context.Background(), f.actor))
	f.expireList.Store(true)
	requirePoolSuccess(t, f.execute(context.Background(), f.actor))
	if f.handshakes.Load() != 2 || f.calls.Load() != 2 || f.lists.Load() != 3 {
		t.Fatal("expired catalog did not rebuild before the call")
	}
	f.failCall.Store(true)
	f.tool.Risk = core.RiskWrite
	f.mu.Lock()
	before := len(f.callSessions)
	f.mu.Unlock()
	r := f.execute(context.Background(), f.actor)
	f.mu.Lock()
	after := len(f.callSessions)
	f.mu.Unlock()
	if r.State != core.StateUnknown || f.handshakes.Load() != 2 || after != before+1 || f.adapter.SessionPoolStats().Retained != 0 {
		t.Fatal("ambiguous call was replayed or retained")
	}
	f.failCall.Store(false)
	requirePoolSuccess(t, f.execute(context.Background(), f.actor))
	if f.handshakes.Load() != 3 {
		t.Fatal("next independent operation did not rebuild")
	}
}

func TestPoolCloseCancelsPendingInitialization(t *testing.T) {
	f := newPoolFixture(t, 2, time.Second)
	entered, requestDone := make(chan struct{}), make(chan struct{})
	f.beforeInitialize = func(ctx context.Context) { close(entered); <-ctx.Done(); close(requestDone) }
	result := make(chan core.FinishInput, 1)
	go func() { result <- f.execute(context.Background(), f.actor) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("initialization never entered")
	}
	if f.adapter.SessionPoolStats().Active != 1 {
		t.Fatal("initialization not counted against retained capacity")
	}
	f.adapter.Close()
	select {
	case <-requestDone:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not cancel initializing network request")
	}
	select {
	case r := <-result:
		if r.State != core.StateFailed {
			t.Fatal("initialized after shutdown")
		}
	case <-time.After(time.Second):
		t.Fatal("initialization did not return")
	}
	if f.adapter.SessionPoolStats().Retained != 0 || f.calls.Load() != 0 {
		t.Fatal("shutdown leaked pending session or business call")
	}
}
