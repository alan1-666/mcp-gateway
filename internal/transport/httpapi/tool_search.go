package httpapi

import (
	"fmt"
	"net/url"
	"strconv"

	"github.com/alan1-666/mcp-gateway/internal/core"
)

// ParseToolSearch keeps the public and leased HTTP discovery contracts identical.
// ParseQuery errors must not silently discard part of a malformed query string.
func ParseToolSearch(raw string) (core.ToolSearchInput, error) {
	input := core.ToolSearchInput{}
	values, err := url.ParseQuery(raw)
	if err != nil {
		return input, fmt.Errorf("%w: invalid tool search query string", core.ErrInvalid)
	}
	for name, entries := range values {
		if len(entries) != 1 {
			return input, fmt.Errorf("%w: tool search parameters must not be repeated", core.ErrInvalid)
		}
		switch name {
		case "query":
			input.Query = entries[0]
		case "cursor":
			input.Cursor = entries[0]
		case "limit":
			limit, err := strconv.Atoi(entries[0])
			if err != nil || limit < 1 || limit > 50 {
				return input, fmt.Errorf("%w: limit must be an integer from 1 to 50", core.ErrInvalid)
			}
			input.Limit = limit
		default:
			return input, fmt.Errorf("%w: unsupported tool search parameter", core.ErrInvalid)
		}
	}
	return input, nil
}
