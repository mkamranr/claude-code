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

## On a connected machine

Requires `curl`, `gpg`, `shasum`, `zip`, `go`, and `python3` on PATH. `gpg` in
particular is easy to be missing on a fresh machine — install it before
running this if `command -v gpg` comes back empty.

```bash
./scripts/stage.sh 2.1.252
```

This imports the Claude Code release signing key (fingerprint
`31DD DE24 DDFA B679 F42D 7BD2 BAA9 29FF 1A7E CACE`), verifies it against the
release manifest's GPG signature, downloads the Windows binary, checks its
SHA256, cross-compiles the bridge, and writes
`dist/claude-vllm-airgap-2.1.252.zip` containing `claude.exe`,
`claude-vllm.exe`, `install.ps1`, `README.md`, and `SHA256SUMS`. It aborts
rather than emitting an unverified bundle.

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

`install.ps1` re-verifies every file's SHA256 against `SHA256SUMS` and checks
the Authenticode signature on `claude.exe`, failing loudly and naming the file
on any mismatch — the transfer across the air gap is exactly where corruption
or tampering would happen, so it does not trust the checks that ran on the
connected machine. It needs no network and no administrator rights: it writes
only under `%USERPROFILE%` and sets your user (not machine) PATH. It installs
`claude.exe` and `claude-vllm.exe` to `%USERPROFILE%\.local\bin` and adds that
directory to your PATH only if it isn't already there.

If your organization's policy hard-fails an offline Authenticode revocation
check instead of letting it time out, re-run with `.\install.ps1
-SkipSignatureCheck` — the SHA256 verification still runs regardless and needs
no network under any policy.

## Configuration

Settings live in `%USERPROFILE%\.claude-vllm\config.json`, written with
0600-equivalent permissions since it can hold a vLLM API key. Change settings
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
| `supports_thinking` | Leave false unless your server runs a reasoning parser |
| `log` | `off` (default), `requests`, or `full` |

Every key is also settable as an environment variable: `CLAUDE_VLLM_URL`,
`CLAUDE_VLLM_MODEL`, `CLAUDE_VLLM_API_KEY`, `CLAUDE_VLLM_MODE`,
`CLAUDE_VLLM_MAX_TOKENS`, `CLAUDE_VLLM_CONTEXT_WINDOW`,
`CLAUDE_VLLM_SUPPORTS_THINKING`, `CLAUDE_VLLM_LOG`.

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

## Troubleshooting

| Symptom | Cause and fix |
|---|---|
| `Not signed in to the Cloud gateway` | `CLAUDE_CODE_USE_GATEWAY` is set in your environment. The launcher strips it from the child Claude Code process, but a wrapper-level export can still confuse other tooling — unset it |
| Model replies in prose instead of calling tools | vLLM is missing `--enable-auto-tool-choice` or is using the wrong `--tool-call-parser`. Run `claude-vllm doctor` |
| `cannot reach vLLM at <url>` | Wrong URL or the server is down. `claude-vllm config set url=...` |
| `model "X" is not served by this vLLM` | `claude-vllm doctor` and the error both list what IS served — pick one with `claude-vllm config set model=...` |
| A sign-in prompt appears | `~/.claude.json` is missing `hasCompletedOnboarding`. Re-run `claude-vllm setup` |
| HTTP 400 from vLLM | Set `log=full`, reproduce, read the logged request body in `%USERPROFILE%\.claude-vllm\log\`, then set `log` back to `off` |

## Updating Claude Code

Auto-update is disabled by design (`DISABLE_UPDATES`). To move to a new
version, re-run `stage.sh` with the new version number on a connected machine
and reinstall. Re-run `claude-vllm doctor` afterwards: a new Claude Code
release can change the request shape the bridge filters.
