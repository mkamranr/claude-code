package launch

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"time"

	"claude-vllm/internal/config"
	"claude-vllm/internal/probe"
	"claude-vllm/internal/server"
)

// ClaudeBinary locates claude next to this executable first, so a bundle is
// self-contained, then falls back to PATH.
func ClaudeBinary() (string, error) {
	name := "claude"
	if runtime.GOOS == "windows" {
		name = "claude.exe"
	}
	if self, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(self), name)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	p, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("cannot find %s next to claude-vllm or on PATH: %w", name, err)
	}
	return p, nil
}

// Run starts the bridge on an ephemeral loopback port, then runs Claude Code
// against it, tearing the bridge down when the child exits.
//
// warnings are the non-fatal configuration problems config.Load collected —
// typically a malformed CLAUDE_VLLM_* override. They are printed before the
// session starts rather than dropped, because the alternative is an operator
// debugging a setting they believe they changed.
func Run(cfg config.Config, warnings []string, args []string) (int, error) {
	for _, w := range warnings {
		fmt.Fprintf(os.Stderr, "claude-vllm: WARN %s\n", w)
	}
	if err := cfg.Validate(); err != nil {
		return 1, err
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 1, fmt.Errorf("binding a loopback port: %w", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	caps, probeErr := probe.Detect(ctx, http.DefaultClient, cfg.VLLMURL, cfg.APIKey)
	cancel()
	if probeErr != nil {
		ln.Close()
		return 1, fmt.Errorf("%w\n\nFix the endpoint with:\n  claude-vllm config set url=http://HOST:PORT\nThen re-check with:\n  claude-vllm doctor", probeErr)
	}

	logDir, err := config.Dir()
	if err != nil {
		ln.Close()
		return 1, err
	}
	lg, err := server.NewLogger(cfg.Log, filepath.Join(logDir, "log"))
	if err != nil {
		ln.Close()
		return 1, err
	}
	defer lg.Close()

	srv := server.New(cfg, caps, lg)
	httpSrv := &http.Server{Handler: srv.Handler()}
	go func() {
		if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			lg.Note("bridge server stopped: %v", err)
		}
	}()
	defer func() {
		shutCtx, shutCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer shutCancel()
		httpSrv.Shutdown(shutCtx)
	}()

	bin, err := ClaudeBinary()
	if err != nil {
		return 1, err
	}

	fmt.Fprintf(os.Stderr, "claude-vllm: %s via %s (mode: %s)\n", cfg.Model, cfg.VLLMURL, srv.Mode())

	// In Claude Code, Ctrl+C interrupts the current turn — it is the normal,
	// frequent way to stop a response that is going wrong, not a request to
	// end the session. A console interrupt is delivered to the whole process
	// group on both Unix and Windows, so without this the wrapper's own
	// default terminating disposition would kill claude-vllm (and the bridge
	// with it) on every such Ctrl+C, ending the session instead of the turn.
	// Registering a handler replaces that default with a no-op here; the
	// child still receives and handles its own copy of the signal normally.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt)
	defer signal.Stop(sigs)
	go func() {
		for range sigs {
		}
	}()

	cmd := exec.Command(bin, args...)
	cmd.Env = Env(cfg, port, os.Environ())
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode(), nil
		}
		return 1, fmt.Errorf("running %s: %w", bin, err)
	}
	return 0, nil
}
