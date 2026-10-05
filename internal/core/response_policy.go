package core

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// ResponsePolicy is fixed with the tool and copied into each operation snapshot.
// Include uses JSON Pointer escaping with an array traversal extension: a full
// '*' segment selects every array element. Numeric segments remain object keys;
// array indices and object wildcards are not supported.
type ResponsePolicy struct {
	Include  []string `json:"include,omitempty"`
	MaxBytes int      `json:"max_bytes"`
}

func NormalizeResponsePolicy(in *ResponsePolicy) (*ResponsePolicy, error) {
	p, _, err := normalizeResponsePolicy(in)
	return p, err
}

func normalizeResponsePolicy(in *ResponsePolicy) (*ResponsePolicy, *responseSelection, error) {
	p := ResponsePolicy{MaxBytes: 64 << 10}
	if in != nil {
		p = *in
		p.Include = append([]string(nil), in.Include...)
		if p.MaxBytes == 0 {
			p.MaxBytes = 64 << 10
		}
	}
	if p.MaxBytes < 1024 || p.MaxBytes > 128<<10 || len(p.Include) > 32 {
		return nil, nil, fmt.Errorf("%w: response policy allows 1024–131072 bytes and at most 32 paths", ErrInvalid)
	}
	sort.Strings(p.Include)
	selection := &responseSelection{}
	for _, raw := range p.Include {
		parts, err := responsePath(raw)
		if err != nil {
			return nil, nil, err
		}
		if err := selection.add(parts); err != nil {
			return nil, nil, err
		}
	}
	return &p, selection, nil
}

func responsePath(raw string) ([]string, error) {
	invalid := func() ([]string, error) {
		return nil, fmt.Errorf("%w: include requires nonempty JSON Pointer selectors of at most 256 UTF-8 bytes; '*' must be a complete nonfinal array segment", ErrInvalid)
	}
	if !utf8.ValidString(raw) || len(raw) > 256 || !strings.HasPrefix(raw, "/") || strings.ContainsRune(raw, 0) {
		return invalid()
	}
	parts := strings.Split(raw[1:], "/")
	for i, p := range parts {
		if p == "" || strings.Contains(p, "*") && (p != "*" || i == len(parts)-1) {
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

// A node selects an entire leaf, named object fields, or all array elements.
// Building this tree rejects contradictory selectors before any result arrives.
type responseSelection struct {
	leaf   bool
	fields map[string]*responseSelection
	all    *responseSelection
}

func (s *responseSelection) add(parts []string) error {
	node := s
	for _, part := range parts {
		if node.leaf {
			return fmt.Errorf("%w: response paths must not repeat or overlap", ErrInvalid)
		}
		if part == "*" {
			if len(node.fields) > 0 {
				return fmt.Errorf("%w: response selectors cannot require both array and object traversal at the same node", ErrInvalid)
			}
			if node.all == nil {
				node.all = &responseSelection{}
			}
			node = node.all
		} else {
			if node.all != nil {
				return fmt.Errorf("%w: response selectors cannot require both array and object traversal at the same node", ErrInvalid)
			}
			if node.fields == nil {
				node.fields = make(map[string]*responseSelection)
			}
			if node.fields[part] == nil {
				node.fields[part] = &responseSelection{}
			}
			node = node.fields[part]
		}
	}
	if node.leaf || node.all != nil || len(node.fields) > 0 {
		return fmt.Errorf("%w: response paths must not repeat or overlap", ErrInvalid)
	}
	node.leaf = true
	return nil
}

func (s *responseSelection) project(source any) (any, error) {
	if s.leaf {
		return source, nil
	}
	if s.all != nil {
		array, ok := source.([]any)
		if !ok {
			return nil, fmt.Errorf("response projection requires an array at each '*' selector")
		}
		// Allocate the exact length: no filtering, reordering or partially
		// returned arrays can conceal missing fields in a later element.
		projected := make([]any, len(array))
		for i, value := range array {
			item, err := s.all.project(value)
			if err != nil {
				return nil, err
			}
			projected[i] = item
		}
		return projected, nil
	}
	object, ok := source.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("response projection requires an object at each named selector; arrays must use '*'")
	}
	projected := make(map[string]any, len(s.fields))
	for name, child := range s.fields {
		value, exists := object[name]
		if !exists {
			return nil, fmt.Errorf("response projection references a missing field")
		}
		selected, err := child.project(value)
		if err != nil {
			return nil, err
		}
		projected[name] = selected
	}
	return projected, nil
}

// ApplyMCPResponsePolicy accepts only a validated, successful MCP envelope.
// Structured results get a single regenerated text block, so a textual copy of
// the original cannot bypass projection or basic secret-field filtering.
func ApplyMCPResponsePolicy(raw json.RawMessage, policy *ResponsePolicy) (json.RawMessage, error) {
	p, selection, err := normalizeResponsePolicy(policy)
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
			selected, err := selection.project(source)
			if err != nil {
				return nil, err
			}
			var ok bool
			projected, ok = selected.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("response projection must preserve the structuredContent object")
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
