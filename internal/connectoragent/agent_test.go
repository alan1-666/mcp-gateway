package connectoragent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"

	"fmt"
	"io"

	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alan1-666/mcp-gateway/internal/connectorwire"
	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func privateTemp(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}
func testConfig(t *testing.T, gateway string, targets ...TargetConfig) Config {
	t.Helper()
	dir := privateTemp(t)
	token := filepath.Join(dir, "token")
	if err := os.WriteFile(token, []byte(strings.Repeat("t", 32)), 0600); err != nil {
		t.Fatal(err)
	}
	return Config{GatewayURL: gateway, TokenFile: token, StateDir: filepath.Join(dir, "state"), Targets: targets}
}
func stdioTarget() TargetConfig {
	return TargetConfig{Name: "fixture", Transport: "stdio", Image: "localhost/fixture@sha256:" + strings.Repeat("a", 64)}
}
func testJob(target TargetConfig) connectorwire.Job {
	return connectorwire.Job{ID: "job-1", Kind: "discover", Target: target.Wire(), Deadline: time.Now().Add(15 * time.Second), Server: core.MCPServer{ID: "server-1", WorkspaceID: "workspace-1", Enabled: true, MCPServerInput: core.MCPServerInput{Name: "Fixture", Namespace: "fixture", URL: "https://untrusted.invalid/mcp", TimeoutMS: 10000}}}
}

type fixtureLauncher struct {
	mode     string
	launches atomic.Int32
	waited   atomic.Int32
	cleaned  atomic.Int32
}

func (f *fixtureLauncher) Launch(ctx context.Context, _ TargetConfig, _ string) (runningProcess, error) {
	f.launches.Add(1)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestStdioHelperProcess")
	cmd.Env = append(os.Environ(), "CONNECTOR_HELPER_MODE="+f.mode)
	cmd.Stderr = io.Discard
	cmd.WaitDelay = time.Second
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return runningProcess{}, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return runningProcess{}, err
	}
	if err := cmd.Start(); err != nil {
		return runningProcess{}, err
	}
	return runningProcess{stdin: stdin, stdout: stdout, stop: func() { _ = cmd.Process.Kill() }, wait: func() error { defer f.waited.Add(1); return cmd.Wait() }}, nil
}
func (f *fixtureLauncher) Cleanup(context.Context, string) error { f.cleaned.Add(1); return nil }

// This fixture uses real newline-delimited stdin/stdout; only tests inject its
// host launcher. Production always starts the isolated, digest-pinned image.
func TestStdioHelperProcess(t *testing.T) {
	mode := os.Getenv("CONNECTOR_HELPER_MODE")
	if mode == "" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), maxLineBytes+1)
	for scanner.Scan() {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				ProtocolVersion string `json:"protocolVersion"`
			} `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			os.Exit(2)
		}
		if len(request.ID) == 0 {
			continue
		}
		if mode == "hang" {
			time.Sleep(time.Hour)
		}
		if mode == "oversize" {
			fmt.Fprintln(os.Stdout, strings.Repeat("x", maxLineBytes+2))
			continue
		}
		var result string
		switch request.Method {
		case "initialize":
			result = fmt.Sprintf(`{"protocolVersion":%q,"capabilities":{"tools":{}},"serverInfo":{"name":"fixture","version":"1"}}`, request.Params.ProtocolVersion)
		case "tools/list":
			result = `{"tools":[{"name":"echo","description":"Fixture echo","inputSchema":{"type":"object","properties":{"value":{"type":"integer"}},"required":["value"],"additionalProperties":false},"outputSchema":{"type":"object","properties":{"value":{"type":"integer"}},"required":["value"]},"annotations":{"readOnlyHint":true}}]}`
		case "tools/call":
			result = `{"content":[{"type":"text","text":"fixture"}],"structuredContent":{"value":9007199254740993}}`
		default:
			fmt.Fprintf(os.Stdout, "{\"jsonrpc\":\"2.0\",\"id\":%s,\"error\":{\"code\":-32601,\"message\":\"method not found\"}}\n", request.ID)
			continue
		}
		fmt.Fprintf(os.Stdout, "{\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":%s}\n", request.ID, result)
	}
	os.Exit(0)
}
func executionJob(discovery connectorwire.Job, tool core.RemoteTool) connectorwire.Job {
	job := discovery
	job.ID = "job-call"
	job.Kind = "execute"
	job.Tool = &core.Tool{ID: "tool-1", WorkspaceID: job.Server.WorkspaceID, Name: "fixture.echo", Version: 1, Enabled: true, Risk: core.RiskRead, InputSchema: tool.InputSchema, OutputSchema: tool.OutputSchema, MCP: &core.MCPConfig{ServerID: job.Server.ID, ToolName: tool.Name, SchemaHash: tool.SchemaHash}}
	job.Operation = &core.Operation{ID: "op-1", WorkspaceID: job.Server.WorkspaceID, ToolID: job.Tool.ID, ToolVersion: 1, Risk: core.RiskRead, State: core.StateDispatching, Arguments: json.RawMessage(`{"value":9007199254740993}`)}
	_, job.Operation.ArgumentsHash, _ = core.CanonicalArguments(job.Operation.Arguments)
	return job
}
func TestStdioUsesReviewedContractAndPreservesNumbers(t *testing.T) {
	target := stdioTarget()
	l := &fixtureLauncher{mode: "normal"}
	a := &Agent{launcher: l}
	job := testJob(target)
	discovered := a.execute(context.Background(), job, target)
	if discovered.State != core.StateSucceeded || len(discovered.Tools) != 1 {
		t.Fatalf("discovery: %+v", discovered)
	}
	call := executionJob(job, discovered.Tools[0])
	result := a.execute(context.Background(), call, target)
	if result.State != core.StateSucceeded || !strings.Contains(string(result.Result), "9007199254740993") {
		t.Fatalf("result: %+v", result)
	}
	call.Tool.MCP.SchemaHash = strings.Repeat("0", 64)
	result = a.execute(context.Background(), call, target)
	if result.State != core.StateFailed || !strings.Contains(result.Error, "schema changed") {
		t.Fatalf("schema drift allowed: %+v", result)
	}
	if l.launches.Load() != 3 || l.waited.Load() != 3 || l.cleaned.Load() != 3 {
		t.Fatalf("unreaped process: %d/%d/%d", l.launches.Load(), l.waited.Load(), l.cleaned.Load())
	}
}
func TestStdioBoundsOutputAndCancelsProcesses(t *testing.T) {
	for _, mode := range []string{"oversize", "hang"} {
		t.Run(mode, func(t *testing.T) {
			l := &fixtureLauncher{mode: mode}
			target := stdioTarget()
			job := testJob(target)
			job.Server.TimeoutMS = 150
			started := time.Now()
			result := (&Agent{launcher: l}).execute(context.Background(), job, target)
			if result.State != core.StateFailed || time.Since(started) > 3*time.Second || l.waited.Load() != 1 || l.cleaned.Load() != 1 {
				t.Fatalf("unbounded execution: %+v waited=%d cleaned=%d", result, l.waited.Load(), l.cleaned.Load())
			}
		})
	}
}
func TestOutboundLifecycleAndNoReplayAfterRestart(t *testing.T) {
	target := stdioTarget()
	job := testJob(target)
	var starts, reports, heartbeats, ready atomic.Int32
	resultDone := make(chan struct{}, 1)
	gateway := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("t", 32) {
			t.Error("missing scoped Connector authentication")
		}
		switch r.URL.Path {
		case "/connector/v1/poll":
			var in connectorwire.PollInput
			_ = json.NewDecoder(r.Body).Decode(&in)
			if len(in.Targets) != 1 || in.Targets[0] != target.Wire() {
				t.Error("target fingerprint missing")
			}
			if !in.Ready {
				heartbeats.Add(1)
				w.WriteHeader(204)
				return
			}
			if heartbeats.Load() == 0 {
				t.Error("ready before registration")
			}
			if ready.Add(1) == 1 {
				_ = json.NewEncoder(w).Encode(job)
			} else {
				w.WriteHeader(204)
			}
		case "/connector/v1/jobs/job-1/start":
			starts.Add(1)
			w.WriteHeader(204)
		case "/connector/v1/jobs/job-1/result":
			var result connectorwire.Result
			_ = json.NewDecoder(r.Body).Decode(&result)
			if result.State != core.StateSucceeded || len(result.Tools) != 1 {
				t.Errorf("invalid completion: %+v", result)
			}
			if reports.Add(1) == 1 {
				w.WriteHeader(503)
				return
			}
			w.WriteHeader(204)
			select {
			case resultDone <- struct{}{}:
			default:
			}
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer gateway.Close()
	config := testConfig(t, gateway.URL, target)
	l := &fixtureLauncher{mode: "normal"}
	a, err := newAgent(context.Background(), config, gateway.Client(), l)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	select {
	case <-resultDone:
	case <-ctx.Done():
		t.Fatal("result timeout")
	}
	cancel()
	<-done
	if err := a.handle(context.Background(), job); err == nil {
		t.Fatal("duplicate job accepted")
	}
	a.Close()
	restarted, err := newAgent(context.Background(), config, gateway.Client(), l)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if err := restarted.handle(context.Background(), job); err == nil {
		t.Fatal("job replayed after restart")
	}
	if starts.Load() != 1 || reports.Load() != 2 || l.launches.Load() != 1 || l.cleaned.Load() < 2 {
		t.Fatalf("unexpected lifecycle: start=%d report=%d launch=%d cleanup=%d", starts.Load(), reports.Load(), l.launches.Load(), l.cleaned.Load())
	}
}
func TestStartFailureAndRevocationNeverExecute(t *testing.T) {
	for _, code := range []int{401, 409, 503} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			var starts atomic.Int32
			gateway := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { starts.Add(1); w.WriteHeader(code) }))
			defer gateway.Close()
			target := stdioTarget()
			config := testConfig(t, gateway.URL, target)
			l := &fixtureLauncher{mode: "normal"}
			a, err := newAgent(context.Background(), config, gateway.Client(), l)
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			job := testJob(target)
			if a.handle(context.Background(), job) == nil {
				t.Fatal("failed authorization accepted")
			}
			if a.handle(context.Background(), job) == nil {
				t.Fatal("failed authorization replayed")
			}
			if starts.Load() != 1 || l.launches.Load() != 0 {
				t.Fatal("execution or start retried")
			}
		})
	}
}
func TestTargetFingerprintAndDeadlineFence(t *testing.T) {
	target := stdioTarget()
	a := &Agent{targets: map[string]TargetConfig{target.Name: target}}
	for _, mutate := range []func(*connectorwire.Job){func(j *connectorwire.Job) { j.Target.Fingerprint = "changed" }, func(j *connectorwire.Job) { j.Deadline = time.Now().Add(-time.Second) }, func(j *connectorwire.Job) { j.Deadline = time.Now().Add(time.Hour) }, func(j *connectorwire.Job) { j.ID = "../start" }, func(j *connectorwire.Job) { j.Server.Enabled = false }} {
		job := testJob(target)
		mutate(&job)
		if _, err := a.validateJob(job); err == nil {
			t.Fatal("unsafe job accepted")
		}
	}
}
func TestCloudRedirectDoesNotLeakToken(t *testing.T) {
	var followed atomic.Int32
	destination := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { followed.Add(1) }))
	defer destination.Close()
	source := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination.URL, 307) }))
	defer source.Close()
	config := testConfig(t, source.URL, TargetConfig{Name: "local", Transport: "http", URL: "http://127.0.0.1/mcp", AllowedCIDRs: []string{"127.0.0.1/32"}})
	a, err := newAgent(context.Background(), config, source.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if _, err := a.poll(context.Background(), false); err == nil || followed.Load() != 0 {
		t.Fatal("cloud redirect accepted")
	}
}
func TestActualRootlessPodman(t *testing.T) {
	if os.Getenv("RUN_CONNECTOR_PODMAN_INTEGRATION") != "1" {
		t.Skip("requires isolated Linux rootless Podman and CONNECTOR_FIXTURE_IMAGE")
	}
	target := stdioTarget()
	target.Image = os.Getenv("CONNECTOR_FIXTURE_IMAGE")
	if !imageDigest.MatchString(target.Image) {
		t.Fatal("CONNECTOR_FIXTURE_IMAGE must be preloaded, digest pinned fixture")
	}
	l, err := newPodman(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	a := &Agent{launcher: l}
	job := testJob(target)
	job.ID = fmt.Sprintf("podman-%d", time.Now().UnixNano())
	assertActualCgroupLimits(t, l, target, job.ID+"-limits")
	result := a.execute(context.Background(), job, target)
	if result.State != core.StateSucceeded || len(result.Tools) != 3 {
		t.Fatalf("actual container discovery failed: %+v", result)
	}
	var echo, slow core.RemoteTool
	for _, tool := range result.Tools {
		if tool.Name == "echo" {
			echo = tool
		}
		if tool.Name == "slow" {
			slow = tool
		}
	}
	call := executionJob(job, echo)
	call.Operation.Arguments = json.RawMessage(`{}`)
	_, call.Operation.ArgumentsHash, _ = core.CanonicalArguments(call.Operation.Arguments)
	call.ID = job.ID + "-call"
	result = a.execute(context.Background(), call, target)
	if result.State != core.StateSucceeded || !strings.Contains(string(result.Result), "9007199254740993") {
		t.Fatalf("actual container execution failed: %+v", result)
	}
	slowJob := executionJob(job, slow)
	slowJob.ID = job.ID + "-slow"
	slowJob.Operation.Arguments = json.RawMessage(`{}`)
	slowJob.Server.TimeoutMS = 1500
	started := time.Now()
	slowResult := a.execute(context.Background(), slowJob, target)
	if slowResult.State != core.StateFailed || time.Since(started) > 8*time.Second {
		t.Fatalf("actual timeout did not bound execution: %+v", slowResult)
	}
	// A pre-cancelled operation must neither launch nor leave a container.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result = a.execute(ctx, job, target)
	if result.State != core.StateFailed {
		t.Fatal("cancelled container started")
	}
	for _, id := range []string{job.ID, call.ID, slowJob.ID, job.ID + "-limits"} {
		cmd := exec.Command(l.path, "--remote=false", "container", "exists", containerName(id))
		if cmd.Run() == nil {
			t.Fatal("container was not removed")
		}
	}
}

func assertActualCgroupLimits(t *testing.T, l *podmanLauncher, target TargetConfig, id string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	transport, err := newStdioTransport(ctx, l, target, containerName(id))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		transport.Close()
		if transport.cleanupErr != nil {
			t.Errorf("limit fixture cleanup: %v", transport.cleanupErr)
		}
	}()
	inspectCtx, stopInspect := context.WithTimeout(ctx, 5*time.Second)
	defer stopInspect()
	pid := 0
	for inspectCtx.Err() == nil {
		command := exec.CommandContext(inspectCtx, l.path, "--remote=false", "inspect", "--format", "{{.State.Pid}}", containerName(id))
		var output limitedOutput
		command.Stdout = &output
		command.Stderr = io.Discard
		if command.Run() == nil {
			pid, _ = strconv.Atoi(strings.TrimSpace(output.String()))
			if pid > 0 {
				break
			}
		}
		if !delay(inspectCtx, 50*time.Millisecond) {
			break
		}
	}
	if pid <= 0 {
		t.Fatal("could not inspect the live isolated fixture within five seconds")
	}
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", pid))
	if err != nil {
		t.Fatal(err)
	}
	cgroupPath := ""
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "0::/") {
			cgroupPath = strings.TrimPrefix(line, "0::")
			break
		}
	}
	if cgroupPath == "" || filepath.Clean(cgroupPath) != cgroupPath {
		t.Fatal("could not resolve the live container cgroup v2 path")
	}
	directory := filepath.Join("/sys/fs/cgroup", strings.TrimPrefix(cgroupPath, "/"))
	readLimit := func(name string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(data))
	}
	if value := readLimit("memory.max"); value != "268435456" {
		t.Fatalf("kernel did not enforce memory limit: %s", value)
	}
	if value := readLimit("pids.max"); value != "64" {
		t.Fatalf("kernel did not enforce PID limit: %s", value)
	}
	cpu := strings.Fields(readLimit("cpu.max"))
	if len(cpu) != 2 {
		t.Fatal("invalid live cpu.max")
	}
	quota, quotaErr := strconv.ParseInt(cpu[0], 10, 64)
	period, periodErr := strconv.ParseInt(cpu[1], 10, 64)
	if quotaErr != nil || periodErr != nil || quota <= 0 || period <= 0 || quota != period {
		t.Fatalf("kernel did not enforce one CPU quota: %v", cpu)
	}
	t.Log("live cgroup verified: memory.max=268435456 pids.max=64 cpu quota/period=1")
}

type failedCleanupLauncher struct{ *fixtureLauncher }

func (f failedCleanupLauncher) Cleanup(context.Context, string) error {
	return fmt.Errorf("fixture cleanup failure")
}
func TestCleanupFailureStopsConnectorAndNeverClaimsWriteSuccess(t *testing.T) {
	target := stdioTarget()
	normal := &Agent{launcher: &fixtureLauncher{mode: "normal"}}
	job := testJob(target)
	catalog := normal.execute(context.Background(), job, target)
	job = executionJob(job, catalog.Tools[0])
	job.Tool.Risk = core.RiskWrite
	job.Operation.Risk = core.RiskWrite
	a := &Agent{launcher: failedCleanupLauncher{&fixtureLauncher{mode: "normal"}}}
	result := a.execute(context.Background(), job, target)
	if result.State != core.StateUnknown || !a.unhealthy.Load() {
		t.Fatalf("cleanup uncertainty hidden: %+v", result)
	}
}
func TestHTTPPrivateTargetPolicyAndCredentialSeparation(t *testing.T) {
	var calls atomic.Int32
	sdk := mcp.NewServer(&mcp.Implementation{Name: "private-fixture", Version: "1"}, nil)
	sdk.AddTool(&mcp.Tool{Name: "read", Description: "Synthetic local read", InputSchema: json.RawMessage(`{"type":"object"}`), Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil
	})
	sdkHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return sdk }, &mcp.StreamableHTTPOptions{JSONResponse: true, Stateless: true})
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer local-only" {
			t.Error("local credential missing or cloud token leaked")
		}
		sdkHandler.ServeHTTP(w, r)
	}))
	defer local.Close()
	secret := filepath.Join(privateTemp(t), "headers.json")
	if err := os.WriteFile(secret, []byte(`{"Authorization":"Bearer local-only"}`), 0600); err != nil {
		t.Fatal(err)
	}
	target := TargetConfig{Name: "private", Transport: "http", URL: local.URL + "/mcp", AllowedCIDRs: []string{"127.0.0.1/32"}, HeadersFile: secret}
	job := testJob(target)
	a := &Agent{token: strings.Repeat("cloud-only", 4)}
	result := a.execute(context.Background(), job, target)
	if result.State != core.StateSucceeded || len(result.Tools) != 1 || calls.Load() == 0 {
		t.Fatalf("private HTTP target failed: %+v", result)
	}
	before := calls.Load()
	target.AllowedCIDRs = nil
	result = a.execute(context.Background(), job, target)
	if result.State != core.StateFailed || calls.Load() != before {
		t.Fatal("private network policy bypassed")
	}
}
func TestBoundedCloudResponseAndArgumentHash(t *testing.T) {
	gateway := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(maxWireBytes+1))
		w.WriteHeader(200)
	}))
	defer gateway.Close()
	target := TargetConfig{Name: "local", Transport: "http", URL: "http://127.0.0.1/mcp"}
	config := testConfig(t, gateway.URL, target)
	a, err := newAgent(context.Background(), config, gateway.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if _, err := a.poll(context.Background(), true); err == nil {
		t.Fatal("oversized gateway response accepted")
	}
	st := stdioTarget()
	job := testJob(st)
	catalog := (&Agent{launcher: &fixtureLauncher{mode: "normal"}}).execute(context.Background(), job, st)
	job = executionJob(job, catalog.Tools[0])
	a.targets[st.Name] = st
	if _, err := a.validateJob(job); err != nil {
		t.Fatal(err)
	}
	job.Operation.Arguments = json.RawMessage(`{"value":0}`)
	if _, err := a.validateJob(job); err == nil {
		t.Fatal("approved argument hash was bypassed")
	}
}

func TestJournalFailureStopsWithoutClaimingMoreWork(t *testing.T) {
	target := stdioTarget()
	job := testJob(target)
	var claims, starts atomic.Int32
	gateway := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/connector/v1/poll" {
			starts.Add(1)
			w.WriteHeader(204)
			return
		}
		var in connectorwire.PollInput
		_ = json.NewDecoder(r.Body).Decode(&in)
		if !in.Ready {
			w.WriteHeader(204)
			return
		}
		claims.Add(1)
		_ = json.NewEncoder(w).Encode(job)
	}))
	defer gateway.Close()
	c := testConfig(t, gateway.URL, target)
	a, err := newAgent(context.Background(), c, gateway.Client(), &fixtureLauncher{mode: "normal"})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err := os.WriteFile(filepath.Join(c.StateDir, "journal.pending"), []byte("interrupted"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err = a.Run(ctx)
	var local localStateError
	if !errors.As(err, &local) || claims.Load() != 1 || starts.Load() != 0 {
		t.Fatalf("journal failure did not stop claiming: %v claims=%d starts=%d", err, claims.Load(), starts.Load())
	}
}
