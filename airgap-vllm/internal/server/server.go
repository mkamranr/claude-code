// Package server is the HTTP bridge between Claude Code and vLLM. It owns all
// I/O; the conversion logic it calls is pure.
package server

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"claude-vllm/internal/config"
	"claude-vllm/internal/filter"
	"claude-vllm/internal/probe"
	"claude-vllm/internal/translate"
)

// Server holds capabilities detected once at startup. mode and caps.MessagesAPI
// / caps.Tokenize are never mutated after New: switching translation lanes
// underneath an open conversation would be worse than an occasionally-stale
// flag. caps.Models is the one field that legitimately drifts — a model
// picker query or a downloaded model can add or remove entries at runtime —
// so it alone is refreshed, guarded by capsMu since handlers run concurrently.
type Server struct {
	cfg    config.Config
	mode   string
	client *http.Client
	lg     *Logger

	capsMu sync.RWMutex
	caps   probe.Capabilities
}

func New(cfg config.Config, caps probe.Capabilities, lg *Logger) *Server {
	return &Server{
		cfg:  cfg,
		caps: caps,
		mode: probe.ResolveMode(cfg.Mode, caps),
		// No overall timeout: a long generation is normal. Idle handling is
		// the stream's job, not the client's.
		client: &http.Client{},
		lg:     lg,
	}
}

// hasModel reports whether id is in the current cached model list.
func (s *Server) hasModel(id string) bool {
	s.capsMu.RLock()
	defer s.capsMu.RUnlock()
	return s.caps.HasModel(id)
}

// setModels replaces the cached model list. It never touches MessagesAPI,
// Tokenize, or mode — those are fixed for the life of the Server.
func (s *Server) setModels(models []string) {
	s.capsMu.Lock()
	s.caps.Models = models
	s.capsMu.Unlock()
}

func (s *Server) Mode() string { return s.mode }

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/v1/models", s.handleModels)
	mux.HandleFunc("/v1/messages/count_tokens", s.handleCountTokens)
	mux.HandleFunc("/v1/messages", s.handleMessages)
	return mux
}

// GenID produces an identifier for a tool call or message that arrived without
// one. A tool_use block with no id crashes the Claude Code renderer.
func GenID(prefix string) string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return prefix + fmt.Sprint(time.Now().UnixNano())
	}
	return prefix + hex.EncodeToString(b)
}

func (s *Server) base() string { return strings.TrimRight(s.cfg.VLLMURL, "/") }

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok", "mode": s.mode, "model": s.cfg.Model, "upstream": s.base(),
	})
}

// handleModels reshapes vLLM's model list into the Anthropic list format so the
// /model picker can offer whatever the server is actually serving. It calls
// the cheap FetchModels rather than the full Detect — a poll of this endpoint
// has no business paying for an inference-shaped /v1/messages probe — and
// updates the cached list other handlers consult, so a model the picker just
// offered is not silently rejected a moment later. It never touches
// MessagesAPI, Tokenize, or mode; see the Server doc comment.
func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	models, err := probe.FetchModels(r.Context(), s.client, s.base(), s.cfg.APIKey)
	if err != nil {
		s.fail(w, http.StatusBadGateway, "upstream_unavailable", err.Error())
		return
	}
	s.setModels(models)
	data := make([]map[string]any, 0, len(models))
	for _, id := range models {
		data = append(data, map[string]any{
			"id": id, "type": "model", "display_name": id,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data, "has_more": false})
}

func (s *Server) handleCountTokens(w http.ResponseWriter, r *http.Request) {
	req, err := readJSON(r)
	if err != nil {
		s.fail(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	text := translate.FlattenRequestText(req)

	if s.caps.Tokenize {
		if n, err := s.tokenize(r, text); err == nil {
			writeJSON(w, http.StatusOK, map[string]any{"input_tokens": n})
			return
		}
		s.lg.Note("tokenize failed, falling back to estimate")
	}
	// Roughly four characters per token. Only used to keep the UI's context
	// meter approximately honest when the server exposes no tokenizer.
	est := len(text) / 4
	if est < 1 {
		est = 1
	}
	writeJSON(w, http.StatusOK, map[string]any{"input_tokens": est})
}

func (s *Server) tokenize(r *http.Request, text string) (int, error) {
	body, _ := json.Marshal(map[string]any{"model": s.cfg.Model, "prompt": text})
	resp, err := s.upstream(r, http.MethodPost, s.base()+"/tokenize", body)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	var out struct {
		Count int `json:"count"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0, err
	}
	return out.Count, nil
}

func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		s.fail(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	s.lg.Body("claude->bridge", raw)

	var req map[string]any
	if err := json.Unmarshal(raw, &req); err != nil {
		s.fail(w, http.StatusBadRequest, "invalid_request_error", "request body is not JSON: "+err.Error())
		return
	}

	cleaned, notes := filter.Request(req)
	for _, n := range notes {
		s.lg.Note("filter: %s", n)
	}

	if !s.pinModel(r, cleaned) {
		s.fail(w, http.StatusBadRequest, "invalid_request_error", s.unknownModelMessage(cleaned))
		return
	}

	streaming, _ := cleaned["stream"].(bool)
	if streaming {
		s.streamMessages(w, r, cleaned)
		return
	}
	s.blockingMessages(w, r, cleaned)
}

// pinModel makes cleaned["model"] name something the server actually serves.
// Getting an answer from a model nobody chose, with no trace that it
// happened, is worse than a clean failure — so the only silent path is the
// one where the request specified no model at all.
//
// Returns false when the requested model is confirmed absent and the caller
// must reject the request (unknownModelMessage explains why); the cached
// list at that point holds the names to report.
func (s *Server) pinModel(r *http.Request, cleaned map[string]any) bool {
	requested, _ := cleaned["model"].(string)
	if requested == "" {
		cleaned["model"] = s.cfg.Model
		return true
	}
	if s.hasModel(requested) {
		return true
	}

	// Not in the cached list. The cache may simply be stale — handleModels
	// refreshes independently of this request path — so refresh once before
	// deciding anything.
	refreshed, err := probe.FetchModels(r.Context(), s.client, s.base(), s.cfg.APIKey)
	if err == nil {
		s.setModels(refreshed)
		if s.hasModel(requested) {
			return true // present after refresh
		}
		if len(refreshed) > 0 {
			return false // confirmed absent from a non-empty, live list
		}
	}

	// The refresh failed or came back empty: there is no reliable list to
	// judge "absent" against. Fall back rather than start erroring every
	// request on a deployment that previously worked, but never silently —
	// an operator debugging a wrong answer needs this line.
	s.lg.Note("model %q not found in vLLM's model list (list unavailable or empty); substituting configured model %q", requested, s.cfg.Model)
	cleaned["model"] = s.cfg.Model
	return true
}

// unknownModelMessage names the rejected model and what the server actually
// serves, so the operator can fix it without searching the internet.
func (s *Server) unknownModelMessage(cleaned map[string]any) string {
	requested, _ := cleaned["model"].(string)
	s.capsMu.RLock()
	available := strings.Join(s.caps.Models, ", ")
	s.capsMu.RUnlock()
	return fmt.Sprintf("model %q is not served by this vLLM. Available: %s. Change it with: claude-vllm config set model=NAME",
		requested, available)
}

func (s *Server) blockingMessages(w http.ResponseWriter, r *http.Request, req map[string]any) {
	if s.mode == "messages" {
		body, _ := json.Marshal(req)
		resp, err := s.upstream(r, http.MethodPost, s.base()+"/v1/messages", body)
		if err != nil {
			s.fail(w, http.StatusBadGateway, "upstream_unavailable", err.Error())
			return
		}
		defer resp.Body.Close()
		out, err := io.ReadAll(resp.Body)
		if err != nil {
			s.fail(w, http.StatusBadGateway, "upstream_unavailable", err.Error())
			return
		}
		s.lg.Body("vllm->bridge", out)

		// A non-2xx response is forwarded as-is only when it is already
		// Anthropic-shaped. A native /v1/messages build is not guaranteed to
		// error the way Anthropic does, and Claude Code cannot render
		// arbitrary upstream text as a diagnostic.
		if resp.StatusCode >= 400 && !isAnthropicShapedError(out) {
			s.fail(w, resp.StatusCode, "upstream_error",
				fmt.Sprintf("vLLM returned HTTP %d: %s", resp.StatusCode, truncate(string(out), 800)))
			return
		}

		// Even in passthrough, guarantee tool_use ids.
		var decoded map[string]any
		if json.Unmarshal(out, &decoded) == nil {
			if content, ok := decoded["content"].([]any); ok {
				decoded["content"] = filter.EnsureToolUseIDs(content, func() string { return GenID("toolu_") })
				writeJSON(w, resp.StatusCode, decoded)
				return
			}
		}
		w.WriteHeader(resp.StatusCode)
		w.Write(out)
		return
	}

	oa, err := translate.ToOpenAI(req, s.cfg.Model)
	if err != nil {
		s.fail(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	body, _ := json.Marshal(oa)
	s.lg.Body("bridge->vllm", body)

	resp, err := s.upstream(r, http.MethodPost, s.base()+"/v1/chat/completions", body)
	if err != nil {
		s.fail(w, http.StatusBadGateway, "upstream_unavailable", err.Error())
		return
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		s.fail(w, http.StatusBadGateway, "upstream_unavailable", err.Error())
		return
	}
	s.lg.Body("vllm->bridge", out)

	if resp.StatusCode >= 400 {
		s.fail(w, resp.StatusCode, "upstream_error",
			fmt.Sprintf("vLLM returned HTTP %d: %s", resp.StatusCode, truncate(string(out), 800)))
		return
	}
	var parsed translate.OpenAIResponse
	if err := json.Unmarshal(out, &parsed); err != nil {
		s.fail(w, http.StatusBadGateway, "upstream_error", "unparseable response from vLLM: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, translate.FromOpenAI(parsed, s.cfg.Model, func() string { return GenID("toolu_") }))
}

// streamMessages handles streaming Messages requests. Replaced entirely in
// Task 9; this stub exists only so the package compiles.
func (s *Server) streamMessages(w http.ResponseWriter, r *http.Request, req map[string]any) {
	s.fail(w, http.StatusNotImplemented, "not_implemented", "streaming lands in Task 9")
}

func (s *Server) upstream(r *http.Request, method, url string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(r.Context(), method, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if s.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+s.cfg.APIKey)
	}
	return s.client.Do(req)
}

func readJSON(r *http.Request) (map[string]any, error) {
	b, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("request body is not JSON: %w", err)
	}
	return m, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// fail emits an Anthropic-shaped error so Claude Code renders it rather than
// showing a bare transport failure.
func (s *Server) fail(w http.ResponseWriter, status int, kind, msg string) {
	s.lg.Note("error %d %s: %s", status, kind, msg)
	writeJSON(w, status, map[string]any{
		"type":  "error",
		"error": map[string]any{"type": kind, "message": msg},
	})
}

// isAnthropicShapedError reports whether b already parses as an Anthropic
// error envelope ({"type":"error", ...}), meaning it is safe to forward to
// Claude Code unchanged.
func isAnthropicShapedError(b []byte) bool {
	var v struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return false
	}
	return v.Type == "error"
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
