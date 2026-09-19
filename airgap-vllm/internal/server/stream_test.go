package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"strings"
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
