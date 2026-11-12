#!/usr/bin/env bash
# Build an air-gap bundle on an internet-connected machine.
#
#   ./scripts/stage.sh [claude-code-version]
#
# Verifies the Claude Code release with GPG and SHA256, cross-compiles the
# bridge and the desktop app for Windows x64, and emits a zip plus a checksum
# manifest. Aborts on any verification failure rather than shipping an
# unverified artifact.
#
# Desktop app runtime (optional, but needed on targets without WebView2, which
# includes offline Windows 10 machines that never got it from Windows Update):
#
#   WEBVIEW2_CAB=/path/Microsoft.WebView2.FixedVersionRuntime.<ver>.x64.cab
#   WEBVIEW2_CAB_SHA256=<hex>   # optional: pin the cab you downloaded
#
# Download the x64 "Fixed Version" cab from Microsoft's WebView2 download page.
# Its URL is versioned, so it is passed in rather than fetched here. The
# runtime's own Authenticode signature is checked again on the target by
# install.ps1.
set -euo pipefail

VERSION="${1:-2.1.252}"
if ! [[ "$VERSION" =~ ^[A-Za-z0-9._-]+$ ]]; then
  echo "FATAL: invalid version string: $VERSION" >&2
  exit 1
fi
PLATFORM="win32-x64"
BINARY="claude.exe"
FINGERPRINT="31DDDE24DDFAB679F42D7BD2BAA929FF1A7ECACE"
REPO="https://downloads.claude.ai/claude-code-releases"
KEY_URL="https://downloads.claude.ai/keys/claude-code.asc"

here="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work="$(mktemp -d)"
out="$here/dist"
trap 'rm -rf "$work"' EXIT

for tool in curl gpg shasum zip go python3; do
  command -v "$tool" >/dev/null || { echo "missing required tool: $tool" >&2; exit 1; }
done

# Resolve the WebView2 cab and an extractor before any download, so a missing
# tool fails in the first second rather than after the release is fetched.
WEBVIEW2_CAB="${WEBVIEW2_CAB:-}"
cab_extractor=""
if [ -n "$WEBVIEW2_CAB" ]; then
  [ -f "$WEBVIEW2_CAB" ] || { echo "FATAL: WEBVIEW2_CAB=$WEBVIEW2_CAB does not exist" >&2; exit 1; }
  if command -v cabextract >/dev/null; then
    cab_extractor="cabextract"
  elif command -v cygpath >/dev/null && [ -n "${SYSTEMROOT:-}" ] \
       && [ -x "$(cygpath -u "$SYSTEMROOT")/System32/expand.exe" ]; then
    # Git Bash on Windows: Windows' own expand.exe, by full path, because the
    # expand.exe on Git Bash's PATH is the coreutils tab expander.
    cab_extractor="$(cygpath -u "$SYSTEMROOT")/System32/expand.exe"
  else
    echo "missing required tool: cabextract (needed for WEBVIEW2_CAB)" >&2
    exit 1
  fi
fi

# Every gpg operation below runs against a keyring created for this run and
# thrown away with $work (see the trap above). Importing into the operator's
# real ~/.gnupg would mean verifying against every key they happen to trust:
# on a developer machine holding other keys, a manifest signed by any one of
# them would satisfy a "Good signature" check. This keyring holds exactly one
# key -- the one fetched from $KEY_URL and matched against $FINGERPRINT below.
export GNUPGHOME="$work/gnupg"
mkdir -p "$GNUPGHOME"
chmod 700 "$GNUPGHOME"

echo "==> Importing the release signing key"
curl -fsSL "$KEY_URL" | gpg --quiet --import

imported_fprs="$(gpg --list-keys --with-colons | awk -F: '$1=="fpr"{print $10}')"
if [ -z "$imported_fprs" ]; then
  echo "FATAL: no key was imported from $KEY_URL" >&2
  exit 1
fi
if ! grep -qx "$FINGERPRINT" <<< "$imported_fprs"; then
  echo "FATAL: imported key does not carry fingerprint $FINGERPRINT" >&2
  exit 1
fi
echo "    fingerprint verified"

echo "==> Downloading and verifying manifest for $VERSION"
curl -fsSL -o "$work/manifest.json"     "$REPO/$VERSION/manifest.json"
curl -fsSL -o "$work/manifest.json.sig" "$REPO/$VERSION/manifest.json.sig"
# The gate is VALIDSIG on the machine-readable status output, not "Good
# signature" on the human-readable one. "Good signature" only says some key in
# the keyring signed this; VALIDSIG names the key, so this binds the signature
# to $FINGERPRINT -- the single check the whole air-gap story rests on. This is
# the one download whose contents decide what every later check compares
# against, so it must fail closed.
#
# The status output is captured rather than piped into `grep -q`: `set -o
# pipefail` is in force above, and `grep -q` exits on its first match, which
# can hand gpg a SIGPIPE and fail the pipeline on a signature that was in fact
# good. gpg's own exit status is deliberately not the gate -- VALIDSIG is.
gpg_status="$(gpg --status-fd=1 --verify "$work/manifest.json.sig" "$work/manifest.json" 2>/dev/null || true)"
if ! grep -qE "^\[GNUPG:\] VALIDSIG ${FINGERPRINT}( |$)" <<< "$gpg_status"; then
  echo "FATAL: manifest is not validly signed by $FINGERPRINT" >&2
  exit 1
fi
echo "    manifest signature good (VALIDSIG $FINGERPRINT)"

expected="$(python3 -c "
import json,sys
m=json.load(sys.stdin)
print(m['platforms']['$PLATFORM']['checksum'])
" < "$work/manifest.json")"
[ -n "$expected" ] || { echo "FATAL: no checksum for $PLATFORM in manifest" >&2; exit 1; }

echo "==> Downloading $BINARY ($PLATFORM)"
curl -fSL --progress-bar -o "$work/$BINARY" "$REPO/$VERSION/$PLATFORM/$BINARY"
actual="$(shasum -a 256 "$work/$BINARY" | awk '{print $1}')"
if [ "$actual" != "$expected" ]; then
  echo "FATAL: checksum mismatch" >&2
  echo "  expected $expected" >&2
  echo "  actual   $actual" >&2
  exit 1
fi
echo "    checksum verified: $actual"

# Everything that ships is assembled in $stage, apart from the keyring and
# manifest above, so SHA256SUMS and the zip can simply take the whole folder.
stage="$work/bundle"
mkdir -p "$stage"
mv "$work/$BINARY" "$stage/$BINARY"

echo "==> Cross-compiling the bridge for windows/amd64"
( cd "$here" && CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
    go build -trimpath -ldflags "-s -w" -o "$stage/claude-vllm.exe" ./cmd/claude-vllm )

echo "==> Cross-compiling the desktop app for windows/amd64"
# A Wails "manual build": no wails CLI and no Node toolchain. The
# wv2runtime.error tag replaces Wails' default missing-WebView2 strategy
# (download it), which can only hang on an air-gapped machine, with a message.
( cd "$here" && CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
    go build -tags desktop,production,wv2runtime.error -trimpath \
    -ldflags "-s -w -H windowsgui" -o "$stage/claude-vllm-desktop.exe" ./cmd/claude-vllm-desktop )

if [ -n "$WEBVIEW2_CAB" ]; then
  echo "==> Unpacking the WebView2 Fixed Version runtime"
  cab_sum="$(shasum -a 256 "$WEBVIEW2_CAB" | awk '{print $1}')"
  if [ -n "${WEBVIEW2_CAB_SHA256:-}" ]; then
    if [ "$cab_sum" != "$(tr 'A-F' 'a-f' <<< "$WEBVIEW2_CAB_SHA256")" ]; then
      echo "FATAL: WEBVIEW2_CAB checksum mismatch" >&2
      echo "  expected $WEBVIEW2_CAB_SHA256" >&2
      echo "  actual   $cab_sum" >&2
      exit 1
    fi
    echo "    cab checksum pinned and verified: $cab_sum"
  else
    echo "    cab sha256 $cab_sum (set WEBVIEW2_CAB_SHA256 to pin it)"
  fi
  cab_dir="$work/cab"
  mkdir -p "$cab_dir"
  if [ "$cab_extractor" = "cabextract" ]; then
    cabextract -q -d "$cab_dir" "$WEBVIEW2_CAB"
  else
    "$cab_extractor" "$(cygpath -w "$WEBVIEW2_CAB")" -F:* "$(cygpath -w "$cab_dir")" >/dev/null
  fi
  # The cab holds one top-level Microsoft.WebView2.FixedVersionRuntime.<ver>.x64
  # folder. Ship its contents as webview2/, where the app looks for them.
  runtime_exe="$(find "$cab_dir" -name msedgewebview2.exe -type f | head -n 1)"
  [ -n "$runtime_exe" ] || { echo "FATAL: no msedgewebview2.exe inside $WEBVIEW2_CAB" >&2; exit 1; }
  mv "$(dirname "$runtime_exe")" "$stage/webview2"
  rm -rf "$cab_dir"
  echo "    runtime unpacked ($(find "$stage/webview2" -type f | wc -l | tr -d ' ') files)"
else
  echo "WARN: WEBVIEW2_CAB is not set. The desktop app will only start on targets"
  echo "      that already have the WebView2 runtime; the terminal client is unaffected."
fi

cp "$here/install.ps1" "$stage/install.ps1"
cp "$here/README.md"   "$stage/README.md"

echo "==> Writing SHA256SUMS"
# Every file in the bundle, the WebView2 runtime included, so install.ps1's
# check covers all of it. Paths use forward slashes; PowerShell accepts both.
( cd "$stage" && find . -type f ! -name SHA256SUMS | sed 's|^\./||' | LC_ALL=C sort \
    | while IFS= read -r f; do shasum -a 256 "$f"; done > SHA256SUMS )

mkdir -p "$out"
bundle="$out/claude-vllm-airgap-$VERSION.zip"
tmp_bundle="$out/.claude-vllm-airgap-$VERSION.zip.tmp"
rm -f "$tmp_bundle"
( cd "$stage" && zip -q -r "$tmp_bundle" . )
mv "$tmp_bundle" "$bundle"

echo
echo "Bundle ready: $bundle"
shasum -a 256 "$bundle"
echo
echo "Transfer it to the air-gapped machine, then run install.ps1 from PowerShell."
[ -n "$WEBVIEW2_CAB" ] || echo "(No WebView2 runtime bundled: see WEBVIEW2_CAB at the top of this script.)"
