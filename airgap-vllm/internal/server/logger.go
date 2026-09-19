package server

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Logger writes bridge diagnostics to disk. On an air-gapped machine this is
// the only debugging affordance available, so it records enough to explain a
// rejected request.
//
// Mode "full" writes complete request and response bodies, which contain
// prompts and source code. The operator documentation must say so.
type Logger struct {
	mode string
	dir  string
	mu   sync.Mutex
	f    *os.File
}

func NewLogger(mode, dir string) (*Logger, error) {
	l := &Logger{mode: mode, dir: dir}
	if mode == "off" {
		return l, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("creating log directory: %w", err)
	}
	name := filepath.Join(dir, time.Now().Format("2006-01-02")+".log")
	f, err := os.OpenFile(name, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening log file: %w", err)
	}
	l.f = f
	return l, nil
}

func (l *Logger) Note(format string, args ...any) {
	l.write(fmt.Sprintf(format, args...))
}

// Body records a payload only when logging is set to full.
func (l *Logger) Body(label string, b []byte) {
	if l.mode != "full" {
		return
	}
	l.write(label + " " + string(b))
}

func (l *Logger) write(s string) {
	if l.mode == "off" || l.f == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintf(l.f, "%s %s\n", time.Now().Format(time.RFC3339), s)
}

func (l *Logger) Close() {
	if l.f != nil {
		l.f.Close()
	}
}
