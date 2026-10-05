package runs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alan1-666/mcp-gateway/internal/adapters/httpadapter"
	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/alan1-666/mcp-gateway/internal/execution"
	"github.com/alan1-666/mcp-gateway/internal/identity"
	"github.com/alan1-666/mcp-gateway/internal/store/postgres"
	"github.com/alan1-666/mcp-gateway/migrations"
	"github.com/jackc/pgx/v5/pgxpool"
)

type fixture struct {
	pool                         *pgxpool.Pool
	repo                         *Repository
	svc                          *core.Service
	actor, admin, other, foreign core.Actor
}

func database(t *testing.T) fixture {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	control, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "runs_" + strings.ReplaceAll(core.NewID(), "-", "")
	if _, err = control.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		control.Close()
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	cfg.MaxConns = 2
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, e := control.Exec(c, "DROP SCHEMA "+schema+" CASCADE"); e != nil {
			t.Error(e)
		}
		control.Close()
	})
	if err = migrations.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	f := fixture{pool: pool, repo: NewRepository(pool), svc: core.NewService(postgres.New(pool)), actor: core.Actor{ID: "creator", WorkspaceID: "team", Role: core.RoleOperator}, admin: core.Actor{ID: "admin", WorkspaceID: "team", Role: core.RoleAdmin}, other: core.Actor{ID: "other", WorkspaceID: "team", Role: core.RoleOperator}, foreign: core.Actor{ID: "foreign", WorkspaceID: "different", Role: core.RoleAdmin}}
	for _, a := range []core.Actor{f.actor, f.admin, f.other, f.foreign} {
		if _, err = pool.Exec(ctx, `INSERT INTO gateway_users(id,workspace_id,username,password_hash,role) VALUES($1,$2,$1,$3,$4)`, a.ID, a.WorkspaceID, []byte("unused"), a.Role); err != nil {
			t.Fatal(err)
		}
	}
	return f
}
func (f fixture) create(t *testing.T, a core.Actor) Run {
	t.Helper()
	v, err := f.repo.Create(context.Background(), a, CreateInput{Prompt: "Check the governed service", IdempotencyKey: core.NewID()})
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func (f fixture) claim(t *testing.T, worker string) Claim {
	t.Helper()
	v, err := f.repo.Claim(context.Background(), "team", worker)
	if err != nil {
		t.Fatal(err)
	}
	if v.Run == nil {
		t.Fatal("expected a claim")
	}
	return v
}
func (f fixture) tool(t *testing.T, risk core.Risk, endpoint string) core.Tool {
	t.Helper()
	method := "GET"
	if risk == core.RiskWrite {
		method = "POST"
	}
	v, err := f.svc.CreateTool(context.Background(), f.admin, core.ToolInput{Name: "tool_" + core.NewID(), Description: "Test governed tool", Risk: risk, InputSchema: json.RawMessage(`{"type":"object","properties":{"value":{"type":"integer"}},"additionalProperties":false}`), HTTP: core.HTTPConfig{URL: endpoint, Method: method, TimeoutMS: 5000}})
	if err != nil {
		t.Fatal(err)
	}
	v, err = f.svc.PublishTool(context.Background(), f.admin, v.ID)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func (f fixture) prepare(t *testing.T, c Claim, tool core.Tool, key string) core.Operation {
	t.Helper()
	v, err := f.repo.Prepare(context.Background(), f.svc, "team", c.Run.ID, c.LeaseToken, core.PrepareInput{ToolID: tool.ID, Arguments: json.RawMessage(`{"value":1}`), IdempotencyKey: key})
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func (f fixture) sql(t *testing.T, q string, args ...any) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(), q, args...); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentCreateQueueAndClaimLimits(t *testing.T) {
	f := database(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	var created atomic.Int32
	for i := 0; i < 25; i++ {
		wg.Go(func() {
			_, err := f.repo.Create(ctx, f.actor, CreateInput{Prompt: "Concurrent task", IdempotencyKey: core.NewID()})
			if err == nil {
				created.Add(1)
			} else if !errors.Is(err, core.ErrConflict) {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if created.Load() != 10 {
		t.Fatalf("queue admitted %d, want 10", created.Load())
	}
	var claimed atomic.Int32
	for i := 0; i < 16; i++ {
		worker := fmt.Sprintf("worker-%d", i)
		wg.Go(func() {
			v, err := f.repo.Claim(ctx, "team", worker)
			if err != nil {
				t.Error(err)
			} else if v.Run != nil {
				claimed.Add(1)
			}
		})
	}
	wg.Wait()
	if claimed.Load() != 1 {
		t.Fatalf("user has %d active leases", claimed.Load())
	}
	other := f.create(t, f.other)
	c, err := f.repo.Claim(ctx, "team", "fresh-worker")
	if err != nil || c.Run == nil || c.Run.ID != other.ID {
		t.Fatalf("other user claim: %+v %v", c, err)
	}
	if again, err := f.repo.Claim(ctx, "team", "fresh-worker"); err != nil || again.Run != nil {
		t.Fatalf("worker concurrency limit: %+v %v", again, err)
	}
}
func TestRunReplayPrivacyAndLeaseRecovery(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	in := CreateInput{Prompt: "Immutable task", IdempotencyKey: "same-run-key"}
	run, err := f.repo.Create(ctx, f.actor, in)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := f.repo.Create(ctx, f.actor, in)
	if err != nil || replay.ID != run.ID {
		t.Fatalf("replay %+v %v", replay, err)
	}
	in.Prompt = "Changed"
	if _, err = f.repo.Create(ctx, f.actor, in); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("changed prompt: %v", err)
	}
	for _, a := range []core.Actor{f.other, f.foreign} {
		if _, err = f.repo.Get(ctx, a, run.ID); !errors.Is(err, core.ErrNotFound) {
			t.Fatalf("privacy %v: %v", a, err)
		}
	}
	if _, err = f.repo.Get(ctx, f.admin, run.ID); err != nil {
		t.Fatal(err)
	}
	c := f.claim(t, "stable-worker")
	var saved []byte
	if err = f.pool.QueryRow(ctx, `SELECT lease_hash FROM agent_runs WHERE id=$1`, run.ID).Scan(&saved); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(saved, []byte(c.LeaseToken)) || len(saved) != 32 {
		t.Fatal("lease token not hashed")
	}
	f.sql(t, `UPDATE agent_runs SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, run.ID)
	if _, err = f.repo.Heartbeat(ctx, "team", run.ID, c.LeaseToken); !errors.Is(err, ErrLease) {
		t.Fatalf("expired heartbeat: %v", err)
	}
	if n, err := f.repo.RecoverExpired(ctx, ""); err != nil || n != 1 {
		t.Fatalf("recover %d %v", n, err)
	}
	reviewed, err := f.repo.Get(ctx, f.actor, run.ID)
	if err != nil || reviewed.State != NeedsReview {
		t.Fatalf("recovered %+v %v", reviewed, err)
	}
	if v, err := f.repo.Claim(ctx, "team", "stable-worker"); err != nil || v.Run != nil {
		t.Fatalf("automatic replay %+v %v", v, err)
	}
	if _, err = f.repo.Resume(ctx, f.actor, run.ID); err != nil {
		t.Fatal(err)
	}
	if v, err := f.repo.Claim(ctx, "team", "other-worker"); err != nil || v.Run != nil {
		t.Fatalf("worker binding %+v %v", v, err)
	}
	newer := f.claim(t, "stable-worker")
	if newer.Run.Attempt != 2 || newer.LeaseToken == c.LeaseToken {
		t.Fatal("attempt fencing failed")
	}
	if _, err = f.repo.Finish(ctx, "team", run.ID, c.LeaseToken, FinishInput{State: Succeeded}); !errors.Is(err, ErrLease) {
		t.Fatalf("stale finish: %v", err)
	}
	event := EventInput{EventKey: "attempt2-1", Type: "TEXT_DELTA", Data: json.RawMessage(`{"text":"hello","a":1}`)}
	if err = f.repo.Append(ctx, "team", run.ID, newer.LeaseToken, event); err != nil {
		t.Fatal(err)
	}
	event.Data = json.RawMessage(`{"a":1,"text":"hello"}`)
	if err = f.repo.Append(ctx, "team", run.ID, newer.LeaseToken, event); err != nil {
		t.Fatal(err)
	}
	event.Data = json.RawMessage(`{"text":"different"}`)
	if err = f.repo.Append(ctx, "team", run.ID, newer.LeaseToken, event); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("changed event: %v", err)
	}
	events, err := f.repo.Events(ctx, f.actor, run.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 6 {
		t.Fatalf("events %d, want 6", len(events))
	}
	if _, err = f.repo.Finish(ctx, "team", run.ID, newer.LeaseToken, FinishInput{State: Succeeded, Output: strings.Repeat("x", MaxOutputBytes+1)}); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("output bound: %v", err)
	}
	done, err := f.repo.Finish(ctx, "team", run.ID, newer.LeaseToken, FinishInput{State: Succeeded, Output: "complete"})
	if err != nil || done.State != Succeeded {
		t.Fatalf("finish %+v %v", done, err)
	}
}
func TestAtomicPrepareWriteDedupAndRunScope(t *testing.T) {
	f := database(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	run := f.create(t, f.actor)
	c := f.claim(t, "worker")
	tool := f.tool(t, core.RiskWrite, "https://example.com/action")
	// A failing binding insert must roll back the core savepoint too.
	f.sql(t, `ALTER TABLE agent_run_operations ADD CONSTRAINT deny_binding CHECK(false) NOT VALID`)
	input := core.PrepareInput{ToolID: tool.ID, Arguments: json.RawMessage(`{"value":1}`), IdempotencyKey: "logical-step"}
	if _, err := f.repo.Prepare(ctx, f.svc, "team", run.ID, c.LeaseToken, input); err == nil {
		t.Fatal("expected injected binding failure")
	}
	var n int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM operations`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("operation escaped rollback: %d %v", n, err)
	}
	f.sql(t, `ALTER TABLE agent_run_operations DROP CONSTRAINT deny_binding`)
	var wg sync.WaitGroup
	ids := make(chan string, 12)
	for i := 0; i < 12; i++ {
		key := fmt.Sprintf("write-step-%d", i)
		wg.Go(func() {
			in := input
			in.IdempotencyKey = key
			op, err := f.repo.Prepare(ctx, f.svc, "team", run.ID, c.LeaseToken, in)
			if err != nil {
				t.Error(err)
				return
			}
			ids <- op.ID
		})
	}
	wg.Wait()
	close(ids)
	operation := ""
	for id := range ids {
		if operation != "" && id != operation {
			t.Fatal("duplicate write operation")
		}
		operation = id
	}
	if operation == "" {
		t.Fatal("no operation prepared")
	}
	input.IdempotencyKey = "write-step-0"
	input.Arguments = json.RawMessage(`{"value":2}`)
	if _, err := f.repo.Prepare(ctx, f.svc, "team", run.ID, c.LeaseToken, input); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("intent changed: %v", err)
	}
	f.create(t, f.other)
	other := f.claim(t, "worker-two")
	if _, err := f.repo.Operation(ctx, f.svc, "team", other.Run.ID, other.LeaseToken, operation); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("cross-run operation: %v", err)
	}
	if _, err := f.repo.Finish(ctx, "team", other.Run.ID, other.LeaseToken, FinishInput{State: WaitingApproval, WaitingOperationID: operation}); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("foreign waiting ref: %v", err)
	}
	if _, err := f.repo.Finish(ctx, "team", run.ID, c.LeaseToken, FinishInput{State: WaitingApproval, WaitingOperationID: operation}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.repo.Resume(ctx, f.actor, run.ID); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("approval bypass: %v", err)
	}
	if _, err := f.svc.Approve(ctx, f.admin, operation); err != nil {
		t.Fatal(err)
	}
	if _, err := f.repo.Resume(ctx, f.actor, run.ID); err != nil {
		t.Fatal(err)
	}
	next := f.claim(t, "worker")
	recovered := f.prepare(t, next, tool, "after-resume")
	if recovered.ID != operation {
		t.Fatal("resume changed write identity")
	}
	f.sql(t, `UPDATE operations SET state='UNKNOWN' WHERE id=$1`, operation)
	if _, err := f.repo.Finish(ctx, "team", run.ID, next.LeaseToken, FinishInput{State: NeedsReview, WaitingOperationID: operation}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.repo.Resume(ctx, f.actor, run.ID); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("unknown replay: %v", err)
	}
}
func TestCreatorLiveAccessAndAdminAttenuation(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	run := f.create(t, f.admin)
	c := f.claim(t, "admin-worker")
	actor, err := f.repo.GatewayActor(ctx, "team", run.ID, c.LeaseToken)
	if err != nil || actor.Role != core.RoleOperator || actor.ID != f.admin.ID {
		t.Fatalf("admin attenuation %+v %v", actor, err)
	}
	f.sql(t, `UPDATE gateway_users SET role='viewer' WHERE id=$1`, f.admin.ID)
	if _, err = f.repo.Heartbeat(ctx, "team", run.ID, c.LeaseToken); !errors.Is(err, core.ErrForbidden) {
		t.Fatalf("downgraded actor: %v", err)
	}
	f.sql(t, `UPDATE gateway_users SET role='admin',disabled=true WHERE id=$1`, f.admin.ID)
	if _, err = f.repo.GatewayActor(ctx, "team", run.ID, c.LeaseToken); !errors.Is(err, core.ErrForbidden) {
		t.Fatalf("disabled actor: %v", err)
	}
	if err = f.repo.Append(ctx, "team", run.ID, c.LeaseToken, EventInput{EventKey: "late", Type: "MODEL_STARTED", Data: json.RawMessage(`{}`)}); !errors.Is(err, core.ErrForbidden) {
		t.Fatalf("revoked event: %v", err)
	}
	if _, err = f.repo.Finish(ctx, "team", run.ID, c.LeaseToken, FinishInput{State: Succeeded}); !errors.Is(err, core.ErrForbidden) {
		t.Fatalf("revoked finish: %v", err)
	}
}
func TestCancellationPreservesAdmittedOperationOutcome(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	entered := make(chan struct{})
	release := make(chan struct{})
	var requests atomic.Int32
	downstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		close(entered)
		<-release
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer downstream.Close()
	adapter, err := httpadapter.New([]string{downstream.URL}, []string{"127.0.0.1/32"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	executor := &execution.Executor{Service: f.svc, Adapter: adapter}
	run := f.create(t, f.actor)
	c := f.claim(t, "worker")
	tool := f.tool(t, core.RiskWrite, downstream.URL)
	op := f.prepare(t, c, tool, "write-step")
	if _, err = f.svc.Approve(ctx, f.admin, op.ID); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		v, e := f.repo.Execute(ctx, executor, "team", run.ID, c.LeaseToken, op.ID)
		if e == nil && v.State != core.StateSucceeded {
			e = fmt.Errorf("outcome %s", v.State)
		}
		result <- e
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		close(release)
		t.Fatal("operation never admitted")
	}
	cancelled, err := f.repo.Cancel(ctx, f.actor, run.ID)
	if err != nil || cancelled.State != Cancelled {
		close(release)
		t.Fatalf("cancel %+v %v", cancelled, err)
	}
	if _, err = f.repo.Execute(ctx, executor, "team", run.ID, c.LeaseToken, op.ID); !errors.Is(err, ErrLease) {
		close(release)
		t.Fatalf("cancelled admission: %v", err)
	}
	if _, err = f.repo.Finish(ctx, "team", run.ID, c.LeaseToken, FinishInput{State: Succeeded}); !errors.Is(err, ErrLease) {
		close(release)
		t.Fatalf("cancelled finish: %v", err)
	}
	close(release)
	if err = <-result; err != nil {
		t.Fatal(err)
	}
	final, err := f.svc.GetOperation(ctx, f.actor, op.ID)
	if err != nil || final.State != core.StateSucceeded {
		t.Fatalf("operation ledger %+v %v", final, err)
	}
	finalRun, err := f.repo.Get(ctx, f.actor, run.ID)
	if err != nil || finalRun.State != Cancelled || requests.Load() != 1 {
		t.Fatalf("run %+v requests %d err %v", finalRun, requests.Load(), err)
	}
}

func TestHTTPAuthCSRFIsolationRuntimeAndEventCursor(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	auth, err := identity.NewCloud(ctx, f.pool, "https://console.example", "")
	if err != nil {
		t.Fatal(err)
	}
	key := strings.Repeat("k", 40)
	hash := sha256.Sum256([]byte(key))
	f.sql(t, `INSERT INTO gateway_api_keys(id,user_id,token_hash,name,expires_at) VALUES('test-key',$1,$2,'test',clock_timestamp()+interval '1 hour')`, f.actor.ID, hash[:])
	session := "session-token"
	sessionHash := sha256.Sum256([]byte(session))
	f.sql(t, `INSERT INTO gateway_sessions(token_hash,user_id,csrf_token,expires_at) VALUES($1,$2,'csrf',clock_timestamp()+interval '1 hour')`, sessionHash[:], f.actor.ID)
	adapter, err := httpadapter.New(nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	secret := strings.Repeat("s", 40)
	api, err := New(f.pool, f.svc, &execution.Executor{Service: f.svc, Adapter: adapter}, Config{WorkspaceID: "team", SharedSecret: secret})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/api/", api.PublicHandler(auth))
	mux.Handle("/internal/", api.InternalHandler())
	call := func(method, path, body, bearer, lease string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		req.Header.Set("X-Run-Lease", lease)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	if w := call("POST", "/internal/runner/claim", `{"worker_id":"worker"}`, key, ""); w.Code != 401 {
		t.Fatalf("human key authorized daemon: %d %s", w.Code, w.Body)
	}
	req := httptest.NewRequest("POST", "/api/v1/runs", strings.NewReader(`{"prompt":"hello","idempotency_key":"cookie-key"}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "__Host-gateway-session", Value: session})
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatalf("missing CSRF: %d", w.Code)
	}
	w = call("POST", "/api/v1/runs", `{"prompt":"hello","idempotency_key":"api-key"}`, key, "")
	if w.Code != 200 {
		t.Fatalf("create %d %s", w.Code, w.Body)
	}
	var run Run
	if err = json.Unmarshal(w.Body.Bytes(), &run); err != nil {
		t.Fatal(err)
	}
	w = call("POST", "/internal/runner/claim", `{"worker_id":"worker"}`, secret, "")
	if w.Code != 200 {
		t.Fatalf("claim %d %s", w.Code, w.Body)
	}
	var claim Claim
	if err = json.Unmarshal(w.Body.Bytes(), &claim); err != nil {
		t.Fatal(err)
	}
	base := "/internal/runner/runs/" + run.ID
	if w = call("GET", base+"/gateway/me", "", secret, claim.LeaseToken); w.Code != 200 {
		t.Fatalf("me %d %s", w.Code, w.Body)
	}
	for _, path := range []string{"/gateway/tools/foo/publish", "/gateway/operations/foo/approve", "/gateway/auth/keys"} {
		if w = call("POST", base+path, `{}`, secret, claim.LeaseToken); w.Code != 404 {
			t.Fatalf("privileged route %s: %d", path, w.Code)
		}
	}
	if w = call("GET", base+"/gateway/me", "", secret, ""); w.Code != 409 {
		t.Fatalf("missing lease %d", w.Code)
	}
	if w = call("POST", "/internal/runner/status", `{"worker_id":"worker","model_ready":true,"provider":"test","model_id":"fake","error_code":""}`, secret, ""); w.Code != 200 {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	w = call("GET", "/api/v1/runs/runtime", "", key, "")
	var runtime Runtime
	if err = json.Unmarshal(w.Body.Bytes(), &runtime); err != nil || !runtime.Online || !runtime.ModelReady {
		t.Fatalf("runtime %+v %v", runtime, err)
	}
	f.sql(t, `UPDATE agent_runner_status SET last_seen_at=clock_timestamp()-interval '91 seconds'`)
	w = call("GET", "/api/v1/runs/runtime", "", key, "")
	if err = json.Unmarshal(w.Body.Bytes(), &runtime); err != nil || runtime.Online || runtime.ModelReady {
		t.Fatalf("stale runtime %+v %v", runtime, err)
	}
	w = call("GET", "/api/v1/runs/"+run.ID+"/events?after=0", "", key, "")
	var events struct {
		Items []Event `json:"items"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &events); err != nil || len(events.Items) != 2 {
		t.Fatalf("events %s %v", w.Body, err)
	}
	w = call("GET", "/api/v1/runs/"+run.ID+"/events?after="+events.Items[1].ID, "", key, "")
	if err = json.Unmarshal(w.Body.Bytes(), &events); err != nil || len(events.Items) != 0 {
		t.Fatalf("cursor %s %v", w.Body, err)
	}
	f.sql(t, `UPDATE gateway_api_keys SET revoked_at=clock_timestamp()`)
	if w = call("GET", "/api/v1/runs", "", key, ""); w.Code != 401 {
		t.Fatalf("revoked public key %d", w.Code)
	}
}

func TestBudgetsAndLeaseExpiryDuringLockWait(t *testing.T) {
	f := database(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	run := f.create(t, f.actor)
	c := f.claim(t, "worker")
	tool := f.tool(t, core.RiskRead, "https://example.com/read")
	op := f.prepare(t, c, tool, "read-step")
	f.sql(t, `UPDATE agent_runs SET tool_calls=40 WHERE id=$1`, run.ID)
	if replay := f.prepare(t, c, tool, "read-step"); replay.ID != op.ID {
		t.Fatal("budget blocked idempotent replay")
	}
	_, err := f.repo.Prepare(ctx, f.svc, "team", run.ID, c.LeaseToken, core.PrepareInput{ToolID: tool.ID, IdempotencyKey: "next-step", Arguments: json.RawMessage(`{}`)})
	if !errors.Is(err, ErrBudget) {
		t.Fatalf("tool budget: %v", err)
	}
	event := EventInput{EventKey: "last-event", Type: "TEXT_DELTA", Data: json.RawMessage(`{"text":"hello","attempt":999}`)}
	if err = f.repo.Append(ctx, "team", run.ID, c.LeaseToken, event); err != nil {
		t.Fatal(err)
	}
	var trusted int
	if err = f.pool.QueryRow(ctx, `SELECT (data->>'attempt')::int FROM agent_run_events WHERE event_key='last-event'`).Scan(&trusted); err != nil || trusted != 1 {
		t.Fatalf("event attempt %d %v", trusted, err)
	}
	f.sql(t, `UPDATE agent_runs SET event_count=1000 WHERE id=$1`, run.ID)
	if err = f.repo.Append(ctx, "team", run.ID, c.LeaseToken, event); err != nil {
		t.Fatalf("budget blocked event replay: %v", err)
	}
	event.EventKey = "next-event"
	if err = f.repo.Append(ctx, "team", run.ID, c.LeaseToken, event); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("event budget: %v", err)
	}
	// Do not update the row in the blocking transaction. PostgreSQL otherwise
	// rechecks the predicate on the new row version, masking this expiry race.
	f.sql(t, `UPDATE agent_runs SET lease_expires_at=clock_timestamp()+interval '150 milliseconds' WHERE id=$1`, run.ID)
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, `SELECT id FROM agent_runs WHERE id=$1 FOR UPDATE`, run.ID); err != nil {
		t.Fatal(err)
	}
	heartbeat := make(chan error, 1)
	go func() { _, e := f.repo.Heartbeat(ctx, "team", run.ID, c.LeaseToken); heartbeat <- e }()
	if _, err = tx.Exec(ctx, `SELECT pg_sleep(0.25)`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-heartbeat; !errors.Is(err, ErrLease) {
		t.Fatalf("expired lease revived after row-lock wait: %v", err)
	}
}

func TestExpiredApprovalPersistsRejectedLedger(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	f.create(t, f.actor)
	c := f.claim(t, "worker")
	tool := f.tool(t, core.RiskWrite, "https://example.com/write")
	op := f.prepare(t, c, tool, "write-step")
	if _, err := f.svc.Approve(ctx, f.admin, op.ID); err != nil {
		t.Fatal(err)
	}
	f.sql(t, `UPDATE operations SET approval_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, op.ID)
	adapter, err := httpadapter.New(nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.repo.Execute(ctx, &execution.Executor{Service: f.svc, Adapter: adapter}, "team", c.Run.ID, c.LeaseToken, op.ID)
	if !errors.Is(err, core.ErrApprovalExpired) {
		t.Fatalf("expired approval: %v", err)
	}
	current, err := f.svc.GetOperation(ctx, f.actor, op.ID)
	if err != nil || current.State != core.StateRejected {
		t.Fatalf("rejection was rolled back: %+v %v", current, err)
	}
}

func TestResumeRenewsEventBudgetButRetainsToolBudget(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	run := f.create(t, f.actor)
	first := f.claim(t, "stable-worker")
	tool := f.tool(t, core.RiskRead, "https://example.com/read")
	f.sql(t, `UPDATE agent_runs SET event_count=$2,event_bytes=$3,tool_calls=$4 WHERE id=$1`, run.ID, MaxEvents, MaxStoredEventBytes, MaxToolCalls)
	event := EventInput{EventKey: "attempt-1-over-budget", Type: "TEXT_DELTA", Data: json.RawMessage(`{"text":"budget exhausted"}`)}
	if err := f.repo.Append(ctx, "team", run.ID, first.LeaseToken, event); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("exhausted attempt event budget: %v", err)
	}
	if _, err := f.repo.Finish(ctx, "team", run.ID, first.LeaseToken, FinishInput{State: NeedsReview, ErrorCode: "EVENT_BUDGET_EXHAUSTED"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.repo.Resume(ctx, f.actor, run.ID); err != nil {
		t.Fatal(err)
	}
	second := f.claim(t, "stable-worker")
	if second.Run.Attempt != 2 {
		t.Fatalf("attempt %d, want 2", second.Run.Attempt)
	}
	var count, size, calls int
	if err := f.pool.QueryRow(ctx, `SELECT event_count,event_bytes,tool_calls FROM agent_runs WHERE id=$1`, run.ID).Scan(&count, &size, &calls); err != nil {
		t.Fatal(err)
	}
	if count != 0 || size != 0 || calls != MaxToolCalls {
		t.Fatalf("budget reset count=%d bytes=%d calls=%d", count, size, calls)
	}
	event.EventKey = "attempt-2-event"
	if err := f.repo.Append(ctx, "team", run.ID, second.LeaseToken, event); err != nil {
		t.Fatalf("new attempt event admission: %v", err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT event_count,event_bytes FROM agent_runs WHERE id=$1`, run.ID).Scan(&count, &size); err != nil {
		t.Fatal(err)
	}
	if count != 1 || size <= 0 {
		t.Fatalf("new attempt counters count=%d bytes=%d", count, size)
	}
	_, err := f.repo.Prepare(ctx, f.svc, "team", run.ID, second.LeaseToken, core.PrepareInput{ToolID: tool.ID, IdempotencyKey: "new-attempt-intent", Arguments: json.RawMessage(`{}`)})
	if !errors.Is(err, ErrBudget) {
		t.Fatalf("lifetime tool budget was reset: %v", err)
	}
	if err = f.repo.Append(ctx, "team", run.ID, first.LeaseToken, event); !errors.Is(err, ErrLease) {
		t.Fatalf("old attempt was unfenced: %v", err)
	}
}
