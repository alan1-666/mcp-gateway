package connectors

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/alan1-666/mcp-gateway/internal/adapters/mcpadapter"
	"github.com/alan1-666/mcp-gateway/internal/connectorwire"
	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/alan1-666/mcp-gateway/internal/store/postgres"
	"github.com/jackc/pgx/v5"
)

func (s *Service) Poll(ctx context.Context, token string, in connectorwire.PollInput) (*connectorwire.Job, error) {
	requested, err := targets(in.Targets)
	if err != nil {
		return nil, err
	}
	return transaction(ctx, s.db, func(tx pgx.Tx) (*connectorwire.Job, error) {
		c, err := authenticate(ctx, tx, token)
		if err != nil {
			return nil, err
		}
		raw, _ := json.Marshal(requested)
		old, _ := json.Marshal(c.Targets)
		if len(c.Targets) > 0 && !bytes.Equal(raw, old) {
			return nil, fmt.Errorf("%w: connector target configuration changed; register a new connector", core.ErrConflict)
		}
		if _, err = tx.Exec(ctx, `UPDATE gateway_connectors SET targets=$3,last_seen_at=clock_timestamp() WHERE workspace_id=$1 AND id=$2`, c.WorkspaceID, c.ID, raw); err != nil {
			return nil, err
		}
		if !in.Ready {
			return nil, nil
		}
		// Claimed work is deliberately not leased/requeued. A lost response is an
		// abandoned attempt; Start is a separate, durable authorization boundary.
		var payload []byte
		err = tx.QueryRow(ctx, `UPDATE gateway_connector_jobs SET state='claimed',updated_at=clock_timestamp() WHERE workspace_id=$1 AND id=(SELECT j.id FROM gateway_connector_jobs j JOIN mcp_servers ms ON ms.workspace_id=j.workspace_id AND ms.id=j.server_id WHERE j.workspace_id=$1 AND j.connector_id=$2 AND j.state='queued' AND j.deadline>clock_timestamp() AND ms.enabled AND ms.connector_id=j.connector_id AND ms.target_name=j.target_name ORDER BY j.created_at,j.id LIMIT 1 FOR UPDATE OF j SKIP LOCKED) RETURNING payload`, c.WorkspaceID, c.ID).Scan(&payload)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		var job connectorwire.Job
		if err = json.Unmarshal(payload, &job); err != nil {
			return nil, err
		}
		return &job, nil
	})
}

type storedJob struct {
	Job        connectorwire.Job
	State      string
	Actor      core.Actor
	Result     *connectorwire.Result
	ResultHash *string
	Deadline   time.Time
	StartedAt  *time.Time
	Now        time.Time
}

func loadJob(ctx context.Context, tx pgx.Tx, w, id, cid string) (storedJob, error) {
	var v storedJob
	var payload, actor, result []byte
	err := tx.QueryRow(ctx, `SELECT state,payload,actor,client_key_id,result,result_hash,deadline,started_at,clock_timestamp() FROM gateway_connector_jobs WHERE workspace_id=$1 AND id=$2 AND connector_id=$3 FOR UPDATE`, w, id, cid).Scan(&v.State, &payload, &actor, &v.Actor.ClientKeyID, &result, &v.ResultHash, &v.Deadline, &v.StartedAt, &v.Now)
	if err != nil {
		return v, mapError(err)
	}
	if len(payload) > 0 {
		if err = json.Unmarshal(payload, &v.Job); err != nil {
			return v, err
		}
	}
	key := v.Actor.ClientKeyID
	if err = json.Unmarshal(actor, &v.Actor); err != nil {
		return v, err
	}
	v.Actor.ClientKeyID = key
	if len(result) > 0 {
		v.Result = &connectorwire.Result{}
		if err = json.Unmarshal(result, v.Result); err != nil {
			return v, err
		}
	}
	return v, nil
}
func sameJSON(a, b any) bool {
	rawA, e1 := json.Marshal(a)
	rawB, e2 := json.Marshal(b)
	if e1 != nil || e2 != nil {
		return false
	}
	va, e1 := core.DecodeResult(rawA)
	vb, e2 := core.DecodeResult(rawB)
	rawA, _ = json.Marshal(va)
	rawB, _ = json.Marshal(vb)
	return e1 == nil && e2 == nil && bytes.Equal(rawA, rawB)
}

// Read the authoritative operation and approved tool snapshot, not values sent
// by the Connector. This lock order matches the existing operation executor.
func operation(ctx context.Context, tx pgx.Tx, w, id string) (core.Operation, core.Tool, error) {
	var op core.Operation
	var tool core.Tool
	var raw, snapshot []byte
	err := tx.QueryRow(ctx, `SELECT to_jsonb(o)-'tool_snapshot',tool_snapshot FROM operations o WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, w, id).Scan(&raw, &snapshot)
	if err != nil {
		return op, tool, mapError(err)
	}
	if err = json.Unmarshal(raw, &op); err != nil {
		return op, tool, err
	}
	err = json.Unmarshal(snapshot, &tool)
	return op, tool, err
}
func authorizeOperation(ctx context.Context, tx pgx.Tx, a core.Actor, op core.Operation, tool core.Tool) error {
	if op.State != core.StateDispatching || !core.CanExecuteOperation(a, op) || tool.ID != op.ToolID || tool.Version != op.ToolVersion || tool.Risk != op.Risk || tool.MCP == nil {
		return core.ErrConflict
	}
	if err := postgres.LockOperationClient(ctx, tx, a, op); err != nil {
		return err
	}
	var enabled bool
	var status string
	var currentServer *string
	var policy string
	var version int
	err := tx.QueryRow(ctx, `SELECT enabled,status,definition->'mcp'->>'server_id',COALESCE(definition->>'approval_policy',''),version FROM tools WHERE workspace_id=$1 AND id=$2 FOR SHARE`, a.WorkspaceID, op.ToolID).Scan(&enabled, &status, &currentServer, &policy, &version)
	if err != nil {
		return mapError(err)
	}
	if !enabled || status != "published" {
		return core.ErrConflict
	}
	if currentServer != nil {
		if err = tx.QueryRow(ctx, `SELECT enabled FROM mcp_servers WHERE workspace_id=$1 AND id=$2 FOR SHARE`, a.WorkspaceID, *currentServer).Scan(&enabled); err != nil {
			return mapError(err)
		}
		if !enabled {
			return core.ErrConflict
		}
	}
	requiresApproval, err := core.RequiresOperationApproval(tool, core.Tool{Risk: tool.Risk, Version: version, ApprovalPolicy: core.ApprovalPolicy(policy)})
	if err != nil {
		return err
	}
	if requiresApproval || op.ApprovedBy != "" {
		var valid bool
		if err = tx.QueryRow(ctx, `SELECT approved_by<>'' AND approved_by<>actor_id AND approval_expires_at>clock_timestamp() FROM operations WHERE workspace_id=$1 AND id=$2`, a.WorkspaceID, op.ID).Scan(&valid); err != nil {
			return err
		}
		if !valid {
			return core.ErrApprovalExpired
		}
	}
	return nil
}
func boundServer(ctx context.Context, tx pgx.Tx, w, id, cid, target string) (core.MCPServer, error) {
	v := core.MCPServer{ID: id, WorkspaceID: w}
	err := tx.QueryRow(ctx, `SELECT name,namespace,url,credential_ref,timeout_ms,enabled,coalesce(connector_id,''),target_name,created_at,updated_at FROM mcp_servers WHERE workspace_id=$1 AND id=$2 FOR SHARE`, w, id).Scan(&v.Name, &v.Namespace, &v.URL, &v.CredentialRef, &v.TimeoutMS, &v.Enabled, &v.ConnectorID, &v.TargetName, &v.CreatedAt, &v.UpdatedAt)
	if err != nil {
		return v, mapError(err)
	}
	if !v.Enabled || v.ConnectorID != cid || v.TargetName != target || v.URL != "" || v.CredentialRef != "" {
		return v, core.ErrConflict
	}
	return v, nil
}
func validateBinding(ctx context.Context, tx pgx.Tx, c connectorwire.Connector, v storedJob, execution bool) error {
	if v.Job.ID == "" || v.Job.Server.WorkspaceID != c.WorkspaceID || v.Job.Server.ConnectorID != c.ID {
		return core.ErrConflict
	}
	target, err := targetFor(c, v.Job.Target.Name)
	if err != nil {
		return err
	}
	if target != v.Job.Target {
		return core.ErrConflict
	}
	if execution {
		if v.Job.Operation == nil || v.Job.Tool == nil {
			return core.ErrConflict
		}
		op, tool, err := operation(ctx, tx, c.WorkspaceID, v.Job.Operation.ID)
		if err != nil {
			return err
		}
		if !sameJSON(tool, v.Job.Tool) || op.ArgumentsHash != v.Job.Operation.ArgumentsHash || !sameJSON(op.Arguments, v.Job.Operation.Arguments) || op.ToolID != v.Job.Tool.ID {
			return core.ErrConflict
		}
		if err = authorizeOperation(ctx, tx, v.Actor, op, tool); err != nil {
			return err
		}
	}
	_, err = boundServer(ctx, tx, c.WorkspaceID, v.Job.Server.ID, c.ID, v.Job.Target.Name)
	return err
}

func (s *Service) Start(ctx context.Context, token, id string) error {
	_, err := transaction(ctx, s.db, func(tx pgx.Tx) (bool, error) {
		c, err := authenticate(ctx, tx, token)
		if err != nil {
			return false, err
		}
		v, err := loadJob(ctx, tx, c.WorkspaceID, id, c.ID)
		if err != nil {
			return false, err
		}
		if v.State != "claimed" || !v.Now.Before(v.Deadline) {
			return false, core.ErrConflict
		}
		if err = validateBinding(ctx, tx, c, v, v.Job.Kind == "execute"); err != nil {
			return false, err
		}
		// Commit before authorizing any local process or network call. Revoke fences
		// this transaction; already authorized downstream actions cannot be recalled.
		tag, err := tx.Exec(ctx, `UPDATE gateway_connector_jobs SET state='started',started_at=clock_timestamp(),updated_at=clock_timestamp() WHERE workspace_id=$1 AND id=$2 AND deadline>clock_timestamp()`, c.WorkspaceID, id)
		if err == nil && tag.RowsAffected() != 1 {
			err = core.ErrConflict
		}
		return true, err
	})
	return err
}
func validResult(r connectorwire.Result, kind string) error {
	raw, err := json.Marshal(r)
	if err != nil || len(raw) > MaxResultBytes || len(r.Error) > 2000 {
		return core.ErrInvalid
	}
	if _, err = DecodeJSON(raw); err != nil {
		return core.ErrInvalid
	}
	if kind == "discover" {
		if len(r.Result) > 0 || len(r.Tools) > 1000 || (r.State != "" && r.State != core.StateSucceeded && r.State != core.StateFailed) {
			return core.ErrInvalid
		}
		if r.State != "" && r.State != core.StateFailed && r.Error != "" {
			return core.ErrInvalid
		}
	} else {
		if len(r.Tools) > 0 || (r.State != core.StateSucceeded && r.State != core.StateFailed && r.State != core.StateUnknown) || len(r.Result) > 1<<20 {
			return core.ErrInvalid
		}
		if r.State == core.StateSucceeded && (len(r.Result) == 0 || r.Error != "") {
			return core.ErrInvalid
		}
	}
	return nil
}
func (s *Service) Complete(ctx context.Context, token, id string, r connectorwire.Result) error {
	_, err := transaction(ctx, s.db, func(tx pgx.Tx) (bool, error) {
		c, err := authenticate(ctx, tx, token)
		if err != nil {
			return false, err
		}
		v, err := loadJob(ctx, tx, c.WorkspaceID, id, c.ID)
		if err != nil {
			return false, err
		}
		if err = validResult(r, v.Job.Kind); err != nil {
			return false, err
		}
		raw, _ := json.Marshal(r)
		normalized, err := DecodeJSON(raw)
		if err != nil {
			return false, core.ErrInvalid
		}
		raw, _ = json.Marshal(normalized)
		hash := digest(string(raw))
		if v.State == "completed" && v.ResultHash != nil && *v.ResultHash == hash {
			return true, validateBinding(ctx, tx, c, v, false)
		}
		if v.State != "started" || !v.Now.Before(v.Deadline) {
			return false, core.ErrConflict
		}
		if err = validateBinding(ctx, tx, c, v, v.Job.Kind == "execute"); err != nil {
			return false, err
		}
		// Only the projected result reaches durable cloud storage. The raw digest
		// still recognizes a retried completion without retaining dropped fields.
		r = prepareStoredResult(v.Job, r)
		raw, err = json.Marshal(r)
		if err != nil {
			return false, core.ErrInvalid
		}
		tag, err := tx.Exec(ctx, `UPDATE gateway_connector_jobs SET state='completed',result=$3,result_hash=$4,updated_at=clock_timestamp() WHERE workspace_id=$1 AND id=$2 AND deadline>clock_timestamp()`, c.WorkspaceID, id, raw, hash)
		if err == nil && tag.RowsAffected() != 1 {
			err = core.ErrConflict
		}
		return true, err
	})
	return err
}

func prepareStoredResult(job connectorwire.Job, r connectorwire.Result) connectorwire.Result {
	if job.Kind == "discover" {
		if r.State == core.StateFailed || r.Error != "" {
			return connectorwire.Result{State: core.StateFailed, Error: "connector discovery failed"}
		}
		return r
	}
	if r.State != core.StateSucceeded {
		return connectorwire.Result{State: r.State, Error: "connector execution did not produce a confirmed result"}
	}
	var raw json.RawMessage
	var err error
	if job.Tool == nil {
		err = core.ErrInvalid
	} else {
		raw, err = mcpadapter.SanitizeResult(r.Result, job.Tool.OutputSchema)
		if err == nil {
			raw, err = core.ApplyMCPResponsePolicy(raw, job.Tool.ResponsePolicy)
		}
	}
	if err != nil {
		state := core.StateFailed
		if job.Tool != nil && job.Tool.Risk == core.RiskWrite {
			state = core.StateUnknown
		}
		return connectorwire.Result{State: state, Error: "connector result did not satisfy the reviewed output contract and response policy"}
	}
	return connectorwire.Result{State: core.StateSucceeded, Result: raw}
}

func (s *Service) enqueue(ctx context.Context, a core.Actor, server core.MCPServer, opID string) (connectorwire.Job, error) {
	return transaction(ctx, s.db, func(tx pgx.Tx) (connectorwire.Job, error) {
		c, err := scan(tx.QueryRow(ctx, `SELECT `+columns+` FROM gateway_connectors WHERE workspace_id=$1 AND id=$2 AND enabled FOR UPDATE`, a.WorkspaceID, server.ConnectorID))
		if err != nil {
			return connectorwire.Job{}, err
		}
		target, err := targetFor(c, server.TargetName)
		if err != nil {
			return connectorwire.Job{}, err
		}
		job := connectorwire.Job{ID: core.NewID(), Kind: "discover", Target: target, Server: server}
		if opID != "" {
			op, tool, err := operation(ctx, tx, a.WorkspaceID, opID)
			if err != nil {
				return job, err
			}
			if err = authorizeOperation(ctx, tx, a, op, tool); err != nil {
				return job, err
			}
			if tool.MCP.ServerID != server.ID {
				return job, core.ErrConflict
			}
			job.Kind = "execute"
			job.Tool = &tool
			job.Operation = &op
			var raw []byte
			err = tx.QueryRow(ctx, `SELECT payload FROM gateway_connector_jobs WHERE workspace_id=$1 AND operation_id=$2`, a.WorkspaceID, opID).Scan(&raw)
			if err == nil {
				if len(raw) == 0 {
					return job, core.ErrConflict
				}
				err = json.Unmarshal(raw, &job)
				return job, err
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return job, err
			}
		} else if err = admin(a); err != nil {
			return job, err
		}
		current, err := boundServer(ctx, tx, a.WorkspaceID, server.ID, c.ID, target.Name)
		if err != nil {
			return job, err
		}
		job.Server = current
		// A workspace lock covers jobs across every Connector, not only this row.
		if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, a.WorkspaceID+"\x1fconnector-jobs"); err != nil {
			return job, err
		}
		var active int
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM gateway_connector_jobs WHERE workspace_id=$1 AND state IN ('queued','claimed','started') AND deadline>clock_timestamp()`, a.WorkspaceID).Scan(&active); err != nil {
			return job, err
		}
		if active >= MaxActiveJobs {
			return job, fmt.Errorf("%w: connector work queue is full", core.ErrConflict)
		}
		var now time.Time
		if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
			return job, err
		}
		job.Deadline = now.Add(time.Duration(current.TimeoutMS) * time.Millisecond)
		if deadline, ok := ctx.Deadline(); ok && deadline.Before(job.Deadline) {
			job.Deadline = deadline
		}
		if !now.Before(job.Deadline) {
			return job, context.DeadlineExceeded
		}
		raw, err := json.Marshal(job)
		if err != nil || len(raw) > 1<<20 {
			return job, core.ErrInvalid
		}
		actor, _ := json.Marshal(a)
		var op any
		if opID != "" {
			op = opID
		}
		_, err = tx.Exec(ctx, `INSERT INTO gateway_connector_jobs(workspace_id,id,connector_id,server_id,operation_id,kind,target_name,target_fingerprint,payload,actor,client_key_id,deadline) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, a.WorkspaceID, job.ID, c.ID, server.ID, op, job.Kind, target.Name, target.Fingerprint, raw, actor, a.ClientKeyID, job.Deadline)
		return job, err
	})
}

func (s *Service) wait(ctx context.Context, job connectorwire.Job) (connectorwire.Result, bool, error) {
	ctx, cancel := context.WithDeadline(ctx, job.Deadline)
	defer cancel()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		var state string
		var raw []byte
		var started *time.Time
		err := s.db.QueryRow(ctx, `SELECT state,result,started_at FROM gateway_connector_jobs WHERE workspace_id=$1 AND id=$2`, job.Server.WorkspaceID, job.ID).Scan(&state, &raw, &started)
		if err == nil && state == "completed" && len(raw) > 0 {
			var r connectorwire.Result
			err = json.Unmarshal(raw, &r)
			return r, started != nil, err
		}
		if err != nil || state == "expired" {
			break
		}
		select {
		case <-ctx.Done():
			goto expire
		case <-ticker.C:
		}
	}
expire:
	// Seal an unfinished job even if the HTTP caller has gone away, so an old
	// queued/claimed task cannot become executable after we report failure.
	cleanup, cancelCleanup := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelCleanup()
	type outcome struct {
		r        connectorwire.Result
		started  bool
		complete bool
	}
	out, err := transaction(cleanup, s.db, func(tx pgx.Tx) (outcome, error) {
		var id string
		if err := tx.QueryRow(cleanup, `SELECT id FROM gateway_connectors WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, job.Server.WorkspaceID, job.Server.ConnectorID).Scan(&id); err != nil {
			return outcome{}, err
		}
		v, err := loadJob(cleanup, tx, job.Server.WorkspaceID, job.ID, id)
		if err != nil {
			return outcome{}, err
		}
		o := outcome{started: v.StartedAt != nil}
		if v.State == "completed" && v.Result != nil {
			o.r = *v.Result
			o.complete = true
			return o, nil
		}
		_, err = tx.Exec(cleanup, `UPDATE gateway_connector_jobs SET state='expired',updated_at=clock_timestamp() WHERE workspace_id=$1 AND id=$2 AND state IN ('queued','claimed','started')`, job.Server.WorkspaceID, job.ID)
		return o, err
	})
	if err == nil && out.complete {
		return out.r, out.started, nil
	}
	// Database uncertainty after the job was persisted must not claim a write
	// failed before dispatch. Operators reconcile its UNKNOWN outcome.
	return connectorwire.Result{}, out.started || err != nil, fmt.Errorf("connector did not return a confirmed result before the request ended")
}
func (s *Service) Discover(ctx context.Context, a core.Actor, server core.MCPServer) ([]core.RemoteTool, error) {
	if a.WorkspaceID != server.WorkspaceID {
		return nil, core.ErrForbidden
	}
	job, err := s.enqueue(ctx, a, server, "")
	if err != nil {
		return nil, err
	}
	r, _, err := s.wait(ctx, job)
	if err != nil {
		return nil, err
	}
	if r.State == core.StateFailed || r.Error != "" {
		return nil, fmt.Errorf("connector discovery failed")
	}
	return r.Tools, nil
}
func (s *Service) Execute(ctx context.Context, a core.Actor, tool core.Tool, op core.Operation) core.FinishInput {
	fail := func(uncertain bool) core.FinishInput {
		state := core.StateFailed
		if uncertain && tool.Risk == core.RiskWrite {
			state = core.StateUnknown
		}
		return core.FinishInput{State: state, Error: "connector did not produce a confirmed result; inspect the operation before another action"}
	}
	if tool.MCP == nil || tool.WorkspaceID != a.WorkspaceID || op.WorkspaceID != a.WorkspaceID || op.ID == "" {
		return fail(false)
	}
	var server core.MCPServer
	server.ID = tool.MCP.ServerID
	server.WorkspaceID = a.WorkspaceID
	if err := s.db.QueryRow(ctx, `SELECT coalesce(connector_id,''),target_name,timeout_ms FROM mcp_servers WHERE workspace_id=$1 AND id=$2`, a.WorkspaceID, server.ID).Scan(&server.ConnectorID, &server.TargetName, &server.TimeoutMS); err != nil {
		return fail(false)
	}
	job, err := s.enqueue(ctx, a, server, op.ID)
	if err != nil {
		return fail(!errors.Is(err, core.ErrConflict) && !errors.Is(err, core.ErrForbidden) && !errors.Is(err, core.ErrNotFound) && !errors.Is(err, core.ErrInvalid) && !errors.Is(err, core.ErrApprovalExpired))
	}
	r, started, err := s.wait(ctx, job)
	if err != nil {
		return fail(started)
	}
	return core.FinishInput{State: r.State, Result: r.Result, Error: r.Error, MCPResultValidated: r.State == core.StateSucceeded}
}
