package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/iamnikolie/mcpcli/internal/render"
)

func format() string {
	if jsonOutput {
		return "json"
	}
	if outputFormat == "" {
		return "md"
	}
	return outputFormat
}

// writeJSON encodes v as compact UTF-8 JSON without HTML escaping.
func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

func project(row map[string]any, fields []string) map[string]any {
	out := make(map[string]any, len(fields))
	for _, f := range fields {
		if v, ok := getPath(row, f); ok {
			out[f] = v
		}
	}
	return out
}

func getPath(v any, path string) (any, bool) {
	if m, ok := v.(map[string]any); ok {
		if x, ok := m[path]; ok {
			return x, true
		}
	}
	cur := v
	for _, part := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = m[part]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

func projectAll(rows []map[string]any, cols []string) []map[string]any {
	out := make([]map[string]any, len(rows))
	for i, r := range rows {
		out[i] = project(r, cols)
	}
	return out
}

// emitRows prints a list in the active format. Columns default to the
// sorted union of keys; --fields narrows them.
func emitRows(rows []map[string]any, defaults []string) error {
	cols := defaults
	if len(fieldsFlag) > 0 {
		cols = fieldsFlag
	}
	switch format() {
	case "json":
		if rows == nil {
			rows = []map[string]any{}
		}
		if len(fieldsFlag) > 0 {
			return writeJSON(stdout, projectAll(rows, fieldsFlag))
		}
		return writeJSON(stdout, rows)
	case "csv":
		if len(cols) == 0 {
			cols = render.Columns(rows, nil)
		}
		render.Separated(stdout, projectAll(rows, cols), cols, ",")
	case "tsv":
		if len(cols) == 0 {
			cols = render.Columns(rows, nil)
		}
		render.Separated(stdout, projectAll(rows, cols), cols, "\t")
	default:
		if len(cols) == 0 {
			render.Table(stdout, rows, nil)
		} else {
			render.Table(stdout, projectAll(rows, cols), cols)
		}
	}
	return nil
}

func prettyJSON(v any) string {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return fmt.Sprint(v)
	}
	return strings.TrimRight(b.String(), "\n")
}

func clip(s string) string {
	if maxChars <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= maxChars {
		return s
	}
	return string(r[:maxChars]) + fmt.Sprintf("\n… [%d more chars; raise --max-chars]", len(r)-maxChars)
}

func firstSentence(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if i := strings.IndexAny(s, ".!?\n"); i > 0 && i < max {
		s = s[:i+1]
	}
	r := []rune(s)
	if len(r) > max {
		return string(r[:max-1]) + "…"
	}
	return s
}

// readJSONArg parses a JSON value given inline, as @file, or as - (stdin).
func readJSONArg(name, s string) (json.RawMessage, error) {
	var b []byte
	var err error
	switch {
	case s == "":
		return nil, nil
	case s == "-":
		b, err = io.ReadAll(stdin)
	case strings.HasPrefix(s, "@"):
		b, err = os.ReadFile(s[1:])
	default:
		b = []byte(s)
	}
	if err != nil {
		return nil, fmt.Errorf("--%s: %w", name, err)
	}
	b = []byte(strings.TrimSpace(string(b)))
	if !json.Valid(b) {
		return nil, fmt.Errorf("--%s: not valid JSON (pass inline JSON, @file.json or - for stdin)", name)
	}
	return json.RawMessage(b), nil
}
