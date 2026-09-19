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
