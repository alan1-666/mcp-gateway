package mcpadapter

import (
	"context"
	"encoding/json"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/alan1-666/mcp-gateway/internal/upstreams"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Check performs initialization and complete bounded catalog inspection only.
// No tools/call occurs. Error messages are fixed classifications, never upstream
// error bodies, credential values or SDK error strings.
func (a *Adapter) Check(ctx context.Context, actor core.Actor, server core.MCPServer) (report upstreams.CheckReport) {
	start := time.Now()
	defer func() { report.DurationMS = time.Since(start).Milliseconds() }()
	report = upstreams.CheckReport{ServerID: server.ID, Status: "failed", SessionContractStatus: "not_checked", Tools: []upstreams.ToolCompatibility{}}
	fail := func(stage, code, message string) upstreams.CheckReport {
		report.Stage = stage
		report.Code = code
		report.Message = message
		return report
	}
	if !server.Enabled {
		return fail("policy", "server_disabled", "Enable the server before checking its connection.")
	}
	if server.ConnectorID != "" {
		items, err := a.Discover(ctx, actor, server)
		if err != nil {
			return fail("connector", "connector_unavailable", "Connector discovery failed; check its status, target configuration and catalog compatibility.")
		}
		for _, item := range items {
			report.Tools = append(report.Tools, upstreams.ToolCompatibility{Name: item.Name, Status: "compatible", Code: "supported", Message: "Definition is compatible; business execution has not been tested."})
		}
		report.CompatibleCount = len(items)
		report.Status, report.Stage, report.Code = "ok", "complete", "catalog_compatible"
		report.Message = "Connector connection and catalog checks passed; no business tool was executed."
		return report
	}
	ctx, cancel := context.WithTimeout(outboundContext{ctx}, time.Duration(server.TimeoutMS)*time.Millisecond)
	defer cancel()
	if a.ValidateServerContext(ctx, actor, server) != nil {
		if ctx.Err() != nil {
			return diagnosticFailure(ctx, nil, report, "policy", "configuration_blocked", "The endpoint, timeout or credential is blocked by the configured policy.")
		}
		return fail("policy", "configuration_blocked", "The endpoint, timeout or credential is blocked by the configured policy.")
	}
	session, transport, closeSession, err := a.connect(ctx, actor, server)
	if err != nil {
		return diagnosticFailure(ctx, transport, report, "connect", "connection_failed", "Connection or MCP initialization failed; inspect endpoint, network and protocol compatibility.")
	}
	defer func() {
		if closeSession != nil {
			closeSession()
		}
	}()
	cursor, totalBytes, count := "", 0, 0
	seenNames, seenCursors := map[string]bool{}, map[string]bool{}
	catalogVersion := transport.startCatalog()
	hashes := make(map[string]string)
	for page := 0; page < maxPages; page++ {
		if _, err = session.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor}); err != nil {
			return diagnosticFailure(ctx, transport, report, "discovery", "discovery_failed", "The upstream catalog could not be completely read.")
		}
		raw, err := transport.result("tools/list")
		if err != nil {
			return fail("discovery", "invalid_response", "The upstream catalog response is unsupported.")
		}
		if transport.catalogVersion() != catalogVersion {
			// Partial pages cannot be reported as a verified catalog.
			report.Tools = []upstreams.ToolCompatibility{}
			report.CompatibleCount, report.IncompatibleCount = 0, 0
			return fail("discovery", "catalog_changed", "The catalog changed while it was being read; run discovery again before review.")
		}
		totalBytes += len(raw)
		if totalBytes > maxCatalogBytes {
			return fail("discovery", "catalog_limit", "The upstream catalog exceeds the supported size.")
		}
		if _, err = core.DecodeResult(raw); err != nil {
			return fail("discovery", "invalid_response", "The upstream catalog contains unsupported JSON.")
		}
		var result struct {
			Tools []struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				Input       json.RawMessage `json:"inputSchema"`
				Output      json.RawMessage `json:"outputSchema"`
			} `json:"tools"`
			NextCursor string `json:"nextCursor"`
		}
		if json.Unmarshal(raw, &result) != nil || result.Tools == nil {
			return fail("discovery", "invalid_response", "The upstream catalog response is malformed.")
		}
		for _, tool := range result.Tools {
			count++
			if count > maxTools {
				return fail("discovery", "catalog_limit", "The upstream catalog exceeds the supported tool count.")
			}
			item := upstreams.ToolCompatibility{Name: tool.Name, Status: "compatible", Code: "supported", Message: "Definition is compatible; business execution has not been tested."}
			incompatible := func(code, message string) { item.Status = "incompatible"; item.Code = code; item.Message = message }
			if tool.Name == "" || len(tool.Name) > 128 || !utf8.ValidString(tool.Name) || strings.IndexFunc(tool.Name, unicode.IsControl) >= 0 {
				item.Name = "[invalid tool name]"
				incompatible("invalid_name", "Tool name is unsupported.")
			} else if seenNames[tool.Name] {
				incompatible("duplicate_name", "The catalog repeats this tool name.")
			} else if len(tool.Description) > 4000 {
				incompatible("description_limit", "Tool description exceeds the supported size.")
			} else if _, err := canonicalSchema(tool.Input, true); err != nil {
				incompatible("input_schema_unsupported", "Input schema is unsupported or exceeds the schema limit.")
			} else if len(tool.Output) > 0 {
				if _, err := canonicalSchema(tool.Output, false); err != nil {
					incompatible("output_schema_unsupported", "Output schema is unsupported or exceeds the schema limit.")
				}
			}
			seenNames[tool.Name] = true
			if item.Status == "compatible" {
				report.CompatibleCount++
				hashes[tool.Name] = schemaHash(tool.Name, tool.Input, tool.Output)
			} else {
				report.IncompatibleCount++
			}
			report.Tools = append(report.Tools, item)
		}
		if result.NextCursor == "" {
			if report.IncompatibleCount > 0 {
				report.Status = "degraded"
				report.Stage = "compatibility"
				report.Code = "unsupported_definitions"
				report.Message = "Discovery completed with incompatible definitions; strict import and execution remain blocked."
			} else {
				// Definitions are reviewed outside the execution session. A second
				// independent session detects changing contracts without dropping
				// session-bound const/default constraints from the schema hash.
				closeSession()
				closeSession = nil
				return a.checkSessionContracts(ctx, actor, server, report, hashes)
			}
			return report
		}
		if len(result.NextCursor) > 2048 || seenCursors[result.NextCursor] {
			return fail("discovery", "repeated_cursor", "The upstream catalog repeated a pagination cursor.")
		}
		seenCursors[result.NextCursor] = true
		cursor = result.NextCursor
	}
	return fail("discovery", "catalog_limit", "The upstream catalog exceeds the supported page count.")
}
