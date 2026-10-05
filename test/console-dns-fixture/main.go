// Command console-dns-fixture echoes synthetic proxy requests for the isolated
// Docker DNS regression. It has no credentials, persistence or outbound traffic.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"io"
	"log"
	"net/http"
	"time"
)

func main() {
	listen := flag.String("listen", ":8090", "test listen address")
	generation := flag.String("generation", "v1", "synthetic backend generation")
	flag.Parse()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 256<<10))
		if err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		sum := sha256.Sum256(body)
		headers := map[string]string{"Host": r.Host}
		for _, key := range []string{"Authorization", "Content-Type", "Accept", "Origin", "MCP-Protocol-Version", "Mcp-Session-Id", "Last-Event-ID", "X-Trace-Id"} {
			headers[key] = r.Header.Get(key)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"generation": *generation, "method": r.Method, "request_uri": r.RequestURI,
			"body_bytes": len(body), "body_sha256": hex.EncodeToString(sum[:]), "headers": headers,
		})
	})
	server := &http.Server{Addr: *listen, Handler: handler, ReadHeaderTimeout: 3 * time.Second,
		ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second}
	log.Fatal(server.ListenAndServe())
}
