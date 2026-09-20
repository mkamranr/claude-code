package cli

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"claude-vllm/internal/config"
	"claude-vllm/internal/probe"
	"claude-vllm/internal/server"
)

// Serve runs the bridge in the foreground, without launching Claude Code.
// Useful for debugging the bridge on its own, and for pointing several Claude
// Code sessions at one long-lived bridge instead of spawning a fresh one per
// session.
func Serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	addr := fs.String("addr", "127.0.0.1:8787", "address to listen on")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	caps, err := probe.Detect(ctx, http.DefaultClient, cfg.VLLMURL, cfg.APIKey)
	cancel()
	if err != nil {
		return err
	}

	dir, err := config.Dir()
	if err != nil {
		return err
	}
	lg, err := server.NewLogger(cfg.Log, filepath.Join(dir, "log"))
	if err != nil {
		return err
	}
	defer lg.Close()

	srv := server.New(cfg, caps, lg)
	fmt.Fprintf(os.Stderr, "bridge listening on http://%s (mode: %s, model: %s)\n", *addr, srv.Mode(), cfg.Model)
	fmt.Fprintf(os.Stderr, "point Claude Code at it with:\n")
	fmt.Fprintf(os.Stderr, "  ANTHROPIC_BASE_URL=http://%s ANTHROPIC_AUTH_TOKEN=local-vllm claude\n", *addr)
	return http.ListenAndServe(*addr, srv.Handler())
}
