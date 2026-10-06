package core

import (
	"context"
	"fmt"
)

// ApprovalPolicy is managed only through the administrator policy endpoint.
// It never changes the tool's side-effect classification.
type ApprovalPolicy string

const (
	ApprovalRequired ApprovalPolicy = "required"
	ApprovalNone     ApprovalPolicy = "none"
)

type ApprovalPolicyUpdateInput struct {
	ExpectedVersion int            `json:"expected_version"`
	ApprovalPolicy  ApprovalPolicy `json:"approval_policy"`
}

type approvalPolicyRepository interface {
	UpdateToolApprovalPolicy(context.Context, Actor, string, ApprovalPolicyUpdateInput) (Tool, error)
}

// EffectiveApprovalPolicy retains conservative defaults for legacy definitions
// and immutable operation snapshots. Unknown nonempty values never allow calls.
func EffectiveApprovalPolicy(tool Tool) (ApprovalPolicy, error) {
	switch tool.ApprovalPolicy {
	case ApprovalRequired, ApprovalNone:
		return tool.ApprovalPolicy, nil
	case "":
		switch tool.Risk {
		case RiskRead:
			return ApprovalNone, nil
		case RiskWrite:
			return ApprovalRequired, nil
		}
	}
	return "", fmt.Errorf("%w: invalid tool approval policy", ErrInvalid)
}

func (s *Service) UpdateToolApprovalPolicy(ctx context.Context, actor Actor, id string, in ApprovalPolicyUpdateInput) (Tool, error) {
	if err := authorize(actor, RoleAdmin); err != nil {
		return Tool{}, err
	}
	if id == "" || in.ExpectedVersion < 1 || (in.ApprovalPolicy != ApprovalRequired && in.ApprovalPolicy != ApprovalNone) {
		return Tool{}, fmt.Errorf("%w: tool_id, expected_version and explicit approval_policy are required", ErrInvalid)
	}
	repo, ok := s.repo.(approvalPolicyRepository)
	if !ok {
		return Tool{}, fmt.Errorf("%w: approval policy management is unavailable", ErrConflict)
	}
	return repo.UpdateToolApprovalPolicy(ctx, actor, id, in)
}

// RequiresOperationApproval checks both the immutable intent and latest policy.
// Version pinning invalidates stale write exemptions, including tighten/loosen races.
func RequiresOperationApproval(snapshot, current Tool) (bool, error) {
	before, err := EffectiveApprovalPolicy(snapshot)
	if err != nil {
		return true, err
	}
	now, err := EffectiveApprovalPolicy(current)
	if err != nil {
		return true, err
	}
	return before == ApprovalRequired || now == ApprovalRequired || (snapshot.Risk == RiskWrite && snapshot.Version != current.Version), nil
}
