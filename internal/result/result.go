// Package result flattens MCP tool results into things a terminal can show.
package result

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Part is one content item, reduced to text or a placeholder.
type Part struct {
	Kind string // text, image, audio, resource, link, other
	Text string
	MIME string
	URI  string
	Size int
}

// Parts flattens result content. Binary items become bracketed placeholders
// so an agent sees that something was there without the bytes.
func Parts(content []mcp.Content) []Part {
	var out []Part
	for _, c := range content {
		switch x := c.(type) {
		case *mcp.TextContent:
			out = append(out, Part{Kind: "text", Text: x.Text})
		case *mcp.ImageContent:
			out = append(out, Part{Kind: "image", MIME: x.MIMEType, Size: len(x.Data), Text: fmt.Sprintf("[image %s, %d bytes]", x.MIMEType, len(x.Data))})
		case *mcp.AudioContent:
			out = append(out, Part{Kind: "audio", MIME: x.MIMEType, Size: len(x.Data), Text: fmt.Sprintf("[audio %s, %d bytes]", x.MIMEType, len(x.Data))})
		case *mcp.EmbeddedResource:
			p := Part{Kind: "resource"}
			if x.Resource != nil {
				p.URI, p.MIME = x.Resource.URI, x.Resource.MIMEType
				if x.Resource.Text != "" {
					p.Text = x.Resource.Text
				} else {
					p.Size = len(x.Resource.Blob)
					p.Text = fmt.Sprintf("[resource %s %s, %d bytes]", x.Resource.URI, x.Resource.MIMEType, len(x.Resource.Blob))
				}
			}
			out = append(out, p)
		case *mcp.ResourceLink:
			out = append(out, Part{Kind: "link", URI: x.URI, MIME: x.MIMEType, Text: fmt.Sprintf("[link %s %s]", x.URI, x.Name)})
		default:
			b, _ := json.Marshal(c)
			out = append(out, Part{Kind: "other", Text: string(b)})
		}
	}
	return out
}

// Text joins all parts' text with blank lines.
func Text(parts []Part) string {
	var b strings.Builder
	for i, p := range parts {
		if i > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(strings.TrimRight(p.Text, "\n"))
	}
	return b.String()
}

// DecodeJSON parses s when it is a JSON object or array (numbers as
// json.Number). Plain prose and scalars are left alone.
func DecodeJSON(s string) (any, bool) {
	t := strings.TrimSpace(s)
	if !(strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[")) {
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader([]byte(t)))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, false
	}
	if dec.More() {
		return nil, false
	}
	return v, true
}

// Rows returns v as a list of objects when it is one, or when it is an
// object with exactly one array-of-objects field (a common {"items":[...]}
// envelope); the second value names that envelope key, "" otherwise.
func Rows(v any) ([]map[string]any, string, bool) {
	if arr, ok := v.([]any); ok {
		rows, ok := objects(arr)
		return rows, "", ok
	}
	if m, ok := v.(map[string]any); ok {
		var key string
		var rows []map[string]any
		for k, val := range m {
			if arr, ok := val.([]any); ok && len(arr) > 0 {
				if r, ok := objects(arr); ok {
					if key != "" {
						return nil, "", false
					}
					key, rows = k, r
				}
			}
		}
		if key != "" {
			return rows, key, true
		}
	}
	return nil, "", false
}

func objects(arr []any) ([]map[string]any, bool) {
	if len(arr) == 0 {
		return nil, false
	}
	out := make([]map[string]any, 0, len(arr))
	for _, x := range arr {
		m, ok := x.(map[string]any)
		if !ok {
			return nil, false
		}
		out = append(out, m)
	}
	return out, true
}

// Normalize re-encodes any value (including SDK structs) as generic JSON
// with json.Number so renderers see one shape.
func Normalize(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		return v
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var out any
	if err := dec.Decode(&out); err != nil {
		return v
	}
	return out
}
