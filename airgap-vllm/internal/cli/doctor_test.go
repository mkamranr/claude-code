package cli

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"claude-vllm/internal/config"
	"claude-vllm/internal/fakevllm"
	"claude-vllm/internal/probe"
	"claude-vllm/internal/server"
)

func handlerFor(t *testing.T, fake *fakevllm.Server) http.Handler {
	t.Helper()
	cfg := config.Default()
	cfg.VLLMURL = fake.URL()
	cfg.Model = "m"
	caps, err := probe.Detect(context.Background(), http.DefaultClient, fake.URL(), "")
	if err != nil {
		t.Fatal(err)
	}
	lg, _ := server.NewLogger("off", t.TempDir())
	return server.New(cfg, caps, lg).Handler()
}

func TestSmokeTestPassesWhenModelEmitsAToolCall(t *testing.T) {
	fake := fakevllm.New(fakevllm.Config{
		Models: []string{"m"},
		ChatCompletion: `{"id":"c1","choices":[{"message":{"tool_calls":[
			{"id":"call_1","type":"function","function":{"name":"ping","arguments":"{\"value\":\"ok\"}"}}]},
			"finish_reason":"tool_calls"}]}`,
	})
	defer fake.Close()

	if err := SmokeTest(context.Background(), handlerFor(t, fake), "m"); err != nil {
		t.Fatalf("SmokeTest = %v, want nil", err)
	}
}

func TestSmokeTestFailsWhenModelReturnsProseInsteadOfAToolCall(t *testing.T) {
	fake := fakevllm.New(fakevllm.Config{
		Models: []string{"m"},
		ChatCompletion: `{"id":"c1","choices":[{"message":{"content":
			"Sure! I would call ping with value ok."},"finish_reason":"stop"}]}`,
	})
	defer fake.Close()

	err := SmokeTest(context.Background(), handlerFor(t, fake), "m")
	if err == nil {
		t.Fatal("SmokeTest = nil, want an error naming the missing tool call")
	}
	if !strings.Contains(err.Error(), "tool") {
		t.Errorf("error should mention tool calling, got: %v", err)
	}
}

func TestSmokeTestFailsWhenToolCallHasNoName(t *testing.T) {
	fake := fakevllm.New(fakevllm.Config{
		Models: []string{"m"},
		ChatCompletion: `{"id":"c1","choices":[{"message":{"tool_calls":[
			{"id":"call_1","type":"function","function":{"arguments":"{}"}}]},
			"finish_reason":"tool_calls"}]}`,
	})
	defer fake.Close()

	if err := SmokeTest(context.Background(), handlerFor(t, fake), "m"); err == nil {
		t.Fatal("a nameless tool call should fail the smoke test")
	}
}
