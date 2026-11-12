package desktop

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"claude-vllm/internal/config"
	"claude-vllm/internal/launch"
)

// Emitter delivers backend events to the UI. Wails' runtime.EventsEmit is the
// production implementation; tests record calls.
type Emitter interface {
	Emit(event string, data any)
}

// Event names the UI listens for.
const (
	EventClaude     = "claude:event"      // one stream-json object from claude.exe
	EventExit       = "claude:exit"       // claude.exe exited; payload ExitInfo
	EventPermission = "claude:permission" // a PermRequest needing an answer
	EventPermCancel = "claude:permission-cancel"
)

// ExitInfo describes a claude.exe exit. Expected is true when the app itself
// stopped the process (Stop or Close), so the UI does not report an error.
type ExitInfo struct {
	Code     int    `json:"code"`
	Expected bool   `json:"expected"`
	Stderr   string `json:"stderr"`
}

// PermissionModes are the --permission-mode values the UI offers. "manual"
// asks through the permission prompt tool for anything not already allowed.
var PermissionModes = map[string]bool{"manual": true, "acceptEdits": true, "plan": true}

// permTimeout bounds how long a tool call waits for a human answer before it
// is denied, so a forgotten dialog cannot wedge a session forever.
const permTimeout = 30 * time.Minute

// Session is one conversation: a bridge, a permission server, and a claude.exe
// child that is (re)started on demand. Stop kills the child; the next Send
// starts a new one with --resume, so a stopped turn never loses the session.
type Session struct {
	cfg       config.Config
	folder    string
	mode      string
	claudeBin string
	emit      Emitter
	bridge    *launch.Bridge
	perm      *PermServer

	// closed is set by Close before anything is torn down. A closed session's
	// late events (its exit, trailing output) must not reach the UI, which by
	// then shows a different conversation.
	closed atomic.Bool

	mu        sync.Mutex
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	sessionID string
	stopping  bool
	exited    chan struct{}
}

// SessionOptions selects the working folder, an optional session to resume,
// and the permission mode.
type SessionOptions struct {
	Folder         string
	ResumeID       string
	PermissionMode string
}

// NewSession starts the bridge and permission server. claude.exe itself is
// started by the first Send.
func NewSession(cfg config.Config, opts SessionOptions, claudeBin string, emit Emitter) (*Session, error) {
	if st, err := os.Stat(opts.Folder); err != nil || !st.IsDir() {
		return nil, fmt.Errorf("project folder %q does not exist", opts.Folder)
	}
	if opts.PermissionMode == "" {
		opts.PermissionMode = "manual"
	}
	if !PermissionModes[opts.PermissionMode] {
		return nil, fmt.Errorf("unknown permission mode %q", opts.PermissionMode)
	}
	if opts.ResumeID != "" && !validSessionID(opts.ResumeID) {
		return nil, fmt.Errorf("invalid session id %q", opts.ResumeID)
	}

	bridge, err := launch.StartBridge(cfg)
	if err != nil {
		return nil, err
	}
	s := &Session{
		cfg:       cfg,
		folder:    opts.Folder,
		mode:      opts.PermissionMode,
		claudeBin: claudeBin,
		emit:      emit,
		bridge:    bridge,
		sessionID: opts.ResumeID,
	}
	perm, err := NewPermServer(func(r PermRequest) { s.send(EventPermission, r) }, permTimeout)
	if err != nil {
		bridge.Close()
		return nil, err
	}
	s.perm = perm
	return s, nil
}

// send emits to the UI unless the session has been closed.
func (s *Session) send(event string, data any) {
	if !s.closed.Load() {
		s.emit.Emit(event, data)
	}
}

// BridgeMode is the bridge lane (passthrough or translate) for the UI.
func (s *Session) BridgeMode() string { return s.bridge.Mode() }

// ClaudeArgs builds the headless command line. Exported for tests.
func ClaudeArgs(mcpConfig, permissionMode, resumeID string) []string {
	args := []string{
		"-p",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--verbose",
		"--include-partial-messages",
		"--mcp-config", mcpConfig,
		"--permission-prompt-tool", PermToolName,
		"--permission-mode", permissionMode,
	}
	if resumeID != "" {
		args = append(args, "--resume", resumeID)
	}
	return args
}

// Send delivers one user turn, starting claude.exe first if it is not running.
func (s *Session) Send(text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cmd == nil {
		if err := s.startLocked(); err != nil {
			return err
		}
	}
	msg, _ := json.Marshal(map[string]any{
		"type":    "user",
		"message": map[string]any{"role": "user", "content": text},
	})
	if _, err := s.stdin.Write(append(msg, '\n')); err != nil {
		return fmt.Errorf("sending to claude: %w", err)
	}
	return nil
}

func (s *Session) startLocked() error {
	cmd := exec.Command(s.claudeBin, ClaudeArgs(s.perm.MCPConfig(), s.mode, s.sessionID)...)
	cmd.Dir = s.folder
	cmd.Env = launch.Env(s.cfg, s.bridge.Port, os.Environ())
	hideWindow(cmd)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr := &tailBuffer{max: 16 << 10}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting %s: %w", s.claudeBin, err)
	}
	s.cmd, s.stdin, s.stopping = cmd, stdin, false
	exited := make(chan struct{})
	s.exited = exited

	go func() {
		s.pump(stdout)
		err := cmd.Wait()
		code := 0
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code = exitErr.ExitCode()
		} else if err != nil {
			code = -1
		}
		s.mu.Lock()
		expected := s.stopping
		if s.cmd == cmd {
			s.cmd, s.stdin = nil, nil
		}
		s.mu.Unlock()
		s.perm.CancelAll()
		s.send(EventExit, ExitInfo{Code: code, Expected: expected, Stderr: stderr.String()})
		close(exited)
	}()
	return nil
}

// pump forwards each stream-json line to the UI and remembers the session id
// so a restarted child can --resume it.
func (s *Session) pump(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 64<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var head struct {
			Type      string `json:"type"`
			SessionID string `json:"session_id"`
		}
		if err := json.Unmarshal(line, &head); err != nil {
			// Not JSON: surface it rather than drop it, it is usually an error.
			s.send(EventClaude, map[string]any{"type": "raw", "text": string(line)})
			continue
		}
		if head.SessionID != "" {
			s.mu.Lock()
			s.sessionID = head.SessionID
			s.mu.Unlock()
		}
		s.send(EventClaude, json.RawMessage(append([]byte(nil), line...)))
	}
}

// SessionID is the Claude Code session id, once claude.exe has reported it.
func (s *Session) SessionID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessionID
}

// Stop ends the current turn by killing claude.exe (and its tool processes).
// It returns once the child has exited.
func (s *Session) Stop() {
	s.mu.Lock()
	cmd, exited := s.cmd, s.exited
	if cmd != nil {
		s.stopping = true
	}
	s.mu.Unlock()
	if cmd == nil {
		return
	}
	s.perm.CancelAll()
	s.send(EventPermCancel, nil)
	killTree(cmd)
	select {
	case <-exited:
	case <-time.After(10 * time.Second):
	}
}

// Respond answers a permission prompt.
func (s *Session) Respond(id string, allow, always bool) error {
	return s.perm.Respond(id, allow, always)
}

// Close stops claude.exe and tears down the servers. Nothing more is
// emitted for this session once Close starts.
func (s *Session) Close() {
	s.closed.Store(true)
	s.Stop()
	s.perm.Close()
	s.bridge.Close()
}

// tailBuffer keeps the last max bytes written, for reporting a crash.
type tailBuffer struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}
