package cmd

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/iamnikolie/mcpcli/internal/config"
	"github.com/iamnikolie/mcpcli/internal/oauth"
	"github.com/spf13/cobra"
	"golang.org/x/oauth2"
)

var (
	loginDevice    bool
	loginNoBrowser bool
	loginPort      int
	loginScopes    []string
	loginClientID  string
	loginClientSec string
	loginRediscov  bool
)

// nowFn is swapped in tests.
var nowFn = time.Now

// openBrowser is swapped in tests.
var openBrowser = func(u string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", u).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", u).Start()
	default:
		return exec.Command("xdg-open", u).Start()
	}
}

var loginCmd = &cobra.Command{
	Use:   "login <profile>",
	Short: "Sign in with OAuth: discovers the server's auth, registers a client, opens the browser",
	Long: "Discovery follows the MCP authorization spec (401 challenge → protected-resource\n" +
		"metadata → authorization-server metadata), registers mcpcli as a public PKCE\n" +
		"client (RFC 7591) and runs the authorization-code flow with a loopback redirect.\n" +
		"--device uses the RFC 8628 device flow instead: no browser on this machine needed.",
	Example: `  mcpcli login wispr-work
  mcpcli login wispr-work --device          # on a server: shows a code to enter elsewhere
  mcpcli login wispr-work --no-browser      # print the URL instead of opening it`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		p, err := config.Load(args[0])
		if err != nil {
			return err
		}
		if p.Auth != config.AuthOAuth {
			return fmt.Errorf("profile %q uses auth=%s; login only applies to oauth profiles", p.Name, p.Auth)
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Minute)
		defer cancel()
		hc := baseHTTP()

		var lis net.Listener
		redirect := ""
		if !loginDevice {
			lis, err = net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", loginPort))
			if err != nil {
				return fmt.Errorf("listen for the OAuth redirect: %w (pass --port)", err)
			}
			defer lis.Close()
			redirect = fmt.Sprintf("http://%s/callback", lis.Addr().String())
		}

		// Reuse the registration when nothing about it changed; otherwise
		// discover and register again.
		reuse := p.OAuth != nil && p.OAuth.ClientID != "" && !loginRediscov && loginClientID == "" &&
			(loginDevice || p.OAuth.RedirectURL == redirect)
		if !reuse {
			fmt.Fprintf(stderr, "Discovering OAuth configuration for %s ...\n", p.URL)
			d, err := oauth.Discover(ctx, hc, p.URL)
			if err != nil {
				return err
			}
			if len(loginScopes) > 0 {
				d.Scopes = loginScopes
			}
			var c *oauth.Client
			if loginClientID != "" {
				c = &oauth.Client{ClientID: loginClientID, ClientSecret: loginClientSec, AuthURL: d.AuthURL, TokenURL: d.TokenURL,
					DeviceAuthURL: d.DeviceAuthURL, RedirectURL: redirect, Scopes: d.Scopes, Resource: d.Resource}
			} else {
				fmt.Fprintf(stderr, "Registering client with %s ...\n", d.Issuer)
				c, err = oauth.Register(ctx, hc, d, redirect, "mcpcli")
				if err != nil {
					return err
				}
			}
			p.OAuth = &config.OAuthState{
				Issuer: d.Issuer, AuthURL: c.AuthURL, TokenURL: c.TokenURL, DeviceAuthURL: c.DeviceAuthURL,
				RegistrationURL: d.RegistrationURL, ClientID: c.ClientID, ClientSecret: c.ClientSecret,
				RedirectURL: redirect, Scopes: c.Scopes, Resource: c.Resource,
			}
			if _, err := config.Save(p); err != nil {
				return err
			}
		}
		c, err := oauthClient(p)
		if err != nil {
			return err
		}
		if loginDevice {
			c.RedirectURL = ""
		}

		var tok *oauth2.Token
		if loginDevice {
			tok, err = oauth.AuthorizeDevice(ctx, hc, c, stderr)
		} else {
			open := openBrowser
			if loginNoBrowser {
				open = nil
			}
			tok, err = oauth.AuthorizeBrowser(ctx, hc, c, oauth.BrowserOptions{Listener: lis, Open: open, Out: stderr})
		}
		if err != nil {
			return err
		}
		if err := config.SaveToken(p.Name, &config.Token{AccessToken: tok.AccessToken, RefreshToken: tok.RefreshToken, TokenType: tok.TokenType, Expiry: tok.Expiry}); err != nil {
			return err
		}
		exp := "no expiry"
		if !tok.Expiry.IsZero() {
			exp = "expires " + tok.Expiry.Local().Format(time.RFC3339)
		}
		refresh := "no refresh token: you will need to log in again"
		if tok.RefreshToken != "" {
			refresh = "refresh token saved"
		}
		fmt.Fprintf(stdout, "Logged in %s (%s; %s)\n", p.Name, exp, refresh)

		// Warm the tool cache so shorthand calls can coerce arguments.
		if _, err := refreshTools(ctx, p.Name); err != nil {
			fmt.Fprintf(stderr, "note: could not list tools yet: %v\n", err)
		} else if c, _ := config.LoadTools(p.Name); c != nil {
			names := make([]string, 0, len(c.Tools))
			for _, t := range cachedTools(c) {
				names = append(names, t.Name)
			}
			fmt.Fprintf(stdout, "%d tools: %s\n", len(names), strings.Join(names, ", "))
		}
		return nil
	},
}

func init() {
	loginCmd.Flags().BoolVar(&loginDevice, "device", false, "use the device-code flow (no local browser)")
	loginCmd.Flags().BoolVar(&loginNoBrowser, "no-browser", false, "print the sign-in URL instead of opening a browser")
	loginCmd.Flags().IntVar(&loginPort, "port", 0, "fixed port for the loopback redirect (default: random)")
	loginCmd.Flags().StringSliceVar(&loginScopes, "scope", nil, "override the scopes to request")
	loginCmd.Flags().StringVar(&loginClientID, "client-id", "", "use a pre-registered client instead of dynamic registration")
	loginCmd.Flags().StringVar(&loginClientSec, "client-secret", "", "secret for --client-id, if the client is confidential")
	loginCmd.Flags().BoolVar(&loginRediscov, "rediscover", false, "ignore the saved registration and discover again")
	rootCmd.AddCommand(loginCmd)
}
