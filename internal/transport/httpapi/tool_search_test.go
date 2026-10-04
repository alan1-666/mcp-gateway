package httpapi

import (
	"errors"
	"testing"

	"github.com/alan1-666/mcp-gateway/internal/core"
)

func TestToolSearchQueryBoundary(t *testing.T) {
	for _, raw := range []string{"limit=0", "limit=-1", "limit=51", "limit=1.5", "limit=", "limit=1&limit=2", "query=a&query=b", "cursor=x&cursor=y", "offset=0", "query=%GG", "query=a;b"} {
		t.Run(raw, func(t *testing.T) {
			if _, err := ParseToolSearch(raw); !errors.Is(err, core.ErrInvalid) {
				t.Fatalf("expected invalid search query, got %v", err)
			}
		})
	}
	input, err := ParseToolSearch("query=100%25_%5C&limit=1&cursor=opaque-position")
	if err != nil || input.Query != `100%_\` || input.Limit != 1 || input.Cursor != "opaque-position" {
		t.Fatalf("query round trip: %+v %v", input, err)
	}
	if input, err = ParseToolSearch(""); err != nil || input.Limit != 0 {
		t.Fatalf("omitted limit should use the service default: %+v %v", input, err)
	}
}
