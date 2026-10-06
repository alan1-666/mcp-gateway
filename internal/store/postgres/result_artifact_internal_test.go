package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Fault injection verifies completion aborts atomically for storage failures and
// quota exhaustion; PostgreSQL success/authorization behavior is covered by the
// external database-backed tests.
type artifactTx struct {
	pgx.Tx
	failAt, step int
	used, count  int64
}

var errArtifactDB = errors.New("injected artifact database failure")

func (t *artifactTx) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	t.step++
	if t.step == t.failAt {
		return pgconn.CommandTag{}, errArtifactDB
	}
	return pgconn.NewCommandTag("DELETE 0"), nil
}
func (t *artifactTx) QueryRow(_ context.Context, _ string, _ ...any) pgx.Row {
	t.step++
	return artifactRow{err: t.step == t.failAt, used: t.used, count: t.count}
}

type artifactRow struct {
	err         bool
	used, count int64
}

func (r artifactRow) Scan(dest ...any) error {
	if r.err {
		return errArtifactDB
	}
	if len(dest) == 2 {
		*dest[0].(*int64) = r.used
		*dest[1].(*int64) = r.count
	} else {
		*dest[0].(*time.Time) = time.Now().Add(time.Hour)
	}
	return nil
}
func TestResultArtifactStorageFailureAndQuota(t *testing.T) {
	ctx := context.Background()
	tool := core.Tool{MCP: &core.MCPConfig{}, ResponsePolicy: &core.ResponsePolicy{MaxBytes: 1024, Artifact: &core.ResultArtifactPolicy{MaxBytes: 16384, TTLSeconds: 60}}}
	in := core.FinishInput{State: core.StateSucceeded, Result: json.RawMessage(`{"isError":false,"content":[],"structuredContent":{"data":"` + strings.Repeat("x", 2000) + `"}}`)}
	op := core.Operation{ID: "op", WorkspaceID: "workspace"}
	for i := 1; i <= 4; i++ {
		if _, err := persistResultArtifact(ctx, &artifactTx{failAt: i}, op, tool, in); !errors.Is(err, errArtifactDB) {
			t.Fatalf("step %d: %v", i, err)
		}
	}
	for _, tx := range []*artifactTx{{used: workspaceArtifactBytes}, {count: 1000}} {
		if _, err := persistResultArtifact(ctx, tx, op, tool, in); !errors.Is(err, errArtifactQuota) {
			t.Fatal("quota", err)
		}
	}
	disabled := tool
	disabled.ResponsePolicy = nil
	if out, err := persistResultArtifact(ctx, nil, op, disabled, in); err != nil || string(out) != string(in.Result) {
		t.Fatal("default changed", err)
	}
	invalid := tool
	invalid.ResponsePolicy = &core.ResponsePolicy{MaxBytes: 1024, Artifact: &core.ResultArtifactPolicy{TTLSeconds: 1}}
	if _, err := persistResultArtifact(ctx, nil, op, invalid, in); !errors.Is(err, core.ErrInvalid) {
		t.Fatal("invalid policy accepted", err)
	}
	invalidResult := in
	invalidResult.Result = json.RawMessage(`{"isError":true}`)
	if _, err := persistResultArtifact(ctx, nil, op, tool, invalidResult); err == nil {
		t.Fatal("error result accepted")
	}
}
