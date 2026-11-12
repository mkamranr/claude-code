package launch

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"time"

	"claude-vllm/internal/config"
	"claude-vllm/internal/probe"
	"claude-vllm/internal/server"
)

// Bridge is a running loopback bridge: the Anthropic-shaped endpoint that
// claude.exe is pointed at through ANTHROPIC_BASE_URL.
type Bridge struct {
	Port   int
	srv    *server.Server
	http   *http.Server
	logger *server.Logger
}

// Mode is the lane the bridge resolved to (passthrough or translate).
func (b *Bridge) Mode() string { return b.srv.Mode() }

// Close stops the HTTP server and closes the log file.
func (b *Bridge) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	b.http.Shutdown(ctx)
	b.logger.Close()
}

// StartBridge validates cfg, probes vLLM once, and serves the bridge on an
// ephemeral 127.0.0.1 port. Both the terminal launcher and the desktop app
// start the bridge through here, so the two cannot drift apart.
func StartBridge(cfg config.Config) (*Bridge, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("binding a loopback port: %w", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	caps, probeErr := probe.Detect(ctx, http.DefaultClient, cfg.VLLMURL, cfg.APIKey)
	cancel()
	if probeErr != nil {
		ln.Close()
		return nil, fmt.Errorf("%w\n\nFix the endpoint with:\n  claude-vllm config set url=http://HOST:PORT\nThen re-check with:\n  claude-vllm doctor", probeErr)
	}

	logDir, err := config.Dir()
	if err != nil {
		ln.Close()
		return nil, err
	}
	lg, err := server.NewLogger(cfg.Log, filepath.Join(logDir, "log"))
	if err != nil {
		ln.Close()
		return nil, err
	}

	srv := server.New(cfg, caps, lg)
	httpSrv := &http.Server{Handler: srv.Handler()}
	go func() {
		if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			lg.Note("bridge server stopped: %v", err)
		}
	}()
	return &Bridge{Port: port, srv: srv, http: httpSrv, logger: lg}, nil
}
