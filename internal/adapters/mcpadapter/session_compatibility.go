package mcpadapter

import (
	"context"
	"errors"
	"net/http"

	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/alan1-666/mcp-gateway/internal/upstreams"
)

// checkSessionContracts compares two complete catalogs under the original
// check deadline. It observes contract stability, not business correctness or
// the cause of a change. No session ID, raw schema or upstream error is exposed.
func (a *Adapter) checkSessionContracts(ctx context.Context, actor core.Actor, server core.MCPServer, report upstreams.CheckReport, reviewed map[string]string) upstreams.CheckReport {
	report.SessionContractStatus = "unverified"
	session, transport, closeSession, err := a.connect(ctx, actor, server)
	if err != nil {
		return diagnosticFailure(ctx, transport, report, "compatibility", "session_verification_failed", "A fresh-session catalog could not be verified; reconnect compatibility is unknown.")
	}
	defer closeSession()
	tools, err := discover(ctx, session, transport)
	if err != nil {
		return diagnosticFailure(ctx, transport, report, "compatibility", "session_verification_failed", "A fresh-session catalog could not be verified; reconnect compatibility is unknown.")
	}
	live := make(map[string]string, len(tools))
	for _, tool := range tools {
		live[tool.Name] = tool.SchemaHash
	}
	changed := len(live) != len(reviewed)
	for i := range report.Tools {
		item := &report.Tools[i]
		if hash, exists := live[item.Name]; !exists || hash != reviewed[item.Name] {
			changed = true
			item.Status = "incompatible"
			item.Code = "session_contract_changed"
			item.Message = "This tool changed or disappeared in a fresh session; its reviewed contract cannot be reused safely."
			report.CompatibleCount--
			report.IncompatibleCount++
		}
	}
	if changed {
		report.Status, report.Stage, report.Code = "degraded", "compatibility", "session_catalog_changed"
		report.SessionContractStatus = "changed"
		report.Message = "The tool catalog changed between independent sessions; review upstream contract stability before publishing."
		return report
	}
	report.Status, report.Stage, report.Code = "ok", "complete", "catalog_compatible"
	report.SessionContractStatus = "stable"
	report.Message = "Connection and two independent catalogs passed; no business tool was executed."
	return report
}

// Fixed classifications prevent response bodies, endpoints and credentials
// embedded in SDK errors from reaching stored or displayed diagnostics.
func diagnosticFailure(ctx context.Context, transport *protocolTransport, report upstreams.CheckReport, stage, code, message string) upstreams.CheckReport {
	report.Status, report.Stage, report.Code, report.Message = "failed", stage, code, message
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		report.Code, report.Message = "check_timeout", "The connection check exceeded its deadline; contract compatibility remains unverified."
		return report
	}
	if ctx.Err() != nil {
		report.Code, report.Message = "check_cancelled", "The connection check was cancelled; contract compatibility remains unverified."
		return report
	}
	if transport == nil {
		return report
	}
	switch status := transport.status(); {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		report.Stage, report.Code, report.Message = "authentication", "authentication_rejected", "The upstream server rejected authentication."
	case status == http.StatusTooManyRequests:
		report.Code, report.Message = "upstream_rate_limited", "The upstream rate-limited the connection check; try again later."
	case status >= 500 && status <= 599:
		report.Code, report.Message = "upstream_unavailable", "The upstream was unavailable during the connection check; try again later."
	case status == http.StatusNotFound || status == http.StatusMethodNotAllowed:
		report.Code, report.Message = "endpoint_incompatible", "The endpoint did not accept the required MCP request; check its URL and transport."
	}
	return report
}
