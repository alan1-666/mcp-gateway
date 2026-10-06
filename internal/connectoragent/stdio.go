package connectoragent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"time"
)

const maxLineBytes = 1 << 20

type runningProcess struct {
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stop   func()
	wait   func() error
}
type launcher interface {
	Launch(context.Context, TargetConfig, string) (runningProcess, error)
	Cleanup(context.Context, string) error
}
type podmanLauncher struct{ path string }

func newPodman(ctx context.Context) (*podmanLauncher, error) {
	if runtime.GOOS != "linux" || os.Geteuid() == 0 {
		return nil, errors.New("stdio requires Linux and an unprivileged local rootless Podman user")
	}
	for _, key := range []string{"CONTAINER_HOST", "CONTAINER_CONNECTION", "DOCKER_HOST"} {
		if os.Getenv(key) != "" {
			return nil, errors.New("remote container engines are not supported")
		}
	}
	path, err := exec.LookPath("podman")
	if err != nil {
		return nil, errors.New("rootless Podman is required for stdio")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--remote=false", "info", "--format", "json")
	output := limitedOutput{limit: 64 << 10}
	cmd.Stdout = &output
	cmd.Stderr = io.Discard
	if cmd.Run() != nil || !podmanResourceSupport(output.String()) {
		return nil, errors.New("local rootless Podman requires cgroup v2 with delegated cpu, memory and pids controllers")
	}
	return &podmanLauncher{path: path}, nil
}

// Rootless cgroup v1 and hosts without delegated controllers cannot enforce
// this execution profile. Fail before starting an image on either host.
func podmanResourceSupport(raw string) bool {
	var info struct {
		Host struct {
			Security struct {
				Rootless bool `json:"rootless"`
			} `json:"security"`
			CgroupVersion string   `json:"cgroupVersion"`
			Controllers   []string `json:"cgroupControllers"`
		} `json:"host"`
	}
	if len(raw) > 64<<10 || json.Unmarshal([]byte(raw), &info) != nil || !info.Host.Security.Rootless || info.Host.CgroupVersion != "v2" {
		return false
	}
	controllers := map[string]bool{}
	for _, controller := range info.Host.Controllers {
		controllers[controller] = true
	}
	return controllers["cpu"] && controllers["memory"] && controllers["pids"]
}

type limitedOutput struct {
	bytes.Buffer
	limit int
}

func (w *limitedOutput) Write(p []byte) (int, error) {
	limit := w.limit
	if limit <= 0 {
		limit = 4096
	}
	if w.Len()+len(p) > limit {
		return 0, errors.New("process output exceeds limit")
	}
	return w.Buffer.Write(p)
}
func podmanArgs(t TargetConfig, name string) []string {
	args := []string{"--remote=false", "run", "--pull=never", "--rm", "--log-driver=none", "--http-proxy=false", "-i", "--name", name, "--network=none", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--pids-limit=64", "--memory=256m", "--cpus=1", "--user=65532:65532", "--tmpfs", "/tmp:size=16m,nosuid,nodev,noexec", t.Image}
	return append(args, t.Args...)
}
func (p *podmanLauncher) Launch(ctx context.Context, t TargetConfig, name string) (runningProcess, error) {
	// The image and argv come exclusively from the local, immutable configuration.
	cmd := exec.CommandContext(ctx, p.path, podmanArgs(t, name)...)
	cmd.Stderr = io.Discard
	cmd.WaitDelay = 2 * time.Second
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return runningProcess{}, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return runningProcess{}, err
	}
	if err := cmd.Start(); err != nil {
		stdin.Close()
		stdout.Close()
		return runningProcess{}, errors.New("could not start isolated stdio target")
	}
	return runningProcess{stdin: stdin, stdout: stdout, stop: func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}, wait: cmd.Wait}, nil
}
func (p *podmanLauncher) Cleanup(ctx context.Context, name string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, p.path, "--remote=false", "rm", "--force", "--ignore", "--time=0", name)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if cmd.Run() != nil {
		return errors.New("isolated target cleanup could not be confirmed")
	}
	return nil
}

type stdioLine struct {
	data []byte
	err  error
}
type stdioTransport struct {
	ctx        context.Context
	process    runningProcess
	lines      chan stdioLine
	done       chan struct{}
	mu         sync.Mutex
	once       sync.Once
	cleanupErr error
	cancel     context.CancelFunc
	cleanup    func() error
}

func newStdioTransport(ctx context.Context, l launcher, t TargetConfig, name string) (*stdioTransport, error) {
	p, err := l.Launch(ctx, t, name)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	s := &stdioTransport{ctx: ctx, cancel: cancel, process: p, lines: make(chan stdioLine, 1), done: make(chan struct{})}
	s.cleanup = func() error {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return l.Cleanup(cleanupCtx, name)
	}
	context.AfterFunc(ctx, s.Close)
	go func() {
		defer close(s.done)
		scanner := bufio.NewScanner(p.stdout)
		scanner.Buffer(make([]byte, 4096), maxLineBytes+1)
		for scanner.Scan() {
			data := append([]byte(nil), scanner.Bytes()...)
			if len(data) > maxLineBytes {
				break
			}
			select {
			case s.lines <- stdioLine{data: data}:
			case <-ctx.Done():
				return
			}
		}
		select {
		case s.lines <- stdioLine{err: errors.New("stdio target closed or exceeded its output bound")}:
		case <-ctx.Done():
		}
	}()
	return s, nil
}
func (s *stdioTransport) Close() {
	s.once.Do(func() {
		s.cancel()
		s.process.stdin.Close()
		s.process.stop()
		s.process.stdout.Close()
		_ = s.process.wait()
		s.cleanupErr = s.cleanup()
	})
}
func (s *stdioTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ctx.Err(); err != nil {
		return nil, err
	}
	if err := req.Context().Err(); err != nil {
		return nil, err
	}
	response := func(code int, data []byte) *http.Response {
		return &http.Response{StatusCode: code, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(bytes.NewReader(data)), ContentLength: int64(len(data)), Request: req}
	}
	if req.Method == http.MethodDelete {
		s.Close()
		return response(204, nil), nil
	}
	if req.Method != http.MethodPost || req.Body == nil {
		return nil, errors.New("unsupported stdio transport request")
	}
	raw, err := io.ReadAll(io.LimitReader(req.Body, maxLineBytes+1))
	req.Body.Close()
	if err != nil || len(raw) > maxLineBytes {
		return nil, errors.New("stdio request exceeds limit")
	}
	var message struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	if json.Unmarshal(raw, &message) != nil || message.Method == "" {
		return nil, errors.New("invalid stdio JSON-RPC request")
	}
	compact := new(bytes.Buffer)
	if json.Compact(compact, raw) != nil {
		return nil, errors.New("invalid stdio JSON")
	}
	compact.WriteByte('\n')
	if err := req.Context().Err(); err != nil {
		return nil, err
	}
	if _, err := s.process.stdin.Write(compact.Bytes()); err != nil {
		return nil, errors.New("stdio input unavailable")
	}
	if len(message.ID) == 0 {
		return response(202, nil), nil
	}
	totalBytes := 0
	for count := 0; count < 100; count++ {
		select {
		case <-req.Context().Done():
			s.Close()
			return nil, req.Context().Err()
		case <-s.ctx.Done():
			s.Close()
			return nil, s.ctx.Err()
		case line := <-s.lines:
			if line.err != nil {
				s.Close()
				return nil, line.err
			}
			totalBytes += len(line.data)
			if totalBytes > maxLineBytes {
				s.Close()
				return nil, errors.New("stdio response stream exceeds its output bound")
			}
			var reply struct {
				ID      json.RawMessage `json:"id"`
				Method  string          `json:"method"`
				JSONRPC string          `json:"jsonrpc"`
			}
			if json.Unmarshal(line.data, &reply) != nil || reply.JSONRPC != "2.0" {
				s.Close()
				return nil, errors.New("invalid stdio JSON-RPC response")
			}
			if len(reply.ID) == 0 && reply.Method != "" {
				continue
			}
			if reply.Method != "" || !bytes.Equal(bytes.TrimSpace(reply.ID), bytes.TrimSpace(message.ID)) {
				s.Close()
				return nil, errors.New("unsupported or unmatched stdio response")
			}
			return response(200, line.data), nil
		}
	}
	s.Close()
	return nil, errors.New("stdio notification limit exceeded")
}
