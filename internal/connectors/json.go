package connectors

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// DecodeJSON bounds nesting and rejects duplicate fields before decoding the
// authenticated work protocol. json.Number prevents losing 64-bit result IDs.
func DecodeJSON(raw []byte) (any, error) {
	if len(raw) == 0 || len(raw) > MaxResultBytes {
		return nil, fmt.Errorf("JSON exceeds limits")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	v, err := readJSON(d, 0)
	if err != nil {
		return nil, err
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, fmt.Errorf("trailing JSON")
	}
	return v, nil
}
func readJSON(d *json.Decoder, depth int) (any, error) {
	if depth > 64 {
		return nil, fmt.Errorf("JSON nesting exceeds limits")
	}
	t, err := d.Token()
	if err != nil {
		return nil, err
	}
	delimiter, ok := t.(json.Delim)
	if !ok {
		return t, nil
	}
	switch delimiter {
	case '{':
		out := map[string]any{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return nil, err
			}
			name, ok := key.(string)
			if !ok {
				return nil, fmt.Errorf("invalid field")
			}
			if _, ok = out[name]; ok {
				return nil, fmt.Errorf("duplicate field")
			}
			value, err := readJSON(d, depth+1)
			if err != nil {
				return nil, err
			}
			out[name] = value
		}
		if close, err := d.Token(); err != nil || close != json.Delim('}') {
			return nil, fmt.Errorf("unclosed object")
		}
		return out, nil
	case '[':
		out := []any{}
		for d.More() {
			value, err := readJSON(d, depth+1)
			if err != nil {
				return nil, err
			}
			out = append(out, value)
		}
		if close, err := d.Token(); err != nil || close != json.Delim(']') {
			return nil, fmt.Errorf("unclosed array")
		}
		return out, nil
	default:
		return nil, fmt.Errorf("unexpected delimiter")
	}
}
