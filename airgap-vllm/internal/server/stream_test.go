package server

import (
	"net/http"
	"strings"
	"testing"
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

var _ = http.StatusOK
