package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
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

func fakevllmStreaming(t *testing.T, chunks ...string) *fakevllm.Server {
	t.Helper()
	return fakevllm.New(fakevllm.Config{Models: []string{"m"}, StreamChunks: chunks})
}

func fakevllmMessagesStreaming(t *testing.T, chunks ...string) *fakevllm.Server {
	t.Helper()
	return fakevllm.New(fakevllm.Config{
		MessagesAPI: true, Models: []string{"m"}, MessagesStreamChunks: chunks,
	})
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

// A model absent from the cached list is rejected with a diagnostic naming
// what the request asked for and what the server actually serves — never
// silently rewritten to the configured model when a live list says it truly
// does not exist.
func TestUnknownModelIsRejectedWithAvailableList(t *testing.T) {
	fake := fakevllm.New(fakevllm.Config{Models: []string{"m"}})
	defer fake.Close()
	h := newTestServer(t, fake, "openai")

	rr := post(t, h, "/v1/messages", `{"model":"nonexistent","max_tokens":10,
		"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status %d body %s, want 400", rr.Code, rr.Body)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "nonexistent") {
		t.Errorf("body = %s, want it to name the requested model", body)
	}
	if !strings.Contains(body, "Available: m") {
		t.Errorf("body = %s, want it to list what the server serves", body)
	}
	if !strings.Contains(body, "config set model=NAME") {
		t.Errorf("body = %s, want it to tell the operator how to fix it", body)
	}
}

// A model missing from a stale cache but present on a live refresh is used,
// not rejected — the cache captured at New() can simply be out of date.
func TestModelBecomesAvailableAfterRefresh(t *testing.T) {
	fake := fakevllm.New(fakevllm.Config{
		Models:         []string{"m"},
		ChatCompletion: `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`,
	})
	defer fake.Close()

	cfg := config.Default()
	cfg.VLLMURL = fake.URL()
	cfg.Model = "m"
	cfg.Mode = "openai"
	lg, err := NewLogger("off", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Deliberately stale: constructed before the server ever reported "m".
	h := New(cfg, probe.Capabilities{}, lg).Handler()

	rr := post(t, h, "/v1/messages", `{"model":"m","max_tokens":10,
		"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	if rr.Code != 200 {
		t.Fatalf("status %d body %s, want the stale cache to refresh and succeed", rr.Code, rr.Body)
	}
}

// When the live model list cannot be trusted (empty or unreachable), the
// bridge falls back to the configured model rather than start rejecting
// every request on a deployment that previously worked — but it must never
// do so silently.
func TestUnknownModelFallsBackWhenListUnavailable(t *testing.T) {
	fake := fakevllm.New(fakevllm.Config{
		Models:         []string{}, // refresh succeeds but confirms nothing
		ChatCompletion: `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`,
	})
	defer fake.Close()

	cfg := config.Default()
	cfg.VLLMURL = fake.URL()
	cfg.Model = "m"
	cfg.Mode = "openai"
	caps, err := probe.Detect(context.Background(), http.DefaultClient, fake.URL(), "")
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	logDir := t.TempDir()
	lg, err := NewLogger("requests", logDir)
	if err != nil {
		t.Fatal(err)
	}
	h := New(cfg, caps, lg).Handler()

	rr := post(t, h, "/v1/messages", `{"model":"ghost","max_tokens":10,
		"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	if rr.Code != 200 {
		t.Fatalf("status %d body %s, want a fallback to the configured model", rr.Code, rr.Body)
	}
	lg.Close()

	logged, err := os.ReadFile(logFilePath(logDir))
	if err != nil {
		t.Fatalf("reading log: %v", err)
	}
	if !strings.Contains(string(logged), `model "ghost"`) {
		t.Errorf("log = %q, want a note naming the substituted model", logged)
	}

	upstream := fake.Requests()
	last := upstream[len(upstream)-1]
	if !strings.Contains(last.Body, `"model":"m"`) {
		t.Errorf("upstream body = %s, want it to use the configured model m, not ghost", last.Body)
	}
}

// Passthrough (native /v1/messages) mode had zero coverage before this test:
// the request must reach /v1/messages, not /v1/chat/completions, and the
// same tool_use id guarantee that applies to the OpenAI lane must apply here.
func TestPassthroughModeForwardsToNativeMessagesAndEnsuresToolUseID(t *testing.T) {
	fake := fakevllm.New(fakevllm.Config{
		MessagesAPI: true,
		Models:      []string{"m"},
		MessagesBody: `{"id":"msg_1","type":"message","role":"assistant","content":[
			{"type":"tool_use","name":"read_file","input":{}}]}`,
	})
	defer fake.Close()
	h := newTestServer(t, fake, "messages")

	rr := post(t, h, "/v1/messages", `{"model":"m","max_tokens":10,
		"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	if rr.Code != 200 {
		t.Fatalf("status %d body %s", rr.Code, rr.Body)
	}

	upstream := fake.Requests()
	last := upstream[len(upstream)-1]
	if last.Path != "/v1/messages" {
		t.Errorf("called %s, want /v1/messages", last.Path)
	}

	var got map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("bad body %s: %v", rr.Body, err)
	}
	block := got["content"].([]any)[0].(map[string]any)
	if id, _ := block["id"].(string); id == "" {
		t.Errorf("tool_use block missing id after passthrough: %v", block)
	}
}

// A native /v1/messages build that answers a 400 in its own shape, not
// Anthropic's, must still reach Claude Code as an Anthropic-shaped error —
// otherwise the client renders garbage instead of a diagnostic.
func TestPassthroughErrorIsWrappedWhenNotAnthropicShaped(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m"}}})
		case "/v1/messages":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"detail":"bad request"}`)) // not Anthropic-shaped
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

	rr := post(t, h, "/v1/messages", `{"model":"m","max_tokens":10,
		"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want the upstream's 400 preserved", rr.Code)
	}
	var got map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("bad body %s: %v", rr.Body, err)
	}
	if got["type"] != "error" {
		t.Errorf("body = %s, want an Anthropic-shaped error envelope", rr.Body)
	}
}
