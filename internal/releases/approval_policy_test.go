package releases

import (
	"testing"

	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/alan1-666/mcp-gateway/internal/store/postgres"
)

func TestDefinitionRevisionsPreserveApprovalPolicy(t *testing.T) {
	ctx, pool, s, _, a, tool := fixture(t)
	cs := core.NewService(postgres.New(pool))
	// A read-to-write candidate must not inherit the read's implicit exemption.
	candidate, err := s.Create(ctx, a, tool.ID, CandidateInput{ExpectedVersion: 1, Risk: core.RiskWrite, Reason: "Correct side-effect classification"})
	if err != nil {
		t.Fatal(err)
	}
	var policyChange bool
	for _, change := range candidate.Changes {
		if change.Field == "approval_policy" && change.After == core.ApprovalRequired {
			policyChange = true
		}
	}
	if !policyChange {
		t.Fatal("risk change omitted resulting policy")
	}
	tool, err = s.Publish(ctx, a, tool.ID, candidate.ID, 1)
	if err != nil || tool.ApprovalPolicy != core.ApprovalRequired {
		t.Fatal(tool, err)
	}
	tool, err = cs.UpdateToolApprovalPolicy(ctx, a, tool.ID, core.ApprovalPolicyUpdateInput{ExpectedVersion: tool.Version, ApprovalPolicy: core.ApprovalNone})
	if err != nil {
		t.Fatal(err)
	}
	candidate, err = s.Create(ctx, a, tool.ID, CandidateInput{ExpectedVersion: tool.Version, ResponsePolicy: &core.ResponsePolicy{MaxBytes: 4096}, Reason: "Response limit only"})
	if err != nil {
		t.Fatal(err)
	}
	tool, err = s.Publish(ctx, a, tool.ID, candidate.ID, tool.Version)
	if err != nil || tool.ApprovalPolicy != core.ApprovalNone {
		t.Fatal("unrelated update reset explicit policy", tool, err)
	}
	// Rolling back the definition must not roll back a newer security decision.
	tool, err = cs.UpdateToolApprovalPolicy(ctx, a, tool.ID, core.ApprovalPolicyUpdateInput{ExpectedVersion: tool.Version, ApprovalPolicy: core.ApprovalRequired})
	if err != nil {
		t.Fatal(err)
	}
	candidate, err = s.Create(ctx, a, tool.ID, CandidateInput{ExpectedVersion: tool.Version, SourceVersion: 3, Reason: "Restore earlier response definition"})
	if err != nil {
		t.Fatal(err)
	}
	tool, err = s.Publish(ctx, a, tool.ID, candidate.ID, tool.Version)
	if err != nil || tool.ApprovalPolicy != core.ApprovalRequired {
		t.Fatal("rollback restored obsolete exemption", tool, err)
	}
	versions, err := s.Versions(ctx, a, tool.ID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range versions.Items {
		want := core.ApprovalRequired
		if v.Version == 1 || v.Version == 3 || v.Version == 4 {
			want = core.ApprovalNone
		}
		if v.ApprovalPolicy != want {
			t.Fatal("history lost policy", v.Version, v.ApprovalPolicy, want)
		}
	}
	if revisionApprovalPolicy(tool, core.RiskRead) != core.ApprovalNone {
		t.Fatal("read defaults")
	}
}
