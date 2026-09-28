package quick

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func serve(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	return s
}

func TestCallJSONAndHeaders(t *testing.T) {
	var got http.Header
	var body map[string]any
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`))
	})
	sess := &Session{ID: "sid-1", Protocol: "2025-11-25"}
	res, err := Call(context.Background(), http.DefaultClient, s.URL, sess, "tools/call", map[string]any{"name": "x"})
	if err != nil || string(res) != `{"ok":true}` {
		t.Fatalf("%v %s", err, res)
	}
	if got.Get("Mcp-Session-Id") != "sid-1" || got.Get("Mcp-Protocol-Version") != "2025-11-25" || !strings.Contains(got.Get("Accept"), "text/event-stream") {
		t.Fatalf("headers: %v", got)
	}
	if body["method"] != "tools/call" || body["params"].(map[string]any)["name"] != "x" {
		t.Fatalf("body: %v", body)
	}
}

func TestCallSSE(t *testing.T) {
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{}}\n\n" +
			"event: message\ndata: {\"jsonrpc\":\"2.0\",\n" +
			"data: \"id\":1,\"result\":{\"content\":[]}}\n\n"))
	})
	res, err := Call(context.Background(), http.DefaultClient, s.URL, &Session{Protocol: "2025-11-25", Direct: true}, "tools/call", nil)
	if err != nil || string(res) != `{"content":[]}` {
		t.Fatalf("%v %s", err, res)
	}
}

func TestCallErrors(t *testing.T) {
	cases := []struct {
		name   string
		status int
		ct     string
		body   string
		gone   bool
		rpc    bool
	}{
		{"404 session", 404, "text/plain", "session not found", true, false},
		{"400 bad", 400, "text/plain", "Bad Request: Server not initialized", true, false},
		{"rpc session error", 200, "application/json", `{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"Session not initialized"}}`, true, true},
		{"rpc tool error", 200, "application/json", `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"unknown tool"}}`, false, true},
		{"500", 500, "text/plain", "boom", false, false},
		{"no response", 200, "application/json", `{"jsonrpc":"2.0","method":"notifications/x"}`, false, false},
	}
	for _, c := range cases {
		s := serve(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", c.ct)
			w.WriteHeader(c.status)
			w.Write([]byte(c.body))
		})
		_, err := Call(context.Background(), http.DefaultClient, s.URL, &Session{Protocol: "x", Direct: true}, "tools/call", nil)
		if err == nil {
			t.Fatalf("%s: want error", c.name)
		}
		if errors.Is(err, ErrSessionGone) != c.gone {
			t.Fatalf("%s: gone=%v err=%v", c.name, c.gone, err)
		}
		var rpcErr *RPCError
		if errors.As(err, &rpcErr) != c.rpc {
			t.Fatalf("%s: rpc=%v err=%v", c.name, c.rpc, err)
		}
	}
	s := serve(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(202) })
	if res, err := Call(context.Background(), http.DefaultClient, s.URL, &Session{Protocol: "x", Direct: true}, "notifications/x", nil); err != nil || res != nil {
		t.Fatalf("202: %v %s", err, res)
	}
	if (&Session{}).Usable() || !(&Session{ID: "a", Protocol: "p"}).Usable() || !(&Session{Direct: true, Protocol: "p"}).Usable() {
		t.Fatal("Usable")
	}
}
