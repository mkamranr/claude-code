package desktop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"claude-vllm/internal/cli"
	"claude-vllm/internal/config"
	"claude-vllm/internal/launch"
	"claude-vllm/internal/probe"
)

// Core holds the desktop app state behind the methods the UI calls. It is
// toolkit-independent; cmd/claude-vllm-desktop binds it to the window.
type Core struct {
	emit Emitter
	home string

	mu   sync.Mutex
	sess *Session
}

func NewCore(emit Emitter) (*Core, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return &Core{emit: emit, home: home}, nil
}

// ConfigView is the settings form. The API key is never sent to the UI:
// HasAPIKey reports whether one is stored, and an empty APIKey on save keeps
// it unless ClearAPIKey is set.
type ConfigView struct {
	VLLMURL       string `json:"vllm_url"`
	Model         string `json:"model"`
	APIKey        string `json:"api_key"`
	HasAPIKey     bool   `json:"has_api_key"`
	ClearAPIKey   bool   `json:"clear_api_key"`
	Mode          string `json:"mode"`
	MaxTokens     int    `json:"max_tokens"`
	ContextWindow int    `json:"context_window"`
	Log           string `json:"log"`
	// Problem is why the effective config cannot start a session ("" if it can).
	Problem  string   `json:"problem"`
	Warnings []string `json:"warnings"`
}

// GetConfig returns the stored settings for the form.
func (c *Core) GetConfig() (ConfigView, error) {
	path, err := config.Path()
	if err != nil {
		return ConfigView{}, err
	}
	stored, err := config.LoadFrom(path)
	if err != nil {
		return ConfigView{}, err
	}
	v := ConfigView{
		VLLMURL:       stored.VLLMURL,
		Model:         stored.Model,
		HasAPIKey:     stored.APIKey != "",
		Mode:          stored.Mode,
		MaxTokens:     stored.MaxTokens,
		ContextWindow: stored.ContextWindow,
		Log:           stored.Log,
	}
	effective, warnings, err := config.Load()
	if err != nil {
		v.Problem = err.Error()
	} else if err := effective.Validate(); err != nil {
		v.Problem = err.Error()
	}
	v.Warnings = warnings
	return v, nil
}

// SaveConfig validates and writes the settings, and applies the same offline
// Claude Code settings `claude-vllm setup` does.
func (c *Core) SaveConfig(v ConfigView) error {
	path, err := config.Path()
	if err != nil {
		return err
	}
	stored, err := config.LoadFrom(path)
	if err != nil {
		return err
	}
	next := stored
	next.VLLMURL = strings.TrimSpace(v.VLLMURL)
	next.Model = strings.TrimSpace(v.Model)
	next.Mode = v.Mode
	next.MaxTokens = v.MaxTokens
	next.ContextWindow = v.ContextWindow
	next.Log = v.Log
	switch {
	case v.ClearAPIKey:
		next.APIKey = ""
	case v.APIKey != "":
		next.APIKey = v.APIKey
	}
	if err := next.Validate(); err != nil {
		return err
	}
	if err := next.SaveTo(path); err != nil {
		return err
	}
	if err := launch.WriteSettings(filepath.Join(c.home, ".claude", "settings.json"), next); err != nil {
		return err
	}
	return launch.WriteOnboarding(filepath.Join(c.home, ".claude.json"))
}

// ListModels asks vLLM what it serves. An empty apiKey uses the stored one, so
// the form can refresh the list without re-entering the key.
func (c *Core) ListModels(url, apiKey string) ([]string, error) {
	if apiKey == "" {
		if path, err := config.Path(); err == nil {
			if stored, err := config.LoadFrom(path); err == nil {
				apiKey = stored.APIKey
			}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	models, _, err := probe.FetchModels(ctx, http.DefaultClient, strings.TrimSpace(url), apiKey)
	return models, err
}

// RunDoctor runs the same checks as `claude-vllm doctor`, including the
// tool-calling smoke test, and returns its report.
func (c *Core) RunDoctor() string {
	var buf bytes.Buffer
	if err := cli.Doctor(&buf); err != nil && !errors.Is(err, cli.ErrReported) {
		buf.WriteString("\nFAIL  " + err.Error() + "\n")
	}
	return buf.String()
}

// SessionStart is what the UI shows once a session is ready.
type SessionStart struct {
	Model      string `json:"model"`
	BridgeMode string `json:"bridge_mode"`
	ResumeID   string `json:"resume_id"`
	Folder     string `json:"folder"` // canonical spelling actually used
}

// StartSession replaces any current session with one in folder.
func (c *Core) StartSession(folder, resumeID, permissionMode string) (SessionStart, error) {
	c.closeSession()
	folder = canonicalFolder(folder)

	// Load warnings are surfaced on the settings screen (GetConfig).
	cfg, _, err := config.Load()
	if err != nil {
		return SessionStart{}, err
	}
	bin, err := launch.ClaudeBinary()
	if err != nil {
		return SessionStart{}, err
	}
	s, err := NewSession(cfg, SessionOptions{Folder: folder, ResumeID: resumeID, PermissionMode: permissionMode}, bin, c.emit)
	if err != nil {
		return SessionStart{}, err
	}
	// Swap under the lock and close whatever was there: two overlapping
	// StartSession calls (a mode change racing a history click) must not
	// leave one session's bridge and claude.exe running unowned.
	c.mu.Lock()
	prev := c.sess
	c.sess = s
	c.mu.Unlock()
	if prev != nil {
		prev.Close()
	}
	return SessionStart{Model: cfg.Model, BridgeMode: s.BridgeMode(), ResumeID: resumeID, Folder: folder}, nil
}

func (c *Core) current() (*Session, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sess == nil {
		return nil, errors.New("no session: pick a project folder first")
	}
	return c.sess, nil
}

// Send delivers a user turn to the current session.
func (c *Core) Send(text string) error {
	s, err := c.current()
	if err != nil {
		return err
	}
	return s.Send(text)
}

// Stop interrupts the current turn.
func (c *Core) Stop() error {
	s, err := c.current()
	if err != nil {
		return err
	}
	s.Stop()
	return nil
}

// RespondPermission answers a pending permission prompt.
func (c *Core) RespondPermission(id string, allow, always bool) error {
	s, err := c.current()
	if err != nil {
		return err
	}
	return s.Respond(id, allow, always)
}

// ListSessions lists the folder's saved sessions, newest first.
func (c *Core) ListSessions(folder string) ([]SessionInfo, error) {
	return ListSessions(c.home, canonicalFolder(folder))
}

// LoadTranscript returns a saved session's messages for display.
func (c *Core) LoadTranscript(folder, id string) ([]json.RawMessage, error) {
	return LoadTranscript(c.home, canonicalFolder(folder), id)
}

// CanonicalFolder is the spelling of folder that sessions are stored under.
func (c *Core) CanonicalFolder(folder string) string { return canonicalFolder(folder) }

func (c *Core) closeSession() {
	c.mu.Lock()
	s := c.sess
	c.sess = nil
	c.mu.Unlock()
	if s != nil {
		s.Close()
	}
}

// Shutdown is called when the window closes.
func (c *Core) Shutdown() { c.closeSession() }
