// Package runs provides durable, leased cloud agent tasks over the governed
// operation ledger. It never performs model inference or stores model secrets.
package runs

import (
	"encoding/json"
	"time"
)

type State string

const (
	Queued              State = "QUEUED"
	Running             State = "RUNNING"
	WaitingApproval     State = "WAITING_APPROVAL"
	WaitingCredentials  State = "WAITING_CREDENTIALS"
	NeedsReview         State = "NEEDS_REVIEW"
	Succeeded           State = "SUCCEEDED"
	Failed              State = "FAILED"
	Cancelled           State = "CANCELLED"
	LeaseDuration             = 45 * time.Second
	AttemptDuration           = 5 * time.Minute
	MaxOutputBytes            = 64 << 10
	MaxEventBytes             = 16 << 10
	MaxEvents                 = 1000
	MaxStoredEventBytes       = 1 << 20
	MaxToolCalls              = 40
)

type Run struct {
	ID                 string    `json:"id"`
	WorkspaceID        string    `json:"workspace_id"`
	ActorID            string    `json:"actor_id"`
	Prompt             string    `json:"prompt"`
	State              State     `json:"state"`
	Attempt            int       `json:"attempt"`
	Output             string    `json:"output,omitempty"`
	ErrorCode          string    `json:"error_code,omitempty"`
	WaitingOperationID string    `json:"waiting_operation_id,omitempty"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}
type Event struct {
	ID        string          `json:"id"`
	RunID     string          `json:"run_id"`
	Type      string          `json:"type"`
	Data      json.RawMessage `json:"data"`
	CreatedAt time.Time       `json:"created_at"`
}
type CreateInput struct {
	Prompt         string `json:"prompt"`
	IdempotencyKey string `json:"idempotency_key"`
}
type Claim struct {
	Run            *Run       `json:"run"`
	LeaseToken     string     `json:"lease_token,omitempty"`
	LeaseExpiresAt *time.Time `json:"lease_expires_at,omitempty"`
}
type EventInput struct {
	EventKey string          `json:"event_key"`
	Type     string          `json:"type"`
	Data     json.RawMessage `json:"data"`
}
type FinishInput struct {
	State              State  `json:"state"`
	Output             string `json:"output"`
	ErrorCode          string `json:"error_code"`
	WaitingOperationID string `json:"waiting_operation_id"`
}
type StatusInput struct {
	WorkerID   string `json:"worker_id"`
	ModelReady bool   `json:"model_ready"`
	Provider   string `json:"provider"`
	ModelID    string `json:"model_id"`
	ErrorCode  string `json:"error_code"`
}
type Runtime struct {
	Online     bool       `json:"online"`
	ModelReady bool       `json:"model_ready"`
	Provider   string     `json:"provider"`
	ModelID    string     `json:"model_id"`
	ErrorCode  string     `json:"error_code"`
	LastSeenAt *time.Time `json:"last_seen_at,omitempty"`
}
