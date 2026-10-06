package httpadapter

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/alan1-666/mcp-gateway/internal/core"
)

func TestCircuitStateConcurrencyRecoveryAndBound(t *testing.T) {
	b := &circuitBreaker{}
	key := sha256.Sum256([]byte("one"))
	now := time.Now()
	// Old in-flight success cannot erase an open state triggered by newer failures.
	old, _ := b.admit(key, now)
	for i := 0; i < 3; i++ {
		p, ok := b.admit(key, now)
		if !ok {
			t.Fatal("early open")
		}
		b.complete(p, true, now)
	}
	b.complete(old, false, now)
	if _, ok := b.admit(key, now); ok {
		t.Fatal("open allowed")
	}
	later := now.Add(circuitCooldown)
	var admitted atomic.Int32
	var wg sync.WaitGroup
	var winner circuitPermit
	var mu sync.Mutex
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, ok := b.admit(key, later)
			if ok {
				admitted.Add(1)
				mu.Lock()
				winner = p
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if admitted.Load() != 1 {
		t.Fatal("half-open herd", admitted.Load())
	}
	b.complete(winner, true, later)
	if _, ok := b.admit(key, later); ok {
		t.Fatal("failed probe remained open")
	}
	recovered := later.Add(circuitCooldown)
	p, ok := b.admit(key, recovered)
	if !ok {
		t.Fatal("no recovery")
	}
	b.abandon(p)
	p, ok = b.admit(key, recovered)
	if !ok {
		t.Fatal("canceled probe stranded breaker")
	}
	b.complete(p, false, recovered)
	if _, ok := b.admit(key, recovered); !ok {
		t.Fatal("healthy circuit not closed")
	}
	b.complete(circuitPermit{}, true, now)
	b.abandon(circuitPermit{})
	b.complete(circuitPermit{tracked: true, key: sha256.Sum256([]byte("absent"))}, true, now)
	for i := 0; i < circuitKeyLimit+20; i++ {
		b.admit(sha256.Sum256([]byte(fmt.Sprint(i))), recovered)
	}
	if len(b.states) != circuitKeyLimit {
		t.Fatal("unbounded keyspace", len(b.states))
	}
	q, ok := b.admit(sha256.Sum256([]byte("new")), recovered)
	if !ok || q.tracked {
		t.Fatal("saturation handling")
	}
	q, ok = b.admit(sha256.Sum256([]byte("new")), recovered.Add(6*time.Minute))
	if !ok || !q.tracked || len(b.states) != 1 {
		t.Fatal("stale eviction", len(b.states))
	}
}
func TestTransientClassificationAndRetryBudget(t *testing.T) {
	for _, status := range []int{502, 503, 504} {
		if !transientResponse(&http.Response{StatusCode: status}, nil) {
			t.Fatal(status)
		}
	}
	for _, status := range []int{200, 301, 400, 401, 403, 404, 409, 429, 500} {
		if transientResponse(&http.Response{StatusCode: status}, nil) {
			t.Fatal(status)
		}
	}
	for _, err := range []error{context.Canceled, context.DeadlineExceeded, errors.New("bad TLS certificate"), errors.New("egress forbidden")} {
		if transientResponse(nil, err) {
			t.Fatal(err)
		}
	}
	for _, err := range []error{syscall.ECONNRESET, syscall.ECONNREFUSED, syscall.EPIPE, io.EOF, &net.DNSError{IsTimeout: true}} {
		if !transientResponse(nil, err) {
			t.Fatal(err)
		}
	}
	if transientResponse(nil, nil) {
		t.Fatal("nil is transient")
	}
	now := time.Now().UTC().Truncate(time.Second)
	for _, hint := range []string{"-1", "2", "bad", now.Add(10 * time.Second).Format(http.TimeFormat)} {
		if _, ok := retryDelay(hint, now, time.Time{}); ok {
			t.Fatal(hint)
		}
	}
	for _, hint := range []string{"", "0", "1", now.Format(http.TimeFormat), now.Add(-time.Second).Format(http.TimeFormat), now.Add(time.Second).Format(http.TimeFormat)} {
		if _, ok := retryDelay(hint, now, now.Add(2*time.Second)); !ok {
			t.Fatal(hint)
		}
	}
	if _, ok := retryDelay("1", now, now.Add(time.Second)); ok {
		t.Fatal("deadline ignored")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !errors.Is(waitRetry(ctx, time.Second), context.Canceled) {
		t.Fatal("cancellation ignored")
	}
	if err := waitRetry(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
}
func TestReadRetryButNeverWriteOrAuthSchemaFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		risk   core.Risk
		status int
		body   string
		want   int
		state  core.State
	}{
		{"read retries", core.RiskRead, 503, `{}`, 2, core.StateSucceeded},
		{"write never retries", core.RiskWrite, 503, `{}`, 1, core.StateUnknown},
		{"auth never retries", core.RiskRead, 401, `{}`, 1, core.StateFailed},
		{"invalid JSON never retries", core.RiskRead, 200, `not-json`, 1, core.StateFailed},
		{"wrong schema never retries", core.RiskRead, 200, `{"ok":"wrong"}`, 1, core.StateFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				if n == 1 {
					w.Header().Set("Retry-After", "0")
					w.WriteHeader(tc.status)
					io.WriteString(w, tc.body)
				} else {
					io.WriteString(w, `{"ok":true}`)
				}
			}))
			defer s.Close()
			a, _ := New([]string{s.URL}, []string{"127.0.0.0/8"}, nil)
			method := "GET"
			if tc.risk == core.RiskWrite {
				method = "POST"
			}
			result := a.Execute(context.Background(), core.Actor{WorkspaceID: "w"}, core.Tool{Risk: tc.risk, HTTP: core.HTTPConfig{URL: s.URL, Method: method, TimeoutMS: 1000}, OutputSchema: json.RawMessage(`{"type":"object","required":["ok"],"properties":{"ok":{"type":"boolean"}}}`)}, core.Operation{ID: "op", Arguments: json.RawMessage(`{}`)})
			if calls.Load() != int32(tc.want) || result.State != tc.state {
				t.Fatal(calls.Load(), result)
			}
		})
	}
}

type retryTransport func(*http.Request) (*http.Response, error)

func (f retryTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type retryResolver func(context.Context, string, string, string) (Credential, bool, error)

func (f retryResolver) Resolve(ctx context.Context, w, o, r string) (Credential, bool, error) {
	return f(ctx, w, o, r)
}
func retryResponse(code int, hint string) *http.Response {
	return &http.Response{StatusCode: code, Header: http.Header{"Retry-After": []string{hint}}, Body: io.NopCloser(strings.NewReader(`{}`))}
}
func TestRequestRetryRefreshesCredentialsAndDoesNotRetainRemovedHeaders(t *testing.T) {
	a, _ := New([]string{"https://example.com"}, nil, nil)
	var resolves, calls int
	a.SetCredentialResolver(retryResolver(func(_ context.Context, w, o, ref string) (Credential, bool, error) {
		resolves++
		headers := map[string]string{"X-Fresh": fmt.Sprint(resolves)}
		if resolves == 1 {
			headers["Authorization"] = "old"
		}
		return Credential{WorkspaceID: w, Origin: o, Ref: ref, Headers: headers}, true, nil
	}))
	client := &http.Client{Transport: retryTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Header.Get("X-Fresh") != fmt.Sprint(calls) {
			t.Fatal("stale credential")
		}
		if calls == 1 {
			return retryResponse(503, "0"), nil
		}
		if r.Header.Get("Authorization") != "" {
			t.Fatal("removed credential retained")
		}
		return retryResponse(200, ""), nil
	})}
	tool := core.Tool{Risk: core.RiskRead, HTTP: core.HTTPConfig{URL: "https://example.com/tool", Method: "GET", CredentialRef: "cred"}}
	request, _ := http.NewRequest("GET", tool.HTTP.URL, nil)
	resp, err := a.doRequest(context.Background(), core.Actor{WorkspaceID: "w"}, tool, request, client)
	if err != nil || resp.StatusCode != 200 || calls != 2 || resolves != 2 {
		t.Fatal(err, calls, resolves)
	}
	resp.Body.Close()
}
func TestRequestRetryPolicyRevocationCancellationAndNetworkFailure(t *testing.T) {
	tool := core.Tool{Risk: core.RiskRead, HTTP: core.HTTPConfig{URL: "https://example.com/tool", Method: "GET"}}
	request, _ := http.NewRequest("GET", tool.HTTP.URL, nil)
	actor := core.Actor{WorkspaceID: "w"}
	t.Run("no credentials or egress", func(t *testing.T) {
		a, _ := New(nil, nil, nil)
		if _, err := a.doRequest(context.Background(), actor, tool, request, http.DefaultClient); !errors.Is(err, errRequestPolicy) {
			t.Fatal(err)
		}
	})
	t.Run("retry policy revoked", func(t *testing.T) {
		a, _ := New([]string{"https://example.com"}, nil, nil)
		reads := 0
		a.SetCredentialResolver(retryResolver(func(_ context.Context, w, o, r string) (Credential, bool, error) {
			reads++
			if reads > 1 {
				return Credential{}, false, errors.New("revoked")
			}
			return Credential{WorkspaceID: w, Origin: o, Ref: r}, true, nil
		}))
		tt := tool
		tt.HTTP.CredentialRef = "cred"
		client := &http.Client{Transport: retryTransport(func(*http.Request) (*http.Response, error) { return retryResponse(503, "0"), nil })}
		if _, err := a.doRequest(context.Background(), actor, tt, request, client); !errors.Is(err, errRequestPolicy) {
			t.Fatal(err)
		}
	})
	t.Run("canceled before attempt", func(t *testing.T) {
		a, _ := New([]string{"https://example.com"}, nil, nil)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := a.doRequest(ctx, actor, tool, request, http.DefaultClient); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	})
	t.Run("canceled during backoff", func(t *testing.T) {
		a, _ := New([]string{"https://example.com"}, nil, nil)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		client := &http.Client{Transport: retryTransport(func(*http.Request) (*http.Response, error) { cancel(); return retryResponse(503, "1"), nil })}
		if _, err := a.doRequest(ctx, actor, tool, request, client); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	})
	t.Run("long Retry-After not shortened", func(t *testing.T) {
		a, _ := New([]string{"https://example.com"}, nil, nil)
		calls := 0
		client := &http.Client{Transport: retryTransport(func(*http.Request) (*http.Response, error) { calls++; return retryResponse(503, "60"), nil })}
		resp, err := a.doRequest(context.Background(), actor, tool, request, client)
		if err != nil || calls != 1 || resp.StatusCode != 503 {
			t.Fatal(err, calls)
		}
		resp.Body.Close()
	})
	t.Run("network retry", func(t *testing.T) {
		a, _ := New([]string{"https://example.com"}, nil, nil)
		calls := 0
		client := &http.Client{Transport: retryTransport(func(*http.Request) (*http.Response, error) {
			calls++
			if calls == 1 {
				return nil, io.EOF
			}
			return retryResponse(200, ""), nil
		})}
		resp, err := a.doRequest(context.Background(), actor, tool, request, client)
		if err != nil || calls != 2 {
			t.Fatal(err, calls)
		}
		resp.Body.Close()
	})
}
func TestRequestCircuitTripsAndIsolatesWorkspace(t *testing.T) {
	a, _ := New([]string{"https://example.com"}, nil, nil)
	calls := 0
	client := &http.Client{Transport: retryTransport(func(*http.Request) (*http.Response, error) { calls++; return retryResponse(503, "0"), nil })}
	tool := core.Tool{Risk: core.RiskRead, HTTP: core.HTTPConfig{URL: "https://example.com/tool", Method: "GET"}}
	request, _ := http.NewRequest("GET", tool.HTTP.URL, nil)
	for i := 0; i < 3; i++ {
		resp, err := a.doRequest(context.Background(), core.Actor{WorkspaceID: "a"}, tool, request, client)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	if calls != 6 {
		t.Fatal(calls)
	}
	if _, err := a.doRequest(context.Background(), core.Actor{WorkspaceID: "a"}, tool, request, client); !errors.Is(err, errCircuitOpen) || calls != 6 {
		t.Fatal("breaker failed", err, calls)
	}
	resp, err := a.doRequest(context.Background(), core.Actor{WorkspaceID: "b"}, tool, request, client)
	if err != nil || calls != 8 {
		t.Fatal("cross workspace interference", err, calls)
	}
	resp.Body.Close()
}
