// Package oauthtest is a fake OAuth 2.1 authorization server plus protected
// MCP endpoint for tests: metadata, dynamic registration, authorize
// (auto-approves and redirects), device flow, and a token endpoint for
// code, device_code and refresh_token grants.
package oauthtest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
)

// Server is the fake authorization server. Exported counters let tests
// assert on what the client sent.
type Server struct {
	Srv *httptest.Server
	mu  sync.Mutex

	clients     map[string][]string // client_id → redirect_uris
	codes       map[string]codeRec
	deviceCodes map[string]bool // device_code → approved

	RefreshCount int
	TokenCount   int
	LastResource string
	LastRefresh  url.Values
	ExpiresIn    int
	AutoApprove  bool
	MCPHits      int
	// MCP, when set, serves /mcp behind the bearer check.
	MCP http.Handler
}

type codeRec struct {
	challenge string
	redirect  string
	resource  string
}

// New starts the fake server; call Close when done.
func New() *Server {
	f := &Server{clients: map[string][]string{}, codes: map[string]codeRec{}, deviceCodes: map[string]bool{}, ExpiresIn: 3600, AutoApprove: true}
	mux := http.NewServeMux()
	f.Srv = httptest.NewServer(mux)
	base := f.Srv.URL
	jsonOK := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(v)
	}

	// Protected MCP endpoint: 401 with a challenge unless the bearer is valid.
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.MCPHits++
		mcp := f.MCP
		f.mu.Unlock()
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer at-") {
			w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+base+`/.well-known/oauth-protected-resource/mcp", scope="openid offline_access"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if mcp != nil {
			mcp.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"jsonrpc":"2.0","id":0,"result":{}}`))
	})
	mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", func(w http.ResponseWriter, r *http.Request) {
		jsonOK(w, map[string]any{
			"resource": base + "/mcp", "authorization_servers": []string{base}, "scopes_supported": []string{"openid", "offline_access"},
		})
	})
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		jsonOK(w, map[string]any{
			"issuer": base, "authorization_endpoint": base + "/authorize", "token_endpoint": base + "/token",
			"registration_endpoint": base + "/register", "device_authorization_endpoint": base + "/device",
			"code_challenge_methods_supported": []string{"S256"}, "grant_types_supported": []string{"authorization_code", "refresh_token", "urn:ietf:params:oauth:grant-type:device_code"},
			"response_types_supported": []string{"code"}, "scopes_supported": []string{"openid", "offline_access", "profile"},
			"token_endpoint_auth_methods_supported": []string{"none"},
		})
	})
	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		var meta struct {
			RedirectURIs []string `json:"redirect_uris"`
			GrantTypes   []string `json:"grant_types"`
		}
		json.NewDecoder(r.Body).Decode(&meta)
		f.mu.Lock()
		id := "client-" + string(rune('a'+len(f.clients)))
		f.clients[id] = meta.RedirectURIs
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{"client_id": id, "redirect_uris": meta.RedirectURIs, "grant_types": meta.GrantTypes, "token_endpoint_auth_method": "none"})
	})
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		f.mu.Lock()
		uris, ok := f.clients[q.Get("client_id")]
		f.mu.Unlock()
		if !ok || q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
			http.Error(w, "bad authorize request", http.StatusBadRequest)
			return
		}
		redirect := q.Get("redirect_uri")
		if len(uris) > 0 && uris[0] != redirect {
			http.Error(w, "redirect_uri mismatch", http.StatusBadRequest)
			return
		}
		code := "code-" + q.Get("state")[:6]
		f.mu.Lock()
		f.codes[code] = codeRec{challenge: q.Get("code_challenge"), redirect: redirect, resource: q.Get("resource")}
		f.mu.Unlock()
		http.Redirect(w, r, redirect+"?code="+url.QueryEscape(code)+"&state="+url.QueryEscape(q.Get("state")), http.StatusFound)
	})
	mux.HandleFunc("/device", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.deviceCodes["dev-1"] = f.AutoApprove
		f.mu.Unlock()
		jsonOK(w, map[string]any{"device_code": "dev-1", "user_code": "ABCD-EFGH", "verification_uri": base + "/verify", "verification_uri_complete": base + "/verify?code=ABCD-EFGH", "expires_in": 600, "interval": 0})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		f.mu.Lock()
		defer f.mu.Unlock()
		f.TokenCount++
		f.LastResource = r.Form.Get("resource")
		issue := func(refresh string) {
			jsonOK(w, map[string]any{"access_token": "at-" + strings.Repeat("x", f.TokenCount), "token_type": "Bearer", "expires_in": f.ExpiresIn, "refresh_token": refresh})
		}
		switch r.Form.Get("grant_type") {
		case "authorization_code":
			rec, ok := f.codes[r.Form.Get("code")]
			if !ok {
				http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
				return
			}
			if verifier := r.Form.Get("code_verifier"); verifier == "" || s256(verifier) != rec.challenge {
				http.Error(w, `{"error":"invalid_grant","error_description":"pkce"}`, http.StatusBadRequest)
				return
			}
			delete(f.codes, r.Form.Get("code"))
			issue("rt-1")
		case "urn:ietf:params:oauth:grant-type:device_code":
			if !f.deviceCodes[r.Form.Get("device_code")] {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(`{"error":"authorization_pending"}`))
				return
			}
			issue("rt-dev")
		case "refresh_token":
			f.RefreshCount++
			f.LastRefresh = r.Form
			if r.Form.Get("refresh_token") == "rt-bad" {
				http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
				return
			}
			issue("rt-2")
		default:
			http.Error(w, `{"error":"unsupported_grant_type"}`, http.StatusBadRequest)
		}
	})
	return f
}

// Close shuts the server down.
func (f *Server) Close() { f.Srv.Close() }

// URL is the base URL; the protected MCP endpoint is URL()+"/mcp".
func (f *Server) URL() string { return f.Srv.URL }

func s256(verifier string) string { return oauth2.S256ChallengeFromVerifier(verifier) }

// ApproveLater approves the pending device authorization after d.
func (f *Server) ApproveLater(d time.Duration) {
	go func() {
		time.Sleep(d)
		f.mu.Lock()
		f.deviceCodes["dev-1"] = true
		f.mu.Unlock()
	}()
}
