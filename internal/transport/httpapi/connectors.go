package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/alan1-666/mcp-gateway/internal/connectors"
	"github.com/alan1-666/mcp-gateway/internal/connectorwire"
	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/alan1-666/mcp-gateway/internal/identity"
)

func (a *API) registerConnectors(mux *http.ServeMux) {
	if a.Connectors == nil {
		return
	}
	register := func(pattern string, fn http.HandlerFunc) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			if !identity.CanManageClients(r.Context()) {
				respond(w, nil, core.ErrForbidden)
				return
			}
			fn(w, r)
		})
	}
	register("GET /api/v1/connectors", func(w http.ResponseWriter, r *http.Request) {
		items, err := a.Connectors.List(r.Context(), identity.Actor(r.Context()))
		respond(w, map[string]any{"items": items, "total": len(items)}, err)
	})
	register("POST /api/v1/connectors", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Name string `json:"name"`
		}
		if err := decode(w, r, &in); err != nil {
			respond(w, nil, err)
			return
		}
		value, err := a.Connectors.Create(r.Context(), identity.Actor(r.Context()), in.Name)
		respond(w, value, err)
	})
	register("POST /api/v1/connectors/{id}/revoke", func(w http.ResponseWriter, r *http.Request) {
		if !emptyBody(w, r) {
			return
		}
		value, err := a.Connectors.Revoke(r.Context(), identity.Actor(r.Context()), r.PathValue("id"))
		respond(w, value, err)
	})
}

// ConnectorHandler must be mounted outside browser/API authentication. Its
// single-purpose bearer token cannot authenticate to the management or MCP API.
func ConnectorHandler(service *connectors.Service) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /connector/v1/poll", func(w http.ResponseWriter, r *http.Request) {
		var in connectorwire.PollInput
		if err := connectorDecode(w, r, &in, 16<<10); err != nil {
			respond(w, nil, err)
			return
		}
		job, err := service.Poll(r.Context(), connectorToken(r), in)
		if err != nil {
			respond(w, nil, err)
			return
		}
		if job == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		respond(w, job, nil)
	})
	mux.HandleFunc("POST /connector/v1/jobs/{id}/start", func(w http.ResponseWriter, r *http.Request) {
		var in struct{}
		if err := connectorDecode(w, r, &in, 1024); err != nil {
			respond(w, nil, err)
			return
		}
		if err := service.Start(r.Context(), connectorToken(r), r.PathValue("id")); err != nil {
			respond(w, nil, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /connector/v1/jobs/{id}/result", func(w http.ResponseWriter, r *http.Request) {
		var in connectorwire.Result
		if err := connectorDecode(w, r, &in, connectors.MaxResultBytes); err != nil {
			respond(w, nil, err)
			return
		}
		if err := service.Complete(r.Context(), connectorToken(r), r.PathValue("id"), in); err != nil {
			respond(w, nil, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		// This is a machine channel, never a browser ambient-credential endpoint.
		// Reject duplicate authorization headers as well as any Cookie/Origin.
		if service == nil || len(r.Header.Values("Authorization")) != 1 || len(r.Header.Values("Origin")) > 0 || len(r.Header.Values("Cookie")) > 0 || connectorToken(r) == "" {
			respond(w, nil, core.ErrUnauthorized)
			return
		}
		mux.ServeHTTP(w, r)
	})
}
func connectorToken(r *http.Request) string {
	value := r.Header.Get("Authorization")
	if !strings.HasPrefix(value, "Bearer rgc_") || len(value) != 54 {
		return ""
	}
	return strings.TrimPrefix(value, "Bearer ")
}
func connectorDecode(w http.ResponseWriter, r *http.Request, out any, limit int64) error {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return core.ErrInvalid
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		return core.ErrInvalid
	}
	value, err := connectors.DecodeJSON(raw)
	if err != nil {
		return core.ErrInvalid
	}
	if _, ok := value.(map[string]any); !ok {
		return core.ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err = d.Decode(out); err != nil {
		return core.ErrInvalid
	}
	return nil
}
