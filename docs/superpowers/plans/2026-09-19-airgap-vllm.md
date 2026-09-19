# Air-gapped Claude Code on vLLM — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship a transferable bundle that runs Claude Code against a self-hosted vLLM server on an air-gapped Windows x64 workstation, with no sign-in and a per-developer configurable endpoint and model.

**Architecture:** A single Go binary (`claude-vllm.exe`) runs on loopback between `claude.exe` and vLLM. It has two lanes selected by a startup probe: passthrough to vLLM's native `/v1/messages` when present, or translation over `/v1/chat/completions` when not. The same binary also acts as launcher, configurator and diagnostic tool. A staging script on a connected machine produces a checksum-verified zip; an install script on the air-gapped box verifies and installs it offline.

**Tech Stack:** Go (standard library only), PowerShell 5.1 (installer), Bash (staging), GPG + SHA256 for artifact verification.

**Spec:** `docs/superpowers/specs/2026-09-19-airgap-vllm-design.md`

## Global Constraints

Every task's requirements implicitly include this section.

- **Go standard library only. Zero third-party modules.** `go build` must succeed with an empty module cache. Do not add a dependency for JSON, TOML, CLI parsing, testing, or anything else.
- **`go.mod` declares `go 1.22`.** Module path is `claude-vllm`.
- **Build target:** `CGO_ENABLED=0 GOOS=windows GOARCH=amd64`. The code must also build and test on the host (darwin/linux) so tests run in CI and locally.
- **Never set `CLAUDE_CODE_USE_GATEWAY`.** It forces Cloud-gateway sign-in and fails every request with "Not signed in to the Cloud gateway". The launcher must actively strip it from the inherited environment.
- **Never set `ANTHROPIC_DEFAULT_*_MODEL_SUPPORTED_CAPABILITIES` by default.** These override Claude Code's own capability detection. They are an escape hatch only.
- **Pinned Claude Code version: `2.1.252`.** `win32-x64` artifact is `claude.exe`, size `217406624`, SHA256 `fe688ecde90d65fff0b2dd097597439774116077d80a3c1e131a7e47db307b1a`.
- **GPG signing key fingerprint: `31DDDE24DDFAB679F42D7BD2BAA929FF1A7ECACE`.** Staging must abort if it does not match.
- **All three model tiers (opus, sonnet, haiku) map to the single configured model** so background Haiku-tier calls cannot 404.
- **Config lives at `%USERPROFILE%\.claude-vllm\config.json`**, every key overridable by a `CLAUDE_VLLM_`-prefixed env var.
- Run `gofmt -w` on every Go file before committing. Commit after every task.

## File Structure

```
airgap-vllm/
  go.mod                              module claude-vllm, no deps
  cmd/claude-vllm/main.go             subcommand dispatch only
  internal/config/config.go           load / merge / validate / save
  internal/config/config_test.go
  internal/filter/filter.go           compatibility filter (pure functions)
  internal/filter/filter_test.go
  internal/translate/request.go       Anthropic -> OpenAI request
  internal/translate/response.go      OpenAI -> Anthropic response (non-streaming)
  internal/translate/stream.go        OpenAI SSE -> Anthropic SSE (state machine)
  internal/translate/types.go         shared wire structs
  internal/translate/*_test.go
  internal/fakevllm/fakevllm.go       httptest-based vLLM double
  internal/probe/probe.go             capability detection
  internal/probe/probe_test.go
  internal/server/server.go           HTTP handlers, mode dispatch, SSE, logging
  internal/server/server_test.go
  internal/launch/env.go              Claude Code environment contract
  internal/launch/settings.go         settings.json + ~/.claude.json writers
  internal/launch/run.go              child process supervision
  internal/launch/*_test.go
  internal/cli/config_cmd.go          `config set` / `config show`
  internal/cli/setup.go               interactive first run
  internal/cli/doctor.go              diagnostics + tool-calling smoke test
  scripts/stage.sh                    connected-machine staging
  install.ps1                         air-gapped installer
  README.md                           operator runbook
```

Responsibility boundaries: `filter` and `translate` are pure (no network, no clock, no filesystem) because that is where nearly all the risk lives and purity makes it exhaustively testable. `server` owns all I/O. `launch` owns the environment contract. `cli` owns human interaction only.

---

### Task 1: Module scaffold and config package

**Files:**
- Create: `airgap-vllm/go.mod`
- Create: `airgap-vllm/internal/config/config.go`
- Test: `airgap-vllm/internal/config/config_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `config.Config` struct with fields `VLLMURL, Model, APIKey, Mode string; MaxTokens, ContextWindow int; SupportsThinking bool; Log string`. Functions `config.Default() Config`, `config.Dir() (string, error)`, `config.Path() (string, error)`, `config.Load() (Config, error)`, `config.LoadFrom(path string) (Config, error)`, `(Config).Validate() error`, `(Config).SaveTo(path string) error`, `config.ApplyEnv(*Config, func(string) (string, bool))`.

- [ ] **Step 1: Create the module**

```bash
mkdir -p airgap-vllm/internal/config airgap-vllm/cmd/claude-vllm
cd airgap-vllm && go mod init claude-vllm && go mod edit -go=1.22
```

- [ ] **Step 2: Write the failing test**

Create `airgap-vllm/internal/config/config_test.go`:

```go
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
		"bad mode":      func(c *Config) { c.Mode = "sideways" },
		"bad log":       func(c *Config) { c.Log = "loud" },
		"bad url":       func(c *Config) { c.VLLMURL = "notaurl" },
		"ftp url":       func(c *Config) { c.VLLMURL = "ftp://x/y" },
		"zero maxtok":   func(c *Config) { c.MaxTokens = 0 },
		"empty model":   func(c *Config) { c.Model = "" },
		"zero context":  func(c *Config) { c.ContextWindow = 0 },
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
```

- [ ] **Step 3: Run test to verify it fails**

Run: `cd airgap-vllm && go test ./internal/config/`
Expected: FAIL — `undefined: Default`, `undefined: Config`.

- [ ] **Step 4: Write the implementation**

Create `airgap-vllm/internal/config/config.go`:

```go
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
	VLLMURL          string `json:"vllm_url"`
	Model            string `json:"model"`
	APIKey           string `json:"api_key"`
	Mode             string `json:"mode"`
	MaxTokens        int    `json:"max_tokens"`
	ContextWindow    int    `json:"context_window"`
	SupportsThinking bool   `json:"supports_thinking"`
	Log              string `json:"log"`
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

// Load reads the default config path and applies environment overrides.
func Load() (Config, error) {
	p, err := Path()
	if err != nil {
		return Config{}, err
	}
	c, err := LoadFrom(p)
	if err != nil {
		return Config{}, err
	}
	ApplyEnv(&c, os.LookupEnv)
	return c, nil
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
func ApplyEnv(c *Config, lookup func(string) (string, bool)) {
	str := func(key string, dst *string) {
		if v, ok := lookup(key); ok && v != "" {
			*dst = v
		}
	}
	num := func(key string, dst *int) {
		if v, ok := lookup(key); ok && v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				*dst = n
			}
		}
	}
	boolean := func(key string, dst *bool) {
		if v, ok := lookup(key); ok && v != "" {
			if b, err := strconv.ParseBool(v); err == nil {
				*dst = b
			}
		}
	}
	str("CLAUDE_VLLM_URL", &c.VLLMURL)
	str("CLAUDE_VLLM_MODEL", &c.Model)
	str("CLAUDE_VLLM_API_KEY", &c.APIKey)
	str("CLAUDE_VLLM_MODE", &c.Mode)
	str("CLAUDE_VLLM_LOG", &c.Log)
	num("CLAUDE_VLLM_MAX_TOKENS", &c.MaxTokens)
	num("CLAUDE_VLLM_CONTEXT_WINDOW", &c.ContextWindow)
	boolean("CLAUDE_VLLM_SUPPORTS_THINKING", &c.SupportsThinking)
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
	return os.WriteFile(path, append(b, '\n'), 0o600)
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd airgap-vllm && gofmt -w ./... && go test ./internal/config/ -v`
Expected: PASS, five tests.

- [ ] **Step 6: Commit**

```bash
git add airgap-vllm/go.mod airgap-vllm/internal/config/
git commit -m "feat(airgap-vllm): config load, env override and validation"
```

---

### Task 2: Compatibility filter

Every rule here fixes a documented failure of Anthropic-compatible third-party endpoints. See the spec's compatibility filter table for provenance.

**Files:**
- Create: `airgap-vllm/internal/filter/filter.go`
- Test: `airgap-vllm/internal/filter/filter_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `filter.Request(req map[string]any) (map[string]any, []string)` returning the filtered request and a list of human-readable notes about what was dropped. `filter.EnsureToolUseIDs(content []any, gen func() string) []any`. `filter.StripSchemaKeywords(schema any) any`.

- [ ] **Step 1: Write the failing test**

Create `airgap-vllm/internal/filter/filter_test.go`:

```go
package filter

import (
	"encoding/json"
	"strings"
	"testing"
)

func decode(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("bad test fixture: %v", err)
	}
	return m
}

func encode(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

// Reproduces the shipped regression: 400 "Input tag 'advisor_20260301'".
func TestDropsUnknownContentBlockTypes(t *testing.T) {
	in := decode(t, `{"messages":[{"role":"user","content":[
		{"type":"text","text":"hi"},
		{"type":"advisor_20260301","payload":"x"}
	]}]}`)
	out, notes := Request(in)
	got := encode(t, out)
	if strings.Contains(got, "advisor_20260301") {
		t.Errorf("unknown block survived: %s", got)
	}
	if !strings.Contains(got, `"hi"`) {
		t.Errorf("known block was dropped: %s", got)
	}
	if len(notes) == 0 {
		t.Error("dropping a block should produce a note")
	}
}

func TestDropsToolReferenceBlocks(t *testing.T) {
	in := decode(t, `{"messages":[{"role":"user","content":[
		{"type":"tool_reference","name":"x"},
		{"type":"text","text":"keep"}
	]}]}`)
	out, _ := Request(in)
	if strings.Contains(encode(t, out), "tool_reference") {
		t.Error("tool_reference block survived")
	}
}

func TestStripsCacheControlEverywhere(t *testing.T) {
	in := decode(t, `{"system":[{"type":"text","text":"s","cache_control":{"type":"ephemeral"}}],
		"messages":[{"role":"user","content":[{"type":"text","text":"u","cache_control":{"type":"ephemeral"}}]}],
		"tools":[{"name":"t","input_schema":{"type":"object"},"cache_control":{"type":"ephemeral"}}]}`)
	out, _ := Request(in)
	if strings.Contains(encode(t, out), "cache_control") {
		t.Errorf("cache_control survived: %s", encode(t, out))
	}
}

func TestStripsSchemaPatternKeywords(t *testing.T) {
	in := decode(t, `{"tools":[{"name":"t","input_schema":{
		"type":"object",
		"properties":{
			"a":{"type":"string","pattern":"^[a-z]+$"},
			"b":{"type":"object","properties":{"c":{"type":"string","pattern":"x"}}}
		}}}]}`)
	out, _ := Request(in)
	got := encode(t, out)
	if strings.Contains(got, "pattern") {
		t.Errorf("pattern keyword survived at some depth: %s", got)
	}
	if !strings.Contains(got, `"type":"string"`) {
		t.Errorf("stripping removed too much: %s", got)
	}
}

func TestPreservesUnrelatedFields(t *testing.T) {
	in := decode(t, `{"model":"m","max_tokens":100,"stream":true,
		"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	out, notes := Request(in)
	if out["model"] != "m" || out["stream"] != true {
		t.Errorf("top-level fields mangled: %s", encode(t, out))
	}
	if len(notes) != 0 {
		t.Errorf("clean request produced notes: %v", notes)
	}
}

func TestEnsureToolUseIDsFillsMissingID(t *testing.T) {
	content := []any{
		map[string]any{"type": "tool_use", "name": "x", "input": map[string]any{}},
		map[string]any{"type": "tool_use", "id": "keep_me", "name": "y", "input": map[string]any{}},
	}
	n := 0
	out := EnsureToolUseIDs(content, func() string { n++; return "gen_1" })
	first := out[0].(map[string]any)
	second := out[1].(map[string]any)
	if first["id"] != "gen_1" {
		t.Errorf("missing id not filled, got %v", first["id"])
	}
	if second["id"] != "keep_me" {
		t.Errorf("existing id overwritten, got %v", second["id"])
	}
	if n != 1 {
		t.Errorf("generator called %d times, want 1", n)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd airgap-vllm && go test ./internal/filter/`
Expected: FAIL — `undefined: Request`.

- [ ] **Step 3: Write the implementation**

Create `airgap-vllm/internal/filter/filter.go`:

```go
// Package filter removes request shapes that strict, non-Anthropic endpoints
// reject. Each rule corresponds to an observed HTTP 400 or render failure
// against third-party Anthropic-compatible endpoints; see the design doc.
//
// All functions are pure: no network, no clock, no filesystem.
package filter

import "fmt"

// allowedBlockTypes is a whitelist. Anything else is dropped, because a single
// unrecognized content block type fails the entire request on strict servers.
var allowedBlockTypes = map[string]bool{
	"text":              true,
	"image":             true,
	"tool_use":          true,
	"tool_result":       true,
	"thinking":          true,
	"redacted_thinking": true,
	"document":          true,
}

// schemaKeywordsToStrip are JSON Schema keywords that strict validators on the
// server side have rejected outright rather than ignoring.
var schemaKeywordsToStrip = []string{"pattern", "regex"}

// Request returns a filtered copy of an Anthropic Messages request along with
// notes describing anything removed. The input is not mutated.
func Request(req map[string]any) (map[string]any, []string) {
	var notes []string
	out := cloneMap(req)

	if sys, ok := out["system"]; ok {
		out["system"] = filterContent(sys, &notes, "system")
	}

	if msgs, ok := out["messages"].([]any); ok {
		filtered := make([]any, 0, len(msgs))
		for i, raw := range msgs {
			msg, ok := raw.(map[string]any)
			if !ok {
				filtered = append(filtered, raw)
				continue
			}
			m := cloneMap(msg)
			delete(m, "cache_control")
			if c, ok := m["content"]; ok {
				m["content"] = filterContent(c, &notes, fmt.Sprintf("messages[%d]", i))
			}
			filtered = append(filtered, m)
		}
		out["messages"] = filtered
	}

	if tools, ok := out["tools"].([]any); ok {
		filtered := make([]any, 0, len(tools))
		for _, raw := range tools {
			tool, ok := raw.(map[string]any)
			if !ok {
				filtered = append(filtered, raw)
				continue
			}
			tl := cloneMap(tool)
			delete(tl, "cache_control")
			if s, ok := tl["input_schema"]; ok {
				tl["input_schema"] = StripSchemaKeywords(s)
			}
			filtered = append(filtered, tl)
		}
		out["tools"] = filtered
	}

	return out, notes
}

// filterContent handles both the string and block-array forms of content.
func filterContent(content any, notes *[]string, where string) any {
	blocks, ok := content.([]any)
	if !ok {
		return content // plain string form needs no filtering
	}
	kept := make([]any, 0, len(blocks))
	for _, raw := range blocks {
		block, ok := raw.(map[string]any)
		if !ok {
			kept = append(kept, raw)
			continue
		}
		typ, _ := block["type"].(string)
		if !allowedBlockTypes[typ] {
			*notes = append(*notes, fmt.Sprintf("dropped %s content block of type %q", where, typ))
			continue
		}
		b := cloneMap(block)
		delete(b, "cache_control")
		kept = append(kept, b)
	}
	return kept
}

// StripSchemaKeywords recursively removes validator-hostile keywords from a
// JSON Schema while leaving its structure intact.
func StripSchemaKeywords(schema any) any {
	switch s := schema.(type) {
	case map[string]any:
		out := make(map[string]any, len(s))
		for k, v := range s {
			if isStrippedKeyword(k) {
				continue
			}
			out[k] = StripSchemaKeywords(v)
		}
		return out
	case []any:
		out := make([]any, len(s))
		for i, v := range s {
			out[i] = StripSchemaKeywords(v)
		}
		return out
	default:
		return schema
	}
}

func isStrippedKeyword(k string) bool {
	for _, s := range schemaKeywordsToStrip {
		if k == s {
			return true
		}
	}
	return false
}

// EnsureToolUseIDs guarantees every tool_use block carries an id. A tool_use
// streamed without one crashes the Claude Code renderer on the first tool call.
func EnsureToolUseIDs(content []any, gen func() string) []any {
	out := make([]any, len(content))
	for i, raw := range content {
		block, ok := raw.(map[string]any)
		if !ok {
			out[i] = raw
			continue
		}
		if t, _ := block["type"].(string); t == "tool_use" {
			if id, _ := block["id"].(string); id == "" {
				b := cloneMap(block)
				b["id"] = gen()
				out[i] = b
				continue
			}
		}
		out[i] = block
	}
	return out
}

func cloneMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd airgap-vllm && gofmt -w ./... && go test ./internal/filter/ -v`
Expected: PASS, six tests.

- [ ] **Step 5: Commit**

```bash
git add airgap-vllm/internal/filter/
git commit -m "feat(airgap-vllm): compatibility filter for strict endpoints"
```

---

### Task 3: Anthropic → OpenAI request translation

This is the first half of `translate` mode. The subtlety is that Anthropic carries tool results inside a *user* message, while OpenAI wants separate `role: "tool"` messages — one message in becomes several messages out.

**Files:**
- Create: `airgap-vllm/internal/translate/types.go`
- Create: `airgap-vllm/internal/translate/request.go`
- Test: `airgap-vllm/internal/translate/request_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `translate.ToOpenAI(req map[string]any, defaultModel string) (map[string]any, error)`, `translate.MapStopReason(string) string`, and the wire structs `translate.OpenAIResponse`, `translate.OpenAIToolCall`, `translate.Chunk` used by Tasks 4 and 5.

- [ ] **Step 1: Write the failing test**

Create `airgap-vllm/internal/translate/request_test.go`:

```go
package translate

import (
	"encoding/json"
	"testing"
)

func mustJSON(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("bad fixture: %v", err)
	}
	return m
}

func TestSystemBecomesLeadingSystemMessage(t *testing.T) {
	in := mustJSON(t, `{"system":"be terse","messages":[
		{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	out, err := ToOpenAI(in, "fallback")
	if err != nil {
		t.Fatal(err)
	}
	msgs := out["messages"].([]any)
	first := msgs[0].(map[string]any)
	if first["role"] != "system" || first["content"] != "be terse" {
		t.Errorf("first message = %v, want system/be terse", first)
	}
	if out["model"] != "fallback" {
		t.Errorf("model = %v, want fallback when request omits it", out["model"])
	}
}

func TestAssistantToolUseBecomesToolCalls(t *testing.T) {
	in := mustJSON(t, `{"messages":[
		{"role":"assistant","content":[
			{"type":"text","text":"calling"},
			{"type":"tool_use","id":"tu_1","name":"read","input":{"path":"a.txt"}}]}]}`)
	out, err := ToOpenAI(in, "m")
	if err != nil {
		t.Fatal(err)
	}
	msg := out["messages"].([]any)[0].(map[string]any)
	calls := msg["tool_calls"].([]any)
	if len(calls) != 1 {
		t.Fatalf("got %d tool_calls, want 1", len(calls))
	}
	c := calls[0].(map[string]any)
	fn := c["function"].(map[string]any)
	if c["id"] != "tu_1" || fn["name"] != "read" {
		t.Errorf("tool call = %v", c)
	}
	if fn["arguments"] != `{"path":"a.txt"}` {
		t.Errorf("arguments = %v, want serialized JSON string", fn["arguments"])
	}
}

// Anthropic puts tool results in a user message; OpenAI needs role:tool messages.
func TestToolResultBecomesSeparateToolMessage(t *testing.T) {
	in := mustJSON(t, `{"messages":[
		{"role":"user","content":[
			{"type":"tool_result","tool_use_id":"tu_1","content":"file body"}]}]}`)
	out, err := ToOpenAI(in, "m")
	if err != nil {
		t.Fatal(err)
	}
	msgs := out["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1 tool message only", len(msgs))
	}
	m := msgs[0].(map[string]any)
	if m["role"] != "tool" || m["tool_call_id"] != "tu_1" || m["content"] != "file body" {
		t.Errorf("tool message = %v", m)
	}
}

func TestToolsBecomeFunctions(t *testing.T) {
	in := mustJSON(t, `{"tools":[
		{"name":"read","description":"reads","input_schema":{"type":"object"}}]}`)
	out, err := ToOpenAI(in, "m")
	if err != nil {
		t.Fatal(err)
	}
	tl := out["tools"].([]any)[0].(map[string]any)
	fn := tl["function"].(map[string]any)
	if tl["type"] != "function" || fn["name"] != "read" {
		t.Errorf("tool = %v", tl)
	}
	if fn["parameters"] == nil {
		t.Error("input_schema did not become parameters")
	}
}

func TestToolChoiceMapping(t *testing.T) {
	cases := []struct {
		in   string
		want any
	}{
		{`{"type":"auto"}`, "auto"},
		{`{"type":"any"}`, "required"},
		{`{"type":"none"}`, "none"},
	}
	for _, c := range cases {
		req := mustJSON(t, `{"tool_choice":`+c.in+`}`)
		out, err := ToOpenAI(req, "m")
		if err != nil {
			t.Fatal(err)
		}
		if out["tool_choice"] != c.want {
			t.Errorf("tool_choice %s -> %v, want %v", c.in, out["tool_choice"], c.want)
		}
	}
	req := mustJSON(t, `{"tool_choice":{"type":"tool","name":"read"}}`)
	out, _ := ToOpenAI(req, "m")
	tc := out["tool_choice"].(map[string]any)
	if tc["type"] != "function" || tc["function"].(map[string]any)["name"] != "read" {
		t.Errorf("named tool_choice = %v", tc)
	}
}

func TestStopSequencesBecomeStop(t *testing.T) {
	in := mustJSON(t, `{"stop_sequences":["END"],"max_tokens":50,"temperature":0.3,"stream":true}`)
	out, _ := ToOpenAI(in, "m")
	if out["stop"] == nil {
		t.Error("stop_sequences did not map to stop")
	}
	if out["max_tokens"] != float64(50) || out["temperature"] != 0.3 || out["stream"] != true {
		t.Errorf("passthrough params mangled: %v", out)
	}
}

func TestMapStopReason(t *testing.T) {
	cases := map[string]string{
		"stop": "end_turn", "length": "max_tokens",
		"tool_calls": "tool_use", "content_filter": "end_turn", "": "end_turn",
	}
	for in, want := range cases {
		if got := MapStopReason(in); got != want {
			t.Errorf("MapStopReason(%q) = %q, want %q", in, got, want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd airgap-vllm && go test ./internal/translate/`
Expected: FAIL — `undefined: ToOpenAI`.

- [ ] **Step 3: Write the wire types**

Create `airgap-vllm/internal/translate/types.go`:

```go
// Package translate converts between the Anthropic Messages API and the
// OpenAI chat completions API. Every function here is pure: no network, no
// clock, no filesystem, so the conversion can be tested exhaustively offline.
package translate

// OpenAIToolCall is one function call in a non-streaming response.
type OpenAIToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// OpenAIResponse is a non-streaming /v1/chat/completions response.
type OpenAIResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content   string           `json:"content"`
			ToolCalls []OpenAIToolCall `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

// Chunk is a streamed chat.completion.chunk. Tool call arguments arrive as
// fragments across many chunks and must be accumulated by the consumer.
type Chunk struct {
	Choices []struct {
		Delta struct {
			Content   string `json:"content"`
			ToolCalls []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

// MapStopReason converts an OpenAI finish_reason to an Anthropic stop_reason.
func MapStopReason(finish string) string {
	switch finish {
	case "length":
		return "max_tokens"
	case "tool_calls", "function_call":
		return "tool_use"
	default:
		return "end_turn"
	}
}
```

- [ ] **Step 4: Write the request translator**

Create `airgap-vllm/internal/translate/request.go`:

```go
package translate

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ToOpenAI converts an Anthropic Messages request into an OpenAI chat
// completions request. defaultModel is used when the request omits one.
func ToOpenAI(req map[string]any, defaultModel string) (map[string]any, error) {
	out := map[string]any{}

	model := defaultModel
	if m, ok := req["model"].(string); ok && m != "" {
		model = m
	}
	out["model"] = model

	for _, k := range []string{"max_tokens", "temperature", "top_p", "stream"} {
		if v, ok := req[k]; ok {
			out[k] = v
		}
	}
	if v, ok := req["stop_sequences"]; ok {
		out["stop"] = v
	}

	msgs := []any{}
	if sys, ok := req["system"]; ok {
		if s := flattenText(sys); s != "" {
			msgs = append(msgs, map[string]any{"role": "system", "content": s})
		}
	}
	inMsgs, _ := req["messages"].([]any)
	for i, raw := range inMsgs {
		m, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("messages[%d] is not an object", i)
		}
		role, _ := m["role"].(string)
		converted, err := convertMessage(role, m["content"])
		if err != nil {
			return nil, fmt.Errorf("messages[%d]: %w", i, err)
		}
		msgs = append(msgs, converted...)
	}
	out["messages"] = msgs

	if tools, ok := req["tools"].([]any); ok && len(tools) > 0 {
		converted := make([]any, 0, len(tools))
		for _, raw := range tools {
			t, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			fn := map[string]any{"name": t["name"]}
			if d, ok := t["description"]; ok {
				fn["description"] = d
			}
			if s, ok := t["input_schema"]; ok {
				fn["parameters"] = s
			} else {
				fn["parameters"] = map[string]any{"type": "object"}
			}
			converted = append(converted, map[string]any{"type": "function", "function": fn})
		}
		out["tools"] = converted
	}

	if tc, ok := req["tool_choice"]; ok {
		if v := convertToolChoice(tc); v != nil {
			out["tool_choice"] = v
		}
	}
	return out, nil
}

// convertMessage may expand one Anthropic message into several OpenAI ones,
// because tool results ride inside a user message on the Anthropic side but
// need their own role:"tool" messages on the OpenAI side.
func convertMessage(role string, content any) ([]any, error) {
	if s, ok := content.(string); ok {
		return []any{map[string]any{"role": role, "content": s}}, nil
	}
	blocks, ok := content.([]any)
	if !ok {
		return []any{map[string]any{"role": role, "content": ""}}, nil
	}

	var text strings.Builder
	var parts []any
	var toolCalls []any
	var toolResults []any
	hasImage := false

	for _, raw := range blocks {
		b, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		switch t, _ := b["type"].(string); t {
		case "text":
			s, _ := b["text"].(string)
			text.WriteString(s)
			parts = append(parts, map[string]any{"type": "text", "text": s})
		case "image":
			if u := imageDataURL(b); u != "" {
				hasImage = true
				parts = append(parts, map[string]any{
					"type": "image_url", "image_url": map[string]any{"url": u},
				})
			}
		case "tool_use":
			id, _ := b["id"].(string)
			name, _ := b["name"].(string)
			args, err := json.Marshal(b["input"])
			if err != nil {
				args = []byte("{}")
			}
			call := map[string]any{"id": id, "type": "function"}
			call["function"] = map[string]any{"name": name, "arguments": string(args)}
			toolCalls = append(toolCalls, call)
		case "tool_result":
			id, _ := b["tool_use_id"].(string)
			toolResults = append(toolResults, map[string]any{
				"role": "tool", "tool_call_id": id, "content": flattenText(b["content"]),
			})
		case "thinking", "redacted_thinking":
			// No OpenAI equivalent on input. Dropped deliberately.
		}
	}

	out := make([]any, 0, len(toolResults)+1)
	out = append(out, toolResults...)

	// A user turn that carried nothing but tool results adds no further message.
	if len(toolResults) > 0 && text.Len() == 0 && len(toolCalls) == 0 && !hasImage {
		return out, nil
	}

	msg := map[string]any{"role": role}
	if hasImage {
		msg["content"] = parts
	} else {
		msg["content"] = text.String()
	}
	if len(toolCalls) > 0 {
		msg["tool_calls"] = toolCalls
	}
	return append(out, msg), nil
}

func convertToolChoice(tc any) any {
	m, ok := tc.(map[string]any)
	if !ok {
		return nil
	}
	switch t, _ := m["type"].(string); t {
	case "auto":
		return "auto"
	case "any":
		return "required"
	case "none":
		return "none"
	case "tool":
		return map[string]any{
			"type":     "function",
			"function": map[string]any{"name": m["name"]},
		}
	}
	return nil
}

// flattenText reduces a string or a block array to plain text.
func flattenText(v any) string {
	switch c := v.(type) {
	case string:
		return c
	case []any:
		var b strings.Builder
		for _, raw := range c {
			if m, ok := raw.(map[string]any); ok {
				if s, ok := m["text"].(string); ok {
					b.WriteString(s)
				}
			}
		}
		return b.String()
	}
	return ""
}

func imageDataURL(block map[string]any) string {
	src, ok := block["source"].(map[string]any)
	if !ok {
		return ""
	}
	switch t, _ := src["type"].(string); t {
	case "base64":
		media, _ := src["media_type"].(string)
		data, _ := src["data"].(string)
		if media == "" || data == "" {
			return ""
		}
		return "data:" + media + ";base64," + data
	case "url":
		u, _ := src["url"].(string)
		return u
	}
	return ""
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd airgap-vllm && gofmt -w ./... && go test ./internal/translate/ -v`
Expected: PASS, seven tests.

- [ ] **Step 6: Commit**

```bash
git add airgap-vllm/internal/translate/
git commit -m "feat(airgap-vllm): Anthropic to OpenAI request translation"
```

---

### Task 4: OpenAI → Anthropic non-streaming response translation

**Files:**
- Create: `airgap-vllm/internal/translate/response.go`
- Test: `airgap-vllm/internal/translate/response_test.go`

**Interfaces:**
- Consumes: `OpenAIResponse`, `MapStopReason` from Task 3.
- Produces: `translate.FromOpenAI(r OpenAIResponse, fallbackModel string, genID func() string) map[string]any`.

- [ ] **Step 1: Write the failing test**

Create `airgap-vllm/internal/translate/response_test.go`:

```go
package translate

import (
	"encoding/json"
	"testing"
)

func decodeResp(t *testing.T, s string) OpenAIResponse {
	t.Helper()
	var r OpenAIResponse
	if err := json.Unmarshal([]byte(s), &r); err != nil {
		t.Fatalf("bad fixture: %v", err)
	}
	return r
}

func TestTextResponseShape(t *testing.T) {
	r := decodeResp(t, `{"id":"c1","model":"m","choices":[
		{"message":{"content":"hello"},"finish_reason":"stop"}],
		"usage":{"prompt_tokens":7,"completion_tokens":3}}`)
	got := FromOpenAI(r, "fallback", func() string { return "gen" })

	if got["type"] != "message" || got["role"] != "assistant" {
		t.Errorf("envelope = %v", got)
	}
	content := got["content"].([]any)
	block := content[0].(map[string]any)
	if block["type"] != "text" || block["text"] != "hello" {
		t.Errorf("content block = %v", block)
	}
	if got["stop_reason"] != "end_turn" {
		t.Errorf("stop_reason = %v", got["stop_reason"])
	}
	usage := got["usage"].(map[string]any)
	if usage["input_tokens"] != 7 || usage["output_tokens"] != 3 {
		t.Errorf("usage = %v", usage)
	}
}

func TestToolCallBecomesToolUseBlockWithParsedInput(t *testing.T) {
	r := decodeResp(t, `{"id":"c1","model":"m","choices":[
		{"message":{"content":"","tool_calls":[
			{"id":"call_1","type":"function","function":{"name":"read","arguments":"{\"path\":\"a.txt\"}"}}]},
		 "finish_reason":"tool_calls"}]}`)
	got := FromOpenAI(r, "m", func() string { return "gen" })

	content := got["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("got %d blocks, want 1 (empty text must not emit a block)", len(content))
	}
	b := content[0].(map[string]any)
	if b["type"] != "tool_use" || b["id"] != "call_1" || b["name"] != "read" {
		t.Errorf("tool_use block = %v", b)
	}
	input := b["input"].(map[string]any)
	if input["path"] != "a.txt" {
		t.Errorf("arguments were not parsed into input: %v", input)
	}
	if got["stop_reason"] != "tool_use" {
		t.Errorf("stop_reason = %v, want tool_use", got["stop_reason"])
	}
}

// A tool_use block without an id crashes the Claude Code renderer.
func TestMissingToolCallIDIsGenerated(t *testing.T) {
	r := decodeResp(t, `{"choices":[{"message":{"tool_calls":[
		{"type":"function","function":{"name":"x","arguments":"{}"}}]},
		"finish_reason":"tool_calls"}]}`)
	got := FromOpenAI(r, "m", func() string { return "toolu_generated" })
	b := got["content"].([]any)[0].(map[string]any)
	if b["id"] != "toolu_generated" {
		t.Errorf("id = %v, want generated", b["id"])
	}
}

func TestUnparseableArgumentsDoNotPanic(t *testing.T) {
	r := decodeResp(t, `{"choices":[{"message":{"tool_calls":[
		{"id":"c","type":"function","function":{"name":"x","arguments":"not json"}}]},
		"finish_reason":"tool_calls"}]}`)
	got := FromOpenAI(r, "m", func() string { return "gen" })
	b := got["content"].([]any)[0].(map[string]any)
	if _, ok := b["input"].(map[string]any); !ok {
		t.Errorf("input should fall back to an empty object, got %v", b["input"])
	}
}

func TestEmptyChoicesYieldsEmptyContent(t *testing.T) {
	got := FromOpenAI(OpenAIResponse{}, "m", func() string { return "gen" })
	if len(got["content"].([]any)) != 0 {
		t.Errorf("content = %v, want empty", got["content"])
	}
	if got["stop_reason"] != "end_turn" {
		t.Errorf("stop_reason = %v", got["stop_reason"])
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd airgap-vllm && go test ./internal/translate/ -run FromOpenAI`
Expected: FAIL — `undefined: FromOpenAI`.

- [ ] **Step 3: Write the implementation**

Create `airgap-vllm/internal/translate/response.go`:

```go
package translate

import "encoding/json"

// FromOpenAI converts a non-streaming chat completion into an Anthropic
// Messages response. genID supplies an id for any tool call that arrives
// without one, which strict clients require.
func FromOpenAI(r OpenAIResponse, fallbackModel string, genID func() string) map[string]any {
	model := r.Model
	if model == "" {
		model = fallbackModel
	}
	id := r.ID
	if id == "" {
		id = genID()
	}

	content := []any{}
	stop := "end_turn"

	if len(r.Choices) > 0 {
		ch := r.Choices[0]
		if ch.Message.Content != "" {
			content = append(content, map[string]any{
				"type": "text", "text": ch.Message.Content,
			})
		}
		for _, tc := range ch.Message.ToolCalls {
			callID := tc.ID
			if callID == "" {
				callID = genID()
			}
			input := map[string]any{}
			if tc.Function.Arguments != "" {
				var parsed map[string]any
				if err := json.Unmarshal([]byte(tc.Function.Arguments), &parsed); err == nil {
					input = parsed
				}
			}
			content = append(content, map[string]any{
				"type": "tool_use", "id": callID, "name": tc.Function.Name, "input": input,
			})
		}
		stop = MapStopReason(ch.FinishReason)
		// Some servers report "stop" even when they emitted tool calls.
		if stop == "end_turn" && len(ch.Message.ToolCalls) > 0 {
			stop = "tool_use"
		}
	}

	return map[string]any{
		"id":            id,
		"type":          "message",
		"role":          "assistant",
		"model":         model,
		"content":       content,
		"stop_reason":   stop,
		"stop_sequence": nil,
		"usage": map[string]any{
			"input_tokens":  r.Usage.PromptTokens,
			"output_tokens": r.Usage.CompletionTokens,
		},
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd airgap-vllm && gofmt -w ./... && go test ./internal/translate/ -v`
Expected: PASS, twelve tests total in the package.

- [ ] **Step 5: Commit**

```bash
git add airgap-vllm/internal/translate/
git commit -m "feat(airgap-vllm): OpenAI to Anthropic response translation"
```

---

### Task 5: Streaming translation state machine

The highest-risk unit in the project. OpenAI streams flat deltas; Anthropic requires properly nested, correctly indexed, explicitly opened and closed content blocks.

**Files:**
- Create: `airgap-vllm/internal/translate/stream.go`
- Test: `airgap-vllm/internal/translate/stream_test.go`

**Interfaces:**
- Consumes: `Chunk`, `MapStopReason` from Task 3.
- Produces: `translate.EventWriter` interface with method `Event(name string, data any) error`; `translate.NewStreamTranslator(w EventWriter, messageID, model string, genID func() string) *StreamTranslator`; methods `Start() error`, `Chunk(Chunk) error`, `Finish() error`.

- [ ] **Step 1: Write the failing test**

Create `airgap-vllm/internal/translate/stream_test.go`:

```go
package translate

import (
	"encoding/json"
	"strings"
	"testing"
)

type recorder struct{ events []string }

func (r *recorder) Event(name string, data any) error {
	b, _ := json.Marshal(data)
	r.events = append(r.events, name+" "+string(b))
	return nil
}

func (r *recorder) names() []string {
	out := make([]string, len(r.events))
	for i, e := range r.events {
		out[i] = strings.SplitN(e, " ", 2)[0]
	}
	return out
}

func eq(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("event sequence\n got %v\nwant %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("event %d = %q, want %q\nfull: %v", i, got[i], want[i], got)
		}
	}
}

func chunkText(s string) Chunk {
	var c Chunk
	json.Unmarshal([]byte(`{"choices":[{"delta":{"content":`+strconv.Quote(s)+`}}]}`), &c)
	return c
}

func TestTextOnlyStreamSequence(t *testing.T) {
	r := &recorder{}
	tr := NewStreamTranslator(r, "msg_1", "m", func() string { return "gen" })
	if err := tr.Start(); err != nil {
		t.Fatal(err)
	}
	tr.Chunk(chunkText("Hel"))
	tr.Chunk(chunkText("lo"))
	tr.Finish()

	eq(t, r.names(), []string{
		"message_start",
		"content_block_start", "content_block_delta", "content_block_delta",
		"content_block_stop",
		"message_delta", "message_stop",
	})
}

func TestToolCallStreamOpensSecondBlockAndClosesText(t *testing.T) {
	r := &recorder{}
	tr := NewStreamTranslator(r, "msg_1", "m", func() string { return "gen" })
	tr.Start()
	tr.Chunk(chunkText("thinking"))

	var first, rest, final Chunk
	json.Unmarshal([]byte(`{"choices":[{"delta":{"tool_calls":[
		{"index":0,"id":"call_1","function":{"name":"read","arguments":"{\"pa"}}]}}]}`), &first)
	json.Unmarshal([]byte(`{"choices":[{"delta":{"tool_calls":[
		{"index":0,"function":{"arguments":"th\":\"a\"}"}}]}}]}`), &rest)
	json.Unmarshal([]byte(`{"choices":[{"delta":{},"finish_reason":"tool_calls"}],
		"usage":{"prompt_tokens":11,"completion_tokens":4}}`), &final)

	tr.Chunk(first)
	tr.Chunk(rest)
	tr.Chunk(final)
	tr.Finish()

	eq(t, r.names(), []string{
		"message_start",
		"content_block_start", "content_block_delta", // text
		"content_block_stop", // text closed before the tool block opens
		"content_block_start", "content_block_delta", "content_block_delta",
		"content_block_stop",
		"message_delta", "message_stop",
	})

	joined := strings.Join(r.events, "\n")
	if !strings.Contains(joined, `"type":"input_json_delta"`) {
		t.Error("tool arguments were not emitted as input_json_delta")
	}
	if !strings.Contains(joined, `"id":"call_1"`) {
		t.Error("tool_use id missing from content_block_start")
	}
	if !strings.Contains(joined, `"stop_reason":"tool_use"`) {
		t.Error("stop_reason should be tool_use")
	}
	if !strings.Contains(joined, `"output_tokens":4`) {
		t.Error("final usage not carried into message_delta")
	}
}

func TestToolCallWithoutIDGetsGeneratedOne(t *testing.T) {
	r := &recorder{}
	tr := NewStreamTranslator(r, "msg_1", "m", func() string { return "toolu_gen" })
	tr.Start()
	var c Chunk
	json.Unmarshal([]byte(`{"choices":[{"delta":{"tool_calls":[
		{"index":0,"function":{"name":"x","arguments":"{}"}}]}}]}`), &c)
	tr.Chunk(c)
	tr.Finish()
	if !strings.Contains(strings.Join(r.events, "\n"), "toolu_gen") {
		t.Error("missing tool call id was not generated")
	}
}

// Servers sometimes report finish_reason "stop" despite emitting tool calls.
func TestStopReasonUpgradedWhenToolBlocksExist(t *testing.T) {
	r := &recorder{}
	tr := NewStreamTranslator(r, "msg_1", "m", func() string { return "gen" })
	tr.Start()
	var c, fin Chunk
	json.Unmarshal([]byte(`{"choices":[{"delta":{"tool_calls":[
		{"index":0,"id":"c1","function":{"name":"x","arguments":"{}"}}]}}]}`), &c)
	json.Unmarshal([]byte(`{"choices":[{"delta":{},"finish_reason":"stop"}]}`), &fin)
	tr.Chunk(c)
	tr.Chunk(fin)
	tr.Finish()
	if !strings.Contains(strings.Join(r.events, "\n"), `"stop_reason":"tool_use"`) {
		t.Error("stop_reason should be upgraded to tool_use")
	}
}

func TestFinishIsIdempotent(t *testing.T) {
	r := &recorder{}
	tr := NewStreamTranslator(r, "msg_1", "m", func() string { return "gen" })
	tr.Start()
	tr.Finish()
	n := len(r.events)
	tr.Finish()
	if len(r.events) != n {
		t.Errorf("second Finish emitted %d extra events", len(r.events)-n)
	}
}
```

Add `"strconv"` to the test file's imports.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd airgap-vllm && go test ./internal/translate/ -run Stream`
Expected: FAIL — `undefined: NewStreamTranslator`.

- [ ] **Step 3: Write the implementation**

Create `airgap-vllm/internal/translate/stream.go`:

```go
package translate

import "sort"

// EventWriter receives named SSE events. The server implements it against an
// http.ResponseWriter; tests implement it as a recorder.
type EventWriter interface {
	Event(name string, data any) error
}

// StreamTranslator converts a sequence of OpenAI chunks into a well-formed
// Anthropic event stream. Anthropic requires content blocks to be explicitly
// opened, indexed and closed in order, while OpenAI emits flat deltas, so this
// type carries the block bookkeeping.
//
// Call Start once, Chunk per received chunk, then Finish exactly once.
type StreamTranslator struct {
	w         EventWriter
	messageID string
	model     string
	genID     func() string

	started      bool
	closed       bool
	textOpen     bool
	textIndex    int
	nextIndex    int
	toolBlocks   map[int]int // OpenAI tool_call index -> Anthropic block index
	inputTokens  int
	outputTokens int
	stopReason   string
}

func NewStreamTranslator(w EventWriter, messageID, model string, genID func() string) *StreamTranslator {
	return &StreamTranslator{
		w: w, messageID: messageID, model: model, genID: genID,
		toolBlocks: map[int]int{},
	}
}

func (t *StreamTranslator) Start() error {
	if t.started {
		return nil
	}
	t.started = true
	return t.w.Event("message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id": t.messageID, "type": "message", "role": "assistant",
			"model": t.model, "content": []any{},
			"stop_reason": nil, "stop_sequence": nil,
			"usage": map[string]any{"input_tokens": t.inputTokens, "output_tokens": 0},
		},
	})
}

func (t *StreamTranslator) Chunk(c Chunk) error {
	if err := t.Start(); err != nil {
		return err
	}
	if c.Usage != nil {
		if c.Usage.PromptTokens > 0 {
			t.inputTokens = c.Usage.PromptTokens
		}
		if c.Usage.CompletionTokens > 0 {
			t.outputTokens = c.Usage.CompletionTokens
		}
	}
	if len(c.Choices) == 0 {
		return nil
	}
	ch := c.Choices[0]

	if ch.Delta.Content != "" {
		if !t.textOpen {
			t.textIndex = t.nextIndex
			t.nextIndex++
			t.textOpen = true
			if err := t.w.Event("content_block_start", map[string]any{
				"type": "content_block_start", "index": t.textIndex,
				"content_block": map[string]any{"type": "text", "text": ""},
			}); err != nil {
				return err
			}
		}
		if err := t.w.Event("content_block_delta", map[string]any{
			"type": "content_block_delta", "index": t.textIndex,
			"delta": map[string]any{"type": "text_delta", "text": ch.Delta.Content},
		}); err != nil {
			return err
		}
	}

	for _, tc := range ch.Delta.ToolCalls {
		idx, known := t.toolBlocks[tc.Index]
		if !known {
			// Anthropic blocks nest strictly; close text before opening a tool.
			if err := t.closeText(); err != nil {
				return err
			}
			idx = t.nextIndex
			t.nextIndex++
			t.toolBlocks[tc.Index] = idx
			id := tc.ID
			if id == "" {
				id = t.genID()
			}
			if err := t.w.Event("content_block_start", map[string]any{
				"type": "content_block_start", "index": idx,
				"content_block": map[string]any{
					"type": "tool_use", "id": id,
					"name": tc.Function.Name, "input": map[string]any{},
				},
			}); err != nil {
				return err
			}
		}
		if tc.Function.Arguments != "" {
			if err := t.w.Event("content_block_delta", map[string]any{
				"type": "content_block_delta", "index": idx,
				"delta": map[string]any{
					"type": "input_json_delta", "partial_json": tc.Function.Arguments,
				},
			}); err != nil {
				return err
			}
		}
	}

	if ch.FinishReason != "" {
		t.stopReason = MapStopReason(ch.FinishReason)
	}
	return nil
}

func (t *StreamTranslator) closeText() error {
	if !t.textOpen {
		return nil
	}
	t.textOpen = false
	return t.w.Event("content_block_stop", map[string]any{
		"type": "content_block_stop", "index": t.textIndex,
	})
}

// Finish closes every open block and terminates the message. It is safe to
// call more than once so that both the normal path and a deferred cleanup can
// invoke it.
func (t *StreamTranslator) Finish() error {
	if err := t.Start(); err != nil {
		return err
	}
	if t.closed {
		return nil
	}
	t.closed = true

	if err := t.closeText(); err != nil {
		return err
	}
	indices := make([]int, 0, len(t.toolBlocks))
	for _, v := range t.toolBlocks {
		indices = append(indices, v)
	}
	sort.Ints(indices)
	for _, i := range indices {
		if err := t.w.Event("content_block_stop", map[string]any{
			"type": "content_block_stop", "index": i,
		}); err != nil {
			return err
		}
	}

	stop := t.stopReason
	if stop == "" {
		stop = "end_turn"
	}
	if stop == "end_turn" && len(t.toolBlocks) > 0 {
		stop = "tool_use"
	}
	if err := t.w.Event("message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": stop, "stop_sequence": nil},
		"usage": map[string]any{"output_tokens": t.outputTokens},
	}); err != nil {
		return err
	}
	return t.w.Event("message_stop", map[string]any{"type": "message_stop"})
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd airgap-vllm && gofmt -w ./... && go test ./internal/translate/ -v`
Expected: PASS, seventeen tests total.

- [ ] **Step 5: Commit**

```bash
git add airgap-vllm/internal/translate/
git commit -m "feat(airgap-vllm): OpenAI to Anthropic streaming state machine"
```

---

### Task 6: vLLM test double

Test infrastructure consumed by Tasks 7, 8 and 9. Built as its own task because three later tasks depend on its interface.

**Files:**
- Create: `airgap-vllm/internal/fakevllm/fakevllm.go`
- Test: `airgap-vllm/internal/fakevllm/fakevllm_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `fakevllm.Config{MessagesAPI bool; Models []string; ChatCompletion string; StreamChunks []string; MessagesBody string; TokenCount int; HasTokenize bool}`, `fakevllm.New(Config) *Server`, `(*Server).URL() string`, `(*Server).Close()`, `(*Server).Requests() []Recorded`, `fakevllm.Recorded{Method, Path, Body string}`.

- [ ] **Step 1: Write the failing test**

Create `airgap-vllm/internal/fakevllm/fakevllm_test.go`:

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd airgap-vllm && go test ./internal/fakevllm/`
Expected: FAIL — `undefined: New`.

- [ ] **Step 3: Write the implementation**

Create `airgap-vllm/internal/fakevllm/fakevllm.go`:

```go
// Package fakevllm is an in-process stand-in for a vLLM server, used by tests
// that need to exercise probing, mode selection and streaming without a model.
package fakevllm

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
)

type Config struct {
	// MessagesAPI reports whether this build exposes the native Anthropic
	// endpoint. When false, /v1/messages answers 404 as an older vLLM does.
	MessagesAPI bool
	Models      []string
	// ChatCompletion is the raw body returned by /v1/chat/completions when the
	// request is not streaming.
	ChatCompletion string
	// StreamChunks are raw SSE `data:` payloads returned when stream is true.
	// A terminating [DONE] is appended automatically.
	StreamChunks []string
	// MessagesBody is the raw body returned by a native /v1/messages call.
	MessagesBody string
	HasTokenize  bool
	TokenCount   int
}

type Recorded struct {
	Method string
	Path   string
	Body   string
}

type Server struct {
	http *httptest.Server
	cfg  Config

	mu       sync.Mutex
	requests []Recorded
}

func New(cfg Config) *Server {
	s := &Server{cfg: cfg}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", s.record(s.handleModels))
	mux.HandleFunc("/v1/chat/completions", s.record(s.handleChat))
	mux.HandleFunc("/v1/messages", s.record(s.handleMessages))
	mux.HandleFunc("/tokenize", s.record(s.handleTokenize))
	s.http = httptest.NewServer(mux)
	return s
}

func (s *Server) URL() string { return s.http.URL }
func (s *Server) Close()      { s.http.Close() }

func (s *Server) Requests() []Recorded {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Recorded, len(s.requests))
	copy(out, s.requests)
	return out
}

func (s *Server) record(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.requests = append(s.requests, Recorded{r.Method, r.URL.Path, string(body)})
		s.mu.Unlock()
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		next(w, r)
	}
}

func (s *Server) handleModels(w http.ResponseWriter, _ *http.Request) {
	data := make([]map[string]any, 0, len(s.cfg.Models))
	for _, m := range s.cfg.Models {
		data = append(data, map[string]any{"id": m, "object": "model"})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var req struct {
		Stream bool `json:"stream"`
	}
	json.Unmarshal(body, &req)

	if req.Stream {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		for _, c := range s.cfg.StreamChunks {
			fmt.Fprintf(w, "data: %s\n\n", c)
			if flusher != nil {
				flusher.Flush()
			}
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, s.cfg.ChatCompletion)
}

func (s *Server) handleMessages(w http.ResponseWriter, _ *http.Request) {
	if !s.cfg.MessagesAPI {
		http.NotFound(w, nil)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	body := s.cfg.MessagesBody
	if body == "" {
		body = `{"id":"msg_1","type":"message","role":"assistant","content":[]}`
	}
	io.WriteString(w, body)
}

func (s *Server) handleTokenize(w http.ResponseWriter, _ *http.Request) {
	if !s.cfg.HasTokenize {
		http.NotFound(w, nil)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"count": s.cfg.TokenCount})
}
```

Note: `http.NotFound(w, nil)` panics on a nil request in some Go versions; use `w.WriteHeader(http.StatusNotFound)` instead if the tests report it.

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd airgap-vllm && gofmt -w ./... && go test ./internal/fakevllm/ -v`
Expected: PASS, three tests.

- [ ] **Step 5: Commit**

```bash
git add airgap-vllm/internal/fakevllm/
git commit -m "test(airgap-vllm): in-process vLLM test double"
```

---

### Task 7: Capability probe

**Files:**
- Create: `airgap-vllm/internal/probe/probe.go`
- Test: `airgap-vllm/internal/probe/probe_test.go`

**Interfaces:**
- Consumes: `fakevllm` from Task 6.
- Produces: `probe.Capabilities{MessagesAPI bool; Tokenize bool; Models []string}`, `probe.Detect(ctx context.Context, client *http.Client, baseURL, apiKey string) (Capabilities, error)`, `(Capabilities).HasModel(string) bool`, `probe.ResolveMode(configured string, caps Capabilities) string`.

- [ ] **Step 1: Write the failing test**

Create `airgap-vllm/internal/probe/probe_test.go`:

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd airgap-vllm && go test ./internal/probe/`
Expected: FAIL — `undefined: Detect`.

- [ ] **Step 3: Write the implementation**

Create `airgap-vllm/internal/probe/probe.go`:

```go
// Package probe detects what a vLLM deployment supports, so the bridge can
// choose passthrough or translation without the operator having to know their
// server's version.
package probe

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

type Capabilities struct {
	MessagesAPI bool
	Tokenize    bool
	Models      []string
}

func (c Capabilities) HasModel(id string) bool {
	for _, m := range c.Models {
		if m == id {
			return true
		}
	}
	return false
}

// ResolveMode turns the configured mode plus detected capabilities into the
// lane actually used. An explicit mode always wins over detection.
func ResolveMode(configured string, caps Capabilities) string {
	if configured == "messages" || configured == "openai" {
		return configured
	}
	if caps.MessagesAPI {
		return "messages"
	}
	return "openai"
}

// Detect queries the server. Reaching /v1/models is treated as the liveness
// check: if that fails, the server is considered unreachable and the error
// names the URL so the operator can correct it.
func Detect(ctx context.Context, client *http.Client, baseURL, apiKey string) (Capabilities, error) {
	base := strings.TrimRight(baseURL, "/")
	var caps Capabilities

	models, err := fetchModels(ctx, client, base, apiKey)
	if err != nil {
		return caps, fmt.Errorf("cannot reach vLLM at %s: %w", base, err)
	}
	caps.Models = models
	caps.MessagesAPI = endpointExists(ctx, client, base+"/v1/messages", apiKey,
		`{"model":"probe","max_tokens":1,"messages":[{"role":"user","content":"x"}]}`)
	caps.Tokenize = endpointExists(ctx, client, base+"/tokenize", apiKey,
		`{"prompt":"x"}`)
	return caps, nil
}

func fetchModels(ctx context.Context, client *http.Client, base, apiKey string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/models", nil)
	if err != nil {
		return nil, err
	}
	auth(req, apiKey)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("/v1/models returned HTTP %d", resp.StatusCode)
	}
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("parsing /v1/models: %w", err)
	}
	out := make([]string, 0, len(body.Data))
	for _, m := range body.Data {
		out = append(out, m.ID)
	}
	return out, nil
}

// endpointExists distinguishes "this route is not implemented" from "this route
// exists and rejected my probe payload". Only the former means absent.
func endpointExists(ctx context.Context, client *http.Client, url, apiKey, payload string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(payload))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	auth(req, apiKey)
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusNotImplemented:
		return false
	}
	return true
}

func auth(req *http.Request, apiKey string) {
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd airgap-vllm && gofmt -w ./... && go test ./internal/probe/ -v`
Expected: PASS, four tests.

- [ ] **Step 5: Commit**

```bash
git add airgap-vllm/internal/probe/
git commit -m "feat(airgap-vllm): vLLM capability probe and mode resolution"
```

---

### Task 8: Bridge server — non-streaming paths

**Files:**
- Create: `airgap-vllm/internal/server/logger.go`
- Create: `airgap-vllm/internal/server/server.go`
- Modify: `airgap-vllm/internal/translate/request.go` (add one exported helper)
- Test: `airgap-vllm/internal/server/server_test.go`

**Interfaces:**
- Consumes: `config.Config`, `probe.Capabilities`, `probe.ResolveMode`, `filter.Request`, `filter.EnsureToolUseIDs`, `translate.ToOpenAI`, `translate.FromOpenAI`, `translate.OpenAIResponse`, `fakevllm`.
- Produces: `server.New(cfg config.Config, caps probe.Capabilities, lg *server.Logger) *server.Server`, `(*Server).Mode() string`, `(*Server).Handler() http.Handler`, `server.NewLogger(mode, dir string) (*Logger, error)`, `server.GenID(prefix string) string`. Also adds `translate.FlattenRequestText(req map[string]any) string`.

- [ ] **Step 1: Add the text-flattening helper**

Append to `airgap-vllm/internal/translate/request.go`:

```go
// FlattenRequestText concatenates every text fragment in a Messages request.
// Used for token counting when the server exposes no tokenizer.
func FlattenRequestText(req map[string]any) string {
	var b strings.Builder
	if sys, ok := req["system"]; ok {
		b.WriteString(flattenText(sys))
	}
	msgs, _ := req["messages"].([]any)
	for _, raw := range msgs {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		b.WriteString(flattenText(m["content"]))
	}
	return b.String()
}
```

- [ ] **Step 2: Write the failing test**

Create `airgap-vllm/internal/server/server_test.go`:

```go
package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"claude-vllm/internal/config"
	"claude-vllm/internal/fakevllm"
	"claude-vllm/internal/probe"
)

func newTestServer(t *testing.T, fake *fakevllm.Server, mode string) http.Handler {
	t.Helper()
	cfg := config.Default()
	cfg.VLLMURL = fake.URL()
	cfg.Model = "m"
	cfg.Mode = mode
	caps, err := probe.Detect(context.Background(), http.DefaultClient, fake.URL(), "")
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	lg, err := NewLogger("off", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return New(cfg, caps, lg).Handler()
}

func post(t *testing.T, h http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func TestHealthReportsMode(t *testing.T) {
	fake := fakevllm.New(fakevllm.Config{Models: []string{"m"}})
	defer fake.Close()
	h := newTestServer(t, fake, "auto")

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"mode":"openai"`) {
		t.Errorf("health = %d %s, want mode openai (fake has no /v1/messages)", rr.Code, rr.Body)
	}
}

func TestModelsListIsAnthropicShaped(t *testing.T) {
	fake := fakevllm.New(fakevllm.Config{Models: []string{"Qwen/Qwen3.8-27B"}})
	defer fake.Close()
	h := newTestServer(t, fake, "auto")

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	var body struct {
		Data []struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("bad body %s: %v", rr.Body, err)
	}
	if len(body.Data) != 1 || body.Data[0].ID != "Qwen/Qwen3.8-27B" || body.Data[0].Type != "model" {
		t.Errorf("models = %+v", body.Data)
	}
}

func TestOpenAIModeTranslatesEndToEnd(t *testing.T) {
	fake := fakevllm.New(fakevllm.Config{
		Models: []string{"m"},
		ChatCompletion: `{"id":"c1","model":"m","choices":[
			{"message":{"content":"hi there"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":5,"completion_tokens":2}}`,
	})
	defer fake.Close()
	h := newTestServer(t, fake, "openai")

	rr := post(t, h, "/v1/messages", `{"model":"m","max_tokens":10,
		"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	if rr.Code != 200 {
		t.Fatalf("status %d body %s", rr.Code, rr.Body)
	}
	var got map[string]any
	json.Unmarshal(rr.Body.Bytes(), &got)
	block := got["content"].([]any)[0].(map[string]any)
	if block["text"] != "hi there" {
		t.Errorf("content = %v", got["content"])
	}

	// The upstream must have received an OpenAI-shaped request.
	upstream := fake.Requests()
	last := upstream[len(upstream)-1]
	if last.Path != "/v1/chat/completions" {
		t.Errorf("called %s, want /v1/chat/completions", last.Path)
	}
}

// The advisor block caused HTTP 400 against real proxied endpoints.
func TestUnknownBlocksAreFilteredBeforeUpstream(t *testing.T) {
	fake := fakevllm.New(fakevllm.Config{
		Models:         []string{"m"},
		ChatCompletion: `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`,
	})
	defer fake.Close()
	h := newTestServer(t, fake, "openai")

	post(t, h, "/v1/messages", `{"model":"m","messages":[{"role":"user","content":[
		{"type":"text","text":"hi"},{"type":"advisor_20260301","payload":"x"}]}]}`)

	for _, r := range fake.Requests() {
		if strings.Contains(r.Body, "advisor_20260301") {
			t.Errorf("unknown block reached upstream: %s", r.Body)
		}
	}
}

func TestCountTokensUsesTokenizeWhenAvailable(t *testing.T) {
	fake := fakevllm.New(fakevllm.Config{Models: []string{"m"}, HasTokenize: true, TokenCount: 42})
	defer fake.Close()
	h := newTestServer(t, fake, "openai")

	rr := post(t, h, "/v1/messages/count_tokens",
		`{"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`)
	if !strings.Contains(rr.Body.String(), "42") {
		t.Errorf("count_tokens = %s, want 42 from /tokenize", rr.Body)
	}
}

func TestCountTokensFallsBackToEstimate(t *testing.T) {
	fake := fakevllm.New(fakevllm.Config{Models: []string{"m"}, HasTokenize: false})
	defer fake.Close()
	h := newTestServer(t, fake, "openai")

	rr := post(t, h, "/v1/messages/count_tokens",
		`{"messages":[{"role":"user","content":[{"type":"text","text":"12345678"}]}]}`)
	var got struct {
		InputTokens int `json:"input_tokens"`
	}
	json.Unmarshal(rr.Body.Bytes(), &got)
	if got.InputTokens < 1 {
		t.Errorf("estimate = %d, want a positive fallback", got.InputTokens)
	}
}

func TestUpstreamErrorIsReportedNotSwallowed(t *testing.T) {
	fake := fakevllm.New(fakevllm.Config{Models: []string{"m"}, ChatCompletion: ``})
	fake.Close() // force a connection failure
	cfg := config.Default()
	cfg.VLLMURL = fake.URL()
	cfg.Model = "m"
	cfg.Mode = "openai"
	lg, _ := NewLogger("off", t.TempDir())
	h := New(cfg, probe.Capabilities{}, lg).Handler()

	rr := post(t, h, "/v1/messages", `{"messages":[]}`)
	if rr.Code < 500 {
		t.Errorf("status = %d, want 5xx when upstream is down", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "error") {
		t.Errorf("body = %s, want an Anthropic-shaped error", rr.Body)
	}
}

var _ = io.Discard
```

- [ ] **Step 3: Run test to verify it fails**

Run: `cd airgap-vllm && go test ./internal/server/`
Expected: FAIL — `undefined: New`, `undefined: NewLogger`.

- [ ] **Step 4: Write the logger**

Create `airgap-vllm/internal/server/logger.go`:

```go
package server

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Logger writes bridge diagnostics to disk. On an air-gapped machine this is
// the only debugging affordance available, so it records enough to explain a
// rejected request.
//
// Mode "full" writes complete request and response bodies, which contain
// prompts and source code. The operator documentation must say so.
type Logger struct {
	mode string
	dir  string
	mu   sync.Mutex
	f    *os.File
}

func NewLogger(mode, dir string) (*Logger, error) {
	l := &Logger{mode: mode, dir: dir}
	if mode == "off" {
		return l, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("creating log directory: %w", err)
	}
	name := filepath.Join(dir, time.Now().Format("2006-01-02")+".log")
	f, err := os.OpenFile(name, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening log file: %w", err)
	}
	l.f = f
	return l, nil
}

func (l *Logger) Note(format string, args ...any) {
	l.write(fmt.Sprintf(format, args...))
}

// Body records a payload only when logging is set to full.
func (l *Logger) Body(label string, b []byte) {
	if l.mode != "full" {
		return
	}
	l.write(label + " " + string(b))
}

func (l *Logger) write(s string) {
	if l.mode == "off" || l.f == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintf(l.f, "%s %s\n", time.Now().Format(time.RFC3339), s)
}

func (l *Logger) Close() {
	if l.f != nil {
		l.f.Close()
	}
}
```

- [ ] **Step 5: Write the server**

Create `airgap-vllm/internal/server/server.go`:

```go
// Package server is the HTTP bridge between Claude Code and vLLM. It owns all
// I/O; the conversion logic it calls is pure.
package server

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"claude-vllm/internal/config"
	"claude-vllm/internal/filter"
	"claude-vllm/internal/probe"
	"claude-vllm/internal/translate"
)

type Server struct {
	cfg    config.Config
	caps   probe.Capabilities
	mode   string
	client *http.Client
	lg     *Logger
}

func New(cfg config.Config, caps probe.Capabilities, lg *Logger) *Server {
	return &Server{
		cfg:  cfg,
		caps: caps,
		mode: probe.ResolveMode(cfg.Mode, caps),
		// No overall timeout: a long generation is normal. Idle handling is
		// the stream's job, not the client's.
		client: &http.Client{},
		lg:     lg,
	}
}

func (s *Server) Mode() string { return s.mode }

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/v1/models", s.handleModels)
	mux.HandleFunc("/v1/messages/count_tokens", s.handleCountTokens)
	mux.HandleFunc("/v1/messages", s.handleMessages)
	return mux
}

// GenID produces an identifier for a tool call or message that arrived without
// one. A tool_use block with no id crashes the Claude Code renderer.
func GenID(prefix string) string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return prefix + fmt.Sprint(time.Now().UnixNano())
	}
	return prefix + hex.EncodeToString(b)
}

func (s *Server) base() string { return strings.TrimRight(s.cfg.VLLMURL, "/") }

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok", "mode": s.mode, "model": s.cfg.Model, "upstream": s.base(),
	})
}

// handleModels reshapes vLLM's model list into the Anthropic list format so the
// /model picker can offer whatever the server is actually serving.
func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	caps, err := probe.Detect(r.Context(), s.client, s.base(), s.cfg.APIKey)
	if err != nil {
		s.fail(w, http.StatusBadGateway, "upstream_unavailable", err.Error())
		return
	}
	data := make([]map[string]any, 0, len(caps.Models))
	for _, id := range caps.Models {
		data = append(data, map[string]any{
			"id": id, "type": "model", "display_name": id,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data, "has_more": false})
}

func (s *Server) handleCountTokens(w http.ResponseWriter, r *http.Request) {
	req, err := readJSON(r)
	if err != nil {
		s.fail(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	text := translate.FlattenRequestText(req)

	if s.caps.Tokenize {
		if n, err := s.tokenize(r, text); err == nil {
			writeJSON(w, http.StatusOK, map[string]any{"input_tokens": n})
			return
		}
		s.lg.Note("tokenize failed, falling back to estimate")
	}
	// Roughly four characters per token. Only used to keep the UI's context
	// meter approximately honest when the server exposes no tokenizer.
	est := len(text) / 4
	if est < 1 {
		est = 1
	}
	writeJSON(w, http.StatusOK, map[string]any{"input_tokens": est})
}

func (s *Server) tokenize(r *http.Request, text string) (int, error) {
	body, _ := json.Marshal(map[string]any{"model": s.cfg.Model, "prompt": text})
	resp, err := s.upstream(r, http.MethodPost, s.base()+"/tokenize", body)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	var out struct {
		Count int `json:"count"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0, err
	}
	return out.Count, nil
}

func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		s.fail(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	s.lg.Body("claude->bridge", raw)

	var req map[string]any
	if err := json.Unmarshal(raw, &req); err != nil {
		s.fail(w, http.StatusBadRequest, "invalid_request_error", "request body is not JSON: "+err.Error())
		return
	}

	cleaned, notes := filter.Request(req)
	for _, n := range notes {
		s.lg.Note("filter: %s", n)
	}

	// Honor a model the server actually serves; otherwise pin to the configured
	// one so a stale picker selection cannot 404 the session.
	if m, ok := cleaned["model"].(string); !ok || !s.caps.HasModel(m) {
		cleaned["model"] = s.cfg.Model
	}

	streaming, _ := cleaned["stream"].(bool)
	if streaming {
		s.streamMessages(w, r, cleaned)
		return
	}
	s.blockingMessages(w, r, cleaned)
}

func (s *Server) blockingMessages(w http.ResponseWriter, r *http.Request, req map[string]any) {
	if s.mode == "messages" {
		body, _ := json.Marshal(req)
		resp, err := s.upstream(r, http.MethodPost, s.base()+"/v1/messages", body)
		if err != nil {
			s.fail(w, http.StatusBadGateway, "upstream_unavailable", err.Error())
			return
		}
		defer resp.Body.Close()
		out, err := io.ReadAll(resp.Body)
		if err != nil {
			s.fail(w, http.StatusBadGateway, "upstream_unavailable", err.Error())
			return
		}
		s.lg.Body("vllm->bridge", out)
		// Even in passthrough, guarantee tool_use ids.
		var decoded map[string]any
		if json.Unmarshal(out, &decoded) == nil {
			if content, ok := decoded["content"].([]any); ok {
				decoded["content"] = filter.EnsureToolUseIDs(content, func() string { return GenID("toolu_") })
				writeJSON(w, resp.StatusCode, decoded)
				return
			}
		}
		w.WriteHeader(resp.StatusCode)
		w.Write(out)
		return
	}

	oa, err := translate.ToOpenAI(req, s.cfg.Model)
	if err != nil {
		s.fail(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	body, _ := json.Marshal(oa)
	s.lg.Body("bridge->vllm", body)

	resp, err := s.upstream(r, http.MethodPost, s.base()+"/v1/chat/completions", body)
	if err != nil {
		s.fail(w, http.StatusBadGateway, "upstream_unavailable", err.Error())
		return
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		s.fail(w, http.StatusBadGateway, "upstream_unavailable", err.Error())
		return
	}
	s.lg.Body("vllm->bridge", out)

	if resp.StatusCode >= 400 {
		s.fail(w, resp.StatusCode, "upstream_error",
			fmt.Sprintf("vLLM returned HTTP %d: %s", resp.StatusCode, truncate(string(out), 800)))
		return
	}
	var parsed translate.OpenAIResponse
	if err := json.Unmarshal(out, &parsed); err != nil {
		s.fail(w, http.StatusBadGateway, "upstream_error", "unparseable response from vLLM: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, translate.FromOpenAI(parsed, s.cfg.Model, func() string { return GenID("toolu_") }))
}

func (s *Server) upstream(r *http.Request, method, url string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(r.Context(), method, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if s.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+s.cfg.APIKey)
	}
	return s.client.Do(req)
}

func readJSON(r *http.Request) (map[string]any, error) {
	b, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("request body is not JSON: %w", err)
	}
	return m, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// fail emits an Anthropic-shaped error so Claude Code renders it rather than
// showing a bare transport failure.
func (s *Server) fail(w http.ResponseWriter, status int, kind, msg string) {
	s.lg.Note("error %d %s: %s", status, kind, msg)
	writeJSON(w, status, map[string]any{
		"type":  "error",
		"error": map[string]any{"type": kind, "message": msg},
	})
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
```

- [ ] **Step 6: Add a streaming stub so the package compiles**

Add to `airgap-vllm/internal/server/server.go` (replaced entirely in Task 9):

```go
func (s *Server) streamMessages(w http.ResponseWriter, r *http.Request, req map[string]any) {
	s.fail(w, http.StatusNotImplemented, "not_implemented", "streaming lands in Task 9")
}
```

- [ ] **Step 7: Run tests to verify they pass**

Run: `cd airgap-vllm && gofmt -w ./... && go test ./internal/server/ -v`
Expected: PASS, eight tests.

- [ ] **Step 8: Commit**

```bash
git add airgap-vllm/internal/server/ airgap-vllm/internal/translate/request.go
git commit -m "feat(airgap-vllm): bridge server, non-streaming paths"
```

---

### Task 9: Bridge server — streaming and keepalive

Claude Code's stream idle timeout has fired on custom gateways during long gaps. A 27B model's prefill is slow enough to matter, so the bridge emits its own `ping` events.

**Files:**
- Create: `airgap-vllm/internal/server/stream.go`
- Modify: `airgap-vllm/internal/server/server.go` (delete the Task 8 stub)
- Test: `airgap-vllm/internal/server/stream_test.go`

**Interfaces:**
- Consumes: `translate.NewStreamTranslator`, `translate.Chunk`, `translate.EventWriter`, `filter`.
- Produces: `(*Server).streamMessages` (unexported); `server.ScanSSE(r io.Reader, fn func(event, data string) error) error`.

- [ ] **Step 1: Write the failing test**

Create `airgap-vllm/internal/server/stream_test.go`:

```go
package server

import (
	"net/http"
	"strings"
	"testing"
)

func TestStreamingProducesAnthropicEvents(t *testing.T) {
	fake := fakevllmStreaming(t,
		`{"choices":[{"delta":{"content":"Hel"}}]}`,
		`{"choices":[{"delta":{"content":"lo"}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`,
	)
	defer fake.Close()
	h := newTestServer(t, fake, "openai")

	rr := post(t, h, "/v1/messages", `{"model":"m","stream":true,
		"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)

	body := rr.Body.String()
	for _, want := range []string{
		"event: message_start", "event: content_block_start",
		"event: content_block_delta", "event: content_block_stop",
		"event: message_delta", "event: message_stop",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in stream:\n%s", want, body)
		}
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("Content-Type = %q", ct)
	}
}

func TestStreamingToolCallEmitsInputJSONDelta(t *testing.T) {
	fake := fakevllmStreaming(t,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"read","arguments":"{\"p\":"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"1}"}}]}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
	)
	defer fake.Close()
	h := newTestServer(t, fake, "openai")

	rr := post(t, h, "/v1/messages", `{"model":"m","stream":true,"messages":[]}`)
	body := rr.Body.String()
	if !strings.Contains(body, "input_json_delta") {
		t.Errorf("no input_json_delta:\n%s", body)
	}
	if !strings.Contains(body, `"id":"call_1"`) {
		t.Errorf("tool id missing:\n%s", body)
	}
	if !strings.Contains(body, `"stop_reason":"tool_use"`) {
		t.Errorf("stop_reason wrong:\n%s", body)
	}
}

func TestScanSSEParsesEventsAndSkipsDone(t *testing.T) {
	in := strings.NewReader("event: x\ndata: {\"a\":1}\n\ndata: {\"b\":2}\n\ndata: [DONE]\n\n")
	var got []string
	err := ScanSSE(in, func(event, data string) error {
		got = append(got, event+"|"+data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{`x|{"a":1}`, `|{"b":2}`}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("event %d = %q, want %q", i, got[i], want[i])
		}
	}
}

var _ = http.StatusOK
```

Add this helper to `server_test.go`:

```go
func fakevllmStreaming(t *testing.T, chunks ...string) *fakevllm.Server {
	t.Helper()
	return fakevllm.New(fakevllm.Config{Models: []string{"m"}, StreamChunks: chunks})
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd airgap-vllm && go test ./internal/server/ -run Stream`
Expected: FAIL — `undefined: ScanSSE`, and the streaming test hits the Task 8 stub's 501.

- [ ] **Step 3: Remove the stub**

Delete the `streamMessages` stub added in Task 8 Step 6 from `server.go`.

- [ ] **Step 4: Write the streaming implementation**

Create `airgap-vllm/internal/server/stream.go`:

```go
package server

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"claude-vllm/internal/filter"
	"claude-vllm/internal/translate"
)

// pingInterval bounds how long the stream can be silent. Claude Code's idle
// timeout has fired on custom gateways during long prefills.
const pingInterval = 15 * time.Second

// sseWriter serializes writes because the keepalive goroutine and the
// translation loop both emit events.
type sseWriter struct {
	mu sync.Mutex
	w  http.ResponseWriter
	f  http.Flusher
}

func newSSEWriter(w http.ResponseWriter) *sseWriter {
	f, _ := w.(http.Flusher)
	return &sseWriter{w: w, f: f}
}

func (s *sseWriter) Event(name string, data any) error {
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", name, b); err != nil {
		return err
	}
	if s.f != nil {
		s.f.Flush()
	}
	return nil
}

// ScanSSE reads an SSE body, invoking fn for each event. The terminal [DONE]
// sentinel is consumed rather than forwarded.
func ScanSSE(r io.Reader, fn func(event, data string) error) error {
	sc := bufio.NewScanner(r)
	// Tool call arguments can exceed the 64KB default line budget.
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	var event string
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		switch {
		case line == "":
			event = ""
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if data == "[DONE]" {
				return nil
			}
			if err := fn(event, data); err != nil {
				return err
			}
		}
	}
	return sc.Err()
}

func (s *Server) streamMessages(w http.ResponseWriter, r *http.Request, req map[string]any) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	out := newSSEWriter(w)
	stop := s.startKeepalive(r, out)
	defer stop()

	if s.mode == "messages" {
		s.streamPassthrough(w, r, req, out)
		return
	}
	s.streamTranslated(w, r, req, out)
}

// startKeepalive emits ping events until the returned function is called.
func (s *Server) startKeepalive(r *http.Request, out *sseWriter) func() {
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(pingInterval)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-r.Context().Done():
				return
			case <-t.C:
				out.Event("ping", map[string]any{"type": "ping"})
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(done) }) }
}

func (s *Server) streamTranslated(w http.ResponseWriter, r *http.Request, req map[string]any, out *sseWriter) {
	oa, err := translate.ToOpenAI(req, s.cfg.Model)
	if err != nil {
		s.streamFail(out, "invalid_request_error", err.Error())
		return
	}
	oa["stream"] = true
	body, _ := json.Marshal(oa)
	s.lg.Body("bridge->vllm(stream)", body)

	resp, err := s.upstream(r, http.MethodPost, s.base()+"/v1/chat/completions", body)
	if err != nil {
		s.streamFail(out, "upstream_unavailable", err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		s.lg.Body("vllm->bridge(error)", b)
		s.streamFail(out, "upstream_error",
			fmt.Sprintf("vLLM returned HTTP %d: %s", resp.StatusCode, truncate(string(b), 800)))
		return
	}

	tr := translate.NewStreamTranslator(out, GenID("msg_"), s.cfg.Model,
		func() string { return GenID("toolu_") })
	if err := tr.Start(); err != nil {
		return
	}
	err = ScanSSE(resp.Body, func(_, data string) error {
		var c translate.Chunk
		if err := json.Unmarshal([]byte(data), &c); err != nil {
			s.lg.Note("skipping unparseable chunk: %v", err)
			return nil
		}
		return tr.Chunk(c)
	})
	if err != nil {
		s.lg.Note("stream read ended: %v", err)
	}
	tr.Finish()
}

// streamPassthrough forwards the upstream Anthropic stream, repairing tool_use
// blocks that arrive without an id.
func (s *Server) streamPassthrough(w http.ResponseWriter, r *http.Request, req map[string]any, out *sseWriter) {
	body, _ := json.Marshal(req)
	resp, err := s.upstream(r, http.MethodPost, s.base()+"/v1/messages", body)
	if err != nil {
		s.streamFail(out, "upstream_unavailable", err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		s.streamFail(out, "upstream_error",
			fmt.Sprintf("vLLM returned HTTP %d: %s", resp.StatusCode, truncate(string(b), 800)))
		return
	}

	ScanSSE(resp.Body, func(event, data string) error {
		var decoded map[string]any
		if json.Unmarshal([]byte(data), &decoded) != nil {
			return out.Event(event, json.RawMessage(data))
		}
		if event == "content_block_start" {
			if cb, ok := decoded["content_block"].(map[string]any); ok {
				fixed := filter.EnsureToolUseIDs([]any{cb}, func() string { return GenID("toolu_") })
				decoded["content_block"] = fixed[0]
			}
		}
		name := event
		if name == "" {
			name, _ = decoded["type"].(string)
		}
		return out.Event(name, decoded)
	})
}

// streamFail reports an error inside the stream, since headers are already sent.
func (s *Server) streamFail(out *sseWriter, kind, msg string) {
	s.lg.Note("stream error %s: %s", kind, msg)
	out.Event("error", map[string]any{
		"type":  "error",
		"error": map[string]any{"type": kind, "message": msg},
	})
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd airgap-vllm && gofmt -w ./... && go test ./internal/... -v`
Expected: PASS across all packages.

- [ ] **Step 6: Commit**

```bash
git add airgap-vllm/internal/server/
git commit -m "feat(airgap-vllm): SSE streaming translation and ping keepalive"
```

---

### Task 10: Launcher — environment contract and process supervision

The environment contract is the sign-in bypass. Getting it wrong is the difference between a working install and an OAuth prompt on a machine with no browser.

**Files:**
- Create: `airgap-vllm/internal/launch/env.go`
- Create: `airgap-vllm/internal/launch/settings.go`
- Create: `airgap-vllm/internal/launch/run.go`
- Test: `airgap-vllm/internal/launch/env_test.go`
- Test: `airgap-vllm/internal/launch/settings_test.go`

**Interfaces:**
- Consumes: `config.Config`, `probe.Detect`, `server.New`, `server.NewLogger`.
- Produces: `launch.Env(cfg config.Config, port int, base []string) []string`, `launch.WriteSettings(path string, cfg config.Config) error`, `launch.WriteOnboarding(path string) error`, `launch.Run(cfg config.Config, args []string) (int, error)`, `launch.ClaudeBinary() (string, error)`.

- [ ] **Step 1: Write the failing tests**

Create `airgap-vllm/internal/launch/env_test.go`:

```go
package launch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"claude-vllm/internal/config"
)

func envMap(t *testing.T, kv []string) map[string]string {
	t.Helper()
	m := map[string]string{}
	for _, e := range kv {
		parts := strings.SplitN(e, "=", 2)
		if len(parts) == 2 {
			m[parts[0]] = parts[1]
		}
	}
	return m
}

func testCfg() config.Config {
	c := config.Default()
	c.VLLMURL = "http://10.0.0.5:8000"
	c.Model = "Qwen/Qwen3.8-27B"
	return c
}

func TestEnvPointsClaudeAtTheBridge(t *testing.T) {
	m := envMap(t, Env(testCfg(), 41234, nil))
	if m["ANTHROPIC_BASE_URL"] != "http://127.0.0.1:41234" {
		t.Errorf("ANTHROPIC_BASE_URL = %q", m["ANTHROPIC_BASE_URL"])
	}
	if m["ANTHROPIC_AUTH_TOKEN"] == "" {
		t.Error("ANTHROPIC_AUTH_TOKEN must be non-empty to bypass sign-in")
	}
}

// Every tier must resolve, or background Haiku-tier calls 404.
func TestEnvMapsAllThreeModelTiers(t *testing.T) {
	m := envMap(t, Env(testCfg(), 1, nil))
	for _, k := range []string{
		"ANTHROPIC_DEFAULT_OPUS_MODEL",
		"ANTHROPIC_DEFAULT_SONNET_MODEL",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL",
	} {
		if m[k] != "Qwen/Qwen3.8-27B" {
			t.Errorf("%s = %q, want the configured model", k, m[k])
		}
	}
	if m["CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY"] != "1" {
		t.Error("gateway model discovery should be enabled so /model lists served models")
	}
}

// Setting this forces Cloud-gateway sign-in and fails every request.
func TestEnvStripsUseGatewayFromInheritedEnvironment(t *testing.T) {
	base := []string{"CLAUDE_CODE_USE_GATEWAY=1", "PATH=/usr/bin"}
	m := envMap(t, Env(testCfg(), 1, base))
	if _, present := m["CLAUDE_CODE_USE_GATEWAY"]; present {
		t.Error("CLAUDE_CODE_USE_GATEWAY must be stripped, it forces Cloud sign-in")
	}
	if m["PATH"] != "/usr/bin" {
		t.Error("unrelated inherited variables must survive")
	}
}

// These override Claude Code's own detection; guessing is worse than omitting.
func TestEnvOmitsCapabilityOverridesByDefault(t *testing.T) {
	m := envMap(t, Env(testCfg(), 1, nil))
	for k := range m {
		if strings.Contains(k, "SUPPORTED_CAPABILITIES") {
			t.Errorf("%s should not be set by default", k)
		}
	}
}

func TestEnvSetsOfflineSwitches(t *testing.T) {
	m := envMap(t, Env(testCfg(), 1, nil))
	for _, k := range []string{
		"DISABLE_UPDATES", "DISABLE_AUTOUPDATER", "DISABLE_TELEMETRY",
		"DISABLE_ERROR_REPORTING", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC",
	} {
		if m[k] != "1" {
			t.Errorf("%s = %q, want 1", k, m[k])
		}
	}
}

func TestEnvOverridesStaleInheritedAnthropicValues(t *testing.T) {
	base := []string{"ANTHROPIC_BASE_URL=https://api.anthropic.com", "ANTHROPIC_API_KEY=sk-leftover"}
	m := envMap(t, Env(testCfg(), 999, base))
	if m["ANTHROPIC_BASE_URL"] != "http://127.0.0.1:999" {
		t.Errorf("inherited base URL not overridden: %q", m["ANTHROPIC_BASE_URL"])
	}
	if _, present := m["ANTHROPIC_API_KEY"]; present {
		t.Error("a leftover first-party API key must not reach the session")
	}
}
```

Create `airgap-vllm/internal/launch/settings_test.go`:

```go
package launch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteSettingsPreservesExistingKeys(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	os.WriteFile(p, []byte(`{"theme":"dark","env":{"MY_VAR":"keep"}}`), 0o600)

	if err := WriteSettings(p, testCfg()); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	b, _ := os.ReadFile(p)
	json.Unmarshal(b, &got)

	if got["theme"] != "dark" {
		t.Error("unrelated top-level settings were lost")
	}
	env := got["env"].(map[string]any)
	if env["MY_VAR"] != "keep" {
		t.Error("existing env entries were lost")
	}
	if env["DISABLE_UPDATES"] != "1" {
		t.Errorf("offline switches not written: %v", env)
	}
}

func TestWriteOnboardingMarksFirstRunComplete(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".claude.json")
	if err := WriteOnboarding(p); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	b, _ := os.ReadFile(p)
	json.Unmarshal(b, &got)
	if got["hasCompletedOnboarding"] != true {
		t.Errorf("hasCompletedOnboarding = %v", got["hasCompletedOnboarding"])
	}
	if got["hasTrustDialogAccepted"] != true {
		t.Errorf("hasTrustDialogAccepted = %v", got["hasTrustDialogAccepted"])
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd airgap-vllm && go test ./internal/launch/`
Expected: FAIL — `undefined: Env`, `undefined: WriteSettings`.

- [ ] **Step 3: Write the environment contract**

Create `airgap-vllm/internal/launch/env.go`:

```go
// Package launch owns the Claude Code environment contract and the child
// process that runs under it.
package launch

import (
	"fmt"
	"os"
	"runtime"
	"strings"

	"claude-vllm/internal/config"
)

// strippedPrefixes are inherited variables that must not reach the child.
// A leftover first-party credential or endpoint silently redirects traffic.
var strippedExact = map[string]bool{
	// Forces Cloud-gateway sign-in; every request then fails with
	// "Not signed in to the Cloud gateway".
	"CLAUDE_CODE_USE_GATEWAY": true,
	"ANTHROPIC_API_KEY":       true,
	"ANTHROPIC_API_HOST":      true,
	"CLAUDE_CODE_USE_BEDROCK": true,
	"CLAUDE_CODE_USE_VERTEX":  true,
}

// Env builds the environment for the child claude process. base is the
// inherited environment; pass nil in tests, os.Environ() in production.
func Env(cfg config.Config, port int, base []string) []string {
	set := map[string]string{}
	order := []string{}
	put := func(k, v string) {
		if _, seen := set[k]; !seen {
			order = append(order, k)
		}
		set[k] = v
	}

	for _, e := range base {
		parts := strings.SplitN(e, "=", 2)
		if len(parts) != 2 {
			continue
		}
		if strippedExact[parts[0]] {
			continue
		}
		put(parts[0], parts[1])
	}

	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	put("ANTHROPIC_BASE_URL", baseURL)
	// Any non-empty value satisfies the credential check and skips OAuth.
	put("ANTHROPIC_AUTH_TOKEN", "local-vllm")

	// Every tier resolves to the one served model so background Haiku-tier
	// calls cannot 404.
	label := shortLabel(cfg.Model) + " (local vLLM)"
	for _, tier := range []string{"OPUS", "SONNET", "HAIKU"} {
		put("ANTHROPIC_DEFAULT_"+tier+"_MODEL", cfg.Model)
		put("ANTHROPIC_DEFAULT_"+tier+"_MODEL_NAME", label)
		put("ANTHROPIC_DEFAULT_"+tier+"_MODEL_DESCRIPTION", "Self-hosted vLLM")
	}
	// ANTHROPIC_DEFAULT_*_MODEL_SUPPORTED_CAPABILITIES is deliberately not set.
	// It overrides Claude Code's own capability detection; guessing its accepted
	// token set is worse than letting detection run.

	// The bridge serves /v1/models, so the /model picker can list what vLLM
	// actually serves. Exempt from the nonessential-traffic block below.
	put("CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY", "1")

	for _, k := range []string{
		"DISABLE_UPDATES", "DISABLE_AUTOUPDATER", "DISABLE_TELEMETRY",
		"DISABLE_ERROR_REPORTING", "DISABLE_BUG_COMMAND", "DISABLE_COST_WARNINGS",
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC",
	} {
		put(k, "1")
	}

	if runtime.GOOS == "windows" {
		if p := gitBashPath(); p != "" {
			put("CLAUDE_CODE_GIT_BASH_PATH", p)
		}
	}

	out := make([]string, 0, len(order))
	for _, k := range order {
		out = append(out, k+"="+set[k])
	}
	return out
}

// shortLabel trims a HuggingFace-style org prefix for display.
func shortLabel(model string) string {
	if i := strings.LastIndex(model, "/"); i >= 0 && i+1 < len(model) {
		return model[i+1:]
	}
	return model
}

// gitBashPath returns Git Bash if installed. Without it Claude Code falls back
// to the PowerShell tool, which works but is reported by doctor.
func gitBashPath() string {
	for _, p := range []string{
		`C:\Program Files\Git\bin\bash.exe`,
		`C:\Program Files (x86)\Git\bin\bash.exe`,
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}
```

- [ ] **Step 4: Write the settings writers**

Create `airgap-vllm/internal/launch/settings.go`:

```go
package launch

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"claude-vllm/internal/config"
)

// WriteSettings merges the offline switches into Claude Code's settings.json
// without disturbing anything the developer already configured.
func WriteSettings(path string, cfg config.Config) error {
	doc, err := readJSONObject(path)
	if err != nil {
		return err
	}
	env, _ := doc["env"].(map[string]any)
	if env == nil {
		env = map[string]any{}
	}
	for _, k := range []string{
		"DISABLE_UPDATES", "DISABLE_AUTOUPDATER", "DISABLE_TELEMETRY",
		"DISABLE_ERROR_REPORTING", "DISABLE_BUG_COMMAND",
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC",
	} {
		env[k] = "1"
	}
	if p := gitBashPath(); p != "" {
		env["CLAUDE_CODE_GIT_BASH_PATH"] = p
	}
	doc["env"] = env
	return writeJSONObject(path, doc)
}

// WriteOnboarding clears the first-run prompts, which cannot be completed on a
// machine with no browser.
func WriteOnboarding(path string) error {
	doc, err := readJSONObject(path)
	if err != nil {
		return err
	}
	doc["hasCompletedOnboarding"] = true
	doc["hasTrustDialogAccepted"] = true
	if _, ok := doc["theme"]; !ok {
		doc["theme"] = "dark"
	}
	return writeJSONObject(path, doc)
}

func readJSONObject(path string) (map[string]any, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	doc := map[string]any{}
	if len(b) > 0 {
		if err := json.Unmarshal(b, &doc); err != nil {
			return nil, fmt.Errorf("%s is not valid JSON: %w", path, err)
		}
	}
	return doc, nil
}

func writeJSONObject(path string, doc map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}
```

- [ ] **Step 5: Write the runner**

Create `airgap-vllm/internal/launch/run.go`:

```go
package launch

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"claude-vllm/internal/config"
	"claude-vllm/internal/probe"
	"claude-vllm/internal/server"
)

// ClaudeBinary locates claude next to this executable first, so a bundle is
// self-contained, then falls back to PATH.
func ClaudeBinary() (string, error) {
	name := "claude"
	if runtime.GOOS == "windows" {
		name = "claude.exe"
	}
	if self, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(self), name)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	p, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("cannot find %s next to claude-vllm or on PATH: %w", name, err)
	}
	return p, nil
}

// Run starts the bridge on an ephemeral loopback port, then runs Claude Code
// against it, tearing the bridge down when the child exits.
func Run(cfg config.Config, args []string) (int, error) {
	if err := cfg.Validate(); err != nil {
		return 1, err
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 1, fmt.Errorf("binding a loopback port: %w", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	caps, probeErr := probe.Detect(ctx, http.DefaultClient, cfg.VLLMURL, cfg.APIKey)
	cancel()
	if probeErr != nil {
		ln.Close()
		return 1, fmt.Errorf("%w\n\nFix the endpoint with:\n  claude-vllm config set url=http://HOST:PORT\nThen re-check with:\n  claude-vllm doctor", probeErr)
	}

	logDir, err := config.Dir()
	if err != nil {
		ln.Close()
		return 1, err
	}
	lg, err := server.NewLogger(cfg.Log, filepath.Join(logDir, "log"))
	if err != nil {
		ln.Close()
		return 1, err
	}
	defer lg.Close()

	srv := server.New(cfg, caps, lg)
	httpSrv := &http.Server{Handler: srv.Handler()}
	go func() {
		if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			lg.Note("bridge server stopped: %v", err)
		}
	}()
	defer func() {
		shutCtx, shutCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer shutCancel()
		httpSrv.Shutdown(shutCtx)
	}()

	bin, err := ClaudeBinary()
	if err != nil {
		return 1, err
	}

	fmt.Fprintf(os.Stderr, "claude-vllm: %s via %s (mode: %s)\n", cfg.Model, cfg.VLLMURL, srv.Mode())

	cmd := exec.Command(bin, args...)
	cmd.Env = Env(cfg, port, os.Environ())
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode(), nil
		}
		return 1, fmt.Errorf("running %s: %w", bin, err)
	}
	return 0, nil
}
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `cd airgap-vllm && gofmt -w ./... && go test ./internal/launch/ -v`
Expected: PASS, eight tests. Remove the unused `os`/`filepath` imports from `env_test.go` if the compiler flags them.

- [ ] **Step 7: Commit**

```bash
git add airgap-vllm/internal/launch/
git commit -m "feat(airgap-vllm): sign-in bypass env contract and process launcher"
```

---

### Task 11: CLI — entry point, config and setup

**Files:**
- Create: `airgap-vllm/cmd/claude-vllm/main.go`
- Create: `airgap-vllm/internal/cli/config_cmd.go`
- Create: `airgap-vllm/internal/cli/setup.go`
- Test: `airgap-vllm/internal/cli/config_cmd_test.go`

**Interfaces:**
- Consumes: `config`, `probe`, `launch`.
- Produces: `cli.ConfigSet(args []string) error`, `cli.ConfigShow() error`, `cli.Setup(in io.Reader, out io.Writer) error`, `cli.ApplyAssignments(c *config.Config, args []string) error`.

- [ ] **Step 1: Write the failing test**

Create `airgap-vllm/internal/cli/config_cmd_test.go`:

```go
package cli

import (
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd airgap-vllm && go test ./internal/cli/`
Expected: FAIL — `undefined: ApplyAssignments`.

- [ ] **Step 3: Write the config command**

Create `airgap-vllm/internal/cli/config_cmd.go`:

```go
// Package cli holds the human-facing commands. It owns interaction only; the
// logic it drives lives in the other packages.
package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"

	"claude-vllm/internal/config"
)

var configKeys = []string{
	"url", "model", "api_key", "mode", "max_tokens",
	"context_window", "supports_thinking", "log",
}

// ApplyAssignments applies key=value pairs so nobody has to hand-edit JSON.
func ApplyAssignments(c *config.Config, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("nothing to set. Valid keys: %v", configKeys)
	}
	for _, a := range args {
		k, v, found := cut(a, "=")
		if !found || k == "" {
			return fmt.Errorf("%q is not a key=value assignment", a)
		}
		switch k {
		case "url":
			c.VLLMURL = v
		case "model":
			c.Model = v
		case "api_key":
			c.APIKey = v
		case "mode":
			c.Mode = v
		case "log":
			c.Log = v
		case "max_tokens":
			n, err := strconv.Atoi(v)
			if err != nil {
				return fmt.Errorf("max_tokens must be a number, got %q", v)
			}
			c.MaxTokens = n
		case "context_window":
			n, err := strconv.Atoi(v)
			if err != nil {
				return fmt.Errorf("context_window must be a number, got %q", v)
			}
			c.ContextWindow = n
		case "supports_thinking":
			b, err := strconv.ParseBool(v)
			if err != nil {
				return fmt.Errorf("supports_thinking must be true or false, got %q", v)
			}
			c.SupportsThinking = b
		default:
			sorted := append([]string{}, configKeys...)
			sort.Strings(sorted)
			return fmt.Errorf("unknown key %q. Valid keys: %v", k, sorted)
		}
	}
	return nil
}

func cut(s, sep string) (string, string, bool) {
	for i := 0; i+len(sep) <= len(s); i++ {
		if s[i:i+len(sep)] == sep {
			return s[:i], s[i+len(sep):], true
		}
	}
	return s, "", false
}

func ConfigSet(args []string) error {
	path, err := config.Path()
	if err != nil {
		return err
	}
	c, err := config.LoadFrom(path)
	if err != nil {
		return err
	}
	if err := ApplyAssignments(&c, args); err != nil {
		return err
	}
	if err := c.SaveTo(path); err != nil {
		return err
	}
	fmt.Printf("Saved %s\n", path)
	return nil
}

func ConfigShow() error {
	c, err := config.Load()
	if err != nil {
		return err
	}
	path, _ := config.Path()
	fmt.Printf("# effective configuration (%s, with CLAUDE_VLLM_* overrides applied)\n", path)
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(c)
}
```

- [ ] **Step 4: Write the setup command**

Create `airgap-vllm/internal/cli/setup.go`:

```go
package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"claude-vllm/internal/config"
	"claude-vllm/internal/launch"
	"claude-vllm/internal/probe"
)

// Setup walks a developer through first configuration. It probes the server so
// the model can be chosen from what is actually served rather than typed blind.
func Setup(in io.Reader, out io.Writer) error {
	path, err := config.Path()
	if err != nil {
		return err
	}
	c, err := config.LoadFrom(path)
	if err != nil {
		return err
	}
	r := bufio.NewReader(in)

	fmt.Fprintln(out, "Claude Code / vLLM bridge setup")
	fmt.Fprintln(out)

	c.VLLMURL = ask(r, out, "vLLM base URL", orDefault(c.VLLMURL, "http://127.0.0.1:8000"))
	if key := ask(r, out, "vLLM API key (blank if none)", c.APIKey); key != "" {
		c.APIKey = key
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	caps, err := probe.Detect(ctx, http.DefaultClient, c.VLLMURL, c.APIKey)
	if err != nil {
		return fmt.Errorf("%w\n\nCheck the URL and that vLLM is running, then re-run: claude-vllm setup", err)
	}

	fmt.Fprintf(out, "\nConnected. Native /v1/messages: %v\n", caps.MessagesAPI)
	if len(caps.Models) == 0 {
		return fmt.Errorf("vLLM reported no models at %s", c.VLLMURL)
	}
	fmt.Fprintln(out, "\nModels served:")
	for i, m := range caps.Models {
		fmt.Fprintf(out, "  %d) %s\n", i+1, m)
	}
	choice := ask(r, out, "Select a model by number", "1")
	n, err := strconv.Atoi(strings.TrimSpace(choice))
	if err != nil || n < 1 || n > len(caps.Models) {
		return fmt.Errorf("%q is not one of the listed numbers", choice)
	}
	c.Model = caps.Models[n-1]

	if err := c.Validate(); err != nil {
		return err
	}
	if err := c.SaveTo(path); err != nil {
		return err
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	if err := launch.WriteSettings(filepath.Join(home, ".claude", "settings.json"), c); err != nil {
		return err
	}
	if err := launch.WriteOnboarding(filepath.Join(home, ".claude.json")); err != nil {
		return err
	}

	fmt.Fprintf(out, "\nSaved %s\n", path)
	fmt.Fprintf(out, "Model: %s\nMode:  %s\n\n", c.Model, probe.ResolveMode(c.Mode, caps))
	fmt.Fprintln(out, "Next: run `claude-vllm doctor` to verify tool calling, then `claude-vllm` to start.")
	return nil
}

func ask(r *bufio.Reader, out io.Writer, prompt, def string) string {
	if def != "" {
		fmt.Fprintf(out, "%s [%s]: ", prompt, def)
	} else {
		fmt.Fprintf(out, "%s: ", prompt)
	}
	line, _ := r.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return def
	}
	return line
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
```

- [ ] **Step 5: Write the entry point**

Create `airgap-vllm/cmd/claude-vllm/main.go`:

```go
// Command claude-vllm bridges Claude Code to a self-hosted vLLM server and
// launches Claude Code against it.
package main

import (
	"fmt"
	"os"

	"claude-vllm/internal/cli"
	"claude-vllm/internal/config"
	"claude-vllm/internal/launch"
)

const usage = `claude-vllm — run Claude Code against a self-hosted vLLM server

Usage:
  claude-vllm [claude args...]   Start the bridge and launch Claude Code
  claude-vllm setup              Configure the vLLM endpoint and model
  claude-vllm doctor             Diagnose the connection and tool calling
  claude-vllm serve              Run the bridge in the foreground
  claude-vllm config show        Print the effective configuration
  claude-vllm config set k=v...  Change configuration
                                 keys: url model api_key mode max_tokens
                                       context_window supports_thinking log
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "claude-vllm: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "-h", "--help", "help":
			fmt.Print(usage)
			return nil
		case "setup":
			return cli.Setup(os.Stdin, os.Stdout)
		case "doctor":
			return cli.Doctor(os.Stdout)
		case "serve":
			return cli.Serve(args[1:])
		case "config":
			if len(args) < 2 {
				return fmt.Errorf("config needs a subcommand: show or set")
			}
			switch args[1] {
			case "show":
				return cli.ConfigShow()
			case "set":
				return cli.ConfigSet(args[2:])
			default:
				return fmt.Errorf("unknown config subcommand %q", args[1])
			}
		}
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	code, err := launch.Run(cfg, args)
	if err != nil {
		return err
	}
	os.Exit(code)
	return nil
}
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `cd airgap-vllm && gofmt -w ./... && go test ./internal/cli/ -v`
Expected: PASS, four tests. The build fails until Task 12 defines `cli.Doctor` and `cli.Serve`; that is expected at this point.

- [ ] **Step 7: Commit**

```bash
git add airgap-vllm/cmd/ airgap-vllm/internal/cli/
git commit -m "feat(airgap-vllm): CLI entry point, config and interactive setup"
```

---

### Task 12: Doctor and serve

`doctor` is the on-box acceptance test. It is the only way a developer with no internet can find out *why* something is failing, so its output has to name the next action, not just the symptom.

**Files:**
- Create: `airgap-vllm/internal/cli/doctor.go`
- Create: `airgap-vllm/internal/cli/serve.go`
- Modify: `airgap-vllm/internal/probe/probe.go` (add `MaxModelLen`)
- Modify: `airgap-vllm/internal/fakevllm/fakevllm.go` (add `MaxModelLen` to Config)
- Test: `airgap-vllm/internal/cli/doctor_test.go`
- Test: `airgap-vllm/internal/probe/probe_test.go` (one added case)

**Interfaces:**
- Consumes: `config`, `probe`, `server`, `fakevllm`.
- Produces: `cli.Doctor(out io.Writer) error`, `cli.Serve(args []string) error`, `cli.SmokeTest(ctx context.Context, handler http.Handler, model string) error`.

- [ ] **Step 1: Write the failing test**

Create `airgap-vllm/internal/cli/doctor_test.go`:

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd airgap-vllm && go test ./internal/cli/ -run Smoke`
Expected: FAIL — `undefined: SmokeTest`.

- [ ] **Step 3: Write doctor**

Create `airgap-vllm/internal/cli/doctor.go`:

```go
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"time"

	"claude-vllm/internal/config"
	"claude-vllm/internal/launch"
	"claude-vllm/internal/probe"
	"claude-vllm/internal/server"
)

// toolCallHint is printed whenever the smoke test fails. The correct parser is
// model-specific and cannot be determined from the bridge, so the message names
// the knob rather than guessing a value.
const toolCallHint = `
Tool calling did not work. Claude Code is tool-heavy and will be unusable
until it does. This is a vLLM server setting, not a bridge setting.

Restart vLLM with automatic tool choice and the parser matching your model:

  vllm serve <MODEL> \
    --enable-auto-tool-choice \
    --tool-call-parser <PARSER>

Common parser values: hermes, llama3_json, mistral, pythonic, deepseek_v3.
For Qwen3-family models, try hermes first. Check your vLLM build's
--tool-call-parser help output for the list it actually supports.
`

func Doctor(out io.Writer) error {
	path, _ := config.Path()
	fmt.Fprintf(out, "config file      %s\n", path)

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "vLLM URL         %s\n", cfg.VLLMURL)
	fmt.Fprintf(out, "model            %s\n", cfg.Model)
	fmt.Fprintf(out, "configured mode  %s\n", cfg.Mode)

	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(out, "\nFAIL  %v\n", err)
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	caps, err := probe.Detect(ctx, http.DefaultClient, cfg.VLLMURL, cfg.APIKey)
	if err != nil {
		fmt.Fprintf(out, "\nFAIL  %v\n", err)
		fmt.Fprintf(out, "\nFix with:  claude-vllm config set url=http://HOST:PORT\n")
		return err
	}
	mode := probe.ResolveMode(cfg.Mode, caps)
	fmt.Fprintf(out, "reachable        yes\n")
	fmt.Fprintf(out, "native /v1/messages  %v\n", caps.MessagesAPI)
	fmt.Fprintf(out, "/tokenize        %v\n", caps.Tokenize)
	fmt.Fprintf(out, "effective mode   %s\n", mode)
	fmt.Fprintf(out, "models served    %v\n", caps.Models)

	if !caps.HasModel(cfg.Model) {
		fmt.Fprintf(out, "\nFAIL  configured model %q is not served.\n", cfg.Model)
		fmt.Fprintf(out, "Pick one of the models above:\n  claude-vllm config set model=NAME\n")
		return fmt.Errorf("model %q not served", cfg.Model)
	}

	if p := gitBashReport(); p != "" {
		fmt.Fprintf(out, "shell tool       Bash via %s\n", p)
	} else {
		fmt.Fprintf(out, "shell tool       PowerShell (Git for Windows not found; Bash tool unavailable)\n")
	}

	bin, err := launch.ClaudeBinary()
	if err != nil {
		fmt.Fprintf(out, "claude binary    NOT FOUND — %v\n", err)
	} else {
		fmt.Fprintf(out, "claude binary    %s\n", bin)
	}

	lg, err := server.NewLogger("off", filepath.Join(filepath.Dir(path), "log"))
	if err != nil {
		return err
	}
	handler := server.New(cfg, caps, lg).Handler()

	fmt.Fprintf(out, "\nrunning tool-calling smoke test...\n")
	if err := SmokeTest(ctx, handler, cfg.Model); err != nil {
		fmt.Fprintf(out, "FAIL  %v\n", err)
		fmt.Fprint(out, toolCallHint)
		return err
	}
	fmt.Fprintf(out, "PASS  the model emitted a well-formed tool call\n")
	fmt.Fprintf(out, "\nAll checks passed. Start with: claude-vllm\n")
	return nil
}

func gitBashReport() string {
	env := launch.Env(config.Default(), 0, nil)
	for _, e := range env {
		const k = "CLAUDE_CODE_GIT_BASH_PATH="
		if len(e) > len(k) && e[:len(k)] == k {
			return e[len(k):]
		}
	}
	return ""
}

// SmokeTest drives a real request through the bridge with one trivial tool and
// asserts the model produced a usable tool call. This is the check that cannot
// be performed from a development machine, only against the live server.
func SmokeTest(ctx context.Context, handler http.Handler, model string) error {
	body := map[string]any{
		"model":      model,
		"max_tokens": 256,
		"messages": []any{map[string]any{
			"role":    "user",
			"content": "Call the ping tool with value \"ok\". Reply only with the tool call.",
		}},
		"tools": []any{map[string]any{
			"name":        "ping",
			"description": "Responds to a ping. Call this tool.",
			"input_schema": map[string]any{
				"type":       "object",
				"properties": map[string]any{"value": map[string]any{"type": "string"}},
				"required":   []any{"value"},
			},
		}},
		"tool_choice": map[string]any{"type": "auto"},
	}
	raw, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(raw)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		return fmt.Errorf("bridge returned HTTP %d: %s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Content []struct {
			Type  string         `json:"type"`
			ID    string         `json:"id"`
			Name  string         `json:"name"`
			Input map[string]any `json:"input"`
		} `json:"content"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		return fmt.Errorf("unparseable response: %w", err)
	}
	for _, b := range resp.Content {
		if b.Type != "tool_use" {
			continue
		}
		if b.Name == "" {
			return fmt.Errorf("model emitted a tool call with no name; the tool-call parser is probably wrong")
		}
		if b.ID == "" {
			return fmt.Errorf("tool call %q has no id", b.Name)
		}
		return nil
	}
	return fmt.Errorf("model returned prose instead of a tool call; vLLM is likely missing --enable-auto-tool-choice")
}
```

- [ ] **Step 4: Add the context-window mismatch check**

The spec requires doctor to warn when the configured context window disagrees
with what vLLM was launched with. vLLM reports `max_model_len` per model on
`/v1/models`.

Extend `Capabilities` in `airgap-vllm/internal/probe/probe.go`:

```go
type Capabilities struct {
	MessagesAPI bool
	Tokenize    bool
	Models      []string
	// MaxModelLen is the context length vLLM reports per model, when it does.
	MaxModelLen map[string]int
}
```

In `fetchModels`, capture it and return both:

```go
func fetchModels(ctx context.Context, client *http.Client, base, apiKey string) ([]string, map[string]int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/models", nil)
	if err != nil {
		return nil, nil, err
	}
	auth(req, apiKey)
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("/v1/models returned HTTP %d", resp.StatusCode)
	}
	var body struct {
		Data []struct {
			ID          string `json:"id"`
			MaxModelLen int    `json:"max_model_len"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, nil, fmt.Errorf("parsing /v1/models: %w", err)
	}
	ids := make([]string, 0, len(body.Data))
	lens := map[string]int{}
	for _, m := range body.Data {
		ids = append(ids, m.ID)
		if m.MaxModelLen > 0 {
			lens[m.ID] = m.MaxModelLen
		}
	}
	return ids, lens, nil
}
```

Update the one call site in `Detect`:

```go
	models, lens, err := fetchModels(ctx, client, base, apiKey)
	if err != nil {
		return caps, fmt.Errorf("cannot reach vLLM at %s: %w", base, err)
	}
	caps.Models = models
	caps.MaxModelLen = lens
```

Add this test to `airgap-vllm/internal/probe/probe_test.go`:

```go
func TestDetectCapturesMaxModelLenWhenReported(t *testing.T) {
	s := fakevllm.New(fakevllm.Config{Models: []string{"m"}, MaxModelLen: 131072})
	defer s.Close()

	caps, err := Detect(context.Background(), http.DefaultClient, s.URL(), "")
	if err != nil {
		t.Fatal(err)
	}
	if caps.MaxModelLen["m"] != 131072 {
		t.Errorf("MaxModelLen = %v, want 131072", caps.MaxModelLen)
	}
}
```

Add `MaxModelLen int` to `fakevllm.Config` and emit it from `handleModels`:

```go
	for _, m := range s.cfg.Models {
		entry := map[string]any{"id": m, "object": "model"}
		if s.cfg.MaxModelLen > 0 {
			entry["max_model_len"] = s.cfg.MaxModelLen
		}
		data = append(data, entry)
	}
```

Finally, in `Doctor`, after the model-availability check:

```go
	if served, ok := caps.MaxModelLen[cfg.Model]; ok {
		fmt.Fprintf(out, "server context   %d tokens
", served)
		if cfg.ContextWindow > served {
			fmt.Fprintf(out, "
WARN  configured context_window (%d) exceeds what vLLM was launched with (%d).
",
				cfg.ContextWindow, served)
			fmt.Fprintf(out, "      Long sessions will fail mid-conversation. Fix with:\n")
			fmt.Fprintf(out, "        claude-vllm config set context_window=%d\n", served)
		}
	}
```

Run: `cd airgap-vllm && go test ./internal/probe/ ./internal/cli/ -v`
Expected: PASS, including the new `TestDetectCapturesMaxModelLenWhenReported`.

- [ ] **Step 5: Write serve**

Create `airgap-vllm/internal/cli/serve.go`:

```go
package cli

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"claude-vllm/internal/config"
	"claude-vllm/internal/probe"
	"claude-vllm/internal/server"
)

// Serve runs the bridge in the foreground. Useful for debugging, and for
// pointing several Claude Code sessions at one bridge.
func Serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	addr := fs.String("addr", "127.0.0.1:8787", "address to listen on")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	caps, err := probe.Detect(ctx, http.DefaultClient, cfg.VLLMURL, cfg.APIKey)
	cancel()
	if err != nil {
		return err
	}

	dir, err := config.Dir()
	if err != nil {
		return err
	}
	lg, err := server.NewLogger(cfg.Log, filepath.Join(dir, "log"))
	if err != nil {
		return err
	}
	defer lg.Close()

	srv := server.New(cfg, caps, lg)
	fmt.Fprintf(os.Stderr, "bridge listening on http://%s (mode: %s, model: %s)\n", *addr, srv.Mode(), cfg.Model)
	fmt.Fprintf(os.Stderr, "point Claude Code at it with:\n")
	fmt.Fprintf(os.Stderr, "  ANTHROPIC_BASE_URL=http://%s ANTHROPIC_AUTH_TOKEN=local-vllm claude\n", *addr)
	return http.ListenAndServe(*addr, srv.Handler())
}
```

- [ ] **Step 6: Run the full suite**

Run: `cd airgap-vllm && gofmt -w ./... && go vet ./... && go test ./... -v`
Expected: PASS across every package, and the `cmd` package now builds.

- [ ] **Step 7: Verify the Windows cross-compile**

Run: `cd airgap-vllm && CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o /tmp/claude-vllm.exe ./cmd/claude-vllm && ls -la /tmp/claude-vllm.exe`
Expected: a PE executable of roughly 6 MB.

- [ ] **Step 8: Commit**

```bash
git add airgap-vllm/internal/cli/ airgap-vllm/internal/probe/ airgap-vllm/internal/fakevllm/
git commit -m "feat(airgap-vllm): doctor diagnostics with tool-calling smoke test"
```

---

### Task 13: Staging script for the connected machine

Produces the transferable bundle. It must refuse to emit anything if verification fails — a silently unverified binary is worse than no bundle.

**Files:**
- Create: `airgap-vllm/scripts/stage.sh`
- Test: manual, steps below.

**Interfaces:**
- Consumes: the Go module from Tasks 1-12.
- Produces: `dist/claude-vllm-airgap-<version>.zip` containing `claude.exe`, `claude-vllm.exe`, `install.ps1`, `README.md`, `SHA256SUMS`.

- [ ] **Step 1: Write the script**

Create `airgap-vllm/scripts/stage.sh`:

```bash
#!/usr/bin/env bash
# Build an air-gap bundle on an internet-connected machine.
#
#   ./scripts/stage.sh [claude-code-version]
#
# Verifies the Claude Code release with GPG and SHA256, cross-compiles the
# bridge for Windows x64, and emits a zip plus a checksum manifest. Aborts on
# any verification failure rather than shipping an unverified artifact.
set -euo pipefail

VERSION="${1:-2.1.252}"
PLATFORM="win32-x64"
BINARY="claude.exe"
FINGERPRINT="31DDDE24DDFAB679F42D7BD2BAA929FF1A7ECACE"
REPO="https://downloads.claude.ai/claude-code-releases"
KEY_URL="https://downloads.claude.ai/keys/claude-code.asc"

here="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work="$(mktemp -d)"
out="$here/dist"
trap 'rm -rf "$work"' EXIT

for tool in curl gpg shasum zip go; do
  command -v "$tool" >/dev/null || { echo "missing required tool: $tool" >&2; exit 1; }
done

echo "==> Importing the release signing key"
curl -fsSL "$KEY_URL" | gpg --quiet --import
if ! gpg --fingerprint security@anthropic.com 2>/dev/null \
     | tr -d ' \n' | grep -qi "$FINGERPRINT"; then
  echo "FATAL: signing key fingerprint does not match $FINGERPRINT" >&2
  exit 1
fi
echo "    fingerprint verified"

echo "==> Downloading and verifying manifest for $VERSION"
curl -fsSL -o "$work/manifest.json"     "$REPO/$VERSION/manifest.json"
curl -fsSL -o "$work/manifest.json.sig" "$REPO/$VERSION/manifest.json.sig"
gpg --verify "$work/manifest.json.sig" "$work/manifest.json" 2>&1 | grep -q "Good signature" || {
  echo "FATAL: manifest signature check failed" >&2
  exit 1
}
echo "    manifest signature good"

expected="$(python3 -c "
import json,sys
m=json.load(open('$work/manifest.json'))
print(m['platforms']['$PLATFORM']['checksum'])
")"
[ -n "$expected" ] || { echo "FATAL: no checksum for $PLATFORM in manifest" >&2; exit 1; }

echo "==> Downloading $BINARY ($PLATFORM)"
curl -fSL --progress-bar -o "$work/$BINARY" "$REPO/$VERSION/$PLATFORM/$BINARY"
actual="$(shasum -a 256 "$work/$BINARY" | awk '{print $1}')"
if [ "$actual" != "$expected" ]; then
  echo "FATAL: checksum mismatch" >&2
  echo "  expected $expected" >&2
  echo "  actual   $actual" >&2
  exit 1
fi
echo "    checksum verified: $actual"

echo "==> Cross-compiling the bridge for windows/amd64"
( cd "$here" && CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
    go build -trimpath -ldflags "-s -w" -o "$work/claude-vllm.exe" ./cmd/claude-vllm )

cp "$here/install.ps1" "$work/install.ps1"
cp "$here/README.md"   "$work/README.md"

echo "==> Writing SHA256SUMS"
( cd "$work" && shasum -a 256 claude.exe claude-vllm.exe install.ps1 README.md > SHA256SUMS )

mkdir -p "$out"
bundle="$out/claude-vllm-airgap-$VERSION.zip"
rm -f "$bundle"
( cd "$work" && zip -q -r "$bundle" claude.exe claude-vllm.exe install.ps1 README.md SHA256SUMS )

echo
echo "Bundle ready: $bundle"
shasum -a 256 "$bundle"
echo
echo "Transfer it to the air-gapped machine, then run install.ps1 from PowerShell."
```

- [ ] **Step 2: Make it executable and lint it**

```bash
chmod +x airgap-vllm/scripts/stage.sh
shellcheck airgap-vllm/scripts/stage.sh || true   # advisory; shellcheck may be absent
bash -n airgap-vllm/scripts/stage.sh              # must report no syntax errors
```

- [ ] **Step 3: Verify the failure path first**

Run with a version that does not exist and confirm it aborts rather than producing a bundle:

```bash
cd airgap-vllm && ./scripts/stage.sh 0.0.0-nonexistent ; echo "exit=$?"
ls dist/ 2>/dev/null
```
Expected: non-zero exit, no `dist/claude-vllm-airgap-0.0.0-nonexistent.zip`.

- [ ] **Step 4: Run the real staging**

Run: `cd airgap-vllm && ./scripts/stage.sh 2.1.252`
Expected: every verification line prints, checksum matches `fe688ecde90d65fff0b2dd097597439774116077d80a3c1e131a7e47db307b1a`, and `dist/claude-vllm-airgap-2.1.252.zip` appears.

- [ ] **Step 5: Commit**

```bash
echo "dist/" >> airgap-vllm/.gitignore
git add airgap-vllm/scripts/stage.sh airgap-vllm/.gitignore
git commit -m "feat(airgap-vllm): staging script with GPG and checksum verification"
```

---

### Task 14: Offline installer and operator runbook

**Files:**
- Create: `airgap-vllm/install.ps1`
- Create: `airgap-vllm/README.md`
- Test: manual, steps below.

**Interfaces:**
- Consumes: the bundle produced by Task 13.
- Produces: an installed `%USERPROFILE%\.local\bin\{claude.exe,claude-vllm.exe}` on PATH.

- [ ] **Step 1: Write the installer**

Create `airgap-vllm/install.ps1`:

```powershell
<#
.SYNOPSIS
  Install Claude Code and the vLLM bridge on an air-gapped Windows machine.
.DESCRIPTION
  Verifies SHA256SUMS locally, checks the Authenticode signature on claude.exe,
  installs both binaries to %USERPROFILE%\.local\bin, and adds that directory to
  the user PATH. Requires no network and no administrator rights.
#>
[CmdletBinding()]
param(
    [string]$InstallDir = (Join-Path $env:USERPROFILE ".local\bin"),
    [switch]$SkipSignatureCheck
)

$ErrorActionPreference = "Stop"
$source = Split-Path -Parent $MyInvocation.MyCommand.Path

function Assert-Checksums {
    $manifest = Join-Path $source "SHA256SUMS"
    if (-not (Test-Path $manifest)) { throw "SHA256SUMS not found next to install.ps1" }

    foreach ($line in Get-Content $manifest) {
        if ($line -notmatch '^\s*([0-9a-fA-F]{64})\s+\*?(.+?)\s*$') { continue }
        $expected = $Matches[1].ToLower()
        $file     = Join-Path $source $Matches[2]
        if (-not (Test-Path $file)) { throw "listed in SHA256SUMS but missing: $($Matches[2])" }
        $actual = (Get-FileHash $file -Algorithm SHA256).Hash.ToLower()
        if ($actual -ne $expected) {
            throw "checksum mismatch for $($Matches[2])`n  expected $expected`n  actual   $actual"
        }
        Write-Host "  ok  $($Matches[2])"
    }
}

function Assert-Signature {
    param([string]$Path)
    $sig = Get-AuthenticodeSignature $Path
    if ($sig.Status -ne "Valid") {
        throw "Authenticode signature on $Path is $($sig.Status). Re-transfer the bundle."
    }
    Write-Host "  ok  signed by $($sig.SignerCertificate.Subject)"
}

Write-Host "==> Verifying bundle checksums"
Assert-Checksums

if (-not $SkipSignatureCheck) {
    Write-Host "==> Verifying the Claude Code code signature"
    Assert-Signature (Join-Path $source "claude.exe")
}

Write-Host "==> Installing to $InstallDir"
New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
foreach ($exe in @("claude.exe", "claude-vllm.exe")) {
    Copy-Item (Join-Path $source $exe) (Join-Path $InstallDir $exe) -Force
    Write-Host "  installed $exe"
}

$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
if ($userPath -notlike "*$InstallDir*") {
    [Environment]::SetEnvironmentVariable("Path", "$userPath;$InstallDir", "User")
    Write-Host "  added $InstallDir to your user PATH (restart your terminal)"
} else {
    Write-Host "  $InstallDir already on PATH"
}

# This variable forces Cloud-gateway sign-in and breaks every request.
if ($env:CLAUDE_CODE_USE_GATEWAY) {
    Write-Warning "CLAUDE_CODE_USE_GATEWAY is set in your environment. Unset it: it forces Cloud sign-in and fails every request."
}

Write-Host ""
Write-Host "Installed. Next steps:" -ForegroundColor Green
Write-Host "  1. Open a new terminal"
Write-Host "  2. claude-vllm setup     # point at your vLLM server and pick a model"
Write-Host "  3. claude-vllm doctor    # verify connectivity and tool calling"
Write-Host "  4. claude-vllm           # start Claude Code"
```

- [ ] **Step 2: Syntax-check the installer**

On any machine with PowerShell available:

```bash
pwsh -NoProfile -Command "
  \$null = [System.Management.Automation.Language.Parser]::ParseFile(
    'airgap-vllm/install.ps1', [ref]\$null, [ref]\$errors)
  if (\$errors) { \$errors; exit 1 } else { 'parse ok' }"
```
Expected: `parse ok`. If `pwsh` is unavailable, note that this check must run on the Windows box before distribution.

- [ ] **Step 3: Write the runbook**

Create `airgap-vllm/README.md`:

````markdown
# Claude Code on a self-hosted vLLM, air-gapped

Runs Claude Code against an internally hosted vLLM server on a Windows machine
with no internet access and no sign-in.

## How it works

```
claude.exe ──Anthropic Messages──▶ claude-vllm.exe ──▶ your vLLM
 (native)                          (127.0.0.1)          │
                                                        ├─ passthrough → /v1/messages
                                                        └─ translate   → /v1/chat/completions
```

`claude-vllm.exe` starts a bridge on loopback, points Claude Code at it, and
exits when Claude Code exits. It probes your vLLM once at startup: if the build
exposes a native Anthropic `/v1/messages` it forwards requests nearly unchanged,
otherwise it translates to and from the OpenAI chat completions API.

## On a connected machine

```bash
./scripts/stage.sh 2.1.252
```

This verifies the Claude Code release against its GPG-signed manifest, downloads
the Windows binary, checks its SHA256, cross-compiles the bridge, and writes
`dist/claude-vllm-airgap-2.1.252.zip`. It aborts rather than emitting an
unverified bundle.

## On the air-gapped machine

Unzip the bundle, then from PowerShell:

```powershell
.\install.ps1
claude-vllm setup      # enter the vLLM URL, pick a model from the list it fetches
claude-vllm doctor     # verify connectivity and tool calling
claude-vllm            # start Claude Code
```

`install.ps1` re-verifies checksums and the Authenticode signature locally. It
needs no network and no administrator rights.

## Configuration

Settings live in `%USERPROFILE%\.claude-vllm\config.json`. Change them with:

```powershell
claude-vllm config set url=http://10.0.0.5:8000
claude-vllm config set model=Qwen/Qwen3.8-27B
claude-vllm config show
```

| Key | Meaning |
|---|---|
| `url` | vLLM base URL |
| `model` | Model id, exactly as vLLM reports it in `/v1/models` |
| `api_key` | Only if vLLM was started with `--api-key` |
| `mode` | `auto` (default), `messages`, or `openai` to force a lane |
| `max_tokens` | Default response cap |
| `context_window` | Must match what vLLM was launched with |
| `supports_thinking` | Leave false unless your server runs a reasoning parser |
| `log` | `off`, `requests`, or `full` |

Every key is also settable as an environment variable: `CLAUDE_VLLM_URL`,
`CLAUDE_VLLM_MODEL`, and so on.

You can also switch models from inside a session with `/model` — the bridge
serves `/v1/models`, so the picker lists whatever your vLLM is currently serving.

> **`log = full` writes complete prompts and response bodies to
> `%USERPROFILE%\.claude-vllm\log\`.** That includes source code from your
> sessions. Enable it only while debugging, and clear the directory afterwards.

## Requirements on the vLLM side

Claude Code is extremely tool-heavy. vLLM must be started with automatic tool
choice and a parser appropriate for your model:

```bash
vllm serve <MODEL> --enable-auto-tool-choice --tool-call-parser <PARSER>
```

The correct parser is model-specific. `claude-vllm doctor` runs a live
tool-calling smoke test and tells you if this is wrong — run it first, before
concluding anything else is broken.

## Troubleshooting

| Symptom | Cause and fix |
|---|---|
| `cannot reach vLLM at ...` | Wrong URL or server down. `claude-vllm config set url=...` |
| `configured model is not served` | `claude-vllm doctor` lists what is served; pick one |
| Model replies in prose instead of calling tools | vLLM missing `--enable-auto-tool-choice` or the wrong `--tool-call-parser` |
| `Not signed in to the Cloud gateway` | `CLAUDE_CODE_USE_GATEWAY` is set in your environment. Unset it |
| HTTP 400 from vLLM | Set `log=full`, reproduce, and read the logged request body |
| Sign-in prompt appears | `~/.claude.json` is missing `hasCompletedOnboarding`. Re-run `claude-vllm setup` |

## Updating Claude Code

Auto-update is disabled by design (`DISABLE_UPDATES`). To move to a new version,
re-run `stage.sh` with the new version number on a connected machine and
reinstall. Re-run `claude-vllm doctor` afterwards: a new Claude Code release can
change the request shape the bridge filters.
````

- [ ] **Step 4: Verify the full bundle end to end**

```bash
cd airgap-vllm && ./scripts/stage.sh 2.1.252
unzip -l dist/claude-vllm-airgap-2.1.252.zip
```
Expected: five entries — `claude.exe`, `claude-vllm.exe`, `install.ps1`, `README.md`, `SHA256SUMS`.

- [ ] **Step 5: Commit**

```bash
git add airgap-vllm/install.ps1 airgap-vllm/README.md
git commit -m "feat(airgap-vllm): offline installer and operator runbook"
```

---

## Acceptance

The work is done when all of the following hold:

1. `cd airgap-vllm && go vet ./... && go test ./...` passes with no third-party modules in `go.mod`.
2. `CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./cmd/claude-vllm` produces a single static `.exe`.
3. `./scripts/stage.sh 2.1.252` verifies the GPG signature and checksum and emits the bundle, and aborts on a bad version.
4. On the target Windows machine: `install.ps1` verifies and installs, `claude-vllm setup` lists the models the real server serves, `claude-vllm doctor` reports all checks passing including the live tool-calling smoke test, and `claude-vllm` opens a working Claude Code session with no sign-in prompt.

Item 4 cannot be verified from this repository. It is the operator's acceptance
step and depends on the real vLLM deployment and the model's tool-calling
behavior.
