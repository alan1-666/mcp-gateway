package httpadapter

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/alan1-666/mcp-gateway/internal/core"
)

var (
	errCircuitOpen   = errors.New("downstream circuit is open; retry later")
	errRequestPolicy = errors.New("tool is blocked by the current egress or credential policy")
)

const circuitKeyLimit = 1024
const circuitCooldown = 15 * time.Second

type circuitState struct {
	failures            int
	openUntil, lastSeen time.Time
	probe               bool
	generation          uint64
}
type circuitPermit struct {
	key        [32]byte
	generation uint64
	tracked    bool
}
type circuitBreaker struct {
	mu     sync.Mutex
	states map[[32]byte]*circuitState
}

func (b *circuitBreaker) admit(key [32]byte, now time.Time) (circuitPermit, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.states == nil {
		b.states = make(map[[32]byte]*circuitState)
	}
	s := b.states[key]
	if s == nil {
		if len(b.states) >= circuitKeyLimit {
			for k, v := range b.states {
				if !v.probe && now.Sub(v.lastSeen) > 5*time.Minute {
					delete(b.states, k)
				}
			}
		}
		// Saturated tracking never grows memory or blocks unrelated healthy hosts;
		// untracked requests retain the same two-attempt/overall-time budget.
		if len(b.states) >= circuitKeyLimit {
			return circuitPermit{}, true
		}
		s = &circuitState{}
		b.states[key] = s
	}
	s.lastSeen = now
	if !s.openUntil.IsZero() {
		if now.Before(s.openUntil) || s.probe {
			return circuitPermit{}, false
		}
		s.probe = true
		s.generation++
	}
	return circuitPermit{key: key, generation: s.generation, tracked: true}, true
}
func (b *circuitBreaker) complete(p circuitPermit, transient bool, now time.Time) {
	if !p.tracked {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.states[p.key]
	if s == nil || s.generation != p.generation {
		return
	}
	s.lastSeen = now
	if !transient {
		s.failures = 0
		s.openUntil = time.Time{}
		s.probe = false
		return
	}
	s.failures++
	if s.probe || s.failures >= 3 {
		s.openUntil = now.Add(circuitCooldown)
		s.probe = false
		s.generation++
	}
}

func (b *circuitBreaker) abandon(p circuitPermit) {
	if !p.tracked {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if s := b.states[p.key]; s != nil && s.generation == p.generation {
		s.probe = false
	}
}

func transientResponse(resp *http.Response, err error) bool {
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return false
		}
		var ne net.Error
		return errors.Is(err, io.EOF) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.EPIPE) || (errors.As(err, &ne) && (ne.Timeout() || ne.Temporary()))
	}
	return resp != nil && (resp.StatusCode == 502 || resp.StatusCode == 503 || resp.StatusCode == 504)
}

// Retry-After is respected, never shortened. Long/malformed hints or insufficient
// remaining operation time suppress retry instead of stretching the deadline.
func retryDelay(header string, now time.Time, deadline time.Time) (time.Duration, bool) {
	delay := 100 * time.Millisecond
	if header != "" {
		seconds, err := strconv.Atoi(header)
		if err == nil {
			if seconds < 0 || seconds > 1 {
				return 0, false
			}
			delay = time.Duration(seconds) * time.Second
		} else {
			at, err := http.ParseTime(header)
			if err != nil {
				return 0, false
			}
			delay = max(0, at.Sub(now))
		}
	}
	if delay > time.Second || (!deadline.IsZero() && !now.Add(delay+50*time.Millisecond).Before(deadline)) {
		return 0, false
	}
	return delay, true
}
func waitRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (a *Adapter) doRequest(ctx context.Context, actor core.Actor, tool core.Tool, request *http.Request, client *http.Client) (*http.Response, error) {
	retryable := tool.Risk == core.RiskRead && tool.HTTP.Method == http.MethodGet
	permit := circuitPermit{}
	// Resolve before admission so a denied policy cannot consume a half-open probe.
	credential, err := a.resolveCredential(ctx, actor.WorkspaceID, tool.HTTP)
	if err != nil {
		return nil, errRequestPolicy
	}
	if retryable {
		u, _ := url.Parse(tool.HTTP.URL)
		key := sha256.Sum256([]byte(actor.WorkspaceID + "\x00" + u.Scheme + "://" + u.Host + "\x00" + tool.HTTP.CredentialRef))
		var allowed bool
		permit, allowed = a.breaker.admit(key, time.Now())
		if !allowed {
			return nil, errCircuitOpen
		}
	}
	transient := false
	defer func() {
		if retryable {
			if ctx.Err() != nil {
				a.breaker.abandon(permit)
			} else {
				a.breaker.complete(permit, transient, time.Now())
			}
		}
	}()
	for attempt := 0; ; attempt++ {
		if attempt > 0 {
			credential, err = a.resolveCredential(ctx, actor.WorkspaceID, tool.HTTP)
			if err != nil {
				return nil, errRequestPolicy
			}
		}
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		next := request.Clone(ctx)
		for k, v := range credential.Headers {
			next.Header.Set(k, v)
		}
		resp, callErr := client.Do(next)
		transient = transientResponse(resp, callErr)
		if !retryable || !transient || attempt == 1 {
			return resp, callErr
		}
		hint := ""
		if resp != nil {
			hint = resp.Header.Get("Retry-After")
		}
		deadline, _ := ctx.Deadline()
		delay, ok := retryDelay(hint, time.Now(), deadline)
		if !ok {
			return resp, callErr
		}
		if resp != nil {
			resp.Body.Close()
		}
		if err = waitRetry(ctx, delay); err != nil {
			transient = false
			return nil, err
		}
	}
}
