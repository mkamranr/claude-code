package server

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"claude-vllm/internal/filter"
	"claude-vllm/internal/translate"
)

// pingInterval bounds how long the stream can be silent. Claude Code's idle
// timeout has fired on custom gateways during long prefills.
const pingInterval = 15 * time.Second

// sseWriter serializes writes because the keepalive goroutine and the
// translation loop both emit events.
type sseWriter struct {
	mu sync.Mutex
	w  http.ResponseWriter
	f  http.Flusher
}

func newSSEWriter(w http.ResponseWriter) *sseWriter {
	f, _ := w.(http.Flusher)
	return &sseWriter{w: w, f: f}
}

func (s *sseWriter) Event(name string, data any) error {
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", name, b); err != nil {
		return err
	}
	if s.f != nil {
		s.f.Flush()
	}
	return nil
}

// ScanSSE reads an SSE body, invoking fn for each event. The terminal [DONE]
// sentinel is consumed rather than forwarded.
func ScanSSE(r io.Reader, fn func(event, data string) error) error {
	sc := bufio.NewScanner(r)
	// Tool call arguments can exceed the 64KB default line budget.
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	var event string
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		switch {
		case line == "":
			event = ""
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if data == "[DONE]" {
				return nil
			}
			if err := fn(event, data); err != nil {
				return err
			}
		}
	}
	return sc.Err()
}

func (s *Server) streamMessages(w http.ResponseWriter, r *http.Request, req map[string]any) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	out := newSSEWriter(w)
	stop := s.startKeepalive(r, out)
	defer stop()

	if s.mode == "messages" {
		s.streamPassthrough(w, r, req, out)
		return
	}
	s.streamTranslated(w, r, req, out)
}

// startKeepalive emits ping events until the returned function is called.
func (s *Server) startKeepalive(r *http.Request, out *sseWriter) func() {
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(pingInterval)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-r.Context().Done():
				return
			case <-t.C:
				out.Event("ping", map[string]any{"type": "ping"})
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(done) }) }
}

func (s *Server) streamTranslated(w http.ResponseWriter, r *http.Request, req map[string]any, out *sseWriter) {
	oa, err := translate.ToOpenAI(req, s.cfg.Model)
	if err != nil {
		s.streamFail(out, "invalid_request_error", err.Error())
		return
	}
	oa["stream"] = true
	body, _ := json.Marshal(oa)
	s.lg.Body("bridge->vllm(stream)", body)

	resp, err := s.upstream(r, http.MethodPost, s.base()+"/v1/chat/completions", body)
	if err != nil {
		s.streamFail(out, "upstream_unavailable", err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		s.lg.Body("vllm->bridge(error)", b)
		s.streamFail(out, "upstream_error",
			fmt.Sprintf("vLLM returned HTTP %d: %s", resp.StatusCode, truncate(string(b), 800)))
		return
	}

	tr := translate.NewStreamTranslator(out, GenID("msg_"), s.cfg.Model,
		func() string { return GenID("toolu_") })
	if err := tr.Start(); err != nil {
		return
	}
	err = ScanSSE(resp.Body, func(_, data string) error {
		var c translate.Chunk
		if err := json.Unmarshal([]byte(data), &c); err != nil {
			s.lg.Note("skipping unparseable chunk: %v", err)
			return nil
		}
		return tr.Chunk(c)
	})
	if err != nil {
		s.lg.Note("stream read ended: %v", err)
	}
	tr.Finish()
}

// streamPassthrough forwards the upstream Anthropic stream, repairing tool_use
// blocks that arrive without an id.
func (s *Server) streamPassthrough(w http.ResponseWriter, r *http.Request, req map[string]any, out *sseWriter) {
	body, _ := json.Marshal(req)
	resp, err := s.upstream(r, http.MethodPost, s.base()+"/v1/messages", body)
	if err != nil {
		s.streamFail(out, "upstream_unavailable", err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		s.streamFail(out, "upstream_error",
			fmt.Sprintf("vLLM returned HTTP %d: %s", resp.StatusCode, truncate(string(b), 800)))
		return
	}

	ScanSSE(resp.Body, func(event, data string) error {
		var decoded map[string]any
		if json.Unmarshal([]byte(data), &decoded) != nil {
			return out.Event(event, json.RawMessage(data))
		}
		if event == "content_block_start" {
			if cb, ok := decoded["content_block"].(map[string]any); ok {
				fixed := filter.EnsureToolUseIDs([]any{cb}, func() string { return GenID("toolu_") })
				decoded["content_block"] = fixed[0]
			}
		}
		name := event
		if name == "" {
			name, _ = decoded["type"].(string)
		}
		return out.Event(name, decoded)
	})
}

// streamFail reports an error inside the stream, since headers are already sent.
func (s *Server) streamFail(out *sseWriter, kind, msg string) {
	s.lg.Note("stream error %s: %s", kind, msg)
	out.Event("error", map[string]any{
		"type":  "error",
		"error": map[string]any{"type": kind, "message": msg},
	})
}
