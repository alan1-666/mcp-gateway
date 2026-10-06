package connectoragent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/alan1-666/mcp-gateway/internal/adapters/httpadapter"
	"github.com/alan1-666/mcp-gateway/internal/adapters/mcpadapter"
	"github.com/alan1-666/mcp-gateway/internal/connectorwire"
	"github.com/alan1-666/mcp-gateway/internal/core"
)

type Agent struct {
	unhealthy atomic.Bool
	config    Config
	token     string
	client    *http.Client
	journal   *journal
	launcher  launcher
	targets   map[string]TargetConfig
}

func New(ctx context.Context, c Config) (*Agent, error) { return newAgent(ctx, c, nil, nil) }
func newAgent(ctx context.Context, c Config, client *http.Client, l launcher) (*Agent, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	token, err := loadToken(c.TokenFile)
	if err != nil {
		return nil, err
	}
	j, err := openJournal(c.StateDir)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*Agent, error) { j.Close(); return nil, err }
	targets := map[string]TargetConfig{}
	needPodman := false
	for _, t := range c.Targets {
		targets[t.Name] = t
		if t.Transport == "stdio" {
			needPodman = true
		}
	}
	for _, entry := range j.entries {
		if entry.Container != "" {
			needPodman = true
		}
	}
	if needPodman && l == nil {
		l, err = newPodman(ctx)
		if err != nil {
			return fail(err)
		}
	}
	if l != nil {
		for _, entry := range j.entries {
			if entry.Container != "" {
				if err := l.Cleanup(ctx, entry.Container); err != nil {
					return fail(err)
				}
			}
		}
	}
	if client == nil {
		client = &http.Client{Transport: &http.Transport{Proxy: nil, MaxResponseHeaderBytes: 32 << 10, ResponseHeaderTimeout: 15 * time.Second, TLSHandshakeTimeout: 10 * time.Second}, Timeout: 20 * time.Second}
	}
	clone := *client
	clone.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Agent{config: c, token: token, client: &clone, journal: j, launcher: l, targets: targets}, nil
}
func (a *Agent) Close() { a.journal.Close(); a.client.CloseIdleConnections() }

type localStateError struct{ reason string }

func (e localStateError) Error() string { return e.reason }

type statusError struct{ status int }

func (e statusError) Error() string {
	return fmt.Sprintf("Connector gateway returned HTTP %d", e.status)
}
func (a *Agent) request(ctx context.Context, path string, in, out any) (int, error) {
	raw, err := json.Marshal(in)
	if err != nil || len(raw) > maxWireBytes {
		return 0, errors.New("Connector message exceeds limit")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(a.config.GatewayURL, "/")+path, bytes.NewReader(raw))
	if err != nil {
		return 0, errors.New("invalid Connector request")
	}
	req.GetBody = nil
	req.Header.Set("Authorization", "Bearer "+a.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		return 0, errors.New("Connector gateway is unavailable")
	}
	defer resp.Body.Close()
	if resp.ContentLength > maxWireBytes {
		return resp.StatusCode, errors.New("Connector response exceeds limit")
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxWireBytes+1))
	if err != nil || len(b) > maxWireBytes {
		return resp.StatusCode, errors.New("Connector response exceeds limit")
	}
	if resp.StatusCode != 200 && resp.StatusCode != 204 {
		return resp.StatusCode, statusError{resp.StatusCode}
	}
	if out != nil && resp.StatusCode == 200 {
		decoder := json.NewDecoder(bytes.NewReader(b))
		decoder.DisallowUnknownFields()
		if decoder.Decode(out) != nil || decoder.Decode(new(any)) != io.EOF {
			return resp.StatusCode, errors.New("invalid Connector gateway response")
		}
	}
	return resp.StatusCode, nil
}
func (a *Agent) poll(ctx context.Context, ready bool) (*connectorwire.Job, error) {
	var job connectorwire.Job
	code, err := a.request(ctx, "/connector/v1/poll", connectorwire.PollInput{Targets: a.config.TargetsWire(), Ready: ready}, &job)
	if err != nil {
		return nil, err
	}
	if code == 204 {
		return nil, nil
	}
	if !ready {
		return nil, errors.New("heartbeat unexpectedly claimed a job")
	}
	return &job, nil
}
func fatalAuth(err error) bool {
	var e statusError
	return errors.As(err, &e) && (e.status == 401 || e.status == 403 || e.status == 409)
}
func delay(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
func (a *Agent) Run(ctx context.Context) error {
	// First freeze the locally approved target catalog before requesting work.
	for {
		if _, err := a.poll(ctx, false); err == nil {
			break
		} else if fatalAuth(err) {
			return err
		}
		if !delay(ctx, 2*time.Second) {
			return ctx.Err()
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	heartbeatErr := make(chan error, 1)
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		for delay(ctx, 5*time.Second) {
			if _, err := a.poll(ctx, false); fatalAuth(err) {
				heartbeatErr <- err
				cancel()
				return
			}
		}
	}()
	defer func() { cancel(); <-heartbeatDone }()
	for {
		if ctx.Err() != nil {
			select {
			case err := <-heartbeatErr:
				return err
			default:
				return ctx.Err()
			}
		}
		job, err := a.poll(ctx, true)
		if err != nil {
			if fatalAuth(err) {
				return err
			}
			if !delay(ctx, 2*time.Second) {
				continue
			}
			continue
		}
		if job == nil {
			delay(ctx, 2*time.Second)
			continue
		}
		// A failed start is never retried. The cloud expires its claimed job.
		if err := a.handle(ctx, *job); err != nil {
			var localError localStateError
			if fatalAuth(err) || errors.As(err, &localError) {
				return err
			}
		}
		if a.unhealthy.Load() {
			return errors.New("isolated target cleanup failed; Connector stopped until cleanup can be confirmed")
		}
	}
}
func (a *Agent) validateJob(job connectorwire.Job) (TargetConfig, error) {
	target, ok := a.targets[job.Target.Name]
	if !ok || target.Wire() != job.Target || job.ID == "" || len(job.ID) > 128 || strings.ContainsAny(job.ID, "/\\?#%") || job.Deadline.IsZero() || !job.Deadline.After(time.Now()) || job.Deadline.After(time.Now().Add(121*time.Second)) {
		return target, errors.New("job does not match the local target or deadline policy")
	}
	if job.Server.WorkspaceID == "" || job.Server.ID == "" || !job.Server.Enabled || job.Server.TimeoutMS < 100 || job.Server.TimeoutMS > 120000 {
		return target, errors.New("invalid local MCP server policy")
	}
	if job.Kind == "discover" {
		if job.Tool != nil || job.Operation != nil {
			return target, errors.New("invalid discovery job")
		}
		return target, nil
	}
	if job.Kind != "execute" || job.Tool == nil || job.Operation == nil {
		return target, errors.New("invalid Connector job kind")
	}
	t, op := job.Tool, job.Operation
	if t.WorkspaceID != job.Server.WorkspaceID || op.WorkspaceID != t.WorkspaceID || t.MCP == nil || t.MCP.ServerID != job.Server.ID || op.ToolID != t.ID || op.ToolVersion != t.Version || op.Risk != t.Risk || op.ID == "" || op.State != core.StateDispatching {
		return target, errors.New("job operation does not match its reviewed tool")
	}
	if _, hash, err := core.CanonicalArguments(op.Arguments); err != nil || hash != op.ArgumentsHash {
		return target, errors.New("job argument hash does not match approved arguments")
	}
	if err := core.ValidateArguments(t.InputSchema, op.Arguments); err != nil {
		return target, errors.New("job arguments do not match the reviewed schema")
	}
	return target, nil
}
func (a *Agent) handle(ctx context.Context, job connectorwire.Job) error {
	if a.unhealthy.Load() {
		return localStateError{"Connector is stopped because local cleanup is unconfirmed"}
	}
	target, err := a.validateJob(job)
	if err != nil {
		return localStateError{"received job does not match the local execution policy"}
	}
	ctx, cancel := context.WithDeadline(ctx, job.Deadline)
	defer cancel()
	entry := journalEntry{ID: job.ID, Deadline: job.Deadline}
	if target.Transport == "stdio" {
		entry.Container = containerName(job.ID)
	}
	if err := a.journal.Record(entry); err != nil {
		return localStateError{"could not durably record a new execution; Connector stopped to prevent replay"}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	code, err := a.request(ctx, "/connector/v1/jobs/"+job.ID+"/start", struct{}{}, nil)
	if err != nil {
		return err
	}
	if code != 204 {
		return errors.New("execution authorization was not confirmed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	result := a.execute(ctx, job, target)
	for {
		code, err = a.request(ctx, "/connector/v1/jobs/"+job.ID+"/result", result, nil)
		if err == nil && code == 204 {
			return nil
		}
		if fatalAuth(err) {
			return err
		}
		if !delay(ctx, time.Second) {
			return ctx.Err()
		}
	}
}

type serverResolver struct{ server core.MCPServer }

func (r serverResolver) GetServer(_ context.Context, workspace, id string) (core.MCPServer, error) {
	if r.server.WorkspaceID != workspace || r.server.ID != id {
		return core.MCPServer{}, core.ErrNotFound
	}
	return r.server, nil
}
func (a *Agent) execute(ctx context.Context, job connectorwire.Job, target TargetConfig) connectorwire.Result {
	fail := func() connectorwire.Result {
		return connectorwire.Result{State: core.StateFailed, Error: "local MCP target could not be initialized"}
	}
	server := job.Server
	// Reconstruct local execution policy; no URL, cloud credential or transport
	// selector from a work item is allowed to reach the private network.
	endpoint := target.URL
	if target.Transport == "stdio" {
		endpoint = "https://connector.invalid/mcp"
	}
	server.MCPServerInput = core.MCPServerInput{Name: server.Name, Namespace: server.Namespace, URL: endpoint, TimeoutMS: server.TimeoutMS}
	u, _ := url.Parse(endpoint)
	origin := u.Scheme + "://" + u.Host
	var credentials []httpadapter.Credential
	if target.HeadersFile != "" {
		data, err := readSecret(target.HeadersFile)
		if err != nil {
			return fail()
		}
		var headers map[string]string
		if json.Unmarshal(data, &headers) != nil || len(headers) == 0 || len(headers) > 16 {
			return fail()
		}
		server.CredentialRef = "connector-local"
		credentials = []httpadapter.Credential{{WorkspaceID: server.WorkspaceID, Ref: server.CredentialRef, Origin: origin, Headers: headers}}
	}
	egress, err := httpadapter.New([]string{origin}, target.AllowedCIDRs, credentials)
	if err != nil {
		return fail()
	}
	adapter := mcpadapter.New(egress, serverResolver{server: server})
	var isolated *stdioTransport
	checkCleanup := func(result connectorwire.Result) connectorwire.Result {
		if isolated != nil {
			isolated.Close()
			if isolated.cleanupErr != nil {
				a.unhealthy.Store(true)
				state := core.StateFailed
				if job.Tool != nil && job.Tool.Risk == core.RiskWrite {
					state = core.StateUnknown
				}
				return connectorwire.Result{State: state, Error: "isolated target cleanup could not be confirmed; Connector stopped"}
			}
		}
		return result
	}
	if target.Transport == "stdio" {
		adapter.SetTransportFactory(func(ctx context.Context, _ core.MCPServer) (*http.Client, func(), error) {
			transport, err := newStdioTransport(ctx, a.launcher, target, containerName(job.ID))
			if err != nil {
				return nil, nil, err
			}
			isolated = transport
			return &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, transport.Close, nil
		})
	}
	actor := core.Actor{ID: "local-connector", WorkspaceID: server.WorkspaceID, Role: core.RoleAdmin}
	if job.Kind == "discover" {
		tools, err := adapter.Discover(ctx, actor, server)
		if err != nil {
			return checkCleanup(connectorwire.Result{State: core.StateFailed, Error: "local MCP discovery failed"})
		}
		return checkCleanup(connectorwire.Result{State: core.StateSucceeded, Tools: tools})
	}
	finished := adapter.Execute(ctx, actor, *job.Tool, *job.Operation)
	return checkCleanup(connectorwire.Result{State: finished.State, Result: finished.Result, Error: finished.Error})
}
