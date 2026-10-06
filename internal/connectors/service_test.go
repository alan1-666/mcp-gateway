package connectors

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alan1-666/mcp-gateway/internal/connectorwire"
	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/alan1-666/mcp-gateway/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type fixture struct {
	t      *testing.T
	ctx    context.Context
	pool   *pgxpool.Pool
	s      *Service
	a      core.Actor
	c      connectorwire.Created
	target connectorwire.Target
	server core.MCPServer
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	base, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "connector_" + strings.ReplaceAll(core.NewID(), "-", "")
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = base.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		base.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := base.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error(err)
		}
		base.Close()
	})
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err = migrations.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, ctx: ctx, pool: pool, s: New(pool), a: core.Actor{ID: core.NewID(), WorkspaceID: core.NewID(), Role: core.RoleAdmin}, target: connectorwire.Target{Name: "private-docs", Fingerprint: strings.Repeat("a", 64), Transport: "stdio"}}
	f.c, err = f.s.Create(ctx, f.a, "Fixture Connector")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Poll(ctx, f.c.Token, connectorwire.PollInput{Targets: []connectorwire.Target{f.target}}); err != nil {
		t.Fatal(err)
	}
	f.server = core.MCPServer{ID: core.NewID(), WorkspaceID: f.a.WorkspaceID, Enabled: true, MCPServerInput: core.MCPServerInput{Name: "Fixture", Namespace: "fixture", ConnectorID: f.c.Connector.ID, TargetName: f.target.Name, TimeoutMS: 5000}}
	if _, err = pool.Exec(ctx, `INSERT INTO mcp_servers(workspace_id,id,name,namespace,url,connector_id,target_name,timeout_ms) VALUES($1,$2,$3,$4,'',$5,$6,$7)`, f.a.WorkspaceID, f.server.ID, f.server.Name, f.server.Namespace, f.c.Connector.ID, f.target.Name, f.server.TimeoutMS); err != nil {
		t.Fatal(err)
	}
	return f
}
func (f *fixture) sql(query string, args ...any) {
	f.t.Helper()
	if _, err := f.pool.Exec(f.ctx, query, args...); err != nil {
		f.t.Fatal(err)
	}
}
func (f *fixture) poll(ready bool) *connectorwire.Job {
	f.t.Helper()
	job, err := f.s.Poll(f.ctx, f.c.Token, connectorwire.PollInput{Targets: []connectorwire.Target{f.target}, Ready: ready})
	if err != nil {
		f.t.Fatal(err)
	}
	return job
}
func (f *fixture) awaitJob() *connectorwire.Job {
	f.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if job := f.poll(true); job != nil {
			return job
		}
		time.Sleep(5 * time.Millisecond)
	}
	f.t.Fatal("job not enqueued")
	return nil
}
func (f *fixture) execution(risk core.Risk, configure ...func(*core.Tool)) (core.Tool, core.Operation) {
	f.t.Helper()
	tool := core.Tool{ID: core.NewID(), WorkspaceID: f.a.WorkspaceID, Name: "fixture." + core.NewID(), Risk: risk, InputSchema: json.RawMessage(`{"type":"object"}`), MCP: &core.MCPConfig{ServerID: f.server.ID, ToolName: "echo", SchemaHash: strings.Repeat("b", 64)}, Enabled: true, Status: "published", Version: 1}
	for _, apply := range configure {
		apply(&tool)
	}
	raw, _ := json.Marshal(tool)
	f.sql(`INSERT INTO tools(workspace_id,id,name,risk,status,enabled,version,definition) VALUES($1,$2,$3,$4,'published',true,1,$5)`, tool.WorkspaceID, tool.ID, tool.Name, risk, raw)
	args, hash, err := core.CanonicalArguments(json.RawMessage(`{"id":9007199254740993}`))
	if err != nil {
		f.t.Fatal(err)
	}
	op := core.Operation{ID: core.NewID(), WorkspaceID: f.a.WorkspaceID, ToolID: tool.ID, ToolName: tool.Name, ToolVersion: 1, Risk: risk, ActorID: f.a.ID, Arguments: args, ArgumentsHash: hash, IdempotencyKey: core.NewID(), State: core.StateDispatching}
	if risk == core.RiskWrite {
		op.ApprovedBy = "independent-approver"
		expires := time.Now().Add(time.Minute)
		op.ApprovalExpiresAt = &expires
	}
	f.sql(`INSERT INTO operations(workspace_id,id,tool_id,tool_name,tool_version,risk,actor_id,arguments,arguments_hash,idempotency_key,tool_snapshot,state,approved_by,approval_expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`, op.WorkspaceID, op.ID, op.ToolID, op.ToolName, op.ToolVersion, op.Risk, op.ActorID, op.Arguments, op.ArgumentsHash, op.IdempotencyKey, raw, op.State, op.ApprovedBy, op.ApprovalExpiresAt)
	return tool, op
}
func TestManagementTokensAndWorkspaceIsolation(t *testing.T) {
	f := newFixture(t)
	if len(f.c.Token) != 47 || !strings.HasPrefix(f.c.Token, "rgc_") {
		t.Fatal("invalid connector credential")
	}
	var hash string
	if err := f.pool.QueryRow(f.ctx, `SELECT token_hash FROM gateway_connectors WHERE id=$1`, f.c.Connector.ID).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if hash == f.c.Token || hash != digest(f.c.Token) {
		t.Fatal("credential not hashed")
	}
	for _, role := range []core.Role{core.RoleOperator, core.RoleApprover, core.RoleViewer} {
		a := f.a
		a.Role = role
		if _, err := f.s.Create(f.ctx, a, "denied"); !errors.Is(err, core.ErrForbidden) {
			t.Fatal(role, err)
		}
		if _, err := f.s.List(f.ctx, a); !errors.Is(err, core.ErrForbidden) {
			t.Fatal(role, err)
		}
	}
	a := f.a
	a.ClientID = a.ID
	if _, err := f.s.Create(f.ctx, a, "denied"); !errors.Is(err, core.ErrForbidden) {
		t.Fatal(err)
	}
	a = f.a
	a.WorkspaceID = "other"
	items, err := f.s.List(f.ctx, a)
	if err != nil || len(items) != 0 {
		t.Fatal(items, err)
	}
	if _, err = f.s.Revoke(f.ctx, a, f.c.Connector.ID); !errors.Is(err, core.ErrNotFound) {
		t.Fatal(err)
	}
	if err = f.s.ValidateServer(a, f.server); err == nil {
		t.Fatal("cross-workspace binding accepted")
	}
	items, err = f.s.List(f.ctx, f.a)
	if err != nil || len(items) != 1 || items[0].LastSeenAt == nil {
		t.Fatal(items, err)
	}
	raw, _ := json.Marshal(items)
	if strings.Contains(string(raw), f.c.Token) || strings.Contains(string(raw), hash) {
		t.Fatal("secret leaked to list")
	}
	if err = f.s.ValidateServer(f.a, f.server); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Revoke(f.ctx, f.a, f.c.Connector.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Poll(f.ctx, f.c.Token, connectorwire.PollInput{Targets: []connectorwire.Target{f.target}}); !errors.Is(err, core.ErrUnauthorized) {
		t.Fatal(err)
	}
	if err = f.s.ValidateServer(f.a, f.server); err == nil {
		t.Fatal("revoked connector accepted")
	}
}
func TestFrozenTargetsAndIndependentCredentials(t *testing.T) {
	f := newFixture(t)
	for _, token := range []string{"", strings.Repeat("x", 47), "rgc_" + strings.Repeat("x", 43)} {
		_, err := f.s.Poll(f.ctx, token, connectorwire.PollInput{Targets: []connectorwire.Target{f.target}})
		if !errors.Is(err, core.ErrUnauthorized) {
			t.Fatal(err)
		}
	}
	for _, modify := range []func(*connectorwire.Target){func(v *connectorwire.Target) { v.Fingerprint = strings.Repeat("b", 64) }, func(v *connectorwire.Target) { v.Transport = "http" }, func(v *connectorwire.Target) { v.Name = "other" }} {
		target := f.target
		modify(&target)
		_, err := f.s.Poll(f.ctx, f.c.Token, connectorwire.PollInput{Targets: []connectorwire.Target{target}})
		if !errors.Is(err, core.ErrConflict) {
			t.Fatal(err)
		}
	}
	for _, in := range [][]connectorwire.Target{nil, {f.target, f.target}, {{Name: "../../invalid", Fingerprint: f.target.Fingerprint, Transport: "stdio"}}, {{Name: "okay", Fingerprint: "not-a-hash", Transport: "stdio"}}} {
		if _, err := f.s.Poll(f.ctx, f.c.Token, connectorwire.PollInput{Targets: in}); !errors.Is(err, core.ErrInvalid) {
			t.Fatal(err)
		}
	}
	if f.poll(false) != nil {
		t.Fatal("heartbeat claimed work")
	}
}
func TestDiscoveryClaimsStartAndIdempotentCompletion(t *testing.T) {
	f := newFixture(t)
	job, err := f.s.enqueue(f.ctx, f.a, f.server, "")
	if err != nil {
		t.Fatal(err)
	}
	if f.poll(false) != nil {
		t.Fatal("not-ready connector claimed work")
	}
	result := connectorwire.Result{State: core.StateSucceeded, Tools: []core.RemoteTool{{Name: "echo", InputSchema: json.RawMessage(`{"type":"object"}`), SchemaHash: strings.Repeat("b", 64)}}}
	if err = f.s.Complete(f.ctx, f.c.Token, job.ID, result); !errors.Is(err, core.ErrConflict) {
		t.Fatal("result accepted before start", err)
	}
	var wg sync.WaitGroup
	claims := make(chan *connectorwire.Job, 2)
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, e := f.s.Poll(f.ctx, f.c.Token, connectorwire.PollInput{Targets: []connectorwire.Target{f.target}, Ready: true})
			claims <- v
			errs <- e
		}()
	}
	wg.Wait()
	close(claims)
	close(errs)
	count := 0
	for v := range claims {
		if v != nil {
			count++
			if v.ID != job.ID || v.Target != f.target || v.Server.ConnectorID != f.c.Connector.ID {
				t.Fatal(v)
			}
		}
	}
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if count != 1 {
		t.Fatalf("claims=%d", count)
	}
	other, err := f.s.Create(f.ctx, f.a, "Other connector")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.s.Start(f.ctx, other.Token, job.ID); !errors.Is(err, core.ErrNotFound) {
		t.Fatal("wrong connector accessed job", err)
	}
	if err = f.s.Start(f.ctx, f.c.Token, job.ID); err != nil {
		t.Fatal(err)
	}
	if err = f.s.Start(f.ctx, f.c.Token, job.ID); !errors.Is(err, core.ErrConflict) {
		t.Fatal("start replay accepted", err)
	}
	if err = f.s.Complete(f.ctx, f.c.Token, job.ID, result); err != nil {
		t.Fatal(err)
	}
	if err = f.s.Complete(f.ctx, f.c.Token, job.ID, result); err != nil {
		t.Fatal("idempotent completion failed", err)
	}
	result.Tools[0].Name = "different"
	if err = f.s.Complete(f.ctx, f.c.Token, job.ID, result); !errors.Is(err, core.ErrConflict) {
		t.Fatal("result mutation accepted", err)
	}
	got, started, err := f.s.wait(f.ctx, job)
	if err != nil || !started || len(got.Tools) != 1 || got.Tools[0].Name != "echo" {
		t.Fatal(got, started, err)
	}
	if f.poll(true) != nil {
		t.Fatal("completed work replayed")
	}
}
func TestDiscoveryPublicMethod(t *testing.T) {
	f := newFixture(t)
	ch := make(chan error, 1)
	go func() {
		tools, err := f.s.Discover(f.ctx, f.a, f.server)
		if err == nil && len(tools) != 1 {
			err = errors.New("unexpected tool catalog")
		}
		ch <- err
	}()
	job := f.awaitJob()
	if job.Kind != "discover" || job.Tool != nil || job.Operation != nil {
		t.Fatal(job)
	}
	if err := f.s.Start(f.ctx, f.c.Token, job.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Complete(f.ctx, f.c.Token, job.ID, connectorwire.Result{Tools: []core.RemoteTool{{Name: "echo"}}}); err != nil {
		t.Fatal(err)
	}
	if err := <-ch; err != nil {
		t.Fatal(err)
	}
}
func TestWriteTimeoutAndRevocationNeverReplay(t *testing.T) {
	for _, stage := range []string{"queued", "claimed", "started", "revoked"} {
		t.Run(stage, func(t *testing.T) {
			f := newFixture(t)
			tool, op := f.execution(core.RiskWrite)
			ctx, cancel := context.WithCancel(f.ctx)
			defer cancel()
			finished := make(chan core.FinishInput, 1)
			go func() { finished <- f.s.Execute(ctx, f.a, tool, op) }()
			var job *connectorwire.Job
			if stage == "queued" {
				deadline := time.Now().Add(3 * time.Second)
				for time.Now().Before(deadline) {
					var count int
					_ = f.pool.QueryRow(f.ctx, `SELECT count(*) FROM gateway_connector_jobs`).Scan(&count)
					if count == 1 {
						break
					}
					time.Sleep(5 * time.Millisecond)
				}
			} else {
				job = f.awaitJob()
			}
			if stage == "started" || stage == "revoked" {
				if err := f.s.Start(f.ctx, f.c.Token, job.ID); err != nil {
					t.Fatal(err)
				}
			}
			if stage == "revoked" {
				if _, err := f.s.Revoke(f.ctx, f.a, f.c.Connector.ID); err != nil {
					t.Fatal(err)
				}
			}
			cancel()
			result := <-finished
			want := core.StateFailed
			if stage == "started" || stage == "revoked" {
				want = core.StateUnknown
			}
			if result.State != want {
				t.Fatal(result, want)
			}
			var state string
			var count int
			if err := f.pool.QueryRow(f.ctx, `SELECT state FROM gateway_connector_jobs WHERE operation_id=$1`, op.ID).Scan(&state); err != nil {
				t.Fatal(err)
			}
			if state != "expired" {
				t.Fatal(state)
			}
			restarted := New(f.pool)
			_ = restarted.Execute(f.ctx, f.a, tool, op)
			if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM gateway_connector_jobs WHERE operation_id=$1`, op.ID).Scan(&count); err != nil || count != 1 {
				t.Fatal(count, err)
			}
			if stage != "revoked" && f.poll(true) != nil {
				t.Fatal("abandoned job requeued")
			}
		})
	}
}
func TestApprovedWriteSuccessPreservesSnapshotAndNumbers(t *testing.T) {
	f := newFixture(t)
	tool, op := f.execution(core.RiskWrite)
	finished := make(chan core.FinishInput, 1)
	go func() { finished <- f.s.Execute(f.ctx, f.a, tool, op) }()
	job := f.awaitJob()
	if job.Kind != "execute" || job.Operation == nil || job.Tool == nil || job.Operation.ArgumentsHash != op.ArgumentsHash || job.Operation.ApprovedBy != op.ApprovedBy || !sameJSON(job.Operation.Arguments, op.Arguments) {
		t.Fatal(job)
	}
	if err := f.s.Start(f.ctx, f.c.Token, job.ID); err != nil {
		t.Fatal(err)
	}
	result := connectorwire.Result{State: core.StateSucceeded, Result: json.RawMessage(`{"content":[],"structuredContent":{"id":9007199254740993,"success":true}}`)}
	if err := f.s.Complete(f.ctx, f.c.Token, job.ID, result); err != nil {
		t.Fatal(err)
	}
	got := <-finished
	decoded, err := core.DecodeResult(got.Result)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != core.StateSucceeded || !got.MCPResultValidated || decoded.(map[string]any)["structuredContent"].(map[string]any)["id"] != json.Number("9007199254740993") {
		t.Fatal(got)
	}
	if err := f.s.Complete(f.ctx, f.c.Token, job.ID, result); err != nil {
		t.Fatal(err)
	}
	duplicate, err := f.s.enqueue(f.ctx, f.a, f.server, op.ID)
	if err != nil || duplicate.ID != job.ID {
		t.Fatal(duplicate, err)
	}
	if f.poll(true) != nil {
		t.Fatal("operation replayed")
	}
}
func TestDispatchRechecksToolServerApprovalAndArguments(t *testing.T) {
	for _, scenario := range []string{"tool-disabled", "server-disabled", "approval-expired", "arguments-changed", "terminal-operation", "connector-revoked"} {
		t.Run(scenario, func(t *testing.T) {
			f := newFixture(t)
			_, op := f.execution(core.RiskWrite)
			job, err := f.s.enqueue(f.ctx, f.a, f.server, op.ID)
			if err != nil {
				t.Fatal(err)
			}
			if f.poll(true) == nil {
				t.Fatal("missing claim")
			}
			switch scenario {
			case "tool-disabled":
				f.sql(`UPDATE tools SET enabled=false WHERE id=$1`, op.ToolID)
			case "server-disabled":
				f.sql(`UPDATE mcp_servers SET enabled=false WHERE id=$1`, f.server.ID)
			case "approval-expired":
				f.sql(`UPDATE operations SET approval_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, op.ID)
			case "arguments-changed":
				f.sql(`UPDATE operations SET arguments='{"id":2}'::jsonb WHERE id=$1`, op.ID)
			case "terminal-operation":
				f.sql(`UPDATE operations SET state='UNKNOWN' WHERE id=$1`, op.ID)
			case "connector-revoked":
				if _, err = f.s.Revoke(f.ctx, f.a, f.c.Connector.ID); err != nil {
					t.Fatal(err)
				}
			}
			if err = f.s.Start(f.ctx, f.c.Token, job.ID); err == nil {
				t.Fatal("revoked dispatch authorized")
			}
		})
	}
}
func TestClientRevocationAndKeyRotationFenceStart(t *testing.T) {
	for _, scenario := range []string{"disabled", "grant-removed", "scope-removed", "key-rotated"} {
		t.Run(scenario, func(t *testing.T) {
			f := newFixture(t)
			f.a = core.Actor{ID: "client_" + core.NewID(), ClientID: "", WorkspaceID: f.a.WorkspaceID, Role: core.RoleOperator, ClientKeyID: "key-original"}
			f.a.ClientID = f.a.ID
			_, op := f.execution(core.RiskRead)
			hash := sha256.Sum256([]byte(core.NewID()))
			f.sql(`INSERT INTO gateway_clients(id,workspace_id,name,scopes,key_id,token_hash,key_expires_at) VALUES($1,$2,'Fixture',ARRAY['tools:read','tools:invoke'],$3,$4,clock_timestamp()+interval '1 hour')`, f.a.ID, f.a.WorkspaceID, f.a.ClientKeyID, hash[:])
			f.sql(`INSERT INTO gateway_client_tool_grants(workspace_id,client_id,tool_id) VALUES($1,$2,$3)`, f.a.WorkspaceID, f.a.ID, op.ToolID)
			job, err := f.s.enqueue(f.ctx, f.a, f.server, op.ID)
			if err != nil {
				t.Fatal(err)
			}
			f.poll(true)
			switch scenario {
			case "disabled":
				f.sql(`UPDATE gateway_clients SET enabled=false WHERE id=$1`, f.a.ID)
			case "grant-removed":
				f.sql(`DELETE FROM gateway_client_tool_grants WHERE client_id=$1`, f.a.ID)
			case "scope-removed":
				f.sql(`UPDATE gateway_clients SET scopes=ARRAY['tools:read'] WHERE id=$1`, f.a.ID)
			case "key-rotated":
				f.sql(`UPDATE gateway_clients SET key_id='new-key' WHERE id=$1`, f.a.ID)
			}
			if err = f.s.Start(f.ctx, f.c.Token, job.ID); err == nil {
				t.Fatal("revoked client started")
			}
		})
	}
}
func TestDeadlineAndResultsAfterRevocation(t *testing.T) {
	f := newFixture(t)
	job, err := f.s.enqueue(f.ctx, f.a, f.server, "")
	if err != nil {
		t.Fatal(err)
	}
	f.poll(true)
	f.sql(`UPDATE gateway_connector_jobs SET deadline=clock_timestamp()-interval '1 second' WHERE id=$1`, job.ID)
	if err = f.s.Start(f.ctx, f.c.Token, job.ID); !errors.Is(err, core.ErrConflict) {
		t.Fatal(err)
	}
	if _, err = f.s.Cleanup(f.ctx); err != nil {
		t.Fatal(err)
	}
	job, err = f.s.enqueue(f.ctx, f.a, f.server, "")
	if err != nil {
		t.Fatal(err)
	}
	f.poll(true)
	if err = f.s.Start(f.ctx, f.c.Token, job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Revoke(f.ctx, f.a, f.c.Connector.ID); err != nil {
		t.Fatal(err)
	}
	if err = f.s.Complete(f.ctx, f.c.Token, job.ID, connectorwire.Result{State: core.StateSucceeded}); !errors.Is(err, core.ErrUnauthorized) {
		t.Fatal(err)
	}
}
func TestRetentionKeepsExecutionReplayFence(t *testing.T) {
	f := newFixture(t)
	_, op := f.execution(core.RiskWrite)
	execution, err := f.s.enqueue(f.ctx, f.a, f.server, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	discovery, err := f.s.enqueue(f.ctx, f.a, f.server, "")
	if err != nil {
		t.Fatal(err)
	}
	f.sql(`UPDATE gateway_connector_jobs SET state='expired',updated_at=clock_timestamp()-interval '2 days'`)
	if _, err = f.s.Cleanup(f.ctx); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	var operationID string
	if err = f.pool.QueryRow(f.ctx, `SELECT payload,operation_id FROM gateway_connector_jobs WHERE id=$1`, execution.ID).Scan(&raw, &operationID); err != nil || raw != nil || operationID != op.ID {
		t.Fatal(string(raw), operationID, err)
	}
	var count int
	if err = f.pool.QueryRow(f.ctx, `SELECT count(*) FROM gateway_connector_jobs WHERE id=$1`, discovery.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
	if _, err = f.s.enqueue(f.ctx, f.a, f.server, op.ID); !errors.Is(err, core.ErrConflict) {
		t.Fatal("erased execution replayed", err)
	}
}
func TestResultBoundsAndStrictJSON(t *testing.T) {
	for _, raw := range []string{`{"a":1,"a":2}`, `{"nested":{"a":1,"a":2}}`, `{} {}`, strings.Repeat("[", 66) + "0" + strings.Repeat("]", 66)} {
		if _, err := DecodeJSON([]byte(raw)); err == nil {
			t.Fatal("unsupported JSON accepted")
		}
	}
	tools := []core.RemoteTool{}
	for range 400 {
		tools = append(tools, core.RemoteTool{Name: "echo", Description: strings.Repeat("x", 3500)})
	}
	if err := validResult(connectorwire.Result{State: core.StateSucceeded, Tools: tools}, "discover"); err != nil {
		t.Fatal("catalog over1MiB should fit4MiB", err)
	}
	if err := validResult(connectorwire.Result{State: core.StateSucceeded, Result: json.RawMessage(`{"body":"` + strings.Repeat("x", 1<<20) + `"}`)}, "execute"); !errors.Is(err, core.ErrInvalid) {
		t.Fatal(err)
	}
	if err := validResult(connectorwire.Result{State: core.StateReady}, "execute"); !errors.Is(err, core.ErrInvalid) {
		t.Fatal(err)
	}
}

func TestSuccessfulEmptyCatalog(t *testing.T) {
	f := newFixture(t)
	job, err := f.s.enqueue(f.ctx, f.a, f.server, "")
	if err != nil {
		t.Fatal(err)
	}
	f.poll(true)
	if err = f.s.Start(f.ctx, f.c.Token, job.ID); err != nil {
		t.Fatal(err)
	}
	if err = f.s.Complete(f.ctx, f.c.Token, job.ID, connectorwire.Result{State: core.StateSucceeded, Tools: []core.RemoteTool{}}); err != nil {
		t.Fatal(err)
	}
	r, _, err := f.s.wait(f.ctx, job)
	if err != nil || r.Tools == nil || len(r.Tools) != 0 {
		t.Fatal(r, err)
	}
}

func TestResultsAreValidatedAndProjectedBeforePersistence(t *testing.T) {
	for _, scenario := range []string{"projected", "invalid-schema-read", "invalid-schema-write", "projection-failure", "failed-raw-data"} {
		t.Run(scenario, func(t *testing.T) {
			f := newFixture(t)
			risk := core.RiskRead
			if scenario == "invalid-schema-write" {
				risk = core.RiskWrite
			}
			_, op := f.execution(risk, func(tool *core.Tool) {
				tool.OutputSchema = json.RawMessage(`{"type":"object","required":["id","internal"],"properties":{"id":{"type":"integer"},"internal":{"type":"string"}}}`)
				tool.ResponsePolicy = &core.ResponsePolicy{Include: []string{"/id"}, MaxBytes: 1024}
				if scenario == "projection-failure" {
					tool.ResponsePolicy.Include = []string{"/missing"}
				}
			})
			job, err := f.s.enqueue(f.ctx, f.a, f.server, op.ID)
			if err != nil {
				t.Fatal(err)
			}
			f.poll(true)
			if err = f.s.Start(f.ctx, f.c.Token, job.ID); err != nil {
				t.Fatal(err)
			}
			result := connectorwire.Result{State: core.StateSucceeded, Result: json.RawMessage(`{"content":[{"type":"text","text":"private-only-sentinel"}],"structuredContent":{"id":9007199254740993,"internal":"private-only-sentinel","nextCursor":"page-2"}}`)}
			if strings.HasPrefix(scenario, "invalid-schema") {
				result.Result = json.RawMessage(`{"content":[],"structuredContent":{"id":"wrong-type","internal":"private-only-sentinel"}}`)
			}
			if scenario == "failed-raw-data" {
				result.State = core.StateFailed
				result.Error = "private-only-sentinel"
			}
			if err = f.s.Complete(f.ctx, f.c.Token, job.ID, result); err != nil {
				t.Fatal(err)
			}
			if err = f.s.Complete(f.ctx, f.c.Token, job.ID, result); err != nil {
				t.Fatal("retry of raw completion failed", err)
			}
			var persisted string
			if err = f.pool.QueryRow(f.ctx, `SELECT result::text FROM gateway_connector_jobs WHERE id=$1`, job.ID).Scan(&persisted); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(persisted, "private-only-sentinel") {
				t.Fatal("unprojected result or error persisted")
			}
			got, _, err := f.s.wait(f.ctx, job)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "projected" {
				if got.State != core.StateSucceeded || !strings.Contains(string(got.Result), `"id": 9007199254740993`) && !strings.Contains(string(got.Result), `"id":9007199254740993`) || !strings.Contains(string(got.Result), "page-2") {
					t.Fatal(got)
				}
			} else {
				want := core.StateFailed
				if risk == core.RiskWrite {
					want = core.StateUnknown
				}
				if got.State != want || len(got.Result) != 0 {
					t.Fatal(got)
				}
			}
		})
	}
}

func TestQueueCapacityAndExpiredWork(t *testing.T) {
	f := newFixture(t)
	job, err := f.s.enqueue(f.ctx, f.a, f.server, "")
	if err != nil {
		t.Fatal(err)
	}
	f.sql(`INSERT INTO gateway_connector_jobs(workspace_id,id,connector_id,server_id,kind,target_name,target_fingerprint,payload,actor,deadline) SELECT workspace_id,id||'-'||n,connector_id,server_id,kind,target_name,target_fingerprint,payload,actor,deadline FROM gateway_connector_jobs CROSS JOIN generate_series(1,99) n WHERE id=$1`, job.ID)
	if _, err = f.s.enqueue(f.ctx, f.a, f.server, ""); !errors.Is(err, core.ErrConflict) {
		t.Fatal("active job limit ignored", err)
	}
	f.sql(`UPDATE gateway_connector_jobs SET deadline=clock_timestamp()-interval '1 second'`)
	if f.poll(true) != nil {
		t.Fatal("expired work claimed")
	}
	if _, err = f.s.enqueue(f.ctx, f.a, f.server, ""); err != nil {
		t.Fatal("expired rows prevented new work", err)
	}
	if n, err := f.s.Cleanup(f.ctx); err != nil || n != 100 {
		t.Fatal(n, err)
	}
}

func TestStartRechecksDeadlineAfterBlockedAuthorization(t *testing.T) {
	f := newFixture(t)
	job, err := f.s.enqueue(f.ctx, f.a, f.server, "")
	if err != nil {
		t.Fatal(err)
	}
	f.poll(true)
	f.sql(`UPDATE gateway_connector_jobs SET deadline=clock_timestamp()+interval '150 milliseconds' WHERE id=$1`, job.ID)
	blocker, err := f.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(context.Background())
	if _, err = blocker.Exec(f.ctx, `SELECT id FROM mcp_servers WHERE id=$1 FOR UPDATE`, f.server.ID); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() { finished <- f.s.Start(f.ctx, f.c.Token, job.ID) }()
	time.Sleep(250 * time.Millisecond)
	if err = blocker.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-finished; !errors.Is(err, core.ErrConflict) {
		t.Fatal("expired authorization succeeded", err)
	}
}
