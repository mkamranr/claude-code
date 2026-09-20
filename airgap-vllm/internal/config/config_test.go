package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDefaultIsValidExceptURL(t *testing.T) {
	c := Default()
	if c.Mode != "auto" {
		t.Errorf("Mode = %q, want auto", c.Mode)
	}
	if c.Log != "off" {
		t.Errorf("Log = %q, want off", c.Log)
	}
	if c.MaxTokens <= 0 {
		t.Errorf("MaxTokens = %d, want > 0", c.MaxTokens)
	}
	if err := c.Validate(); err == nil {
		t.Fatal("Validate() on default = nil, want error about missing vllm_url")
	}
}

func TestValidateRejectsBadValues(t *testing.T) {
	base := Default()
	base.VLLMURL = "http://10.0.0.5:8000"
	base.Model = "m"

	cases := map[string]func(*Config){
		"bad mode":     func(c *Config) { c.Mode = "sideways" },
		"bad log":      func(c *Config) { c.Log = "loud" },
		"bad url":      func(c *Config) { c.VLLMURL = "notaurl" },
		"ftp url":      func(c *Config) { c.VLLMURL = "ftp://x/y" },
		"zero maxtok":  func(c *Config) { c.MaxTokens = 0 },
		"empty model":  func(c *Config) { c.Model = "" },
		"zero context": func(c *Config) { c.ContextWindow = 0 },
	}
	for name, mutate := range cases {
		c := base
		mutate(&c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s: Validate() = nil, want error", name)
		}
	}
	if err := base.Validate(); err != nil {
		t.Errorf("valid config rejected: %v", err)
	}
}

func TestApplyEnvOverridesFileValues(t *testing.T) {
	c := Default()
	c.VLLMURL = "http://from-file:8000"
	c.Model = "file-model"
	env := map[string]string{
		"CLAUDE_VLLM_URL":        "http://from-env:9000",
		"CLAUDE_VLLM_MODEL":      "env-model",
		"CLAUDE_VLLM_MAX_TOKENS": "4096",
	}
	warnings := ApplyEnv(&c, func(k string) (string, bool) { v, ok := env[k]; return v, ok })
	if len(warnings) != 0 {
		t.Errorf("well-formed overrides produced warnings: %v", warnings)
	}

	if c.VLLMURL != "http://from-env:9000" {
		t.Errorf("VLLMURL = %q, want env value", c.VLLMURL)
	}
	if c.Model != "env-model" {
		t.Errorf("Model = %q, want env-model", c.Model)
	}
	if c.MaxTokens != 4096 {
		t.Errorf("MaxTokens = %d, want 4096", c.MaxTokens)
	}
}

// A typo in a numeric override must not vanish. Keeping the prior value is
// right; keeping it silently is what leaves an operator debugging a context
// window they are certain they changed.
func TestApplyEnvWarnsAboutMalformedNumbersAndKeepsPriorValue(t *testing.T) {
	c := Default()
	env := map[string]string{
		"CLAUDE_VLLM_CONTEXT_WINDOW": "131o72",
		"CLAUDE_VLLM_MAX_TOKENS":     "lots",
	}
	warnings := ApplyEnv(&c, func(k string) (string, bool) { v, ok := env[k]; return v, ok })

	if c.ContextWindow != Default().ContextWindow {
		t.Errorf("ContextWindow = %d, want the prior value %d", c.ContextWindow, Default().ContextWindow)
	}
	if c.MaxTokens != Default().MaxTokens {
		t.Errorf("MaxTokens = %d, want the prior value %d", c.MaxTokens, Default().MaxTokens)
	}
	if len(warnings) != 2 {
		t.Fatalf("warnings = %v, want one per malformed value", warnings)
	}
	joined := strings.Join(warnings, "\n")
	for _, want := range []string{"CLAUDE_VLLM_CONTEXT_WINDOW", "131o72", "CLAUDE_VLLM_MAX_TOKENS", "lots"} {
		if !strings.Contains(joined, want) {
			t.Errorf("warnings do not name %q:\n%s", want, joined)
		}
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	want := Default()
	want.VLLMURL = "http://10.0.0.5:8000"
	want.Model = "Qwen/Qwen3.8-27B"
	if err := want.SaveTo(p); err != nil {
		t.Fatalf("SaveTo: %v", err)
	}
	got, err := LoadFrom(p)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if got != want {
		t.Errorf("round trip mismatch\n got %+v\nwant %+v", got, want)
	}
}

func TestSaveToEnforcesRestrictivePermissionsOnExistingFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits; Chmod on Windows only toggles the read-only bit")
	}
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}

	c := Default()
	c.VLLMURL = "http://10.0.0.5:8000"
	c.Model = "m"
	if err := c.SaveTo(p); err != nil {
		t.Fatalf("SaveTo: %v", err)
	}

	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("mode = %o, want 0600 — a pre-existing looser mode must not survive a config holding api_key", mode)
	}
}

func TestLoadFromMissingFileReturnsDefaults(t *testing.T) {
	got, err := LoadFrom(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatalf("LoadFrom on missing file should not error, got %v", err)
	}
	if got != Default() {
		t.Errorf("got %+v, want defaults", got)
	}
}
