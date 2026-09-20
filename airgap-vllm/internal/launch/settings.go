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
//
// cfg is deliberately unused. The one per-run value the launcher holds is the
// ephemeral loopback port, and settings.json outlives the run: persisting it
// would point the next session at a port nothing is listening on. Everything
// derived from cfg belongs in the child environment (see Env), not on disk.
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
		"DISABLE_ERROR_REPORTING", "DISABLE_BUG_COMMAND", "DISABLE_COST_WARNINGS",
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
	// os.WriteFile only applies the mode argument when it creates the file; an
	// existing file (settings.json a developer or another tool already left at
	// a looser mode) keeps its old permissions. This file can hold secrets
	// (an API key can end up under "env"), so the mode is enforced explicitly.
	if err := os.WriteFile(path, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}
