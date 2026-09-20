// Package config holds the developer-editable settings for the vLLM bridge.
// Precedence is defaults, then the config file, then CLAUDE_VLLM_* env vars.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
)

type Config struct {
	VLLMURL       string `json:"vllm_url"`
	Model         string `json:"model"`
	APIKey        string `json:"api_key"`
	Mode          string `json:"mode"`
	MaxTokens     int    `json:"max_tokens"`
	ContextWindow int    `json:"context_window"`
	Log           string `json:"log"`
}

func Default() Config {
	return Config{
		Mode:          "auto",
		MaxTokens:     8192,
		ContextWindow: 131072,
		Log:           "off",
	}
}

// Dir is %USERPROFILE%\.claude-vllm on Windows, $HOME/.claude-vllm elsewhere.
func Dir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locating home directory: %w", err)
	}
	return filepath.Join(home, ".claude-vllm"), nil
}

func Path() (string, error) {
	d, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "config.json"), nil
}

// Load reads the default config path and applies environment overrides. The
// returned warnings describe CLAUDE_VLLM_* values that could not be used; they
// are not fatal, but every caller must put them somewhere an operator reads —
// a silently ignored override is the kind of thing that costs an afternoon on
// a machine with no internet to search from.
func Load() (Config, []string, error) {
	p, err := Path()
	if err != nil {
		return Config{}, nil, err
	}
	c, err := LoadFrom(p)
	if err != nil {
		return Config{}, nil, err
	}
	return c, ApplyEnv(&c, os.LookupEnv), nil
}

// LoadFrom reads one file. A missing file is not an error; it yields defaults
// so that `setup` can run before any config exists.
func LoadFrom(path string) (Config, error) {
	c := Default()
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("reading %s: %w", path, err)
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return Config{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	return c, nil
}

// ApplyEnv overlays CLAUDE_VLLM_* variables. lookup is injected so tests need
// no process environment.
//
// A malformed value is never fatal — refusing to start because of a typo in a
// variable the operator may not even have meant to set would be worse than
// carrying on — but it is never silent either. It is returned as a warning and
// the prior value is kept, so `CLAUDE_VLLM_CONTEXT_WINDOW=131o72` says so
// instead of leaving someone to debug a context window they think they changed.
func ApplyEnv(c *Config, lookup func(string) (string, bool)) []string {
	var warnings []string
	warn := func(key, value, kind string, keeping any) {
		warnings = append(warnings, fmt.Sprintf(
			"%s=%q is not %s; ignoring it and keeping %v", key, value, kind, keeping))
	}
	str := func(key string, dst *string) {
		if v, ok := lookup(key); ok && v != "" {
			*dst = v
		}
	}
	num := func(key string, dst *int) {
		v, ok := lookup(key)
		if !ok || v == "" {
			return
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			warn(key, v, "a whole number", *dst)
			return
		}
		*dst = n
	}
	str("CLAUDE_VLLM_URL", &c.VLLMURL)
	str("CLAUDE_VLLM_MODEL", &c.Model)
	str("CLAUDE_VLLM_API_KEY", &c.APIKey)
	str("CLAUDE_VLLM_MODE", &c.Mode)
	str("CLAUDE_VLLM_LOG", &c.Log)
	num("CLAUDE_VLLM_MAX_TOKENS", &c.MaxTokens)
	num("CLAUDE_VLLM_CONTEXT_WINDOW", &c.ContextWindow)
	return warnings
}

var (
	validModes = map[string]bool{"auto": true, "messages": true, "openai": true}
	validLogs  = map[string]bool{"off": true, "requests": true, "full": true}
)

func (c Config) Validate() error {
	if c.VLLMURL == "" {
		return errors.New("vllm_url is not set — run: claude-vllm config set url=http://HOST:PORT")
	}
	u, err := url.Parse(c.VLLMURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("vllm_url %q is not an http(s) URL, e.g. http://10.0.0.5:8000", c.VLLMURL)
	}
	if c.Model == "" {
		return errors.New("model is not set — run: claude-vllm config set model=NAME")
	}
	if !validModes[c.Mode] {
		return fmt.Errorf("mode %q must be auto, messages or openai", c.Mode)
	}
	if !validLogs[c.Log] {
		return fmt.Errorf("log %q must be off, requests or full", c.Log)
	}
	if c.MaxTokens <= 0 {
		return fmt.Errorf("max_tokens must be positive, got %d", c.MaxTokens)
	}
	if c.ContextWindow <= 0 {
		return fmt.Errorf("context_window must be positive, got %d", c.ContextWindow)
	}
	return nil
}

func (c Config) SaveTo(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	// os.WriteFile only applies the mode argument when it creates the file; an
	// existing config.json left at a looser mode by an earlier version or
	// another tool keeps that mode. This file holds api_key, so the mode is
	// enforced explicitly rather than trusted to WriteFile.
	if err := os.WriteFile(path, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}
