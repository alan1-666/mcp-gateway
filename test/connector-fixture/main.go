// Connector acceptance fixture; synthetic in-memory data only.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	listen := flag.String("http", "", "optional loopback HTTP address; default is stdio")
	flag.Parse()
	server := mcp.NewServer(&mcp.Implementation{Name: "rillgate-connector-fixture", Version: "1"}, nil)
	var writes atomic.Int64
	input := json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
	output := json.RawMessage(`{"type":"object","properties":{"value":{"type":"integer"}},"required":["value"],"additionalProperties":false}`)
	server.AddTool(&mcp.Tool{Name: "echo", Description: "Read a synthetic exact integer", InputSchema: input, OutputSchema: output, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "Synthetic fixture data"}}, StructuredContent: json.RawMessage(`{"value":9007199254740993}`)}, nil
	})
	server.AddTool(&mcp.Tool{Name: "increment", Description: "Increment a synthetic process-local counter", InputSchema: input, OutputSchema: output}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		result, _ := json.Marshal(map[string]int64{"value": writes.Add(1)})
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "Synthetic counter incremented"}}, StructuredContent: json.RawMessage(result)}, nil
	})
	server.AddTool(&mcp.Tool{Name: "slow", Description: "Synthetic timeout fixture", InputSchema: input, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(30 * time.Second):
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "done"}}}, nil
		}
	})
	if *listen == "" {
		if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
			log.Fatal("fixture stopped")
		}
		return
	}
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{JSONResponse: true, Stateless: true})
	mux := http.NewServeMux()
	mux.Handle("/mcp", handler)
	srv := &http.Server{Addr: *listen, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatal("fixture stopped")
	}
}
