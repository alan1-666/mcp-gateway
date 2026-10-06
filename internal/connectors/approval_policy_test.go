package connectors

import (
	"testing"

	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/alan1-666/mcp-gateway/internal/store/postgres"
)

func TestExemptWriteConnectorStartRechecksPolicy(t *testing.T) {
	for _, scenario := range []string{"exempt", "tightened", "tighten-loosen"} {
		t.Run(scenario, func(t *testing.T) {
			f := newFixture(t)
			tool, op := f.execution(core.RiskWrite, func(tool *core.Tool) { tool.ApprovalPolicy = core.ApprovalNone })
			f.sql(`UPDATE operations SET approved_by='',approval_expires_at=NULL WHERE id=$1`, op.ID)
			job, err := f.s.enqueue(f.ctx, f.a, f.server, op.ID)
			if err != nil {
				t.Fatal(err)
			}
			if f.poll(true) == nil {
				t.Fatal("missing job")
			}
			if scenario != "exempt" {
				tool, err = postgres.New(f.pool).UpdateToolApprovalPolicy(f.ctx, f.a, tool.ID, core.ApprovalPolicyUpdateInput{ExpectedVersion: tool.Version, ApprovalPolicy: core.ApprovalRequired})
				if err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "tighten-loosen" {
				_, err = postgres.New(f.pool).UpdateToolApprovalPolicy(f.ctx, f.a, tool.ID, core.ApprovalPolicyUpdateInput{ExpectedVersion: tool.Version, ApprovalPolicy: core.ApprovalNone})
				if err != nil {
					t.Fatal(err)
				}
			}
			err = f.s.Start(f.ctx, f.c.Token, job.ID)
			if (err != nil) != (scenario != "exempt") {
				t.Fatal(scenario, err)
			}
		})
	}
}
