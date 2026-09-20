// Package cli holds the human-facing commands. It owns interaction only; the
// logic it drives lives in the other packages.
package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

	"claude-vllm/internal/config"
)

var configKeys = []string{
	"url", "model", "api_key", "mode", "max_tokens",
	"context_window", "log",
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
		default:
			sorted := append([]string{}, configKeys...)
			sort.Strings(sorted)
			return fmt.Errorf("unknown key %q. Valid keys: %v", k, sorted)
		}
	}
	return nil
}

// ConfigSet loads the config file, applies key=value assignments, and saves it
// back so the operator never has to hand-edit JSON. The result must still
// pass Validate — this is what stops e.g. `config set url=` from silently
// blanking a working config; api_key is exempt from Validate, so clearing it
// with `api_key=` remains a legitimate, accepted operation.
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
	if err := c.Validate(); err != nil {
		return err
	}
	if err := c.SaveTo(path); err != nil {
		return err
	}
	fmt.Printf("Saved %s\n", path)
	return nil
}

// ConfigShow prints the effective configuration (file plus CLAUDE_VLLM_*
// overrides) to stdout. api_key is redacted: the config file can hold a real
// vLLM credential, and this output is meant to be pasted into bug reports and
// chat without also handing over that credential.
func ConfigShow() error {
	return configShow(os.Stdout)
}

// configShow does the actual work against an injected writer so the
// redaction behavior has a regression test that doesn't depend on os.Stdout.
func configShow(out io.Writer) error {
	c, warnings, err := config.Load()
	if err != nil {
		return err
	}
	path, _ := config.Path()
	for _, w := range warnings {
		fmt.Fprintf(out, "WARN  %s\n", w)
	}

	display := c
	if display.APIKey != "" {
		display.APIKey = "(hidden; set — see " + path + ")"
	} else {
		display.APIKey = "(not set)"
	}

	fmt.Fprintf(out, "# effective configuration (%s, with CLAUDE_VLLM_* overrides applied)\n", path)
	fmt.Fprintln(out, "# api_key is redacted above; the file itself is chmod 600")
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(display)
}
