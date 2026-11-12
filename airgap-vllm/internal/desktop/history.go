package desktop

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Claude Code stores each session as ~/.claude/projects/<encoded cwd>/<id>.jsonl,
// where the cwd is encoded by replacing every non-alphanumeric character
// with '-' (C:\Projects\app -> C--Projects-app).
var nonAlnum = regexp.MustCompile(`[^A-Za-z0-9]`)

var sessionIDPattern = regexp.MustCompile(`^[0-9a-fA-F-]{8,64}$`)

func validSessionID(id string) bool { return sessionIDPattern.MatchString(id) }

// ProjectDir is where Claude Code keeps the sessions for folder.
func ProjectDir(home, folder string) string {
	return filepath.Join(home, ".claude", "projects", nonAlnum.ReplaceAllString(folder, "-"))
}

// SessionInfo is one entry in the history sidebar.
type SessionInfo struct {
	ID       string    `json:"id"`
	Title    string    `json:"title"`
	Modified time.Time `json:"modified"`
}

// record is the subset of a session .jsonl line the desktop app reads.
type record struct {
	Type        string          `json:"type"`
	IsSidechain bool            `json:"isSidechain"`
	IsMeta      bool            `json:"isMeta"`
	Message     json.RawMessage `json:"message"`
}

// ListSessions returns the folder's sessions, newest first. Sessions with no
// user prompt (nothing to resume) are skipped.
func ListSessions(home, folder string) ([]SessionInfo, error) {
	dir := ProjectDir(home, folder)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return []SessionInfo{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []SessionInfo{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		id := strings.TrimSuffix(name, ".jsonl")
		if !validSessionID(id) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		title := sessionTitle(filepath.Join(dir, name))
		if title == "" {
			continue
		}
		out = append(out, SessionInfo{ID: id, Title: title, Modified: info.ModTime()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Modified.After(out[j].Modified) })
	return out, nil
}

// sessionTitle is the first prompt the person typed, trimmed for display.
func sessionTitle(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 64<<20)
	for sc.Scan() {
		var r record
		if json.Unmarshal(sc.Bytes(), &r) != nil || r.Type != "user" || r.IsSidechain || r.IsMeta {
			continue
		}
		if t := promptText(r.Message); t != "" {
			t = strings.Join(strings.Fields(t), " ")
			if len([]rune(t)) > 80 {
				t = string([]rune(t)[:79]) + "…"
			}
			return t
		}
	}
	return ""
}

// promptText extracts typed text from a user message, ignoring tool results
// and the <tag>-wrapped context Claude Code injects.
func promptText(raw json.RawMessage) string {
	var m struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	var s string
	if json.Unmarshal(m.Content, &s) == nil {
		if strings.HasPrefix(strings.TrimSpace(s), "<") {
			return ""
		}
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(m.Content, &blocks) != nil {
		return ""
	}
	for _, b := range blocks {
		if b.Type == "text" && !strings.HasPrefix(strings.TrimSpace(b.Text), "<") {
			return b.Text
		}
	}
	return ""
}

// LoadTranscript returns the main-thread user and assistant messages of a
// session, shaped like the live stream-json events ({"type":..., "message":...})
// so the UI renders history and live turns with the same code.
func LoadTranscript(home, folder, id string) ([]json.RawMessage, error) {
	if !validSessionID(id) {
		return nil, errors.New("invalid session id")
	}
	f, err := os.Open(filepath.Join(ProjectDir(home, folder), id+".jsonl"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := []json.RawMessage{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 64<<20)
	for sc.Scan() {
		var r record
		if json.Unmarshal(sc.Bytes(), &r) != nil || r.IsSidechain || r.IsMeta || len(r.Message) == 0 {
			continue
		}
		if r.Type != "user" && r.Type != "assistant" {
			continue
		}
		if r.Type == "user" && promptText(r.Message) == "" && !hasToolResult(r.Message) {
			continue
		}
		ev, _ := json.Marshal(map[string]any{"type": r.Type, "message": r.Message, "replay": true})
		out = append(out, ev)
	}
	return out, sc.Err()
}

func hasToolResult(raw json.RawMessage) bool {
	var m struct {
		Content []struct {
			Type string `json:"type"`
		} `json:"content"`
	}
	if json.Unmarshal(raw, &m) != nil {
		return false
	}
	for _, b := range m.Content {
		if b.Type == "tool_result" {
			return true
		}
	}
	return false
}
