# Air-gapped Claude Code on self-hosted vLLM

**Date:** 2026-09-19
**Status:** Approved design, pending implementation plan
**Target:** Windows x86_64 developer workstation with no internet access
**Upstream:** self-hosted vLLM serving `Qwen/Qwen3.8-27B`

## Problem

Developers on an air-gapped Windows network need Claude Code running against an
internally hosted vLLM server. Three things stand in the way:

1. Claude Code authenticates against Anthropic by default and expects a browser
   sign-in flow that cannot complete without internet.
2. vLLM's primary API is OpenAI-shaped; Claude Code speaks the Anthropic
   Messages API. Recent vLLM builds add a native `/v1/messages`, but its
   presence and completeness vary by version.
3. Both the installer and the auto-updater fetch from the network.

The deliverable is a transferable bundle that installs and runs offline, plus a
configuration surface that lets each developer point at their own vLLM endpoint
and model without editing code.

## Goals

- Claude Code runs on the air-gapped box with no sign-in and no network egress.
- vLLM URL and model are configurable per developer, at runtime, without a rebuild.
- Works whether or not the vLLM build exposes a native Anthropic endpoint.
- Installation artifacts are cryptographically verifiable on the far side of the gap.
- A single diagnostic command tells a developer why it is not working, offline.

## Non-goals

Deliberately cut to keep the surface small:

- Routing, load balancing, or failover across *multiple vLLM backends*. (Listing
  the models a single vLLM instance serves, and letting the developer pick one
  from the `/model` picker, is in scope and is how model selection works.)
- Authentication, OIDC, RBAC, audit logging, spend caps. (The first-party Claude
  apps gateway does this and needs Postgres plus an IdP; wrong tool for one
  air-gapped workstation.)
- Prompt-cache emulation. vLLM performs prefix caching server-side.
- Windows service or tray integration. The shim is a child process of the launcher.
- Supporting Linux or macOS targets in v1. The architecture does not preclude
  them; only the packaging scripts are platform-specific.

## Verified facts

Everything below was confirmed against the real 2.1.252 binary or the live
release bucket on 2026-09-19, not from memory. Implementation should not
re-litigate these.

**Endpoints Claude Code calls** (string occurrences in the binary):
`/v1/messages` (43), `/v1/models` (31), `/v1/messages/count_tokens` (14).

**Onboarding keys present in the binary:** `hasCompletedOnboarding`,
`hasTrustDialogAccepted`, `theme`, `customApiKeyResponses`,
`bypassPermissionsModeAccepted`.

**Env vars confirmed present:** `ANTHROPIC_BASE_URL`, `ANTHROPIC_AUTH_TOKEN`,
`ANTHROPIC_DEFAULT_{OPUS,SONNET,HAIKU}_MODEL` plus each one's `_NAME`,
`_DESCRIPTION` and `_SUPPORTED_CAPABILITIES` variants,
`ANTHROPIC_CUSTOM_MODEL_OPTION`, `CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY`,
`CLAUDE_CODE_GIT_BASH_PATH`, `CLAUDE_CODE_USE_POWERSHELL_TOOL`,
`USE_BUILTIN_RIPGREP`, `DISABLE_UPDATES`, `DISABLE_AUTOUPDATER`,
`DISABLE_TELEMETRY`, `DISABLE_ERROR_REPORTING`, `DISABLE_BUG_COMMAND`,
`DISABLE_COST_WARNINGS`, `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC`.

**Release bucket layout** (HTTP 200/206 verified):

    https://downloads.claude.ai/claude-code-releases/<version>/manifest.json
    https://downloads.claude.ai/claude-code-releases/<version>/manifest.json.sig
    https://downloads.claude.ai/claude-code-releases/<version>/win32-x64/claude.exe
    https://downloads.claude.ai/keys/claude-code.asc

GPG signing key fingerprint: `31DDDE24DDFAB679F42D7BD2BAA929FF1A7ECACE`.
Windows binaries additionally carry an Authenticode signature from
"Anthropic, PBC", verifiable offline via `Get-AuthenticodeSignature`.

For 2.1.252, `win32-x64` is `claude.exe`, 217,406,624 bytes, SHA256
`fe688ecde90d65fff0b2dd097597439774116077d80a3c1e131a7e47db307b1a`.

**The native Windows binary does not invoke Node at runtime.** Nothing on the
air-gapped box can be assumed to have a JavaScript or Python runtime, which
constrains the shim's implementation language.

## Architecture

    claude.exe ──Anthropic Messages──▶ claude-vllm.exe ──▶ vLLM
     (native)                          (127.0.0.1:PORT)     │
                                                            ├─ passthrough → /v1/messages
                                                            └─ translate   → /v1/chat/completions

A single local process sits between Claude Code and vLLM in all configurations.
Its `mode` setting selects a lane:

- `messages` — passthrough. Forward Anthropic-shaped requests to vLLM's native
  `/v1/messages`, applying only the compatibility filter.
- `openai` — translate. Convert Anthropic Messages to OpenAI chat completions
  and convert the response (including the SSE stream) back.
- `auto` — default. Probe `/v1/messages` once at startup; use passthrough if it
  answers, translate otherwise. The decision is logged and reported by `doctor`.

### Why a shim rather than pointing Claude Code straight at vLLM

Passthrough mode makes the shim nearly a no-op on the happy path, so its cost is
low. Its value is that it is the one seam where three things can live:

1. The compatibility filter (see below), which fixes real, documented breakages
   that otherwise surface as opaque HTTP 400s.
2. Request/response logging, which is the only debugging affordance available to
   a developer who cannot search the internet.
3. The model-tier mapping and `/v1/models` synthesis.

On a network with internet access, approach A (no shim) would be defensible. On
an air gap, the inability to diagnose a failure is the dominant risk.

### Language: Go

Cross-compiled with `CGO_ENABLED=0 GOOS=windows GOARCH=amd64` to a single static
executable of roughly 6 MB with no runtime dependency. The alternatives were
rejected: a Node shim would require shipping a ~110 MB `node.exe` since the
native Claude Code binary bundles no Node; Python cannot be assumed present;
PowerShell 5.1 is universally available but implementing a streaming SSE proxy
in it is fragile.

The shim uses only the Go standard library. No third-party modules, so `go build`
succeeds with an empty module cache — relevant if the bundle ever has to be
rebuilt inside a restricted network.

## Components

### `claude-vllm.exe`

One binary, several subcommands. Each has a single clear purpose.

| Command | Purpose |
|---|---|
| `setup` | Interactive first run. Prompts for vLLM URL, probes it, lists models from `/v1/models` as a menu, writes config and Claude Code settings. |
| `doctor` | Read-only diagnostics. Reports reachability, selected mode, model availability, and runs a live tool-calling smoke test. Prints suggested vLLM launch flags on failure. |
| *(default)* | Start shim on an ephemeral loopback port, export env, exec `claude.exe` with passed-through args, tear down on exit. |
| `serve` | Run the shim in the foreground on a fixed port. For debugging, or for sharing one shim across a team. |
| `config set key=value …` | Non-interactive config edit so no one hand-edits JSON. |
| `config show` | Print effective config including which values came from env overrides. |

### Internal packages

- `config` — load, validate, merge (file then env override), save. No I/O beyond the config file.
- `probe` — capability detection against a vLLM base URL: does `/v1/messages` exist, what does `/v1/models` return, does `/tokenize` exist.
- `filter` — the compatibility filter. Pure functions over decoded JSON; no network.
- `translate` — Anthropic ↔ OpenAI conversion, request and response, streaming and non-streaming. Pure functions; the most heavily tested package.
- `server` — HTTP handlers, mode dispatch, SSE plumbing, logging.
- `launch` — process supervision for the `claude.exe` child and env construction.

The `filter` and `translate` packages are pure and independently testable, which
is where nearly all the risk lives.

## Configuration

`%USERPROFILE%\.claude-vllm\config.json`:

```json
{
  "vllm_url":          "http://10.0.0.5:8000",
  "model":             "Qwen/Qwen3.8-27B",
  "api_key":           "",
  "mode":              "auto",
  "max_tokens":        8192,
  "context_window":    131072,
  "supports_thinking": false,
  "log":               "off"
}
```

Every key is overridable by a `CLAUDE_VLLM_`-prefixed environment variable
(`CLAUDE_VLLM_URL`, `CLAUDE_VLLM_MODEL`, and so on) so the setup can be driven
from a script or a shared profile.

- `api_key` is only needed if vLLM was started with `--api-key`.
- `supports_thinking` defaults false; `doctor` flips it if it detects a
  reasoning parser on the server.
- `log` is `off` | `requests` | `full`. `full` writes complete request and
  response bodies to `%USERPROFILE%\.claude-vllm\log\`. It will contain source
  code and prompts; the documentation must say so plainly.

## Claude Code environment contract

The launcher constructs this environment for the child `claude.exe`.

Auth and routing:

    ANTHROPIC_BASE_URL   = http://127.0.0.1:<ephemeral port>
    ANTHROPIC_AUTH_TOKEN = <non-empty placeholder>

Model mapping. All three tiers resolve to the single configured model so that
background Haiku-tier calls cannot 404:

    ANTHROPIC_DEFAULT_OPUS_MODEL   = <model>
    ANTHROPIC_DEFAULT_SONNET_MODEL = <model>
    ANTHROPIC_DEFAULT_HAIKU_MODEL  = <model>
    ANTHROPIC_DEFAULT_{OPUS,SONNET,HAIKU}_MODEL_NAME        = "<short label> (local vLLM)"
    ANTHROPIC_DEFAULT_{OPUS,SONNET,HAIKU}_MODEL_DESCRIPTION = "Self-hosted vLLM"
    CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY = 1

`ANTHROPIC_DEFAULT_{OPUS,SONNET,HAIKU}_MODEL_SUPPORTED_CAPABILITIES` is
deliberately **left unset by default**. These variables *override* Claude Code's
own capability detection, so setting them on a guess is riskier than not setting
them. The exact accepted token set is not reliably determinable from the
stripped binary and would be version-fragile if reverse-engineered. Treat them
as a documented escape hatch: expose them as optional config keys, leave them
empty, and only populate them if a concrete capability misdetection appears in
practice. `supports_thinking` in the config file drives this escape hatch.

Because the shim serves `/v1/models`, gateway model discovery lets the in-session
`/model` picker list whatever vLLM is currently serving. Developers can switch
models without touching config at all. This flag is specifically exempt from the
nonessential-traffic block, since it only queries the local shim.

Offline hygiene, written into `settings.json` under `env`:

    DISABLE_UPDATES, DISABLE_AUTOUPDATER, DISABLE_TELEMETRY,
    DISABLE_ERROR_REPORTING, DISABLE_BUG_COMMAND, DISABLE_COST_WARNINGS,
    CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC

`DISABLE_UPDATES` is the correct choice over `DISABLE_AUTOUPDATER` alone: the
documentation describes it as intended for distributing Claude Code through your
own channels, and it blocks manual update paths too. Both are set.

**Must not be set: `CLAUDE_CODE_USE_GATEWAY`.** It appears relevant and is a
trap — it forces Cloud-gateway sign-in and fails every request with "Not signed
in to the Cloud gateway". The installer should assert it is absent.

First-run suppression, written to `~/.claude.json`:

    hasCompletedOnboarding: true
    hasTrustDialogAccepted: true
    theme: <default>

Windows shell: if Git for Windows is present, set `CLAUDE_CODE_GIT_BASH_PATH`
so the Bash tool works. Otherwise Claude Code falls back to the PowerShell tool,
which is acceptable; `doctor` reports which is in effect.

## Compatibility filter

Applied in both modes. Every rule addresses a documented failure of
Anthropic-compatible third-party endpoints, not a hypothetical one.

| Rule | Why |
|---|---|
| Drop unknown content-block types | A shipped regression produced `400 … Input tag 'advisor_20260301'` against proxied endpoints. Whitelist known types, drop the rest. |
| Guarantee `tool_use` blocks carry an `id` | Third-party endpoints have streamed `tool_use` without one, causing a render crash on first tool call. Synthesize if absent. |
| Strip JSON-Schema `pattern`/`regex` keywords from tool inputs | Strict validators rejected a regex in a tool input schema, failing every turn. |
| Drop `tool_reference` blocks | Rejected by proxy endpoints; caused 400s until detection was added. |
| Strip `cache_control` | Meaningless to vLLM, which does prefix caching server-side. |
| Emit periodic SSE `ping` events | Claude Code's stream idle timeout has fired on custom gateways during long gaps. A 27B model's prefill can be slow enough to trigger it. |

`CLAUDE_CODE_DISABLE_ADVISOR_TOOL` is available as a belt-and-braces switch if
the advisor block type causes trouble despite the filter.

## Endpoints served by the shim

| Endpoint | Behavior |
|---|---|
| `POST /v1/messages` | Mode-dependent. Streaming and non-streaming. |
| `POST /v1/messages/count_tokens` | Proxy vLLM `/tokenize` for an exact count; fall back to a character-based estimate if absent. |
| `GET /v1/models` | Proxy vLLM's model list, reshaped to the Anthropic list format. |
| `GET /health` | Liveness for the launcher's readiness wait. |

## Streaming translation

The hardest part of `translate` mode. OpenAI `chat.completion.chunk` deltas must
become a well-formed Anthropic event sequence:

    message_start
      content_block_start / content_block_delta (text_delta) / content_block_stop
      content_block_start / content_block_delta (input_json_delta) / content_block_stop
    message_delta   (stop_reason, usage)
    message_stop

Specific hazards to handle: tool-call arguments arrive as fragments that must be
accumulated and re-emitted as `input_json_delta`; a tool call's `id` and `name`
arrive in the first fragment only; `finish_reason` maps to `stop_reason`
(`tool_calls` → `tool_use`, `length` → `max_tokens`, `stop` → `end_turn`); usage
may only appear on the final chunk.

## Packaging and workflow

**On a connected machine**, `stage.sh`:

1. Download `claude-code.asc`, import, assert fingerprint `31DD…CACE`.
2. Download `manifest.json` and `manifest.json.sig`; `gpg --verify`.
3. Download `win32-x64/claude.exe`; verify SHA256 against the manifest.
4. Cross-compile `claude-vllm.exe` for `windows/amd64`.
5. Emit `claude-vllm-airgap-<version>.zip` containing the binaries, `install.ps1`,
   a config template, docs, and `SHA256SUMS`.

The script must refuse to produce a bundle if any verification step fails.

**Transfer** by whatever approved removable media the environment permits.

**On the air-gapped Windows box:**

1. Unzip; `install.ps1` re-verifies `SHA256SUMS` locally and optionally checks
   the Authenticode signature on `claude.exe`. Both work with no network.
2. Installs to `%USERPROFILE%\.local\bin\`, matching the native installer's own
   layout, and adds it to `PATH`.
3. Writes `settings.json` and `~/.claude.json`.
4. `claude-vllm setup` → configure URL and model.
5. `claude-vllm doctor` → verify.
6. `claude-vllm` → run.

## Error handling

Failures must be legible to someone with no internet. Each of these produces a
specific message naming the likely cause and the next action:

- vLLM unreachable → show the URL tried, and that `config set url=` changes it.
- vLLM reachable, model not in `/v1/models` → list the models actually served.
- Tool-calling smoke test fails → print the suggested vLLM launch flags,
  `--enable-auto-tool-choice --tool-call-parser <parser>`, and note that the
  correct parser is model-specific.
- HTTP 400 from vLLM → log the exact request body when `log` is enabled, and say so.
- Port in use → pick another; only fail if the fixed `serve` port is taken.

## Testing

- **Golden-file tests** in `translate`: Anthropic request JSON → expected OpenAI
  JSON, and recorded OpenAI SSE streams → expected Anthropic event sequences.
  Pure, fast, offline.
- **`filter` unit tests**, one per rule in the table above, each with a fixture
  reproducing the shape that caused the original breakage.
- **`fakevllm`**, an in-process `httptest` server, for integration tests of
  streaming, tool calls, and mode selection without a real model.
- **`doctor`** is the on-box acceptance test.

**Known gap, stated plainly:** the translation layer can be tested exhaustively
here, but nothing in this repository can be tested against the real vLLM
deployment or against `Qwen/Qwen3.8-27B`. `doctor`'s live smoke test is what
closes that loop, and it is the first thing to run after install.

## Risks

| Risk | Mitigation |
|---|---|
| `Qwen/Qwen3.8-27B` tool-calling quality is unknown, and the correct vLLM `--tool-call-parser` for it cannot be verified from here. Claude Code is extremely tool-heavy; a model that emits malformed tool calls will be unusable regardless of this work. | `doctor` runs a live smoke test and suggests parser flags. This is a property of the model and server config, not something the shim can fix. |
| vLLM's native `/v1/messages` may be present but incomplete, so `auto` picks passthrough and then fails. | `mode` can be forced to `openai`. `doctor` reports which lane was chosen and why. |
| Claude Code's request shape changes in a future version and breaks the filter. | `DISABLE_UPDATES` pins the version. Upgrades are a deliberate re-staging action. |
| Context window mismatch between config and what vLLM was launched with causes truncation failures deep in a session. | `doctor` reads the served model's max length where vLLM exposes it and warns on mismatch. |

## Open questions

- Which vLLM version is deployed, and does it expose `/v1/messages`? `auto`
  removes the need to answer before building, but the answer determines which
  lane gets the real-world exercise.
- Is Git for Windows present on the target image? Determines Bash vs PowerShell
  tool. Not blocking; `doctor` reports it.
