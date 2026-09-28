package oauth

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/iamnikolie/mcpcli/internal/oauth/oauthtest"
	"golang.org/x/oauth2"
)

func TestDiscoverAndRegister(t *testing.T) {
	f := oauthtest.New()
	defer f.Close()
	ctx := context.Background()
	d, err := Discover(ctx, http.DefaultClient, f.URL()+"/mcp")
	if err != nil {
		t.Fatal(err)
	}
	if d.Resource != f.URL()+"/mcp" || d.Issuer != f.URL() || d.AuthURL != f.URL()+"/authorize" || d.DeviceAuthURL != f.URL()+"/device" || d.RegistrationURL == "" {
		t.Fatalf("discovery: %+v", d)
	}
	if strings.Join(d.Scopes, " ") != "openid offline_access" {
		t.Fatalf("scopes from protected resource metadata: %v", d.Scopes)
	}
	c, err := Register(ctx, http.DefaultClient, d, "http://127.0.0.1:1/callback", "test")
	if err != nil {
		t.Fatal(err)
	}
	if c.ClientID == "" || c.Resource != d.Resource || c.RedirectURL != "http://127.0.0.1:1/callback" {
		t.Fatalf("client: %+v", c)
	}
	// An open server (no 401) is reported as such.
	open := oauthtest.New()
	defer open.Close()
	if _, err := Discover(ctx, http.DefaultClient, open.URL()+"/.well-known/oauth-authorization-server"); err == nil || !strings.Contains(err.Error(), "does not require login") {
		t.Fatalf("want no-login error, got %v", err)
	}
}

func TestBrowserFlow(t *testing.T) {
	f := oauthtest.New()
	defer f.Close()
	ctx := context.Background()
	d, err := Discover(ctx, http.DefaultClient, f.URL()+"/mcp")
	if err != nil {
		t.Fatal(err)
	}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer lis.Close()
	redirect := "http://" + lis.Addr().String() + "/callback"
	c, err := Register(ctx, http.DefaultClient, d, redirect, "test")
	if err != nil {
		t.Fatal(err)
	}
	// "Open" the browser: follow the authorize redirect back to our listener.
	open := func(u string) error {
		go func() {
			resp, err := http.Get(u)
			if err == nil {
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			}
		}()
		return nil
	}
	var out strings.Builder
	tok, err := AuthorizeBrowser(ctx, http.DefaultClient, c, BrowserOptions{Listener: lis, Open: open, Out: &out, Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(tok.AccessToken, "at-") || tok.RefreshToken != "rt-1" || tok.Expiry.IsZero() {
		t.Fatalf("token: %+v", tok)
	}
	if !strings.Contains(out.String(), "code_challenge_method=S256") || !strings.Contains(out.String(), "resource=") {
		t.Fatalf("auth url must carry PKCE and resource:\n%s", out.String())
	}
	if f.LastResource != f.URL()+"/mcp" {
		t.Fatalf("token exchange must send resource, got %q", f.LastResource)
	}
}

func TestBrowserFlowStateMismatch(t *testing.T) {
	f := oauthtest.New()
	defer f.Close()
	ctx := context.Background()
	d, err := Discover(ctx, http.DefaultClient, f.URL()+"/mcp")
	if err != nil {
		t.Fatal(err)
	}
	lis, _ := net.Listen("tcp", "127.0.0.1:0")
	defer lis.Close()
	c, _ := Register(ctx, http.DefaultClient, d, "http://"+lis.Addr().String()+"/callback", "test")
	open := func(u string) error {
		go http.Get("http://" + lis.Addr().String() + "/callback?code=x&state=forged")
		return nil
	}
	_, err = AuthorizeBrowser(ctx, http.DefaultClient, c, BrowserOptions{Listener: lis, Open: open, Timeout: 5 * time.Second})
	if err == nil || !strings.Contains(err.Error(), "state mismatch") {
		t.Fatalf("want state mismatch, got %v", err)
	}
}

func TestDeviceFlow(t *testing.T) {
	f := oauthtest.New()
	defer f.Close()
	f.AutoApprove = false
	ctx := context.Background()
	d, err := Discover(ctx, http.DefaultClient, f.URL()+"/mcp")
	if err != nil {
		t.Fatal(err)
	}
	c, _ := Register(ctx, http.DefaultClient, d, "", "test")
	f.ApproveLater(300 * time.Millisecond)
	var out strings.Builder
	tok, err := AuthorizeDevice(ctx, http.DefaultClient, c, &out)
	if err != nil {
		t.Fatal(err)
	}
	if tok.RefreshToken != "rt-dev" || !strings.Contains(out.String(), "ABCD-EFGH") {
		t.Fatalf("device: %+v\n%s", tok, out.String())
	}
	c.DeviceAuthURL = ""
	if _, err := AuthorizeDevice(ctx, http.DefaultClient, c, nil); err == nil {
		t.Fatal("no device endpoint must error")
	}
}

func TestRefreshAndTokenSource(t *testing.T) {
	f := oauthtest.New()
	defer f.Close()
	ctx := context.Background()
	c := &Client{ClientID: "client-a", TokenURL: f.URL() + "/token", Resource: f.URL() + "/mcp"}
	tok, err := Refresh(ctx, http.DefaultClient, c, "rt-1")
	if err != nil {
		t.Fatal(err)
	}
	if tok.RefreshToken != "rt-2" || f.LastRefresh.Get("resource") != c.Resource || f.LastRefresh.Get("client_id") != "client-a" {
		t.Fatalf("refresh: %+v form=%v", tok, f.LastRefresh)
	}
	if _, err := Refresh(ctx, http.DefaultClient, c, "rt-bad"); err == nil {
		t.Fatal("bad refresh must error")
	}

	var saved *oauth2.Token
	src := &TokenSource{Client: c, HTTP: http.DefaultClient,
		Token:  &oauth2.Token{AccessToken: "at-old", RefreshToken: "rt-1", Expiry: time.Now().Add(-time.Minute)},
		OnSave: func(t *oauth2.Token) error { saved = t; return nil }}
	got, err := src.Get(ctx)
	if err != nil || !strings.HasPrefix(got, "at-") || got == "at-old" || saved == nil {
		t.Fatalf("expired token must refresh and save: %q %v", got, err)
	}
	again, _ := src.Get(ctx)
	if again != got || f.RefreshCount != 3 {
		t.Fatalf("fresh token must be reused without refreshing: %q refreshes=%d", again, f.RefreshCount)
	}
	src = &TokenSource{Client: c, HTTP: http.DefaultClient, Token: &oauth2.Token{AccessToken: "at-old", Expiry: time.Now().Add(-time.Minute)}}
	if _, err := src.Get(ctx); err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Fatalf("expired without refresh token → ErrNeedLogin, got %v", err)
	}
	src = &TokenSource{Client: c, HTTP: http.DefaultClient, Token: &oauth2.Token{AccessToken: "at-forever"}}
	if got, _ := src.Get(ctx); got != "at-forever" {
		t.Fatal("token without expiry is always valid")
	}
}
