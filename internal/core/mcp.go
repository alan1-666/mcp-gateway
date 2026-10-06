package core

import (
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"
)

// MCPConfig pins an imported upstream contract. A schema change requires a new
// reviewed tool definition; discovery never mutates existing executable tools.
type MCPConfig struct {
	ServerID   string `json:"server_id"`
	ToolName   string `json:"tool_name"`
	SchemaHash string `json:"schema_hash"`
}

type MCPServerInput struct {
	Name          string `json:"name"`
	Namespace     string `json:"namespace"`
	URL           string `json:"url"`
	CredentialRef string `json:"credential_ref,omitempty"`
	ConnectorID   string `json:"connector_id,omitempty"`
	TargetName    string `json:"target_name,omitempty"`
	TimeoutMS     int    `json:"timeout_ms"`
}

type MCPServer struct {
	MCPServerInput
	ID          string    `json:"id"`
	WorkspaceID string    `json:"workspace_id"`
	Enabled     bool      `json:"enabled"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type RemoteTool struct {
	Name           string          `json:"name"`
	Description    string          `json:"description"`
	InputSchema    json.RawMessage `json:"input_schema"`
	OutputSchema   json.RawMessage `json:"output_schema,omitempty"`
	ReadOnlyHint   bool            `json:"read_only_hint"`
	SchemaHash     string          `json:"schema_hash"`
	GatewayName    string          `json:"gateway_name,omitempty"`
	ImportedToolID string          `json:"imported_tool_id,omitempty"`
}

// RemoteDescription uses the same bounded representation for import, refresh
// and comparison so whitespace/truncation cannot create permanent false drift.
func RemoteDescription(tool RemoteTool) string {
	description := strings.TrimSpace(tool.Description)
	if description == "" {
		description = "Remote MCP tool " + tool.Name
	}
	if len(description) > 4000 {
		description = description[:4000]
		for !utf8.ValidString(description) {
			description = description[:len(description)-1]
		}
	}
	return description
}
