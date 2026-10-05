package mcpadapter

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestTransportRejectsConcurrentOrSDKReplayBeforeNetwork(t *testing.T) {
	var networkCalls atomic.Int32
	transport := &protocolTransport{ctx: context.Background(), responses: map[string]*responseCapture{}, base: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		networkCalls.Add(1)
		if r.GetBody != nil {
			t.Error("transparent net/http replay remained enabled")
		}
		return &http.Response{StatusCode: 404, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("session missing"))}, nil
	})}
	var workers sync.WaitGroup
	for range 3 {
		workers.Go(func() {
			req, _ := http.NewRequest("POST", "http://test.invalid", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"write"}}`))
			resp, err := transport.RoundTrip(req)
			if err == nil {
				resp.Body.Close()
			}
		})
	}
	workers.Wait()
	if networkCalls.Load() != 1 {
		t.Fatalf("replayed tools/call reached upstream %d times", networkCalls.Load())
	}
	req, _ := http.NewRequest("GET", "http://test.invalid", nil)
	if _, err := transport.RoundTrip(req); err == nil || networkCalls.Load() != 1 {
		t.Fatal("SSE resumption was not disabled")
	}
}

func TestBoundedSSEBodyAndRawMultilinePrecision(t *testing.T) {
	capture := &responseCapture{id: []byte("4"), mediaType: "text/event-stream"}
	data := "\xef\xbb\xbf: keepalive\r\nevent: ignored\r\ndata: {\"jsonrpc\":\"2.0\",\"id\":4,\"result\":{\"incorrect\":true}}\r\n\r\nevent: message\r\ndata: {\"jsonrpc\":\"2.0\",\"id\":4,\r\ndata: \"result\":{\"content\":[],\"structuredContent\":{\"id\":9007199254740993}}}\r\n\r\n"
	body := &boundedBody{body: io.NopCloser(strings.NewReader(data)), capture: capture, remaining: maxResponseBytes, close: func() {}}
	if _, err := io.ReadAll(body); err != nil {
		t.Fatal(err)
	}
	result, err := capture.result()
	if err != nil || !bytes.Contains(result, []byte("9007199254740993")) {
		t.Fatalf("multiline raw result: %s %v", result, err)
	}
	body = &boundedBody{body: io.NopCloser(strings.NewReader(strings.Repeat("x", maxResponseBytes+1))), capture: &responseCapture{}, remaining: maxResponseBytes, close: func() {}}
	if _, err := io.ReadAll(body); err == nil {
		t.Fatal("chunked response exceeded wire limit")
	}
	if body.capture.data.Len() > maxResponseBytes {
		t.Fatal("unbounded captured response")
	}
}
