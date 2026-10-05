package httpapi

import "net/http"

func (a *API) registerCapacity(mux *http.ServeMux) {
	if a.Capacity != nil {
		a.Capacity.Register(mux)
	}
}
