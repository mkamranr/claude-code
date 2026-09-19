package probe

import (
	"context"
	"net/http"
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
