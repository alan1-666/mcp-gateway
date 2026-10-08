package integration

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/alan1-666/mcp-gateway/internal/adapters/httpadapter"
	"github.com/alan1-666/mcp-gateway/internal/adapters/mcpadapter"
	"github.com/alan1-666/mcp-gateway/internal/core"
)

// Opt-in named public-provider checks. Only initialization and complete catalog
// inspection are sent; no credentials, account or cloud allowlist are changed.
func TestExternalPublicMCPCompatibility(t *testing.T) {
	if os.Getenv("RUN_EXTERNAL_MCP_COMPATIBILITY") != "1" {
		t.Skip("set RUN_EXTERNAL_MCP_COMPATIBILITY=1 for named public MCP checks")
	}
	evidence := map[string]any{
		"verified_at":                      time.Now().UTC().Format(time.RFC3339),
		"environment":                      "local adapter with fixed public endpoint allowlists",
		"production_configuration_changed": false,
		"business_tools_called":            false,
		"checks":                           []string{"initialization", "complete_catalog", "fresh_session_catalog", "contract_comparison"},
		"limitations":                      []string{"two observed sessions do not guarantee future contract stability", "no real vendor OAuth or business writes", "public provider availability is not a CI gate"},
	}
	providers := map[string]any{}
	for _, provider := range []struct{ name, origin, endpoint, reference string }{
		{"microsoft_learn", "https://learn.microsoft.com", "https://learn.microsoft.com/api/mcp", "https://learn.microsoft.com/en-us/training/support/mcp"},
		{"cloudflare_docs", "https://docs.mcp.cloudflare.com", "https://docs.mcp.cloudflare.com/mcp", "https://developers.cloudflare.com/agents/model-context-protocol/cloudflare/servers-for-cloudflare/"},
	} {
		t.Run(provider.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 65*time.Second)
			defer cancel()
			egress, err := httpadapter.New([]string{provider.origin}, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			server := core.MCPServer{ID: provider.name, WorkspaceID: "public-compatibility", Enabled: true, MCPServerInput: core.MCPServerInput{Name: provider.name, Namespace: provider.name, URL: provider.endpoint, TimeoutMS: 60000}}
			report := mcpadapter.New(egress, nil).Check(ctx, core.Actor{WorkspaceID: server.WorkspaceID}, server)
			providers[provider.name] = map[string]any{"endpoint": provider.endpoint, "official_reference": provider.reference, "authentication": "public; no account", "report": report}
			if report.Status == "failed" || (report.SessionContractStatus != "stable" && report.SessionContractStatus != "changed") || len(report.Tools) == 0 {
				t.Fatalf("public compatibility was not observed: status=%s code=%s session=%s", report.Status, report.Code, report.SessionContractStatus)
			}
			if report.SessionContractStatus == "changed" && report.Code != "session_catalog_changed" {
				t.Fatal("changed contracts not explained")
			}
			t.Logf("PUBLIC_COMPATIBILITY provider=%s status=%s session=%s compatible=%d incompatible=%d duration_ms=%d", provider.name, report.Status, report.SessionContractStatus, report.CompatibleCount, report.IncompatibleCount, report.DurationMS)
		})
	}
	evidence["providers"] = providers
	if path := os.Getenv("EXTERNAL_COMPATIBILITY_EVIDENCE"); path != "" {
		raw, err := json.MarshalIndent(evidence, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, append(raw, '\n'), 0644); err != nil {
			t.Fatal(err)
		}
	}
}
