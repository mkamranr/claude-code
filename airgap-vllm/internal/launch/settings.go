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
