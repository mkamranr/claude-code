# Claude Code on a self-hosted vLLM, air-gapped

Runs Claude Code against an internally hosted vLLM server on a Windows machine
with no internet access and no sign-in.

## How it works

```
claude.exe ──Anthropic Messages──▶ claude-vllm.exe ──▶ your vLLM
 (native)                          (127.0.0.1)          │
                                                        ├─ passthrough → /v1/messages
                                                        └─ translate   → /v1/chat/completions
```

`claude-vllm.exe` starts a bridge on loopback, points Claude Code at it, and
exits when Claude Code exits. It probes your vLLM once at startup: if the build
exposes a native Anthropic `/v1/messages` it forwards requests nearly unchanged,
otherwise it translates to and from the OpenAI chat completions API.

`claude-vllm-desktop.exe` is a windowed alternative to the terminal. It starts
the same bridge and runs the same `claude.exe`, headless, rendering its event
stream in a chat window. See [Desktop app](#desktop-app).

## On a connected machine

Requires `curl`, `gpg`, `shasum`, `zip`, `go` (1.26 or newer), and `python3`
on PATH. `gpg` in particular is easy to be missing on a fresh machine — install
it before running this if `command -v gpg` comes back empty.

For the desktop app, also download the WebView2 **Fixed Version** runtime for
x64 (a `.cab`) from Microsoft's WebView2 download page, and pass its path. Its
URL is versioned, so the script does not fetch it itself. Unpacking it needs
`cabextract` (Linux/macOS), or nothing extra under Git Bash on Windows, where
the built-in `expand.exe` is used.

```bash
WEBVIEW2_CAB=~/Downloads/Microsoft.WebView2.FixedVersionRuntime.<ver>.x64.cab \
WEBVIEW2_CAB_SHA256=<optional: pin the cab you downloaded> \
./scripts/stage.sh 2.1.252
```

This imports the Claude Code release signing key (fingerprint
`31DD DE24 DDFA B679 F42D 7BD2 BAA9 29FF 1A7E CACE`), verifies it against the
release manifest's GPG signature, downloads the Windows binary, checks its
SHA256, cross-compiles the bridge and the desktop app, unpacks the WebView2
runtime, and writes `dist/claude-vllm-airgap-2.1.252.zip` containing
`claude.exe`, `claude-vllm.exe`, `claude-vllm-desktop.exe`, `webview2\`,
`install.ps1`, `README.md`, and a `SHA256SUMS` covering every file. It aborts
rather than emitting an unverified bundle.

Without `WEBVIEW2_CAB` the bundle is about 180 MB smaller and the desktop app
starts only on targets that already have WebView2. Offline Windows 10 machines
that never received it through Windows Update (the 1909 target included) do
not have it. The terminal client never needs it.

For reference, the `2.1.252` / `win32-x64` artifact this pins is `claude.exe`,
217406624 bytes, SHA256
`fe688ecde90d65fff0b2dd097597439774116077d80a3c1e131a7e47db307b1a`. `stage.sh`
verifies this itself against the signed manifest; the numbers are here so you
can eyeball them without re-running the tool.

## On the air-gapped machine

Unzip the bundle, then from PowerShell:

```powershell
.\install.ps1
claude-vllm setup      # enter the vLLM URL, pick a model from the list it fetches
claude-vllm doctor     # verify connectivity and tool calling
claude-vllm            # start Claude Code
```

Or open **Claude vLLM Desktop** from the Start Menu instead of the last three
lines; see [Desktop app](#desktop-app).

`install.ps1` re-verifies every file's SHA256 against `SHA256SUMS` and checks
the Authenticode signatures on `claude.exe` and the bundled WebView2 runtime
(`webview2\msedgewebview2.exe`, signed by Microsoft), failing loudly and naming the file
on any mismatch — the transfer across the air gap is exactly where corruption
or tampering would happen, so it does not trust the checks that ran on the
connected machine. It needs no network and no administrator rights: it writes
only under `%USERPROFILE%` and sets your user (not machine) PATH. It installs
`claude.exe`, `claude-vllm.exe`, `claude-vllm-desktop.exe` and `webview2\` to
`%USERPROFILE%\.local\bin`, adds that directory to your PATH only if it isn't
already there, and creates a Start Menu shortcut (skip it with `-NoShortcut`).

If your organization's policy hard-fails an offline Authenticode revocation
check instead of letting it time out, re-run with `.\install.ps1
-SkipSignatureCheck` — the SHA256 verification still runs regardless and needs
no network under any policy.

Note what that signature check does and does not cover. `claude.exe` is
Authenticode-signed by Anthropic, so its signature is an independent statement
about the file, as Microsoft's is for the WebView2 runtime.
`claude-vllm.exe` and `claude-vllm-desktop.exe` are not signed: their only integrity check
across the air gap is its entry in the in-bundle `SHA256SUMS`, and that file
travels inside the same zip. Anyone who could alter either in
transit could alter `SHA256SUMS` to match. If the bundle's own provenance
matters to you, verify the zip's SHA256 (printed by `stage.sh` at the end of
the run) against a value carried over a separate channel.

## Configuration

Settings live in `%USERPROFILE%\.claude-vllm\config.json`. It can hold a vLLM
API key, so it is written with mode 0600 — which really is owner-only on
Unix-like systems, but not on Windows: Go's `os.Chmod` there only toggles the
read-only attribute and writes no ACL. On Windows the file's actual protection
is whatever your `%USERPROFILE%` grants, inherited from the parent directory.
If your `%USERPROFILE%` is readable by others — a roaming profile, a shared
administrative account — treat the key in this file as readable by them too,
and prefer `CLAUDE_VLLM_API_KEY` in the session environment. Change settings
with:

```powershell
claude-vllm config set url=http://10.0.0.5:8000
claude-vllm config set model=Qwen/Qwen3.8-27B
claude-vllm config show
```

| Key | Meaning |
|---|---|
| `url` | vLLM base URL |
| `model` | Model id, exactly as vLLM reports it in `/v1/models` |
| `api_key` | Only if vLLM was started with `--api-key` |
| `mode` | `auto` (default), `messages`, or `openai` to force a lane |
| `max_tokens` | Default response cap |
| `context_window` | Must match what vLLM was launched with |
| `log` | `off` (default), `requests`, or `full` |

Every key is also settable as an environment variable: `CLAUDE_VLLM_URL`,
`CLAUDE_VLLM_MODEL`, `CLAUDE_VLLM_API_KEY`, `CLAUDE_VLLM_MODE`,
`CLAUDE_VLLM_MAX_TOKENS`, `CLAUDE_VLLM_CONTEXT_WINDOW`, `CLAUDE_VLLM_LOG`. A
value that cannot be parsed — `CLAUDE_VLLM_CONTEXT_WINDOW=131o72` — is not
fatal: the previous value is kept, and `claude-vllm doctor`, `config show` and
startup all say so rather than leaving you to wonder why the override did
nothing.

There is deliberately no setting here for model capabilities such as extended
thinking. Claude Code detects those itself, and claude-vllm cannot validate the
token values that would override that detection, so it does not offer to guess
them. If the detection is wrong for your model, set the override where Claude
Code documents it — add the relevant
`ANTHROPIC_DEFAULT_<TIER>_MODEL_SUPPORTED_CAPABILITIES` entry to the `env`
block of `%USERPROFILE%\.claude\settings.json` directly. `<TIER>` is `OPUS`,
`SONNET` or `HAIKU`; claude-vllm maps all three to your one served model.
claude-vllm strips any inherited value of these from the child process, so
settings.json is the place that actually takes effect.

`claude-vllm config show` prints the effective configuration (file plus any
environment overrides) with `api_key` redacted, so it is safe to paste into a
bug report or chat.

You can also switch models from inside a session with `/model` — the bridge
serves `/v1/models`, so the picker lists whatever your vLLM is currently
serving.

> **`log = full` writes complete prompts and response bodies to
> `%USERPROFILE%\.claude-vllm\log\`.** That includes source code from your
> sessions. Enable it only while debugging, and clear the directory afterwards.

## Commands

```
claude-vllm [claude args...]   Start the bridge and launch Claude Code
claude-vllm setup              Configure the vLLM endpoint and model
claude-vllm doctor             Diagnose the connection and tool calling
claude-vllm serve              Run the bridge in the foreground
claude-vllm config show        Print the effective configuration
claude-vllm config set k=v...  Change configuration
claude-vllm -- [claude args...]
                                Bypass claude-vllm's own subcommands and pass
                                everything after -- straight to Claude Code.
                                Needed because claude-vllm intercepts
                                -h/--help/setup/doctor/serve/config itself —
                                use `claude-vllm -- --help` to reach Claude
                                Code's own help instead of claude-vllm's.
```

`claude-vllm serve` runs the bridge on its own, without launching Claude Code —
useful for debugging the bridge in isolation, or for pointing several Claude
Code sessions at one long-lived bridge instead of spawning a fresh one per
session.

## Requirements on the vLLM side

Claude Code is extremely tool-heavy. vLLM must be started with automatic tool
choice and a parser appropriate for your model:

```bash
vllm serve <MODEL> --enable-auto-tool-choice --tool-call-parser <PARSER>
```

The correct parser is model-specific. `claude-vllm doctor` runs a live
tool-calling smoke test and tells you if this is wrong — run it first, before
concluding anything else is broken.

`claude-vllm doctor` also compares your configured `context_window` against
what vLLM reports as `max_model_len` for the model, and warns if yours is
larger — that mismatch only bites mid-conversation, long after setup, so it is
worth fixing before you rely on long sessions.

## Desktop app

`claude-vllm-desktop.exe` (Start Menu: **Claude vLLM Desktop**) is the same
Claude Code, in a window. Nothing about the model path changes: it starts the
bridge described above and runs the bundled `claude.exe` headlessly against
it, with the same environment `claude-vllm` uses. It reads and writes the same
`%USERPROFILE%\.claude-vllm\config.json`, so configuring either configures both.

- **First run.** If the configuration is incomplete, the app opens Settings:
  enter the vLLM URL, press **Fetch models**, pick one, and **Save**. Saving
  also applies the offline Claude Code settings `claude-vllm setup` writes.
  **Run doctor** in the same dialog runs exactly `claude-vllm doctor`,
  including the tool-calling smoke test.
- **Project folder.** Choose one in the sidebar, or start the app with a
  folder argument (`claude-vllm-desktop.exe C:\work\app`), useful for a
  per-project shortcut. Claude Code's working directory is that folder.
- **Permissions.** *Ask before tools* shows every tool call Claude Code would
  otherwise prompt for, with an inline diff for edits and the command for
  shell calls: **Allow**, **Allow &lt;tool&gt; for this chat**, or **Deny** (Claude
  is told it was denied and carries on). Unanswered prompts are denied after
  30 minutes. *Auto-accept edits* and *Plan only* map to Claude Code's
  `acceptEdits` and `plan` modes. Read-only commands Claude Code already
  considers safe run without a prompt, exactly as in the terminal.
- **Stop** ends the current turn by stopping Claude Code and any process it
  started. Your next message resumes the same conversation.
- **History** lists the folder's saved sessions (the same ones `claude
  --resume` sees). Opening one replays it and continues it.

How it is built: a Go [Wails v2](https://wails.io) window around a plain
HTML/CSS/JS page, embedded in the exe. There is no Node toolchain and no npm
dependency. Model output is rendered by a deliberately small Markdown renderer
that escapes everything and never passes raw HTML through. That matters
because the page can answer permission prompts, so markup in model output must
never become live elements. Permission prompts reach the window through Claude
Code's `--permission-prompt-tool` hook, served by a tiny MCP server on
`127.0.0.1` that requires a per-session random token.

The terminal client `claude-vllm.exe` links no third-party code. Wails and its
dependencies are only in `claude-vllm-desktop.exe`, and `go build
./cmd/claude-vllm` still works offline with an empty module cache.

Developers: `go build -tags desktop,production,wv2runtime.error,e2e` adds a
test-only hook (`cmd/claude-vllm-desktop/e2e.go`, never part of the bundle). It
evaluates JavaScript in the page from `127.0.0.1:$CLAUDE_VLLM_DESKTOP_E2E_PORT`,
so a script can drive the real window end to end.

## Troubleshooting

| Symptom | Cause and fix |
|---|---|
| Desktop app: "The WebView2 runtime was not found" | The bundle was staged without `WEBVIEW2_CAB` and this machine has no WebView2. Re-stage with it and reinstall; `webview2\` must sit next to `claude-vllm-desktop.exe` |
| Desktop app opens Settings with a red message | The configuration is incomplete or invalid; the message says which field. Same rules as `claude-vllm config` |
| Desktop app: "Claude Code exited unexpectedly" | The message shows the tail of Claude Code's stderr. Send another message to restart and resume; if it repeats, run doctor from Settings |
| Desktop app: tool calls are denied without a prompt | The prompt waited 30 minutes unanswered, or the turn was stopped. Ask again |
| `Not signed in to the Cloud gateway` | `CLAUDE_CODE_USE_GATEWAY` is set in your environment. The launcher strips it from the child Claude Code process, but a wrapper-level export can still confuse other tooling — unset it |
| Model replies in prose instead of calling tools | vLLM is missing `--enable-auto-tool-choice` or is using the wrong `--tool-call-parser`. Run `claude-vllm doctor` |
| `cannot reach vLLM at <url>` | Wrong URL or the server is down. `claude-vllm config set url=...` |
| `model "X" is not served by this vLLM` | `claude-vllm doctor` and the error both list what IS served — pick one with `claude-vllm config set model=...` |
| A sign-in prompt appears | `~/.claude.json` is missing `hasCompletedOnboarding`. Re-run `claude-vllm setup` |
| HTTP 400 from vLLM | Set `log=full`, reproduce, read the logged request body in `%USERPROFILE%\.claude-vllm\log\`, then set `log` back to `off` |

## Updating Claude Code

Auto-update is disabled by design (`DISABLE_UPDATES`). To move to a new
version, re-run `stage.sh` with the new version number on a connected machine
and reinstall (close the desktop app first; `install.ps1` refuses while it runs). Re-run `claude-vllm doctor` afterwards: a new Claude Code
release can change the request shape the bridge filters.
