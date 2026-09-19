package config

import (
	"path/filepath"
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
		"CLAUDE_VLLM_URL":               "http://from-env:9000",
		"CLAUDE_VLLM_MODEL":             "env-model",
		"CLAUDE_VLLM_MAX_TOKENS":        "4096",
		"CLAUDE_VLLM_SUPPORTS_THINKING": "true",
	}
	ApplyEnv(&c, func(k string) (string, bool) { v, ok := env[k]; return v, ok })

	if c.VLLMURL != "http://from-env:9000" {
		t.Errorf("VLLMURL = %q, want env value", c.VLLMURL)
	}
	if c.Model != "env-model" {
		t.Errorf("Model = %q, want env-model", c.Model)
	}
	if c.MaxTokens != 4096 {
		t.Errorf("MaxTokens = %d, want 4096", c.MaxTokens)
	}
	if !c.SupportsThinking {
		t.Error("SupportsThinking = false, want true")
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

func TestLoadFromMissingFileReturnsDefaults(t *testing.T) {
	got, err := LoadFrom(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatalf("LoadFrom on missing file should not error, got %v", err)
	}
	if got != Default() {
		t.Errorf("got %+v, want defaults", got)
	}
}
