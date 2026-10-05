package capacity

import (
	"context"
	"github.com/alan1-666/mcp-gateway/internal/core"
	"time"
)

type CallMetric struct {
	Transport       string     `json:"transport"`
	State           core.State `json:"state"`
	Count           int64      `json:"count"`
	DurationMSTotal int64      `json:"duration_ms_total"`
}
type RejectionMetric struct {
	Scope  string `json:"scope"`
	Reason string `json:"reason"`
	Count  int64  `json:"count"`
}
type Metrics struct {
	Calls        []CallMetric      `json:"calls"`
	Rejections   []RejectionMetric `json:"rejections"`
	ActiveLeases int               `json:"active_leases"`
	ObservedAt   time.Time         `json:"observed_at"`
}

// Observe counts dispatch outcomes, not retries that reused an existing result.
// Dimensions come from the bounded enums below, never upstream error text.
func (s *Service) Observe(ctx context.Context, actor core.Actor, tool core.Tool, state core.State, duration time.Duration) error {
	if actor.WorkspaceID == "" {
		return core.ErrUnauthorized
	}
	if state != core.StateSucceeded && state != core.StateFailed && state != core.StateUnknown {
		return invalid("unsupported metric state")
	}
	transport := "http"
	if tool.MCP != nil {
		transport = "mcp"
	}
	_, err := s.db.Exec(ctx, `INSERT INTO capacity_call_metrics(workspace_id,transport,state,count,duration_ms_total) VALUES($1,$2,$3,1,$4) ON CONFLICT(workspace_id,transport,state) DO UPDATE SET count=capacity_call_metrics.count+1,duration_ms_total=capacity_call_metrics.duration_ms_total+excluded.duration_ms_total`, actor.WorkspaceID, transport, state, max(int64(0), duration.Milliseconds()))
	return err
}
func (s *Service) Metrics(ctx context.Context, actor core.Actor) (Metrics, error) {
	out := Metrics{Calls: []CallMetric{}, Rejections: []RejectionMetric{}}
	if err := authorize(actor); err != nil {
		return out, err
	}
	rows, err := s.db.Query(ctx, `SELECT transport,state,count,duration_ms_total FROM capacity_call_metrics WHERE workspace_id=$1 ORDER BY transport,state`, actor.WorkspaceID)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var v CallMetric
		if err = rows.Scan(&v.Transport, &v.State, &v.Count, &v.DurationMSTotal); err != nil {
			rows.Close()
			return out, err
		}
		out.Calls = append(out.Calls, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	rows, err = s.db.Query(ctx, `SELECT scope,reason,count FROM capacity_rejection_metrics WHERE workspace_id=$1 ORDER BY scope,reason`, actor.WorkspaceID)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var v RejectionMetric
		if err = rows.Scan(&v.Scope, &v.Reason, &v.Count); err != nil {
			rows.Close()
			return out, err
		}
		out.Rejections = append(out.Rejections, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	err = s.db.QueryRow(ctx, `SELECT count(*),clock_timestamp() FROM capacity_leases WHERE workspace_id=$1 AND expires_at>clock_timestamp()`, actor.WorkspaceID).Scan(&out.ActiveLeases, &out.ObservedAt)
	return out, err
}
