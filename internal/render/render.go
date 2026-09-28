// Package render turns decoded JSON rows into token-lean terminal output.
package render

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

// Cell formats a decoded JSON value for display. Numbers are expected to be
// json.Number (decoded with UseNumber); nested values render as compact JSON.
func Cell(v any) string {
	switch n := v.(type) {
	case nil:
		return ""
	case json.Number:
		return n.String()
	case string:
		return n
	case bool:
		if n {
			return "true"
		}
		return "false"
	case map[string]any, []any:
		b, _ := json.Marshal(n)
		return string(b)
	default:
		return fmt.Sprintf("%v", v)
	}
}

// oneLine collapses newlines so a value stays on one row.
func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.ReplaceAll(s, "\r", " ")
}

// Columns returns cols when set, else the sorted union of keys across rows.
func Columns(rows []map[string]any, cols []string) []string {
	if len(cols) > 0 {
		return cols
	}
	set := map[string]bool{}
	for _, r := range rows {
		for k := range r {
			set[k] = true
		}
	}
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Table renders rows as a Markdown table in column order.
func Table(w io.Writer, rows []map[string]any, cols []string) {
	if len(rows) == 0 {
		fmt.Fprintln(w, "_No results._")
		return
	}
	cols = Columns(rows, cols)
	sep := make([]string, len(cols))
	for i := range sep {
		sep[i] = "---"
	}
	fmt.Fprintf(w, "| %s |\n", strings.Join(cols, " | "))
	fmt.Fprintf(w, "| %s |\n", strings.Join(sep, " | "))
	for _, r := range rows {
		cells := make([]string, len(cols))
		for i, c := range cols {
			cells[i] = strings.ReplaceAll(oneLine(Cell(r[c])), "|", "\\|")
		}
		fmt.Fprintf(w, "| %s |\n", strings.Join(cells, " | "))
	}
}

// KV renders one object as "**key:** value" lines in column order.
func KV(w io.Writer, row map[string]any, cols []string) {
	for _, c := range Columns([]map[string]any{row}, cols) {
		v, ok := row[c]
		if !ok {
			continue
		}
		fmt.Fprintf(w, "**%s:** %s\n", c, Cell(v))
	}
}

// Separated renders rows as CSV (sep ",") or TSV (sep "\t") with a header.
func Separated(w io.Writer, rows []map[string]any, cols []string, sep string) {
	if len(rows) == 0 {
		return
	}
	cols = Columns(rows, cols)
	fmt.Fprintln(w, strings.Join(cols, sep))
	for _, r := range rows {
		cells := make([]string, len(cols))
		for i, c := range cols {
			s := Cell(r[c])
			if sep == "," {
				if strings.ContainsAny(s, ",\"\n\r") {
					s = `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
				}
			} else {
				s = strings.ReplaceAll(oneLine(s), "\t", " ")
			}
			cells[i] = s
		}
		fmt.Fprintln(w, strings.Join(cells, sep))
	}
}
