package cmd

import (
	_ "embed"
	"fmt"
	"os"
	"strings"

	"github.com/iamnikolie/mcpcli/internal/args"
	"github.com/spf13/cobra"
)

//go:embed skill.md
var skillDoc string

var skillOut string

var skillCmd = &cobra.Command{
	Use:   "skill [profile]",
	Short: "Print the agent reference; with a profile, generate a SKILL.md for that server",
	Example: `  mcpcli skill                                  # how to use mcpcli
  mcpcli skill wispr-work                       # SKILL.md for that server's tools
  mcpcli skill wispr-work -o ~/.claude/skills/wispr-work/SKILL.md`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, cargs []string) error {
		if len(cargs) == 0 {
			fmt.Fprint(stdout, skillDoc)
			return nil
		}
		ctx, cancel := ctxOf(cmd)
		defer cancel()
		profile := cargs[0]
		tools, err := loadTools(ctx, profile, false)
		if err != nil {
			return err
		}
		instructions, server := "", ""
		if _, session, err := connect(ctx, profile); err == nil {
			ir := session.InitializeResult()
			instructions = strings.TrimSpace(ir.Instructions)
			if ir.ServerInfo != nil {
				server = ir.ServerInfo.Name
			}
			session.Close()
		}
		doc := generateSkill(profile, server, instructions, tools)
		if skillOut == "" {
			fmt.Fprint(stdout, doc)
			return nil
		}
		if err := os.MkdirAll(dirOf(skillOut), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(skillOut, []byte(doc), 0o644); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "Wrote %s\n", skillOut)
		return nil
	},
}

func dirOf(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i > 0 {
		return p[:i]
	}
	return "."
}

// generateSkill renders a Claude Code style SKILL.md: frontmatter, how to
// call, one line per tool with required arguments, then per-tool details.
func generateSkill(profile, server, instructions string, tools []toolInfo) string {
	var b strings.Builder
	title := server
	if title == "" {
		title = profile
	}
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		names = append(names, t.Name)
	}
	desc := fmt.Sprintf("Use the `%s` MCP server via `mcpcli %s <tool> k=v` when the task needs its data or actions. Tools: %s.",
		title, profile, strings.Join(names, ", "))
	fmt.Fprintf(&b, "---\nname: mcp-%s\ndescription: %q\n---\n\n", profile, desc)
	fmt.Fprintf(&b, "# %s via mcpcli (profile `%s`)\n\n", title, profile)
	b.WriteString("Run tools from Bash; output is Markdown by default, `--json` for raw results,\n")
	b.WriteString("`--format text` for plain text, `--fields a,b` to pick columns, `--max-chars N`\n")
	b.WriteString("to cap long output. `key=value` is coerced to the schema type; `key:=json` passes\n")
	b.WriteString("raw JSON. `mcpcli " + profile + " tool <name>` prints a tool's full parameters.\n\n")
	if instructions != "" {
		b.WriteString("## Server instructions\n\n")
		b.WriteString(instructions)
		b.WriteString("\n\n")
	}
	b.WriteString("## Tools\n\n")
	for _, t := range tools {
		ps := args.Params(t.InputSchema)
		fmt.Fprintf(&b, "- `mcpcli %s %s%s` — %s\n", profile, t.Name, exampleArgs(ps), firstSentence(t.Description, 140))
	}
	b.WriteString("\n## Parameters\n\n")
	for _, t := range tools {
		ps := args.Params(t.InputSchema)
		fmt.Fprintf(&b, "### %s\n", t.Name)
		if d := strings.TrimSpace(t.Description); d != "" {
			b.WriteString(strings.Join(strings.Fields(d), " "))
			b.WriteString("\n")
		}
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
			b.WriteString(line + "\n")
		}
		b.WriteString("\n")
	}
	return b.String()
}

func init() {
	skillCmd.Flags().StringVarP(&skillOut, "out", "o", "", "write the generated SKILL.md here instead of stdout")
	rootCmd.AddCommand(skillCmd)
}
