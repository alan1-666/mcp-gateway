package httpapi

import (
	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/alan1-666/mcp-gateway/internal/identity"
	"github.com/alan1-666/mcp-gateway/internal/releases"
	"net/http"
	"net/url"
	"strconv"
)

func (a *API) registerReleases(mux *http.ServeMux) {
	if a.Releases == nil {
		return
	}
	mux.HandleFunc("GET /api/v1/tools/{id}/versions", func(w http.ResponseWriter, r *http.Request) {
		q, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil {
			respond(w, nil, core.ErrInvalid)
			return
		}
		limit, before := 20, 0
		for k, v := range q {
			if len(v) != 1 || v[0] == "" {
				respond(w, nil, core.ErrInvalid)
				return
			}
			switch k {
			case "limit":
				limit, err = strconv.Atoi(v[0])
			case "before":
				before, err = strconv.Atoi(v[0])
			default:
				err = core.ErrInvalid
			}
			if err != nil {
				respond(w, nil, core.ErrInvalid)
				return
			}
		}
		result, err := a.Releases.Versions(r.Context(), identity.Actor(r.Context()), r.PathValue("id"), before, limit)
		respond(w, result, err)
	})
	mux.HandleFunc("GET /api/v1/tools/{id}/candidates", func(w http.ResponseWriter, r *http.Request) {
		result, err := a.Releases.Candidates(r.Context(), identity.Actor(r.Context()), r.PathValue("id"))
		respond(w, map[string]any{"items": result}, err)
	})
	mux.HandleFunc("POST /api/v1/tools/{id}/candidates", func(w http.ResponseWriter, r *http.Request) {
		var in releases.CandidateInput
		if err := decode(w, r, &in); err != nil {
			respond(w, nil, err)
			return
		}
		result, err := a.Releases.Create(r.Context(), identity.Actor(r.Context()), r.PathValue("id"), in)
		respond(w, result, err)
	})
	mux.HandleFunc("GET /api/v1/tools/{id}/candidates/{candidate}", func(w http.ResponseWriter, r *http.Request) {
		result, err := a.Releases.Candidate(r.Context(), identity.Actor(r.Context()), r.PathValue("id"), r.PathValue("candidate"))
		respond(w, result, err)
	})
	mux.HandleFunc("POST /api/v1/tools/{id}/candidates/{candidate}/discard", func(w http.ResponseWriter, r *http.Request) {
		if !emptyBody(w, r) {
			return
		}
		err := a.Releases.Discard(r.Context(), identity.Actor(r.Context()), r.PathValue("id"), r.PathValue("candidate"))
		respond(w, map[string]bool{"discarded": err == nil}, err)
	})
	mux.HandleFunc("POST /api/v1/tools/{id}/candidates/{candidate}/publish", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			ExpectedVersion int `json:"expected_version"`
		}
		if err := decode(w, r, &in); err != nil {
			respond(w, nil, err)
			return
		}
		result, err := a.Releases.Publish(r.Context(), identity.Actor(r.Context()), r.PathValue("id"), r.PathValue("candidate"), in.ExpectedVersion)
		respond(w, result, err)
	})
	mux.HandleFunc("POST /api/v1/tools/{id}/retire", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			ExpectedVersion int `json:"expected_version"`
		}
		if err := decode(w, r, &in); err != nil {
			respond(w, nil, err)
			return
		}
		result, err := a.Releases.Retire(r.Context(), identity.Actor(r.Context()), r.PathValue("id"), in.ExpectedVersion)
		respond(w, result, err)
	})
}
