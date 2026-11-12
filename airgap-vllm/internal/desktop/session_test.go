package desktop

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"claude-vllm/internal/config"
	"claude-vllm/internal/fakevllm"
)

// The test binary doubles as a fake claude.exe when FAKE_CLAUDE is set: it
// appends its argv to FAKE_CLAUDE_ARGS, then answers each stream-json user
// turn with init, assistant and result events. A turn whose text is "hang"
// gets no answer, so Stop has something to interrupt.
func TestMain(m *testing.M) {
	if os.Getenv("FAKE_CLAUDE") == "1" {
		fakeClaude()
		return
	}
	os.Exit(m.Run())
}

func fakeClaude() {
	if f, err := os.OpenFile(os.Getenv("FAKE_CLAUDE_ARGS"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
		b, _ := json.Marshal(os.Args[1:])
		f.Write(append(b, '\n'))
		f.Close()
	}
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		var msg struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		}
		json.Unmarshal(sc.Bytes(), &msg)
		if msg.Message.Content == "hang" {
			time.Sleep(time.Hour)
		}
		fmt.Println(`{"type":"system","subtype":"init","session_id":"11111111-2222-3333-4444-555555555555"}`)
		fmt.Println(`not json at all`)
		fmt.Printf(`{"type":"assistant","message":{"content":[{"type":"text","text":"echo: %s"}]}}`+"\n", msg.Message.Content)
		fmt.Println(`{"type":"result","subtype":"success","is_error":false}`)
	}
}

type recorder struct {
	mu     sync.Mutex
	events []recorded
	ch     chan recorded
}

type recorded struct {
	name string
	data any
}

func newRecorder() *recorder { return &recorder{ch: make(chan recorded, 100)} }

func (r *recorder) Emit(name string, data any) {
	r.mu.Lock()
	r.events = append(r.events, recorded{name, data})
	r.mu.Unlock()
	r.ch <- recorded{name, data}
}

// waitFor returns the first event matching pred, failing after a timeout.
func (r *recorder) waitFor(t *testing.T, what string, pred func(recorded) bool) recorded {
	t.Helper()
	deadline := time.After(20 * time.Second)
	for {
		select {
		case ev := <-r.ch:
			if pred(ev) {
				return ev
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func isResult(ev recorded) bool {
	raw, ok := ev.data.(json.RawMessage)
	return ev.name == EventClaude && ok && strings.Contains(string(raw), `"type":"result"`)
}

func newTestSession(t *testing.T, resumeID string) (*Session, *recorder, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	argsFile := home + "/args.jsonl"
	t.Setenv("FAKE_CLAUDE", "1")
	t.Setenv("FAKE_CLAUDE_ARGS", argsFile)

	fv := fakevllm.New(fakevllm.Config{MessagesAPI: true, Models: []string{"m1"}})
	t.Cleanup(fv.Close)
	cfg := config.Default()
	cfg.VLLMURL = fv.URL()
	cfg.Model = "m1"

	rec := newRecorder()
	exe, _ := os.Executable()
	s, err := NewSession(cfg, SessionOptions{Folder: t.TempDir(), ResumeID: resumeID}, exe, rec)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(s.Close)
	return s, rec, argsFile
}

func readArgs(t *testing.T, path string) [][]string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading recorded args: %v", err)
	}
	var out [][]string
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var a []string
		json.Unmarshal([]byte(line), &a)
		out = append(out, a)
	}
	return out
}

func argValue(args []string, flag string) (string, bool) {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1], true
		}
	}
	return "", false
}

func TestSessionMultiTurnInOneProcess(t *testing.T) {
	s, rec, argsFile := newTestSession(t, "")

	for _, text := range []string{"first", "second"} {
		if err := s.Send(text); err != nil {
			t.Fatalf("Send(%q): %v", text, err)
		}
		rec.waitFor(t, "result for "+text, isResult)
	}

	if got := s.SessionID(); got != "11111111-2222-3333-4444-555555555555" {
		t.Fatalf("session id = %q", got)
	}
	runs := readArgs(t, argsFile)
	if len(runs) != 1 {
		t.Fatalf("claude started %d times for two turns, want 1", len(runs))
	}
	args := runs[0]
	if v, _ := argValue(args, "--permission-prompt-tool"); v != PermToolName {
		t.Fatalf("--permission-prompt-tool = %q", v)
	}
	if v, _ := argValue(args, "--permission-mode"); v != "manual" {
		t.Fatalf("--permission-mode = %q, want manual by default", v)
	}
	if _, ok := argValue(args, "--resume"); ok {
		t.Fatal("first start must not pass --resume")
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	sawRaw := false
	for _, ev := range rec.events {
		if m, ok := ev.data.(map[string]any); ok && m["type"] == "raw" {
			sawRaw = true
		}
	}
	if !sawRaw {
		t.Fatal("non-JSON stdout line was dropped instead of surfaced as a raw event")
	}
}

func TestSessionStopThenSendResumes(t *testing.T) {
	s, rec, argsFile := newTestSession(t, "")

	if err := s.Send("first"); err != nil {
		t.Fatal(err)
	}
	rec.waitFor(t, "first result", isResult)

	if err := s.Send("hang"); err != nil {
		t.Fatal(err)
	}
	s.Stop()
	exit := rec.waitFor(t, "exit event", func(ev recorded) bool { return ev.name == EventExit })
	if info := exit.data.(ExitInfo); !info.Expected {
		t.Fatalf("exit after Stop reported as unexpected: %+v", info)
	}

	if err := s.Send("again"); err != nil {
		t.Fatalf("Send after Stop: %v", err)
	}
	rec.waitFor(t, "result after resume", isResult)

	runs := readArgs(t, argsFile)
	if len(runs) != 2 {
		t.Fatalf("claude started %d times, want 2", len(runs))
	}
	if v, ok := argValue(runs[1], "--resume"); !ok || v != "11111111-2222-3333-4444-555555555555" {
		t.Fatalf("restart args %v do not resume the session", runs[1])
	}
}

// Switching conversations closes the old session while its claude.exe is
// still idling between turns. Its exit must not surface in the new chat.
func TestClosedSessionEmitsNothingMore(t *testing.T) {
	s, rec, _ := newTestSession(t, "")
	if err := s.Send("first"); err != nil {
		t.Fatal(err)
	}
	rec.waitFor(t, "result", isResult)

	rec.mu.Lock()
	before := len(rec.events)
	rec.mu.Unlock()
	s.Close()
	time.Sleep(300 * time.Millisecond) // let the exit goroutine run

	rec.mu.Lock()
	defer rec.mu.Unlock()
	for _, ev := range rec.events[before:] {
		t.Errorf("closed session emitted %s: %v", ev.name, ev.data)
	}
}

func TestSessionResumeIDPassedOnFirstStart(t *testing.T) {
	id := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	s, rec, argsFile := newTestSession(t, id)
	if err := s.Send("hi"); err != nil {
		t.Fatal(err)
	}
	rec.waitFor(t, "result", isResult)
	if v, _ := argValue(readArgs(t, argsFile)[0], "--resume"); v != id {
		t.Fatalf("--resume = %q, want %q", v, id)
	}
}

func TestNewSessionRejectsBadOptions(t *testing.T) {
	cfg := config.Default()
	cases := []SessionOptions{
		{Folder: t.TempDir() + "/missing"},
		{Folder: t.TempDir(), PermissionMode: "bypassPermissions"},
		{Folder: t.TempDir(), ResumeID: "../../etc/passwd"},
	}
	for _, o := range cases {
		if _, err := NewSession(cfg, o, "claude", newRecorder()); err == nil {
			t.Errorf("NewSession(%+v) accepted bad options", o)
		}
	}
}

func TestClaudeArgs(t *testing.T) {
	args := ClaudeArgs(`{"mcpServers":{}}`, "plan", "")
	for _, want := range []string{"-p", "--verbose", "--include-partial-messages"} {
		found := false
		for _, a := range args {
			found = found || a == want
		}
		if !found {
			t.Errorf("args missing %s: %v", want, args)
		}
	}
	if v, _ := argValue(args, "--input-format"); v != "stream-json" {
		t.Errorf("--input-format = %q", v)
	}
	if v, _ := argValue(args, "--output-format"); v != "stream-json" {
		t.Errorf("--output-format = %q", v)
	}
	if v, _ := argValue(args, "--permission-mode"); v != "plan" {
		t.Errorf("--permission-mode = %q", v)
	}
}
