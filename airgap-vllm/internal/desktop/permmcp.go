// Package desktop is the backend of claude-vllm-desktop: it runs claude.exe in
// headless stream-json mode against the loopback bridge and relays its event
// stream and permission prompts to a UI. It has no dependency on the window
// toolkit; cmd/claude-vllm-desktop adapts it to Wails.
package desktop

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

// PermToolName is the name Claude Code is told to call for permission prompts
// (--permission-prompt-tool): MCP tools are addressed as mcp__<server>__<tool>.
const (
	permServerName = "desktop"
	permToolName   = "approve"
	PermToolName   = "mcp__" + permServerName + "__" + permToolName
)

// PermRequest is one permission prompt, as shown to the person at the UI.
type PermRequest struct {
	ID        string          `json:"id"`
	ToolName  string          `json:"tool_name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
}

type decision struct {
	allow   bool
	always  bool // remember the allow for this tool for the rest of the session
	message string
}

// PermServer is a minimal MCP server (Streamable HTTP transport, plain JSON
// responses) exposing a single tool that Claude Code calls before running any
// tool its permission mode does not already allow. Each call blocks until the
// UI answers through Respond, the request times out, or claude hangs up.
//
// It listens on 127.0.0.1 only and requires a per-session bearer token, so
// another local process cannot approve tool calls on the person's behalf.
type PermServer struct {
	token     string
	ln        net.Listener
	srv       *http.Server
	onRequest func(PermRequest)
	timeout   time.Duration

	mu          sync.Mutex
	pending     map[string]chan decision
	alwaysAllow map[string]bool
	seq         int
}

// NewPermServer starts the server. onRequest is called (from the HTTP
// goroutine) for every prompt that needs a human answer.
func NewPermServer(onRequest func(PermRequest), timeout time.Duration) (*PermServer, error) {
	tok := make([]byte, 24)
	if _, err := rand.Read(tok); err != nil {
		return nil, fmt.Errorf("generating permission token: %w", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("binding the permission server: %w", err)
	}
	p := &PermServer{
		token:       hex.EncodeToString(tok),
		ln:          ln,
		onRequest:   onRequest,
		timeout:     timeout,
		pending:     map[string]chan decision{},
		alwaysAllow: map[string]bool{},
	}
	p.srv = &http.Server{Handler: http.HandlerFunc(p.handle)}
	go p.srv.Serve(ln)
	return p, nil
}

// URL is the MCP endpoint.
func (p *PermServer) URL() string { return "http://" + p.ln.Addr().String() + "/mcp" }

// MCPConfig is the --mcp-config JSON that points Claude Code at this server.
func (p *PermServer) MCPConfig() string {
	cfg := map[string]any{"mcpServers": map[string]any{
		permServerName: map[string]any{
			"type":    "http",
			"url":     p.URL(),
			"headers": map[string]string{"Authorization": "Bearer " + p.token},
		},
	}}
	b, _ := json.Marshal(cfg)
	return string(b)
}

// Respond answers a pending prompt. always remembers an allow for this tool
// for the rest of the session.
func (p *PermServer) Respond(id string, allow, always bool) error {
	p.mu.Lock()
	ch, ok := p.pending[id]
	if ok {
		delete(p.pending, id)
	}
	p.mu.Unlock()
	if !ok {
		return fmt.Errorf("permission request %s is no longer pending", id)
	}
	ch <- decision{
		allow:   allow,
		always:  allow && always,
		message: "The user denied this tool call in Claude vLLM Desktop.",
	}
	return nil
}

// CancelAll denies every pending prompt, e.g. when the person stops a turn.
func (p *PermServer) CancelAll() {
	p.mu.Lock()
	pending := p.pending
	p.pending = map[string]chan decision{}
	p.mu.Unlock()
	for _, ch := range pending {
		ch <- decision{allow: false, message: "The user stopped the turn."}
	}
}

// Close stops the server and denies anything still waiting.
func (p *PermServer) Close() {
	p.CancelAll()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	p.srv.Shutdown(ctx)
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

func (p *PermServer) handle(w http.ResponseWriter, r *http.Request) {
	got := []byte(r.Header.Get("Authorization"))
	want := []byte("Bearer " + p.token)
	if subtle.ConstantTimeCompare(got, want) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.URL.Path != "/mcp" {
		http.NotFound(w, r)
		return
	}
	// The client may open a GET stream for server-initiated messages; this
	// server never sends any, which the transport allows signalling with 405.
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		http.Error(w, "reading body", http.StatusBadRequest)
		return
	}
	var req rpcRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeRPC(w, nil, nil, &rpcError{Code: -32700, Message: "parse error"})
		return
	}
	// Notifications carry no id and get no response body.
	if len(req.ID) == 0 || string(req.ID) == "null" {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	switch req.Method {
	case "initialize":
		var params struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		json.Unmarshal(req.Params, &params)
		version := params.ProtocolVersion
		if version == "" {
			version = "2025-06-18"
		}
		writeRPC(w, req.ID, map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "claude-vllm-desktop", "version": "1"},
		}, nil)
	case "ping":
		writeRPC(w, req.ID, map[string]any{}, nil)
	case "tools/list":
		writeRPC(w, req.ID, map[string]any{"tools": []any{map[string]any{
			"name":        permToolName,
			"description": "Ask the person at Claude vLLM Desktop whether a tool call may run.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"tool_name":   map[string]any{"type": "string"},
					"input":       map[string]any{"type": "object"},
					"tool_use_id": map[string]any{"type": "string"},
				},
				"required": []string{"tool_name", "input"},
			},
		}}}, nil)
	case "tools/call":
		p.handleCall(w, r, req)
	default:
		writeRPC(w, req.ID, nil, &rpcError{Code: -32601, Message: "method not found: " + req.Method})
	}
}

func (p *PermServer) handleCall(w http.ResponseWriter, r *http.Request, req rpcRequest) {
	var params struct {
		Name      string `json:"name"`
		Arguments struct {
			ToolName  string          `json:"tool_name"`
			Input     json.RawMessage `json:"input"`
			ToolUseID string          `json:"tool_use_id"`
		} `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil || params.Name != permToolName {
		writeRPC(w, req.ID, nil, &rpcError{Code: -32602, Message: "unknown tool"})
		return
	}
	args := params.Arguments
	if len(args.Input) == 0 {
		args.Input = json.RawMessage("{}")
	}

	d := p.awaitDecision(r.Context(), args.ToolName, args.Input, args.ToolUseID)

	var verdict map[string]any
	if d.allow {
		verdict = map[string]any{"behavior": "allow", "updatedInput": args.Input}
	} else {
		verdict = map[string]any{"behavior": "deny", "message": d.message}
	}
	text, _ := json.Marshal(verdict)
	writeRPC(w, req.ID, map[string]any{
		"content": []any{map[string]any{"type": "text", "text": string(text)}},
	}, nil)
}

func (p *PermServer) awaitDecision(ctx context.Context, tool string, input json.RawMessage, toolUseID string) decision {
	p.mu.Lock()
	if p.alwaysAllow[tool] {
		p.mu.Unlock()
		return decision{allow: true}
	}
	p.seq++
	id := fmt.Sprintf("perm-%d", p.seq)
	ch := make(chan decision, 1)
	p.pending[id] = ch
	p.mu.Unlock()

	if p.onRequest != nil {
		p.onRequest(PermRequest{ID: id, ToolName: tool, Input: input, ToolUseID: toolUseID})
	}

	timer := time.NewTimer(p.timeout)
	defer timer.Stop()
	select {
	case d := <-ch:
		if d.always {
			p.mu.Lock()
			p.alwaysAllow[tool] = true
			p.mu.Unlock()
		}
		return d
	case <-timer.C:
		p.drop(id)
		return decision{allow: false, message: fmt.Sprintf("No answer from the user within %s; the tool call was denied.", p.timeout)}
	case <-ctx.Done():
		p.drop(id)
		return decision{allow: false, message: "The permission request was abandoned."}
	}
}

func (p *PermServer) drop(id string) {
	p.mu.Lock()
	delete(p.pending, id)
	p.mu.Unlock()
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func writeRPC(w http.ResponseWriter, id json.RawMessage, result any, rpcErr *rpcError) {
	resp := map[string]any{"jsonrpc": "2.0", "id": id}
	if rpcErr != nil {
		resp["error"] = rpcErr
	} else {
		resp["result"] = result
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
