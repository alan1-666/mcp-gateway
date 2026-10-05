package clients

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/alan1-666/mcp-gateway/internal/identity"
)

// Handler includes authentication and browser-session checks. Mount both
// /api/v1/clients and /api/v1/clients/ before the general management API.
func (s *Service) Handler(auth *identity.Auth) http.Handler { return auth.Middleware(s.routes()) }

func (s *Service) Register(mux *http.ServeMux) {
	handler := s.routes()
	mux.Handle("/api/v1/clients", handler)
	mux.Handle("/api/v1/clients/", handler)
}

func (s *Service) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/clients", func(w http.ResponseWriter, r *http.Request) {
		items, err := s.List(r.Context(), identity.Actor(r.Context()))
		respond(w, 200, map[string]any{"items": items}, err)
	})
	mux.HandleFunc("POST /api/v1/clients", func(w http.ResponseWriter, r *http.Request) {
		var in CreateInput
		if !decode(w, r, &in) {
			return
		}
		v, err := s.Create(r.Context(), identity.Actor(r.Context()), in)
		respond(w, 201, v, err)
	})
	mux.HandleFunc("POST /api/v1/clients/{id}", func(w http.ResponseWriter, r *http.Request) {
		var in UpdateInput
		if !decode(w, r, &in) {
			return
		}
		v, err := s.Update(r.Context(), identity.Actor(r.Context()), r.PathValue("id"), in)
		respond(w, 200, v, err)
	})
	mux.HandleFunc("POST /api/v1/clients/{id}/rotate", func(w http.ResponseWriter, r *http.Request) {
		var in RotateInput
		if !decode(w, r, &in) {
			return
		}
		v, err := s.Rotate(r.Context(), identity.Actor(r.Context()), r.PathValue("id"), in)
		respond(w, 200, v, err)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !identity.CanManageClients(r.Context()) {
			respond(w, 403, nil, core.ErrForbidden)
			return
		}
		mux.ServeHTTP(w, r)
	})
}
func decode(w http.ResponseWriter, r *http.Request, value any) bool {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		respond(w, 400, nil, invalid("a JSON request is required"))
		return false
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10))
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		respond(w, 400, nil, invalid("invalid request fields"))
		return false
	}
	if d.Decode(new(any)) != io.EOF {
		respond(w, 400, nil, invalid("expected one JSON object"))
		return false
	}
	return true
}
func respond(w http.ResponseWriter, status int, value any, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if err != nil {
		code, message := "internal_error", "The client change could not be completed."
		status = 500
		switch {
		case errors.Is(err, core.ErrUnauthorized):
			status, code, message = 401, "unauthorized", "Authentication required."
		case errors.Is(err, core.ErrForbidden):
			status, code, message = 403, "forbidden", "An administrator browser session is required."
		case errors.Is(err, core.ErrNotFound):
			status, code, message = 404, "not_found", "Client not found."
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
