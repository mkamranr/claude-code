package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"claude-vllm/internal/config"
	"claude-vllm/internal/launch"
	"claude-vllm/internal/probe"
)

// Setup walks a developer through first configuration. It probes the server so
// the model can be chosen from what is actually served rather than typed blind.
func Setup(in io.Reader, out io.Writer) error {
	path, err := config.Path()
	if err != nil {
		return err
	}
	c, err := config.LoadFrom(path)
	if err != nil {
		return err
	}
	r := bufio.NewReader(in)

	fmt.Fprintln(out, "Claude Code / vLLM bridge setup")
	fmt.Fprintln(out)

	c.VLLMURL = ask(r, out, "vLLM base URL", orDefault(c.VLLMURL, "http://127.0.0.1:8000"))

	// The literal key must never be echoed: scrollback, session logging and
	// screen-shared remote support are all ordinary on this deployment, and
	// re-running setup after a network change is routine. What is displayed
	// (keyDisplay) is decoupled from what is actually kept (c.APIKey).
	keyDisplay := ""
	if c.APIKey != "" {
		keyDisplay = "set; Enter to keep, or type to replace"
	}
	c.APIKey = askMasked(r, out, "vLLM API key (blank if none)", keyDisplay, c.APIKey)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	caps, err := probe.Detect(ctx, http.DefaultClient, c.VLLMURL, c.APIKey)
	if err != nil {
		return fmt.Errorf("%w\n\nCheck the URL and that vLLM is running, then re-run: claude-vllm setup", err)
	}

	fmt.Fprintf(out, "\nConnected. Native /v1/messages: %v\n", caps.MessagesAPI)
	if len(caps.Models) == 0 {
		return fmt.Errorf("vLLM reported no models at %s\n\nLoad a model in vLLM, then re-run: claude-vllm setup", c.VLLMURL)
	}
	fmt.Fprintln(out, "\nModels served:")
	for i, m := range caps.Models {
		fmt.Fprintf(out, "  %d) %s\n", i+1, m)
	}
	choice := ask(r, out, "Select a model by number", "1")
	n, err := strconv.Atoi(strings.TrimSpace(choice))
	if err != nil || n < 1 || n > len(caps.Models) {
		return fmt.Errorf("%q is not one of the listed numbers\n\nEnter a number from the list above, then re-run: claude-vllm setup", choice)
	}
	c.Model = caps.Models[n-1]

	if err := c.Validate(); err != nil {
		return err
	}
	if err := c.SaveTo(path); err != nil {
		return err
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	if err := launch.WriteSettings(filepath.Join(home, ".claude", "settings.json"), c); err != nil {
		return err
	}
	if err := launch.WriteOnboarding(filepath.Join(home, ".claude.json")); err != nil {
		return err
	}

	fmt.Fprintf(out, "\nSaved %s\n", path)
	fmt.Fprintf(out, "Model: %s\nMode:  %s\n\n", c.Model, probe.ResolveMode(c.Mode, caps))
	fmt.Fprintln(out, "Next: run `claude-vllm doctor` to verify tool calling, then `claude-vllm` to start.")
	return nil
}

func ask(r *bufio.Reader, out io.Writer, prompt, def string) string {
	if def != "" {
		fmt.Fprintf(out, "%s [%s]: ", prompt, def)
	} else {
		fmt.Fprintf(out, "%s: ", prompt)
	}
	line, _ := r.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return def
	}
	return line
}

// askMasked is like ask, but decouples what is shown from what is returned
// on Enter: display is printed in the bracket, real is what an empty line
// resolves to. Use it for secrets — the literal value never reaches out.
func askMasked(r *bufio.Reader, out io.Writer, prompt, display, real string) string {
	if display != "" {
		fmt.Fprintf(out, "%s [%s]: ", prompt, display)
	} else {
		fmt.Fprintf(out, "%s: ", prompt)
	}
	line, _ := r.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return real
	}
	return line
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
