package mcpadapter

import (
	"context"
	"net/http"
	"time"

	"github.com/alan1-666/mcp-gateway/internal/core"
)

// The timer records disjoint phases and never accepts upstream text as a label.
// Reconnecting an expired session during discovery belongs to the catalog phase.
type executionTimer struct {
	started, phaseStarted time.Time
	observation           core.MCPObservation
}

func newExecutionTimer() *executionTimer {
	now := time.Now()
	return &executionTimer{started: now, phaseStarted: now, observation: core.MCPObservation{Code: "ok", Phase: "configuration"}}
}
func (t *executionTimer) phase(phase string) {
	elapsed := time.Since(t.phaseStarted).Milliseconds()
	switch t.observation.Phase {
	case "configuration":
		t.observation.PhasesMS.Configuration += elapsed
	case "session":
		t.observation.PhasesMS.Session += elapsed
	case "catalog":
		t.observation.PhasesMS.Catalog += elapsed
	case "call":
		t.observation.PhasesMS.Call += elapsed
	case "result":
		t.observation.PhasesMS.Result += elapsed
	}
	t.observation.Phase, t.phaseStarted = phase, time.Now()
}
func (t *executionTimer) finish(transport *protocolTransport) *core.MCPObservation {
	t.phase(t.observation.Phase)
	t.observation.TotalMS = time.Since(t.started).Milliseconds()
	if transport != nil {
		transport.mu.Lock()
		t.observation.CallAttempted = transport.callSent
		if transport.callSent {
			t.observation.HTTPStatus = transport.callStatus
		} else {
			t.observation.HTTPStatus = transport.lastStatus
		}
		transport.mu.Unlock()
	}
	return &t.observation
}

// Only context and HTTP status classify network failures. SDK/upstream messages
// are deliberately excluded: they may contain credentials or business payloads.
func observationFailure(ctx context.Context, transport *protocolTransport, fallback string) string {
	if ctx.Err() == context.DeadlineExceeded {
		return "timeout"
	}
	if ctx.Err() == context.Canceled {
		return "cancelled"
	}
	status := 0
	if transport != nil {
		transport.mu.Lock()
		status = transport.lastStatus
		if transport.callSent {
			status = transport.callStatus
		}
		transport.mu.Unlock()
	}
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return "authentication_rejected"
	case status == http.StatusTooManyRequests:
		return "rate_limited"
	case status >= 500 && status <= 599:
		return "upstream_unavailable"
	default:
		return fallback
	}
}
