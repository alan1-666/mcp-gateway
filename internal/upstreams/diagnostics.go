package upstreams

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/alan1-666/mcp-gateway/internal/core"
)

type ToolCompatibility struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Code    string `json:"code"`
	Message string `json:"message"`
}
type CheckReport struct {
	ID                    int64               `json:"id,string"`
	ServerID              string              `json:"server_id"`
	CheckedAt             time.Time           `json:"checked_at"`
	Status                string              `json:"status"`
	Stage                 string              `json:"stage"`
	Code                  string              `json:"code"`
	Message               string              `json:"message"`
	DurationMS            int64               `json:"duration_ms"`
	CompatibleCount       int                 `json:"compatible_count"`
	IncompatibleCount     int                 `json:"incompatible_count"`
	SessionContractStatus string              `json:"session_contract_status,omitempty"`
	Tools                 []ToolCompatibility `json:"tools"`
}
type CheckPage struct {
	Items      []CheckReport `json:"items"`
	NextCursor string        `json:"next_cursor,omitempty"`
}
type RemoteChecker interface {
	Check(context.Context, core.Actor, core.MCPServer) CheckReport
}

func (s *Service) Check(ctx context.Context, actor core.Actor, id string) (CheckReport, error) {
	if err := admin(actor); err != nil {
		return CheckReport{}, err
	}
	server, err := s.store.GetServer(ctx, actor.WorkspaceID, id)
	if err != nil {
		return CheckReport{}, err
	}
	checker, ok := s.remote.(RemoteChecker)
	if !ok {
		return CheckReport{}, fmt.Errorf("connection diagnostics unavailable")
	}
	report := checker.Check(ctx, actor, server)
	report.ID = 0
	report.ServerID = server.ID
	report.CheckedAt = time.Time{}
	if report.Tools == nil {
		report.Tools = []ToolCompatibility{}
	}
	raw, err := json.Marshal(report)
	if err != nil {
		return CheckReport{}, fmt.Errorf("could not encode connection check")
	}
	if err = s.store.db.QueryRow(ctx, `INSERT INTO mcp_server_checks(workspace_id,server_id,report) VALUES($1,$2,$3) RETURNING id,checked_at`, actor.WorkspaceID, server.ID, raw).Scan(&report.ID, &report.CheckedAt); err != nil {
		return CheckReport{}, err
	}
	return report, nil
}
func (s *Service) Checks(ctx context.Context, actor core.Actor, id string, limit int, before int64) (CheckPage, error) {
	p := CheckPage{Items: []CheckReport{}}
	if err := admin(actor); err != nil {
		return p, err
	}
	if limit == 0 {
		limit = 20
	}
	if limit < 1 || limit > 50 || before < 0 {
		return p, fmt.Errorf("%w: check history limit must be 1-50 and cursor nonnegative", core.ErrInvalid)
	}
	if _, err := s.store.GetServer(ctx, actor.WorkspaceID, id); err != nil {
		return p, err
	}
	rows, err := s.store.db.Query(ctx, `SELECT id,checked_at,report FROM mcp_server_checks WHERE workspace_id=$1 AND server_id=$2 AND ($3::bigint=0 OR id<$3) ORDER BY id DESC LIMIT $4`, actor.WorkspaceID, id, before, limit+1)
	if err != nil {
		return p, err
	}
	defer rows.Close()
	for rows.Next() {
		var report CheckReport
		var raw []byte
		var checkID int64
		var checked time.Time
		if err = rows.Scan(&checkID, &checked, &raw); err != nil {
			return p, err
		}
		if err = json.Unmarshal(raw, &report); err != nil {
			return p, fmt.Errorf("could not decode connection check")
		}
		report.ID = checkID
		report.CheckedAt = checked
		p.Items = append(p.Items, report)
	}
	if err = rows.Err(); err != nil {
		return p, err
	}
	if len(p.Items) > limit {
		p.Items = p.Items[:limit]
		p.NextCursor = fmt.Sprint(p.Items[limit-1].ID)
	}
	return p, nil
}
