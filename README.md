<div align="center">

# Claude Code Air-Gap

**Run Claude Code fully offline, against your own vLLM server.**
No internet, no sign-in, no cloud. Windows, Linux and macOS.

[![Build](https://github.com/mkamranr/claude-code-airgap/actions/workflows/airgap-packages.yml/badge.svg)](https://github.com/mkamranr/claude-code-airgap/actions/workflows/airgap-packages.yml)
[![Release](https://img.shields.io/github/v/release/mkamranr/claude-code-airgap?label=release&color=2ea44f)](https://github.com/mkamranr/claude-code-airgap/releases/latest)
[![Downloads](https://img.shields.io/github/downloads/mkamranr/claude-code-airgap/total?color=blue)](https://github.com/mkamranr/claude-code-airgap/releases)
[![Platforms](https://img.shields.io/badge/platforms-Windows%20%7C%20Linux%20%7C%20macOS-informational)](#downloads)
[![Claude Code](https://img.shields.io/badge/Claude%20Code-2.1.252-d97757)](https://code.claude.com/docs/en/overview)
[![Go](https://img.shields.io/github/go-mod/go-version/mkamranr/claude-code-airgap?filename=airgap-vllm%2Fgo.mod)](airgap-vllm/go.mod)

[Downloads](#downloads) ·
[Quick start](#quick-start) ·
[How it works](#how-it-works) ·
[Security model](#security-model) ·
[Operator runbook](airgap-vllm/README.md)

</div>

---

This fork of [`anthropics/claude-code`](https://github.com/anthropics/claude-code) adds
[`airgap-vllm/`](airgap-vllm/): a small Go bridge that lets the official, unmodified
Claude Code binary talk to a self-hosted [vLLM](https://github.com/vllm-project/vllm)
server. It ships as a verified, self-contained package that installs on a machine
with no network at all.

## Features

- **Fully offline:** no sign-in, telemetry, auto-update or outbound traffic. The bridge binds to `127.0.0.1` only.
- **Unmodified Claude Code:** Anthropic's signed binary, connected through the documented `ANTHROPIC_BASE_URL` setting.
- **Packages for each platform:** Windows x64 (`.zip` with a native PowerShell installer), and Linux x64, macOS Apple silicon and macOS Intel (`.tar.gz` with a bash installer).
- **Desktop app (Windows):** Claude Code in a window, with streamed replies, inline diffs, per-tool approve/deny, Stop and resume, and session history.
- **Works with any vLLM build:** native Anthropic passthrough where vLLM supports it, automatic translation to the OpenAI API where it doesn't.
- **Verified end to end:** GPG and SHA256 when the package is built. On the target, checksums are re-checked along with each platform's own signature check: Authenticode on Windows, Apple Developer ID on macOS, Anthropic's GPG-signed manifest on Linux.
- **No admin or root needed:** both installers write only to your user profile.
- **Built-in diagnostics:** `claude-vllm doctor` checks connectivity, model, context window and live tool calling.

## Downloads

Get the packages from the [latest release](https://github.com/mkamranr/claude-code-airgap/releases/latest).

| Package | Platform | Size | Contents |
|---|---|---|---|
| [`claude-vllm-airgap-2.1.252-windows-x64.zip`](https://github.com/mkamranr/claude-code-airgap/releases/latest/download/claude-vllm-airgap-2.1.252-windows-x64.zip) | Windows 10 / 11, x64 | ~420 MB | Claude Code, bridge, desktop app, WebView2 runtime, `install.ps1` |
| [`claude-vllm-airgap-2.1.252-linux-x64.tar.gz`](https://github.com/mkamranr/claude-code-airgap/releases/latest/download/claude-vllm-airgap-2.1.252-linux-x64.tar.gz) | Linux x64, glibc | ~100 MB | Claude Code, bridge, signed release manifest, `install.sh` |
| [`claude-vllm-airgap-2.1.252-macos-arm64.tar.gz`](https://github.com/mkamranr/claude-code-airgap/releases/latest/download/claude-vllm-airgap-2.1.252-macos-arm64.tar.gz) | macOS, Apple silicon | ~90 MB | Claude Code, bridge, signed release manifest, `install.sh` |
| [`claude-vllm-airgap-2.1.252-macos-x64.tar.gz`](https://github.com/mkamranr/claude-code-airgap/releases/latest/download/claude-vllm-airgap-2.1.252-macos-x64.tar.gz) | macOS, Intel | ~90 MB | Claude Code, bridge, signed release manifest, `install.sh` |

<details>
<summary><b>SHA256 checksums</b>: verify before you carry a package across</summary>

```
e5674871f67445cf530c3442b14d7d9b4808858c89b96769b13d2033ec84f138  claude-vllm-airgap-2.1.252-windows-x64.zip
1bc9c3ccf23ce6e47197c10e9cb5012a50b9ba53dde5a51aed5b8b4a4f9a13e3  claude-vllm-airgap-2.1.252-linux-x64.tar.gz
78d172a81e4f1c1e30aa7686adf83d11cc65b00148706806489a85c9b38e2a0b  claude-vllm-airgap-2.1.252-macos-arm64.tar.gz
0a2422b53be7e20efc8cb773a238054fd5edb767bd468695742c1594e9db0e91  claude-vllm-airgap-2.1.252-macos-x64.tar.gz
```

Each package also has a `.sha256` file beside it in the release. Carry the hash to
the air-gapped side separately from the package, and compare it again there.

</details>

## Quick start

Download the package on a connected machine, check its hash, and move it across on
whatever media your environment permits. Then:

### Windows

```powershell
Expand-Archive .\claude-vllm-airgap-2.1.252-windows-x64.zip -DestinationPath .\claude-vllm
cd .\claude-vllm
.\install.ps1              # verifies checksums and signatures, installs, adds to PATH
```

Open a new terminal, then:

```powershell
claude-vllm setup          # enter your vLLM URL and pick a model from the list
claude-vllm doctor         # check connectivity, context window and tool calling
claude-vllm                # start Claude Code
```

Or open **Claude vLLM Desktop** from the Start Menu. It opens its Settings screen the
first time and can run the same doctor checks.

### Linux

```bash
tar -xzf claude-vllm-airgap-2.1.252-linux-x64.tar.gz
cd claude-vllm-airgap-2.1.252-linux-x64
./install.sh               # verifies checksums and Anthropic's signature, installs, adds to PATH
```

Open a new terminal, then:

```bash
claude-vllm setup
claude-vllm doctor
claude-vllm
```

### macOS

Use `macos-arm64` on Apple silicon and `macos-x64` on Intel Macs. In Terminal:

```bash
tar -xzf claude-vllm-airgap-2.1.252-macos-arm64.tar.gz
cd claude-vllm-airgap-2.1.252-macos-arm64
./install.sh               # verifies checksums and Anthropic's Apple signature, installs, adds to PATH
```

Then the same `claude-vllm setup`, `doctor` and `claude-vllm` in a new terminal.

### Your vLLM server

Claude Code relies heavily on tools, so start vLLM with automatic tool choice and the
parser that matches your model:

```bash
vllm serve <MODEL> --enable-auto-tool-choice --tool-call-parser <PARSER>
```

`claude-vllm doctor` runs a live tool-calling test and tells you when the parser is
wrong.

## How it works

```mermaid
flowchart LR
    subgraph ws["Air-gapped workstation"]
        direction LR
        CC["Claude Code<br/>official signed binary"]
        DA["Desktop app<br/>Windows only"]
        BR["claude-vllm bridge<br/>127.0.0.1 only"]
        DA -- "headless stream-json" --> CC
        CC -- "Anthropic Messages API" --> BR
    end
    BR -- "/v1/messages (passthrough)" --> V[("Your vLLM server")]
    BR -. "/v1/chat/completions (translate)" .-> V
```

1. `claude-vllm` starts the bridge on a random loopback port and runs Claude Code
   with `ANTHROPIC_BASE_URL` pointing at it, plus the settings that keep it offline.
2. At startup the bridge probes your server once. If vLLM exposes a native Anthropic
   `/v1/messages` endpoint, requests pass through almost unchanged. Otherwise they are
   translated to and from the OpenAI chat-completions API, streaming included.
3. Either way, a compatibility filter removes request features a self-hosted server
   would reject.
4. The bridge exits when Claude Code exits.

The **desktop app** runs the same bridge and the same `claude.exe` through Claude
Code's documented headless mode. It renders the event stream as a chat, and answers
permission prompts through the `--permission-prompt-tool` hook, served on loopback
behind a per-session token.

## Security model

| Check | When the package is built | On the target |
|---|---|---|
| Release manifest signed by Anthropic (`31DD DE24 … 1A7E CACE`) | ✓ GPG `VALIDSIG` | ✓ Linux and macOS, when `gpg` is installed |
| Claude Code binary matches the signed manifest | ✓ | ✓ Linux and macOS, when `gpg` is installed |
| Apple Developer ID: Anthropic PBC (Team `Q6L2SF6YDW`) | | ✓ macOS |
| Authenticode signatures (Anthropic, Microsoft WebView2) | | ✓ Windows |
| Every file matches the in-package `SHA256SUMS` | written | ✓ both |
| Package matches its published SHA256 | you, before transfer | you, after transfer |

The bridge and desktop app are not code-signed. Their integrity on the target rests
on the package hash, so carry that hash over a channel separate from the package.
Full details are in the [runbook](airgap-vllm/README.md#what-is-verified-and-where).

## Configuration

Settings live in `~/.claude-vllm/config.json`, or `%USERPROFILE%\.claude-vllm\config.json` on Windows:

```bash
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

Every key can also be set as `CLAUDE_VLLM_<KEY>` in the environment. Inside a session,
`/model` lists whatever your server is currently serving.

> [!WARNING]
> `log = full` writes complete prompts and responses to disk, including source code
> from your sessions. Use it only while debugging.

## Building from source

On an internet-connected machine with `curl`, `gpg`, `shasum`, `zip`, `go` 1.26+ and
`python3`:

```bash
cd airgap-vllm
WEBVIEW2_CAB=/path/to/Microsoft.WebView2.FixedVersionRuntime.<ver>.x64.cab \
./scripts/stage.sh 2.1.252
```

This verifies the release, cross-compiles everything and writes both packages to
`airgap-vllm/dist/`. If any check fails, it stops without producing a package.
`PLATFORMS="linux macos"`, for example, builds a subset. The
[runbook](airgap-vllm/README.md#building-it-yourself) lists every option.

```
airgap-vllm/
├── cmd/claude-vllm/            CLI entry point
├── cmd/claude-vllm-desktop/    desktop app: Wails window and embedded HTML/CSS/JS UI
├── internal/config/            settings, environment overrides, validation
├── internal/filter/            request compatibility filter
├── internal/translate/         Anthropic ↔ OpenAI, including SSE streaming
├── internal/probe/             vLLM capability detection
├── internal/server/            the HTTP bridge
├── internal/launch/            bridge startup, environment contract, process supervision
├── internal/desktop/           desktop backend: headless sessions, permission prompts, history
├── internal/cli/               setup, doctor, serve, config
├── internal/fakevllm/          in-process vLLM test double
├── scripts/stage.sh            builds the verified packages
├── install.ps1                 Windows installer
└── install.sh                  Linux and macOS installer
```

`claude-vllm` links no third-party code and builds offline with an empty module
cache. The desktop app adds one dependency, [Wails v2](https://wails.io).

## Status

Every push that touches `airgap-vllm/` runs the
[Air-gap packages workflow](https://github.com/mkamranr/claude-code-airgap/actions/workflows/airgap-packages.yml).
It tests the code, builds all four packages, then installs each one on a GitHub-hosted
runner of its own platform:

| Platform | What CI checks on a real runner |
|---|---|
| Windows x64 | `install.ps1` under Windows PowerShell 5.1 verifies the Anthropic and Microsoft Authenticode signatures, installs the three executables and the WebView2 runtime, runs `claude.exe --version`, and uninstalls |
| Linux x64 | `install.sh` verifies Anthropic's GPG-signed manifest, installs, runs `claude --version`, rejects a tampered package, and uninstalls |
| macOS arm64 | `codesign` confirms Anthropic's Developer ID (Team `Q6L2SF6YDW`), along with the GPG check; then the same run, reject and uninstall steps as Linux |
| macOS x64 | The same checks, with the binaries running under Rosetta 2 on an Apple silicon runner |

The Go tests (`go test`, `go vet`, `gofmt`) run on Linux, Windows and macOS. The
Windows desktop app has also been driven end to end against `claude.exe` 2.1.252 and
a stand-in vLLM.

**Your acceptance checks.** CI runners have internet access and no vLLM, so these
need your environment:

1. `claude-vllm doctor`'s tool-calling test against your model.
2. An install on an actual air-gapped machine, for each platform you deploy.
3. The desktop app on your air-gapped Windows machine.

## Credit and license

Claude Code is built by [Anthropic](https://www.anthropic.com). This fork does not
patch, wrap or reimplement it. The bridge is an ordinary HTTP service on the
documented `ANTHROPIC_BASE_URL` extension point, and the desktop app is a front end
for Claude Code's documented headless mode. Everything outside `airgap-vllm/` is
upstream content, unmodified.

- Upstream: <https://github.com/anthropics/claude-code>
- Documentation: <https://code.claude.com/docs/en/overview>

Upstream content remains © Anthropic PBC, all rights reserved, under
[`LICENSE.md`](LICENSE.md) and Anthropic's
[Commercial Terms of Service](https://www.anthropic.com/legal/commercial-terms). This
fork does not relicense any of it. Using Claude Code requires a valid Anthropic
subscription or API access under Anthropic's terms, and this project does not alter,
bypass or substitute for that.

<sub>Claude and Claude Code are trademarks of Anthropic PBC. This project is
independent and not affiliated with or endorsed by Anthropic.</sub>
