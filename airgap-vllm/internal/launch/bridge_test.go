package launch

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"claude-vllm/internal/config"
	"claude-vllm/internal/fakevllm"
)

func bridgeConfig(url string) config.Config {
	c := config.Default()
	c.VLLMURL = url
	c.Model = "m1"
	return c
}

func TestStartBridgeServesModelsOnLoopback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	fv := fakevllm.New(fakevllm.Config{MessagesAPI: true, Models: []string{"m1"}})
	defer fv.Close()

	b, err := StartBridge(bridgeConfig(fv.URL()))
	if err != nil {
		t.Fatalf("StartBridge: %v", err)
	}
	defer b.Close()

	if b.Port == 0 {
		t.Fatal("bridge reported port 0")
	}
	if b.Mode() == "" {
		t.Fatal("bridge reported no mode")
	}
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/v1/models", b.Port))
	if err != nil {
		t.Fatalf("GET /v1/models: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "m1") {
		t.Fatalf("GET /v1/models = %d %s", resp.StatusCode, body)
	}
}

func TestStartBridgeRejectsInvalidConfig(t *testing.T) {
	if _, err := StartBridge(config.Default()); err == nil {
		t.Fatal("StartBridge accepted a config with no vllm_url")
	}
}

func TestStartBridgeReportsUnreachableEndpoint(t *testing.T) {
	fv := fakevllm.New(fakevllm.Config{})
	url := fv.URL()
	fv.Close()

	_, err := StartBridge(bridgeConfig(url))
	if err == nil || !strings.Contains(err.Error(), "claude-vllm doctor") {
		t.Fatalf("want an error pointing at doctor, got %v", err)
	}
}

func TestBridgeCloseStopsServing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	fv := fakevllm.New(fakevllm.Config{MessagesAPI: true, Models: []string{"m1"}})
	defer fv.Close()

	b, err := StartBridge(bridgeConfig(fv.URL()))
	if err != nil {
		t.Fatalf("StartBridge: %v", err)
	}
	b.Close()
	if _, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/health", b.Port)); err == nil {
		t.Fatal("bridge still answering after Close")
	}
}
