// Package connectorwire defines the bounded outbound Connector work protocol.
package connectorwire

import (
	"encoding/json"
	"time"

	"github.com/alan1-666/mcp-gateway/internal/core"
)

type Target struct {
	Name        string `json:"name"`
	Fingerprint string `json:"fingerprint"`
	Transport   string `json:"transport"`
}
type PollInput struct {
	Targets []Target `json:"targets"`
	Ready   bool     `json:"ready"`
}
type Job struct {
	ID        string          `json:"id"`
	Kind      string          `json:"kind"`
	Target    Target          `json:"target"`
	Deadline  time.Time       `json:"deadline"`
	Server    core.MCPServer  `json:"server"`
	Tool      *core.Tool      `json:"tool,omitempty"`
	Operation *core.Operation `json:"operation,omitempty"`
}
type Result struct {
	Tools  []core.RemoteTool `json:"tools"`
	State  core.State        `json:"state,omitempty"`
	Result json.RawMessage   `json:"result,omitempty"`
	Error  string            `json:"error,omitempty"`
}
type Connector struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	WorkspaceID string     `json:"workspace_id"`
	Enabled     bool       `json:"enabled"`
	Targets     []Target   `json:"targets"`
	LastSeenAt  *time.Time `json:"last_seen_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
}
type Created struct {
	Connector Connector `json:"connector"`
	Token     string    `json:"token"`
}
