package cli

import (
	"bytes"
	"strings"
	"testing"

	"claude-vllm/internal/config"
	"claude-vllm/internal/fakevllm"
)

// TestSetupNeverEchoesExistingAPIKey guards the API-key prompt: the literal
// key must never reach Setup's output, and pressing Enter (blank line) must
// still preserve it, matching the affordance the plain-text prompt used to
// give.
func TestSetupNeverEchoesExistingAPIKey(t *testing.T) {
	fv := fakevllm.New(fakevllm.Config{
		MessagesAPI: true,
		Models:      []string{"model-a", "model-b"},
	})
	defer fv.Close()

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	const sentinel = "sk-setup-echo-sentinel-do-not-print"

	path, err := config.Path()
	if err != nil {
		t.Fatal(err)
	}
	pre := config.Default()
	pre.VLLMURL = fv.URL()
	pre.APIKey = sentinel
	if err := pre.SaveTo(path); err != nil {
		t.Fatal(err)
	}

	// Blank line for the URL prompt (keep default), blank line for the API
	// key prompt (Enter-to-keep), "1" to pick the first model.
	in := strings.NewReader("\n\n1\n")
	var out bytes.Buffer

	if err := Setup(in, &out); err != nil {
		t.Fatalf("Setup: %v\noutput so far:\n%s", err, out.String())
	}

	if strings.Contains(out.String(), sentinel) {
		t.Errorf("Setup printed the literal API key into its output:\n%s", out.String())
	}

	c, err := config.LoadFrom(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.APIKey != sentinel {
		t.Errorf("Enter-to-keep did not preserve the existing API key: got %q, want %q", c.APIKey, sentinel)
	}
}

// TestSetupCanReplaceAPIKeyByTyping checks the other half of the affordance:
// typing a new value still replaces the stored key even though it is masked
// on display.
func TestSetupCanReplaceAPIKeyByTyping(t *testing.T) {
	fv := fakevllm.New(fakevllm.Config{
		MessagesAPI: true,
		Models:      []string{"model-a"},
	})
	defer fv.Close()

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	path, err := config.Path()
	if err != nil {
		t.Fatal(err)
	}
	pre := config.Default()
	pre.VLLMURL = fv.URL()
	pre.APIKey = "old-key"
	if err := pre.SaveTo(path); err != nil {
		t.Fatal(err)
	}

	in := strings.NewReader("\nnew-key\n1\n")
	var out bytes.Buffer
	if err := Setup(in, &out); err != nil {
		t.Fatalf("Setup: %v\noutput so far:\n%s", err, out.String())
	}

	c, err := config.LoadFrom(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.APIKey != "new-key" {
		t.Errorf("APIKey = %q, want new-key", c.APIKey)
	}
}

// TestSetupNoModelsMessageNamesNextAction covers finding 2: an empty model
// list must tell the operator what to do, not just what failed.
func TestSetupNoModelsMessageNamesNextAction(t *testing.T) {
	fv := fakevllm.New(fakevllm.Config{MessagesAPI: true, Models: nil})
	defer fv.Close()

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	in := strings.NewReader(fv.URL() + "\n\n")
	var out bytes.Buffer
	err := Setup(in, &out)
	if err == nil {
		t.Fatal("Setup with no models = nil error, want error")
	}
	if !strings.Contains(err.Error(), "re-run: claude-vllm setup") {
		t.Errorf("error does not name a next action: %v", err)
	}
}

// TestSetupBadSelectionMessageNamesNextAction covers finding 2: an
// out-of-range or non-numeric model selection must tell the operator what to
// do, not just what failed.
func TestSetupBadSelectionMessageNamesNextAction(t *testing.T) {
	fv := fakevllm.New(fakevllm.Config{MessagesAPI: true, Models: []string{"model-a"}})
	defer fv.Close()

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	in := strings.NewReader(fv.URL() + "\n\n99\n")
	var out bytes.Buffer
	err := Setup(in, &out)
	if err == nil {
		t.Fatal("Setup with an out-of-range selection = nil error, want error")
	}
	if !strings.Contains(err.Error(), "re-run: claude-vllm setup") {
		t.Errorf("error does not name a next action: %v", err)
	}
}
