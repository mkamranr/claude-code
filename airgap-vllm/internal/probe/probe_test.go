package probe

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"claude-vllm/internal/fakevllm"
)

func TestDetectsNativeMessagesAPI(t *testing.T) {
	s := fakevllm.New(fakevllm.Config{MessagesAPI: true, Models: []string{"m"}, HasTokenize: true})
	defer s.Close()

	caps, err := Detect(context.Background(), http.DefaultClient, s.URL(), "")
	if err != nil {
		t.Fatal(err)
	}
	if !caps.MessagesAPI {
		t.Error("MessagesAPI = false, want true")
	}
	if !caps.Tokenize {
		t.Error("Tokenize = false, want true")
	}
	if !caps.HasModel("m") {
		t.Errorf("Models = %v, want to contain m", caps.Models)
	}
}

func TestDetectsAbsentMessagesAPI(t *testing.T) {
	s := fakevllm.New(fakevllm.Config{MessagesAPI: false, Models: []string{"m"}})
	defer s.Close()

	caps, err := Detect(context.Background(), http.DefaultClient, s.URL(), "")
	if err != nil {
		t.Fatal(err)
	}
	if caps.MessagesAPI {
		t.Error("MessagesAPI = true, want false on a 404")
	}
}

func TestDetectErrorsWhenUnreachable(t *testing.T) {
	_, err := Detect(context.Background(), http.DefaultClient, "http://127.0.0.1:1", "")
	if err == nil {
		t.Fatal("Detect on an unreachable host = nil error, want error")
	}
}

func TestResolveMode(t *testing.T) {
	cases := []struct {
		configured string
		caps       Capabilities
		want       string
	}{
		{"auto", Capabilities{MessagesAPI: true}, "messages"},
		{"auto", Capabilities{MessagesAPI: false}, "openai"},
		{"messages", Capabilities{MessagesAPI: false}, "messages"},
		{"openai", Capabilities{MessagesAPI: true}, "openai"},
	}
	for _, c := range cases {
		if got := ResolveMode(c.configured, c.caps); got != c.want {
			t.Errorf("ResolveMode(%q, %+v) = %q, want %q", c.configured, c.caps, got, c.want)
		}
	}
}

// TestMessagesAPITransportErrorSurfaces verifies that a transport failure
// on /v1/messages after successful /v1/models does not silently become
// "no Messages API available". The operator must see the error with the URL.
func TestMessagesAPITransportErrorSurfaces(t *testing.T) {
	// Create a server that serves /v1/models normally but panics on /v1/messages.
	// The panic will cause a client-side connection error.
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"data": []map[string]string{{"id": "model1"}},
			})
			return
		}
		if r.URL.Path == "/v1/messages" {
			// Simulate transport failure by panicking (causes connection reset).
			panic("simulated transport failure")
		}
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer s.Close()

	_, err := Detect(context.Background(), http.DefaultClient, s.URL, "")
	if err == nil {
		t.Fatal("Detect with transport failure on /v1/messages = nil error, want error")
	}
	if !strings.Contains(err.Error(), s.URL) {
		t.Errorf("error does not contain base URL %s: %v", s.URL, err)
	}
	if !strings.Contains(err.Error(), "/v1/messages") {
		t.Errorf("error does not mention /v1/messages: %v", err)
	}
}

// TestTokenizeTransportErrorDegrades verifies that a transport failure
// on /tokenize does not abort Detect. Instead, Tokenize becomes false
// and the probe succeeds, allowing the bridge to estimate token counts.
func TestTokenizeTransportErrorDegrades(t *testing.T) {
	// Create a server that serves /v1/models and /v1/messages normally
	// but panics on /tokenize. This should not cause Detect to fail.
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"data": []map[string]string{{"id": "model1"}},
			})
			return
		}
		if r.URL.Path == "/v1/messages" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "bad request"})
			return
		}
		if r.URL.Path == "/tokenize" {
			// Simulate transport failure by panicking.
			panic("simulated transport failure")
		}
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer s.Close()

	caps, err := Detect(context.Background(), http.DefaultClient, s.URL, "")
	if err != nil {
		t.Fatalf("Detect with transport failure on /tokenize = error, want nil: %v", err)
	}
	if caps.Tokenize {
		t.Error("Tokenize = true, want false (degraded due to transport error)")
	}
	if len(caps.Models) == 0 {
		t.Error("Models empty, want populated")
	}
}
