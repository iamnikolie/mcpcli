// Package oauth implements the OAuth 2.1 client side of the MCP
// authorization spec: discovery from a 401, dynamic client registration,
// PKCE authorization-code flow with a loopback redirect, RFC 8628 device
// flow, and refresh with the RFC 8707 resource indicator.
package oauth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"
)

// Discovery is what the protected resource and its authorization server
// advertise.
type Discovery struct {
	Resource        string
	Issuer          string
	AuthURL         string
	TokenURL        string
	DeviceAuthURL   string
	RegistrationURL string
	Scopes          []string
	AuthMethods     []string
}

// Discover asks the MCP endpoint for its OAuth configuration: it expects a
// 401 with a WWW-Authenticate challenge naming the protected-resource
// metadata (RFC 9728), falls back to the well-known locations, then fetches
// the authorization server metadata (RFC 8414 / OIDC discovery).
func Discover(ctx context.Context, hc *http.Client, mcpURL string) (*Discovery, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, mcpURL, strings.NewReader(`{"jsonrpc":"2.0","id":0,"method":"ping"}`))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("probe %s: %w", mcpURL, err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	var metaURLs []string
	var challengeScopes []string
	if resp.StatusCode == http.StatusUnauthorized {
		if chs, err := oauthex.ParseWWWAuthenticate(resp.Header.Values("WWW-Authenticate")); err == nil {
			for _, ch := range chs {
				if u := ch.Params["resource_metadata"]; u != "" {
					metaURLs = append(metaURLs, u)
				}
				if s := ch.Params["scope"]; s != "" {
					challengeScopes = strings.Fields(s)
				}
			}
		}
	} else if resp.StatusCode < 400 {
		return nil, fmt.Errorf("%s answered %d without an OAuth challenge: it does not require login (use --no-auth)", mcpURL, resp.StatusCode)
	}
	u, err := url.Parse(mcpURL)
	if err != nil {
		return nil, err
	}
	base := u.Scheme + "://" + u.Host
	if p := strings.TrimSuffix(u.Path, "/"); p != "" {
		metaURLs = append(metaURLs, base+"/.well-known/oauth-protected-resource"+p)
	}
	metaURLs = append(metaURLs, base+"/.well-known/oauth-protected-resource")

	var prm *oauthex.ProtectedResourceMetadata
	var lastErr error
	for _, mu := range metaURLs {
		prm, lastErr = oauthex.GetProtectedResourceMetadata(ctx, mu, mcpURL, hc)
		if lastErr == nil && prm != nil {
			break
		}
	}
	if prm == nil {
		if lastErr == nil {
			lastErr = errors.New("no protected-resource metadata found")
		}
		return nil, fmt.Errorf("discover %s: %w", mcpURL, lastErr)
	}
	if len(prm.AuthorizationServers) == 0 {
		return nil, fmt.Errorf("discover %s: metadata lists no authorization servers", mcpURL)
	}
	d := &Discovery{Resource: prm.Resource, Scopes: prm.ScopesSupported}
	if d.Resource == "" {
		d.Resource = mcpURL
	}
	if len(d.Scopes) == 0 {
		d.Scopes = challengeScopes
	}
	issuer := prm.AuthorizationServers[0]
	as, err := fetchAuthServer(ctx, hc, issuer)
	if err != nil {
		return nil, err
	}
	d.Issuer, d.AuthURL, d.TokenURL = as.Issuer, as.AuthorizationEndpoint, as.TokenEndpoint
	d.DeviceAuthURL, d.RegistrationURL, d.AuthMethods = as.DeviceAuthorizationEndpoint, as.RegistrationEndpoint, as.TokenEndpointAuthMethodsSupported
	if len(d.Scopes) == 0 {
		d.Scopes = as.ScopesSupported
	}
	return d, nil
}

type authServerMeta struct {
	Issuer                            string   `json:"issuer"`
	AuthorizationEndpoint             string   `json:"authorization_endpoint"`
	TokenEndpoint                     string   `json:"token_endpoint"`
	DeviceAuthorizationEndpoint       string   `json:"device_authorization_endpoint"`
	RegistrationEndpoint              string   `json:"registration_endpoint"`
	ScopesSupported                   []string `json:"scopes_supported"`
	CodeChallengeMethodsSupported     []string `json:"code_challenge_methods_supported"`
	TokenEndpointAuthMethodsSupported []string `json:"token_endpoint_auth_methods_supported"`
}

// fetchAuthServer tries the RFC 8414 location, then OIDC discovery, honouring
// issuers with a path component.
func fetchAuthServer(ctx context.Context, hc *http.Client, issuer string) (*authServerMeta, error) {
	iu, err := url.Parse(issuer)
	if err != nil || iu.Host == "" {
		return nil, fmt.Errorf("bad issuer %q", issuer)
	}
	if iu.Scheme != "https" && !isLoopback(iu.Hostname()) {
		return nil, fmt.Errorf("issuer %q must use https", issuer)
	}
	base := iu.Scheme + "://" + iu.Host
	path := strings.TrimSuffix(iu.Path, "/")
	candidates := []string{
		base + "/.well-known/oauth-authorization-server" + path,
		base + path + "/.well-known/openid-configuration",
		base + "/.well-known/openid-configuration" + path,
	}
	var lastErr error
	for _, c := range candidates {
		m, err := getJSON[authServerMeta](ctx, hc, c)
		if err != nil {
			lastErr = err
			continue
		}
		if m.AuthorizationEndpoint == "" || m.TokenEndpoint == "" {
			lastErr = fmt.Errorf("%s: incomplete authorization server metadata", c)
			continue
		}
		if m.Issuer != "" && strings.TrimSuffix(m.Issuer, "/") != strings.TrimSuffix(issuer, "/") {
			lastErr = fmt.Errorf("%s: issuer %q does not match %q", c, m.Issuer, issuer)
			continue
		}
		if len(m.CodeChallengeMethodsSupported) > 0 && !contains(m.CodeChallengeMethodsSupported, "S256") {
			lastErr = fmt.Errorf("%s: server does not support PKCE S256", c)
			continue
		}
		if m.Issuer == "" {
			m.Issuer = issuer
		}
		return m, nil
	}
	return nil, fmt.Errorf("authorization server metadata for %s: %w", issuer, lastErr)
}

func getJSON[T any](ctx context.Context, hc *http.Client, u string) (*T, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d", u, resp.StatusCode)
	}
	var v T
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&v); err != nil {
		return nil, fmt.Errorf("%s: %w", u, err)
	}
	return &v, nil
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// Client is a registered (or preconfigured) OAuth client for one resource.
type Client struct {
	ClientID      string
	ClientSecret  string
	AuthURL       string
	TokenURL      string
	DeviceAuthURL string
	RedirectURL   string
	Scopes        []string
	Resource      string
}

// Register performs dynamic client registration (RFC 7591) as a public
// client using PKCE, advertising the grants we may use.
func Register(ctx context.Context, hc *http.Client, d *Discovery, redirectURL, clientName string) (*Client, error) {
	if d.RegistrationURL == "" {
		return nil, fmt.Errorf("%s does not support dynamic client registration; pass --client-id", d.Issuer)
	}
	grants := []string{"authorization_code", "refresh_token"}
	if d.DeviceAuthURL != "" {
		grants = append(grants, "urn:ietf:params:oauth:grant-type:device_code")
	}
	meta := &oauthex.ClientRegistrationMetadata{
		ClientName:              clientName,
		RedirectURIs:            []string{redirectURL},
		GrantTypes:              grants,
		ResponseTypes:           []string{"code"},
		TokenEndpointAuthMethod: "none",
		Scope:                   strings.Join(d.Scopes, " "),
	}
	resp, err := oauthex.RegisterClient(ctx, d.RegistrationURL, meta, hc)
	if err != nil {
		return nil, fmt.Errorf("register client at %s: %w", d.RegistrationURL, err)
	}
	return &Client{
		ClientID: resp.ClientID, ClientSecret: resp.ClientSecret,
		AuthURL: d.AuthURL, TokenURL: d.TokenURL, DeviceAuthURL: d.DeviceAuthURL,
		RedirectURL: redirectURL, Scopes: d.Scopes, Resource: d.Resource,
	}, nil
}

func (c *Client) config() *oauth2.Config {
	return &oauth2.Config{
		ClientID:     c.ClientID,
		ClientSecret: c.ClientSecret,
		RedirectURL:  c.RedirectURL,
		Scopes:       c.Scopes,
		Endpoint: oauth2.Endpoint{
			AuthURL:       c.AuthURL,
			TokenURL:      c.TokenURL,
			DeviceAuthURL: c.DeviceAuthURL,
			AuthStyle:     oauth2.AuthStyleInParams,
		},
	}
}

func (c *Client) resourceOpt() []oauth2.AuthCodeOption {
	if c.Resource == "" {
		return nil
	}
	return []oauth2.AuthCodeOption{oauth2.SetAuthURLParam("resource", c.Resource)}
}

// BrowserOptions controls the authorization-code flow.
type BrowserOptions struct {
	// Listener receives the redirect; it must match Client.RedirectURL.
	Listener net.Listener
	// Open is called with the authorization URL; nil prints it only.
	Open func(url string) error
	// Out receives the "open this URL" line.
	Out io.Writer
	// Timeout bounds the wait for the browser; 0 means 5 minutes.
	Timeout time.Duration
}

// AuthorizeBrowser runs the PKCE authorization-code flow: it prints or opens
// the authorization URL, waits for the loopback redirect and exchanges the
// code.
func AuthorizeBrowser(ctx context.Context, hc *http.Client, c *Client, opts BrowserOptions) (*oauth2.Token, error) {
	if opts.Timeout == 0 {
		opts.Timeout = 5 * time.Minute
	}
	ctx = context.WithValue(ctx, oauth2.HTTPClient, hc)
	ctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	verifier := oauth2.GenerateVerifier()
	state := randomString(24)
	cfg := c.config()
	authOpts := append([]oauth2.AuthCodeOption{oauth2.AccessTypeOffline, oauth2.S256ChallengeOption(verifier)}, c.resourceOpt()...)
	authURL := cfg.AuthCodeURL(state, authOpts...)

	type result struct {
		code string
		err  error
	}
	results := make(chan result, 1)
	ru, err := url.Parse(c.RedirectURL)
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	path := ru.Path
	if path == "" {
		path = "/"
	}
	mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if e := q.Get("error"); e != "" {
			http.Error(w, "Authorization failed: "+e+" "+q.Get("error_description"), http.StatusBadRequest)
			results <- result{err: fmt.Errorf("authorization failed: %s %s", e, q.Get("error_description"))}
			return
		}
		if q.Get("state") != state {
			http.Error(w, "State mismatch", http.StatusBadRequest)
			results <- result{err: errors.New("authorization failed: state mismatch")}
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, "<!doctype html><title>mcpcli</title><p style='font:16px system-ui;margin:2em'>Signed in. You can close this tab and return to the terminal.</p>")
		results <- result{code: q.Get("code")}
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(opts.Listener)
	defer srv.Close()

	if opts.Out != nil {
		fmt.Fprintf(opts.Out, "Open this URL to sign in:\n%s\n", authURL)
	}
	if opts.Open != nil {
		if err := opts.Open(authURL); err != nil && opts.Out != nil {
			fmt.Fprintf(opts.Out, "(could not open a browser: %v)\n", err)
		}
	}
	var code string
	select {
	case r := <-results:
		if r.err != nil {
			return nil, r.err
		}
		code = r.code
	case <-ctx.Done():
		return nil, fmt.Errorf("timed out waiting for the browser sign-in")
	}
	exOpts := append([]oauth2.AuthCodeOption{oauth2.VerifierOption(verifier)}, c.resourceOpt()...)
	tok, err := cfg.Exchange(ctx, code, exOpts...)
	if err != nil {
		return nil, fmt.Errorf("exchange code: %w", err)
	}
	return tok, nil
}

// AuthorizeDevice runs the RFC 8628 device flow: prints the user code and
// verification URL, then polls the token endpoint until approved.
func AuthorizeDevice(ctx context.Context, hc *http.Client, c *Client, out io.Writer) (*oauth2.Token, error) {
	if c.DeviceAuthURL == "" {
		return nil, errors.New("the authorization server does not advertise a device_authorization_endpoint; use the browser flow")
	}
	ctx = context.WithValue(ctx, oauth2.HTTPClient, hc)
	cfg := c.config()
	da, err := cfg.DeviceAuth(ctx, c.resourceOpt()...)
	if err != nil {
		return nil, fmt.Errorf("device authorization: %w", err)
	}
	if out != nil {
		if da.VerificationURIComplete != "" {
			fmt.Fprintf(out, "Open %s\n(or go to %s and enter code %s)\n", da.VerificationURIComplete, da.VerificationURI, da.UserCode)
		} else {
			fmt.Fprintf(out, "Go to %s and enter code %s\n", da.VerificationURI, da.UserCode)
		}
		fmt.Fprintln(out, "Waiting for approval...")
	}
	tok, err := cfg.DeviceAccessToken(ctx, da, c.resourceOpt()...)
	if err != nil {
		return nil, fmt.Errorf("device token: %w", err)
	}
	return tok, nil
}

// Refresh exchanges a refresh token, sending the resource indicator so the
// new access token is minted for the same MCP server.
func Refresh(ctx context.Context, hc *http.Client, c *Client, refreshToken string) (*oauth2.Token, error) {
	if refreshToken == "" {
		return nil, errors.New("no refresh token")
	}
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {c.ClientID},
	}
	if c.ClientSecret != "" {
		form.Set("client_secret", c.ClientSecret)
	}
	if c.Resource != "" {
		form.Set("resource", c.Resource)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("refresh token: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("refresh token: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var tr struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		TokenType    string `json:"token_type"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tr); err != nil {
		return nil, fmt.Errorf("refresh token: %w", err)
	}
	if tr.AccessToken == "" {
		return nil, errors.New("refresh token: response has no access_token")
	}
	tok := &oauth2.Token{AccessToken: tr.AccessToken, RefreshToken: tr.RefreshToken, TokenType: tr.TokenType}
	if tok.RefreshToken == "" {
		tok.RefreshToken = refreshToken
	}
	if tr.ExpiresIn > 0 {
		tok.Expiry = time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	}
	return tok, nil
}

// TokenSource hands out a valid access token, refreshing through Refresh
// when the saved one is within Buffer of expiry, and reports new tokens to
// OnSave so they can be persisted.
type TokenSource struct {
	Client *Client
	HTTP   *http.Client
	Token  *oauth2.Token
	OnSave func(*oauth2.Token) error
	Buffer time.Duration

	mu sync.Mutex
}

// ErrNeedLogin is returned when there is no token and no way to refresh.
var ErrNeedLogin = errors.New("not logged in")

// Get returns a usable access token.
func (s *TokenSource) Get(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	buf := s.Buffer
	if buf == 0 {
		buf = 30 * time.Second
	}
	if s.Token != nil && s.Token.AccessToken != "" && (s.Token.Expiry.IsZero() || time.Now().Add(buf).Before(s.Token.Expiry)) {
		return s.Token.AccessToken, nil
	}
	if s.Token == nil || s.Token.RefreshToken == "" {
		return "", ErrNeedLogin
	}
	tok, err := Refresh(ctx, s.HTTP, s.Client, s.Token.RefreshToken)
	if err != nil {
		return "", fmt.Errorf("%w (%v)", ErrNeedLogin, err)
	}
	s.Token = tok
	if s.OnSave != nil {
		if err := s.OnSave(tok); err != nil {
			return "", err
		}
	}
	return tok.AccessToken, nil
}

func randomString(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
