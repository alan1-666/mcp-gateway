package core

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

var (
	ErrUnauthorized    = errors.New("authentication required")
	ErrForbidden       = errors.New("permission denied")
	ErrNotFound        = errors.New("resource not found")
	ErrConflict        = errors.New("resource conflict")
	ErrInvalid         = errors.New("invalid input")
	ErrApprovalExpired = errors.New("approval expired")
)

type Role string

const (
	RoleAdmin    Role = "admin"
	RoleOperator Role = "operator"
	RoleApprover Role = "approver"
	RoleViewer   Role = "viewer"
)

type Actor struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	Role        Role   `json:"role"`
}
type Risk string

const (
	RiskRead  Risk = "read"
	RiskWrite Risk = "write"
)

type HTTPConfig struct {
	URL           string `json:"url"`
	Method        string `json:"method"`
	CredentialRef string `json:"credential_ref,omitempty"`
	TimeoutMS     int    `json:"timeout_ms"`
}
type ToolInput struct {
	Name           string          `json:"name"`
	Description    string          `json:"description"`
	Risk           Risk            `json:"risk"`
	InputSchema    json.RawMessage `json:"input_schema"`
	OutputSchema   json.RawMessage `json:"output_schema,omitempty"`
	HTTP           HTTPConfig      `json:"http"`
	MCP            *MCPConfig      `json:"mcp,omitempty"`
	ResponsePolicy *ResponsePolicy `json:"response_policy,omitempty"`
}
type Tool struct {
	ID             string          `json:"id"`
	WorkspaceID    string          `json:"workspace_id"`
	Name           string          `json:"name"`
	Description    string          `json:"description"`
	Risk           Risk            `json:"risk"`
	InputSchema    json.RawMessage `json:"input_schema"`
	OutputSchema   json.RawMessage `json:"output_schema,omitempty"`
	HTTP           HTTPConfig      `json:"http"`
	MCP            *MCPConfig      `json:"mcp,omitempty"`
	ResponsePolicy *ResponsePolicy `json:"response_policy,omitempty"`
	Enabled        bool            `json:"enabled"`
	Status         string          `json:"status"`
	Version        int             `json:"version"`
	CreatedAt      time.Time       `json:"created_at"`
}
type State string

const (
	StateWaitingApproval State = "WAITING_APPROVAL"
	StateReady           State = "READY"
	StateDispatching     State = "DISPATCHING"
	StateSucceeded       State = "SUCCEEDED"
	StateFailed          State = "FAILED"
	StateUnknown         State = "UNKNOWN"
	StateRejected        State = "REJECTED"
)

type PrepareInput struct {
	ToolID         string          `json:"tool_id"`
	Arguments      json.RawMessage `json:"arguments"`
	IdempotencyKey string          `json:"idempotency_key"`
}
type Operation struct {
	ID                string          `json:"id"`
	WorkspaceID       string          `json:"workspace_id"`
	ToolID            string          `json:"tool_id"`
	ToolName          string          `json:"tool_name"`
	ToolVersion       int             `json:"tool_version"`
	Risk              Risk            `json:"risk"`
	ActorID           string          `json:"actor_id"`
	Arguments         json.RawMessage `json:"arguments"`
	ArgumentsHash     string          `json:"arguments_hash"`
	IdempotencyKey    string          `json:"idempotency_key"`
	State             State           `json:"state"`
	Result            json.RawMessage `json:"result,omitempty"`
	Error             string          `json:"error,omitempty"`
	ApprovedBy        string          `json:"approved_by,omitempty"`
	ApprovalExpiresAt *time.Time      `json:"approval_expires_at,omitempty"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
}
type FinishInput struct {
	State  State
	Result json.RawMessage
	Error  string
}
type Event struct {
	ID          int64           `json:"id"`
	OperationID string          `json:"operation_id"`
	Type        string          `json:"type"`
	ActorID     string          `json:"actor_id"`
	CreatedAt   time.Time       `json:"created_at"`
	Data        json.RawMessage `json:"data"`
}

// Repository methods receive a trusted actor. Implementations must scope every
// resource lookup to WorkspaceID and recheck ownership inside write transactions.
type Repository interface {
	CreateTool(context.Context, Actor, ToolInput) (Tool, error)
	ListTools(context.Context, Actor) ([]Tool, error)
	SearchTools(context.Context, Actor, ToolSearchInput, ToolVisibilityScope) (ToolPage, error)
	GetTool(context.Context, Actor, string) (Tool, error)
	PublishTool(context.Context, Actor, string) (Tool, error)
	SetToolEnabled(context.Context, Actor, string, bool) (Tool, error)
	UpdateToolResponsePolicy(context.Context, Actor, string, ResponsePolicyUpdateInput) (Tool, error)
	Prepare(context.Context, Actor, PrepareInput) (Operation, error)
	Approve(context.Context, Actor, string) (Operation, error)
	Reject(context.Context, Actor, string) (Operation, error)
	GetOperation(context.Context, Actor, string) (Operation, error)
	ListOperations(context.Context, Actor) ([]Operation, error)
	ListEvents(context.Context, Actor, string, int64) ([]Event, error)
	Claim(context.Context, Actor, string) (Operation, Tool, bool, error)
	Finish(context.Context, Actor, string, FinishInput) (Operation, error)
	Recover(context.Context, time.Duration) (int, error)
}
