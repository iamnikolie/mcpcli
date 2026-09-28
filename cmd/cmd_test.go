package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamnikolie/mcpcli/internal/config"
	"github.com/iamnikolie/mcpcli/internal/oauth/oauthtest"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// resetFlags restores every flag to its default: cobra keeps parsed values in
// package variables between Execute calls.
func resetFlags(c *cobra.Command) {
	reset := func(f *pflag.Flag) {
		if sv, ok := f.Value.(pflag.SliceValue); ok {
			sv.Replace(nil)
		} else {
			f.Value.Set(f.DefValue)
		}
		f.Changed = false
	}
	c.Flags().VisitAll(reset)
	c.PersistentFlags().VisitAll(reset)
	for _, sub := range c.Commands() {
		resetFlags(sub)
	}
}

type addIn struct {
	A int `json:"a" jsonschema:"first number"`
	B int `json:"b" jsonschema:"second number"`
}
type addOut struct {
	Sum int `json:"sum"`
}
type echoIn struct {
	Text  string   `json:"text"`
	Tags  []string `json:"tags,omitempty"`
	Loud  bool     `json:"loud,omitempty"`
	Limit *int     `json:"limit,omitempty"`
}

// newMCP builds an in-process MCP server with a few tools, a resource and a prompt.
func newMCP() *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "fake", Version: "1.2.3"}, &mcp.ServerOptions{Instructions: "Use echo for text."})
	mcp.AddTool(s, &mcp.Tool{Name: "echo", Description: "Echo text back. Second sentence."}, func(ctx context.Context, req *mcp.CallToolRequest, in echoIn) (*mcp.CallToolResult, any, error) {
		b, _ := json.Marshal(in)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "echo:" + string(b)}}}, nil, nil
	})
	mcp.AddTool(s, &mcp.Tool{Name: "add", Description: "Add two integers."}, func(ctx context.Context, req *mcp.CallToolRequest, in addIn) (*mcp.CallToolResult, addOut, error) {
		return nil, addOut{Sum: in.A + in.B}, nil
	})
	mcp.AddTool(s, &mcp.Tool{Name: "rows", Description: "Return a JSON list."}, func(ctx context.Context, req *mcp.CallToolRequest, in struct{}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: `{"total":2,"items":[{"id":1,"name":"a","x":"y"},{"id":2,"name":"b","x":"z"}]}`}}}, nil, nil
	})
	mcp.AddTool(s, &mcp.Tool{Name: "fail", Description: "Always fails."}, func(ctx context.Context, req *mcp.CallToolRequest, in struct{}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "boom"}}}, nil, nil
	})
	mcp.AddTool(s, &mcp.Tool{Name: "long", Description: "Long prose."}, func(ctx context.Context, req *mcp.CallToolRequest, in struct{}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: strings.Repeat("word ", 100)}}}, nil, nil
	})
	s.AddResource(&mcp.Resource{URI: "mem://notes", Name: "notes", MIMEType: "text/plain", Description: "Some notes."}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: "mem://notes", MIMEType: "text/plain", Text: "note body"}}}, nil
	})
	s.AddPrompt(&mcp.Prompt{Name: "greet", Description: "Greeting prompt.", Arguments: []*mcp.PromptArgument{{Name: "name", Required: true}}}, func(ctx context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		return &mcp.GetPromptResult{Messages: []*mcp.PromptMessage{{Role: "user", Content: &mcp.TextContent{Text: "Hello " + req.Params.Arguments["name"]}}}}, nil
	})
	return s
}

func handler(s *mcp.Server) http.Handler {
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, &mcp.StreamableHTTPOptions{Stateless: true})
}

// run executes the CLI with a temp MCPCLI_HOME.
func run(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	resetFlags(rootCmd)
	var out, errb bytes.Buffer
	stdout, stderr = &out, &errb
	t.Cleanup(func() { stdout, stderr = os.Stdout, os.Stderr })
	rootCmd.SetArgs(rewriteArgs(args))
	err := rootCmd.ExecuteContext(context.Background())
	return out.String(), errb.String(), err
}

func home(t *testing.T) string {
	t.Helper()
	h := t.TempDir()
	t.Setenv("MCPCLI_HOME", h)
	return h
}

func TestProfilesLifecycle(t *testing.T) {
	home(t)
	if _, _, err := run(t, "add", "bad name", "https://x/mcp"); err == nil {
		t.Fatal("bad name must error")
	}
	if _, _, err := run(t, "add", "tools", "https://x/mcp"); err == nil {
		t.Fatal("command name must be rejected as profile")
	}
	if _, _, err := run(t, "add", "p1", "x/mcp"); err == nil {
		t.Fatal("bad url must error")
	}
	out, errs, err := run(t, "add", "p1", "https://x/mcp", "-H", "X-Team: a")
	if err != nil || !strings.Contains(out, "Saved") || !strings.Contains(errs, "mcpcli login p1") {
		t.Fatalf("add: %v %q %q", err, out, errs)
	}
	if _, _, err := run(t, "add", "p1", "https://y/mcp"); err == nil {
		t.Fatal("duplicate without --force must error")
	}
	out, _, _ = run(t, "profiles")
	if !strings.Contains(out, "| p1 | https://x/mcp | oauth | not logged in |") {
		t.Fatalf("profiles:\n%s", out)
	}
	out, _, _ = run(t, "show", "p1")
	if !strings.Contains(out, "headers: X-Team") {
		t.Fatalf("show:\n%s", out)
	}
	if _, _, err := run(t, "logout", "p1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := run(t, "remove", "p1"); err != nil {
		t.Fatal(err)
	}
	if config.Exists("p1") {
		t.Fatal("profile still exists")
	}
	if _, _, err := run(t, "tools", "p1"); err == nil || !strings.Contains(err.Error(), "profile not found") {
		t.Fatalf("missing profile error: %v", err)
	}
}

func TestRewriteArgs(t *testing.T) {
	home(t)
	run(t, "add", "srv", "https://x/mcp", "--no-auth")
	cases := map[string]string{
		"srv":                 "tools srv",
		"srv tools --refresh": "tools srv --refresh",
		"srv echo text=hi":    "call srv echo text=hi",
		"srv skill -o x":      "skill srv -o x",
		"call srv echo":       "call srv echo",
		"nosuch echo":         "nosuch echo",
		"--json srv echo":     "--json srv echo",
		"profiles":            "profiles",
		"tools srv":           "tools srv",
		"srv login --device":  "login srv --device",
	}
	for in, want := range cases {
		got := strings.Join(rewriteArgs(strings.Fields(in)), " ")
		if got != want {
			t.Errorf("rewrite %q = %q, want %q", in, got, want)
		}
	}
}

func TestNoAuthServer(t *testing.T) {
	home(t)
	srv := httptest.NewServer(handler(newMCP()))
	defer srv.Close()
	run(t, "add", "fake", srv.URL, "--no-auth")

	out, _, err := run(t, "fake")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "| add | a,b | Add two integers. |") || !strings.Contains(out, "| echo | text | Echo text back. |") {
		t.Fatalf("tools:\n%s", out)
	}
	if c, _ := config.LoadTools("fake"); c == nil || len(c.Tools) != 5 {
		t.Fatal("tools should be cached")
	}
	out, _, _ = run(t, "fake", "tool", "echo")
	if !strings.Contains(out, "- `text` string (required)") || !strings.Contains(out, "- `tags` string[]") || !strings.Contains(out, "Call: mcpcli fake echo text=...") {
		t.Fatalf("tool:\n%s", out)
	}
	out, _, _ = run(t, "fake", "tools", "--full")
	if strings.Count(out, "## ") != 5 {
		t.Fatalf("tools --full:\n%s", out)
	}

	// Coercion: ints, bools, arrays from the schema; result is JSON text → rendered as key/values.
	out, _, err = run(t, "fake", "echo", "text=hi there", "loud=true", "tags=a,b", "limit=3")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"text":"hi there"`, `"loud":true`, `"tags":["a","b"]`, `"limit":3`} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	// Structured content.
	out, _, err = run(t, "fake", "add", "a=2", "b=40")
	if err != nil || strings.TrimSpace(out) != "sum: 42" {
		t.Fatalf("structured: %v %q", err, out)
	}
	out, _, _ = run(t, "fake", "add", "a=2", "b=40", "--json")
	var res map[string]any
	if err := json.Unmarshal([]byte(out), &res); err != nil || res["structuredContent"].(map[string]any)["sum"].(float64) != 42 {
		t.Fatalf("json: %v %s", err, out)
	}
	// Envelope rows with --fields and csv.
	out, _, _ = run(t, "fake", "rows")
	if !strings.Contains(out, "total: 2\n") || !strings.Contains(out, "| id | name | x |") || !strings.Contains(out, "| 2 | b | z |") {
		t.Fatalf("rows:\n%s", out)
	}
	out, _, _ = run(t, "fake", "rows", "--fields", "id,name", "--format", "csv")
	if strings.TrimSpace(out) != "id,name\n1,a\n2,b" {
		t.Fatalf("csv: %q", out)
	}
	// --format text gives raw text; --max-chars clips.
	out, _, _ = run(t, "fake", "rows", "--format", "text")
	if !strings.HasPrefix(out, `{"total":2`) {
		t.Fatalf("text: %q", out)
	}
	out, _, _ = run(t, "fake", "long", "--max-chars", "20")
	if !strings.Contains(out, "… [") || len(out) > 80 {
		t.Fatalf("clip: %q", out)
	}
	// Tool errors exit non-zero with the message on stderr.
	_, errs, err := run(t, "fake", "fail")
	if err == nil || !strings.Contains(errs, "tool error: boom") {
		t.Fatalf("fail: %v %q", err, errs)
	}
	if _, _, err := run(t, "fake", "nosuch"); err == nil || !strings.Contains(err.Error(), `no tool "nosuch"`) {
		t.Fatalf("unknown tool: %v", err)
	}
	if _, _, err := run(t, "fake", "echo", "text"); err == nil {
		t.Fatal("bad arg syntax must error")
	}
	// --args base + override.
	out, _, _ = run(t, "fake", "echo", "--args", `{"text":"base","loud":true}`, "text=over")
	if !strings.Contains(out, `"text":"over"`) || !strings.Contains(out, `"loud":true`) {
		t.Fatalf("args merge:\n%s", out)
	}

	out, _, _ = run(t, "fake", "info")
	if !strings.Contains(out, "server: fake\nversion: 1.2.3\n") || !strings.Contains(out, "capabilities: tools, resources, prompts") || !strings.Contains(out, "Use echo for text.") {
		t.Fatalf("info:\n%s", out)
	}
	out, _, _ = run(t, "fake", "resources")
	if !strings.Contains(out, "| mem://notes | notes | text/plain | Some notes. |") {
		t.Fatalf("resources:\n%s", out)
	}
	out, _, _ = run(t, "fake", "read", "mem://notes")
	if strings.TrimSpace(out) != "note body" {
		t.Fatalf("read: %q", out)
	}
	out, _, _ = run(t, "fake", "prompts")
	if !strings.Contains(out, "| greet | name* | Greeting prompt. |") {
		t.Fatalf("prompts:\n%s", out)
	}
	out, _, _ = run(t, "fake", "prompt", "greet", "name=Ann")
	if !strings.Contains(out, "**user:** Hello Ann") {
		t.Fatalf("prompt:\n%s", out)
	}

	out, _, _ = run(t, "fake", "skill")
	for _, want := range []string{"name: mcp-fake", "# fake via mcpcli (profile `fake`)", "## Server instructions\n\nUse echo for text.", "- `mcpcli fake add a=... b=...` — Add two integers.", "### echo\n", "- `text` string (required)"} {
		if !strings.Contains(out, want) {
			t.Fatalf("skill missing %q:\n%s", want, out)
		}
	}
	dest := filepath.Join(t.TempDir(), "s", "SKILL.md")
	run(t, "skill", "fake", "-o", dest)
	if _, err := os.Stat(dest); err != nil {
		t.Fatal(err)
	}
	out, _, _ = run(t, "skill")
	if !strings.HasPrefix(out, "# mcpcli") {
		t.Fatalf("generic skill: %q", out[:40])
	}
}

func TestBearerAuth(t *testing.T) {
	home(t)
	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("Authorization")
		if seen != "Bearer secret-1" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		handler(newMCP()).ServeHTTP(w, r)
	}))
	defer srv.Close()
	run(t, "add", "b", srv.URL, "--bearer-env", "TEST_MCP_TOKEN")
	if _, _, err := run(t, "b", "tools"); err == nil || !strings.Contains(err.Error(), "$TEST_MCP_TOKEN") {
		t.Fatalf("empty env: %v", err)
	}
	t.Setenv("TEST_MCP_TOKEN", "wrong")
	if _, _, err := run(t, "b", "tools"); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("wrong token: %v", err)
	}
	t.Setenv("TEST_MCP_TOKEN", "secret-1")
	if out, _, err := run(t, "b", "add", "a=1", "b=2"); err != nil || !strings.Contains(out, "sum: 3") {
		t.Fatalf("bearer call: %v %q", err, out)
	}
	if _, _, err := run(t, "login", "b"); err == nil {
		t.Fatal("login on a bearer profile must error")
	}
}

func TestOAuthLoginAndRefresh(t *testing.T) {
	h := home(t)
	f := oauthtest.New()
	defer f.Close()
	f.MCP = handler(newMCP())
	run(t, "add", "o", f.URL()+"/mcp")

	if _, _, err := run(t, "o", "tools"); err == nil || !strings.Contains(err.Error(), "mcpcli login o") {
		t.Fatalf("not logged in: %v", err)
	}

	// Browser login: the stub "browser" follows the authorize URL, which
	// redirects to the loopback listener.
	openBrowser = func(u string) error {
		go func() {
			resp, err := http.Get(u)
			if err == nil {
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			}
		}()
		return nil
	}
	out, _, err := run(t, "login", "o")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Logged in o (expires") || !strings.Contains(out, "refresh token saved") || !strings.Contains(out, "5 tools: add, echo, fail, long, rows") {
		t.Fatalf("login:\n%s", out)
	}
	tok, _ := config.LoadToken("o")
	if tok == nil || tok.RefreshToken != "rt-1" {
		t.Fatalf("token not saved: %+v", tok)
	}
	if b, _ := os.ReadFile(filepath.Join(h, "o", "config.yaml")); !strings.Contains(string(b), "client_id: client-") || !strings.Contains(string(b), "resource: "+f.URL()+"/mcp") {
		t.Fatalf("registration not saved:\n%s", b)
	}
	if out, _, err := run(t, "o", "add", "a=1", "b=1"); err != nil || !strings.Contains(out, "sum: 2") {
		t.Fatalf("authed call: %v %q", err, out)
	}
	out, _, _ = run(t, "profiles")
	if !strings.Contains(out, "| o | "+f.URL()+"/mcp | oauth | logged in (token expires in") {
		t.Fatalf("profiles state:\n%s", out)
	}

	// Expire the token: the next call must refresh (with resource) and persist.
	tok.Expiry = time.Now().Add(-time.Hour)
	config.SaveToken("o", tok)
	before := f.RefreshCount
	if out, _, err := run(t, "o", "add", "a=2", "b=2"); err != nil || !strings.Contains(out, "sum: 4") {
		t.Fatalf("call after expiry: %v %q", err, out)
	}
	if f.RefreshCount != before+1 || f.LastRefresh.Get("resource") != f.URL()+"/mcp" {
		t.Fatalf("expected one refresh with resource, got %d %v", f.RefreshCount-before, f.LastRefresh)
	}
	if tok2, _ := config.LoadToken("o"); tok2.RefreshToken != "rt-2" || !tok2.Valid(0) {
		t.Fatalf("refreshed token not persisted: %+v", tok2)
	}

	// Expired with a dead refresh token → clear login hint.
	tok2, _ := config.LoadToken("o")
	tok2.Expiry, tok2.RefreshToken = time.Now().Add(-time.Hour), "rt-bad"
	config.SaveToken("o", tok2)
	if _, _, err := run(t, "o", "tools", "--refresh"); err == nil || !strings.Contains(err.Error(), "mcpcli login o") {
		t.Fatalf("dead refresh: %v", err)
	}

	// Second login reuses the registration (same redirect port is not
	// guaranteed, so allow re-registration) and works again.
	if _, _, err := run(t, "login", "o", "--port", "0"); err != nil {
		t.Fatal(err)
	}
	// Logout removes the token but keeps the client.
	run(t, "logout", "o")
	if tok, _ := config.LoadToken("o"); tok != nil {
		t.Fatal("token should be gone")
	}
	p, _ := config.Load("o")
	if p.OAuth == nil || p.OAuth.ClientID == "" {
		t.Fatal("registration should be kept")
	}
}

func TestOAuthDeviceLogin(t *testing.T) {
	home(t)
	f := oauthtest.New()
	defer f.Close()
	f.MCP = handler(newMCP())
	f.AutoApprove = false
	run(t, "add", "d", f.URL()+"/mcp")
	f.ApproveLater(300 * time.Millisecond)
	out, errs, err := run(t, "login", "d", "--device")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errs, "ABCD-EFGH") || !strings.Contains(out, "Logged in d") {
		t.Fatalf("device login:\n%s\n%s", out, errs)
	}
	if out, _, err := run(t, "d", "echo", "text=x"); err != nil || !strings.Contains(out, `"text":"x"`) {
		t.Fatalf("call after device login: %v %q", err, out)
	}
}

func TestBadFormat(t *testing.T) {
	home(t)
	if _, _, err := run(t, "--format", "xml", "profiles"); err == nil {
		t.Fatal("bad format must error")
	}
}
