package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/iamnikolie/mcpcli/internal/args"
	"github.com/iamnikolie/mcpcli/internal/config"
	"github.com/iamnikolie/mcpcli/internal/result"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
)

var (
	toolsFull    bool
	toolsRefresh bool
	callArgs     string
	callRaw      bool
)

// toolInfo is the cached, schema-flattened view of a tool.
type toolInfo struct {
	Name        string         `json:"name"`
	Title       string         `json:"title,omitempty"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"inputSchema,omitempty"`
	ReadOnly    *bool          `json:"readOnly,omitempty"`
}

func cachedTools(c *config.ToolCache) []toolInfo {
	out := make([]toolInfo, 0, len(c.Tools))
	for _, raw := range c.Tools {
		var t toolInfo
		if err := json.Unmarshal(raw, &t); err == nil {
			out = append(out, t)
		}
	}
	return out
}

func fromTool(t *mcp.Tool) toolInfo {
	info := toolInfo{Name: t.Name, Title: t.Title, Description: t.Description}
	if s, ok := result.Normalize(t.InputSchema).(map[string]any); ok {
		info.InputSchema = s
	}
	if t.Annotations != nil && t.Annotations.ReadOnlyHint {
		ro := true
		info.ReadOnly = &ro
	}
	return info
}

// refreshTools lists tools from the server and writes the cache.
func refreshTools(ctx context.Context, name string) ([]toolInfo, error) {
	_, session, err := connect(ctx, name)
	if err != nil {
		return nil, err
	}
	defer session.Close()
	var infos []toolInfo
	var raws []json.RawMessage
	for t, err := range session.Tools(ctx, nil) {
		if err != nil {
			return nil, fmt.Errorf("list tools: %w", err)
		}
		info := fromTool(t)
		infos = append(infos, info)
		b, _ := json.Marshal(info)
		raws = append(raws, b)
	}
	if err := config.SaveTools(name, &config.ToolCache{FetchedAt: nowFn(), Tools: raws}); err != nil {
		return nil, err
	}
	return infos, nil
}

// loadTools returns cached tools, fetching when the cache is missing or
// --refresh was given.
func loadTools(ctx context.Context, name string, force bool) ([]toolInfo, error) {
	if !force {
		if c, _ := config.LoadTools(name); c != nil {
			return cachedTools(c), nil
		}
	}
	return refreshTools(ctx, name)
}

func findTool(tools []toolInfo, name string) *toolInfo {
	for i := range tools {
		if tools[i].Name == name {
			return &tools[i]
		}
	}
	return nil
}

var toolsCmd = &cobra.Command{
	Use:   "tools <profile>",
	Short: "List a server's tools (cached after the first fetch; --refresh to re-list)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, cargs []string) error {
		ctx, cancel := ctxOf(cmd)
		defer cancel()
		tools, err := loadTools(ctx, cargs[0], toolsRefresh)
		if err != nil {
			return err
		}
		switch {
		case format() == "json":
			return writeJSON(stdout, tools)
		case toolsFull:
			for _, t := range tools {
				printTool(cargs[0], t)
				fmt.Fprintln(stdout)
			}
			return nil
		default:
			rows := make([]map[string]any, 0, len(tools))
			for _, t := range tools {
				ps := args.Params(t.InputSchema)
				var req []string
				for _, p := range ps {
					if p.Required {
						req = append(req, p.Name)
					}
				}
				rows = append(rows, map[string]any{"tool": t.Name, "required": strings.Join(req, ","), "description": firstSentence(t.Description, 110)})
			}
			return emitRows(rows, []string{"tool", "required", "description"})
		}
	},
}

func printTool(profile string, t toolInfo) {
	fmt.Fprintf(stdout, "## %s\n", t.Name)
	if t.Description != "" {
		fmt.Fprintln(stdout, strings.TrimSpace(t.Description))
	}
	ps := args.Params(t.InputSchema)
	if len(ps) > 0 {
		fmt.Fprintln(stdout)
		for _, p := range ps {
			line := "- `" + p.Name + "`"
			if p.Type != "" {
				line += " " + p.Type
			}
			if p.Required {
				line += " (required)"
			}
			if p.Description != "" {
				line += ": " + strings.Join(strings.Fields(p.Description), " ")
			}
			if len(p.Enum) > 0 {
				line += " [" + strings.Join(p.Enum, "|") + "]"
			}
			if p.Default != "" {
				line += " default " + p.Default
			}
			fmt.Fprintln(stdout, line)
		}
	}
	fmt.Fprintf(stdout, "\nCall: mcpcli %s %s%s\n", profile, t.Name, exampleArgs(ps))
}

func exampleArgs(ps []args.Param) string {
	var b strings.Builder
	for _, p := range ps {
		if !p.Required {
			continue
		}
		b.WriteString(" ")
		b.WriteString(p.Name)
		switch {
		case strings.HasSuffix(p.Type, "[]"), p.Type == "object":
			b.WriteString(":='...'")
		default:
			b.WriteString("=...")
		}
	}
	return b.String()
}

var toolCmd = &cobra.Command{
	Use:   "tool <profile> <name>",
	Short: "Describe one tool: parameters and how to call it",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, cargs []string) error {
		ctx, cancel := ctxOf(cmd)
		defer cancel()
		tools, err := loadTools(ctx, cargs[0], false)
		if err != nil {
			return err
		}
		t := findTool(tools, cargs[1])
		if t == nil {
			if tools, err = refreshTools(ctx, cargs[0]); err != nil {
				return err
			}
			if t = findTool(tools, cargs[1]); t == nil {
				return fmt.Errorf("no tool %q on %s (see `mcpcli %s tools`)", cargs[1], cargs[0], cargs[0])
			}
		}
		if format() == "json" {
			return writeJSON(stdout, t)
		}
		printTool(cargs[0], *t)
		return nil
	},
}

var callCmd = &cobra.Command{
	Use:   "call <profile> <tool> [key=value|key:=json ...]",
	Short: "Call a tool (also: mcpcli <profile> <tool> ...)",
	Long: "Arguments: key=value is a string, coerced to the type the tool's schema\n" +
		"declares (integer, number, boolean, arrays split on commas); key:=json passes\n" +
		"raw JSON; nested.key=value builds objects; --args gives a JSON object base.\n\n" +
		"Output: text content prints as is; JSON text or structured content renders as\n" +
		"a table (--fields to pick columns) or key/value lines; --json prints the raw\n" +
		"result; --format text prints only the text parts.",
	Example: `  mcpcli wispr-work search_meetings query="release review"
  mcpcli wispr-work get_meeting meeting_id=abc --format text
  mcpcli linear list_issues limit=5 --fields id,title --json
  mcpcli call deepwiki read_wiki_structure repoName=golang/go`,
	Args: cobra.MinimumNArgs(2),
	RunE: func(cmd *cobra.Command, cargs []string) error {
		ctx, cancel := ctxOf(cmd)
		defer cancel()
		profile, tool, pairs := cargs[0], cargs[1], cargs[2:]
		base, err := readJSONArg("args", callArgs)
		if err != nil {
			return err
		}
		var schema map[string]any
		if !callRaw {
			tools, err := loadTools(ctx, profile, false)
			if err != nil {
				return err
			}
			t := findTool(tools, tool)
			if t == nil {
				if tools, err = refreshTools(ctx, profile); err == nil {
					t = findTool(tools, tool)
				}
			}
			if t == nil {
				return fmt.Errorf("no tool %q on %s (see `mcpcli %s tools`)", tool, profile, profile)
			}
			schema = t.InputSchema
		}
		params, err := args.Parse(base, pairs, schema)
		if err != nil {
			return err
		}
		start := time.Now()
		raw, err := quickCall(ctx, profile, "tools/call", &mcp.CallToolParams{Name: tool, Arguments: params}, func(s *mcp.ClientSession) (any, error) {
			return s.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: params})
		})
		if err != nil {
			return fmt.Errorf("%s: %w", tool, err)
		}
		var res mcp.CallToolResult
		if err := json.Unmarshal(raw, &res); err != nil {
			return fmt.Errorf("%s: decode result: %w", tool, err)
		}
		if verbose {
			fmt.Fprintf(stderr, "(%s in %s)\n", tool, time.Since(start).Round(time.Millisecond))
		}
		return renderResult(&res)
	},
}

// renderResult prints a CallToolResult in the active format. Tool errors
// (IsError) go to stderr and exit non-zero so scripts notice.
func renderResult(res *mcp.CallToolResult) error {
	parts := result.Parts(res.Content)
	if res.IsError {
		if format() == "json" {
			writeJSON(stdout, result.Normalize(res))
		} else {
			fmt.Fprintln(stderr, "tool error: "+result.Text(parts))
		}
		return fmt.Errorf("tool returned an error")
	}
	switch format() {
	case "json":
		return writeJSON(stdout, result.Normalize(res))
	case "text":
		fmt.Fprintln(stdout, clip(result.Text(parts)))
		return nil
	}
	if res.StructuredContent != nil {
		return renderValue(result.Normalize(res.StructuredContent))
	}
	if len(parts) == 1 && parts[0].Kind == "text" {
		if v, ok := result.DecodeJSON(parts[0].Text); ok {
			return renderValue(v)
		}
	}
	fmt.Fprintln(stdout, clip(result.Text(parts)))
	return nil
}

// renderValue prints decoded JSON: rows as a table, an object as key/value
// lines, anything else as pretty JSON.
func renderValue(v any) error {
	if rows, key, ok := result.Rows(v); ok {
		if key != "" && format() != "csv" && format() != "tsv" {
			if m, ok := v.(map[string]any); ok {
				for k, val := range m {
					if k == key {
						continue
					}
					fmt.Fprintf(stdout, "%s: %s\n", k, compact(val))
				}
			}
		}
		return emitRows(rows, nil)
	}
	if m, ok := v.(map[string]any); ok {
		if len(fieldsFlag) > 0 {
			m = project(m, fieldsFlag)
		}
		// A one-field envelope around prose ({"result": "..."}) is just prose.
		if len(m) == 1 {
			for _, val := range m {
				if s, ok := val.(string); ok && strings.Contains(s, "\n") {
					fmt.Fprintln(stdout, clip(s))
					return nil
				}
			}
		}
		keys := sortedKeys(m)
		for _, k := range keys {
			fmt.Fprintf(stdout, "%s: %s\n", k, clip(compact(m[k])))
		}
		return nil
	}
	fmt.Fprintln(stdout, clip(prettyJSON(v)))
	return nil
}

func compact(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case nil:
		return ""
	case json.Number:
		return x.String()
	case bool:
		if x {
			return "true"
		}
		return "false"
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

func init() {
	toolsCmd.Flags().BoolVar(&toolsFull, "full", false, "print every tool's parameters")
	toolsCmd.Flags().BoolVar(&toolsRefresh, "refresh", false, "re-list from the server and update the cache")
	callCmd.Flags().StringVar(&callArgs, "args", "", "JSON object of arguments (inline, @file or - for stdin); key=value pairs override it")
	callCmd.Flags().BoolVar(&callRaw, "no-schema", false, "skip the tool schema: send key=value as strings")
	rootCmd.AddCommand(toolsCmd, toolCmd, callCmd)
}
