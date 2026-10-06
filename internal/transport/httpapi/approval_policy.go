package httpapi

import (
	"net/http"

	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/alan1-666/mcp-gateway/internal/identity"
)

func (a *API) registerApprovalPolicies(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/tools/{id}/approval-policy", func(w http.ResponseWriter, r *http.Request) {
		var input core.ApprovalPolicyUpdateInput
		if err := decode(w, r, &input); err != nil {
			respond(w, nil, err)
			return
		}
		result, err := a.Service.UpdateToolApprovalPolicy(r.Context(), identity.Actor(r.Context()), r.PathValue("id"), input)
		respond(w, result, err)
	})
}
