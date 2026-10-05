package core

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

const MaxArgumentsBytes = 256 << 10
const MaxResultBytes = 1 << 20
const MaxSchemaBytes = 64 << 10

var namePattern = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_.-]{0,63}$`)
var credentialPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,127}$`)

func NewID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		panic("cryptographic randomness unavailable")
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16])
}

// CanonicalArguments sorts object keys and preserves JSON numbers without
// float64 conversion. The resulting bytes are both persisted and executed.
func CanonicalArguments(raw json.RawMessage) (json.RawMessage, string, error) {
	if len(raw) == 0 || len(raw) > MaxArgumentsBytes {
		return nil, "", fmt.Errorf("%w: arguments must be a JSON object of at most %d bytes", ErrInvalid, MaxArgumentsBytes)
	}
	value, err := decodeJSON(raw)
	if err != nil {
		return nil, "", fmt.Errorf("%w: malformed arguments", ErrInvalid)
	}
	if _, ok := value.(map[string]any); !ok {
		return nil, "", fmt.Errorf("%w: arguments must be an object", ErrInvalid)
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return nil, "", fmt.Errorf("%w: malformed arguments", ErrInvalid)
	}
	digest := sha256.Sum256(canonical)
	return canonical, hex.EncodeToString(digest[:]), nil
}

func decodeJSON(raw json.RawMessage) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("unexpected trailing JSON")
	}
	if !boundedDepth(value, 0) {
		return nil, fmt.Errorf("JSON structure or numeric value exceeds allowed bounds")
	}
	return value, nil
}

// DecodeResult validates the storage and schema-evaluation boundaries before a
// downstream response is inspected or persisted. It preserves JSON numbers.
func DecodeResult(raw json.RawMessage) (any, error) {
	if len(raw) == 0 || len(raw) > MaxResultBytes {
		return nil, fmt.Errorf("%w: result exceeds allowed bounds", ErrInvalid)
	}
	value, err := decodeJSON(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: malformed or unsupported result JSON", ErrInvalid)
	}
	return value, nil
}
func boundedDepth(value any, depth int) bool {
	if depth > 64 {
		return false
	}
	switch current := value.(type) {
	case map[string]any:
		for key, child := range current {
			if strings.ContainsRune(key, 0) {
				return false
			}
			if !boundedDepth(child, depth+1) {
				return false
			}
		}
	case []any:
		for _, child := range current {
			if !boundedDepth(child, depth+1) {
				return false
			}
		}
	case string:
		if strings.ContainsRune(current, 0) {
			return false
		}
	case json.Number:
		// Bound exponent work before JSON Schema's arbitrary-precision number
		// conversion. Preserve large integer identifiers without float64 rounding.
		text := current.String()
		if len(text) > 1024 {
			return false
		}
		if index := strings.IndexAny(text, "eE"); index >= 0 {
			exponent, err := strconv.Atoi(text[index+1:])
			if err != nil || exponent < -1000 || exponent > 1000 {
				return false
			}
		}
	}
	return true
}

func CompileSchema(raw json.RawMessage, objectRoot bool) (*jsonschema.Schema, error) {
	if len(raw) == 0 || len(raw) > MaxSchemaBytes {
		return nil, fmt.Errorf("%w: schema must contain at most %d bytes", ErrInvalid, MaxSchemaBytes)
	}
	value, err := decodeJSON(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: malformed schema", ErrInvalid)
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%w: schema must be an object", ErrInvalid)
	}
	if objectRoot && object["type"] != "object" {
		return nil, fmt.Errorf("%w: input schema root type must be object", ErrInvalid)
	}
	compiler := jsonschema.NewCompiler()
	compiler.LoadURL = func(string) (io.ReadCloser, error) { return nil, fmt.Errorf("external schema references are disabled") }
	if err := compiler.AddResource("https://gateway.invalid/schema.json", bytes.NewReader(raw)); err != nil {
		return nil, fmt.Errorf("%w: invalid schema", ErrInvalid)
	}
	schema, err := compiler.Compile("https://gateway.invalid/schema.json")
	if err != nil {
		return nil, fmt.Errorf("%w: schema cannot be compiled; external references are not allowed", ErrInvalid)
	}
	return schema, nil
}

func ValidateArguments(schemaRaw, arguments json.RawMessage) error {
	schema, err := CompileSchema(schemaRaw, true)
	if err != nil {
		return err
	}
	value, err := decodeJSON(arguments)
	if err != nil {
		return fmt.Errorf("%w: malformed arguments", ErrInvalid)
	}
	if err := schema.Validate(value); err != nil {
		return fmt.Errorf("%w: arguments do not match the input schema", ErrInvalid)
	}
	return nil
}

func NormalizeTool(input ToolInput) (ToolInput, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.Description = strings.TrimSpace(input.Description)
	input.HTTP.Method = strings.ToUpper(strings.TrimSpace(input.HTTP.Method))
	if !namePattern.MatchString(input.Name) {
		return input, fmt.Errorf("%w: tool name must start with a letter and contain 1-64 letters, digits, dots, underscores or hyphens", ErrInvalid)
	}
	if input.Description == "" || len(input.Description) > 4000 {
		return input, fmt.Errorf("%w: description must contain 1-4000 bytes", ErrInvalid)
	}
	if input.Risk != RiskRead && input.Risk != RiskWrite {
		return input, fmt.Errorf("%w: risk must be read or write", ErrInvalid)
	}
	if input.MCP != nil {
		if input.HTTP != (HTTPConfig{}) {
			return input, fmt.Errorf("%w: an MCP tool cannot also configure HTTP", ErrInvalid)
		}
		if input.MCP.ServerID == "" || len(input.MCP.ServerID) > 128 || input.MCP.ToolName == "" || len(input.MCP.ToolName) > 128 || strings.ContainsAny(input.MCP.ToolName, "\x00\r\n") {
			return input, fmt.Errorf("%w: invalid MCP binding", ErrInvalid)
		}
		hash, err := hex.DecodeString(input.MCP.SchemaHash)
		if err != nil || len(hash) != 32 {
			return input, fmt.Errorf("%w: MCP schema hash must be SHA-256", ErrInvalid)
		}
		input.ResponsePolicy, err = NormalizeResponsePolicy(input.ResponsePolicy)
		if err != nil {
			return input, err
		}
	} else {
		if input.ResponsePolicy != nil {
			return input, fmt.Errorf("%w: response policy currently requires an MCP tool", ErrInvalid)
		}
		switch input.HTTP.Method {
		case "GET", "POST", "PUT", "PATCH", "DELETE":
		default:
			return input, fmt.Errorf("%w: unsupported HTTP method", ErrInvalid)
		}
		if input.Risk == RiskRead && input.HTTP.Method != "GET" {
			return input, fmt.Errorf("%w: read tools require GET", ErrInvalid)
		}
		endpoint, err := url.Parse(input.HTTP.URL)
		if err != nil || endpoint.Host == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.User != nil || endpoint.Fragment != "" {
			return input, fmt.Errorf("%w: tool URL must be an HTTP(S) URL without userinfo or fragment", ErrInvalid)
		}
		if input.HTTP.TimeoutMS == 0 {
			input.HTTP.TimeoutMS = 10000
		}
		if input.HTTP.TimeoutMS < 100 || input.HTTP.TimeoutMS > 120000 {
			return input, fmt.Errorf("%w: timeout must be between 100 and 120000 ms", ErrInvalid)
		}
		if input.HTTP.CredentialRef != "" && !credentialPattern.MatchString(input.HTTP.CredentialRef) {
			return input, fmt.Errorf("%w: credential reference must contain uppercase letters, digits or underscores and start with a letter", ErrInvalid)
		}
	}
	if _, err := CompileSchema(input.InputSchema, true); err != nil {
		return input, err
	}
	if len(input.OutputSchema) > 0 {
		if _, err := CompileSchema(input.OutputSchema, false); err != nil {
			return input, err
		}
	}
	return input, nil
}
