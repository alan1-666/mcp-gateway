package runs

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/alan1-666/mcp-gateway/internal/execution"
	"github.com/alan1-666/mcp-gateway/internal/identity"
	"github.com/alan1-666/mcp-gateway/internal/transport/httpapi"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Config struct {
	WorkspaceID  string
	SharedSecret string
}
type API struct {
	Repository *Repository
	service    *core.Service
	executor   *execution.Executor
	workspace  string
	secretHash [32]byte
}

func New(pool *pgxpool.Pool, service *core.Service, executor *execution.Executor, config Config) (*API, error) {
	if pool == nil || service == nil || executor == nil || executor.Adapter == nil {
		return nil, fmt.Errorf("run repository, service and executor are required")
	}
	if config.WorkspaceID == "" {
		config.WorkspaceID = "team"
	}
	if !keyOK(config.WorkspaceID, 128) || len(config.SharedSecret) < 32 || len(config.SharedSecret) > 4096 || strings.TrimSpace(config.SharedSecret) != config.SharedSecret {
		return nil, fmt.Errorf("runner workspace and a daemon secret of at least 32 bytes are required")
	}
	return &API{Repository: NewRepository(pool), service: service, executor: executor, workspace: config.WorkspaceID, secretHash: sha256.Sum256([]byte(config.SharedSecret))}, nil
}
func respond(w http.ResponseWriter, v any, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	status := 200
	if err != nil {
		code, message := "internal_error", "Run service is unavailable."
		status = 503
		switch {
		case errors.Is(err, core.ErrUnauthorized):
			status, code, message = 401, "unauthorized", "A valid daemon credential is required."
		case errors.Is(err, core.ErrForbidden):
			status, code, message = 403, "forbidden", "The task creator no longer has operator access."
		case errors.Is(err, core.ErrNotFound):
			status, code, message = 404, "not_found", "Resource not found."
		case errors.Is(err, ErrLease):
			status, code, message = 409, "lease_expired", "The task lease is no longer active."
		case errors.Is(err, ErrBudget):
			status, code, message = 409, "budget_exhausted", "The task tool budget is exhausted."
		case errors.Is(err, core.ErrConflict), errors.Is(err, core.ErrApprovalExpired):
			status, code, message = 409, "conflict", err.Error()
		case errors.Is(err, core.ErrInvalid):
			status, code, message = 400, "invalid_input", err.Error()
		}
		v = map[string]any{"error": map[string]string{"code": code, "message": message}}
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func decode(w http.ResponseWriter, r *http.Request, out any) bool {
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		respond(w, nil, invalid("Content-Type must be application/json"))
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, core.MaxArgumentsBytes+(32<<10))
	body, err := io.ReadAll(r.Body)
	if err != nil {
		respond(w, nil, invalid("request body exceeds allowed bounds"))
		return false
	}
	body = bytes.TrimSpace(body)
	if len(body) == 0 || body[0] != '{' {
		respond(w, nil, invalid("a JSON object is required"))
		return false
	}
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		respond(w, nil, invalid("a bounded JSON object is required"))
		return false
	}
	if err := d.Decode(new(any)); err != io.EOF {
		respond(w, nil, invalid("unexpected trailing JSON"))
		return false
	}
	return true
}
func empty(w http.ResponseWriter, r *http.Request) bool { var in struct{}; return decode(w, r, &in) }
func (a *API) PublicHandler(auth *identity.Auth) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/runs", func(w http.ResponseWriter, r *http.Request) {
		v, e := a.Repository.List(r.Context(), identity.Actor(r.Context()))
		respond(w, map[string]any{"items": v}, e)
	})
	mux.HandleFunc("POST /api/v1/runs", func(w http.ResponseWriter, r *http.Request) {
		var in CreateInput
		if !decode(w, r, &in) {
			return
		}
		v, e := a.Repository.Create(r.Context(), identity.Actor(r.Context()), in)
		respond(w, v, e)
	})
	mux.HandleFunc("GET /api/v1/runs/runtime", func(w http.ResponseWriter, r *http.Request) {
		actor := identity.Actor(r.Context())
		if actor.WorkspaceID != a.workspace {
			respond(w, Runtime{ErrorCode: "RUNNER_OFFLINE"}, nil)
			return
		}
		v, e := a.Repository.Runtime(r.Context(), actor.WorkspaceID)
		respond(w, v, e)
	})
	mux.HandleFunc("GET /api/v1/runs/{id}", func(w http.ResponseWriter, r *http.Request) {
		v, e := a.Repository.Get(r.Context(), identity.Actor(r.Context()), r.PathValue("id"))
		respond(w, v, e)
	})
	mux.HandleFunc("GET /api/v1/runs/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		var after int64
		var err error
		if s := r.URL.Query().Get("after"); s != "" {
			after, err = strconv.ParseInt(s, 10, 64)
		}
		if err != nil || after < 0 {
			respond(w, nil, invalid("cursor must be a nonnegative decimal integer"))
			return
		}
		v, e := a.Repository.Events(r.Context(), identity.Actor(r.Context()), r.PathValue("id"), after)
		respond(w, map[string]any{"items": v}, e)
	})
	mux.HandleFunc("POST /api/v1/runs/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		if !empty(w, r) {
			return
		}
		v, e := a.Repository.Cancel(r.Context(), identity.Actor(r.Context()), r.PathValue("id"))
		respond(w, v, e)
	})
	mux.HandleFunc("POST /api/v1/runs/{id}/resume", func(w http.ResponseWriter, r *http.Request) {
		if !empty(w, r) {
			return
		}
		v, e := a.Repository.Resume(r.Context(), identity.Actor(r.Context()), r.PathValue("id"))
		respond(w, v, e)
	})
	return auth.Middleware(mux)
}
func (a *API) InternalHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /internal/runner/status", func(w http.ResponseWriter, r *http.Request) {
		var in StatusInput
		if !decode(w, r, &in) {
			return
		}
		err := a.Repository.Status(r.Context(), a.workspace, in)
		respond(w, map[string]bool{"ok": true}, err)
	})
	mux.HandleFunc("POST /internal/runner/claim", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			WorkerID string `json:"worker_id"`
		}
		if !decode(w, r, &in) {
			return
		}
		v, e := a.Repository.Claim(r.Context(), a.workspace, in.WorkerID)
		respond(w, v, e)
	})
	mux.HandleFunc("POST /internal/runner/runs/{id}/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		if !empty(w, r) {
			return
		}
		v, e := a.Repository.Heartbeat(r.Context(), a.workspace, r.PathValue("id"), r.Header.Get("X-Run-Lease"))
		respond(w, map[string]any{"ok": true, "lease_expires_at": v}, e)
	})
	mux.HandleFunc("POST /internal/runner/runs/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		var in EventInput
		if !decode(w, r, &in) {
			return
		}
		e := a.Repository.Append(r.Context(), a.workspace, r.PathValue("id"), r.Header.Get("X-Run-Lease"), in)
		respond(w, map[string]bool{"ok": true}, e)
	})
	mux.HandleFunc("POST /internal/runner/runs/{id}/finish", func(w http.ResponseWriter, r *http.Request) {
		var in FinishInput
		if !decode(w, r, &in) {
			return
		}
		v, e := a.Repository.Finish(r.Context(), a.workspace, r.PathValue("id"), r.Header.Get("X-Run-Lease"), in)
		respond(w, v, e)
	})
	mux.HandleFunc("GET /internal/runner/runs/{id}/gateway/me", func(w http.ResponseWriter, r *http.Request) { v, e := a.actor(r); respond(w, v, e) })
	mux.HandleFunc("GET /internal/runner/runs/{id}/gateway/tools", a.listTools)
	mux.HandleFunc("GET /internal/runner/runs/{id}/gateway/tools/{tool}", func(w http.ResponseWriter, r *http.Request) {
		actor, err := a.actor(r)
		if err != nil {
			respond(w, nil, err)
			return
		}
		v, e := a.service.GetTool(r.Context(), actor, r.PathValue("tool"))
		if e == nil && (!v.Enabled || v.Status != "published") {
			e = core.ErrNotFound
		}
		respond(w, v, e)
	})
	mux.HandleFunc("POST /internal/runner/runs/{id}/gateway/operations", func(w http.ResponseWriter, r *http.Request) {
		var in core.PrepareInput
		if !decode(w, r, &in) {
			return
		}
		v, e := a.Repository.Prepare(r.Context(), a.service, a.workspace, r.PathValue("id"), r.Header.Get("X-Run-Lease"), in)
		respond(w, v, e)
	})
	mux.HandleFunc("GET /internal/runner/runs/{id}/gateway/operations/{operation}", func(w http.ResponseWriter, r *http.Request) {
		v, e := a.Repository.Operation(r.Context(), a.service, a.workspace, r.PathValue("id"), r.Header.Get("X-Run-Lease"), r.PathValue("operation"))
		respond(w, v, e)
	})
	mux.HandleFunc("POST /internal/runner/runs/{id}/gateway/operations/{operation}/execute", func(w http.ResponseWriter, r *http.Request) {
		if !empty(w, r) {
			return
		}
		v, e := a.Repository.Execute(r.Context(), a.executor, a.workspace, r.PathValue("id"), r.Header.Get("X-Run-Lease"), r.PathValue("operation"))
		respond(w, v, e)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Fields(r.Header.Get("Authorization"))
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			respond(w, nil, core.ErrUnauthorized)
			return
		}
		candidate := sha256.Sum256([]byte(parts[1]))
		if subtle.ConstantTimeCompare(candidate[:], a.secretHash[:]) != 1 {
			respond(w, nil, core.ErrUnauthorized)
			return
		}
		mux.ServeHTTP(w, r)
	})
}
func (a *API) actor(r *http.Request) (core.Actor, error) {
	return a.Repository.GatewayActor(r.Context(), a.workspace, r.PathValue("id"), r.Header.Get("X-Run-Lease"))
}
func (a *API) listTools(w http.ResponseWriter, r *http.Request) {
	actor, err := a.actor(r)
	if err != nil {
		respond(w, nil, err)
		return
	}
	input, err := httpapi.ParseToolSearch(r.URL.RawQuery)
	if err != nil {
		respond(w, nil, err)
		return
	}
	page, err := a.service.DiscoverTools(r.Context(), actor, input)
	respond(w, page, err)
}
