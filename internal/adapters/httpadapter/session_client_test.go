package httpadapter

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSessionClientTCPReuseAndLiveCredentialFence(t *testing.T) {
	var connections, requests atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Authorization") != "Bearer fixture" {
			t.Error("credential missing on reused connection")
		}
		_, _ = io.WriteString(w, "ok")
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}
	server.Start()
	defer server.Close()
	a, err := New([]string{server.URL}, []string{"127.0.0.0/8"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var version atomic.Int32
	version.Store(1)
	a.SetCredentialResolver(resolverFunc(func(context.Context, string, string, string) (Credential, bool, error) {
		return Credential{WorkspaceID: "workspace", Ref: "AUTH", Origin: server.URL, Version: int(version.Load()), Headers: map[string]string{"Authorization": "Bearer fixture"}}, true, nil
	}))
	client, closeClient, err := a.NewSessionClientContext(context.Background(), "workspace", server.URL, "AUTH", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer closeClient()
	request := func(c *http.Client) error {
		req, _ := http.NewRequest(http.MethodPost, server.URL, strings.NewReader(`{}`))
		resp, err := c.Do(req)
		if err == nil {
			_, err = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
		return err
	}
	for range 2 {
		if err = request(client); err != nil {
			t.Fatal(err)
		}
	}
	if connections.Load() != 1 || requests.Load() != 2 {
		t.Fatal("exclusive session did not reuse checked TCP connection")
	}
	version.Store(2)
	if err = request(client); err == nil || requests.Load() != 2 {
		t.Fatal("credential version fence allowed stale headers over retained TCP")
	}
	// The legacy fresh-client API still disables persistent TCP sessions.
	fresh, closeFresh, err := a.NewClient("workspace", server.URL, "AUTH", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFresh()
	for range 2 {
		if err = request(fresh); err != nil {
			t.Fatal(err)
		}
	}
	if connections.Load() != 3 || requests.Load() != 4 {
		t.Fatal("fresh transport policy changed")
	}
	if _, _, err := a.NewSessionClientContext(context.Background(), "workspace", server.URL, "AUTH", time.Millisecond); err == nil {
		t.Fatal("invalid session timeout accepted")
	}
}
