package cli

import (
	"strings"
	"testing"

	"claude-vllm/internal/config"
)

// TestServeAddrDefaultsToLoopback pins a security property, not a
// preference: the bridge has no authentication, so binding every interface
// by default would expose it to the whole network. A future change to the
// -addr default must fail this test rather than ship silently.
func TestServeAddrDefaultsToLoopback(t *testing.T) {
	addr, err := parseServeAddr(nil)
	if err != nil {
		t.Fatal(err)
	}
	if addr != "127.0.0.1:8787" {
		t.Errorf("default addr = %q, want 127.0.0.1:8787", addr)
	}
}

func TestServeAddrRespectsFlag(t *testing.T) {
	addr, err := parseServeAddr([]string{"-addr", "127.0.0.1:9999"})
	if err != nil {
		t.Fatal(err)
	}
	if addr != "127.0.0.1:9999" {
		t.Errorf("addr = %q, want 127.0.0.1:9999", addr)
	}
}

func TestServeAddrRejectsUnknownFlag(t *testing.T) {
	if _, err := parseServeAddr([]string{"-bogus", "x"}); err == nil {
		t.Fatal("an unknown flag was accepted, want an error")
	}
}

// TestServeFailsFastOnInvalidConfig checks that an unconfigured (or
// otherwise invalid) config is rejected before Serve ever tries to bind a
// socket. http.ListenAndServe blocks forever on success, so every Serve test
// must fail before reaching it.
func TestServeFailsFastOnInvalidConfig(t *testing.T) {
	setHome(t)
	// No config file has been written: config.Load() yields config.Default(),
	// which has neither vllm_url nor model set.

	if err := Serve(nil); err == nil {
		t.Fatal("Serve with an unconfigured vllm_url = nil error, want a validation error")
	}
}

// TestServeFailsFastOnUnreachableUpstream checks that a probe failure is
// returned — and named — before Serve reaches http.ListenAndServe.
func TestServeFailsFastOnUnreachableUpstream(t *testing.T) {
	setHome(t)

	c := config.Default()
	c.VLLMURL = "http://127.0.0.1:1"
	c.Model = "m"
	saveConfig(t, c)

	err := Serve(nil)
	if err == nil {
		t.Fatal("Serve against an unreachable upstream = nil error, want the probe error")
	}
	if !strings.Contains(err.Error(), c.VLLMURL) {
		t.Errorf("error does not name the unreachable URL: %v", err)
	}
}
