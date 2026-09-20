package server

import (
	"bufio"
	"encoding/json"
	"errors"
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
	// vLLM's OpenAI server attaches a usage block to streamed chunks only when
	// the request asks for one. Without this the translator never learns a
	// token count, message_start reports input_tokens: 0 for the whole session,
	// and Claude Code's context meter stays empty right up until the
	// conversation dies of a hard context-length error.
	oa["stream_options"] = map[string]any{"include_usage": true}
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
		// Almost always means the client already hung up, so a client-facing
		// event would land nowhere — but the failure still needs a trace.
		s.lg.Note("stream start failed: %v", err)
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
		// The client otherwise sees a stream that just ends, indistinguishable
		// from a short but complete answer. Report the truncation before
		// finishing so the error event lands ahead of the clean termination.
		s.reportStreamReadError(r, out, err)
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

	err = ScanSSE(resp.Body, func(event, data string) error {
		var decoded map[string]any
		if json.Unmarshal([]byte(data), &decoded) != nil {
			return out.Event(event, json.RawMessage(data))
		}
		// The effective event name falls back to the payload's own "type"
		// field when the frame carries no `event:` line — some upstreams
		// (and fakevllm) send bare `data:` frames. The repair check below
		// must use this same effective name, not the raw pre-fallback
		// `event`, or a bare-data content_block_start never gets repaired.
		name := event
		if name == "" {
			name, _ = decoded["type"].(string)
		}
		if name == "content_block_start" {
			if cb, ok := decoded["content_block"].(map[string]any); ok {
				fixed := filter.EnsureToolUseIDs([]any{cb}, func() string { return GenID("toolu_") })
				decoded["content_block"] = fixed[0]
			}
		}
		return out.Event(name, decoded)
	})
	if err != nil {
		// Passthrough has no translator to close out, but the client must
		// still learn the answer was cut short rather than seeing a stream
		// that just quietly ends.
		s.reportStreamReadError(r, out, err)
	}
}

// streamFail reports an error inside the stream, since headers are already sent.
func (s *Server) streamFail(out *sseWriter, kind, msg string) {
	s.lg.Note("stream error %s: %s", kind, msg)
	out.Event("error", map[string]any{
		"type":  "error",
		"error": map[string]any{"type": kind, "message": msg},
	})
}

// reportStreamReadError decides what, if anything, the client is told about a
// stream that stopped early.
//
// Pressing Esc in Claude Code aborts the in-flight request. That cancels
// r.Context(), which both kills the upstream request and breaks the writer the
// callbacks write into, so the single most common thing an operator does
// arrives here looking exactly like a dropped network connection. Reporting it
// as one sends someone with no internet to debug a network that is fine, and
// there is nobody left listening for an SSE error event anyway — the client
// that would have read it is the client that just hung up.
func (s *Server) reportStreamReadError(r *http.Request, out *sseWriter, err error) {
	if r.Context().Err() != nil {
		s.lg.Note("stream ended early: the client cancelled the request (%v)", r.Context().Err())
		return
	}
	kind, msg := classifyStreamReadError(err)
	s.streamFail(out, kind, msg)
}

// classifyStreamReadError distinguishes an oversized SSE frame — which needs
// a larger buffer or a fix upstream — from a dropped connection, since an
// operator on an air-gapped machine has no way to attach a debugger and needs
// the message alone to point at the right remedy.
func classifyStreamReadError(err error) (kind, msg string) {
	if errors.Is(err, bufio.ErrTooLong) {
		return "upstream_error", fmt.Sprintf(
			"stream response was truncated: an SSE line from vLLM exceeded the %d-byte read buffer",
			4*1024*1024)
	}
	return "upstream_unavailable", fmt.Sprintf(
		"stream response was truncated: the connection to vLLM was interrupted before the response finished: %v", err)
}
