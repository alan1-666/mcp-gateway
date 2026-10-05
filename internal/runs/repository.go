package runs

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository struct{ pool *pgxpool.Pool }

func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

const runColumns = `id,workspace_id,actor_id,prompt,state,attempt,output,error_code,waiting_operation_id,created_at,updated_at`

type scanner interface{ Scan(...any) error }

func scanRun(row scanner) (Run, error) {
	var v Run
	err := row.Scan(&v.ID, &v.WorkspaceID, &v.ActorID, &v.Prompt, &v.State, &v.Attempt, &v.Output, &v.ErrorCode, &v.WaitingOperationID, &v.CreatedAt, &v.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		err = core.ErrNotFound
	}
	return v, err
}
func transact[T any](ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) (T, error)) (T, error) {
	var zero T
	tx, err := pool.Begin(ctx)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback(context.Background())
	v, err := fn(tx)
	if err != nil {
		return zero, err
	}
	if err = tx.Commit(ctx); err != nil {
		return zero, err
	}
	return v, nil
}
func invalid(reason string) error  { return fmt.Errorf("%w: %s", core.ErrInvalid, reason) }
func conflict(reason string) error { return fmt.Errorf("%w: %s", core.ErrConflict, reason) }
func keyOK(s string, max int) bool {
	return len(s) > 0 && len(s) <= max && strings.TrimSpace(s) == s && !strings.ContainsAny(s, "\x00\r\n")
}

var codePattern = regexp.MustCompile(`^[A-Za-z0-9_.:/-]{0,128}$`)
var eventPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)

func liveActor(ctx context.Context, tx pgx.Tx, workspace, id string) (core.Actor, error) {
	a := core.Actor{ID: id, WorkspaceID: workspace}
	err := tx.QueryRow(ctx, `SELECT role FROM gateway_users WHERE workspace_id=$1 AND id=$2 AND NOT disabled FOR SHARE`, workspace, id).Scan(&a.Role)
	if errors.Is(err, pgx.ErrNoRows) {
		return a, core.ErrForbidden
	}
	return a, err
}
func liveOperator(ctx context.Context, tx pgx.Tx, workspace, id string) (core.Actor, error) {
	a, err := liveActor(ctx, tx, workspace, id)
	if err != nil {
		return a, err
	}
	if a.Role != core.RoleAdmin && a.Role != core.RoleOperator {
		return a, core.ErrForbidden
	}
	a.Role = core.RoleOperator
	return a, nil
}
func addEvent(ctx context.Context, tx pgx.Tx, run Run, kind string, data any) error {
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO agent_run_events(workspace_id,run_id,type,data) VALUES($1,$2,$3,$4)`, run.WorkspaceID, run.ID, kind, b)
	return err
}
func actorLock(ctx context.Context, tx pgx.Tx, workspace, id string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "agent-user:"+workspace+":"+id)
	return err
}
func (r *Repository) Create(ctx context.Context, actor core.Actor, in CreateInput) (Run, error) {
	if !utf8.ValidString(in.Prompt) || strings.TrimSpace(in.Prompt) == "" || utf8.RuneCountInString(in.Prompt) > 8000 || strings.ContainsRune(in.Prompt, 0) || !keyOK(in.IdempotencyKey, 128) {
		return Run{}, invalid("prompt must contain 1–8000 characters and idempotency_key 1–128 bytes")
	}
	return transact(ctx, r.pool, func(tx pgx.Tx) (Run, error) {
		if _, err := liveOperator(ctx, tx, actor.WorkspaceID, actor.ID); err != nil {
			return Run{}, err
		}
		if err := actorLock(ctx, tx, actor.WorkspaceID, actor.ID); err != nil {
			return Run{}, err
		}
		old, err := scanRun(tx.QueryRow(ctx, `SELECT `+runColumns+` FROM agent_runs WHERE workspace_id=$1 AND actor_id=$2 AND idempotency_key=$3`, actor.WorkspaceID, actor.ID, in.IdempotencyKey))
		if err == nil {
			if old.Prompt != in.Prompt {
				return Run{}, conflict("idempotency key already has a different prompt")
			}
			return old, nil
		}
		if !errors.Is(err, core.ErrNotFound) {
			return Run{}, err
		}
		var n int
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM agent_runs WHERE workspace_id=$1 AND actor_id=$2 AND state='QUEUED'`, actor.WorkspaceID, actor.ID).Scan(&n); err != nil {
			return Run{}, err
		}
		if n >= 10 {
			return Run{}, conflict("queued task limit reached")
		}
		run, err := scanRun(tx.QueryRow(ctx, `INSERT INTO agent_runs(workspace_id,id,actor_id,prompt,idempotency_key,state) VALUES($1,$2,$3,$4,$5,'QUEUED') RETURNING `+runColumns, actor.WorkspaceID, core.NewID(), actor.ID, in.Prompt, in.IdempotencyKey))
		if err != nil {
			return Run{}, err
		}
		return run, addEvent(ctx, tx, run, "RUN_CREATED", map[string]any{})
	})
}
func (r *Repository) Get(ctx context.Context, actor core.Actor, id string) (Run, error) {
	return scanRun(r.pool.QueryRow(ctx, `SELECT `+runColumns+` FROM agent_runs WHERE workspace_id=$1 AND id=$2 AND (actor_id=$3 OR $4)`, actor.WorkspaceID, id, actor.ID, actor.Role == core.RoleAdmin))
}
func (r *Repository) List(ctx context.Context, actor core.Actor) ([]Run, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+runColumns+` FROM agent_runs WHERE workspace_id=$1 AND (actor_id=$2 OR $3) ORDER BY created_at DESC,id DESC LIMIT 100`, actor.WorkspaceID, actor.ID, actor.Role == core.RoleAdmin)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Run{}
	for rows.Next() {
		v, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (r *Repository) Events(ctx context.Context, actor core.Actor, id string, after int64) ([]Event, error) {
	if after < 0 {
		return nil, invalid("cursor must be nonnegative")
	}
	if _, err := r.Get(ctx, actor, id); err != nil {
		return nil, err
	}
	rows, err := r.pool.Query(ctx, `SELECT id::text,run_id,type,data,created_at FROM agent_run_events WHERE workspace_id=$1 AND run_id=$2 AND id>$3 ORDER BY id LIMIT 200`, actor.WorkspaceID, id, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var v Event
		if err = rows.Scan(&v.ID, &v.RunID, &v.Type, &v.Data, &v.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (r *Repository) change(ctx context.Context, actor core.Actor, id string, resume bool) (Run, error) {
	return transact(ctx, r.pool, func(tx pgx.Tx) (Run, error) {
		// Lock identity before the Run consistently with lease admission.
		current, err := liveActor(ctx, tx, actor.WorkspaceID, actor.ID)
		if err != nil {
			return Run{}, err
		}
		run, err := scanRun(tx.QueryRow(ctx, `SELECT `+runColumns+` FROM agent_runs WHERE workspace_id=$1 AND id=$2 AND (actor_id=$3 OR $4) FOR UPDATE`, actor.WorkspaceID, id, actor.ID, current.Role == core.RoleAdmin))
		if err != nil {
			return Run{}, err
		}
		state, event := Cancelled, "RUN_CANCELLED"
		if resume {
			if run.State != WaitingApproval && run.State != WaitingCredentials && run.State != NeedsReview {
				return Run{}, conflict("task is not resumable")
			}
			if _, err = liveOperator(ctx, tx, run.WorkspaceID, run.ActorID); err != nil {
				return Run{}, err
			}
			if err = actorLock(ctx, tx, run.WorkspaceID, run.ActorID); err != nil {
				return Run{}, err
			}
			var blocked bool
			var queued int
			if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_run_operations b JOIN operations o ON o.workspace_id=b.workspace_id AND o.id=b.operation_id WHERE b.workspace_id=$1 AND b.run_id=$2 AND o.state IN ('UNKNOWN','DISPATCHING','WAITING_APPROVAL'))`, run.WorkspaceID, run.ID).Scan(&blocked); err != nil {
				return Run{}, err
			}
			if blocked {
				return Run{}, conflict("a bound operation requires approval or reconciliation")
			}
			if err = tx.QueryRow(ctx, `SELECT count(*) FROM agent_runs WHERE workspace_id=$1 AND actor_id=$2 AND state='QUEUED'`, run.WorkspaceID, run.ActorID).Scan(&queued); err != nil {
				return Run{}, err
			}
			if queued >= 10 {
				return Run{}, conflict("queued task limit reached")
			}
			state, event = Queued, "RUN_RESUMED"
		} else {
			if run.State == Cancelled {
				return run, nil
			}
			if run.State == Succeeded || run.State == Failed {
				return Run{}, conflict("task is already complete")
			}
		}
		run, err = scanRun(tx.QueryRow(ctx, `UPDATE agent_runs SET state=$3,lease_hash=NULL,lease_expires_at=NULL,attempt_deadline=NULL,error_code='',output=CASE WHEN $4 THEN '' ELSE output END,updated_at=clock_timestamp() WHERE workspace_id=$1 AND id=$2 RETURNING `+runColumns, run.WorkspaceID, run.ID, state, resume))
		if err != nil {
			return Run{}, err
		}
		return run, addEvent(ctx, tx, run, event, map[string]any{})
	})
}
func (r *Repository) Cancel(ctx context.Context, a core.Actor, id string) (Run, error) {
	return r.change(ctx, a, id, false)
}
func (r *Repository) Resume(ctx context.Context, a core.Actor, id string) (Run, error) {
	return r.change(ctx, a, id, true)
}

// RecoverExpired never redispatches a task; only an explicit user resume can requeue it.
// An empty workspace performs internal cross-workspace maintenance.
func (r *Repository) RecoverExpired(ctx context.Context, workspace string) (int, error) {
	return transact(ctx, r.pool, func(tx pgx.Tx) (int, error) {
		rows, err := tx.Query(ctx, `SELECT `+runColumns+` FROM agent_runs WHERE ($1='' OR workspace_id=$1) AND state='RUNNING' AND (lease_expires_at<=clock_timestamp() OR attempt_deadline<=clock_timestamp()) ORDER BY lease_expires_at LIMIT 100 FOR UPDATE SKIP LOCKED`, workspace)
		if err != nil {
			return 0, err
		}
		var stale []Run
		for rows.Next() {
			v, e := scanRun(rows)
			if e != nil {
				rows.Close()
				return 0, e
			}
			stale = append(stale, v)
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return 0, err
		}
		for _, run := range stale {
			if _, err = tx.Exec(ctx, `UPDATE agent_runs SET state='NEEDS_REVIEW',error_code='LEASE_EXPIRED',lease_hash=NULL,lease_expires_at=NULL,attempt_deadline=NULL,updated_at=clock_timestamp() WHERE workspace_id=$1 AND id=$2`, run.WorkspaceID, run.ID); err != nil {
				return 0, err
			}
			if err = addEvent(ctx, tx, run, "RUN_NEEDS_REVIEW", map[string]any{"error_code": "LEASE_EXPIRED", "attempt": run.Attempt}); err != nil {
				return 0, err
			}
		}
		return len(stale), nil
	})
}
func (r *Repository) Claim(ctx context.Context, workspace, worker string) (Claim, error) {
	if !keyOK(worker, 128) {
		return Claim{}, invalid("worker_id must contain 1–128 bytes")
	}
	if _, err := r.RecoverExpired(ctx, workspace); err != nil {
		return Claim{}, err
	}
	return transact(ctx, r.pool, func(tx pgx.Tx) (Claim, error) {
		// Serialize the short scheduler admission per workspace; row locks fence all
		// mutations and partial unique indexes independently enforce both limits.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "agent-scheduler:"+workspace); err != nil {
			return Claim{}, err
		}
		var busy bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_runs WHERE workspace_id=$1 AND worker_id=$2 AND state='RUNNING')`, workspace, worker).Scan(&busy); err != nil {
			return Claim{}, err
		}
		if busy {
			return Claim{}, nil
		}
		run, err := scanRun(tx.QueryRow(ctx, `SELECT `+runColumns+` FROM agent_runs r WHERE workspace_id=$1 AND state='QUEUED' AND (worker_id='' OR worker_id=$2) AND EXISTS(SELECT 1 FROM gateway_users u WHERE u.id=r.actor_id AND u.workspace_id=r.workspace_id AND NOT u.disabled AND u.role IN ('admin','operator')) AND NOT EXISTS(SELECT 1 FROM agent_runs active WHERE active.workspace_id=r.workspace_id AND active.actor_id=r.actor_id AND active.state='RUNNING') ORDER BY created_at,id LIMIT 1 FOR UPDATE SKIP LOCKED`, workspace, worker))
		if errors.Is(err, core.ErrNotFound) {
			return Claim{}, nil
		}
		if err != nil {
			return Claim{}, err
		}
		if _, err = liveOperator(ctx, tx, workspace, run.ActorID); err != nil {
			return Claim{}, err
		}
		var random [32]byte
		if _, err = rand.Read(random[:]); err != nil {
			return Claim{}, err
		}
		token := base64.RawURLEncoding.EncodeToString(random[:])
		hash := sha256.Sum256([]byte(token))
		run, err = scanRun(tx.QueryRow(ctx, `UPDATE agent_runs SET state='RUNNING',attempt=attempt+1,event_count=0,event_bytes=0,worker_id=$3,lease_hash=$4,lease_expires_at=clock_timestamp()+interval '45 seconds',attempt_deadline=clock_timestamp()+interval '5 minutes',updated_at=clock_timestamp() WHERE workspace_id=$1 AND id=$2 RETURNING `+runColumns, workspace, run.ID, worker, hash[:]))
		if err != nil {
			return Claim{}, err
		}
		var expires time.Time
		if err = tx.QueryRow(ctx, `SELECT lease_expires_at FROM agent_runs WHERE workspace_id=$1 AND id=$2`, workspace, run.ID).Scan(&expires); err != nil {
			return Claim{}, err
		}
		if err = addEvent(ctx, tx, run, "RUN_CLAIMED", map[string]any{"attempt": run.Attempt}); err != nil {
			return Claim{}, err
		}
		return Claim{Run: &run, LeaseToken: token, LeaseExpiresAt: &expires}, nil
	})
}

// lease locks a task and checks the creator's current enabled operator/admin role.
// Holding this lock through operation Claim makes cancellation and admission ordered.
func (r *Repository) lease(ctx context.Context, tx pgx.Tx, workspace, id, token string) (Run, core.Actor, error) {
	var zero core.Actor
	if len(token) < 32 || len(token) > 128 {
		return Run{}, zero, ErrLease
	}
	hash := sha256.Sum256([]byte(token))
	run, err := scanRun(tx.QueryRow(ctx, `SELECT `+runColumns+` FROM agent_runs WHERE workspace_id=$1 AND id=$2 AND state='RUNNING' AND lease_hash=$3 AND lease_expires_at>clock_timestamp() AND attempt_deadline>clock_timestamp() FOR UPDATE`, workspace, id, hash[:]))
	if errors.Is(err, core.ErrNotFound) {
		return Run{}, zero, ErrLease
	}
	if err != nil {
		return Run{}, zero, err
	}
	// A SELECT may have evaluated its predicate before waiting on a row lock
	// whose holder made no changes. Recheck database time after obtaining it.
	var current bool
	if err = tx.QueryRow(ctx, `SELECT lease_expires_at>clock_timestamp() AND attempt_deadline>clock_timestamp() FROM agent_runs WHERE workspace_id=$1 AND id=$2`, workspace, id).Scan(&current); err != nil {
		return Run{}, zero, err
	}
	if !current {
		return Run{}, zero, ErrLease
	}
	actor, err := liveOperator(ctx, tx, workspace, run.ActorID)
	return run, actor, err
}
func (r *Repository) Heartbeat(ctx context.Context, workspace, id, token string) (time.Time, error) {
	return transact(ctx, r.pool, func(tx pgx.Tx) (time.Time, error) {
		if _, _, err := r.lease(ctx, tx, workspace, id, token); err != nil {
			return time.Time{}, err
		}
		var expiry time.Time
		err := tx.QueryRow(ctx, `UPDATE agent_runs SET lease_expires_at=LEAST(clock_timestamp()+interval '45 seconds',attempt_deadline),updated_at=clock_timestamp() WHERE workspace_id=$1 AND id=$2 RETURNING lease_expires_at`, workspace, id).Scan(&expiry)
		return expiry, err
	})
}
func (r *Repository) Append(ctx context.Context, workspace, id, token string, in EventInput) error {
	if !keyOK(in.EventKey, 128) || !eventPattern.MatchString(in.Type) || strings.HasPrefix(in.Type, "RUN_") || len(in.Data) > MaxEventBytes {
		return invalid("event key, type or body exceeds allowed bounds")
	}
	canonical, _, err := core.CanonicalArguments(in.Data)
	if err != nil {
		return err
	}
	_, err = transact(ctx, r.pool, func(tx pgx.Tx) (bool, error) {
		run, _, err := r.lease(ctx, tx, workspace, id, token)
		if err != nil {
			return false, err
		}
		// Attempt is authored by the server, never taken on trust from a worker.
		var fields map[string]json.RawMessage
		if err = json.Unmarshal(canonical, &fields); err != nil {
			return false, err
		}
		fields["attempt"] = json.RawMessage(strconv.Itoa(run.Attempt))
		canonical, err = json.Marshal(fields)
		if err != nil {
			return false, err
		}
		if len(canonical) > MaxEventBytes {
			return false, invalid("event body exceeds allowed bounds")
		}
		var identical bool
		err = tx.QueryRow(ctx, `SELECT type=$4 AND data=$5::jsonb FROM agent_run_events WHERE workspace_id=$1 AND run_id=$2 AND event_key=$3`, workspace, id, in.EventKey, in.Type, canonical).Scan(&identical)
		if err == nil {
			if !identical {
				return false, conflict("event key is bound to a different event")
			}
			return true, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return false, err
		}
		tag, err := tx.Exec(ctx, `UPDATE agent_runs SET event_count=event_count+1,event_bytes=event_bytes+$3 WHERE workspace_id=$1 AND id=$2 AND event_count<$4 AND event_bytes+$3<=$5`, workspace, id, len(canonical), MaxEvents, MaxStoredEventBytes)
		if err != nil {
			return false, err
		}
		if tag.RowsAffected() != 1 {
			return false, conflict("event budget exhausted")
		}
		_, err = tx.Exec(ctx, `INSERT INTO agent_run_events(workspace_id,run_id,event_key,type,data) VALUES($1,$2,$3,$4,$5)`, workspace, id, in.EventKey, in.Type, canonical)
		return err == nil, err
	})
	return err
}
func (r *Repository) Finish(ctx context.Context, workspace, id, token string, in FinishInput) (Run, error) {
	switch in.State {
	case WaitingApproval, WaitingCredentials, NeedsReview, Succeeded, Failed:
	default:
		return Run{}, invalid("unsupported completion state")
	}
	if len(in.Output) > MaxOutputBytes || !utf8.ValidString(in.Output) || strings.ContainsRune(in.Output, 0) || !codePattern.MatchString(in.ErrorCode) || len(in.WaitingOperationID) > 128 {
		return Run{}, invalid("completion fields exceed allowed bounds")
	}
	return transact(ctx, r.pool, func(tx pgx.Tx) (Run, error) {
		run, _, err := r.lease(ctx, tx, workspace, id, token)
		if err != nil {
			return Run{}, err
		}
		if in.WaitingOperationID != "" {
			var bound bool
			if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_run_operations WHERE workspace_id=$1 AND run_id=$2 AND operation_id=$3)`, workspace, id, in.WaitingOperationID).Scan(&bound); err != nil {
				return Run{}, err
			}
			if !bound {
				return Run{}, core.ErrNotFound
			}
		}
		if in.State == WaitingApproval && in.WaitingOperationID == "" {
			return Run{}, invalid("waiting approval requires a bound operation")
		}
		run, err = scanRun(tx.QueryRow(ctx, `UPDATE agent_runs SET state=$3,output=$4,error_code=$5,waiting_operation_id=$6,lease_hash=NULL,lease_expires_at=NULL,attempt_deadline=NULL,updated_at=clock_timestamp() WHERE workspace_id=$1 AND id=$2 RETURNING `+runColumns, workspace, id, in.State, in.Output, in.ErrorCode, in.WaitingOperationID))
		if err != nil {
			return Run{}, err
		}
		return run, addEvent(ctx, tx, run, "RUN_"+string(in.State), map[string]any{"error_code": in.ErrorCode, "waiting_operation_id": in.WaitingOperationID})
	})
}
func (r *Repository) Status(ctx context.Context, workspace string, in StatusInput) error {
	if !keyOK(in.WorkerID, 128) || !codePattern.MatchString(in.Provider) || !codePattern.MatchString(in.ModelID) || !codePattern.MatchString(in.ErrorCode) {
		return invalid("runtime identifiers exceed allowed bounds")
	}
	_, err := r.pool.Exec(ctx, `INSERT INTO agent_runner_status(workspace_id,worker_id,model_ready,provider,model_id,error_code) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(workspace_id) DO UPDATE SET worker_id=EXCLUDED.worker_id,model_ready=EXCLUDED.model_ready,provider=EXCLUDED.provider,model_id=EXCLUDED.model_id,error_code=EXCLUDED.error_code,last_seen_at=clock_timestamp()`, workspace, in.WorkerID, in.ModelReady, in.Provider, in.ModelID, in.ErrorCode)
	return err
}
func (r *Repository) Runtime(ctx context.Context, workspace string) (Runtime, error) {
	var v Runtime
	err := r.pool.QueryRow(ctx, `SELECT last_seen_at>clock_timestamp()-interval '90 seconds',model_ready,provider,model_id,error_code,last_seen_at FROM agent_runner_status WHERE workspace_id=$1`, workspace).Scan(&v.Online, &v.ModelReady, &v.Provider, &v.ModelID, &v.ErrorCode, &v.LastSeenAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Runtime{ErrorCode: "RUNNER_OFFLINE"}, nil
	}
	if !v.Online {
		v.ModelReady = false
		v.ErrorCode = "RUNNER_OFFLINE"
	}
	return v, err
}
