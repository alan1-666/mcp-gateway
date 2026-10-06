// Package upstreams manages reviewed remote MCP connections and tool imports.
package upstreams

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/alan1-666/mcp-gateway/internal/core"
)

const MaxServers = 100

var namespacePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,23}$`)
var credentialPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,127}$`)
var targetPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

type Remote interface {
	ValidateServer(core.Actor, core.MCPServer) error
	Discover(context.Context, core.Actor, core.MCPServer) ([]core.RemoteTool, error)
}
type Service struct {
	store  *Store
	remote Remote
}

func New(store *Store, remote Remote) *Service { return &Service{store: store, remote: remote} }

type ServerPage struct {
	Items []core.MCPServer `json:"items"`
	Total int              `json:"total"`
}
type DiscoveryPage struct {
	Items  []core.RemoteTool `json:"items"`
	Total  int               `json:"total"`
	Review CatalogReview     `json:"review"`
}
type ImportInput struct {
	ToolName       string               `json:"tool_name"`
	SchemaHash     string               `json:"schema_hash"`
	Risk           core.Risk            `json:"risk"`
	ResponsePolicy *core.ResponsePolicy `json:"response_policy,omitempty"`
}

func admin(actor core.Actor) error {
	if strings.TrimSpace(actor.ID) == "" || strings.TrimSpace(actor.WorkspaceID) == "" {
		return core.ErrUnauthorized
	}
	if actor.Role != core.RoleAdmin || actor.ClientID != "" {
		return core.ErrForbidden
	}
	return nil
}
func normalize(in core.MCPServerInput) (core.MCPServerInput, error) {
	in.Name = strings.TrimSpace(in.Name)
	in.Namespace = strings.TrimSpace(in.Namespace)
	in.URL = strings.TrimSpace(in.URL)
	in.ConnectorID = strings.TrimSpace(in.ConnectorID)
	in.TargetName = strings.TrimSpace(in.TargetName)
	if len(in.Name) < 1 || len(in.Name) > 120 || !utf8.ValidString(in.Name) || strings.ContainsAny(in.Name, "\x00\r\n") {
		return in, fmt.Errorf("%w: server name must contain 1-120 UTF-8 bytes", core.ErrInvalid)
	}
	if !namespacePattern.MatchString(in.Namespace) {
		return in, fmt.Errorf("%w: namespace must start with a lowercase letter and contain 1-24 lowercase letters, digits, underscores or hyphens", core.ErrInvalid)
	}
	if in.ConnectorID != "" {
		if len(in.ConnectorID) > 128 || !targetPattern.MatchString(in.TargetName) || in.URL != "" || in.CredentialRef != "" {
			return in, fmt.Errorf("%w: a Connector binding requires a target name and cannot include a URL or cloud credential", core.ErrInvalid)
		}
	} else {
		u, err := url.Parse(in.URL)
		if err != nil || len(in.URL) > 2048 || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.Fragment != "" || u.RawQuery != "" || u.ForceQuery || in.TargetName != "" {
			return in, fmt.Errorf("%w: server URL must be HTTP(S) without userinfo, query or fragment", core.ErrInvalid)
		}
	}
	if in.TimeoutMS == 0 {
		in.TimeoutMS = 10000
	}
	if in.TimeoutMS < 100 || in.TimeoutMS > 120000 {
		return in, fmt.Errorf("%w: timeout must be between 100 and 120000 ms", core.ErrInvalid)
	}
	if in.CredentialRef != "" && !credentialPattern.MatchString(in.CredentialRef) {
		return in, fmt.Errorf("%w: invalid credential reference", core.ErrInvalid)
	}
	return in, nil
}
func (s *Service) Create(ctx context.Context, actor core.Actor, in core.MCPServerInput) (core.MCPServer, error) {
	if err := admin(actor); err != nil {
		return core.MCPServer{}, err
	}
	normalized, err := normalize(in)
	if err != nil {
		return core.MCPServer{}, err
	}
	server := core.MCPServer{MCPServerInput: normalized, WorkspaceID: actor.WorkspaceID, Enabled: true}
	if err := s.remote.ValidateServer(actor, server); err != nil {
		return core.MCPServer{}, err
	}
	return s.store.create(ctx, actor, normalized)
}
func (s *Service) List(ctx context.Context, actor core.Actor) (ServerPage, error) {
	if err := admin(actor); err != nil {
		return ServerPage{}, err
	}
	items, err := s.store.list(ctx, actor.WorkspaceID)
	return ServerPage{Items: items, Total: len(items)}, err
}
func (s *Service) SetEnabled(ctx context.Context, actor core.Actor, id string, enabled bool) (core.MCPServer, error) {
	if err := admin(actor); err != nil {
		return core.MCPServer{}, err
	}
	return s.store.setEnabled(ctx, actor, id, enabled)
}
func (s *Service) Discover(ctx context.Context, actor core.Actor, id string) (DiscoveryPage, error) {
	if err := admin(actor); err != nil {
		return DiscoveryPage{}, err
	}
	server, err := s.store.GetServer(ctx, actor.WorkspaceID, id)
	if err != nil {
		return DiscoveryPage{}, err
	}
	if !server.Enabled {
		return DiscoveryPage{}, fmt.Errorf("%w: MCP server is disabled", core.ErrConflict)
	}
	started := time.Now().UTC()
	items, err := s.remote.Discover(ctx, actor, server)
	if err != nil {
		return DiscoveryPage{}, err
	}
	return s.store.reviewCatalog(ctx, actor, server, started, items)
}
func (s *Service) Import(ctx context.Context, actor core.Actor, id string, in ImportInput) (core.Tool, error) {
	if err := admin(actor); err != nil {
		return core.Tool{}, err
	}
	if in.ToolName == "" || len(in.ToolName) > 256 || len(in.SchemaHash) != 64 || (in.Risk != core.RiskRead && in.Risk != core.RiskWrite) {
		return core.Tool{}, fmt.Errorf("%w: tool_name, schema_hash and explicit risk are required", core.ErrInvalid)
	}
	if _, err := hex.DecodeString(in.SchemaHash); err != nil {
		return core.Tool{}, fmt.Errorf("%w: invalid schema_hash", core.ErrInvalid)
	}
	server, err := s.store.GetServer(ctx, actor.WorkspaceID, id)
	if err != nil {
		return core.Tool{}, err
	}
	if !server.Enabled {
		return core.Tool{}, fmt.Errorf("%w: MCP server is disabled", core.ErrConflict)
	}
	items, err := s.remote.Discover(ctx, actor, server)
	if err != nil {
		return core.Tool{}, err
	}
	for _, item := range items {
		if item.Name == in.ToolName {
			if item.SchemaHash != in.SchemaHash {
				return core.Tool{}, fmt.Errorf("%w: upstream schema changed; discover and review again", core.ErrConflict)
			}
			input, err := core.NormalizeTool(core.ToolInput{Name: GatewayName(server.Namespace, item.Name), Description: core.RemoteDescription(item), Risk: in.Risk, InputSchema: item.InputSchema, OutputSchema: item.OutputSchema, MCP: &core.MCPConfig{ServerID: id, ToolName: item.Name, SchemaHash: item.SchemaHash}, ResponsePolicy: in.ResponsePolicy})
			if err != nil {
				return core.Tool{}, err
			}
			return s.store.importTool(ctx, actor, input)
		}
	}
	return core.Tool{}, fmt.Errorf("%w: upstream tool no longer exists", core.ErrConflict)
}

// GatewayName preserves namespace identity and bounds aliases to 64 bytes. A
// hash of the full remote name prevents sanitization/truncation collisions.
func GatewayName(namespace, name string) string {
	var b strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	suffix := b.String()
	budget := 64 - len(namespace) - 2 - 9
	if len(suffix) > budget {
		suffix = suffix[:budget]
	}
	if suffix == "" {
		suffix = "tool"
	}
	digest := sha256.Sum256([]byte(name))
	return namespace + "__" + suffix + "_" + hex.EncodeToString(digest[:4])
}
