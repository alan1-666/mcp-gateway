package httpapi

import (
	"fmt"
	"net/http"

	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/alan1-666/mcp-gateway/internal/identity"
	"github.com/alan1-666/mcp-gateway/internal/upstreams"
)

func (a *API) registerUpstreams(mux *http.ServeMux) {
	if a.Upstreams == nil {
		return
	}
	mux.HandleFunc("GET /api/v1/mcp/servers/{id}/execution-metrics", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" {
			respond(w, nil, fmt.Errorf("%w: execution metrics accept no query parameters", core.ErrInvalid))
			return
		}
		value, err := a.Upstreams.ExecutionMetrics(r.Context(), identity.Actor(r.Context()), r.PathValue("id"))
		respond(w, value, err)
	})
	mux.HandleFunc("GET /api/v1/mcp/servers/{id}/catalog-schedule", func(w http.ResponseWriter, r *http.Request) {
		value, err := a.Upstreams.CatalogSchedule(r.Context(), identity.Actor(r.Context()), r.PathValue("id"))
		respond(w, value, err)
	})
	mux.HandleFunc("PUT /api/v1/mcp/servers/{id}/catalog-schedule", func(w http.ResponseWriter, r *http.Request) {
		var in upstreams.ScheduleInput
		if err := decode(w, r, &in); err != nil {
			respond(w, nil, err)
			return
		}
		value, err := a.Upstreams.SetCatalogSchedule(r.Context(), identity.Actor(r.Context()), r.PathValue("id"), in)
		respond(w, value, err)
	})

	mux.HandleFunc("GET /api/v1/mcp/servers/{id}/catalog-reviews", func(w http.ResponseWriter, r *http.Request) {
		q, err := strictCheckQuery(r)
		if err != nil {
			respond(w, nil, err)
			return
		}
		value, err := a.Upstreams.CatalogReviews(r.Context(), identity.Actor(r.Context()), r.PathValue("id"), q.limit, q.before)
		respond(w, value, err)
	})
	mux.HandleFunc("GET /api/v1/mcp/servers", func(w http.ResponseWriter, r *http.Request) {
		value, err := a.Upstreams.List(r.Context(), identity.Actor(r.Context()))
		respond(w, value, err)
	})
	mux.HandleFunc("POST /api/v1/mcp/servers", func(w http.ResponseWriter, r *http.Request) {
		var input core.MCPServerInput
		if err := decode(w, r, &input); err != nil {
			respond(w, nil, err)
			return
		}
		value, err := a.Upstreams.Create(r.Context(), identity.Actor(r.Context()), input)
		respond(w, value, err)
	})
	mux.HandleFunc("POST /api/v1/mcp/servers/{id}/enabled", func(w http.ResponseWriter, r *http.Request) {
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
		value, err := a.Upstreams.SetEnabled(r.Context(), identity.Actor(r.Context()), r.PathValue("id"), *input.Enabled)
		respond(w, value, err)
	})
	mux.HandleFunc("POST /api/v1/mcp/servers/{id}/discover", func(w http.ResponseWriter, r *http.Request) {
		if !emptyBody(w, r) {
			return
		}
		value, err := a.Upstreams.Discover(r.Context(), identity.Actor(r.Context()), r.PathValue("id"))
		respond(w, value, err)
	})
	mux.HandleFunc("POST /api/v1/mcp/servers/{id}/import", func(w http.ResponseWriter, r *http.Request) {
		var input upstreams.ImportInput
		if err := decode(w, r, &input); err != nil {
			respond(w, nil, err)
			return
		}
		value, err := a.Upstreams.Import(r.Context(), identity.Actor(r.Context()), r.PathValue("id"), input)
		respond(w, value, err)
	})
}
