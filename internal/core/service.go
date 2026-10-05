package core

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type Service struct{ repo Repository }

func NewService(repo Repository) *Service { return &Service{repo: repo} }

func authorize(actor Actor, roles ...Role) error {
	if strings.TrimSpace(actor.ID) == "" || strings.TrimSpace(actor.WorkspaceID) == "" {
		return ErrUnauthorized
	}
	switch actor.Role {
	case RoleAdmin, RoleOperator, RoleApprover, RoleViewer:
	default:
		return ErrForbidden
	}
	if len(roles) == 0 {
		return nil
	}
	for _, role := range roles {
		if actor.Role == role {
			return nil
		}
	}
	return ErrForbidden
}
func CanReadOperation(actor Actor, op Operation) bool {
	return actor.WorkspaceID == op.WorkspaceID && (actor.ID == op.ActorID || actor.Role == RoleAdmin || actor.Role == RoleApprover)
}
func CanExecuteOperation(actor Actor, op Operation) bool {
	return actor.WorkspaceID == op.WorkspaceID && (actor.Role == RoleAdmin || (actor.Role == RoleOperator && actor.ID == op.ActorID))
}
func (s *Service) CreateTool(ctx context.Context, actor Actor, in ToolInput) (Tool, error) {
	if err := authorize(actor, RoleAdmin); err != nil {
		return Tool{}, err
	}
	input, err := NormalizeTool(in)
	if err != nil {
		return Tool{}, err
	}
	return s.repo.CreateTool(ctx, actor, input)
}
func (s *Service) ListTools(ctx context.Context, actor Actor) ([]Tool, error) {
	if err := authorize(actor); err != nil {
		return nil, err
	}
	return s.repo.ListTools(ctx, actor)
}
func (s *Service) GetTool(ctx context.Context, actor Actor, id string) (Tool, error) {
	if err := authorize(actor); err != nil {
		return Tool{}, err
	}
	return s.repo.GetTool(ctx, actor, id)
}
func (s *Service) PublishTool(ctx context.Context, actor Actor, id string) (Tool, error) {
	if err := authorize(actor, RoleAdmin); err != nil {
		return Tool{}, err
	}
	return s.repo.PublishTool(ctx, actor, id)
}
func (s *Service) SetToolEnabled(ctx context.Context, actor Actor, id string, enabled bool) (Tool, error) {
	if err := authorize(actor, RoleAdmin); err != nil {
		return Tool{}, err
	}
	return s.repo.SetToolEnabled(ctx, actor, id, enabled)
}
func (s *Service) Prepare(ctx context.Context, actor Actor, in PrepareInput) (Operation, error) {
	if err := authorize(actor, RoleAdmin, RoleOperator); err != nil {
		return Operation{}, err
	}
	if in.ToolID == "" || len(in.IdempotencyKey) < 8 || len(in.IdempotencyKey) > 128 || strings.TrimSpace(in.IdempotencyKey) != in.IdempotencyKey {
		return Operation{}, fmt.Errorf("%w: tool_id and an 8-128 byte idempotency_key are required", ErrInvalid)
	}
	canonical, _, err := CanonicalArguments(in.Arguments)
	if err != nil {
		return Operation{}, err
	}
	in.Arguments = canonical
	return s.repo.Prepare(ctx, actor, in)
}
func (s *Service) Approve(ctx context.Context, actor Actor, id string) (Operation, error) {
	if err := authorize(actor, RoleAdmin, RoleApprover); err != nil {
		return Operation{}, err
	}
	return s.repo.Approve(ctx, actor, id)
}
func (s *Service) Reject(ctx context.Context, actor Actor, id string) (Operation, error) {
	if err := authorize(actor, RoleAdmin, RoleApprover); err != nil {
		return Operation{}, err
	}
	return s.repo.Reject(ctx, actor, id)
}
func (s *Service) GetOperation(ctx context.Context, actor Actor, id string) (Operation, error) {
	if err := authorize(actor); err != nil {
		return Operation{}, err
	}
	return s.repo.GetOperation(ctx, actor, id)
}
func (s *Service) ListOperations(ctx context.Context, actor Actor) ([]Operation, error) {
	if err := authorize(actor); err != nil {
		return nil, err
	}
	return s.repo.ListOperations(ctx, actor)
}
func (s *Service) ListEvents(ctx context.Context, actor Actor, id string, after int64) ([]Event, error) {
	if err := authorize(actor); err != nil {
		return nil, err
	}
	if after < 0 {
		return nil, fmt.Errorf("%w: event cursor must be nonnegative", ErrInvalid)
	}
	return s.repo.ListEvents(ctx, actor, id, after)
}
func (s *Service) Claim(ctx context.Context, actor Actor, id string) (Operation, Tool, bool, error) {
	if err := authorize(actor, RoleAdmin, RoleOperator); err != nil {
		return Operation{}, Tool{}, false, err
	}
	return s.repo.Claim(ctx, actor, id)
}
func (s *Service) Finish(ctx context.Context, actor Actor, id string, in FinishInput) (Operation, error) {
	if err := authorize(actor, RoleAdmin, RoleOperator); err != nil {
		return Operation{}, err
	}
	if in.State != StateSucceeded && in.State != StateFailed && in.State != StateUnknown {
		return Operation{}, fmt.Errorf("%w: unsupported completion state", ErrInvalid)
	}
	if len(in.Result) > MaxResultBytes || (len(in.Result) > 0 && !json.Valid(in.Result)) || len(in.Error) > 2000 {
		return Operation{}, fmt.Errorf("%w: result or error exceeds allowed bounds", ErrInvalid)
	}
	if len(in.Result) > 0 {
		if _, err := DecodeResult(in.Result); err != nil {
			return Operation{}, err
		}
	}
	return s.repo.Finish(ctx, actor, id, in)
}

// Recover is an internal maintenance API; transport handlers must not expose it.
func (s *Service) Recover(ctx context.Context, olderThan time.Duration) (int, error) {
	if olderThan < 150*time.Second {
		return 0, fmt.Errorf("%w: recovery threshold must exceed tool deadlines (minimum 150s)", ErrInvalid)
	}
	return s.repo.Recover(ctx, olderThan)
}
