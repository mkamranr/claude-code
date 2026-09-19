package fakevllm

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestServesModelsAndRecordsRequests(t *testing.T) {
	s := New(Config{Models: []string{"Qwen/Qwen3.8-27B"}})
	defer s.Close()

	resp, err := http.Get(s.URL() + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), "Qwen/Qwen3.8-27B") {
		t.Errorf("models body = %s", b)
	}
	if got := s.Requests(); len(got) != 1 || got[0].Path != "/v1/models" {
		t.Errorf("requests = %+v", got)
	}
}

func TestMessagesAPIAbsentReturns404(t *testing.T) {
	s := New(Config{MessagesAPI: false})
	defer s.Close()
	resp, _ := http.Post(s.URL()+"/v1/messages", "application/json", strings.NewReader("{}"))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestChatCompletionEchoesConfiguredBody(t *testing.T) {
	s := New(Config{ChatCompletion: `{"id":"c1"}`})
	defer s.Close()
	resp, _ := http.Post(s.URL()+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"m"}`))
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if strings.TrimSpace(string(b)) != `{"id":"c1"}` {
		t.Errorf("body = %s", b)
	}
	if !strings.Contains(s.Requests()[0].Body, `"model":"m"`) {
		t.Errorf("request body not recorded: %+v", s.Requests())
	}
}
