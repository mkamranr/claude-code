package desktop

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProjectDirEncoding(t *testing.T) {
	got := filepath.Base(ProjectDir("H", `C:\Projects\claude-code`))
	if got != "C--Projects-claude-code" {
		t.Fatalf("encoded = %q", got)
	}
	got = filepath.Base(ProjectDir("H", `C:\Users\MUHAMM~1.RAF\work`))
	if got != "C--Users-MUHAMM-1-RAF-work" {
		t.Fatalf("encoded = %q", got)
	}
}

const sessionA = `{"type":"queue-operation","operation":"enqueue"}
{"type":"user","isMeta":true,"message":{"role":"user","content":"<command-name>/init</command-name>"}}
{"type":"user","message":{"role":"user","content":"<system-reminder>ctx</system-reminder>"}}
{"type":"user","message":{"role":"user","content":"Add a   login page\nwith OAuth"}}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Write","input":{}}]}}
{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}}
{"type":"assistant","isSidechain":true,"message":{"role":"assistant","content":[{"type":"text","text":"subagent"}]}}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Done."}]}}
broken line
`

func writeSession(t *testing.T, home, folder, id, body string, mtime time.Time) {
	t.Helper()
	dir := ProjectDir(home, folder)
	os.MkdirAll(dir, 0o700)
	p := filepath.Join(dir, id+".jsonl")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	os.Chtimes(p, mtime, mtime)
}

func TestListSessionsNewestFirstWithTitles(t *testing.T) {
	home, folder := t.TempDir(), `C:\work\app`
	now := time.Now()
	writeSession(t, home, folder, "aaaaaaaa-0000-0000-0000-000000000001", sessionA, now.Add(-time.Hour))
	writeSession(t, home, folder, "aaaaaaaa-0000-0000-0000-000000000002",
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"Fix the build"}]}}`, now)
	writeSession(t, home, folder, "aaaaaaaa-0000-0000-0000-000000000003",
		`{"type":"queue-operation"}`, now) // nothing typed: skipped
	writeSession(t, home, folder, "not-a-session!", sessionA, now)

	got, err := ListSessions(home, folder)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d sessions: %+v", len(got), got)
	}
	if got[0].Title != "Fix the build" || got[1].Title != "Add a login page with OAuth" {
		t.Fatalf("titles/order = %q, %q", got[0].Title, got[1].Title)
	}
}

func TestListSessionsMissingFolderIsEmpty(t *testing.T) {
	got, err := ListSessions(t.TempDir(), `C:\nowhere`)
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestLoadTranscriptKeepsMainThreadMessages(t *testing.T) {
	home, folder := t.TempDir(), `C:\work\app`
	id := "aaaaaaaa-0000-0000-0000-000000000001"
	writeSession(t, home, folder, id, sessionA, time.Now())

	msgs, err := LoadTranscript(home, folder, id)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, m := range msgs {
		s := string(m)
		switch {
		case strings.Contains(s, "login page"):
			kinds = append(kinds, "prompt")
		case strings.Contains(s, "tool_result"):
			kinds = append(kinds, "tool_result")
		case strings.Contains(s, "tool_use"):
			kinds = append(kinds, "tool_use")
		case strings.Contains(s, "Done."):
			kinds = append(kinds, "text")
		default:
			t.Errorf("unexpected message kept: %s", s)
		}
		if !strings.Contains(s, `"replay":true`) {
			t.Errorf("message not marked as replay: %s", s)
		}
	}
	if strings.Join(kinds, ",") != "prompt,tool_use,tool_result,text" {
		t.Fatalf("kinds = %v", kinds)
	}
}

func TestLoadTranscriptRejectsPathTraversal(t *testing.T) {
	if _, err := LoadTranscript(t.TempDir(), `C:\x`, `..\..\secret`); err == nil {
		t.Fatal("accepted a traversal id")
	}
}
