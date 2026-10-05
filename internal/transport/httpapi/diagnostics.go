package httpapi

import (
	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/alan1-666/mcp-gateway/internal/identity"
	"net/http"
	"net/url"
	"strconv"
)

func (a *API) registerDiagnostics(mux *http.ServeMux) {
	if a.Upstreams == nil {
		return
	}
	mux.HandleFunc("POST /api/v1/mcp/servers/{id}/check", func(w http.ResponseWriter, r *http.Request) {
		if !emptyBody(w, r) {
			return
		}
		value, err := a.Upstreams.Check(r.Context(), identity.Actor(r.Context()), r.PathValue("id"))
		respond(w, value, err)
	})
	mux.HandleFunc("GET /api/v1/mcp/servers/{id}/checks", func(w http.ResponseWriter, r *http.Request) {
		query, err := strictCheckQuery(r)
		if err != nil {
			respond(w, nil, err)
			return
		}
		value, err := a.Upstreams.Checks(r.Context(), identity.Actor(r.Context()), r.PathValue("id"), query.limit, query.before)
		respond(w, value, err)
	})
}

type checkQuery struct {
	limit  int
	before int64
}

func strictCheckQuery(r *http.Request) (checkQuery, error) {
	q := checkQuery{limit: 20}
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return q, core.ErrInvalid
	}
	for name, value := range values {
		if len(value) != 1 || value[0] == "" {
			return q, core.ErrInvalid
		}
		switch name {
		case "limit":
			q.limit, err = strconv.Atoi(value[0])
		case "before":
			q.before, err = strconv.ParseInt(value[0], 10, 64)
		default:
			return q, core.ErrInvalid
		}
		if err != nil {
			return q, core.ErrInvalid
		}
	}
	if q.limit < 1 || q.limit > 50 || q.before < 0 {
		return q, core.ErrInvalid
	}
	return q, nil
}
