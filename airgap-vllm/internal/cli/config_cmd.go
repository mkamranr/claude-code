// Package cli holds the human-facing commands. It owns interaction only; the
// logic it drives lives in the other packages.
package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"claude-vllm/internal/config"
)

var configKeys = []string{
	"url", "model", "api_key", "mode", "max_tokens",
	"context_window", "supports_thinking", "log",
}

// ApplyAssignments applies key=value pairs so nobody has to hand-edit JSON.
func ApplyAssignments(c *config.Config, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("nothing to set. Valid keys: %v", configKeys)
	}
	for _, a := range args {
		k, v, found := strings.Cut(a, "=")
		if !found || k == "" {
			return fmt.Errorf("%q is not a key=value assignment", a)
		}
		switch k {
		case "url":
			c.VLLMURL = v
		case "model":
			c.Model = v
		case "api_key":
			c.APIKey = v
		case "mode":
			c.Mode = v
		case "log":
			c.Log = v
		case "max_tokens":
			n, err := strconv.Atoi(v)
			if err != nil {
				return fmt.Errorf("max_tokens must be a number, got %q", v)
			}
			c.MaxTokens = n
		case "context_window":
			n, err := strconv.Atoi(v)
			if err != nil {
				return fmt.Errorf("context_window must be a number, got %q", v)
			}
			c.ContextWindow = n
		case "supports_thinking":
			b, err := strconv.ParseBool(v)
			if err != nil {
				return fmt.Errorf("supports_thinking must be true or false, got %q", v)
			}
			c.SupportsThinking = b
		default:
			sorted := append([]string{}, configKeys...)
			sort.Strings(sorted)
			return fmt.Errorf("unknown key %q. Valid keys: %v", k, sorted)
		}
	}
	return nil
}

// ConfigSet loads the config file, applies key=value assignments, and saves it
// back so the operator never has to hand-edit JSON.
func ConfigSet(args []string) error {
	path, err := config.Path()
	if err != nil {
		return err
	}
	c, err := config.LoadFrom(path)
	if err != nil {
		return err
	}
	if err := ApplyAssignments(&c, args); err != nil {
		return err
	}
	if err := c.SaveTo(path); err != nil {
		return err
	}
	fmt.Printf("Saved %s\n", path)
	return nil
}

// ConfigShow prints the effective configuration (file plus CLAUDE_VLLM_*
// overrides). api_key is redacted: the config file can hold a real vLLM
// credential, and this output is meant to be pasted into bug reports and chat
// without also handing over that credential.
func ConfigShow() error {
	c, err := config.Load()
	if err != nil {
		return err
	}
	path, _ := config.Path()

	display := c
	if display.APIKey != "" {
		display.APIKey = "(hidden; set — see " + path + ")"
	} else {
		display.APIKey = "(not set)"
	}

	fmt.Printf("# effective configuration (%s, with CLAUDE_VLLM_* overrides applied)\n", path)
	fmt.Println("# api_key is redacted above; the file itself is chmod 600")
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(display)
}
