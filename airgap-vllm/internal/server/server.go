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
	"time"

	"claude-vllm/internal/config"
	"claude-vllm/internal/filter"
	"claude-vllm/internal/probe"
	"claude-vllm/internal/translate"
)

type Server struct {
	cfg    config.Config
	caps   probe.Capabilities
	mode   string
	client *http.Client
	lg     *Logger
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
// /model picker can offer whatever the server is actually serving.
func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	caps, err := probe.Detect(r.Context(), s.client, s.base(), s.cfg.APIKey)
	if err != nil {
		s.fail(w, http.StatusBadGateway, "upstream_unavailable", err.Error())
		return
	}
	data := make([]map[string]any, 0, len(caps.Models))
	for _, id := range caps.Models {
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

	// Honor a model the server actually serves; otherwise pin to the configured
	// one so a stale picker selection cannot 404 the session.
	if m, ok := cleaned["model"].(string); !ok || !s.caps.HasModel(m) {
		cleaned["model"] = s.cfg.Model
	}

	streaming, _ := cleaned["stream"].(bool)
	if streaming {
		s.streamMessages(w, r, cleaned)
		return
	}
	s.blockingMessages(w, r, cleaned)
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

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
