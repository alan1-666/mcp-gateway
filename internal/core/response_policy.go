package core

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// ResponsePolicy is fixed with the tool and copied into each operation snapshot.
// Include contains object-only RFC 6901 paths into MCP structuredContent.
type ResponsePolicy struct {
	Include  []string `json:"include,omitempty"`
	MaxBytes int      `json:"max_bytes"`
}

func NormalizeResponsePolicy(in *ResponsePolicy) (*ResponsePolicy, error) {
	p := ResponsePolicy{MaxBytes: 64 << 10}
	if in != nil {
		p = *in
		p.Include = append([]string(nil), in.Include...)
		if p.MaxBytes == 0 {
			p.MaxBytes = 64 << 10
		}
	}
	if p.MaxBytes < 1024 || p.MaxBytes > 128<<10 || len(p.Include) > 32 {
		return nil, fmt.Errorf("%w: response policy allows 1024–131072 bytes and at most 32 paths", ErrInvalid)
	}
	sort.Strings(p.Include)
	paths := make([][]string, 0, len(p.Include))
	for _, raw := range p.Include {
		parts, err := responsePath(raw)
		if err != nil {
			return nil, err
		}
		for _, old := range paths {
			n := len(parts)
			if len(old) < n {
				n = len(old)
			}
			same := true
			for i := 0; i < n; i++ {
				if parts[i] != old[i] {
					same = false
					break
				}
			}
			if same {
				return nil, fmt.Errorf("%w: response paths must not repeat or overlap", ErrInvalid)
			}
		}
		paths = append(paths, parts)
	}
	return &p, nil
}
func responsePath(raw string) ([]string, error) {
	invalid := func() ([]string, error) {
		return nil, fmt.Errorf("%w: include must contain valid nonempty object JSON pointers of at most 256 UTF-8 bytes", ErrInvalid)
	}
	if !utf8.ValidString(raw) || len(raw) > 256 || !strings.HasPrefix(raw, "/") || strings.ContainsRune(raw, 0) {
		return invalid()
	}
	parts := strings.Split(raw[1:], "/")
	for i, p := range parts {
		if p == "" || strings.Contains(p, "*") {
			return invalid()
		}
		var out strings.Builder
		for j := 0; j < len(p); j++ {
			if p[j] != '~' {
				out.WriteByte(p[j])
				continue
			}
			j++
			if j >= len(p) {
				return invalid()
			}
			switch p[j] {
			case '0':
				out.WriteByte('~')
			case '1':
				out.WriteByte('/')
			default:
				return invalid()
			}
		}
		parts[i] = out.String()
	}
	return parts, nil
}

// ApplyMCPResponsePolicy accepts only a validated, successful MCP envelope.
// Structured results get a single regenerated text block, so a textual copy of
// the original cannot bypass projection or basic secret-field filtering.
func ApplyMCPResponsePolicy(raw json.RawMessage, policy *ResponsePolicy) (json.RawMessage, error) {
	p, err := NormalizeResponsePolicy(policy)
	if err != nil {
		return nil, err
	}
	decoded, err := DecodeResult(raw)
	if err != nil {
		return nil, err
	}
	result, ok := decoded.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("invalid MCP result envelope")
	}
	if flag, ok := result["isError"].(bool); !ok || flag {
		return nil, fmt.Errorf("only successful MCP results may be projected")
	}
	// Keep only protocol fields deliberately supported by the adapter.
	filtered := map[string]any{"isError": false, "content": result["content"]}
	if structured, exists := result["structuredContent"]; exists {
		source, ok := structured.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("structuredContent must be an object")
		}
		redactResponseFields(source)
		projected := source
		if len(p.Include) > 0 {
			projected = map[string]any{}
			for _, path := range p.Include {
				parts, _ := responsePath(path)
				var current any = source
				for _, part := range parts {
					object, ok := current.(map[string]any)
					if !ok {
						return nil, fmt.Errorf("response projection cannot traverse arrays or scalar values")
					}
					value, exists := object[part]
					if !exists {
						return nil, fmt.Errorf("response projection references a missing field")
					}
					current = value
				}
				target := projected
				for _, part := range parts[:len(parts)-1] {
					if target[part] == nil {
						target[part] = map[string]any{}
					}
					target = target[part].(map[string]any)
				}
				target[parts[len(parts)-1]] = current
			}
			// Common root pagination cursors are control data, not expendable text.
			for _, key := range []string{"nextCursor", "next_cursor"} {
				if value, ok := source[key]; ok {
					if value != nil {
						if _, ok := value.(string); !ok {
							return nil, fmt.Errorf("pagination cursor must be a string or null before projection")
						}
					}
					projected[key] = value
				}
			}
		}
		filtered["structuredContent"] = projected
		text, err := json.Marshal(projected)
		if err != nil {
			return nil, fmt.Errorf("cannot encode projected result")
		}
		filtered["content"] = []any{map[string]any{"type": "text", "text": string(text)}}
	} else if len(p.Include) > 0 {
		return nil, fmt.Errorf("response projection requires structuredContent")
	}
	if len(p.Include) > 0 {
		filtered["gateway_projection"] = map[string]any{"include": p.Include, "preserved_root_cursors": true}
	}
	encoded, err := json.Marshal(filtered)
	if err != nil {
		return nil, fmt.Errorf("cannot encode bounded MCP result")
	}
	if len(encoded) > p.MaxBytes {
		return nil, fmt.Errorf("MCP result exceeds the configured response limit; narrow the projection")
	}
	return encoded, nil
}
func redactResponseFields(value any) {
	switch x := value.(type) {
	case map[string]any:
		for k, v := range x {
			normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(k, "_", ""), "-", ""))
			switch normalized {
			case "password", "passwd", "token", "accesstoken", "refreshtoken", "authorization", "secret", "clientsecret", "apikey", "cookie", "setcookie":
				x[k] = "[REDACTED]"
			default:
				redactResponseFields(v)
			}
		}
	case []any:
		for _, v := range x {
			redactResponseFields(v)
		}
	}
}
