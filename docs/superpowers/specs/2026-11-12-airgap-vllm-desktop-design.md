# airgap-vllm desktop app — design addendum

Date: 2026-11-12. Extends `2026-09-19-airgap-vllm-design.md`.

## Goal

A windowed Claude Code for the same air-gapped Windows 10 (1909) target, against the same
self-hosted vLLM, without changing the model path. It is a full agent, not a chat toy:
project folder, streamed replies, tool calls with diffs, per-call approval, Stop, history,
settings and doctor.

## Decisions

| Question | Decision | Why |
|---|---|---|
| Whose app? | Our own, in this repo | Anthropic's desktop app is closed, needs sign-in and updates itself; none of that works offline |
| Window toolkit | Wails v2, Go | Reuses the bridge packages in-process; one more Go module instead of a second language stack (Electron) |
| WebView | WebView2 Fixed Version runtime, bundled | The 1909 target has no WebView2. Built with `wv2runtime.error` so a missing runtime gives a message instead of an attempted download |
| Frontend | Plain HTML/CSS/JS, `embed.FS` | No Node or npm on the build machine and no JS supply chain |
| Markdown | Small in-house renderer that escapes everything | The page can answer permission prompts, so model output must never become live markup. `marked` passes raw HTML through |
| Model path | Unchanged: `launch.StartBridge` + `launch.Env` | The desktop app and the CLI cannot drift; every compatibility fix applies to both |
| Driving Claude Code | `claude -p --input-format stream-json --output-format stream-json --verbose --include-partial-messages` | Documented headless mode. One process serves many turns |
| Permission prompts | `--permission-prompt-tool mcp__desktop__approve`, an MCP server (Streamable HTTP, JSON only) on 127.0.0.1 with a per-session bearer token | Documented hook. The token stops other local processes approving calls |
| Stop | Kill the process tree (`taskkill /T`), then `--resume <session_id>` on the next send | Nothing is lost; a running Bash tool does not outlive Stop |
| History | Read `~/.claude/projects/<encoded cwd>/*.jsonl` | The same sessions `claude --resume` sees. The folder is canonicalized first (long path names, upper-case drive letter), because the encoding is of the literal path string |

## Verified behaviour of claude.exe 2.1.252 (spike, 2026-11-12)

- One `-p` stream-json process accepts successive user turns on stdin; the session id
  arrives in the `system`/`init` event.
- `--permission-prompt-tool` is accepted (hidden from `--help`). The tool receives
  `{tool_name, input, tool_use_id}`. A text block holding `{"behavior":"allow","updatedInput":…}`
  runs the tool; `{"behavior":"deny","message":…}` returns an `is_error` tool_result
  carrying the message to the model.
- An `http` MCP server with `headers` in `--mcp-config` receives the header on every request.
  The client also opens a GET stream; answering 405 is fine.
- `--resume <id>` in headless mode carries the full history.
- Permission modes in this version are `manual` (prompts), `acceptEdits`, `plan`, `auto`,
  `dontAsk` and `bypassPermissions`; there is no `default`. The app offers the first three.
- Read-only shell commands (e.g. `echo`) are auto-allowed and never reach the prompt tool.

## Layout

```
cmd/claude-vllm-desktop/   main.go (Wails), e2e.go (test hook, -tags e2e), frontend/
internal/desktop/          core.go (UI-facing methods), session.go (claude.exe child),
                           permmcp.go (permission MCP server), history.go, folder_*.go,
                           proc_*.go (hidden console window, process-tree kill)
internal/launch/bridge.go  StartBridge, shared with the CLI launcher
```

## Testing

- Unit: the permission server (handshake, token, allow/deny/always, timeout, cancel), the
  session (a fake claude via the TestMain re-exec pattern: multi-turn, Stop then resume,
  `--resume` arguments), history parsing, folder canonicalization, and `StartBridge`
  against `fakevllm`.
- End to end: the e2e build plus a stand-in vLLM, driven through the page.
  Verified:
  - approve and deny, Stop with a prompt open, resume
  - history replay, doctor
  - HTML in model output staying inert
  - loading WebView2 from the bundled folder

  Neither UI Automation nor WebView2 remote debugging could reach the page on the test
  host, hence the build-tagged hook.
