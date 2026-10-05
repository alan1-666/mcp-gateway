package core

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestResponsePolicyValidation(t *testing.T) {
	cases := []*ResponsePolicy{
		{MaxBytes: 100}, {MaxBytes: 131073}, {Include: []string{"field"}}, {Include: []string{"/"}}, {Include: []string{"/a//b"}},
		{Include: []string{"/a~2b"}}, {Include: []string{"/a~"}}, {Include: []string{"/a", "/a"}}, {Include: []string{"/a", "/a/b"}},
		{Include: []string{"/a/*"}}, {Include: []string{"/" + strings.Repeat("中", 86)}}, {Include: make([]string, 33)},
		{Include: []string{"/rows/a*b/id"}}, {Include: []string{"/rows/**/id"}}, {Include: []string{"/rows/*/"}},
		{Include: []string{"/rows/*/id", "/rows/*/id"}}, {Include: []string{"/rows", "/rows/*/id"}},
		{Include: []string{"/rows/*/user", "/rows/*/user/id"}},
		{Include: []string{"/rows/*/id", "/rows/0/id"}}, {Include: []string{"/rows/name", "/rows/*/id"}},
		{Include: []string{"/rows/*/nested/*/id", "/rows/*/nested/name"}},
		{Include: []string{"/a/\x00b"}}, {Include: []string{"/a/\xff"}},
	}
	for _, in := range cases {
		if _, err := NormalizeResponsePolicy(in); err == nil {
			t.Fatalf("accepted invalid policy: %+v", in)
		}
	}
	if p, err := NormalizeResponsePolicy(nil); err != nil || p.MaxBytes != 65536 {
		t.Fatalf("defaults: %+v %v", p, err)
	}
	if _, err := NormalizeResponsePolicy(&ResponsePolicy{Include: []string{"/a~1b/~0", "/numeric/0"}}); err != nil {
		t.Fatal(err)
	}
	valid := &ResponsePolicy{Include: []string{"/rows/*/name", "/rows/*/id", "/matrix/*/*/value", "/groups/*/members/*/name"}}
	before := append([]string(nil), valid.Include...)
	normalized, err := NormalizeResponsePolicy(valid)
	if err != nil || !reflect.DeepEqual(valid.Include, before) {
		t.Fatalf("valid paths rejected or input mutated: %v", err)
	}
	if normalized.Include[0] != "/groups/*/members/*/name" {
		t.Fatal("normalized selectors are not sorted")
	}
	if _, err := NormalizeResponsePolicy(&ResponsePolicy{Include: []string{"/" + strings.Repeat("a", 255)}}); err != nil {
		t.Fatal("256-byte selector should fit")
	}
}

func projectedStructured(t *testing.T, raw string, policy *ResponsePolicy) (map[string]any, json.RawMessage) {
	t.Helper()
	encoded, err := ApplyMCPResponsePolicy(json.RawMessage(raw), policy)
	if err != nil {
		t.Fatal(err)
	}
	value, err := DecodeResult(encoded)
	if err != nil {
		t.Fatal(err)
	}
	envelope := value.(map[string]any)
	structured := envelope["structuredContent"].(map[string]any)
	content := envelope["content"].([]any)
	if len(content) != 1 {
		t.Fatal("projected result must have one regenerated text block")
	}
	text := content[0].(map[string]any)["text"].(string)
	textValue, err := DecodeResult(json.RawMessage(text))
	if err != nil || !reflect.DeepEqual(textValue, structured) {
		t.Fatalf("text bypasses projected structured result: %v", err)
	}
	return structured, encoded
}

func TestMCPArrayProjectionMergesFieldsAndPreservesOrderPrecisionAndCursors(t *testing.T) {
	raw := `{"isError":false,"_meta":{"secret":"private metadata"},"content":[{"type":"text","text":"UNSELECTED original text"}],"structuredContent":{"results":[{"id":9007199254740993123,"title":"third","url":"https://example.test/3","secret":"UNSELECTED secret","extra":"UNSELECTED field"},{"id":1,"title":"first","url":null,"secret":"UNSELECTED secret","extra":"UNSELECTED field"},{"id":2,"title":"second","url":"https://example.test/2","secret":"UNSELECTED secret","extra":"UNSELECTED field"}],"nextCursor":"page-2","next_cursor":null,"excluded":"UNSELECTED root"}}`
	policy := &ResponsePolicy{Include: []string{"/results/*/title", "/results/*/id", "/results/*/url", "/results/*/secret"}}
	structured, encoded := projectedStructured(t, raw, policy)
	wantRaw := `{"results":[{"id":9007199254740993123,"title":"third","url":"https://example.test/3","secret":"[REDACTED]"},{"id":1,"title":"first","url":null,"secret":"[REDACTED]"},{"id":2,"title":"second","url":"https://example.test/2","secret":"[REDACTED]"}],"nextCursor":"page-2","next_cursor":null}`
	want, _ := DecodeResult(json.RawMessage(wantRaw))
	if !reflect.DeepEqual(structured, want) {
		t.Fatalf("array elements merged, reordered or lost: %s", encoded)
	}
	if strings.Contains(string(encoded), "UNSELECTED") || strings.Contains(string(encoded), "_meta") {
		t.Fatalf("unselected output leaked: %s", encoded)
	}
	reversed := &ResponsePolicy{Include: []string{"/results/*/secret", "/results/*/url", "/results/*/id", "/results/*/title"}}
	_, other := projectedStructured(t, raw, reversed)
	if string(encoded) != string(other) {
		t.Fatal("projection depends on selector order")
	}
}

func TestMCPArrayProjectionSupportsNestedArraysEmptyArraysAndEscapedKeys(t *testing.T) {
	raw := `{"isError":false,"structuredContent":{"groups":[{"name":"a","members":[{"id":2,"profile":{"label":null,"secret":"drop"}},{"id":1,"profile":{"label":"one","secret":"drop"}}],"omit":"drop"},{"name":"b","members":[],"omit":"drop"}],"matrix":[[{"value":"x","omit":"drop"}],[],[{"value":"y","omit":"drop"},{"value":"z","omit":"drop"}]],"empty":[],"a/b":[{"~":null,"omit":"drop"}],"numeric":{"0":{"name":"object key","omit":"drop"}}}}`
	policy := &ResponsePolicy{Include: []string{"/groups/*/name", "/groups/*/members/*/id", "/groups/*/members/*/profile/label", "/matrix/*/*/value", "/empty/*/missing", "/a~1b/*/~0", "/numeric/0/name"}}
	structured, encoded := projectedStructured(t, raw, policy)
	want, _ := DecodeResult(json.RawMessage(`{"groups":[{"name":"a","members":[{"id":2,"profile":{"label":null}},{"id":1,"profile":{"label":"one"}}]},{"name":"b","members":[]}],"matrix":[[{"value":"x"}],[],[{"value":"y"},{"value":"z"}]],"empty":[],"a/b":[{"~":null}],"numeric":{"0":{"name":"object key"}}}`))
	if !reflect.DeepEqual(structured, want) {
		t.Fatalf("nested/empty projection changed shape: %s", encoded)
	}
}

func TestMCPArrayProjectionFailsClosedForEveryElement(t *testing.T) {
	for _, tc := range []struct{ name, structured, path string }{
		{"missing-first", `{"rows":[{}, {"id":2}]}`, "/rows/*/id"},
		{"missing-last", `{"rows":[{"id":1},{}]}`, "/rows/*/id"},
		{"null-element", `{"rows":[{"id":1},null]}`, "/rows/*/id"},
		{"scalar-element", `{"rows":[{"id":1},2]}`, "/rows/*/id"},
		{"nested-array-element", `{"rows":[{"id":1},[{"id":2}]]}`, "/rows/*/id"},
		{"null-array", `{"rows":null}`, "/rows/*/id"},
		{"object-wildcard", `{"rows":{"one":{"id":1}}}`, "/rows/*/id"},
		{"literal-star-key", `{"rows":{"*":{"id":1}}}`, "/rows/*/id"},
		{"root-object-wildcard", `{"one":{"id":1}}`, "/*/id"},
		{"array-index", `{"rows":[{"id":1}]}`, "/rows/0/id"},
		{"array-field", `{"rows":[{"id":1}]}`, "/rows/id"},
		{"null-intermediate", `{"rows":[{"profile":null}]}`, "/rows/*/profile/id"},
		{"nested-mixed-type", `{"rows":[{"children":[{"id":1}]},{"children":{}}]}`, "/rows/*/children/*/id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := json.RawMessage(`{"isError":false,"content":[{"type":"text","text":"original fallback forbidden"}],"structuredContent":` + tc.structured + `}`)
			result, err := ApplyMCPResponsePolicy(raw, &ResponsePolicy{Include: []string{tc.path}})
			if err == nil || result != nil {
				t.Fatalf("must reject whole response without partial/fallback output: %s %v", result, err)
			}
		})
	}
	// Conflicting traversal types are rejected even when the source array is
	// empty, rather than being hidden by a vacuous per-element traversal.
	for _, include := range [][]string{{"/rows/*/id", "/rows/metadata"}, {"/rows/0/id", "/rows/*/id"}} {
		_, err := NormalizeResponsePolicy(&ResponsePolicy{Include: include})
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("conflicting selectors not rejected during normalization: %v", err)
		}
	}
}

func TestMCPArrayProjectionKeepsByteBudgetAndNoSilentTruncation(t *testing.T) {
	rows := []any{map[string]any{"id": 3, "extra": strings.Repeat("x", 12000)}, map[string]any{"id": 1, "extra": strings.Repeat("y", 12000)}, map[string]any{"id": 2, "extra": strings.Repeat("z", 12000)}}
	raw, _ := json.Marshal(map[string]any{"isError": false, "structuredContent": map[string]any{"rows": rows}})
	if result, err := ApplyMCPResponsePolicy(raw, &ResponsePolicy{MaxBytes: 1024, Include: []string{"/rows"}}); err == nil || result != nil {
		t.Fatal("oversized whole array was accepted or truncated")
	}
	structured, encoded := projectedStructured(t, string(raw), &ResponsePolicy{MaxBytes: 1024, Include: []string{"/rows/*/id"}})
	if len(encoded) > 1024 || len(structured["rows"].([]any)) != 3 {
		t.Fatal("bounded projection lost rows")
	}
	rows = make([]any, 300)
	for i := range rows {
		rows[i] = map[string]any{"id": i}
	}
	raw, _ = json.Marshal(map[string]any{"isError": false, "structuredContent": map[string]any{"rows": rows}})
	if result, err := ApplyMCPResponsePolicy(raw, &ResponsePolicy{MaxBytes: 1024, Include: []string{"/rows/*/id"}}); err == nil || result != nil {
		t.Fatal("oversized selected array was accepted or silently shortened")
	}
}
func TestMCPProjectionDoesNotLeakTextOrMetadata(t *testing.T) {
	raw := json.RawMessage(`{"isError":false,"_meta":{"private":"secret"},"content":[{"type":"text","text":"original confidential customer data"}],"structuredContent":{"order":{"id":9007199254740993123,"private":"excluded"},"next_cursor":"page-2","access_token":"secret","a/b":{"~":null},"rows":[{"id":1}]}}`)
	result, err := ApplyMCPResponsePolicy(raw, &ResponsePolicy{Include: []string{"/order/id", "/a~1b/~0", "/rows", "/access_token"}})
	if err != nil {
		t.Fatal(err)
	}
	s := string(result)
	for _, bad := range []string{"confidential", "excluded", "secret", "_meta"} {
		if strings.Contains(s, bad) {
			t.Fatalf("leaked %q: %s", bad, s)
		}
	}
	for _, want := range []string{"9007199254740993123", "[REDACTED]", "page-2", "gateway_projection"} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q: %s", want, s)
		}
	}
	decoded, err := DecodeResult(result)
	if err != nil {
		t.Fatal(err)
	}
	envelope := decoded.(map[string]any)
	text := envelope["content"].([]any)[0].(map[string]any)["text"].(string)
	textJSON, err := DecodeResult(json.RawMessage(text))
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(envelope["structuredContent"])
	b, _ := json.Marshal(textJSON)
	if string(a) != string(b) {
		t.Fatalf("text bypass: %s vs %s", a, b)
	}
	if strings.Contains(string(raw), "[REDACTED]") {
		t.Fatal("source was mutated")
	}
}
func TestMCPProjectionFailsClosed(t *testing.T) {
	for _, tc := range []struct{ raw, path string }{
		{`{"isError":true,"content":[]}`, "/id"},
		{`{"content":[]}`, "/id"},
		{`{"isError":false,"content":[{"type":"text","text":"text-only"}]}`, "/id"},
		{`{"isError":false,"structuredContent":{"rows":[{"id":1}]}}`, "/rows/0/id"},
		{`{"isError":false,"structuredContent":{"id":1}}`, "/missing"},
		{`{"isError":false,"structuredContent":{"id":1,"nextCursor":{"private":"excluded"}}}`, "/id"},
	} {
		if _, err := ApplyMCPResponsePolicy(json.RawMessage(tc.raw), &ResponsePolicy{Include: []string{tc.path}}); err == nil {
			t.Errorf("accepted %s", tc.raw)
		}
	}
	raw, _ := json.Marshal(map[string]any{"isError": false, "content": []any{}, "structuredContent": map[string]any{"id": 1, "extra": strings.Repeat("x", 3000)}})
	if _, err := ApplyMCPResponsePolicy(raw, &ResponsePolicy{MaxBytes: 1024}); err == nil {
		t.Fatal("oversized accepted")
	}
	if result, err := ApplyMCPResponsePolicy(raw, &ResponsePolicy{MaxBytes: 1024, Include: []string{"/id"}}); err != nil || len(result) > 1024 {
		t.Fatalf("projection should fit: %v", err)
	}
}
func TestMCPTextOnlyPolicyAndFullStructuredRedaction(t *testing.T) {
	raw := json.RawMessage(`{"isError":false,"content":[{"type":"text","text":"bounded plain text"}]}`)
	if result, err := ApplyMCPResponsePolicy(raw, nil); err != nil || !strings.Contains(string(result), "bounded plain text") {
		t.Fatal(err)
	}
	raw = json.RawMessage(`{"isError":false,"content":[{"type":"text","text":"RAW_SECRET"}],"structuredContent":{"password":"RAW_SECRET","data":{"API-Key":"RAW_SECRET"}}}`)
	result, err := ApplyMCPResponsePolicy(raw, nil)
	if err != nil || strings.Contains(string(result), "RAW_SECRET") {
		t.Fatalf("redaction bypass: %s %v", result, err)
	}
}
func TestNormalizeMCPTool(t *testing.T) {
	base := ToolInput{Name: "example__query", Description: "Query upstream", Risk: RiskWrite, InputSchema: json.RawMessage(`{"type":"object"}`), MCP: &MCPConfig{ServerID: "server", ToolName: "query", SchemaHash: strings.Repeat("a", 64)}}
	normalized, err := NormalizeTool(base)
	if err != nil || normalized.ResponsePolicy.MaxBytes != 65536 {
		t.Fatalf("MCP normalize: %+v %v", normalized, err)
	}
	base.HTTP = HTTPConfig{Method: "GET", URL: "https://example.com"}
	if _, err = NormalizeTool(base); err == nil {
		t.Fatal("mixed adapters accepted")
	}
}
