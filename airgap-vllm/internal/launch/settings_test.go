package launch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
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

func TestWriteSettingsEnforcesRestrictivePermissionsOnExistingFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits; Chmod on Windows only toggles the read-only bit")
	}
	p := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(p, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := WriteSettings(p, testCfg()); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("mode = %o, want 0600 — a pre-existing looser mode must not survive a settings file that can hold an api key", mode)
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
