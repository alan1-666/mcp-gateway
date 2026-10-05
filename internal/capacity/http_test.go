package capacity

import (
	"errors"
	"net/http/httptest"
	"testing"
)

func TestInternalFailureIsNeverHTTPSuccess(t *testing.T) {
	recorder := httptest.NewRecorder()
	respond(recorder, nil, errors.New("fixture database unavailable"))
	if recorder.Code != 503 {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("missing cache protection")
	}
}
