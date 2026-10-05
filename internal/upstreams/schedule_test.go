package upstreams

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/alan1-666/mcp-gateway/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func scheduleFixture(t *testing.T) (context.Context, *pgxpool.Pool, *Service, *fakeRemote, core.Actor) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required")
	}
	ctx := context.Background()
	base, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "schedule_" + strings.ReplaceAll(core.NewID(), "-", "")
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = base.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, err := base.Exec(ctx, "DROP SCHEMA "+quoted+" CASCADE")
		if err != nil {
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
	remote := &fakeRemote{items: []core.RemoteTool{{Name: "lookup", SchemaHash: strings.Repeat("a", 64)}}}
	return ctx, pool, New(NewStore(pool), remote), remote, core.Actor{ID: core.NewID(), WorkspaceID: core.NewID(), Role: core.RoleAdmin}
}
func configSchedule(enabled bool, interval, rev int) ScheduleInput {
	return ScheduleInput{Enabled: &enabled, IntervalSeconds: interval, ExpectedRevision: &rev}
}
func due(t *testing.T, pool *pgxpool.Pool, a core.Actor, id string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `UPDATE mcp_catalog_schedules SET next_check_at=clock_timestamp()-interval '1 second' WHERE workspace_id=$1 AND server_id=$2`, a.WorkspaceID, id); err != nil {
		t.Fatal(err)
	}
}

func TestScheduleConfigurationAndIsolation(t *testing.T) {
	ctx, pool, s, _, a := scheduleFixture(t)
	v := server(t, ctx, s, a)
	initial, err := s.CatalogSchedule(ctx, a, v.ID)
	if err != nil || initial.Enabled || initial.Revision != 0 || initial.IntervalSeconds != 3600 {
		t.Fatalf("default %+v %v", initial, err)
	}
	for _, in := range []ScheduleInput{{}, configSchedule(true, 299, 0), configSchedule(true, 86401, 0), configSchedule(true, 300, -1)} {
		if _, err := s.SetCatalogSchedule(ctx, a, v.ID, in); !errors.Is(err, core.ErrInvalid) {
			t.Fatal("invalid schedule accepted", err)
		}
	}
	for _, role := range []core.Role{core.RoleViewer, core.RoleOperator, core.RoleApprover} {
		actor := a
		actor.Role = role
		if _, err := s.CatalogSchedule(ctx, actor, v.ID); !errors.Is(err, core.ErrForbidden) {
			t.Fatal("unauthorized read", err)
		}
		if _, err := s.SetCatalogSchedule(ctx, actor, v.ID, configSchedule(true, 300, 0)); !errors.Is(err, core.ErrForbidden) {
			t.Fatal("unauthorized edit", err)
		}
	}
	machine := a
	machine.ClientID = "client"
	if _, err := s.SetCatalogSchedule(ctx, machine, v.ID, configSchedule(true, 300, 0)); !errors.Is(err, core.ErrForbidden) {
		t.Fatal("machine edit", err)
	}
	foreign := a
	foreign.WorkspaceID = core.NewID()
	if _, err := s.CatalogSchedule(ctx, foreign, v.ID); !errors.Is(err, core.ErrNotFound) {
		t.Fatal("cross tenant read", err)
	}
	if _, err := s.SetCatalogSchedule(ctx, foreign, v.ID, configSchedule(true, 300, 0)); !errors.Is(err, core.ErrNotFound) {
		t.Fatal("cross tenant edit", err)
	}
	enabled, err := s.SetCatalogSchedule(ctx, a, v.ID, configSchedule(true, 300, 0))
	if err != nil || enabled.Revision != 1 || enabled.NextCheckAt == nil {
		t.Fatal(enabled, err)
	}
	same, err := s.SetCatalogSchedule(ctx, a, v.ID, configSchedule(true, 300, 1))
	if err != nil || same.Revision != 1 || !same.NextCheckAt.Equal(*enabled.NextCheckAt) {
		t.Fatal("no-op rescheduled job", same, err)
	}
	if _, err := s.SetCatalogSchedule(ctx, a, v.ID, configSchedule(false, 300, 0)); !errors.Is(err, core.ErrConflict) {
		t.Fatal("stale edit", err)
	}
	var events int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE action='MCP_CATALOG_SCHEDULE_CHANGED'`).Scan(&events); err != nil || events != 1 {
		t.Fatal("audit", events, err)
	}
}

func TestCatalogClaimsFencedAcrossWorkersEditsAndServerChanges(t *testing.T) {
	ctx, pool, s, remote, a := scheduleFixture(t)
	v := server(t, ctx, s, a)
	if _, err := s.SetCatalogSchedule(ctx, a, v.ID, configSchedule(true, 300, 0)); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	claims := make(chan catalogClaim, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := s.store.claimCatalog(ctx)
			if err == nil {
				claims <- c
			} else if !errors.Is(err, core.ErrNotFound) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	close(claims)
	var first catalogClaim
	count := 0
	for c := range claims {
		first = c
		count++
	}
	if count != 1 {
		t.Fatalf("concurrent claim count %d", count)
	}
	if first.actor().Role != "" || first.actor().WorkspaceID != a.WorkspaceID {
		t.Fatal("scheduler impersonates administrator")
	}
	// Crash recovery takes over an expired lease. A resumed old process is fenced.
	if _, err := pool.Exec(ctx, `UPDATE mcp_catalog_schedules SET lease_until=clock_timestamp()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	second, err := s.store.claimCatalog(ctx)
	if err != nil || second.LeaseID == first.LeaseID {
		t.Fatal("reclaim", err)
	}
	if err := s.store.finishCatalog(ctx, first, remote.items, ""); !errors.Is(err, core.ErrConflict) {
		t.Fatal("old worker completion", err)
	}
	if _, err := s.SetCatalogSchedule(ctx, a, v.ID, configSchedule(false, 300, 1)); err != nil {
		t.Fatal(err)
	}
	if err := s.store.finishCatalog(ctx, second, remote.items, ""); !errors.Is(err, core.ErrConflict) {
		t.Fatal("paused schedule completion", err)
	}
	if worked, err := s.CheckNextCatalog(ctx); err != nil || worked {
		t.Fatal("disabled schedule dispatched", err)
	}
	if _, err := s.SetCatalogSchedule(ctx, a, v.ID, configSchedule(true, 300, 2)); err != nil {
		t.Fatal(err)
	}
	third, err := s.store.claimCatalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetEnabled(ctx, a, v.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetEnabled(ctx, a, v.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := s.store.finishCatalog(ctx, third, remote.items, ""); err != nil {
		t.Fatal(err)
	}
	state, err := s.CatalogSchedule(ctx, a, v.ID)
	if err != nil || state.LastErrorCode != "server_changed" || state.LastSuccessAt != nil {
		t.Fatal("stale server review", state, err)
	}
	history, err := s.CatalogReviews(ctx, a, v.ID, 50, 0)
	if err != nil || len(history.Items) != 0 {
		t.Fatal("stale history saved", err)
	}
	due(t, pool, a, v.ID)
	if _, err := s.SetEnabled(ctx, a, v.ID, false); err != nil {
		t.Fatal(err)
	}
	if worked, err := s.CheckNextCatalog(ctx); err != nil || worked {
		t.Fatal("disabled server dispatched", err)
	}
}

func TestScheduledCatalogFailuresBackoffAndHistory(t *testing.T) {
	ctx, pool, s, remote, a := scheduleFixture(t)
	v := server(t, ctx, s, a)
	if _, err := s.SetCatalogSchedule(ctx, a, v.ID, configSchedule(true, 300, 0)); err != nil {
		t.Fatal(err)
	}
	if worked, err := s.CheckNextCatalog(ctx); err != nil || !worked {
		t.Fatal("scheduled success", err)
	}
	history, err := s.CatalogReviews(ctx, a, v.ID, 50, 0)
	if err != nil || len(history.Items) != 1 || history.Items[0].Source != "scheduled" || history.Items[0].Counts.Unimported != 1 {
		t.Fatal("success history", history, err)
	}
	first := history.Items[0].ID
	state, err := s.CatalogSchedule(ctx, a, v.ID)
	if err != nil || state.LastSuccessAt == nil || state.LeaseUntil != nil || state.Running || state.ConsecutiveFailures != 0 {
		t.Fatal("success state", state, err)
	}
	if worked, err := s.CheckNextCatalog(ctx); err != nil || worked {
		t.Fatal("cadence ignored", err)
	}
	remote.err = errors.New("sensitive upstream error MUST NOT be persisted")
	for i := 1; i <= 3; i++ {
		due(t, pool, a, v.ID)
		if worked, err := s.CheckNextCatalog(ctx); err != nil || !worked {
			t.Fatal("failure outcome not recorded", err)
		}
		state, err = s.CatalogSchedule(ctx, a, v.ID)
		if err != nil || state.LastErrorCode != "discovery_failed" || state.ConsecutiveFailures != i || state.LastSuccessAt == nil {
			t.Fatal("failure state", state, err)
		}
		delay := state.NextCheckAt.Sub(*state.LastFinishedAt)
		if delay != time.Duration(300<<(i-1))*time.Second {
			t.Fatal("backoff", delay)
		}
	}
	history, err = s.CatalogReviews(ctx, a, v.ID, 50, 0)
	if err != nil || len(history.Items) != 1 || history.Items[0].ID != first {
		t.Fatal("failure replaced history", err)
	}
	var raw string
	if err := pool.QueryRow(ctx, `SELECT string_agg(data::text,'') FROM audit_events`).Scan(&raw); err != nil || strings.Contains(raw, "sensitive") {
		t.Fatal("unsafe error audit", err)
	}
	remote.err = nil
	remote.items = nil
	due(t, pool, a, v.ID)
	if _, err := s.CheckNextCatalog(ctx); err != nil {
		t.Fatal(err)
	}
	state, err = s.CatalogSchedule(ctx, a, v.ID)
	if err != nil || state.ConsecutiveFailures != 0 || state.LastErrorCode != "" {
		t.Fatal("recovery didn't reset backoff", state, err)
	}
	history, err = s.CatalogReviews(ctx, a, v.ID, 50, 0)
	if err != nil || len(history.Items) != 2 || len(history.Items[0].Items) != 0 {
		t.Fatal("complete empty catalog", err)
	}
	remote.items = []core.RemoteTool{{Name: "duplicate"}, {Name: "duplicate"}}
	due(t, pool, a, v.ID)
	if _, err := s.CheckNextCatalog(ctx); err != nil {
		t.Fatal("comparison failure outcome", err)
	}
	state, err = s.CatalogSchedule(ctx, a, v.ID)
	if err != nil || state.LastErrorCode != "comparison_failed" {
		t.Fatal("comparison status", state, err)
	}
	for _, n := range []int{20, 1000} {
		if retrySeconds(300, n) != 86400 {
			t.Fatal("unbounded backoff")
		}
	}
	// A worker process with no pending checks stops without waiting for polling.
	stopCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { s.RunCatalogScheduler(stopCtx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("scheduler ignored shutdown")
	}
}
