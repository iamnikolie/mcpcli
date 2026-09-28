// Package quick sends a single JSON-RPC request over MCP Streamable HTTP
// against an existing session, skipping the initialize handshake. It is
// the fast path for repeated CLI invocations: the session id from the
// first handshake is reused until the server says it is gone.
package quick

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Session is what survives between invocations.
type Session struct {
	// ID is the Mcp-Session-Id; empty for stateless servers.
	ID string `json:"id,omitempty"`
	// Protocol is the negotiated MCP protocol version.
	Protocol string `json:"protocol"`
	// Direct records whether requests without a session id are accepted
	// (stateless servers). False after such a request was rejected.
	Direct  bool      `json:"direct"`
	SavedAt time.Time `json:"saved_at"`
}

// Usable reports whether the fast path may be tried.
func (s *Session) Usable() bool {
	return s != nil && s.Protocol != "" && (s.ID != "" || s.Direct)
}

// ErrSessionGone means the server no longer knows the session (or rejects
// unsessioned requests); the caller should run a full handshake.
var ErrSessionGone = errors.New("mcp session is gone")

// RPCError is a JSON-RPC error returned by the server for the request.
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string { return fmt.Sprintf("%s (code %d)", e.Message, e.Code) }

// Call posts one request and returns its result.
func Call(ctx context.Context, hc *http.Client, endpoint string, s *Session, method string, params any) (json.RawMessage, error) {
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Mcp-Protocol-Version", s.Protocol)
	if s.ID != "" {
		req.Header.Set("Mcp-Session-Id", s.ID)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusAccepted || resp.StatusCode == http.StatusNoContent:
		return nil, nil
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusGone:
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("%w: HTTP %d %s", ErrSessionGone, resp.StatusCode, strings.TrimSpace(string(b)))
	case resp.StatusCode != http.StatusOK:
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("%s: HTTP %d %s", method, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	ct := resp.Header.Get("Content-Type")
	var msg rpcResponse
	var found bool
	if strings.HasPrefix(ct, "text/event-stream") {
		msg, found, err = readSSE(resp.Body)
	} else {
		msg, found, err = readJSON(resp.Body)
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", method, err)
	}
	if !found {
		return nil, fmt.Errorf("%s: stream ended without a response", method)
	}
	if msg.Error != nil {
		if sessionError(msg.Error) {
			return nil, fmt.Errorf("%w: %w", ErrSessionGone, msg.Error)
		}
		return nil, msg.Error
	}
	return msg.Result, nil
}

type rpcResponse struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *RPCError       `json:"error"`
	Method string          `json:"method"`
}

func isOurs(m rpcResponse) bool {
	return m.Method == "" && strings.TrimSpace(string(m.ID)) == "1"
}

func readJSON(r io.Reader) (rpcResponse, bool, error) {
	b, err := io.ReadAll(io.LimitReader(r, 64<<20))
	if err != nil {
		return rpcResponse{}, false, err
	}
	b = bytes.TrimSpace(b)
	if len(b) == 0 {
		return rpcResponse{}, false, nil
	}
	if b[0] == '[' {
		var batch []rpcResponse
		if err := json.Unmarshal(b, &batch); err != nil {
			return rpcResponse{}, false, err
		}
		for _, m := range batch {
			if isOurs(m) {
				return m, true, nil
			}
		}
		return rpcResponse{}, false, nil
	}
	var m rpcResponse
	if err := json.Unmarshal(b, &m); err != nil {
		return rpcResponse{}, false, err
	}
	return m, isOurs(m), nil
}

// readSSE scans server-sent events until the response to our request
// arrives; notifications and progress events are skipped.
func readSSE(r io.Reader) (rpcResponse, bool, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	var data []string
	flush := func() (rpcResponse, bool) {
		if len(data) == 0 {
			return rpcResponse{}, false
		}
		payload := strings.Join(data, "\n")
		data = nil
		var m rpcResponse
		if err := json.Unmarshal([]byte(payload), &m); err != nil {
			return rpcResponse{}, false
		}
		return m, isOurs(m)
	}
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if m, ok := flush(); ok {
				return m, true, nil
			}
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if err := sc.Err(); err != nil {
		return rpcResponse{}, false, err
	}
	if m, ok := flush(); ok {
		return m, true, nil
	}
	return rpcResponse{}, false, nil
}

// sessionError recognises servers that report a missing/expired session as
// a JSON-RPC error instead of an HTTP status.
func sessionError(e *RPCError) bool {
	msg := strings.ToLower(e.Message)
	if strings.Contains(msg, "session") || strings.Contains(msg, "initializ") {
		return true
	}
	return false
}
