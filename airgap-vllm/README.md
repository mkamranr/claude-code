# Claude Code on a self-hosted vLLM — operator runbook

Run Claude Code against an internally hosted [vLLM](https://github.com/vllm-project/vllm)
server on a **Windows**, **Linux** or **macOS** machine with no internet access
and no sign-in.

**Contents:**
[How it works](#how-it-works) ·
[Packages](#packages) ·
[Getting a bundle](#getting-a-bundle) ·
[Install on Windows](#install-on-windows) ·
[Install on Linux](#install-on-linux) ·
[Install on macOS](#install-on-macos) ·
[What is verified, and where](#what-is-verified-and-where) ·
[Configuration](#configuration) ·
[Commands](#commands) ·
[vLLM requirements](#vllm-requirements) ·
[Desktop app](#desktop-app-windows) ·
[Troubleshooting](#troubleshooting) ·
[Updating and uninstalling](#updating-and-uninstalling)

## How it works

```
claude ──Anthropic Messages──▶ claude-vllm ──▶ your vLLM
(native)                       (127.0.0.1)      │
                                                ├─ passthrough → /v1/messages
                                                └─ translate   → /v1/chat/completions
```

`claude-vllm` starts a bridge on loopback, points Claude Code at it through the
documented `ANTHROPIC_BASE_URL` setting, and exits when Claude Code exits. It
probes your vLLM once at startup: if the build exposes a native Anthropic
`/v1/messages` endpoint it forwards requests nearly unchanged; otherwise it
translates to and from the OpenAI chat-completions API. Both lanes run the same
compatibility filter.

On Windows, `claude-vllm-desktop.exe` is a windowed alternative to the
terminal. It starts the same bridge and runs the same `claude.exe` headlessly,
rendering its event stream as a chat. See [Desktop app](#desktop-app-windows).

## Packages

| Package | Contents | Installer |
|---|---|---|
| `claude-vllm-airgap-<ver>-windows-x64.zip` | `claude.exe`, `claude-vllm.exe`, `claude-vllm-desktop.exe`, `webview2\` (Microsoft's WebView2 runtime, needed by the desktop app), `SHA256SUMS` | `install.ps1` (PowerShell 5.1+, no admin) |
| `claude-vllm-airgap-<ver>-linux-x64.tar.gz` | `claude`, `claude-vllm`, `release/` (Anthropic's signed release manifest and key), `SHA256SUMS` | `install.sh` (bash, no root) |
| `claude-vllm-airgap-<ver>-macos-arm64.tar.gz` | The same, for Apple silicon Macs | `install.sh` (bash 3.2+, no admin) |
| `claude-vllm-airgap-<ver>-macos-x64.tar.gz` | The same, for Intel Macs | `install.sh` |

Each package also carries this README. `claude.exe` and `claude` are
Anthropic's binaries, byte for byte; nothing in this project modifies them.

The Linux and macOS packages are terminal-only. The desktop app is Windows-only
for now.

## Getting a bundle

### Prebuilt

Prebuilt packages for both platforms are attached to the project's
[GitHub releases](https://github.com/mkamranr/claude-code-airgap/releases/latest).
Each package has a `.sha256` file beside it, and its hash is also in the
release notes. Check it on the connected machine before you carry the package
across:

```powershell
Get-FileHash .\claude-vllm-airgap-<ver>-windows-x64.zip      # Windows
```
```bash
sha256sum -c claude-vllm-airgap-<ver>-linux-x64.tar.gz.sha256      # Linux
shasum -a 256 -c claude-vllm-airgap-<ver>-macos-arm64.tar.gz.sha256 # macOS
```

Write the hash down and carry it separately from the package, then compare it
again on the target. See [What is verified, and where](#what-is-verified-and-where)
for why.

### Building it yourself

On an internet-connected machine (Linux, macOS, or Git Bash on Windows) with
`curl`, `gpg`, `shasum`, `zip`, `go` (1.26 or newer) and `python3` on PATH:

```bash
cd airgap-vllm
WEBVIEW2_CAB=~/Downloads/Microsoft.WebView2.FixedVersionRuntime.<ver>.x64.cab \
WEBVIEW2_CAB_SHA256=<optional: pin the cab you downloaded> \
./scripts/stage.sh 2.1.252
```

`stage.sh`:

1. Imports Anthropic's release signing key into a throwaway keyring and checks
   its fingerprint, `31DD DE24 DDFA B679 F42D 7BD2 BAA9 29FF 1A7E CACE`.
2. Verifies the release manifest's GPG signature against that key.
3. Downloads Claude Code for each platform and checks each binary against the
   SHA256 in the signed manifest.
4. Cross-compiles the bridge for every platform, and the desktop app for
   Windows.
5. Unpacks the WebView2 runtime into the Windows package.
6. Writes the packages to `dist/`, each with a `SHA256SUMS` inside and a
   `.sha256` file beside it.

It aborts rather than emit an unverified package.

| Variable | Default | Purpose |
|---|---|---|
| `WEBVIEW2_CAB` | unset | Path to the WebView2 **Fixed Version** x64 `.cab` from Microsoft's WebView2 download page. Without it the desktop app starts only where WebView2 is already installed, which excludes offline Windows 10 machines that never got it from Windows Update. |
| `WEBVIEW2_CAB_SHA256` | unset | Pins the cab you downloaded. |
| `PLATFORMS` | `windows linux macos` | Build only a subset. |
| `LINUX_PLATFORM` | `linux-x64` | `linux-x64-musl` for musl distributions such as Alpine. |
| `MACOS_ARCHS` | `arm64 x64` | Build only one macOS architecture. |

Unpacking the cab needs `cabextract` on Linux and macOS. Git Bash on Windows
uses the built-in `expand.exe`. The WebView2 runtime dominates the Windows
package: about 420 MB with it, about 100 MB without. The Linux and macOS
packages are about 90–100 MB each.

## Install on Windows

Unzip the package, then from PowerShell in that folder:

```powershell
.\install.ps1
claude-vllm setup      # enter the vLLM URL, pick a model from the list it fetches
claude-vllm doctor     # verify connectivity and tool calling
claude-vllm            # start Claude Code
```

Or open **Claude vLLM Desktop** from the Start Menu instead of the last three
lines; see [Desktop app](#desktop-app-windows).

`install.ps1`:

- Re-verifies every file against `SHA256SUMS`.
- Checks the Authenticode signatures on `claude.exe` (Anthropic) and
  `webview2\msedgewebview2.exe` (Microsoft).
- Installs to `%USERPROFILE%\.local\bin`, adds that folder to your user PATH
  if it isn't there, and creates a Start Menu shortcut.

It needs no network and no administrator rights.

| Option | Effect |
|---|---|
| `-InstallDir <path>` | Install somewhere other than `%USERPROFILE%\.local\bin` |
| `-NoShortcut` | Skip the Start Menu shortcut |
| `-SkipSignatureCheck` | Skip Authenticode. Use only if your policy hard-fails offline revocation checks rather than letting them time out; the SHA256 check still runs |
| `-Uninstall` | Remove what was installed (see [below](#updating-and-uninstalling)) |

## Install on Linux

```bash
tar -xzf claude-vllm-airgap-<ver>-linux-x64.tar.gz
cd claude-vllm-airgap-<ver>-linux-x64
./install.sh
claude-vllm setup
claude-vllm doctor
claude-vllm
```

`install.sh`:

- Re-verifies every file against `SHA256SUMS`.
- When `gpg` is installed, re-verifies `claude` against Anthropic's signed
  release manifest shipped in `release/`, using a throwaway keyring. Linux has
  no Authenticode, so this is the provenance check on this platform.
- Installs `claude` and `claude-vllm` to `~/.local/bin`, adding it to PATH in
  your existing `~/.bashrc`, `~/.zshrc` and `~/.profile` inside a marked block
  that `--uninstall` removes.

It needs no network and no root.

| Option | Effect |
|---|---|
| `--prefix DIR` | Install somewhere other than `~/.local/bin` |
| `--no-modify-path` | Leave shell startup files alone |
| `--skip-signature-check` | Skip the GPG and (on macOS) Apple signature checks; the SHA256 check still runs. `--skip-gpg` is an alias |
| `--uninstall` | Remove what was installed |

Requirements: x86_64 and glibc. For musl distributions, build the package with
`LINUX_PLATFORM=linux-x64-musl`.

## Install on macOS

Pick `macos-arm64` for Apple silicon (M-series) Macs and `macos-x64` for Intel
Macs. `uname -m` prints `arm64` or `x86_64`. Then, in Terminal:

```bash
tar -xzf claude-vllm-airgap-<ver>-macos-arm64.tar.gz
cd claude-vllm-airgap-<ver>-macos-arm64
./install.sh
claude-vllm setup
claude-vllm doctor
claude-vllm
```

`install.sh` is the same script as on Linux, with these differences on macOS:

- **Signature:** it verifies `claude`'s Apple code signature with `codesign`,
  and requires it to be Anthropic's Developer ID (`Developer ID Application:
  Anthropic PBC`, Team ID `Q6L2SF6YDW`). `codesign` is part of macOS, so this
  check always runs. The GPG check also runs if you have `gpg`, which macOS
  does not include.
- **Quarantine:** it clears the `com.apple.quarantine` flag on the two
  installed binaries, after their hashes have been verified. A
  browser-downloaded archive passes that flag to everything extracted from it.
  `claude` is notarized by Anthropic, but `claude-vllm` is not, so Gatekeeper
  would otherwise block it.
- **PATH:** it adds `~/.local/bin` to `~/.zshrc` (zsh is the default shell),
  plus any existing `~/.bashrc`, `~/.bash_profile` or `~/.profile`.

It needs no administrator rights, and the options are the same as on Linux.

## What is verified, and where

| Check | On the connected machine | On the target |
|---|---|---|
| Release manifest signed by Anthropic's key (`31DD…CACE`) | `stage.sh` | `install.sh`, when `gpg` is present |
| `claude` / `claude.exe` matches the signed manifest | `stage.sh` | `install.sh`, when `gpg` is present |
| Apple Developer ID: `claude` signed by Anthropic (Team `Q6L2SF6YDW`) | — | `install.sh` (macOS) |
| Authenticode: `claude.exe` by Anthropic, WebView2 by Microsoft | — | `install.ps1` |
| Every file matches `SHA256SUMS` | written by `stage.sh` | both installers |
| The package matches its published SHA256 | you, before transfer | you, after transfer |

The installers re-check everything on the target rather than trusting the
connected machine. The transfer across the air gap is exactly where corruption
or tampering would happen.

The bridge (`claude-vllm`) and the desktop app (`claude-vllm-desktop.exe`) are
not code-signed. On macOS the bridge carries only the ad-hoc signature that Go
adds when it builds the binary, which identifies no publisher. Their only in-package check is `SHA256SUMS`, which travels in
the same package: anyone who could alter them in transit could alter
`SHA256SUMS` to match. That is what the last row is for. Compare the package's
hash on the target against a value you carried over a separate channel.

## Configuration

Settings live in `~/.claude-vllm/config.json` (`%USERPROFILE%\.claude-vllm\config.json`
on Windows). Change them with:

```bash
claude-vllm config set url=http://10.0.0.5:8000
claude-vllm config set model=Qwen/Qwen3.8-27B
claude-vllm config show          # effective settings, api_key redacted
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
value that cannot be parsed (`CLAUDE_VLLM_CONTEXT_WINDOW=131o72`) is not fatal:
the previous value is kept, and `doctor`, `config show` and startup all say so.

The config file can hold an API key, so it is written with mode 0600. That is
owner-only on Linux. On Windows, Go's `os.Chmod` only toggles the read-only
attribute, so the file's real protection is whatever your `%USERPROFILE%`
grants. On a shared or roaming profile, prefer `CLAUDE_VLLM_API_KEY` in the
session environment.

There is deliberately no setting for model capabilities such as extended
thinking. Claude Code detects those itself. If its detection is wrong for your
model, add the relevant
`ANTHROPIC_DEFAULT_<TIER>_MODEL_SUPPORTED_CAPABILITIES` entry (`<TIER>` is
`OPUS`, `SONNET` or `HAIKU`; all three map to your served model) to the `env`
block of `~/.claude/settings.json`. claude-vllm strips inherited values of these
from the child process, so `settings.json` is where an override takes effect.

You can switch models inside a session with `/model`. The bridge serves
`/v1/models`, so the picker lists whatever your vLLM currently serves.

> **`log = full` writes complete prompts and response bodies to
> `~/.claude-vllm/log/`**, including source code from your sessions. Enable it
> only while debugging, and clear the directory afterwards.

## Commands

```
claude-vllm [claude args...]   Start the bridge and launch Claude Code
claude-vllm setup              Configure the vLLM endpoint and model
claude-vllm doctor             Diagnose the connection and tool calling
claude-vllm serve              Run the bridge in the foreground
claude-vllm config show        Print the effective configuration
claude-vllm config set k=v...  Change configuration
claude-vllm -- [claude args...]
                               Pass everything after -- straight to Claude Code,
                               e.g. `claude-vllm -- --help` for Claude Code's help
```

`claude-vllm serve` runs the bridge without launching Claude Code. Use it to
debug the bridge in isolation, or to point several Claude Code sessions at one
long-lived bridge.

## vLLM requirements

Claude Code is extremely tool-heavy. Start vLLM with automatic tool choice and
the parser that matches your model:

```bash
vllm serve <MODEL> --enable-auto-tool-choice --tool-call-parser <PARSER>
```

`claude-vllm doctor` runs a live tool-calling smoke test and says when the
parser is wrong, so run it before concluding anything else is broken. It also
warns when your `context_window` exceeds the model's `max_model_len`, a
mismatch that otherwise only shows up mid-conversation.

## Desktop app (Windows)

`claude-vllm-desktop.exe` (Start Menu: **Claude vLLM Desktop**) is the same
Claude Code, in a window. The model path is unchanged: it starts the bridge and
runs the bundled `claude.exe` headlessly with the same environment
`claude-vllm` uses, and it shares `%USERPROFILE%\.claude-vllm\config.json` with
the terminal client.

- **First run.** If the configuration is incomplete, the app opens Settings:
  enter the vLLM URL, press **Fetch models**, pick one, and **Save**. **Run
  doctor** in the same dialog runs exactly `claude-vllm doctor`.
- **Project folder.** Choose one in the sidebar, or pass it on the command line
  (`claude-vllm-desktop.exe C:\work\app`) for a per-project shortcut.
- **Permissions.** *Ask before tools* shows each tool call Claude Code would
  otherwise prompt for, with an inline diff for edits and the command for shell
  calls: **Allow**, **Allow &lt;tool&gt; for this chat**, or **Deny** (Claude is
  told and carries on). Unanswered prompts are denied after 30 minutes.
  *Auto-accept edits* and *Plan only* map to Claude Code's `acceptEdits` and
  `plan` modes.
- **Stop** ends the turn by stopping Claude Code and anything it started. Your
  next message resumes the same conversation.
- **History** lists the folder's saved sessions, the same ones
  `claude --resume` sees. Opening one replays and continues it.

How it is built:

- **Window:** a Go [Wails v2](https://wails.io) window around a plain
  HTML/CSS/JS page embedded in the exe, with no Node toolchain and no npm
  dependencies.
- **Rendering:** model output goes through a small Markdown renderer that
  escapes everything. The page can answer permission prompts, so markup in
  model output must never become live elements.
- **Permission prompts:** they arrive through Claude Code's
  `--permission-prompt-tool` hook, served by an MCP server on `127.0.0.1` that
  requires a per-session random token.
- **Dependencies:** the terminal client links no third-party code. Wails is
  only in the desktop app.

Developers: building with `-tags desktop,production,wv2runtime.error,e2e` adds
a test-only hook (`cmd/claude-vllm-desktop/e2e.go`, never shipped) that
evaluates JavaScript in the page from `127.0.0.1:$CLAUDE_VLLM_DESKTOP_E2E_PORT`.

## Troubleshooting

| Symptom | Cause and fix |
|---|---|
| Model replies in prose instead of calling tools | vLLM is missing `--enable-auto-tool-choice` or uses the wrong `--tool-call-parser`. Run `claude-vllm doctor` |
| `cannot reach vLLM at <url>` | Wrong URL or the server is down. `claude-vllm config set url=...` |
| `model "X" is not served by this vLLM` | The error and `doctor` list what is served. `claude-vllm config set model=...` |
| A sign-in prompt appears | `~/.claude.json` lacks `hasCompletedOnboarding`. Re-run `claude-vllm setup` |
| `Not signed in to the Cloud gateway` | `CLAUDE_CODE_USE_GATEWAY` is set in your environment. Unset it |
| HTTP 400 from vLLM | Set `log=full`, reproduce, read the request in `~/.claude-vllm/log/`, then set `log` back to `off` |
| `claude-vllm: command not found` | Open a new terminal so the PATH change applies, or run `export PATH="$HOME/.local/bin:$PATH"` |
| Linux: `claude` fails with "No such file or directory" though it exists | A musl distribution running the glibc build. Re-stage with `LINUX_PLATFORM=linux-x64-musl` |
| Linux: `install.sh` warns that gpg is missing | Install `gpg` from your internal mirror and re-run, or accept SHA256-only verification with `--skip-signature-check` |
| macOS: "claude-vllm cannot be opened because the developer cannot be verified" | The binary was copied by hand rather than by `install.sh`, so it kept the quarantine flag. Re-run `install.sh`, or `xattr -d com.apple.quarantine ~/.local/bin/claude-vllm` |
| macOS: `install.sh` says the bundle is for the other architecture | Use `macos-arm64` on Apple silicon and `macos-x64` on Intel Macs |
| Windows: "The WebView2 runtime was not found" | The package was staged without `WEBVIEW2_CAB` and this machine has no WebView2. Re-stage with it and reinstall |
| Desktop app opens Settings with a red message | The configuration is incomplete or invalid; the message names the field |
| Desktop app: "Claude Code exited unexpectedly" | The message shows the tail of Claude Code's stderr. Send another message to resume; if it repeats, run doctor from Settings |
| Desktop app: tool calls denied without a prompt | The prompt waited 30 minutes unanswered, or the turn was stopped. Ask again |

## Updating and uninstalling

Claude Code's auto-update is disabled by design (`DISABLE_UPDATES`). To move to
a new version, build or download the new package and run its installer over
the old install (on Windows, close the desktop app first). Re-run
`claude-vllm doctor` afterwards: a new Claude Code release can change the
request shape the bridge filters.

To uninstall:

```powershell
.\install.ps1 -Uninstall      # Windows: executables, webview2\, Start Menu shortcut
```
```bash
./install.sh --uninstall      # Linux and macOS: binaries and the PATH block
```

Both keep your settings (`~/.claude-vllm`) and Claude Code's sessions
(`~/.claude`). Delete those by hand if you no longer need them. On Windows the
PATH entry is also kept, since other tools may install to the same folder.
