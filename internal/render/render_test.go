package render

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestTableKeepsColumnOrderAndEscapes(t *testing.T) {
	var b bytes.Buffer
	Table(&b, []map[string]any{{"url": "u", "title": "a|b\nc", "n": json.Number("5918904")}}, []string{"title", "url", "n"})
	want := "| title | url | n |\n| --- | --- | --- |\n| a\\|b c | u | 5918904 |\n"
	if b.String() != want {
		t.Fatalf("got\n%s", b.String())
	}
}

func TestSeparated(t *testing.T) {
	var b bytes.Buffer
	Separated(&b, []map[string]any{{"a": "x,y", "b": "q\"t"}}, []string{"a", "b"}, ",")
	if b.String() != "a,b\n\"x,y\",\"q\"\"t\"\n" {
		t.Fatalf("csv: %q", b.String())
	}
	b.Reset()
	Separated(&b, []map[string]any{{"a": "x\ty\nz"}}, []string{"a"}, "\t")
	if b.String() != "a\nx y z\n" {
		t.Fatalf("tsv: %q", b.String())
	}
}

func TestEmptyTable(t *testing.T) {
	var b bytes.Buffer
	Table(&b, nil, []string{"a"})
	if b.String() != "_No results._\n" {
		t.Fatalf("got %q", b.String())
	}
}
