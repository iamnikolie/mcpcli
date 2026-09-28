package result

import (
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestParts(t *testing.T) {
	parts := Parts([]mcp.Content{
		&mcp.TextContent{Text: "hello"},
		&mcp.ImageContent{MIMEType: "image/png", Data: []byte{1, 2, 3}},
		&mcp.EmbeddedResource{Resource: &mcp.ResourceContents{URI: "file:///a", MIMEType: "text/plain", Text: "body"}},
		&mcp.EmbeddedResource{Resource: &mcp.ResourceContents{URI: "file:///b", MIMEType: "application/octet-stream", Blob: []byte{9}}},
		&mcp.ResourceLink{URI: "https://x", Name: "x"},
	})
	if len(parts) != 5 || parts[0].Kind != "text" || parts[1].Text != "[image image/png, 3 bytes]" || parts[2].Text != "body" ||
		parts[3].Text != "[resource file:///b application/octet-stream, 1 bytes]" || parts[4].Kind != "link" {
		t.Fatalf("%+v", parts)
	}
	if Text(parts[:2]) != "hello\n\n[image image/png, 3 bytes]" {
		t.Fatalf("text join: %q", Text(parts[:2]))
	}
}

func TestDecodeJSONAndRows(t *testing.T) {
	if _, ok := DecodeJSON("plain prose"); ok {
		t.Fatal("prose is not json")
	}
	if _, ok := DecodeJSON(`{"a":1} trailing`); ok {
		t.Fatal("trailing garbage is not json")
	}
	v, ok := DecodeJSON(` [{"id":1},{"id":2}] `)
	if !ok {
		t.Fatal("array should decode")
	}
	rows, key, ok := Rows(v)
	if !ok || key != "" || len(rows) != 2 || rows[1]["id"].(interface{ String() string }).String() != "2" {
		t.Fatalf("rows: %v %q %v", rows, key, ok)
	}
	v, _ = DecodeJSON(`{"total":2,"items":[{"id":1},{"id":2}]}`)
	rows, key, ok = Rows(v)
	if !ok || key != "items" || len(rows) != 2 {
		t.Fatalf("envelope: %v %q %v", rows, key, ok)
	}
	v, _ = DecodeJSON(`{"a":[{"id":1}],"b":[{"id":2}]}`)
	if _, _, ok := Rows(v); ok {
		t.Fatal("two array fields is ambiguous")
	}
	v, _ = DecodeJSON(`[1,2,3]`)
	if _, _, ok := Rows(v); ok {
		t.Fatal("scalars are not rows")
	}
	n := Normalize(struct {
		A int `json:"a"`
	}{A: 3})
	if m, ok := n.(map[string]any); !ok || m["a"].(interface{ String() string }).String() != "3" {
		t.Fatalf("normalize: %#v", n)
	}
}
