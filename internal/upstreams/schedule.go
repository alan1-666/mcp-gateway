package upstreams

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/jackc/pgx/v5"
)

const catalogLeaseSeconds = 180

// Schedule is configuration plus the most recent background check outcome.
// A disabled server pauses an enabled schedule without discarding its settings.
type Schedule struct {
	ServerID            string     `json:"server_id"`
	Enabled             bool       `json:"enabled"`
	IntervalSeconds     int        `json:"interval_seconds"`
	Revision            int        `json:"revision"`
	NextCheckAt         *time.Time `json:"next_check_at"`
	LeaseUntil          *time.Time `json:"lease_until"`
	Running             bool       `json:"running"`
	LastStartedAt       *time.Time `json:"last_started_at"`
	LastFinishedAt      *time.Time `json:"last_finished_at"`
	LastSuccessAt       *time.Time `json:"last_success_at"`
	ConsecutiveFailures int        `json:"consecutive_failures"`
	LastErrorCode       string     `json:"last_error_code"`
}
type ScheduleInput struct {
	Enabled          *bool `json:"enabled"`
	IntervalSeconds  int   `json:"interval_seconds"`
	ExpectedRevision *int  `json:"expected_revision"`
}

const scheduleColumns = `server_id,enabled,interval_seconds,revision,next_check_at,lease_until,COALESCE(lease_until>clock_timestamp(),false),last_started_at,last_finished_at,last_success_at,consecutive_failures,last_error_code`

func scanSchedule(row interface{ Scan(...any) error }) (Schedule, error) {
	var v Schedule
	err := row.Scan(&v.ServerID, &v.Enabled, &v.IntervalSeconds, &v.Revision, &v.NextCheckAt, &v.LeaseUntil, &v.Running, &v.LastStartedAt, &v.LastFinishedAt, &v.LastSuccessAt, &v.ConsecutiveFailures, &v.LastErrorCode)
	return v, mapError(err)
}
func (s *Service) CatalogSchedule(ctx context.Context, a core.Actor, id string) (Schedule, error) {
	if err := admin(a); err != nil {
		return Schedule{}, err
	}
	if _, err := s.store.GetServer(ctx, a.WorkspaceID, id); err != nil {
		return Schedule{}, err
	}
	v, err := scanSchedule(s.store.db.QueryRow(ctx, `SELECT `+scheduleColumns+` FROM mcp_catalog_schedules WHERE workspace_id=$1 AND server_id=$2`, a.WorkspaceID, id))
	if errors.Is(err, core.ErrNotFound) {
		return Schedule{ServerID: id, IntervalSeconds: 3600}, nil
	}
	return v, err
}
func (s *Service) SetCatalogSchedule(ctx context.Context, a core.Actor, id string, in ScheduleInput) (Schedule, error) {
	if err := admin(a); err != nil {
		return Schedule{}, err
	}
	if in.Enabled == nil || in.ExpectedRevision == nil || *in.ExpectedRevision < 0 || in.IntervalSeconds < 300 || in.IntervalSeconds > 86400 {
		return Schedule{}, fmt.Errorf("%w: enabled, expected_revision and interval_seconds (300–86400) are required", core.ErrInvalid)
	}
	return withTx(ctx, s.store.db, func(tx pgx.Tx) (Schedule, error) {
		// All operations that lock both rows use server -> schedule order.
		if _, err := scan(tx.QueryRow(ctx, `SELECT `+columns+` FROM mcp_servers WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, a.WorkspaceID, id)); err != nil {
			return Schedule{}, err
		}
		current, err := scanSchedule(tx.QueryRow(ctx, `SELECT `+scheduleColumns+` FROM mcp_catalog_schedules WHERE workspace_id=$1 AND server_id=$2 FOR UPDATE`, a.WorkspaceID, id))
		if err != nil && !errors.Is(err, core.ErrNotFound) {
			return Schedule{}, err
		}
		if current.Revision != *in.ExpectedRevision {
			return Schedule{}, fmt.Errorf("%w: schedule changed; reload before saving", core.ErrConflict)
		}
		if current.Revision > 0 && current.Enabled == *in.Enabled && current.IntervalSeconds == in.IntervalSeconds {
			return current, nil
		}
		v, err := scanSchedule(tx.QueryRow(ctx, `INSERT INTO mcp_catalog_schedules(workspace_id,server_id,enabled,interval_seconds,next_check_at)
   VALUES($1,$2,$3,$4,CASE WHEN $3 THEN clock_timestamp() END)
   ON CONFLICT(workspace_id,server_id) DO UPDATE SET enabled=$3,interval_seconds=$4,revision=mcp_catalog_schedules.revision+1,
   next_check_at=CASE WHEN $3 THEN clock_timestamp() END,lease_id='',lease_until=NULL,updated_at=clock_timestamp()
   RETURNING `+scheduleColumns, a.WorkspaceID, id, *in.Enabled, in.IntervalSeconds))
		if err != nil {
			return v, err
		}
		return v, audit(ctx, tx, a, "MCP_CATALOG_SCHEDULE_CHANGED", id, map[string]any{"enabled": v.Enabled, "interval_seconds": v.IntervalSeconds, "revision": v.Revision})
	})
}

type catalogClaim struct {
	WorkspaceID string
	Server      core.MCPServer
	LeaseID     string
	StartedAt   time.Time
}

func (s *Store) claimCatalog(ctx context.Context) (catalogClaim, error) {
	return withTx(ctx, s.db, func(tx pgx.Tx) (catalogClaim, error) {
		c := catalogClaim{LeaseID: core.NewID()}
		var id string
		var recovered bool
		// Row claims last only for this transaction; network I/O is outside it.
		err := tx.QueryRow(ctx, `SELECT q.workspace_id,q.server_id,q.lease_id<>'' FROM mcp_catalog_schedules q
   JOIN mcp_servers s ON s.workspace_id=q.workspace_id AND s.id=q.server_id
   WHERE q.enabled AND s.enabled AND q.next_check_at<=clock_timestamp() AND (q.lease_until IS NULL OR q.lease_until<=clock_timestamp())
   ORDER BY q.next_check_at,q.workspace_id,q.server_id FOR UPDATE OF q SKIP LOCKED LIMIT 1`).Scan(&c.WorkspaceID, &id, &recovered)
		if err != nil {
			return c, err
		}
		c.Server, err = scan(tx.QueryRow(ctx, `SELECT `+columns+` FROM mcp_servers WHERE workspace_id=$1 AND id=$2`, c.WorkspaceID, id))
		if err != nil {
			return c, err
		}
		err = tx.QueryRow(ctx, `UPDATE mcp_catalog_schedules SET lease_id=$3,lease_until=clock_timestamp()+make_interval(secs=>$4),last_started_at=clock_timestamp()
   WHERE workspace_id=$1 AND server_id=$2 RETURNING last_started_at`, c.WorkspaceID, id, c.LeaseID, catalogLeaseSeconds).Scan(&c.StartedAt)
		if err != nil {
			return c, err
		}
		return c, audit(ctx, tx, c.actor(), "MCP_CATALOG_CHECK_STARTED", id, map[string]any{"recovered_lease": recovered})
	})
}

// This internal actor scopes credentials/audit to the claimed workspace. It has
// no human role, session or machine-client token and cannot use management APIs.
func (c catalogClaim) actor() core.Actor {
	return core.Actor{ID: "system:catalog-scheduler", WorkspaceID: c.WorkspaceID}
}

func retrySeconds(interval, failures int) int {
	// Exponential delay, never faster than the configured cadence; at most 24 h.
	delay := interval
	for i := 1; i < failures && delay < 86400; i++ {
		delay *= 2
	}
	if delay > 86400 {
		delay = 86400
	}
	return delay
}
func (s *Store) finishCatalog(ctx context.Context, c catalogClaim, items []core.RemoteTool, code string) error {
	_, err := withTx(ctx, s.db, func(tx pgx.Tx) (struct{}, error) {
		var zero struct{}
		server, err := scan(tx.QueryRow(ctx, `SELECT `+columns+` FROM mcp_servers WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, c.WorkspaceID, c.Server.ID))
		if err != nil {
			return zero, err
		}
		var interval, failures int
		// Both lease expiry and lease identity fence late completions, including
		// schedule edits, pause/resume and another worker reclaiming a crashed run.
		err = tx.QueryRow(ctx, `SELECT interval_seconds,consecutive_failures FROM mcp_catalog_schedules
   WHERE workspace_id=$1 AND server_id=$2 AND enabled AND lease_id=$3 AND lease_until>clock_timestamp() FOR UPDATE`, c.WorkspaceID, c.Server.ID, c.LeaseID).Scan(&interval, &failures)
		if errors.Is(err, pgx.ErrNoRows) {
			return zero, core.ErrConflict
		}
		if err != nil {
			return zero, err
		}
		if !server.Enabled || !server.UpdatedAt.Equal(c.Server.UpdatedAt) {
			code = "server_changed"
		}
		if code == "" {
			if _, err = reviewCatalogTx(ctx, tx, c.actor(), c.Server, c.StartedAt, items, "scheduled"); err != nil {
				return zero, err
			}
			failures = 0
		} else {
			failures++
			if failures > 1000 {
				failures = 1000
			}
			if err = audit(ctx, tx, c.actor(), "MCP_CATALOG_CHECK_FAILED", c.Server.ID, map[string]any{"error_code": code, "consecutive_failures": failures}); err != nil {
				return zero, err
			}
		}
		// One statement timestamp binds completion and next due time exactly.
		// Independent wall-clock reads can be evaluated in either column order.
		_, err = tx.Exec(ctx, `UPDATE mcp_catalog_schedules SET lease_id='',lease_until=NULL,last_finished_at=statement_timestamp(),
   last_success_at=CASE WHEN $4='' THEN statement_timestamp() ELSE last_success_at END,last_error_code=$4,consecutive_failures=$5,
   next_check_at=statement_timestamp()+make_interval(secs=>$6) WHERE workspace_id=$1 AND server_id=$2 AND lease_id=$3`, c.WorkspaceID, c.Server.ID, c.LeaseID, code, failures, retrySeconds(interval, failures))
		return zero, err
	})
	return err
}

// CheckNextCatalog claims and checks at most one due server. No business tool
// calls or registry mutations occur here. A stopped process leaves a bounded
// lease; another worker can safely repeat this read-only discovery.
func (s *Service) CheckNextCatalog(ctx context.Context) (bool, error) {
	claimCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	c, err := s.store.claimCatalog(claimCtx)
	cancel()
	if errors.Is(err, core.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	checkCtx, checkCancel := context.WithTimeout(ctx, 120*time.Second)
	items, discoveryErr := s.remote.Discover(checkCtx, c.actor(), c.Server)
	code := ""
	if discoveryErr != nil {
		code = "discovery_failed"
		if errors.Is(checkCtx.Err(), context.DeadlineExceeded) {
			code = "timeout"
		}
	}
	checkCancel()
	if ctx.Err() != nil {
		return true, ctx.Err()
	}
	finishCtx, finishCancel := context.WithTimeout(ctx, 10*time.Second)
	defer finishCancel()
	err = s.store.finishCatalog(finishCtx, c, items, code)
	if err != nil && (errors.Is(err, core.ErrInvalid) || errors.Is(err, core.ErrConflict)) {
		// A complete transport response can still exceed comparison/report limits.
		// The first transaction rolled back; preserve history and record safe status.
		err = s.store.finishCatalog(finishCtx, c, nil, "comparison_failed")
	}
	return true, err
}

// Two bounded consumers keep slow upstreams independent from operation/run
// recovery. SKIP LOCKED coordinates consumers across worker processes.
func (s *Service) RunCatalogScheduler(ctx context.Context) {
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				worked, err := s.CheckNextCatalog(ctx)
				if err != nil && ctx.Err() == nil && !errors.Is(err, core.ErrConflict) {
					slog.Error("scheduled catalog check could not be completed")
				}
				if !worked || err != nil {
					timer := time.NewTimer(15 * time.Second)
					select {
					case <-ctx.Done():
						timer.Stop()
						return
					case <-timer.C:
					}
				}
			}
		}()
	}
	wg.Wait()
}
