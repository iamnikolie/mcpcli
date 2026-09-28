package cmd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/iamnikolie/mcpcli/internal/config"
	"github.com/iamnikolie/mcpcli/internal/oauth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/oauth2"
)

// authTransport adds the profile's credentials and static headers.
type authTransport struct {
	base    http.RoundTripper
	headers map[string]string
	bearer  func(ctx context.Context) (string, error)
}

func (t *authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	for k, v := range t.headers {
		r.Header.Set(k, v)
	}
	if t.bearer != nil {
		tok, err := t.bearer(req.Context())
		if err != nil {
			return nil, err
		}
		r.Header.Set("Authorization", "Bearer "+tok)
	}
	if verbose {
		fmt.Fprintf(stderr, "> %s %s\n", r.Method, r.URL)
	}
	resp, err := t.base.RoundTrip(r)
	if err == nil && verbose {
		fmt.Fprintf(stderr, "< %s\n", resp.Status)
	}
	if err == nil && resp.StatusCode == http.StatusUnauthorized {
		resp.Body.Close()
		return nil, errUnauthorized
	}
	return resp, err
}

var errUnauthorized = errors.New("server rejected the credentials (HTTP 401)")

// protocolVersion is the MCP revision mcpcli speaks; servers negotiate down
// from it if they are older.
const protocolVersion = "2025-11-25"

func baseHTTP() *http.Client {
	return &http.Client{Timeout: timeout}
}

// oauthClient rebuilds the OAuth client from the saved profile state.
func oauthClient(p *config.Profile) (*oauth.Client, error) {
	if p.OAuth == nil || p.OAuth.ClientID == "" {
		return nil, fmt.Errorf("profile %q has no OAuth registration: run `mcpcli login %s`", p.Name, p.Name)
	}
	o := p.OAuth
	return &oauth.Client{
		ClientID: o.ClientID, ClientSecret: o.ClientSecret,
		AuthURL: o.AuthURL, TokenURL: o.TokenURL, DeviceAuthURL: o.DeviceAuthURL,
		RedirectURL: o.RedirectURL, Scopes: o.Scopes, Resource: o.Resource,
	}, nil
}

// httpClientFor returns an HTTP client that authenticates as the profile.
func httpClientFor(p *config.Profile) (*http.Client, error) {
	t := &authTransport{base: http.DefaultTransport, headers: p.Headers}
	switch p.Auth {
	case config.AuthNone:
	case config.AuthBearer:
		tok := p.Bearer
		if p.BearerEnv != "" {
			tok = os.Getenv(p.BearerEnv)
			if tok == "" {
				return nil, fmt.Errorf("profile %q reads its token from $%s, which is empty", p.Name, p.BearerEnv)
			}
		}
		if tok == "" {
			return nil, fmt.Errorf("profile %q has no bearer token", p.Name)
		}
		t.bearer = func(context.Context) (string, error) { return tok, nil }
	default:
		c, err := oauthClient(p)
		if err != nil {
			return nil, err
		}
		saved, err := config.LoadToken(p.Name)
		if err != nil {
			return nil, err
		}
		if saved == nil {
			return nil, fmt.Errorf("profile %q is not logged in: run `mcpcli login %s`", p.Name, p.Name)
		}
		src := &oauth.TokenSource{
			Client: c, HTTP: baseHTTP(),
			Token: &oauth2.Token{AccessToken: saved.AccessToken, RefreshToken: saved.RefreshToken, TokenType: saved.TokenType, Expiry: saved.Expiry},
			OnSave: func(tok *oauth2.Token) error {
				return config.SaveToken(p.Name, &config.Token{AccessToken: tok.AccessToken, RefreshToken: tok.RefreshToken, TokenType: tok.TokenType, Expiry: tok.Expiry})
			},
		}
		t.bearer = func(ctx context.Context) (string, error) {
			tok, err := src.Get(ctx)
			if errors.Is(err, oauth.ErrNeedLogin) {
				return "", fmt.Errorf("profile %q needs a fresh login: run `mcpcli login %s` (%v)", p.Name, p.Name, err)
			}
			return tok, err
		}
	}
	return &http.Client{Transport: t, Timeout: timeout}, nil
}

// connect opens an MCP session for the profile. Callers must Close it.
func connect(ctx context.Context, name string) (*config.Profile, *mcp.ClientSession, error) {
	p, err := config.Load(name)
	if err != nil {
		return nil, nil, err
	}
	hc, err := httpClientFor(p)
	if err != nil {
		return nil, nil, err
	}
	transport := &mcp.StreamableClientTransport{Endpoint: p.URL, HTTPClient: hc, DisableStandaloneSSE: true, MaxRetries: 1}
	client := mcp.NewClient(&mcp.Implementation{Name: "mcpcli", Version: version}, nil)
	// Pin the protocol version: newer SDKs first probe with a stateless
	// server/discover request that most servers reject with a 400 before the
	// real initialize, which is a wasted round trip on every invocation.
	session, err := client.Connect(ctx, transport, &mcp.ClientSessionOptions{ProtocolVersion: protocolVersion})
	if err != nil {
		if errors.Is(err, errUnauthorized) {
			if p.Auth == config.AuthOAuth {
				return nil, nil, fmt.Errorf("%s: %v; run `mcpcli login %s`", p.URL, err, name)
			}
			return nil, nil, fmt.Errorf("%s: %v", p.URL, err)
		}
		return nil, nil, fmt.Errorf("connect %s: %w", p.URL, err)
	}
	return p, session, nil
}

func hostOf(u string) string {
	u = strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
	if i := strings.IndexAny(u, "/?"); i > 0 {
		return u[:i]
	}
	return u
}

func ago(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
