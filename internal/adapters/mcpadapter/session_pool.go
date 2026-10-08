package mcpadapter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// SessionPoolOptions bounds retained execution sessions per process. Existing
// database admission budgets still bound dispatched operations. A busy or full
// pool uses an independent disposable connection, never a new operation queue.
type SessionPoolOptions struct {
	MaxSessions int
	IdleTTL     time.Duration
	MaxLifetime time.Duration
}

type SessionPoolStats struct {
	Retained, Active, Idle                       int
	Hits, Misses, Evictions, Discards, Fallbacks uint64
}

type pooledSession struct {
	key            string
	session        *mcp.ClientSession
	transport      *protocolTransport
	close          func()
	initialization context.Context
	cancel         context.CancelFunc
	busy           bool
	created        time.Time
	used           time.Time
}

type sessionPool struct {
	mu         sync.Mutex
	options    SessionPoolOptions
	entries    map[*pooledSession]struct{}
	stats      SessionPoolStats
	closed     bool
	stop       chan struct{}
	done       chan struct{}
	closedDone chan struct{}
}

// EnableSessionPool is startup-only configuration. Discovery, diagnostics,
// Connectors, custom transports and anonymous actors never use retained sessions.
func (a *Adapter) EnableSessionPool(options SessionPoolOptions) error {
	if a.pool != nil || options.MaxSessions < 1 || options.MaxSessions > 256 || options.IdleTTL < 100*time.Millisecond || options.IdleTTL > 30*time.Minute || options.MaxLifetime < options.IdleTTL || options.MaxLifetime > time.Hour {
		return fmt.Errorf("invalid MCP session pool configuration")
	}
	p := &sessionPool{options: options, entries: make(map[*pooledSession]struct{}), stop: make(chan struct{}), done: make(chan struct{}), closedDone: make(chan struct{})}
	a.pool = p
	go p.reap()
	return nil
}

// Close retires retained sessions. Service startup defers it until HTTP requests
// drain, and before closing the database used by per-request credential fences.
func (a *Adapter) Close() {
	if a.pool != nil {
		a.pool.shutdown()
	}
}

func (a *Adapter) SessionPoolStats() SessionPoolStats {
	if a.pool == nil {
		return SessionPoolStats{}
	}
	p := a.pool
	p.mu.Lock()
	defer p.mu.Unlock()
	stats := p.stats
	stats.Retained = len(p.entries)
	for e := range p.entries {
		if e.busy {
			stats.Active++
		} else {
			stats.Idle++
		}
	}
	return stats
}

func (a *Adapter) executionSession(ctx context.Context, actor core.Actor, server core.MCPServer) (*mcp.ClientSession, *protocolTransport, func(bool), error) {
	if a.pool == nil || actor.ID == "" || a.factory != nil {
		s, t, close, err := a.connect(ctx, actor, server)
		return s, t, func(bool) {
			if close != nil {
				close()
			}
		}, err
	}
	client, closeClient, identity, err := a.prepareClient(ctx, actor, server, true)
	if err != nil {
		return nil, nil, nil, err
	}
	if identity == "" {
		s, t, close, err := connectClient(ctx, server, client, closeClient)
		return s, t, func(bool) {
			if close != nil {
				close()
			}
		}, err
	}
	key := sessionKey(actor, server, identity)
	entry, reused, evicted, err := a.pool.reserve(ctx, key, time.Now())
	if evicted != nil {
		disposeSession(evicted)
	}
	if err != nil {
		closeClient()
		return nil, nil, nil, err
	}
	if reused {
		closeClient()
		entry.transport.begin(ctx)
	} else {
		connectCtx := ctx
		if entry != nil {
			connectCtx = entry.initialization
		}
		s, t, close, err := connectClient(connectCtx, server, client, closeClient)
		if err != nil {
			if entry != nil {
				if retired := a.pool.release(entry, false, time.Now()); retired != nil {
					disposeSession(retired)
				}
			}
			return nil, nil, nil, err
		}
		if entry == nil {
			return s, t, func(bool) { close() }, nil
		}
		// Publication and shutdown are synchronized; a close racing initialization
		// cannot strand a newly created SDK session outside pool ownership.
		if !a.pool.attach(entry, s, t, close) {
			cancel := t.shutdown()
			close()
			cancel()
			return nil, nil, nil, fmt.Errorf("MCP session pool is closed")
		}
		t.begin(ctx)
	}
	var once sync.Once
	return entry.session, entry.transport, func(healthy bool) {
		once.Do(func() {
			entry.transport.idle()
			if retired := a.pool.release(entry, healthy && ctx.Err() == nil, time.Now()); retired != nil {
				disposeSession(retired)
			}
		})
	}, nil
}

func sessionKey(actor core.Actor, server core.MCPServer, identity string) string {
	// Role and key identity matter even for workspace-wide upstream credentials.
	raw, _ := json.Marshal(struct {
		Actor    core.Actor
		KeyID    string
		Server   core.MCPServer
		Identity string
	}{actor, actor.ClientKeyID, server, identity})
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func (p *sessionPool) expired(e *pooledSession, now time.Time) bool {
	return now.Sub(e.used) >= p.options.IdleTTL || now.Sub(e.created) >= p.options.MaxLifetime
}

func (p *sessionPool) reserve(ctx context.Context, key string, now time.Time) (*pooledSession, bool, *pooledSession, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil, false, nil, fmt.Errorf("MCP session pool is closed")
	}
	var oldest *pooledSession
	for e := range p.entries {
		if e.busy {
			continue
		}
		if e.key == key && !p.expired(e, now) {
			e.busy = true
			p.stats.Hits++
			return e, true, nil, nil
		}
		if oldest == nil || e.used.Before(oldest.used) {
			oldest = e
		}
	}
	p.stats.Misses++
	var evicted *pooledSession
	if len(p.entries) >= p.options.MaxSessions {
		if oldest == nil {
			p.stats.Fallbacks++
			return nil, false, nil, nil
		}
		delete(p.entries, oldest)
		p.stats.Evictions++
		evicted = oldest
	}
	initialization, cancel := context.WithCancel(ctx)
	e := &pooledSession{key: key, busy: true, created: now, used: now, initialization: initialization, cancel: cancel}
	p.entries[e] = struct{}{}
	return e, false, evicted, nil
}

func (p *sessionPool) attach(e *pooledSession, s *mcp.ClientSession, t *protocolTransport, close func()) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return false
	}
	e.session, e.transport, e.close = s, t, close
	return true
}

func (p *sessionPool) release(e *pooledSession, healthy bool, now time.Time) *pooledSession {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, present := p.entries[e]; !present {
		return nil
	}
	if !healthy || p.closed || now.Sub(e.created) >= p.options.MaxLifetime {
		delete(p.entries, e)
		p.stats.Discards++
		return e
	}
	e.busy, e.used = false, now
	return nil
}

func disposeSession(e *pooledSession) {
	if e.cancel != nil {
		e.cancel()
	}
	if e.close != nil {
		cancel := e.transport.shutdown()
		e.close()
		cancel()
	}
}

func (p *sessionPool) reap() {
	defer close(p.done)
	ticker := time.NewTicker(min(p.options.IdleTTL, 30*time.Second))
	defer ticker.Stop()
	for {
		select {
		case <-p.stop:
			return
		case now := <-ticker.C:
			p.mu.Lock()
			var retired []*pooledSession
			for e := range p.entries {
				if !e.busy && p.expired(e, now) {
					delete(p.entries, e)
					p.stats.Evictions++
					retired = append(retired, e)
				}
			}
			p.mu.Unlock()
			disposeSessions(retired)
		}
	}
}

func (p *sessionPool) shutdown() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		<-p.closedDone
		return
	}
	defer close(p.closedDone)
	p.closed = true
	close(p.stop)
	var retired []*pooledSession
	for e := range p.entries {
		retired = append(retired, e)
		delete(p.entries, e)
	}
	p.mu.Unlock()
	disposeSessions(retired)
	<-p.done
}

// Pool eligibility is not authorization: each request still fences the live
// server configuration. Changes require a new session, never a mutated lease.
type serverScopeTransport struct {
	base     http.RoundTripper
	resolver Resolver
	server   core.MCPServer
}

func (t *serverScopeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	current, err := t.resolver.GetServer(req.Context(), t.server.WorkspaceID, t.server.ID)
	if err != nil || !current.Enabled || current != t.server {
		return nil, fmt.Errorf("MCP session server configuration is unavailable or changed")
	}
	return t.base.RoundTrip(req)
}

// All entries are bounded by MaxSessions. Parallel two-second teardown prevents
// slow upstream DELETE handlers from multiplying the process shutdown deadline.
func disposeSessions(entries []*pooledSession) {
	var group sync.WaitGroup
	for _, e := range entries {
		group.Go(func() { disposeSession(e) })
	}
	group.Wait()
}
