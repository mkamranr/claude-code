package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// logFilePath reconstructs the path NewLogger uses for today's log file.
func logFilePath(dir string) string {
	return filepath.Join(dir, time.Now().Format("2006-01-02")+".log")
}

func TestLoggerOffCreatesNothingAndWritesNothing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs") // does not exist yet
	lg, err := NewLogger("off", dir)
	if err != nil {
		t.Fatal(err)
	}
	defer lg.Close()

	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("NewLogger(\"off\", ...) created %s, want it to stay absent", dir)
	}

	lg.Note("this must not appear anywhere")
	lg.Body("label", []byte("this must not appear anywhere either"))

	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("logging in mode off created %s, want no directory on disk", dir)
	}
}

func TestLoggerRequestsWritesNotesButNotBodies(t *testing.T) {
	dir := t.TempDir()
	lg, err := NewLogger("requests", dir)
	if err != nil {
		t.Fatal(err)
	}

	lg.Note("filter: %s", "dropped a block")
	lg.Body("claude->bridge", []byte("top-secret-prompt-content"))
	lg.Close()

	b, err := os.ReadFile(logFilePath(dir))
	if err != nil {
		t.Fatalf("reading log file: %v", err)
	}
	content := string(b)
	if !strings.Contains(content, "filter: dropped a block") {
		t.Errorf("log = %q, want the Note output present", content)
	}
	if strings.Contains(content, "top-secret-prompt-content") {
		t.Errorf("log = %q, want Body to write nothing in mode requests", content)
	}
}

func TestLoggerFullWritesNotesAndBodies(t *testing.T) {
	dir := t.TempDir()
	lg, err := NewLogger("full", dir)
	if err != nil {
		t.Fatal(err)
	}

	lg.Note("filter: %s", "dropped a block")
	lg.Body("claude->bridge", []byte("top-secret-prompt-content"))
	lg.Close()

	b, err := os.ReadFile(logFilePath(dir))
	if err != nil {
		t.Fatalf("reading log file: %v", err)
	}
	content := string(b)
	if !strings.Contains(content, "filter: dropped a block") {
		t.Errorf("log = %q, want the Note output present", content)
	}
	if !strings.Contains(content, "claude->bridge") || !strings.Contains(content, "top-secret-prompt-content") {
		t.Errorf("log = %q, want the full body (label and content) present in mode full", content)
	}
}
