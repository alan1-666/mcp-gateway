// Command mcp-fixture is a loopback-only, synthetic MCP server for acceptance.
// It has no business integrations, persistence, external calls or credentials.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"log"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

func main() {
	address := flag.String("addr", "127.0.0.1:8361", "loopback listen address")
	label := flag.String("label", "warehouse-a", "synthetic source label")
	flag.Parse()
	host, _, err := net.SplitHostPort(*address)
	if err != nil {
		log.Fatal(err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		log.Fatal("acceptance fixture only listens on loopback")
	}
	var writes atomic.Int64
	sdk := mcp.NewServer(&mcp.Implementation{Name: "acceptance-fixture", Version: "1"}, &mcp.ServerOptions{PageSize: 1})
	schema := json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
	sdk.AddTool(&mcp.Tool{Name: "inventory", Description: "Synthetic inventory with a large internal note; use structured projection /source and /items.", InputSchema: schema, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		body := map[string]any{"source": *label, "items": []any{map[string]any{"sku": "TEST-001", "stock": 42}}, "next_cursor": "test-page-2", "internal_note": strings.Repeat("synthetic private test data; ", 500), "api_key": "synthetic-secret"}
		raw, _ := json.Marshal(body)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(raw)}}, StructuredContent: json.RawMessage(raw)}, nil
	})
	sdk.AddTool(&mcp.Tool{Name: "create_ticket", Description: "Synthetic write: increment the in-memory acceptance counter. No real ticket is created.", InputSchema: json.RawMessage(`{"type":"object","properties":{"title":{"type":"string","minLength":1}},"required":["title"],"additionalProperties":false}`)}, func(_ context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var input struct {
			Title string `json:"title"`
		}
		if err := json.Unmarshal(r.Params.Arguments, &input); err != nil {
			return nil, err
		}
		result := map[string]any{"ticket_id": fmt.Sprintf("TEST-%d", writes.Add(1)), "title": input.Title, "source": *label}
		raw, _ := json.Marshal(result)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(raw)}}, StructuredContent: json.RawMessage(raw)}, nil
	})
	mux := http.NewServeMux()
	mux.Handle("/mcp", mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return sdk }, &mcp.StreamableHTTPOptions{JSONResponse: true, SessionTimeout: time.Minute}))
	mux.HandleFunc("GET /stats", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"source": *label, "writes": writes.Load()})
	})
	log.Printf("Synthetic MCP acceptance server: http://%s/mcp", *address)
	log.Fatal((&http.Server{Addr: *address, Handler: mux, ReadHeaderTimeout: 5 * time.Second}).ListenAndServe())
}
