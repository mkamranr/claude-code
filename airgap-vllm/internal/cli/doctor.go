package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"time"

	"claude-vllm/internal/config"
	"claude-vllm/internal/launch"
	"claude-vllm/internal/probe"
	"claude-vllm/internal/server"
)

// ErrReported marks an error whose explanation the command already wrote to
// its own output (e.g. Doctor's FAIL blocks). Callers such as main must still
// exit non-zero but must not print the error a second time.
var ErrReported = errors.New("already reported")

// toolCallHint is printed whenever the smoke test fails. The correct parser is
// model-specific and cannot be determined from the bridge, so the message names
// the knob rather than guessing a value.
const toolCallHint = `
Tool calling did not work. Claude Code is tool-heavy and will be unusable
until it does. This is a vLLM server setting, not a bridge setting.

Restart vLLM with automatic tool choice and the parser matching your model:

  vllm serve <MODEL> \
    --enable-auto-tool-choice \
    --tool-call-parser <PARSER>

Common parser values: hermes, llama3_json, mistral, pythonic, deepseek_v3.
For Qwen3-family models, try hermes first. Check your vLLM build's
--tool-call-parser help output for the list it actually supports.
`

// Doctor is the on-box acceptance test. It is the only way a developer with no
// internet can find out why something is failing, so every failing line names
// the next action rather than just describing the symptom. It never prints the
// configured API key.
func Doctor(out io.Writer) error {
	path, _ := config.Path()
	fmt.Fprintf(out, "config file      %s\n", path)

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "vLLM URL         %s\n", cfg.VLLMURL)
	fmt.Fprintf(out, "model            %s\n", cfg.Model)
	fmt.Fprintf(out, "configured mode  %s\n", cfg.Mode)

	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(out, "\nFAIL  %v\n", err)
		return ErrReported
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	caps, err := probe.Detect(ctx, http.DefaultClient, cfg.VLLMURL, cfg.APIKey)
	if err != nil {
		fmt.Fprintf(out, "\nFAIL  %v\n", err)
		fmt.Fprintf(out, "\nFix with:  claude-vllm config set url=http://HOST:PORT\n")
		return ErrReported
	}
	mode := probe.ResolveMode(cfg.Mode, caps)
	fmt.Fprintf(out, "reachable        yes\n")
	fmt.Fprintf(out, "native /v1/messages  %v\n", caps.MessagesAPI)
	fmt.Fprintf(out, "/tokenize        %v\n", caps.Tokenize)
	fmt.Fprintf(out, "effective mode   %s\n", mode)
	fmt.Fprintf(out, "models served    %v\n", caps.Models)

	if !caps.HasModel(cfg.Model) {
		fmt.Fprintf(out, "\nFAIL  configured model %q is not served.\n", cfg.Model)
		fmt.Fprintf(out, "Pick one of the models above:\n  claude-vllm config set model=NAME\n")
		return ErrReported
	}

	// The configured context window is a claude-vllm setting; the server's
	// max_model_len is what vLLM was actually launched with. A mismatch only
	// bites mid-conversation, long after setup, so it is worth flagging here
	// even though it does not fail the check outright.
	if served, ok := caps.MaxModelLen[cfg.Model]; ok {
		fmt.Fprintf(out, "server context   %d tokens\n", served)
		if cfg.ContextWindow > served {
			fmt.Fprintf(out, "\nWARN  configured context_window (%d) exceeds what vLLM was launched with (%d).\n",
				cfg.ContextWindow, served)
			fmt.Fprintf(out, "      Long sessions will fail mid-conversation. Fix with:\n")
			fmt.Fprintf(out, "        claude-vllm config set context_window=%d\n", served)
		}
	}

	if p := gitBashReport(); p != "" {
		fmt.Fprintf(out, "shell tool       Bash via %s\n", p)
	} else {
		fmt.Fprintf(out, "shell tool       PowerShell (Git for Windows not found; Bash tool unavailable)\n")
	}

	bin, err := launch.ClaudeBinary()
	if err != nil {
		fmt.Fprintf(out, "claude binary    NOT FOUND — %v\n", err)
	} else {
		fmt.Fprintf(out, "claude binary    %s\n", bin)
	}

	lg, err := server.NewLogger("off", filepath.Join(filepath.Dir(path), "log"))
	if err != nil {
		return err
	}
	handler := server.New(cfg, caps, lg).Handler()

	fmt.Fprintf(out, "\nrunning tool-calling smoke test...\n")
	if err := SmokeTest(ctx, handler, cfg.Model); err != nil {
		fmt.Fprintf(out, "FAIL  %v\n", err)
		fmt.Fprint(out, toolCallHint)
		return ErrReported
	}
	fmt.Fprintf(out, "PASS  the model emitted a well-formed tool call\n")
	fmt.Fprintf(out, "\nAll checks passed. Start with: claude-vllm\n")
	return nil
}

// gitBashReport surfaces whether launch.Env would find Git Bash, without
// duplicating its detection logic. On non-Windows this is always empty, since
// Env only looks for Git Bash under runtime.GOOS == "windows".
func gitBashReport() string {
	env := launch.Env(config.Default(), 0, nil)
	for _, e := range env {
		const k = "CLAUDE_CODE_GIT_BASH_PATH="
		if len(e) > len(k) && e[:len(k)] == k {
			return e[len(k):]
		}
	}
	return ""
}

// SmokeTest drives a real request through the bridge with one trivial tool and
// asserts the model produced a usable tool call. This is the one check that
// cannot be performed from a development machine — only against the live
// server — and it is the check nothing else in this repository substitutes
// for: Claude Code is extremely tool-heavy, and a model that cannot emit
// well-formed tool calls is unusable no matter how correct the rest of the
// bridge is.
func SmokeTest(ctx context.Context, handler http.Handler, model string) error {
	body := map[string]any{
		"model":      model,
		"max_tokens": 256,
		"messages": []any{map[string]any{
			"role":    "user",
			"content": "Call the ping tool with value \"ok\". Reply only with the tool call.",
		}},
		"tools": []any{map[string]any{
			"name":        "ping",
			"description": "Responds to a ping. Call this tool.",
			"input_schema": map[string]any{
				"type":       "object",
				"properties": map[string]any{"value": map[string]any{"type": "string"}},
				"required":   []any{"value"},
			},
		}},
		"tool_choice": map[string]any{"type": "auto"},
	}
	raw, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(raw)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		return fmt.Errorf("bridge returned HTTP %d: %s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Content []struct {
			Type  string         `json:"type"`
			ID    string         `json:"id"`
			Name  string         `json:"name"`
			Input map[string]any `json:"input"`
		} `json:"content"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		return fmt.Errorf("unparseable response: %w", err)
	}
	for _, b := range resp.Content {
		if b.Type != "tool_use" {
			continue
		}
		if b.Name == "" {
			return fmt.Errorf("model emitted a tool call with no name; the tool-call parser is probably wrong")
		}
		if b.ID == "" {
			return fmt.Errorf("tool call %q has no id", b.Name)
		}
		return nil
	}
	return fmt.Errorf("model returned prose instead of a tool call; vLLM is likely missing --enable-auto-tool-choice")
}
