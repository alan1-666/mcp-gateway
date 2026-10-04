package platform

import (
	"context"
	"github.com/alan1-666/mcp-gateway/internal/core"
	"log/slog"
	"net/http"
	"time"
)

type responseRecorder struct {
	http.ResponseWriter
	status int
}

func (w *responseRecorder) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
		w.ResponseWriter.WriteHeader(code)
	}
}
func (w *responseRecorder) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}
func (w *responseRecorder) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *responseRecorder) Flush() {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

func requests(next http.Handler) http.Handler {
	active := make(chan struct{}, 128)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := core.NewID()
		w.Header().Set("X-Request-ID", requestID)
		select {
		case active <- struct{}{}:
			defer func() { <-active }()
		default:
			w.Header().Set("Retry-After", "1")
			http.Error(w, "request capacity reached", http.StatusTooManyRequests)
			return
		}
		start := time.Now()
		recorder := &responseRecorder{ResponseWriter: w}
		ctx, cancel := context.WithTimeout(r.Context(), 135*time.Second)
		defer cancel()
		r = r.WithContext(ctx)
		next.ServeHTTP(recorder, r)
		status := recorder.status
		if status == 0 {
			status = 200
		}
		// Route templates contain no query values, resource IDs, tokens or payloads.
		slog.Info("http request", "request_id", requestID, "method", r.Method, "route", r.Pattern, "status", status, "duration_ms", time.Since(start).Milliseconds())
	})
}
