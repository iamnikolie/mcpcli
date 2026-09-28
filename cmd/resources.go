package cmd

import (
	"fmt"
	"strings"

	"github.com/iamnikolie/mcpcli/internal/args"
	"github.com/iamnikolie/mcpcli/internal/result"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
)

var infoCmd = &cobra.Command{
	Use:   "info <profile>",
	Short: "Server name, version, capabilities and instructions",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, cargs []string) error {
		ctx, cancel := ctxOf(cmd)
		defer cancel()
		p, session, err := connect(ctx, cargs[0])
		if err != nil {
			return err
		}
		defer session.Close()
		ir := session.InitializeResult()
		row := map[string]any{"profile": p.Name, "url": p.URL, "protocol": ir.ProtocolVersion}
		if ir.ServerInfo != nil {
			row["server"] = ir.ServerInfo.Name
			row["version"] = ir.ServerInfo.Version
			row["title"] = ir.ServerInfo.Title
		}
		var caps []string
		if ir.Capabilities != nil {
			if ir.Capabilities.Tools != nil {
				caps = append(caps, "tools")
			}
			if ir.Capabilities.Resources != nil {
				caps = append(caps, "resources")
			}
			if ir.Capabilities.Prompts != nil {
				caps = append(caps, "prompts")
			}
			if ir.Capabilities.Logging != nil {
				caps = append(caps, "logging")
			}
		}
		row["capabilities"] = strings.Join(caps, ", ")
		row["instructions"] = ir.Instructions
		if format() == "json" {
			return writeJSON(stdout, row)
		}
		for _, k := range []string{"profile", "url", "server", "version", "title", "protocol", "capabilities"} {
			if v := fmt.Sprint(row[k]); v != "" {
				fmt.Fprintf(stdout, "%s: %s\n", k, v)
			}
		}
		if ir.Instructions != "" {
			fmt.Fprintf(stdout, "\n%s\n", clip(strings.TrimSpace(ir.Instructions)))
		}
		return nil
	},
}

var resourcesCmd = &cobra.Command{
	Use:   "resources <profile>",
	Short: "List resources and resource templates",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, cargs []string) error {
		ctx, cancel := ctxOf(cmd)
		defer cancel()
		_, session, err := connect(ctx, cargs[0])
		if err != nil {
			return err
		}
		defer session.Close()
		var rows []map[string]any
		for r, err := range session.Resources(ctx, nil) {
			if err != nil {
				return fmt.Errorf("list resources: %w", err)
			}
			rows = append(rows, map[string]any{"uri": r.URI, "name": r.Name, "mime": r.MIMEType, "description": firstSentence(r.Description, 100)})
		}
		for t, err := range session.ResourceTemplates(ctx, nil) {
			if err != nil {
				return fmt.Errorf("list resource templates: %w", err)
			}
			rows = append(rows, map[string]any{"uri": t.URITemplate, "name": t.Name, "mime": t.MIMEType, "description": firstSentence(t.Description, 100) + " (template)"})
		}
		return emitRows(rows, []string{"uri", "name", "mime", "description"})
	},
}

var readCmd = &cobra.Command{
	Use:   "read <profile> <uri>",
	Short: "Read a resource",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, cargs []string) error {
		ctx, cancel := ctxOf(cmd)
		defer cancel()
		_, session, err := connect(ctx, cargs[0])
		if err != nil {
			return err
		}
		defer session.Close()
		res, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: cargs[1]})
		if err != nil {
			return fmt.Errorf("read %s: %w", cargs[1], err)
		}
		if format() == "json" {
			return writeJSON(stdout, result.Normalize(res))
		}
		var texts []string
		for _, c := range res.Contents {
			if c.Text != "" {
				texts = append(texts, c.Text)
			} else {
				texts = append(texts, fmt.Sprintf("[resource %s %s, %d bytes]", c.URI, c.MIMEType, len(c.Blob)))
			}
		}
		fmt.Fprintln(stdout, clip(strings.Join(texts, "\n\n")))
		return nil
	},
}

var promptsCmd = &cobra.Command{
	Use:   "prompts <profile>",
	Short: "List prompts",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, cargs []string) error {
		ctx, cancel := ctxOf(cmd)
		defer cancel()
		_, session, err := connect(ctx, cargs[0])
		if err != nil {
			return err
		}
		defer session.Close()
		var rows []map[string]any
		for p, err := range session.Prompts(ctx, nil) {
			if err != nil {
				return fmt.Errorf("list prompts: %w", err)
			}
			var as []string
			for _, a := range p.Arguments {
				s := a.Name
				if a.Required {
					s += "*"
				}
				as = append(as, s)
			}
			rows = append(rows, map[string]any{"prompt": p.Name, "arguments": strings.Join(as, ","), "description": firstSentence(p.Description, 100)})
		}
		return emitRows(rows, []string{"prompt", "arguments", "description"})
	},
}

var promptCmd = &cobra.Command{
	Use:   "prompt <profile> <name> [key=value ...]",
	Short: "Get a prompt's messages",
	Args:  cobra.MinimumNArgs(2),
	RunE: func(cmd *cobra.Command, cargs []string) error {
		ctx, cancel := ctxOf(cmd)
		defer cancel()
		params, err := args.Parse(nil, cargs[2:], nil)
		if err != nil {
			return err
		}
		strArgs := map[string]string{}
		for k, v := range params {
			strArgs[k] = fmt.Sprint(v)
		}
		_, session, err := connect(ctx, cargs[0])
		if err != nil {
			return err
		}
		defer session.Close()
		res, err := session.GetPrompt(ctx, &mcp.GetPromptParams{Name: cargs[1], Arguments: strArgs})
		if err != nil {
			return fmt.Errorf("prompt %s: %w", cargs[1], err)
		}
		if format() == "json" {
			return writeJSON(stdout, result.Normalize(res))
		}
		if res.Description != "" {
			fmt.Fprintf(stdout, "%s\n\n", res.Description)
		}
		for _, m := range res.Messages {
			parts := result.Parts([]mcp.Content{m.Content})
			fmt.Fprintf(stdout, "**%s:** %s\n\n", m.Role, clip(result.Text(parts)))
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(infoCmd, resourcesCmd, readCmd, promptsCmd, promptCmd)
}
