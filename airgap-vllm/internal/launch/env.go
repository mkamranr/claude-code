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

// strippedExact are inherited variables that must not reach the child.
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

// strippedByPattern matches inherited variables by shape rather than by exact
// name, because the tier is part of the name:
// ANTHROPIC_DEFAULT_<TIER>_MODEL_SUPPORTED_CAPABILITIES (and the older
// _MODEL_SUPPORTS spelling) override Claude Code's own capability detection
// for whichever tier they name. An inherited value is a guess made somewhere
// else about a different model, and it silently wins over detection — the
// exact failure the design set out to avoid by never setting these itself.
func strippedByPattern(name string) bool {
	if !strings.HasPrefix(name, "ANTHROPIC_DEFAULT_") {
		return false
	}
	return strings.HasSuffix(name, "_MODEL_SUPPORTED_CAPABILITIES") ||
		strings.HasSuffix(name, "_MODEL_SUPPORTS")
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
		if strippedExact[parts[0]] || strippedByPattern(parts[0]) {
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
	// ANTHROPIC_DEFAULT_*_MODEL_SUPPORTED_CAPABILITIES is deliberately not set,
	// and is stripped from the inherited environment above. It overrides Claude
	// Code's own capability detection; guessing its accepted token set is worse
	// than letting detection run.

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
