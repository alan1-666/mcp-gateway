package clients_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/alan1-666/mcp-gateway/internal/identity"
	"github.com/alan1-666/mcp-gateway/internal/transport/httpapi"
	"github.com/alan1-666/mcp-gateway/internal/transport/mcpserver"
)

// Exercise the exact browser helper against real identity/storage and the SDK
// server transport, rather than treating mocked JSON as protocol compatibility.
func TestBrowserConnectionAgainstGateway(t *testing.T) {
	if os.Getenv("RUN_CLOUD_WORKER_INTEGRATION") != "1" {
		t.Skip("RUN_CLOUD_WORKER_INTEGRATION=1 enables Node integration")
	}
	f := database(t)
	tool := f.tool(t, core.RiskRead)
	key := f.issue(t, tool.ID)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	auth, err := identity.NewCloud(ctx, f.pool, "https://connection.example", "")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/api/", (&httpapi.API{Service: f.core}).Handler(auth))
	mux.Handle("/mcp", mcpserver.Handler(f.core, nil, auth))
	server := httptest.NewServer(mux)
	defer server.Close()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	script := `
import assert from 'node:assert/strict';
import {checkClientConnection} from './apps/console/src/client-connection.ts';
const transport = (path, init) => fetch(new URL(path, process.env.CHECK_ORIGIN), init);
const report = await checkClientConnection(process.env.CHECK_CLIENT, process.env.CHECK_KEY, new AbortController().signal, transport);
assert.equal(report.total,1);
assert.equal(report.tools[0].id,process.env.CHECK_TOOL);
await assert.rejects(checkClientConnection('wrong-client',process.env.CHECK_KEY,new AbortController().signal,transport),/selected client/);
process.stdout.write('real Gateway connection check passed\n');
`
	command := exec.CommandContext(ctx, "node", "--import", "tsx", "--input-type=module", "-e", script)
	command.Dir = root
	command.Env = append(os.Environ(), "CHECK_ORIGIN="+server.URL, "CHECK_CLIENT="+key.Client.ID, "CHECK_KEY="+key.APIKey, "CHECK_TOOL="+tool.ID)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("browser protocol check failed: %v\n%s", err, output)
	}
}
