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
