package httpapi

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/alan1-666/mcp-gateway/internal/identity"
)

func parseResultRead(id, rawQuery string) (core.ResultReadInput, error) {
	in := core.ResultReadInput{OperationID: id}
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return in, fmt.Errorf("%w: invalid result query", core.ErrInvalid)
	}
	for name, entries := range values {
		if len(entries) != 1 {
			return in, fmt.Errorf("%w: repeated result parameter", core.ErrInvalid)
		}
		switch name {
		case "cursor":
			in.Cursor = entries[0]
		case "limit_bytes":
			in.LimitBytes, err = strconv.Atoi(entries[0])
			if err != nil || in.LimitBytes < 1024 || in.LimitBytes > 16384 {
				return in, fmt.Errorf("%w: limit_bytes must be 1024–16384", core.ErrInvalid)
			}
		default:
			return in, fmt.Errorf("%w: unsupported result parameter", core.ErrInvalid)
		}
	}
	return in, nil
}

func (a *API) registerResultReads(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/operations/{id}/result", func(w http.ResponseWriter, r *http.Request) {
		input, err := parseResultRead(r.PathValue("id"), r.URL.RawQuery)
		if err != nil {
			respond(w, nil, err)
			return
		}
		page, err := a.Service.ReadResult(r.Context(), identity.Actor(r.Context()), input)
		respond(w, page, err)
	})
}
