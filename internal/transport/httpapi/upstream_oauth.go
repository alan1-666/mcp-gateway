package httpapi

import (
	"net/http"
	"net/url"

	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/alan1-666/mcp-gateway/internal/identity"
	"github.com/alan1-666/mcp-gateway/internal/upstreamoauth"
)

// An expired/revoked browser session also leaves the credential-bearing callback
// URL immediately. Authentication remains mandatory; no code exchange occurs.
type oauthCallbackResponse struct {
	http.ResponseWriter
	rejected bool
}

func (w *oauthCallbackResponse) WriteHeader(status int) {
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		w.rejected = true
		w.Header().Set("Location", "/console/?oauth=reconnect_required")
		w.ResponseWriter.WriteHeader(http.StatusSeeOther)
		return
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w *oauthCallbackResponse) Write(body []byte) (int, error) {
	if w.rejected {
		return len(body), nil
	}
	return w.ResponseWriter.Write(body)
}

func (a *API) registerUpstreamOAuth(mux *http.ServeMux) {
	if a.UpstreamOAuth == nil {
		return
	}
	register := func(pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Referrer-Policy", "no-referrer")
			if identity.BrowserSessionBinding(r.Context()) == "" || !identity.CanManageClients(r.Context()) {
				respond(w, nil, core.ErrForbidden)
				return
			}
			handler(w, r)
		})
	}
	register("GET /api/v1/mcp/servers/{id}/oauth", func(w http.ResponseWriter, r *http.Request) {
		value, err := a.UpstreamOAuth.Status(r.Context(), identity.Actor(r.Context()), r.PathValue("id"))
		respond(w, value, err)
	})
	register("POST /api/v1/mcp/servers/{id}/oauth/discover", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Issuer string `json:"issuer"`
		}
		if err := decode(w, r, &in); err != nil {
			respond(w, nil, err)
			return
		}
		value, err := a.UpstreamOAuth.Discover(r.Context(), identity.Actor(r.Context()), r.PathValue("id"), in.Issuer)
		respond(w, value, err)
	})
	register("PUT /api/v1/mcp/servers/{id}/oauth", func(w http.ResponseWriter, r *http.Request) {
		var in upstreamoauth.ConfigInput
		if err := decode(w, r, &in); err != nil {
			respond(w, nil, err)
			return
		}
		value, err := a.UpstreamOAuth.Configure(r.Context(), identity.Actor(r.Context()), r.PathValue("id"), in)
		respond(w, value, err)
	})
	register("POST /api/v1/mcp/servers/{id}/oauth/connect", func(w http.ResponseWriter, r *http.Request) {
		var in upstreamoauth.VersionInput
		if err := decode(w, r, &in); err != nil {
			respond(w, nil, err)
			return
		}
		value, err := a.UpstreamOAuth.Connect(r.Context(), identity.Actor(r.Context()), r.PathValue("id"), in, identity.BrowserSessionBinding(r.Context()))
		respond(w, value, err)
	})
	register("POST /api/v1/mcp/servers/{id}/oauth/disconnect", func(w http.ResponseWriter, r *http.Request) {
		var in upstreamoauth.VersionInput
		if err := decode(w, r, &in); err != nil {
			respond(w, nil, err)
			return
		}
		value, err := a.UpstreamOAuth.Disconnect(r.Context(), identity.Actor(r.Context()), r.PathValue("id"), in)
		respond(w, value, err)
	})
	register("GET "+upstreamoauth.CallbackPath, func(w http.ResponseWriter, r *http.Request) {
		q, err := oauthCallbackQuery(r.URL.RawQuery)
		if err == nil {
			err = a.UpstreamOAuth.Complete(r.Context(), identity.Actor(r.Context()), identity.BrowserSessionBinding(r.Context()), q.Get("state"), q.Get("code"), q.Get("error"), q.Get("iss"))
		}
		outcome := "connected"
		if err != nil {
			outcome = "reconnect_required"
		}
		// Neither provider-controlled errors nor credentials are reflected in the URI.
		http.Redirect(w, r, "/console/?oauth="+outcome, http.StatusSeeOther)
	})
}
func oauthCallbackQuery(raw string) (url.Values, error) {
	if len(raw) > 16384 {
		return nil, core.ErrInvalid
	}
	q, err := url.ParseQuery(raw)
	if err != nil {
		return nil, core.ErrInvalid
	}
	for key, values := range q {
		switch key {
		case "code", "state", "error", "iss", "error_description", "error_uri", "session_state":
		default:
			delete(q, key) // RFC 6749: ignore unrecognized authorization response fields.
			continue
		}
		if len(values) != 1 {
			return nil, core.ErrInvalid
		}
	}
	return q, nil
}
