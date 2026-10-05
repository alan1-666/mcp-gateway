package httpadapter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/alan1-666/mcp-gateway/internal/core"
)

const maxResult = 1 << 20

// Credentials are scoped to a workspace and an exact, pre-authorized origin.
type Credential struct {
	WorkspaceID string            `json:"workspace_id"`
	Ref         string            `json:"ref"`
	Origin      string            `json:"origin"`
	Headers     map[string]string `json:"headers"`
}
type Adapter struct {
	origins     map[string]bool
	networks    []netip.Prefix
	credentials map[string]Credential
}

func New(origins, cidrs []string, credentials []Credential) (*Adapter, error) {
	a := &Adapter{origins: map[string]bool{}, credentials: map[string]Credential{}}
	for _, s := range origins {
		if s == "" {
			continue
		}
		u, err := url.Parse(s)
		if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
			return nil, fmt.Errorf("invalid allowed origin")
		}
		a.origins[u.Scheme+"://"+u.Host] = true
	}
	for _, s := range cidrs {
		if s == "" {
			continue
		}
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return nil, fmt.Errorf("invalid allowed network")
		}
		a.networks = append(a.networks, p)
	}
	for _, c := range credentials {
		if c.WorkspaceID == "" || c.Ref == "" || !a.origins[c.Origin] {
			return nil, fmt.Errorf("credential requires workspace, ref and an allowed origin")
		}
		for k, v := range c.Headers {
			switch strings.ToLower(k) {
			case "host", "content-length", "transfer-encoding", "connection", "idempotency-key":
				return nil, fmt.Errorf("reserved credential header")
			}
			if strings.ContainsAny(k+v, "\r\n") {
				return nil, fmt.Errorf("invalid credential header")
			}
		}
		key := c.WorkspaceID + "/" + c.Ref
		if _, ok := a.credentials[key]; ok {
			return nil, fmt.Errorf("duplicate credential reference")
		}
		a.credentials[key] = c
	}
	return a, nil
}

func (a *Adapter) Validate(workspace string, cfg core.HTTPConfig) error {
	u, err := url.Parse(cfg.URL)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" || !a.origins[u.Scheme+"://"+u.Host] {
		return fmt.Errorf("%w: tool URL requires an explicitly allowed origin and must not contain credentials, query or fragment", core.ErrInvalid)
	}
	if cfg.CredentialRef != "" {
		c, ok := a.credentials[workspace+"/"+cfg.CredentialRef]
		if !ok || c.Origin != u.Scheme+"://"+u.Host {
			return fmt.Errorf("%w: credential reference is not bound to this workspace and origin", core.ErrInvalid)
		}
	}
	return nil
}

func (a *Adapter) allowedIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	// Metadata/link-local and unspecified targets can never be opted in.
	if !ip.IsValid() || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return false
	}
	if ip == netip.MustParseAddr("100.100.100.200") {
		return false
	}
	if ip.IsPrivate() || ip.IsLoopback() || netip.MustParsePrefix("100.64.0.0/10").Contains(ip) {
		for _, p := range a.networks {
			if p.Contains(ip) {
				return true
			}
		}
		return false
	}
	return ip.IsGlobalUnicast()
}

func (a *Adapter) dial(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("invalid destination")
	}
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(ips) == 0 {
		return nil, fmt.Errorf("destination resolution failed")
	}
	for _, ip := range ips {
		if !a.allowedIP(ip) {
			return nil, fmt.Errorf("destination address is outside the egress policy")
		}
	}
	var last error
	for _, ip := range ips {
		conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
		last = err
	}
	return nil, last
}

func (a *Adapter) Execute(ctx context.Context, actor core.Actor, tool core.Tool, op core.Operation) core.FinishInput {
	fail := func(message string, uncertain bool) core.FinishInput {
		state := core.StateFailed
		if uncertain && tool.Risk == core.RiskWrite {
			state = core.StateUnknown
		}
		return core.FinishInput{State: state, Error: message}
	}
	if err := a.Validate(actor.WorkspaceID, tool.HTTP); err != nil {
		return fail("tool is blocked by the current egress or credential policy", false)
	}
	u, _ := url.Parse(tool.HTTP.URL)
	var body io.Reader
	if tool.HTTP.Method == http.MethodGet {
		var args map[string]json.RawMessage
		if json.Unmarshal(op.Arguments, &args) != nil {
			return fail("invalid persisted arguments", false)
		}
		q := u.Query()
		for k, value := range args {
			var s string
			if json.Unmarshal(value, &s) == nil {
				q.Set(k, s)
			} else {
				q.Set(k, string(value))
			}
		}
		u.RawQuery = q.Encode()
	} else {
		body = bytes.NewReader(op.Arguments)
	}
	request, err := http.NewRequestWithContext(ctx, tool.HTTP.Method, u.String(), body)
	if err != nil {
		return fail("could not construct downstream request", false)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", op.ID)
	if c, ok := a.credentials[actor.WorkspaceID+"/"+tool.HTTP.CredentialRef]; ok {
		for k, v := range c.Headers {
			request.Header.Set(k, v)
		}
	}
	transport := a.newTransport(time.Duration(tool.HTTP.TimeoutMS) * time.Millisecond)
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Duration(tool.HTTP.TimeoutMS) * time.Millisecond, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(request)
	if err != nil {
		return fail("downstream request did not produce a confirmed result", true)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResult+1))
	if err != nil || len(data) > maxResult {
		return fail("downstream response was incomplete or exceeded the size limit", true)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fail(fmt.Sprintf("downstream returned HTTP %d; verify write outcome before another action", resp.StatusCode), true)
	}
	result, err := core.DecodeResult(data)
	if err != nil {
		return fail("downstream response is not valid bounded JSON", true)
	}
	if len(tool.OutputSchema) > 0 {
		schema, err := core.CompileSchema(tool.OutputSchema, false)
		if err != nil || schema.Validate(result) != nil {
			return fail("downstream response failed the output contract", true)
		}
	}
	result = redact(result)
	encoded, err := json.Marshal(result)
	if err != nil {
		return fail("could not encode downstream result", true)
	}
	return core.FinishInput{State: core.StateSucceeded, Result: encoded}
}

func redact(v any) any {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(k, "_", ""), "-", ""))
			switch normalized {
			case "password", "passwd", "token", "accesstoken", "refreshtoken", "authorization", "secret", "clientsecret", "apikey", "cookie", "setcookie":
				x[k] = "[REDACTED]"
			default:
				x[k] = redact(x[k])
			}
		}
	case []any:
		for i := range x {
			x[i] = redact(x[i])
		}
	}
	return v
}
