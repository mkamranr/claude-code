package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"strings"
	"sync"
	"testing"

	"claude-vllm/internal/config"
	"claude-vllm/internal/probe"
)

func TestStreamingProducesAnthropicEvents(t *testing.T) {
	fake := fakevllmStreaming(t,
		`{"choices":[{"delta":{"content":"Hel"}}]}`,
		`{"choices":[{"delta":{"content":"lo"}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`,
	)
	defer fake.Close()
	h := newTestServer(t, fake, "openai")

	rr := post(t, h, "/v1/messages", `{"model":"m","stream":true,
		"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)

	body := rr.Body.String()
	for _, want := range []string{
		"event: message_start", "event: content_block_start",
		"event: content_block_delta", "event: content_block_stop",
		"event: message_delta", "event: message_stop",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in stream:\n%s", want, body)
		}
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("Content-Type = %q", ct)
	}
}

func TestStreamingToolCallEmitsInputJSONDelta(t *testing.T) {
	fake := fakevllmStreaming(t,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"read","arguments":"{\"p\":"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"1}"}}]}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
	)
	defer fake.Close()
	h := newTestServer(t, fake, "openai")

	rr := post(t, h, "/v1/messages", `{"model":"m","stream":true,"messages":[]}`)
	body := rr.Body.String()
	if !strings.Contains(body, "input_json_delta") {
		t.Errorf("no input_json_delta:\n%s", body)
	}
	if !strings.Contains(body, `"id":"call_1"`) {
		t.Errorf("tool id missing:\n%s", body)
	}
	if !strings.Contains(body, `"stop_reason":"tool_use"`) {
		t.Errorf("stop_reason wrong:\n%s", body)
	}
}

func TestScanSSEParsesEventsAndSkipsDone(t *testing.T) {
	in := strings.NewReader("event: x\ndata: {\"a\":1}\n\ndata: {\"b\":2}\n\ndata: [DONE]\n\n")
	var got []string
	err := ScanSSE(in, func(event, data string) error {
		got = append(got, event+"|"+data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{`x|{"a":1}`, `|{"b":2}`}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("event %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// --- streamPassthrough coverage (mode=messages streaming) ---
//
// internal/fakevllm previously had no way to exercise a native /v1/messages
// streaming response: handleMessages ignored "stream" entirely. These tests
// use the new MessagesStreamChunks field (see fakevllm.go), which only
// activates when non-empty and the request carries "stream": true, so every
// other config's behavior is untouched.

func TestPassthroughStreamingForwardsToNativeMessagesEndpoint(t *testing.T) {
	fake := fakevllmMessagesStreaming(t,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`,
	)
	defer fake.Close()
	h := newTestServer(t, fake, "messages")

	post(t, h, "/v1/messages", `{"model":"m","stream":true,
		"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)

	upstream := fake.Requests()
	last := upstream[len(upstream)-1]
	if last.Path != "/v1/messages" {
		t.Errorf("called %s, want /v1/messages", last.Path)
	}
	if last.Path == "/v1/chat/completions" {
		t.Errorf("streaming passthrough must not reach /v1/chat/completions")
	}
	if !strings.Contains(last.Body, `"stream":true`) {
		t.Errorf("upstream body = %s, want stream:true forwarded", last.Body)
	}
}

func TestPassthroughStreamingRepairsToolUseIDWithoutID(t *testing.T) {
	fake := fakevllmMessagesStreaming(t,
		`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","name":"read_file","input":{}}}`,
	)
	defer fake.Close()
	h := newTestServer(t, fake, "messages")

	rr := post(t, h, "/v1/messages", `{"model":"m","stream":true,
		"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	body := rr.Body.String()
	if !strings.Contains(body, `"type":"tool_use"`) {
		t.Fatalf("tool_use block missing from stream:\n%s", body)
	}
	if !strings.Contains(body, `"id":"toolu_`) {
		t.Errorf("tool_use block missing a repaired id:\n%s", body)
	}
}

func TestPassthroughStreamingUsesTypeFieldWhenEventLineOmitted(t *testing.T) {
	fake := fakevllmMessagesStreaming(t,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`,
	)
	defer fake.Close()
	h := newTestServer(t, fake, "messages")

	rr := post(t, h, "/v1/messages", `{"model":"m","stream":true,
		"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	body := rr.Body.String()
	// fakevllm emits bare `data:` frames with no `event:` line, matching how a
	// real upstream sometimes behaves; the event name must come from the
	// payload's own "type" field in that case.
	if !strings.Contains(body, "event: content_block_delta") {
		t.Errorf("event name not derived from the payload's type field:\n%s", body)
	}
}

// TestPassthroughMidStreamFailureEmitsErrorEvent simulates a dropped upstream
// connection after one valid event by hijacking the raw connection and
// closing it before the chunked body is terminated — a real network failure,
// not something fakevllm's Config can express, so this builds its own tiny
// upstream the way TestPassthroughErrorIsWrappedWhenNotAnthropicShaped does.
func TestPassthroughMidStreamFailureEmitsErrorEvent(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m"}}})
		case "/v1/messages":
			body, _ := io.ReadAll(r.Body)
			var req struct {
				Stream bool `json:"stream"`
			}
			json.Unmarshal(body, &req)
			if !req.Stream {
				// The capability probe from probe.Detect: any 2xx marks the
				// endpoint present.
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","content":[]}`))
				return
			}

			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Fatalf("test server's ResponseWriter does not support hijacking")
			}
			conn, buf, err := hj.Hijack()
			if err != nil {
				t.Fatalf("hijack: %v", err)
			}
			defer conn.Close()
			buf.WriteString("HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nTransfer-Encoding: chunked\r\n\r\n")
			cw := httputil.NewChunkedWriter(buf)
			cw.Write([]byte(`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"hi"}}` + "\n\n"))
			buf.Flush()
			// Deliberately no terminating zero-length chunk: the connection
			// just dies, like a network drop mid-generation.
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	cfg := config.Default()
	cfg.VLLMURL = upstream.URL
	cfg.Model = "m"
	cfg.Mode = "messages"
	caps, err := probe.Detect(context.Background(), http.DefaultClient, upstream.URL, "")
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	lg, err := NewLogger("off", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := New(cfg, caps, lg).Handler()

	rr := post(t, h, "/v1/messages", `{"model":"m","stream":true,
		"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	body := rr.Body.String()
	if !strings.Contains(body, "event: error") {
		t.Errorf("mid-stream failure produced no client-visible error event:\n%s", body)
	}
	// The one event that did arrive before the drop must still have reached
	// the client — a truncated answer, not a silently empty one.
	if !strings.Contains(body, "event: content_block_delta") {
		t.Errorf("the event received before the drop is missing:\n%s", body)
	}
}

// TestStreamTranslatedAsksUpstreamForUsage pins FIX 1's outgoing contract.
// vLLM's OpenAI server attaches usage to streamed chunks only when the request
// carries stream_options.include_usage, so without this line on the wire the
// translator reports zero tokens for an entire session and Claude Code's
// context meter stays blank until the conversation dies of a context error.
func TestStreamTranslatedAsksUpstreamForUsage(t *testing.T) {
	fake := fakevllmStreaming(t,
		`{"choices":[{"delta":{"content":"hi"}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`,
	)
	defer fake.Close()
	h := newTestServer(t, fake, "openai")

	rr := post(t, h, "/v1/messages", `{"model":"m","stream":true,
		"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)

	reqs := fake.Requests()
	var chat string
	for _, r := range reqs {
		if r.Path == "/v1/chat/completions" {
			chat = r.Body
		}
	}
	if chat == "" {
		t.Fatalf("no /v1/chat/completions request recorded: %+v", reqs)
	}
	if !strings.Contains(chat, `"include_usage":true`) {
		t.Errorf("upstream stream request did not ask for usage:\n%s", chat)
	}

	// End to end: because fakevllm now withholds usage from a request that
	// did not ask for it, the token counts reaching the client are proof the
	// request asked.
	body := rr.Body.String()
	if !strings.Contains(body, `"input_tokens":3`) {
		t.Errorf("input_tokens never reached the client:\n%s", body)
	}
	if !strings.Contains(body, `"output_tokens":2`) {
		t.Errorf("output_tokens never reached the client:\n%s", body)
	}
}

// TestStreamCancelledByClientIsNotReportedAsAVLLMFailure covers FIX 2.
// Pressing Esc in Claude Code aborts the request, cancelling the handler's
// context and breaking the upstream read — the single most common user action
// in a session. Calling that a dropped vLLM connection sends an operator with
// no internet to debug a network that is working.
func TestStreamCancelledByClientIsNotReportedAsAVLLMFailure(t *testing.T) {
	streaming := make(chan struct{})
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m"}}})
		case "/v1/chat/completions":
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			close(streaming)
			// Hold the stream open with no terminator, so the only thing that
			// can end the read is the client going away.
			<-release
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	// LIFO: release the upstream handler first, then shut the server down.
	defer upstream.Close()
	defer close(release)

	cfg := config.Default()
	cfg.VLLMURL = upstream.URL
	cfg.Model = "m"
	cfg.Mode = "openai"
	caps, err := probe.Detect(context.Background(), http.DefaultClient, upstream.URL, "")
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	lg, err := NewLogger("off", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := New(cfg, caps, lg).Handler()

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(
		`{"model":"m","stream":true,"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`,
	)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	rr := newSignalRecorder()

	done := make(chan struct{})
	go func() {
		h.ServeHTTP(rr, req)
		close(done)
	}()
	<-streaming
	// Wait for the first byte to reach the client before cancelling, so the
	// failure under test is a read error mid-stream rather than a cancelled
	// dial — the handler is then certainly inside ScanSSE.
	<-rr.wrote
	cancel() // what pressing Esc does
	<-done

	body := rr.String()
	if !strings.Contains(body, "event: message_start") {
		t.Fatalf("stream never started, so the cancellation path was not exercised:\n%s", body)
	}
	if strings.Contains(body, "event: error") {
		t.Errorf("a client cancellation was reported as a stream error:\n%s", body)
	}
	if strings.Contains(body, "connection to vLLM was interrupted") {
		t.Errorf("a client cancellation was blamed on vLLM:\n%s", body)
	}
}

// signalRecorder is an httptest.ResponseRecorder that can be read safely while
// a handler is still writing, and that announces its first write. The
// cancellation test needs both: it must cancel at a precise point in the
// handler's life, then inspect what was written.
type signalRecorder struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	hdr   http.Header
	code  int
	once  sync.Once
	wrote chan struct{}
}

func newSignalRecorder() *signalRecorder {
	return &signalRecorder{hdr: http.Header{}, code: http.StatusOK, wrote: make(chan struct{})}
}

func (s *signalRecorder) Header() http.Header { return s.hdr }

func (s *signalRecorder) WriteHeader(code int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.code = code
}

func (s *signalRecorder) Write(p []byte) (int, error) {
	s.mu.Lock()
	n, err := s.buf.Write(p)
	s.mu.Unlock()
	s.once.Do(func() { close(s.wrote) })
	return n, err
}

// Flush exists so newSSEWriter treats this as a flushable stream, matching how
// a real connection behaves.
func (s *signalRecorder) Flush() {}

func (s *signalRecorder) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}
