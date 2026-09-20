package launch

import (
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

// Capability overrides must neither be set by the launcher nor inherited: they
// override Claude Code's own detection for the tier they name, and a value
// left in the environment is a guess made elsewhere about a different model.
// Both the current name (_SUPPORTED_CAPABILITIES, verified against the pinned
// 2.1.252 binary) and an older variant (_MODEL_SUPPORTS) are guarded against,
// so the test also catches a future accidental reintroduction under either
// spelling.
//
// base carries them so this proves stripping, not merely that Env declines to
// set them; PATH is along to prove the pattern match is not eating everything.
func TestEnvStripsCapabilityOverrides(t *testing.T) {
	base := []string{
		"ANTHROPIC_DEFAULT_OPUS_MODEL_SUPPORTED_CAPABILITIES=thinking",
		"ANTHROPIC_DEFAULT_SONNET_MODEL_SUPPORTED_CAPABILITIES=thinking",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL_SUPPORTS=thinking",
		"PATH=/usr/bin",
	}
	m := envMap(t, Env(testCfg(), 1, base))
	for k := range m {
		if strings.Contains(k, "SUPPORTED_CAPABILITIES") || strings.Contains(k, "MODEL_SUPPORTS") {
			t.Errorf("%s reached the child; it overrides Claude Code's own detection", k)
		}
	}
	if m["PATH"] != "/usr/bin" {
		t.Error("unrelated inherited variables must survive the pattern strip")
	}
	// The tier names themselves must still be set, or background Haiku-tier
	// calls 404 — the strip must not have taken the ANTHROPIC_DEFAULT_* family
	// with it.
	if m["ANTHROPIC_DEFAULT_HAIKU_MODEL"] == "" {
		t.Error("ANTHROPIC_DEFAULT_HAIKU_MODEL was stripped along with the capability overrides")
	}
}

func TestEnvSetsOfflineSwitches(t *testing.T) {
	m := envMap(t, Env(testCfg(), 1, nil))
	for _, k := range []string{
		"DISABLE_UPDATES", "DISABLE_AUTOUPDATER", "DISABLE_TELEMETRY",
		"DISABLE_ERROR_REPORTING", "DISABLE_BUG_COMMAND", "DISABLE_COST_WARNINGS",
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC",
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
