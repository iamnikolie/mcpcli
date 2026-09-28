package cmd

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/iamnikolie/mcpcli/internal/config"
	"github.com/spf13/cobra"
)

var (
	addHeaders   []string
	addBearer    string
	addBearerEnv string
	addNoAuth    bool
	addForce     bool
)

var addCmd = &cobra.Command{
	Use:   "add <profile> <url>",
	Short: "Register a remote MCP server under a profile name",
	Long: "A profile is a server plus one identity. Add the same URL twice under\n" +
		"different names to keep two accounts apart (work vs personal).\n\n" +
		"Auth defaults to OAuth (run `mcpcli login <profile>` next). Use --bearer or\n" +
		"--bearer-env for servers that take a static token, --no-auth for open ones.",
	Example: `  mcpcli add wispr-me https://api.wisprflow.ai/connect/mcp
  mcpcli add wispr-work https://api.wisprflow.ai/connect/mcp
  mcpcli add deepwiki https://mcp.deepwiki.com/mcp --no-auth
  mcpcli add linear https://mcp.linear.app/mcp --bearer-env LINEAR_MCP_TOKEN`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		name, raw := args[0], args[1]
		if err := config.ValidName(name); err != nil {
			return err
		}
		if _, isCmd := knownCommands()[name]; isCmd || profileCommands[name] {
			return fmt.Errorf("%q is a command name; pick another profile name", name)
		}
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return fmt.Errorf("url must be http(s)://host/path, got %q", raw)
		}
		if config.Exists(name) && !addForce {
			return fmt.Errorf("profile %q exists; pass --force to overwrite (tokens are kept)", name)
		}
		p := &config.Profile{Name: name, URL: raw, Auth: config.AuthOAuth}
		if old, err := config.Load(name); err == nil {
			p.OAuth = old.OAuth
		}
		switch {
		case addNoAuth:
			p.Auth = config.AuthNone
		case addBearerEnv != "":
			p.Auth, p.BearerEnv = config.AuthBearer, addBearerEnv
		case addBearer != "":
			p.Auth, p.Bearer = config.AuthBearer, addBearer
		}
		for _, h := range addHeaders {
			k, v, err := config.ParseHeader(h)
			if err != nil {
				return err
			}
			if p.Headers == nil {
				p.Headers = map[string]string{}
			}
			p.Headers[k] = v
		}
		path, err := config.Save(p)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "Saved %s\n", path)
		if p.Auth == config.AuthOAuth {
			fmt.Fprintf(stderr, "Next: mcpcli login %s\n", name)
		} else {
			fmt.Fprintf(stderr, "Next: mcpcli %s tools\n", name)
		}
		return nil
	},
}

var profilesCmd = &cobra.Command{
	Use:     "profiles",
	Aliases: []string{"list", "ls"},
	Short:   "List profiles and their login state",
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		names, err := config.List()
		if err != nil {
			return err
		}
		rows := make([]map[string]any, 0, len(names))
		for _, n := range names {
			p, err := config.Load(n)
			if err != nil {
				rows = append(rows, map[string]any{"profile": n, "url": "", "auth": "", "state": "broken: " + err.Error()})
				continue
			}
			rows = append(rows, profileRow(p))
		}
		return emitRows(rows, []string{"profile", "url", "auth", "state", "tools"})
	},
}

func profileRow(p *config.Profile) map[string]any {
	state := "ready"
	switch p.Auth {
	case config.AuthOAuth:
		tok, _ := config.LoadToken(p.Name)
		switch {
		case tok == nil:
			state = "not logged in"
		case tok.Valid(0):
			state = "logged in (token expires " + ago2(tok) + ")"
		case tok.RefreshToken != "":
			state = "logged in (will refresh)"
		default:
			state = "expired: login again"
		}
	case config.AuthBearer:
		if p.BearerEnv != "" {
			state = "bearer from $" + p.BearerEnv
		} else {
			state = "bearer"
		}
	case config.AuthNone:
		state = "no auth"
	}
	tools := ""
	if c, _ := config.LoadTools(p.Name); c != nil {
		tools = fmt.Sprintf("%d cached %s", len(c.Tools), ago(c.FetchedAt))
	}
	return map[string]any{"profile": p.Name, "url": p.URL, "auth": p.Auth, "state": state, "tools": tools}
}

func ago2(t *config.Token) string {
	if t.Expiry.IsZero() {
		return "never"
	}
	d := t.Expiry.Sub(nowFn())
	switch {
	case d < 0:
		return "already"
	case d < 2*time.Minute:
		return "in under 2m"
	case d < 2*time.Hour:
		return fmt.Sprintf("in %dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("in %dh", int(d.Hours()))
	}
}

var showCmd = &cobra.Command{
	Use:   "show <profile>",
	Short: "Show one profile's configuration (secrets masked)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		p, err := config.Load(args[0])
		if err != nil {
			return err
		}
		row := profileRow(p)
		if len(p.Headers) > 0 {
			keys := make([]string, 0, len(p.Headers))
			for k := range p.Headers {
				keys = append(keys, k)
			}
			row["headers"] = strings.Join(keys, ", ")
		}
		if p.OAuth != nil {
			row["issuer"] = p.OAuth.Issuer
			row["client_id"] = p.OAuth.ClientID
			row["scopes"] = strings.Join(p.OAuth.Scopes, " ")
			row["device_flow"] = p.OAuth.DeviceAuthURL != ""
		}
		dir, _ := config.Dir(p.Name)
		row["dir"] = dir
		if format() == "json" {
			return writeJSON(stdout, row)
		}
		for _, k := range []string{"profile", "url", "auth", "state", "headers", "issuer", "client_id", "scopes", "device_flow", "tools", "dir"} {
			if v, ok := row[k]; ok && fmt.Sprint(v) != "" {
				fmt.Fprintf(stdout, "%s: %v\n", k, v)
			}
		}
		return nil
	},
}

var removeCmd = &cobra.Command{
	Use:     "remove <profile>",
	Aliases: []string{"rm"},
	Short:   "Delete a profile, its tokens and cache",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := config.Remove(args[0]); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "Removed profile %s\n", args[0])
		return nil
	},
}

var logoutCmd = &cobra.Command{
	Use:   "logout <profile>",
	Short: "Forget the profile's OAuth token (the registration is kept)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if _, err := config.Load(args[0]); err != nil {
			return err
		}
		if err := config.DeleteToken(args[0]); err != nil {
			return err
		}
		_ = config.DeleteSession(args[0])
		fmt.Fprintf(stdout, "Logged out %s\n", args[0])
		return nil
	},
}

func init() {
	addCmd.Flags().StringArrayVarP(&addHeaders, "header", "H", nil, "static header Name:value (repeatable)")
	addCmd.Flags().StringVar(&addBearer, "bearer", "", "static bearer token (stored in the profile, mode 0600)")
	addCmd.Flags().StringVar(&addBearerEnv, "bearer-env", "", "environment variable holding the bearer token")
	addCmd.Flags().BoolVar(&addNoAuth, "no-auth", false, "the server needs no credentials")
	addCmd.Flags().BoolVar(&addForce, "force", false, "overwrite an existing profile")
	rootCmd.AddCommand(addCmd, profilesCmd, showCmd, removeCmd, logoutCmd)
}
