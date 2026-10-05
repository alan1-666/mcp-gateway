package httpapi

import (
	"net/http"

	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/alan1-666/mcp-gateway/internal/identity"
)

func (a *API) registerResponsePolicies(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/tools/{id}/response-policy", func(w http.ResponseWriter, r *http.Request) {
		var input core.ResponsePolicyUpdateInput
		if err := decode(w, r, &input); err != nil {
			respond(w, nil, err)
			return
		}
		result, err := a.Service.UpdateToolResponsePolicy(r.Context(), identity.Actor(r.Context()), r.PathValue("id"), input)
		respond(w, result, err)
	})
	mux.HandleFunc("POST /api/v1/tools/{id}/response-policy/preview", func(w http.ResponseWriter, r *http.Request) {
		var input core.ResponsePolicyPreviewInput
		if err := decode(w, r, &input); err != nil {
			respond(w, nil, err)
			return
		}
		result, err := a.Service.PreviewToolResponsePolicy(r.Context(), identity.Actor(r.Context()), r.PathValue("id"), input)
		respond(w, result, err)
	})
}
