package httpapi

import (
	"github.com/alan1-666/mcp-gateway/internal/credentials"
	"github.com/alan1-666/mcp-gateway/internal/identity"
	"net/http"
)

func (a *API) registerCredentials(mux *http.ServeMux) {
	if a.Credentials == nil {
		return
	}
	mux.HandleFunc("GET /api/v1/credentials", func(w http.ResponseWriter, r *http.Request) {
		value, err := a.Credentials.List(r.Context(), identity.Actor(r.Context()))
		respond(w, value, err)
	})
	mux.HandleFunc("POST /api/v1/credentials", func(w http.ResponseWriter, r *http.Request) {
		var in credentials.CreateInput
		if err := decode(w, r, &in); err != nil {
			respond(w, nil, err)
			return
		}
		value, err := a.Credentials.Create(r.Context(), identity.Actor(r.Context()), in)
		respond(w, value, err)
	})
	mux.HandleFunc("POST /api/v1/credentials/{ref}/rotate", func(w http.ResponseWriter, r *http.Request) {
		var in credentials.RotateInput
		if err := decode(w, r, &in); err != nil {
			respond(w, nil, err)
			return
		}
		value, err := a.Credentials.Rotate(r.Context(), identity.Actor(r.Context()), r.PathValue("ref"), in)
		respond(w, value, err)
	})
	mux.HandleFunc("POST /api/v1/credentials/{ref}/enabled", func(w http.ResponseWriter, r *http.Request) {
		var in credentials.EnabledInput
		if err := decode(w, r, &in); err != nil {
			respond(w, nil, err)
			return
		}
		value, err := a.Credentials.SetEnabled(r.Context(), identity.Actor(r.Context()), r.PathValue("ref"), in)
		respond(w, value, err)
	})
}
