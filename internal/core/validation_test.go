package core

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestCanonicalArguments(t *testing.T) {
	first, firstHash, err := CanonicalArguments(json.RawMessage(`{"b":2,"a":9007199254740993}`))
	if err != nil {
		t.Fatal(err)
	}
	second, secondHash, err := CanonicalArguments(json.RawMessage(`{ "a":9007199254740993, "b":2 }`))
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != `{"a":9007199254740993,"b":2}` || string(first) != string(second) || firstHash != secondHash {
		t.Fatalf("canonicalization changed integer precision or failed key ordering: %s %s", first, second)
	}
	for _, invalid := range []string{`[]`, `null`, `{} {}`, `{"value":`, `{"value":1e99999999}`, `{"value":"\u0000"}`, strings.Repeat("[", 65) + "0" + strings.Repeat("]", 65)} {
		if _, _, err := CanonicalArguments(json.RawMessage(invalid)); !errors.Is(err, ErrInvalid) {
			t.Fatalf("expected invalid input for %q, got %v", invalid, err)
		}
	}
}

func TestSchemaValidationAndReferenceBoundary(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"count":{"type":"integer","minimum":1}},"required":["count"],"additionalProperties":false}`)
	if err := ValidateArguments(schema, json.RawMessage(`{"count":2}`)); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`{"count":0}`, `{"count":"2"}`, `{"count":2,"unexpected":true}`, `{}`} {
		if err := ValidateArguments(schema, json.RawMessage(raw)); !errors.Is(err, ErrInvalid) {
			t.Fatalf("unexpected validation result for %s: %v", raw, err)
		}
	}
	for _, raw := range []string{
		`{"type":"object","$ref":"http://169.254.169.254/latest/meta-data/"}`,
		`{"type":"object","$ref":"file:///etc/passwd"}`,
		`{"type":"array"}`,
		`true`,
	} {
		if _, err := CompileSchema(json.RawMessage(raw), true); !errors.Is(err, ErrInvalid) {
			t.Fatalf("schema unexpectedly accepted: %s", raw)
		}
	}
	if _, err := CompileSchema(json.RawMessage(`{"type":"object","properties":{"name":{"$ref":"#/$defs/name"}},"$defs":{"name":{"type":"string"}}}`), true); err != nil {
		t.Fatalf("local definitions should work: %v", err)
	}
}

func TestResultStorageAndNumericBounds(t *testing.T) {
	for _, raw := range []string{`{"value":1e999999999}`, `{"value":"\u0000"}`, strings.Repeat("[", 65) + "0" + strings.Repeat("]", 65)} {
		if _, err := DecodeResult(json.RawMessage(raw)); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid result accepted: %s", raw)
		}
	}
	value, err := DecodeResult(json.RawMessage(`{"id":9007199254740993}`))
	if err != nil {
		t.Fatal(err)
	}
	if value.(map[string]any)["id"].(json.Number).String() != "9007199254740993" {
		t.Fatal("result lost integer precision")
	}
}

func TestToolValidation(t *testing.T) {
	base := ToolInput{Name: "service.status", Description: "Read service status", Risk: RiskRead, InputSchema: json.RawMessage(`{"type":"object"}`), HTTP: HTTPConfig{URL: "https://example.com/status", Method: "get"}}
	tool, err := NormalizeTool(base)
	if err != nil || tool.HTTP.Method != "GET" || tool.HTTP.TimeoutMS != 10000 {
		t.Fatalf("normalize: %+v %v", tool, err)
	}
	for _, change := range []func(*ToolInput){
		func(in *ToolInput) { in.HTTP.Method = "POST" },
		func(in *ToolInput) { in.HTTP.URL = "https://user:password@example.com/status" },
		func(in *ToolInput) { in.HTTP.TimeoutMS = 120001 },
		func(in *ToolInput) { in.HTTP.CredentialRef = "secret-value" },
		func(in *ToolInput) { in.Name = "../../bad" },
	} {
		input := base
		change(&input)
		if _, err := NormalizeTool(input); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid tool accepted: %+v", input)
		}
	}
}

// An embedded nil Repository fails loudly if authorization accidentally lets a
// forbidden call reach persistence.
type unreachableRepository struct{ Repository }

func TestServiceAuthorizationBeforePersistence(t *testing.T) {
	svc := NewService(unreachableRepository{})
	viewer := Actor{ID: "person", WorkspaceID: "workspace", Role: RoleViewer}
	if _, err := svc.CreateTool(context.Background(), viewer, ToolInput{}); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
	if _, err := svc.Prepare(context.Background(), viewer, PrepareInput{}); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
	operator := viewer
	operator.Role = RoleOperator
	if _, err := svc.Approve(context.Background(), operator, "operation"); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
	if _, err := svc.ListTools(context.Background(), Actor{}); !errors.Is(err, ErrUnauthorized) {
		t.Fatal(err)
	}
	if _, err := svc.Recover(context.Background(), time.Second); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
}
