package upstreams

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/alan1-666/mcp-gateway/internal/core"
)

const executionSampleLimit = 1000

// ExecutionMetrics is a bounded historical sample, not an availability SLA.
// Sampling follows the existing workspace/created_at index and includes at most
// 1000 operations created during the last day before selecting this server.
type ExecutionMetrics struct {
	ServerID          string                 `json:"server_id"`
	ObservedAt        time.Time              `json:"observed_at"`
	WindowStart       time.Time              `json:"window_start"`
	OperationsScanned int                    `json:"operations_scanned"`
	SampleLimit       int                    `json:"sample_limit"`
	Truncated         bool                   `json:"truncated"`
	Calls             int                    `json:"calls"`
	Attempted         int                    `json:"attempted"`
	States            map[core.State]int     `json:"states"`
	Errors            map[string]int         `json:"errors"`
	P50MS             int64                  `json:"p50_ms"`
	P95MS             int64                  `json:"p95_ms"`
	MeanPhasesMS      core.MCPPhaseDurations `json:"mean_phases_ms"`
	Health            string                 `json:"health"`
	LastObservedAt    *time.Time             `json:"last_observed_at,omitempty"`
	LastCode          string                 `json:"last_code,omitempty"`
}

type executionSample struct {
	at          time.Time
	state       core.State
	observation core.MCPObservation
}

func (s *Service) ExecutionMetrics(ctx context.Context, actor core.Actor, id string) (ExecutionMetrics, error) {
	if err := admin(actor); err != nil {
		return ExecutionMetrics{}, err
	}
	server, err := s.store.GetServer(ctx, actor.WorkspaceID, id)
	if err != nil {
		return ExecutionMetrics{}, err
	}
	var observed time.Time
	if err := s.store.db.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&observed); err != nil {
		return ExecutionMetrics{}, err
	}
	out := ExecutionMetrics{ServerID: id, ObservedAt: observed, WindowStart: observed.Add(-24 * time.Hour), SampleLimit: executionSampleLimit, States: map[core.State]int{}, Errors: map[string]int{}}
	rows, err := s.store.db.Query(ctx, `WITH recent AS MATERIALIZED (
 SELECT id,state,tool_snapshot,created_at FROM operations
 WHERE workspace_id=$1 AND created_at >= $2 AND created_at <= $3
 ORDER BY created_at DESC,id DESC LIMIT $4
 ) SELECT r.tool_snapshot->'mcp'->>'server_id',r.state,e.created_at,e.data->'mcp_execution'
 FROM recent r LEFT JOIN LATERAL (
 SELECT created_at,data FROM operation_events WHERE workspace_id=$1 AND operation_id=r.id
 AND created_at <= $3 AND type='OPERATION_'||r.state AND data ? 'mcp_execution' ORDER BY id DESC LIMIT 1
 ) e ON true ORDER BY r.created_at DESC,r.id DESC`, actor.WorkspaceID, out.WindowStart, observed, executionSampleLimit+1)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	samples := make([]executionSample, 0)
	for rows.Next() {
		var serverID *string
		var state core.State
		var at *time.Time
		var raw []byte
		if err := rows.Scan(&serverID, &state, &at, &raw); err != nil {
			return out, err
		}
		if out.OperationsScanned == executionSampleLimit {
			out.Truncated = true
			break
		}
		out.OperationsScanned++
		if serverID == nil || *serverID != id || at == nil || len(raw) == 0 {
			continue
		}
		var observation core.MCPObservation
		if json.Unmarshal(raw, &observation) != nil || observation.Validate() != nil {
			return out, fmt.Errorf("invalid stored MCP observation")
		}
		samples = append(samples, executionSample{*at, state, observation})
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	aggregateExecutions(&out, server, samples)
	return out, nil
}

func aggregateExecutions(out *ExecutionMetrics, server core.MCPServer, samples []executionSample) {
	durations := make([]int64, 0, len(samples))
	for _, sample := range samples {
		o := sample.observation
		out.Calls++
		if o.CallAttempted {
			out.Attempted++
		}
		out.States[sample.state]++
		if o.Code != "ok" {
			out.Errors[o.Code]++
		}
		durations = append(durations, o.TotalMS)
		out.MeanPhasesMS.Configuration += o.PhasesMS.Configuration
		out.MeanPhasesMS.Session += o.PhasesMS.Session
		out.MeanPhasesMS.Catalog += o.PhasesMS.Catalog
		out.MeanPhasesMS.Call += o.PhasesMS.Call
		out.MeanPhasesMS.Result += o.PhasesMS.Result
		out.MeanPhasesMS.Projection += o.PhasesMS.Projection
		if out.LastObservedAt == nil || sample.at.After(*out.LastObservedAt) {
			at := sample.at
			out.LastObservedAt, out.LastCode = &at, o.Code
		}
	}
	if out.Calls > 0 {
		sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
		// Nearest-rank percentiles, calculated only from the included sample.
		out.P50MS = durations[(out.Calls*50+99)/100-1]
		out.P95MS = durations[(out.Calls*95+99)/100-1]
		n := int64(out.Calls)
		out.MeanPhasesMS.Configuration /= n
		out.MeanPhasesMS.Session /= n
		out.MeanPhasesMS.Catalog /= n
		out.MeanPhasesMS.Call /= n
		out.MeanPhasesMS.Result /= n
		out.MeanPhasesMS.Projection /= n
	}
	switch {
	case !server.Enabled:
		out.Health = "disabled"
	case out.LastObservedAt == nil:
		out.Health = "unobserved"
	case out.LastObservedAt.Before(server.UpdatedAt) || out.ObservedAt.Sub(*out.LastObservedAt) > 5*time.Minute:
		out.Health = "stale"
	case out.LastCode == "ok":
		out.Health = "healthy"
	case out.LastCode == "connection_failed" || out.LastCode == "upstream_unavailable" || out.LastCode == "rate_limited" || out.LastCode == "timeout" || out.LastCode == "call_unconfirmed":
		out.Health = "degraded"
	default:
		out.Health = "attention"
	}
}
