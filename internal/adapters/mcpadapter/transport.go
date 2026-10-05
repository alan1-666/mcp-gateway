package mcpadapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"
)

const maxResponseBytes = 1 << 20

// protocolTransport bounds the SDK's reads before decoding (including SSE and
// error bodies), and prevents any tools/call replay even if SDK retry behavior
// changes. A session is used for at most one business call.
type protocolTransport struct {
	base      http.RoundTripper
	ctx       context.Context
	mu        sync.Mutex
	callSent  bool
	responses map[string]*responseCapture
}

func (t *protocolTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := t.ctx.Err(); err != nil {
		return nil, err
	}
	// No standalone SSE or resumption GET is permitted. Reads of the response
	// to the original POST still support SSE, without replaying the operation.
	if req.Method != http.MethodPost && req.Method != http.MethodDelete {
		return nil, errors.New("MCP stream reconnection is disabled")
	}
	var message struct {
		Method string          `json:"method"`
		ID     json.RawMessage `json:"id"`
	}
	if req.Method == http.MethodPost && req.Body != nil {
		data, err := io.ReadAll(io.LimitReader(req.Body, maxResponseBytes+1))
		req.Body.Close()
		if err != nil || len(data) > maxResponseBytes || json.Unmarshal(data, &message) != nil {
			return nil, errors.New("invalid outbound MCP message")
		}
		req = req.Clone(req.Context())
		req.Body = io.NopCloser(bytes.NewReader(data))
		// Disable net/http's transparent retries of requests with GetBody.
		req.GetBody = nil
		if message.Method == "tools/call" {
			t.mu.Lock()
			alreadySent := t.callSent
			t.callSent = true
			t.mu.Unlock()
			if alreadySent {
				return nil, errors.New("MCP tool replay is disabled")
			}
		}
	}
	// SDK session teardown deliberately detaches the Connect context. Bind all
	// network activity back to the operation deadline, including cancellation
	// notifications and DELETE. Teardown itself gets at most two seconds.
	ctx, cancel := context.WithCancel(req.Context())
	stop := context.AfterFunc(t.ctx, cancel)
	if req.Method == http.MethodDelete {
		var previous = cancel
		ctx, cancel = context.WithTimeout(ctx, 2*time.Second)
		defer previous()
	}
	req = req.Clone(ctx)
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		stop()
		cancel()
		return nil, err
	}
	if resp.ContentLength > maxResponseBytes {
		resp.Body.Close()
		stop()
		cancel()
		return nil, errors.New("MCP response exceeds the size limit")
	}
	capture := &responseCapture{id: message.ID}
	capture.mediaType, _, _ = mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if message.Method == "tools/list" || message.Method == "tools/call" {
		t.mu.Lock()
		t.responses[message.Method] = capture
		t.mu.Unlock()
	}
	resp.Body = &boundedBody{body: resp.Body, capture: capture, remaining: maxResponseBytes, close: func() { stop(); cancel() }}
	return resp, nil
}

func (t *protocolTransport) result(method string) (json.RawMessage, error) {
	t.mu.Lock()
	capture := t.responses[method]
	t.mu.Unlock()
	if capture == nil {
		return nil, errors.New("missing MCP wire response")
	}
	return capture.result()
}

// Capture the raw result separately from the SDK's typed result. Its current
// interface{} decoder uses float64; persisting that would round large integer
// identifiers and alter numeric schema constraints. SDK handles the protocol,
// while our bounded JSON decoder preserves numbers at the storage boundary.
type responseCapture struct {
	mu        sync.Mutex
	data      bytes.Buffer
	id        json.RawMessage
	mediaType string
}

func (c *responseCapture) result() (json.RawMessage, error) {
	c.mu.Lock()
	data := append([]byte(nil), c.data.Bytes()...)
	c.mu.Unlock()
	decode := func(raw []byte) (json.RawMessage, bool) {
		var message struct {
			ID     json.RawMessage `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if json.Unmarshal(raw, &message) != nil || !bytes.Equal(bytes.TrimSpace(message.ID), bytes.TrimSpace(c.id)) || len(message.Result) == 0 || len(message.Error) > 0 {
			return nil, false
		}
		return message.Result, true
	}
	if c.mediaType == "application/json" {
		if result, ok := decode(data); ok {
			return result, nil
		}
	} else if c.mediaType == "text/event-stream" {
		// Only extract complete data events; the SDK has already checked framing
		// and associated the response ID with the pending request.
		data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
		data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
		data = bytes.ReplaceAll(data, []byte("\r"), []byte("\n"))
		for _, frame := range bytes.Split(data, []byte("\n\n")) {
			var lines []string
			eventName := ""
			for _, line := range strings.Split(string(frame), "\n") {
				if line == "data" {
					lines = append(lines, "")
				} else if strings.HasPrefix(line, "data:") {
					lines = append(lines, strings.TrimPrefix(line[5:], " "))
				} else if line == "event" {
					eventName = ""
				} else if strings.HasPrefix(line, "event:") {
					eventName = strings.TrimPrefix(line[6:], " ")
				}
			}
			if eventName != "" && eventName != "message" {
				continue
			}
			if result, ok := decode([]byte(strings.Join(lines, "\n"))); ok {
				return result, nil
			}
		}
	}
	return nil, errors.New("invalid MCP wire response")
}

type boundedBody struct {
	body      io.ReadCloser
	capture   *responseCapture
	remaining int
	close     func()
	closed    sync.Once
}

func (b *boundedBody) Read(p []byte) (int, error) {
	if b.remaining < 0 {
		return 0, errors.New("MCP response exceeds the size limit")
	}
	if len(p) > b.remaining+1 {
		p = p[:b.remaining+1]
	}
	n, err := b.body.Read(p)
	b.remaining -= n
	if b.remaining < 0 {
		return 0, errors.New("MCP response exceeds the size limit")
	}
	if n > 0 {
		b.capture.mu.Lock()
		_, _ = b.capture.data.Write(p[:n])
		b.capture.mu.Unlock()
	}
	return n, err
}

func (b *boundedBody) Close() error {
	err := b.body.Close()
	b.closed.Do(b.close)
	return err
}
