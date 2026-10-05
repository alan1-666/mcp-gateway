package httpapi

import (
	"net/http/httptest"
	"testing"
)

func TestCheckHistoryQueryBounds(t *testing.T) {
	for _, query := range []string{"limit=0", "limit=51", "limit=1.5", "limit=1&limit=2", "before=-1", "before=9223372036854775808", "before=", "unexpected=x", "before=%GG", "limit=1;x=y"} {
		if _, err := strictCheckQuery(httptest.NewRequest("GET", "/checks?"+query, nil)); err == nil {
			t.Fatalf("accepted malformed diagnostic query %q", query)
		}
	}
	q, err := strictCheckQuery(httptest.NewRequest("GET", "/checks?limit=10&before=42", nil))
	if err != nil || q.limit != 10 || q.before != 42 {
		t.Fatalf("query %+v %v", q, err)
	}
}
