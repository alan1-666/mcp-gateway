package observability

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/alan1-666/mcp-gateway/internal/store/postgres"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type Reconciliation struct {
	ID          string    `json:"id"`
	OperationID string    `json:"operation_id"`
	Outcome     string    `json:"outcome"`
	EvidenceRef string    `json:"evidence_ref"`
	Note        string    `json:"note"`
	ActorID     string    `json:"actor_id"`
	CreatedAt   time.Time `json:"created_at"`
}
type ReconcileInput struct {
	ExpectedLastID string `json:"expected_last_id"`
	Outcome        string `json:"outcome"`
	EvidenceRef    string `json:"evidence_ref"`
	Note           string `json:"note"`
}

var evidenceID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

func normalizeReconciliation(in ReconcileInput) (ReconcileInput, int64, error) {
	last, err := strconv.ParseInt(in.ExpectedLastID, 10, 64)
	if err != nil || last < 0 || strconv.FormatInt(last, 10) != in.ExpectedLastID {
		return in, 0, invalid("expected_last_id must be a canonical nonnegative decimal string")
	}
	switch in.Outcome {
	case "confirmed_success", "confirmed_failure", "inconclusive":
	default:
		return in, 0, invalid("unsupported reconciliation outcome")
	}
	in.EvidenceRef = strings.TrimSpace(in.EvidenceRef)
	in.Note = strings.TrimSpace(in.Note)
	if len(in.EvidenceRef) < 1 || len(in.EvidenceRef) > 2048 || !utf8.ValidString(in.EvidenceRef) || strings.ContainsAny(in.EvidenceRef, "\x00\r\n") {
		return in, 0, invalid("provide a bounded evidence reference")
	}
	if !evidenceID.MatchString(in.EvidenceRef) {
		u, err := url.Parse(in.EvidenceRef)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Opaque != "" || u.Fragment != "" || u.RawQuery != "" || u.ForceQuery {
			return in, 0, invalid("evidence_ref must be an HTTPS URL without credentials, query or fragment, or a plain record identifier")
		}
	}
	if len(in.Note) < 1 || len(in.Note) > 2000 || !utf8.ValidString(in.Note) || strings.ContainsRune(in.Note, 0) {
		return in, 0, invalid("note must contain 1-2000 UTF-8 bytes")
	}
	return in, last, nil
}
func (s *Service) Reconcile(ctx context.Context, a core.Actor, id string, input ReconcileInput) (Reconciliation, error) {
	if err := authorized(a); err != nil {
		return Reconciliation{}, err
	}
	if a.ClientID != "" || (a.Role != core.RoleAdmin && a.Role != core.RoleApprover) {
		return Reconciliation{}, core.ErrForbidden
	}
	in, last, err := normalizeReconciliation(input)
	if err != nil {
		return Reconciliation{}, err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Reconciliation{}, err
	}
	defer tx.Rollback(context.Background())
	// The operation row serializes all evidence appends. No state transition or
	// invocation is performed; its UNKNOWN state and original result stay intact.
	var locked string
	if err = tx.QueryRow(ctx, `SELECT id FROM operations WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, a.WorkspaceID, id).Scan(&locked); err != nil {
		return Reconciliation{}, mapNotFound(err)
	}
	op, err := core.NewService(postgres.New(tx)).GetOperation(ctx, a, id)
	if err != nil {
		return Reconciliation{}, err
	}
	if op.State != core.StateUnknown {
		return Reconciliation{}, fmt.Errorf("%w: only UNKNOWN operations accept reconciliation evidence", core.ErrConflict)
	}
	var dispatchedByReviewer bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM operation_events WHERE workspace_id=$1 AND operation_id=$2 AND type='OPERATION_DISPATCHING' AND actor_id=$3)`, a.WorkspaceID, id, a.ID).Scan(&dispatchedByReviewer); err != nil {
		return Reconciliation{}, err
	}
	if op.ActorID == a.ID || dispatchedByReviewer {
		return Reconciliation{}, fmt.Errorf("%w: reconciliation requires a reviewer other than the requester and dispatching actor", core.ErrForbidden)
	}
	var current int64
	if err = tx.QueryRow(ctx, `SELECT COALESCE(max(id),0) FROM operation_reconciliations WHERE workspace_id=$1 AND operation_id=$2`, a.WorkspaceID, id).Scan(&current); err != nil {
		return Reconciliation{}, err
	}
	if current != last {
		return Reconciliation{}, fmt.Errorf("%w: reconciliation changed; reload before adding evidence", core.ErrConflict)
	}
	var record Reconciliation
	err = tx.QueryRow(ctx, `INSERT INTO operation_reconciliations(workspace_id,operation_id,outcome,evidence_ref,note,actor_id) VALUES($1,$2,$3,$4,$5,$6) RETURNING id::text,operation_id,outcome,evidence_ref,note,actor_id,created_at`, a.WorkspaceID, id, in.Outcome, in.EvidenceRef, in.Note, a.ID).Scan(&record.ID, &record.OperationID, &record.Outcome, &record.EvidenceRef, &record.Note, &record.ActorID, &record.CreatedAt)
	if err != nil {
		return Reconciliation{}, err
	}
	// Notes and references can contain confidential business data; audit only the
	// relationship and classification, not those user-supplied values.
	audit, _ := json.Marshal(map[string]any{"reconciliation_id": record.ID, "previous_id": in.ExpectedLastID, "outcome": record.Outcome})
	if _, err = tx.Exec(ctx, `INSERT INTO audit_events(workspace_id,actor_id,action,resource_id,data) VALUES($1,$2,'OPERATION_RECONCILIATION_ADDED',$3,$4)`, a.WorkspaceID, a.ID, id, audit); err != nil {
		return Reconciliation{}, err
	}
	return record, tx.Commit(ctx)
}
func (s *Service) Reconciliations(ctx context.Context, a core.Actor, id string, in Filter) (Page[Reconciliation], error) {
	p := Page[Reconciliation]{Items: []Reconciliation{}}
	if err := authorized(a); err != nil {
		return p, err
	}
	if _, err := core.NewService(postgres.New(s.db)).GetOperation(ctx, a, id); err != nil {
		return p, err
	}
	if in.ActorID != "" || in.Action != "" || in.ResourceID != "" || in.ToolID != "" || in.State != "" || in.From != nil || in.To != nil {
		return p, invalid("reconciliation history supports limit and cursor only")
	}
	in, err := normalize(in)
	if err != nil {
		return p, err
	}
	c, err := readCursor(a, in, "reconciliation:"+id, true)
	if err != nil {
		return p, err
	}
	var upperArg, afterArg, afterID any
	if in.Cursor != "" {
		upperArg = c.Upper
		afterArg = c.AfterTime
		afterID = c.AfterID
	}
	var upper time.Time
	var raw []byte
	err = s.db.QueryRow(ctx, `WITH boundary AS(SELECT COALESCE($3::timestamptz,statement_timestamp()) AS upper_bound),
 filtered AS MATERIALIZED(SELECT rc.id,rc.created_at FROM operation_reconciliations rc CROSS JOIN boundary b WHERE rc.workspace_id=$1 AND rc.operation_id=$2 AND rc.created_at<=b.upper_bound),
 page AS(SELECT id,created_at FROM filtered WHERE($4::timestamptz IS NULL OR(created_at,id)<($4,$5::bigint)) ORDER BY created_at DESC,id DESC LIMIT $6)
 SELECT(SELECT upper_bound FROM boundary),(SELECT count(*) FROM filtered),COALESCE((SELECT jsonb_agg((to_jsonb(rc)-'workspace_id')||jsonb_build_object('id',rc.id::text) ORDER BY rc.created_at DESC,rc.id DESC) FROM page p JOIN operation_reconciliations rc ON rc.id=p.id),'[]'::jsonb)`, a.WorkspaceID, id, upperArg, afterArg, afterID, in.Limit+1).Scan(&upper, &p.Total, &raw)
	if err != nil {
		return p, err
	}
	if err = json.Unmarshal(raw, &p.Items); err != nil {
		return p, fmt.Errorf("could not decode reconciliation history")
	}
	if len(p.Items) > in.Limit {
		p.Items = p.Items[:in.Limit]
		last := p.Items[in.Limit-1]
		p.NextCursor, err = writeCursor(c, upper, last.CreatedAt, last.ID)
	}
	return p, err
}
