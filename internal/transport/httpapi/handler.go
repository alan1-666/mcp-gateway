package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/alan1-666/mcp-gateway/internal/adapters/httpadapter"
	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/alan1-666/mcp-gateway/internal/execution"
	"github.com/alan1-666/mcp-gateway/internal/identity"
	"github.com/alan1-666/mcp-gateway/internal/upstreams"
)

type API struct {
	Service   *core.Service
	Executor  *execution.Executor
	Adapter   *httpadapter.Adapter
	Upstreams *upstreams.Service
}

func (a *API) Handler(auth *identity.Auth) http.Handler {
	mux := http.NewServeMux()
	a.registerUpstreams(mux)
	mux.HandleFunc("GET /api/v1/me", func(w http.ResponseWriter, r *http.Request) { respond(w, identity.Actor(r.Context()), nil) })
	mux.HandleFunc("GET /api/v1/tools", a.listTools)
	mux.HandleFunc("GET /api/v1/catalog/tools", a.discoverTools)
	mux.HandleFunc("POST /api/v1/tools", a.createTool)
	mux.HandleFunc("GET /api/v1/tools/{id}", func(w http.ResponseWriter, r *http.Request) {
		result, err := a.Service.GetTool(r.Context(), identity.Actor(r.Context()), r.PathValue("id"))
		respond(w, result, err)
	})
	mux.HandleFunc("POST /api/v1/tools/{id}/publish", func(w http.ResponseWriter, r *http.Request) {
		if !emptyBody(w, r) {
			return
		}
		result, err := a.Service.PublishTool(r.Context(), identity.Actor(r.Context()), r.PathValue("id"))
		respond(w, result, err)
	})
	mux.HandleFunc("POST /api/v1/tools/{id}/enabled", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Enabled *bool `json:"enabled"`
		}
		if err := decode(w, r, &input); err != nil {
			respond(w, nil, err)
			return
		}
		if input.Enabled == nil {
			respond(w, nil, fmt.Errorf("%w: enabled is required", core.ErrInvalid))
			return
		}
		result, err := a.Service.SetToolEnabled(r.Context(), identity.Actor(r.Context()), r.PathValue("id"), *input.Enabled)
		respond(w, result, err)
	})
	mux.HandleFunc("GET /api/v1/operations", func(w http.ResponseWriter, r *http.Request) {
		result, err := a.Service.ListOperations(r.Context(), identity.Actor(r.Context()))
		if result == nil {
			result = []core.Operation{}
		}
		respond(w, map[string]any{"items": result}, err)
	})
	mux.HandleFunc("POST /api/v1/operations", func(w http.ResponseWriter, r *http.Request) {
		var input core.PrepareInput
		if err := decode(w, r, &input); err != nil {
			respond(w, nil, err)
			return
		}
		result, err := a.Service.Prepare(r.Context(), identity.Actor(r.Context()), input)
		respond(w, result, err)
	})
	mux.HandleFunc("GET /api/v1/operations/{id}", func(w http.ResponseWriter, r *http.Request) {
		result, err := a.Service.GetOperation(r.Context(), identity.Actor(r.Context()), r.PathValue("id"))
		respond(w, result, err)
	})
	mux.HandleFunc("POST /api/v1/operations/{id}/approve", func(w http.ResponseWriter, r *http.Request) {
		if !emptyBody(w, r) {
			return
		}
		result, err := a.Service.Approve(r.Context(), identity.Actor(r.Context()), r.PathValue("id"))
		respond(w, result, err)
	})
	mux.HandleFunc("POST /api/v1/operations/{id}/reject", func(w http.ResponseWriter, r *http.Request) {
		if !emptyBody(w, r) {
			return
		}
		result, err := a.Service.Reject(r.Context(), identity.Actor(r.Context()), r.PathValue("id"))
		respond(w, result, err)
	})
	mux.HandleFunc("POST /api/v1/operations/{id}/execute", func(w http.ResponseWriter, r *http.Request) {
		if !emptyBody(w, r) {
			return
		}
		result, err := a.Executor.Execute(r.Context(), identity.Actor(r.Context()), r.PathValue("id"))
		respond(w, result, err)
	})
	mux.HandleFunc("GET /api/v1/operations/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		var after int64
		var err error
		if q := r.URL.Query().Get("after"); q != "" {
			after, err = strconv.ParseInt(q, 10, 64)
			if err != nil {
				respond(w, nil, core.ErrInvalid)
				return
			}
		}
		result, err := a.Service.ListEvents(r.Context(), identity.Actor(r.Context()), r.PathValue("id"), after)
		if result == nil {
			result = []core.Event{}
		}
		respond(w, map[string]any{"items": result}, err)
	})
	return auth.Middleware(mux)
}

func (a *API) listTools(w http.ResponseWriter, r *http.Request) {
	input, err := ParseToolSearch(r.URL.RawQuery)
	if err != nil {
		respond(w, nil, err)
		return
	}
	page, err := a.Service.SearchTools(r.Context(), identity.Actor(r.Context()), input)
	respond(w, page, err)
}
func (a *API) discoverTools(w http.ResponseWriter, r *http.Request) {
	input, err := ParseToolSearch(r.URL.RawQuery)
	if err != nil {
		respond(w, nil, err)
		return
	}
	page, err := a.Service.DiscoverTools(r.Context(), identity.Actor(r.Context()), input)
	respond(w, page, err)
}
func (a *API) createTool(w http.ResponseWriter, r *http.Request) {
	var input core.ToolInput
	if err := decode(w, r, &input); err != nil {
		respond(w, nil, err)
		return
	}
	actor := identity.Actor(r.Context())
	if actor.Role != core.RoleAdmin {
		respond(w, nil, core.ErrForbidden)
		return
	}
	if input.MCP != nil || input.ResponsePolicy != nil {
		respond(w, nil, fmt.Errorf("%w: import MCP tools through their registered server", core.ErrInvalid))
		return
	}
	if err := a.Adapter.Validate(actor.WorkspaceID, input.HTTP); err != nil {
		respond(w, nil, err)
		return
	}
	result, err := a.Service.CreateTool(r.Context(), actor, input)
	respond(w, result, err)
}
func emptyBody(w http.ResponseWriter, r *http.Request) bool {
	var body struct{}
	err := decode(w, r, &body)
	if err != nil {
		respond(w, nil, err)
		return false
	}
	return true
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		return fmt.Errorf("%w: Content-Type must be application/json", core.ErrInvalid)
	}
	r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return fmt.Errorf("%w: request must be a valid JSON object with supported fields", core.ErrInvalid)
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("%w: only one JSON value is allowed", core.ErrInvalid)
	}
	return nil
}
func ErrorDetails(err error) (int, string, string) {
	switch {
	case errors.Is(err, core.ErrUnauthorized):
		return 401, "unauthorized", err.Error()
	case errors.Is(err, core.ErrForbidden):
		return 403, "forbidden", err.Error()
	case errors.Is(err, core.ErrNotFound):
		return 404, "not_found", err.Error()
	case errors.Is(err, core.ErrConflict):
		return 409, "conflict", err.Error()
	case errors.Is(err, core.ErrApprovalExpired):
		return 409, "approval_expired", err.Error()
	case errors.Is(err, core.ErrInvalid):
		return 400, "invalid_input", err.Error()
	default:
		return 503, "unavailable", "the request could not be durably completed; query its operation before attempting another action"
	}
}
func respond(w http.ResponseWriter, value any, err error) {
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		status, code, message := ErrorDetails(err)
		w.WriteHeader(status)
		value = map[string]any{"error": map[string]string{"code": code, "message": message}}
	}
	_ = json.NewEncoder(w).Encode(value)
}
