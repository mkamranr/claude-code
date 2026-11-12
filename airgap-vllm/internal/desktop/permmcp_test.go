package desktop

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func mcpPost(t *testing.T, p *PermServer, token, body string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, p.URL(), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

// verdict decodes the permission decision Claude Code reads from a tools/call
// result: a JSON object carried as the text of the first content block.
func verdict(t *testing.T, resp map[string]any) map[string]any {
	t.Helper()
	result, _ := resp["result"].(map[string]any)
	content, _ := result["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("no content in %v", resp)
	}
	text, _ := content[0].(map[string]any)["text"].(string)
	var v map[string]any
	if err := json.Unmarshal([]byte(text), &v); err != nil {
		t.Fatalf("decision %q is not JSON: %v", text, err)
	}
	return v
}

const callBody = `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"approve","arguments":{"tool_name":"Write","input":{"file_path":"a.txt","content":"x"},"tool_use_id":"toolu_1"}}}`

func newPerm(t *testing.T, timeout time.Duration) (*PermServer, chan PermRequest) {
	t.Helper()
	reqs := make(chan PermRequest, 10)
	p, err := NewPermServer(func(r PermRequest) { reqs <- r }, timeout)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p, reqs
}

func token(t *testing.T, p *PermServer) string {
	t.Helper()
	var cfg struct {
		MCPServers map[string]struct {
			Type    string            `json:"type"`
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(p.MCPConfig()), &cfg); err != nil {
		t.Fatal(err)
	}
	s := cfg.MCPServers[permServerName]
	if s.Type != "http" || s.URL != p.URL() {
		t.Fatalf("mcp config = %+v", s)
	}
	return strings.TrimPrefix(s.Headers["Authorization"], "Bearer ")
}

func TestPermServerHandshake(t *testing.T) {
	p, _ := newPerm(t, time.Minute)
	tok := token(t, p)

	code, resp := mcpPost(t, p, tok, `{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-11-25"}}`)
	if code != 200 {
		t.Fatalf("initialize status %d", code)
	}
	if v := resp["result"].(map[string]any)["protocolVersion"]; v != "2025-11-25" {
		t.Fatalf("protocolVersion = %v, want the client's", v)
	}
	if code, _ := mcpPost(t, p, tok, `{"jsonrpc":"2.0","method":"notifications/initialized"}`); code != http.StatusAccepted {
		t.Fatalf("notification status %d, want 202", code)
	}
	_, resp = mcpPost(t, p, tok, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	tools := resp["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 1 || tools[0].(map[string]any)["name"] != "approve" {
		t.Fatalf("tools = %v", tools)
	}
}

func TestPermServerRejectsMissingOrWrongToken(t *testing.T) {
	p, _ := newPerm(t, time.Minute)
	for _, tok := range []string{"", "wrong"} {
		if code, _ := mcpPost(t, p, tok, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`); code != http.StatusUnauthorized {
			t.Errorf("token %q: status %d, want 401", tok, code)
		}
	}
}

func TestPermServerGetIsNotAllowed(t *testing.T) {
	p, _ := newPerm(t, time.Minute)
	req, _ := http.NewRequest(http.MethodGet, p.URL(), nil)
	req.Header.Set("Authorization", "Bearer "+token(t, p))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET status %d, want 405", resp.StatusCode)
	}
}

func callAsync(t *testing.T, p *PermServer) chan map[string]any {
	out := make(chan map[string]any, 1)
	tok := token(t, p)
	go func() {
		_, resp := mcpPost(t, p, tok, callBody)
		out <- resp
	}()
	return out
}

func TestPermServerAllowBlocksUntilAnswered(t *testing.T) {
	p, reqs := newPerm(t, time.Minute)
	done := callAsync(t, p)

	r := <-reqs
	if r.ToolName != "Write" || r.ToolUseID != "toolu_1" || !bytes.Contains(r.Input, []byte("a.txt")) {
		t.Fatalf("request = %+v", r)
	}
	select {
	case <-done:
		t.Fatal("tools/call answered before the user did")
	case <-time.After(100 * time.Millisecond):
	}
	if err := p.Respond(r.ID, true, false); err != nil {
		t.Fatal(err)
	}
	v := verdict(t, <-done)
	if v["behavior"] != "allow" {
		t.Fatalf("verdict = %v", v)
	}
	if in, _ := v["updatedInput"].(map[string]any); in["file_path"] != "a.txt" {
		t.Fatalf("updatedInput = %v, want the original input", v["updatedInput"])
	}
	if err := p.Respond(r.ID, true, false); err == nil {
		t.Fatal("answering the same request twice succeeded")
	}
}

func TestPermServerDeny(t *testing.T) {
	p, reqs := newPerm(t, time.Minute)
	done := callAsync(t, p)
	p.Respond((<-reqs).ID, false, false)
	v := verdict(t, <-done)
	if v["behavior"] != "deny" || v["message"] == "" {
		t.Fatalf("verdict = %v", v)
	}
}

func TestPermServerAlwaysAllowSkipsLaterPrompts(t *testing.T) {
	p, reqs := newPerm(t, time.Minute)
	done := callAsync(t, p)
	p.Respond((<-reqs).ID, true, true)
	<-done

	v := verdict(t, <-callAsync(t, p))
	if v["behavior"] != "allow" {
		t.Fatalf("second call verdict = %v", v)
	}
	select {
	case r := <-reqs:
		t.Fatalf("second call prompted the user again: %+v", r)
	default:
	}
}

func TestPermServerTimeoutDenies(t *testing.T) {
	p, reqs := newPerm(t, 150*time.Millisecond)
	done := callAsync(t, p)
	<-reqs
	if v := verdict(t, <-done); v["behavior"] != "deny" {
		t.Fatalf("verdict after timeout = %v", v)
	}
}

func TestPermServerCancelAllDenies(t *testing.T) {
	p, reqs := newPerm(t, time.Minute)
	done := callAsync(t, p)
	<-reqs
	p.CancelAll()
	if v := verdict(t, <-done); v["behavior"] != "deny" {
		t.Fatalf("verdict after CancelAll = %v", v)
	}
}
