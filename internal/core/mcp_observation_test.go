package core

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestMCPObservationBoundariesAndWireIsolation(t *testing.T) {
	var nilObservation *MCPObservation
	if nilObservation.Validate() != nil {
		t.Fatal("legacy completion rejected")
	}
	good := MCPObservation{Code: "ok", Phase: "result", TotalMS: 100, PhasesMS: MCPPhaseDurations{Configuration: 1, Session: 10, Catalog: 20, Call: 50, Result: 10, Projection: 9}, CallAttempted: true, HTTPStatus: 200}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, code := range []string{"configuration_unavailable", "connection_failed", "authentication_rejected", "rate_limited", "upstream_unavailable", "timeout", "cancelled", "catalog_unverified", "catalog_changed", "schema_changed", "arguments_invalid", "call_unconfirmed", "tool_error", "unsupported_interaction", "result_invalid", "projection_failed", "artifact_quota"} {
		o := good
		o.Code = code
		if o.Validate() != nil {
			t.Fatal(code)
		}
	}
	for _, phase := range []string{"configuration", "session", "catalog", "call", "result", "projection"} {
		o := good
		o.Phase = phase
		if o.Validate() != nil {
			t.Fatal(phase)
		}
	}
	for _, mutate := range []func(*MCPObservation){
		func(o *MCPObservation) { o.Code = "secret-provider-error" }, func(o *MCPObservation) { o.Phase = "user-controlled" },
		func(o *MCPObservation) { o.HTTPStatus = 99 }, func(o *MCPObservation) { o.HTTPStatus = 600 },
		func(o *MCPObservation) { o.TotalMS = -1 }, func(o *MCPObservation) { o.TotalMS = 600001 },
		func(o *MCPObservation) { o.PhasesMS.Call = -1 }, func(o *MCPObservation) { o.PhasesMS.Call = 101 }, func(o *MCPObservation) { o.PhasesMS.Call = 100 },
	} {
		o := good
		mutate(&o)
		if !errors.Is(o.Validate(), ErrInvalid) {
			t.Fatalf("accepted %+v", o)
		}
	}
	var in FinishInput
	if err := json.Unmarshal([]byte(`{"state":"SUCCEEDED","MCPObservation":{"code":"ok"},"mcp_observation":{"code":"ok"}}`), &in); err != nil || in.MCPObservation != nil {
		t.Fatal("wire telemetry was trusted")
	}
	raw, _ := json.Marshal(FinishInput{State: StateSucceeded, MCPObservation: &good})
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	if _, ok := fields["MCPObservation"]; ok {
		t.Fatal("internal telemetry serialized on completion wire")
	}
}
