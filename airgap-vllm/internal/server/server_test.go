package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"claude-vllm/internal/config"
	"claude-vllm/internal/fakevllm"
	"claude-vllm/internal/probe"
)

func newTestServer(t *testing.T, fake *fakevllm.Server, mode string) http.Handler {
	t.Helper()
	cfg := config.Default()
	cfg.VLLMURL = fake.URL()
	cfg.Model = "m"
	cfg.Mode = mode
	caps, err := probe.Detect(context.Background(), http.DefaultClient, fake.URL(), "")
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	lg, err := NewLogger("off", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return New(cfg, caps, lg).Handler()
}

func post(t *testing.T, h http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func TestHealthReportsMode(t *testing.T) {
	fake := fakevllm.New(fakevllm.Config{Models: []string{"m"}})
	defer fake.Close()
	h := newTestServer(t, fake, "auto")

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"mode":"openai"`) {
		t.Errorf("health = %d %s, want mode openai (fake has no /v1/messages)", rr.Code, rr.Body)
	}
}

func TestModelsListIsAnthropicShaped(t *testing.T) {
	fake := fakevllm.New(fakevllm.Config{Models: []string{"Qwen/Qwen3.8-27B"}})
	defer fake.Close()
	h := newTestServer(t, fake, "auto")

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	var body struct {
		Data []struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("bad body %s: %v", rr.Body, err)
	}
	if len(body.Data) != 1 || body.Data[0].ID != "Qwen/Qwen3.8-27B" || body.Data[0].Type != "model" {
		t.Errorf("models = %+v", body.Data)
	}
}

func TestOpenAIModeTranslatesEndToEnd(t *testing.T) {
	fake := fakevllm.New(fakevllm.Config{
		Models: []string{"m"},
		ChatCompletion: `{"id":"c1","model":"m","choices":[
			{"message":{"content":"hi there"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":5,"completion_tokens":2}}`,
	})
	defer fake.Close()
	h := newTestServer(t, fake, "openai")

	rr := post(t, h, "/v1/messages", `{"model":"m","max_tokens":10,
		"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	if rr.Code != 200 {
		t.Fatalf("status %d body %s", rr.Code, rr.Body)
	}
	var got map[string]any
	json.Unmarshal(rr.Body.Bytes(), &got)
	block := got["content"].([]any)[0].(map[string]any)
	if block["text"] != "hi there" {
		t.Errorf("content = %v", got["content"])
	}

	// The upstream must have received an OpenAI-shaped request.
	upstream := fake.Requests()
	last := upstream[len(upstream)-1]
	if last.Path != "/v1/chat/completions" {
		t.Errorf("called %s, want /v1/chat/completions", last.Path)
	}
}

// The advisor block caused HTTP 400 against real proxied endpoints.
func TestUnknownBlocksAreFilteredBeforeUpstream(t *testing.T) {
	fake := fakevllm.New(fakevllm.Config{
		Models:         []string{"m"},
		ChatCompletion: `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`,
	})
	defer fake.Close()
	h := newTestServer(t, fake, "openai")

	post(t, h, "/v1/messages", `{"model":"m","messages":[{"role":"user","content":[
		{"type":"text","text":"hi"},{"type":"advisor_20260301","payload":"x"}]}]}`)

	for _, r := range fake.Requests() {
		if strings.Contains(r.Body, "advisor_20260301") {
			t.Errorf("unknown block reached upstream: %s", r.Body)
		}
	}
}

func TestCountTokensUsesTokenizeWhenAvailable(t *testing.T) {
	fake := fakevllm.New(fakevllm.Config{Models: []string{"m"}, HasTokenize: true, TokenCount: 42})
	defer fake.Close()
	h := newTestServer(t, fake, "openai")

	rr := post(t, h, "/v1/messages/count_tokens",
		`{"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`)
	if !strings.Contains(rr.Body.String(), "42") {
		t.Errorf("count_tokens = %s, want 42 from /tokenize", rr.Body)
	}
}

func TestCountTokensFallsBackToEstimate(t *testing.T) {
	fake := fakevllm.New(fakevllm.Config{Models: []string{"m"}, HasTokenize: false})
	defer fake.Close()
	h := newTestServer(t, fake, "openai")

	rr := post(t, h, "/v1/messages/count_tokens",
		`{"messages":[{"role":"user","content":[{"type":"text","text":"12345678"}]}]}`)
	var got struct {
		InputTokens int `json:"input_tokens"`
	}
	json.Unmarshal(rr.Body.Bytes(), &got)
	if got.InputTokens < 1 {
		t.Errorf("estimate = %d, want a positive fallback", got.InputTokens)
	}
}

func TestUpstreamErrorIsReportedNotSwallowed(t *testing.T) {
	fake := fakevllm.New(fakevllm.Config{Models: []string{"m"}, ChatCompletion: ``})
	fake.Close() // force a connection failure
	cfg := config.Default()
	cfg.VLLMURL = fake.URL()
	cfg.Model = "m"
	cfg.Mode = "openai"
	lg, _ := NewLogger("off", t.TempDir())
	h := New(cfg, probe.Capabilities{}, lg).Handler()

	rr := post(t, h, "/v1/messages", `{"messages":[]}`)
	if rr.Code < 500 {
		t.Errorf("status = %d, want 5xx when upstream is down", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "error") {
		t.Errorf("body = %s, want an Anthropic-shaped error", rr.Body)
	}
}
