package httpapi

import (
	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/alan1-666/mcp-gateway/internal/identity"
	"github.com/alan1-666/mcp-gateway/internal/observability"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

func (a *API) registerObservability(mux *http.ServeMux) {
	if a.Observability == nil {
		return
	}
	mux.HandleFunc("GET /api/v1/audit", func(w http.ResponseWriter, r *http.Request) {
		in, err := historyQuery(r, "audit")
		if err != nil {
			respond(w, nil, err)
			return
		}
		page, err := a.Observability.Audit(r.Context(), identity.Actor(r.Context()), in)
		respond(w, page, err)
	})
	mux.HandleFunc("GET /api/v1/operations/{id}/reconciliations", func(w http.ResponseWriter, r *http.Request) {
		in, err := historyQuery(r, "reconciliations")
		if err != nil {
			respond(w, nil, err)
			return
		}
		page, err := a.Observability.Reconciliations(r.Context(), identity.Actor(r.Context()), r.PathValue("id"), in)
		respond(w, page, err)
	})
	mux.HandleFunc("POST /api/v1/operations/{id}/reconciliations", func(w http.ResponseWriter, r *http.Request) {
		var in observability.ReconcileInput
		if err := decode(w, r, &in); err != nil {
			respond(w, nil, err)
			return
		}
		record, err := a.Observability.Reconcile(r.Context(), identity.Actor(r.Context()), r.PathValue("id"), in)
		respond(w, record, err)
	})
}
func (a *API) listOperationsPage(w http.ResponseWriter, r *http.Request) {
	in, err := historyQuery(r, "operations")
	if err != nil {
		respond(w, nil, err)
		return
	}
	page, err := a.Observability.Operations(r.Context(), identity.Actor(r.Context()), in)
	respond(w, page, err)
}
func historyQuery(r *http.Request, kind string) (observability.Filter, error) {
	in := observability.Filter{Limit: 50}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return in, core.ErrInvalid
	}
	for name, values := range query {
		if len(values) != 1 || values[0] == "" {
			return in, core.ErrInvalid
		}
		value := values[0]
		switch name {
		case "limit":
			in.Limit, err = strconv.Atoi(value)
			if err != nil || in.Limit < 1 || in.Limit > 100 {
				return in, core.ErrInvalid
			}
		case "cursor":
			in.Cursor = value
		case "actor_id":
			if kind == "reconciliations" {
				return in, core.ErrInvalid
			}
			in.ActorID = value
		case "from", "to":
			if kind == "reconciliations" {
				return in, core.ErrInvalid
			}
			parsed, err := time.Parse(time.RFC3339Nano, value)
			if err != nil {
				return in, core.ErrInvalid
			}
			parsed = parsed.UTC()
			if name == "from" {
				in.From = &parsed
			} else {
				in.To = &parsed
			}
		case "action":
			if kind != "audit" {
				return in, core.ErrInvalid
			}
			in.Action = value
		case "resource_id":
			if kind != "audit" {
				return in, core.ErrInvalid
			}
			in.ResourceID = value
		case "state":
			if kind != "operations" {
				return in, core.ErrInvalid
			}
			in.State = core.State(value)
		case "tool_id":
			if kind != "operations" {
				return in, core.ErrInvalid
			}
			in.ToolID = value
		default:
			return in, core.ErrInvalid
		}
	}
	return in, nil
}
