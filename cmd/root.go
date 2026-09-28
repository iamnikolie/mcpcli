package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"time"

	"github.com/iamnikolie/mcpcli/internal/config"
	"github.com/spf13/cobra"
)

// version is stamped at link time by the Makefile and by GoReleaser
// (-X github.com/iamnikolie/mcpcli/cmd.version=…).
var version = "dev"

func buildVersion() string {
	v := version
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, s := range info.Settings {
			if s.Key == "vcs.revision" {
				rev := s.Value
				if len(rev) > 12 {
					rev = rev[:12]
				}
				v += " (" + rev + ")"
			}
		}
	}
	return v
}

var (
	jsonOutput   bool
	outputFormat string
	fieldsFlag   []string
	maxChars     int
	timeout      time.Duration
	verbose      bool
)

var (
	stdout io.Writer = os.Stdout
	stderr io.Writer = os.Stderr
	stdin  io.Reader = os.Stdin
)

var rootCmd = &cobra.Command{
	Use:   "mcpcli",
	Short: "Call any remote MCP server from the shell, with OAuth profiles per account",
	Long: "mcpcli — remote MCP servers as a CLI.\n\n" +
		"  mcpcli add <profile> <url>          register a server (one profile per account)\n" +
		"  mcpcli login <profile>              OAuth sign-in (browser, or --device)\n" +
		"  mcpcli <profile> tools              list its tools\n" +
		"  mcpcli <profile> <tool> k=v ...     call a tool\n\n" +
		"Run 'mcpcli skill' for the full agent reference.",
	SilenceUsage: true,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		switch outputFormat {
		case "", "md", "table", "json", "csv", "tsv", "text":
			return nil
		default:
			return fmt.Errorf("--format must be one of md, table, json, csv, tsv, text")
		}
	},
}

// profileCommands take a profile as their first argument; the shorthand
// "mcpcli <profile> <tool> ..." rewrites to "mcpcli call <profile> <tool> ...".
var profileCommands = map[string]bool{
	"tools": true, "tool": true, "call": true, "info": true,
	"resources": true, "read": true, "prompts": true, "prompt": true,
	"skill": true, "login": true, "logout": true, "remove": true, "show": true,
}

// rewriteArgs implements the shorthand. It only fires when the first
// argument is an existing profile, so command names always win.
func rewriteArgs(args []string) []string {
	if len(args) == 0 {
		return args
	}
	first := args[0]
	if len(first) > 0 && first[0] == '-' {
		return args
	}
	if _, isCmd := knownCommands()[first]; isCmd {
		return args
	}
	if !config.Exists(first) {
		return args
	}
	if len(args) == 1 {
		return []string{"tools", first}
	}
	if profileCommands[args[1]] {
		return append([]string{args[1], first}, args[2:]...)
	}
	return append([]string{"call", first}, args[1:]...)
}

func knownCommands() map[string]struct{} {
	out := map[string]struct{}{"help": {}, "completion": {}}
	for _, c := range rootCmd.Commands() {
		out[c.Name()] = struct{}{}
		for _, a := range c.Aliases {
			out[a] = struct{}{}
		}
	}
	return out
}

// Execute runs the CLI and exits non-zero on error.
func Execute() {
	rootCmd.SetArgs(rewriteArgs(os.Args[1:]))
	if err := rootCmd.ExecuteContext(context.Background()); err != nil {
		os.Exit(1)
	}
}

func init() {
	pf := rootCmd.PersistentFlags()
	pf.BoolVar(&jsonOutput, "json", false, "JSON output (same as --format json)")
	pf.StringVar(&outputFormat, "format", "", "output format: md (default), table, json, csv, tsv, text")
	pf.StringSliceVar(&fieldsFlag, "fields", nil, "comma-separated fields to keep when a result renders as rows")
	pf.IntVar(&maxChars, "max-chars", 0, "truncate text output to this many characters (0 = no limit)")
	pf.DurationVar(&timeout, "timeout", 2*time.Minute, "per-request timeout")
	pf.BoolVar(&verbose, "verbose", false, "log HTTP requests to stderr")

	rootCmd.Version = buildVersion()
	rootCmd.AddCommand(versionCmd)
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Show the mcpcli version",
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Fprintf(stdout, "mcpcli version %s\n", buildVersion())
		return nil
	},
}

func ctxOf(cmd *cobra.Command) (context.Context, context.CancelFunc) {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	if timeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, timeout)
}
