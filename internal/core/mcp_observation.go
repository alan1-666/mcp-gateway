package core

import "fmt"

// MCPObservation contains only gateway-owned, bounded telemetry. It is never
// decoded from an execution request or an upstream/Connector response.
type MCPObservation struct {
	Code          string            `json:"code"`
	Phase         string            `json:"phase"`
	TotalMS       int64             `json:"total_ms"`
	PhasesMS      MCPPhaseDurations `json:"phases_ms"`
	CallAttempted bool              `json:"call_attempted"`
	HTTPStatus    int               `json:"http_status,omitempty"`
}

type MCPPhaseDurations struct {
	Configuration int64 `json:"configuration"`
	Session       int64 `json:"session"`
	Catalog       int64 `json:"catalog"`
	Call          int64 `json:"call"`
	Result        int64 `json:"result"`
	Projection    int64 `json:"projection"`
}

func (o *MCPObservation) Validate() error {
	if o == nil {
		return nil
	}
	switch o.Code {
	case "ok", "configuration_unavailable", "connection_failed", "authentication_rejected", "rate_limited", "upstream_unavailable", "timeout", "cancelled", "catalog_unverified", "catalog_changed", "schema_changed", "arguments_invalid", "call_unconfirmed", "tool_error", "unsupported_interaction", "result_invalid", "projection_failed", "artifact_quota":
	default:
		return fmt.Errorf("%w: unsupported MCP observation code", ErrInvalid)
	}
	switch o.Phase {
	case "configuration", "session", "catalog", "call", "result", "projection":
	default:
		return fmt.Errorf("%w: unsupported MCP observation phase", ErrInvalid)
	}
	if o.HTTPStatus != 0 && (o.HTTPStatus < 100 || o.HTTPStatus > 599) {
		return fmt.Errorf("%w: invalid MCP observation status", ErrInvalid)
	}
	if o.TotalMS < 0 || o.TotalMS > 600000 {
		return fmt.Errorf("%w: invalid MCP observation duration", ErrInvalid)
	}
	sum := int64(0)
	for _, v := range []int64{o.PhasesMS.Configuration, o.PhasesMS.Session, o.PhasesMS.Catalog, o.PhasesMS.Call, o.PhasesMS.Result, o.PhasesMS.Projection} {
		if v < 0 || v > o.TotalMS {
			return fmt.Errorf("%w: invalid MCP phase duration", ErrInvalid)
		}
		sum += v
	}
	if sum > o.TotalMS {
		return fmt.Errorf("%w: MCP phase durations exceed total", ErrInvalid)
	}
	return nil
}
