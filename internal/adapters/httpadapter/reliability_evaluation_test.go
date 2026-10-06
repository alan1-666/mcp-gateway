package httpadapter

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alan1-666/mcp-gateway/internal/core"
)

// This fixed sequential workload reports measured local behavior, not a cloud
// service SLO. Fault injection is restricted to isolated HTTP test servers.
func TestHTTPReliabilityEvaluation(t *testing.T) {
	type measurement struct {
		Scenario         string  `json:"scenario"`
		Operations       int     `json:"operations"`
		UpstreamAttempts int     `json:"upstream_attempts"`
		Succeeded        int     `json:"succeeded"`
		Failed           int     `json:"failed"`
		Unknown          int     `json:"unknown"`
		CircuitRejected  int     `json:"circuit_rejected"`
		P50MS            float64 `json:"p50_ms"`
		P95MS            float64 `json:"p95_ms"`
		ErrorRate        float64 `json:"error_rate"`
	}
	measurements := make([]measurement, 0, 5)
	for _, scenario := range []string{"healthy_read", "transient_read", "unavailable_read", "unauthorized_read", "unavailable_write"} {
		m := measurement{Scenario: scenario, Operations: 20}
		attempts := map[string]int{}
		var mu sync.Mutex
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			attempts[r.Header.Get("Idempotency-Key")]++
			n := attempts[r.Header.Get("Idempotency-Key")]
			mu.Unlock()
			time.Sleep(2 * time.Millisecond)
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "0")
			switch scenario {
			case "transient_read":
				if n == 1 {
					w.WriteHeader(503)
				}
			case "unavailable_read", "unavailable_write":
				w.WriteHeader(503)
			case "unauthorized_read":
				w.WriteHeader(401)
			}
			io.WriteString(w, `{"status":"fixture"}`)
		}))
		a, err := New([]string{s.URL}, []string{"127.0.0.0/8"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		tool := core.Tool{Risk: core.RiskRead, HTTP: core.HTTPConfig{URL: s.URL, Method: "GET", TimeoutMS: 1000}}
		if scenario == "unavailable_write" {
			tool.Risk = core.RiskWrite
			tool.HTTP.Method = "POST"
		}
		latencies := make([]float64, 0, 20)
		for i := 0; i < m.Operations; i++ {
			started := time.Now()
			result := a.Execute(context.Background(), core.Actor{WorkspaceID: "evaluation"}, tool, core.Operation{ID: core.NewID(), Arguments: json.RawMessage(`{}`)})
			latencies = append(latencies, float64(time.Since(started).Microseconds())/1000)
			switch result.State {
			case core.StateSucceeded:
				m.Succeeded++
			case core.StateFailed:
				m.Failed++
			case core.StateUnknown:
				m.Unknown++
			default:
				t.Fatal("unexpected state", result.State)
			}
			if strings.Contains(result.Error, "circuit is open") {
				m.CircuitRejected++
			}
		}
		s.Close()
		mu.Lock()
		for _, n := range attempts {
			m.UpstreamAttempts += n
		}
		mu.Unlock()
		slices.Sort(latencies)
		m.P50MS = latencies[9]
		m.P95MS = latencies[18]
		m.ErrorRate = float64(m.Failed+m.Unknown) / float64(m.Operations)
		switch scenario {
		case "healthy_read":
			if m.Succeeded != 20 || m.UpstreamAttempts != 20 {
				t.Fatal(m)
			}
		case "transient_read":
			if m.Succeeded != 20 || m.UpstreamAttempts != 40 {
				t.Fatal(m)
			}
		case "unavailable_read":
			if m.Failed != 20 || m.CircuitRejected != 17 || m.UpstreamAttempts != 6 {
				t.Fatal(m)
			}
		case "unauthorized_read":
			if m.Failed != 20 || m.CircuitRejected != 0 || m.UpstreamAttempts != 20 {
				t.Fatal(m)
			}
		case "unavailable_write":
			if m.Unknown != 20 || m.UpstreamAttempts != 20 {
				t.Fatal(m)
			}
		}
		measurements = append(measurements, m)
	}
	report := map[string]any{"schema_version": 1, "go": runtime.Version(), "os": runtime.GOOS, "arch": runtime.GOARCH, "logical_cpus": runtime.NumCPU(), "fixture": "isolated loopback HTTP; 2ms injected server delay; Retry-After: 0; 20 sequential operations per scenario; 1s overall timeout", "measurements": measurements}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	t.Log("HTTP_RELIABILITY_EVALUATION", string(encoded))
}
