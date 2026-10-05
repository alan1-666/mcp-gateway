package capacity

import (
	"encoding/json"
	"errors"
	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/alan1-666/mcp-gateway/internal/identity"
	"io"
	"net/http"
	"strings"
)

// Register must be called inside the existing authenticated management router.
func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/capacity", func(w http.ResponseWriter, r *http.Request) {
		items, err := s.List(r.Context(), identity.Actor(r.Context()))
		respond(w, map[string]any{"defaults": defaults(), "items": items, "total": len(items)}, err)
	})
	mux.HandleFunc("POST /api/v1/capacity/limits", func(w http.ResponseWriter, r *http.Request) {
		actor := identity.Actor(r.Context())
		if err := authorize(actor); err != nil {
			respond(w, nil, err)
			return
		}
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			respond(w, nil, invalid("application/json is required"))
			return
		}
		var in UpdateInput
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
		d.DisallowUnknownFields()
		if d.Decode(&in) != nil || d.Decode(new(any)) != io.EOF {
			respond(w, nil, invalid("invalid request fields"))
			return
		}
		v, err := s.Update(r.Context(), actor, in)
		respond(w, v, err)
	})
	mux.HandleFunc("GET /api/v1/capacity/metrics", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.Metrics(r.Context(), identity.Actor(r.Context()))
		respond(w, v, err)
	})
}
func respond(w http.ResponseWriter, value any, err error) {
	status := 200
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if err != nil {
		status = http.StatusServiceUnavailable
		code, message := "internal_error", "Capacity operation failed."
		switch {
		case errors.Is(err, core.ErrUnauthorized):
			status, code, message = 401, "unauthorized", "Authentication required."
		case errors.Is(err, core.ErrForbidden):
			status, code, message = 403, "forbidden", "Administrator access required."
		case errors.Is(err, core.ErrInvalid):
			status, code, message = 400, "invalid_input", err.Error()
		case errors.Is(err, core.ErrConflict):
			status, code, message = 409, "conflict", err.Error()
		}
		value = map[string]any{"error": map[string]string{"code": code, "message": message}}
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
