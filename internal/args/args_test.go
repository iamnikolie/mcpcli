package args

import (
	"encoding/json"
	"reflect"
	"testing"
)

var schema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"query": map[string]any{"type": "string", "description": "Search text."},
		"limit": map[string]any{"type": "integer", "default": float64(10)},
		"score": map[string]any{"type": "number"},
		"all":   map[string]any{"type": "boolean"},
		"tags":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		"ids":   map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
		"opts":  map[string]any{"type": "object", "properties": map[string]any{"deep": map[string]any{"type": "boolean"}}},
		"kind":  map[string]any{"type": []any{"string", "null"}, "enum": []any{"a", "b"}},
	},
	"required": []any{"query"},
}

func TestParseCoercion(t *testing.T) {
	got, err := Parse(nil, []string{"query=hello world", "limit=5", "score=1.5", "all=true", "tags=a, b,c", "ids=1,2", "opts.deep=true", "kind=a"}, schema)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"query": "hello world", "limit": int64(5), "score": 1.5, "all": true,
		"tags": []any{"a", "b", "c"}, "ids": []any{int64(1), int64(2)},
		"opts": map[string]any{"deep": true}, "kind": "a",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v\nwant %#v", got, want)
	}
}

func TestParseRawAndBase(t *testing.T) {
	got, err := Parse(json.RawMessage(`{"query":"base","limit":1}`), []string{"limit=7", `tags:=["x"]`, `opts:={"deep":false}`}, schema)
	if err != nil {
		t.Fatal(err)
	}
	if got["query"] != "base" || got["limit"] != int64(7) || !reflect.DeepEqual(got["tags"], []any{"x"}) {
		t.Fatalf("merge: %#v", got)
	}
	if _, err := Parse(nil, []string{"novalue"}, schema); err == nil {
		t.Fatal("missing = must error")
	}
	if _, err := Parse(nil, []string{`x:={bad`}, schema); err == nil {
		t.Fatal("bad json must error")
	}
	if _, err := Parse(json.RawMessage(`[1]`), nil, nil); err == nil {
		t.Fatal("non-object base must error")
	}
	// No schema: everything stays a string.
	got, _ = Parse(nil, []string{"n=5", "b=true"}, nil)
	if got["n"] != "5" || got["b"] != "true" {
		t.Fatalf("without schema values must stay strings: %#v", got)
	}
	// Schema says integer but the value is not one: keep the string.
	got, _ = Parse(nil, []string{"limit=many"}, schema)
	if got["limit"] != "many" {
		t.Fatalf("unparseable integer should stay string: %#v", got)
	}
}

func TestParams(t *testing.T) {
	ps := Params(schema)
	if len(ps) != 8 || ps[0].Name != "query" || !ps[0].Required || ps[1].Name != "all" {
		t.Fatalf("order (required first, then alpha): %+v", ps)
	}
	byName := map[string]Param{}
	for _, p := range ps {
		byName[p.Name] = p
	}
	if byName["tags"].Type != "string[]" || byName["ids"].Type != "integer[]" {
		t.Fatalf("array types: %+v", byName["tags"])
	}
	if byName["limit"].Default != "10" || byName["kind"].Type != "string" || len(byName["kind"].Enum) != 2 {
		t.Fatalf("default/enum/nullable: %+v %+v", byName["limit"], byName["kind"])
	}
	if Params(nil) != nil {
		t.Fatal("nil schema → nil params")
	}
}
