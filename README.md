# Claude Code — air-gapped vLLM fork

A fork of [`anthropics/claude-code`](https://github.com/anthropics/claude-code) that adds
**`airgap-vllm/`**: a small Go bridge for running Claude Code against a self-hosted
[vLLM](https://github.com/vllm-project/vllm) server on a machine with **no internet access
and no sign-in**, from the terminal or from a **desktop app**.

Everything outside `airgap-vllm/` is upstream content, unmodified.

## Credit

Claude Code is built by [Anthropic](https://www.anthropic.com). This fork adds nothing to
Claude Code itself — it does not patch, wrap, or reimplement it. The bridge is an ordinary
HTTP service that speaks the Anthropic Messages API on one side and vLLM on the other, and
Claude Code talks to it through the documented `ANTHROPIC_BASE_URL` extension point. The
desktop app drives the same unmodified, signed `claude.exe` through its documented headless
mode (`-p` with stream-json input and output); it is a front end, not a reimplementation.

- Upstream repository: <https://github.com/anthropics/claude-code>
- Official documentation: <https://code.claude.com/docs/en/overview>
- Upstream `README.md` content is preserved in the git history of this fork.

The upstream `LICENSE.md` (© Anthropic PBC, all rights reserved) applies to all upstream
content and is unchanged. Claude Code itself requires a valid Anthropic subscription or API
access under Anthropic's terms; this bridge does not alter, bypass, or substitute for that.

## What this fork adds

```
airgap-vllm/
  cmd/claude-vllm/          CLI entry point
  cmd/claude-vllm-desktop/  desktop app: Wails window + embedded HTML/CSS/JS UI
  internal/config/          settings, env overrides, validation
  internal/filter/          request compatibility filter
  internal/translate/       Anthropic <-> OpenAI, including SSE streaming
  internal/probe/           vLLM capability detection
  internal/server/          the HTTP bridge
  internal/launch/          bridge startup, environment contract, process supervision
  internal/desktop/         desktop backend: headless sessions, permission prompts, history
  internal/cli/             setup, doctor, serve, config
  internal/fakevllm/        in-process vLLM double used by the tests
  scripts/stage.sh          builds a verifiable bundle on a connected machine
  install.ps1               offline Windows installer
  README.md                 full operator runbook
```

Roughly 8,000 lines of Go plus a 1,200-line plain-JS UI with no npm dependencies.
**`claude-vllm.exe` links no third-party code** and still builds offline with an empty
module cache; the desktop app adds one third-party module, [Wails v2](https://wails.io),
and the WebView2 runtime it renders with (bundled, since the target has none). Both
cross-compile to Windows x64 executables. Building needs Go 1.26 or newer.

Design notes and the implementation plan are in
[`docs/superpowers/specs/`](docs/superpowers/specs/) and
[`docs/superpowers/plans/`](docs/superpowers/plans/).

### How it works

```
claude.exe ──Anthropic Messages──▶ claude-vllm.exe ──▶ your vLLM
 (native binary)                   (127.0.0.1)          │
                                                        ├─ passthrough → /v1/messages
                                                        └─ translate   → /v1/chat/completions
```

The bridge probes your server once at startup. If your vLLM build exposes a native
Anthropic `/v1/messages` endpoint it forwards requests nearly unchanged; otherwise it
translates to and from the OpenAI chat-completions API. Either lane runs the same
compatibility filter, so a request that a strict server would reject is repaired the same
way in both.

It binds loopback only and exits when Claude Code exits.

The desktop app (`claude-vllm-desktop.exe`) starts the same bridge and runs the same
`claude.exe` headlessly (`--input-format/--output-format stream-json`), rendering the event
stream as a chat. It adds a folder picker, inline diffs for edits, an approve/deny dialog
for each tool call (served through Claude Code's `--permission-prompt-tool` hook), Stop and
resume, and the folder's session history.

## Setting this up on an air-gapped machine

Two machines are involved: a **connected build machine** that assembles a verifiable
bundle, and the **air-gapped workstation** that consumes it.

### 1. On the connected machine

**Shortcut:** download the prebuilt
[`claude-vllm-airgap-2.1.252.zip`](https://github.com/mkamranr/claude-code-airgap/releases/download/airgap-2.1.252/claude-vllm-airgap-2.1.252.zip)
from the [`airgap-2.1.252` release](https://github.com/mkamranr/claude-code-airgap/releases/tag/airgap-2.1.252),
check its SHA256 against
`8d093a0e8547e23c7c1f414d65aef768c77df5f5db12e922b7dc4e7eb0201d5f`, and go to
step 2. To build the bundle yourself instead:

Requires `curl`, `gpg`, `shasum`, `zip`, `go` and `python3`. **`gpg` is the one commonly
missing** — the script aborts at its preflight if any tool is absent, before touching the
network.

For the desktop app, also download Microsoft's WebView2 **Fixed Version** runtime (x64
`.cab`) and pass its path:

```bash
cd airgap-vllm
WEBVIEW2_CAB=/path/to/Microsoft.WebView2.FixedVersionRuntime.<ver>.x64.cab \
./scripts/stage.sh 2.1.252
```

This imports Anthropic's release signing key into a throwaway keyring, verifies the release
manifest is validly signed by fingerprint `31DDDE24DDFAB679F42D7BD2BAA929FF1A7ECACE`,
downloads the pinned `claude.exe`, checks its SHA256 against the signed manifest,
cross-compiles the bridge and the desktop app, unpacks the WebView2 runtime, and writes
`dist/claude-vllm-airgap-2.1.252.zip`.

It refuses to emit a bundle if any verification step fails. That is the point of it.

### 2. Transfer

Move the zip across by whatever removable media your environment permits. Everything
needed to verify it travels inside.

### 3. On the air-gapped workstation

From PowerShell — no network, no administrator rights:

```powershell
.\install.ps1          # re-verifies SHA256SUMS and the Authenticode signatures
claude-vllm setup      # enter your vLLM URL; pick a model from the list it fetches
claude-vllm doctor     # verify connectivity, mode, and tool calling
claude-vllm            # start Claude Code
```

Or open **Claude vLLM Desktop** from the Start Menu. It opens its Settings screen when
nothing is configured yet, and can run the same doctor there.

`setup` probes your server and offers a menu of the models it actually serves, so nothing
has to be typed from memory.

### Configuration

Settings live in `%USERPROFILE%\.claude-vllm\config.json`.

```powershell
claude-vllm config set url=http://10.0.0.5:8000
claude-vllm config set model=Qwen/Qwen3-32B
claude-vllm config show          # api_key is redacted
```

| Key | Meaning |
|---|---|
| `url` | vLLM base URL |
| `model` | Model id exactly as `/v1/models` reports it |
| `api_key` | Only if vLLM was started with `--api-key` |
| `mode` | `auto` (default), `messages`, or `openai` to force a lane |
| `max_tokens` | Default response cap |
| `context_window` | Should match what vLLM was launched with |
| `log` | `off` (default), `requests`, or `full` |

Every key is also settable as `CLAUDE_VLLM_<KEY>` in the environment. You can switch models
from inside a session with `/model` — the bridge serves `/v1/models`, so the picker lists
whatever your server is currently serving.

> **`log = full` writes complete prompts and response bodies to disk**, including source
> code from your sessions. Use it only while debugging, and clear the directory after.

### What your vLLM needs

Claude Code is extremely tool-heavy, so automatic tool choice is not optional:

```bash
vllm serve <MODEL> --enable-auto-tool-choice --tool-call-parser <PARSER>
```

The correct parser is model-specific. `claude-vllm doctor` runs a live tool-calling smoke
test and tells you when this is wrong — run it before concluding anything else is broken.

## Status

The Go code is complete: 9 packages and 2 commands, with `go test`, `go vet` and `gofmt`
all clean.

Verified on a Windows 10 host:

- A full `stage.sh` run against the live release bucket (GPG `VALIDSIG`, SHA256, both
  cross-compiles, WebView2 unpacking, `SHA256SUMS` over every file).
- `install.ps1` run by PowerShell against that bundle. It rejected an unsigned runtime
  and installed with `-SkipSignatureCheck`.
- The desktop app driven end to end against `claude.exe` 2.1.252 and a stand-in vLLM:
  - streamed replies, approve and deny, the inline diff
  - Stop then resume, history replay
  - doctor from Settings
  - a WebView2 runtime loaded from the bundled `webview2\` folder

Still the operator's acceptance steps, because they need your environment:

1. `claude-vllm doctor`'s tool-calling smoke test against your model.
2. The desktop app on the real air-gapped target, with the WebView2 Fixed Version cab you
   staged.

See [`airgap-vllm/README.md`](airgap-vllm/README.md) for the full runbook, including
troubleshooting.

## License

Upstream content remains © Anthropic PBC, all rights reserved, under
[`LICENSE.md`](LICENSE.md) and Anthropic's
[Commercial Terms of Service](https://www.anthropic.com/legal/commercial-terms). This fork
does not relicense any of it.
