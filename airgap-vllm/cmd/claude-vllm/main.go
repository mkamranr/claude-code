// Command claude-vllm bridges Claude Code to a self-hosted vLLM server and
// launches Claude Code against it.
package main

import (
	"errors"
	"fmt"
	"os"

	"claude-vllm/internal/cli"
	"claude-vllm/internal/config"
	"claude-vllm/internal/launch"
)

const usage = `claude-vllm — run Claude Code against a self-hosted vLLM server

Usage:
  claude-vllm [claude args...]   Start the bridge and launch Claude Code
  claude-vllm setup              Configure the vLLM endpoint and model
  claude-vllm doctor             Diagnose the connection and tool calling
  claude-vllm serve              Run the bridge in the foreground
  claude-vllm config show        Print the effective configuration
  claude-vllm config set k=v...  Change configuration
                                 keys: url model api_key mode max_tokens
                                       context_window supports_thinking log
  claude-vllm -- [claude args...]
                                 Bypass claude-vllm's own subcommands (e.g. to
                                 pass -h/--help/setup/doctor/serve/config
                                 through to Claude Code itself) and launch
                                 Claude Code with everything after -- verbatim
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		if !errors.Is(err, cli.ErrReported) {
			fmt.Fprintf(os.Stderr, "claude-vllm: %v\n", err)
		}
		os.Exit(1)
	}
}

func run(args []string) error {
	// "--" is the conventional escape hatch: it skips claude-vllm's own
	// subcommand dispatch entirely, so `-h`, `setup`, `doctor`, `serve` and
	// `config` remain reachable as literal arguments to Claude Code itself
	// (e.g. `claude-vllm -- --help` reaches Claude Code's own help, not
	// claude-vllm's).
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	} else if len(args) > 0 {
		switch args[0] {
		case "-h", "--help", "help":
			fmt.Print(usage)
			return nil
		case "setup":
			return cli.Setup(os.Stdin, os.Stdout)
		case "doctor":
			return cli.Doctor(os.Stdout)
		case "serve":
			return cli.Serve(args[1:])
		case "config":
			if len(args) < 2 {
				return fmt.Errorf("config needs a subcommand: show or set")
			}
			switch args[1] {
			case "show":
				return cli.ConfigShow()
			case "set":
				return cli.ConfigSet(args[2:])
			default:
				return fmt.Errorf("unknown config subcommand %q", args[1])
			}
		}
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	code, err := launch.Run(cfg, args)
	if err != nil {
		return err
	}
	os.Exit(code)
	return nil
}
