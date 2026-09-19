package cli

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"claude-vllm/internal/config"
)

func TestApplyAssignments(t *testing.T) {
	c := config.Default()
	err := ApplyAssignments(&c, []string{
		"url=http://10.0.0.5:8000", "model=Qwen/Qwen3.8-27B", "max_tokens=4096", "mode=openai",
	})
	if err != nil {
		t.Fatal(err)
	}
	if c.VLLMURL != "http://10.0.0.5:8000" || c.Model != "Qwen/Qwen3.8-27B" {
		t.Errorf("config = %+v", c)
	}
	if c.MaxTokens != 4096 || c.Mode != "openai" {
		t.Errorf("config = %+v", c)
	}
}

func TestApplyAssignmentsRejectsUnknownKey(t *testing.T) {
	c := config.Default()
	if err := ApplyAssignments(&c, []string{"colour=blue"}); err == nil {
		t.Fatal("unknown key accepted, want error naming valid keys")
	}
}

func TestApplyAssignmentsRejectsMalformedPair(t *testing.T) {
	c := config.Default()
	if err := ApplyAssignments(&c, []string{"url"}); err == nil {
		t.Fatal("malformed assignment accepted")
	}
}

func TestApplyAssignmentsRejectsBadNumber(t *testing.T) {
	c := config.Default()
	if err := ApplyAssignments(&c, []string{"max_tokens=lots"}); err == nil {
		t.Fatal("non-numeric max_tokens accepted")
	}
}

// TestConfigSetRejectsInvalidResultingConfig covers finding 3: applying an
// assignment must not be allowed to leave the saved config in a state
// Validate would reject, e.g. `config set url=` blanking a working url. The
// file on disk must be untouched by the rejected attempt.
func TestConfigSetRejectsInvalidResultingConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	if err := ConfigSet([]string{"url=http://127.0.0.1:8000", "model=m"}); err != nil {
		t.Fatalf("initial config set: %v", err)
	}
	path, err := config.Path()
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if err := ConfigSet([]string{"url="}); err == nil {
		t.Fatal("config set url= accepted, want a validation error")
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Errorf("config file changed after a rejected config set\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// TestConfigSetAllowsClearingAPIKey covers the other half of finding 3:
// api_key is exempt from Validate, so `config set api_key=` clearing a key
// must remain a legitimate, accepted operation.
func TestConfigSetAllowsClearingAPIKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	if err := ConfigSet([]string{"url=http://127.0.0.1:8000", "model=m", "api_key=secret"}); err != nil {
		t.Fatalf("initial config set: %v", err)
	}
	if err := ConfigSet([]string{"api_key="}); err != nil {
		t.Fatalf("clearing api_key should succeed: %v", err)
	}

	path, err := config.Path()
	if err != nil {
		t.Fatal(err)
	}
	c, err := config.LoadFrom(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.APIKey != "" {
		t.Errorf("api_key was not cleared, got %q", c.APIKey)
	}
	if c.VLLMURL != "http://127.0.0.1:8000" || c.Model != "m" {
		t.Errorf("unrelated fields changed: %+v", c)
	}
}

// TestConfigShowRedactsAPIKey is a regression test for finding 5: it fails if
// a later edit to the field copy in configShow reintroduces the literal
// api_key into the printed output.
func TestConfigShowRedactsAPIKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	const sentinel = "sk-config-show-sentinel-do-not-print"
	if err := ConfigSet([]string{"url=http://127.0.0.1:8000", "model=m", "api_key=" + sentinel}); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := configShow(&buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if strings.Contains(out, sentinel) {
		t.Errorf("ConfigShow leaked the api_key:\n%s", out)
	}
	if !strings.Contains(out, "http://127.0.0.1:8000") {
		t.Errorf("ConfigShow output missing vllm_url:\n%s", out)
	}
	if !strings.Contains(out, `"model": "m"`) {
		t.Errorf("ConfigShow output missing model:\n%s", out)
	}
}
