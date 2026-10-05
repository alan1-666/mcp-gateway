package httpapi

import "net/http"

func (a *API) registerClients(mux *http.ServeMux) {
	if a.Clients != nil {
		a.Clients.Register(mux)
	}
}
