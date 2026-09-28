// Package args turns shell-friendly key=value pairs into a tool-call
// argument object, coercing types from the tool's JSON schema.
package args

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Parse merges base (an optional JSON object) with pairs of the form
// key=value (string, coerced by schema), key:=json (raw JSON) and
// nested.key=value (dotted paths). schema is the tool's inputSchema.
func Parse(base json.RawMessage, pairs []string, schema map[string]any) (map[string]any, error) {
	out := map[string]any{}
	if len(base) > 0 {
		if err := json.Unmarshal(base, &out); err != nil {
			return nil, fmt.Errorf("--args must be a JSON object: %w", err)
		}
		if out == nil {
			out = map[string]any{}
		}
	}
	for _, p := range pairs {
		key, val, raw, err := split(p)
		if err != nil {
			return nil, err
		}
		var v any
		if raw {
			if err := json.Unmarshal([]byte(val), &v); err != nil {
				return nil, fmt.Errorf("%s: not valid JSON: %w", key, err)
			}
		} else {
			v = coerce(val, propSchema(schema, key))
		}
		set(out, key, v)
	}
	return out, nil
}

func split(p string) (key, val string, raw bool, err error) {
	i := strings.Index(p, "=")
	if i <= 0 {
		return "", "", false, fmt.Errorf("argument %q must be key=value or key:=json", p)
	}
	key = p[:i]
	val = p[i+1:]
	if strings.HasSuffix(key, ":") {
		return strings.TrimSuffix(key, ":"), val, true, nil
	}
	return key, val, false, nil
}

func set(m map[string]any, path string, v any) {
	parts := strings.Split(path, ".")
	for _, part := range parts[:len(parts)-1] {
		next, ok := m[part].(map[string]any)
		if !ok {
			next = map[string]any{}
			m[part] = next
		}
		m = next
	}
	m[parts[len(parts)-1]] = v
}

// propSchema walks properties along a dotted path.
func propSchema(schema map[string]any, path string) map[string]any {
	cur := schema
	for _, part := range strings.Split(path, ".") {
		if cur == nil {
			return nil
		}
		props, _ := cur["properties"].(map[string]any)
		next, _ := props[part].(map[string]any)
		cur = next
	}
	return cur
}

func typeOf(s map[string]any) string {
	if s == nil {
		return ""
	}
	switch t := s["type"].(type) {
	case string:
		return t
	case []any:
		for _, x := range t {
			if str, ok := x.(string); ok && str != "null" {
				return str
			}
		}
	}
	if _, ok := s["enum"]; ok {
		return "string"
	}
	return ""
}

// coerce converts a shell string to the schema's type. Without a schema,
// numbers and booleans stay strings: explicit key:=json exists for that.
func coerce(val string, s map[string]any) any {
	switch typeOf(s) {
	case "integer":
		if n, err := strconv.ParseInt(val, 10, 64); err == nil {
			return n
		}
	case "number":
		if f, err := strconv.ParseFloat(val, 64); err == nil {
			return f
		}
	case "boolean":
		if b, err := strconv.ParseBool(val); err == nil {
			return b
		}
	case "array":
		trimmed := strings.TrimSpace(val)
		if strings.HasPrefix(trimmed, "[") {
			var v any
			if err := json.Unmarshal([]byte(trimmed), &v); err == nil {
				return v
			}
		}
		items, _ := s["items"].(map[string]any)
		var arr []any
		for _, part := range strings.Split(val, ",") {
			if part = strings.TrimSpace(part); part != "" {
				arr = append(arr, coerce(part, items))
			}
		}
		if arr == nil {
			arr = []any{}
		}
		return arr
	case "object":
		var v any
		if err := json.Unmarshal([]byte(val), &v); err == nil {
			return v
		}
	}
	return val
}

// Param describes one input parameter for help and skill output.
type Param struct {
	Name        string
	Type        string
	Required    bool
	Description string
	Enum        []string
	Default     string
}

// Params flattens a tool's inputSchema properties, required first.
func Params(schema map[string]any) []Param {
	props, _ := schema["properties"].(map[string]any)
	req := map[string]bool{}
	if r, ok := schema["required"].([]any); ok {
		for _, x := range r {
			if s, ok := x.(string); ok {
				req[s] = true
			}
		}
	}
	var out []Param
	for name, raw := range props {
		s, _ := raw.(map[string]any)
		p := Param{Name: name, Type: typeOf(s), Required: req[name]}
		p.Description, _ = s["description"].(string)
		if p.Type == "array" {
			if items, ok := s["items"].(map[string]any); ok {
				if it := typeOf(items); it != "" {
					p.Type = it + "[]"
				}
			}
		}
		if e, ok := s["enum"].([]any); ok {
			for _, x := range e {
				p.Enum = append(p.Enum, fmt.Sprint(x))
			}
		}
		if d, ok := s["default"]; ok && d != nil {
			b, _ := json.Marshal(d)
			p.Default = string(b)
		}
		out = append(out, p)
	}
	sortParams(out)
	return out
}

func sortParams(ps []Param) {
	for i := 1; i < len(ps); i++ {
		for j := i; j > 0 && less(ps[j], ps[j-1]); j-- {
			ps[j], ps[j-1] = ps[j-1], ps[j]
		}
	}
}

func less(a, b Param) bool {
	if a.Required != b.Required {
		return a.Required
	}
	return a.Name < b.Name
}
