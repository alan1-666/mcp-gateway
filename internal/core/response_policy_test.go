package core

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestResponsePolicyValidation(t *testing.T) {
	cases := []*ResponsePolicy{
		{MaxBytes: 100}, {MaxBytes: 131073}, {Include: []string{"field"}}, {Include: []string{"/"}}, {Include: []string{"/a//b"}},
		{Include: []string{"/a~2b"}}, {Include: []string{"/a~"}}, {Include: []string{"/a", "/a"}}, {Include: []string{"/a", "/a/b"}},
		{Include: []string{"/a/*"}}, {Include: []string{"/" + strings.Repeat("中", 86)}}, {Include: make([]string, 33)},
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
