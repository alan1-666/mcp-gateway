package core

import (
	"context"
	"errors"
	"testing"
)

type approvalRepo struct {
	Repository
	calls int
}

func (r *approvalRepo) UpdateToolApprovalPolicy(_ context.Context, _ Actor, id string, in ApprovalPolicyUpdateInput) (Tool, error) {
	r.calls++
	return Tool{ID: id, ApprovalPolicy: in.ApprovalPolicy}, nil
}
func TestApprovalPolicyDefaultsAndValidation(t *testing.T) {
	for _, tc := range []struct {
		risk    Risk
		policy  ApprovalPolicy
		want    ApprovalPolicy
		invalid bool
	}{
		{RiskRead, "", ApprovalNone, false}, {RiskWrite, "", ApprovalRequired, false}, {RiskWrite, ApprovalNone, ApprovalNone, false}, {RiskRead, ApprovalRequired, ApprovalRequired, false}, {RiskWrite, "typo", "", true}, {"invalid", "", "", true},
	} {
		got, err := EffectiveApprovalPolicy(Tool{Risk: tc.risk, ApprovalPolicy: tc.policy})
		if got != tc.want || (err != nil) != tc.invalid {
			t.Fatalf("%+v got %s %v", tc, got, err)
		}
	}
}
func TestApprovalPolicyManagementAuthorization(t *testing.T) {
	ctx := context.Background()
	repo := &approvalRepo{}
	svc := NewService(repo)
	admin := Actor{ID: "a", WorkspaceID: "w", Role: RoleAdmin}
	in := ApprovalPolicyUpdateInput{ExpectedVersion: 1, ApprovalPolicy: ApprovalNone}
	for _, actor := range []Actor{{}, {ID: "u", WorkspaceID: "w", Role: RoleOperator}, {ID: "c", WorkspaceID: "w", Role: RoleAdmin, ClientID: "c"}} {
		if _, err := svc.UpdateToolApprovalPolicy(ctx, actor, "tool", in); err == nil {
			t.Fatal("unauthorized policy update")
		}
	}
	for _, bad := range []ApprovalPolicyUpdateInput{{}, {ExpectedVersion: 1}, {ExpectedVersion: 1, ApprovalPolicy: "unknown"}, {ExpectedVersion: 0, ApprovalPolicy: ApprovalNone}} {
		if _, err := svc.UpdateToolApprovalPolicy(ctx, admin, "tool", bad); !errors.Is(err, ErrInvalid) {
			t.Fatal(err)
		}
	}
	if _, err := svc.UpdateToolApprovalPolicy(ctx, admin, "", in); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if _, err := NewService(&policyReadRepository{}).UpdateToolApprovalPolicy(ctx, admin, "tool", in); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	got, err := svc.UpdateToolApprovalPolicy(ctx, admin, "tool", in)
	if err != nil || got.ApprovalPolicy != ApprovalNone || repo.calls != 1 {
		t.Fatal(got, err, repo.calls)
	}
}
func TestRequiresOperationApproval(t *testing.T) {
	none := Tool{Risk: RiskWrite, Version: 1, ApprovalPolicy: ApprovalNone}
	required := none
	required.ApprovalPolicy = ApprovalRequired
	newer := none
	newer.Version = 2
	invalid := none
	invalid.ApprovalPolicy = "invalid"
	for _, tc := range []struct {
		before, now   Tool
		want, invalid bool
	}{{none, none, false, false}, {none, required, true, false}, {required, none, true, false}, {none, newer, true, false}, {invalid, none, true, true}, {none, invalid, true, true}} {
		got, err := RequiresOperationApproval(tc.before, tc.now)
		if got != tc.want || (err != nil) != tc.invalid {
			t.Fatal(tc, got, err)
		}
	}
}
