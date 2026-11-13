#!/usr/bin/env bash
# Build air-gap bundles on an internet-connected machine.
#
#   ./scripts/stage.sh [claude-code-version]
#
# Verifies the Claude Code release with GPG and SHA256 and emits, in dist/:
#
#   claude-vllm-airgap-<ver>-windows-x64.zip     claude.exe, the bridge, the
#                                                desktop app, install.ps1
#   claude-vllm-airgap-<ver>-linux-x64.tar.gz    claude, the bridge, install.sh
#
# each with a SHA256SUMS inside and a .sha256 file beside it. Aborts on any
# verification failure rather than shipping an unverified artifact.
#
#   PLATFORMS="windows linux"   # default; set to one of them to build only that
#   LINUX_PLATFORM=linux-x64    # or linux-x64-musl for musl distributions
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
PLATFORMS="${PLATFORMS:-windows linux}"
LINUX_PLATFORM="${LINUX_PLATFORM:-linux-x64}"
case "$LINUX_PLATFORM" in
  linux-x64|linux-x64-musl) ;;
  *) echo "FATAL: LINUX_PLATFORM must be linux-x64 or linux-x64-musl" >&2; exit 1 ;;
esac
for p in $PLATFORMS; do
  case "$p" in
    windows|linux) ;;
    *) echo "FATAL: unknown platform in PLATFORMS: $p (use windows and/or linux)" >&2; exit 1 ;;
  esac
done
want() { [[ " $PLATFORMS " == *" $1 "* ]]; }
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
if [ -n "$WEBVIEW2_CAB" ] && want windows; then
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
curl -fsSL -o "$work/claude-code.asc" "$KEY_URL"
gpg --quiet --import "$work/claude-code.asc"

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


# fetch_claude <manifest-platform> <binary-name> <dest-dir>
# Downloads one platform's binary and checks it against the signed manifest.
fetch_claude() {
  local platform="$1" binary="$2" dest="$3" expected actual
  expected="$(python3 -c "
import json,sys
m=json.load(sys.stdin)
print(m['platforms']['$platform']['checksum'])
" < "$work/manifest.json")"
  [ -n "$expected" ] || { echo "FATAL: no checksum for $platform in manifest" >&2; exit 1; }
  echo "==> Downloading $binary ($platform)"
  curl -fSL --progress-bar -o "$dest/$binary" "$REPO/$VERSION/$platform/$binary"
  actual="$(shasum -a 256 "$dest/$binary" | awk '{print $1}')"
  if [ "$actual" != "$expected" ]; then
    echo "FATAL: checksum mismatch for $platform/$binary" >&2
    echo "  expected $expected" >&2
    echo "  actual   $actual" >&2
    exit 1
  fi
  echo "    checksum verified: $actual"
}

# write_sums <dir>: SHA256SUMS over every file in <dir>, so the installer's
# check covers all of it. Forward-slash paths; PowerShell accepts both.
write_sums() {
  ( cd "$1" && find . -type f ! -name SHA256SUMS | sed 's|^\./||' | LC_ALL=C sort \
      | while IFS= read -r f; do shasum -a 256 "$f"; done > SHA256SUMS )
}

# publish <tmp-file> <final-name>: move into dist/ and write <name>.sha256.
publish() {
  local final="$out/$2"
  mv "$1" "$final"
  # "<hash>  <name>", the text-mode form both sha256sum -c and Get-FileHash
  # users can read; shasum on Windows would otherwise write "*<name>".
  printf '%s  %s
' "$(shasum -a 256 "$final" | awk '{print $1}')" "$2" > "$final.sha256"
  built+=("$final")
}

mkdir -p "$out"
built=()

if want windows; then
  stage="$work/windows"
  mkdir -p "$stage"
  fetch_claude win32-x64 claude.exe "$stage"

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
  echo "==> Writing SHA256SUMS (windows)"
  write_sums "$stage"

  name="claude-vllm-airgap-$VERSION-windows-x64.zip"
  rm -f "$work/$name"
  ( cd "$stage" && zip -q -r "$work/$name" . )
  publish "$work/$name" "$name"
fi

if want linux; then
  top="claude-vllm-airgap-$VERSION-linux-x64"
  stage="$work/linux/$top"
  mkdir -p "$stage/release"
  fetch_claude "$LINUX_PLATFORM" claude "$stage"

  echo "==> Cross-compiling the bridge for linux/amd64"
  ( cd "$here" && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
      go build -trimpath -ldflags "-s -w" -o "$stage/claude-vllm" ./cmd/claude-vllm )

  # Linux has no Authenticode, so the signed manifest and the key travel with
  # the bundle and install.sh re-checks claude against them offline.
  cp "$work/manifest.json" "$work/manifest.json.sig" "$work/claude-code.asc" "$stage/release/"
  cp "$here/install.sh" "$stage/install.sh"
  cp "$here/README.md"  "$stage/README.md"
  echo "==> Writing SHA256SUMS (linux)"
  write_sums "$stage"

  # Built with Python's tarfile rather than tar so the executable bits are set
  # explicitly: a Windows build host has no POSIX modes to carry over.
  name="$top.tar.gz"
  python3 - "$work/linux" "$top" "$work/$name" <<'PY'
import os, sys, tarfile
root, top, dest = sys.argv[1:]
executable = {"claude", "claude-vllm", "install.sh"}
def add(tar, rel):
    full = os.path.join(root, rel)
    info = tar.gettarinfo(full, arcname=rel.replace(os.sep, "/"))
    info.uid = info.gid = 0
    info.uname = info.gname = "root"
    if info.isdir():
        info.mode = 0o755
        tar.addfile(info)
    else:
        info.mode = 0o755 if os.path.basename(rel) in executable else 0o644
        with open(full, "rb") as f:
            tar.addfile(info, f)
with tarfile.open(dest, "w:gz") as tar:
    add(tar, top)
    for dirpath, dirnames, filenames in os.walk(os.path.join(root, top)):
        dirnames.sort()
        for d in dirnames:
            add(tar, os.path.relpath(os.path.join(dirpath, d), root))
        for fn in sorted(filenames):
            add(tar, os.path.relpath(os.path.join(dirpath, fn), root))
PY
  publish "$work/$name" "$name"
fi

echo
echo "Bundles ready in $out:"
for b in "${built[@]}"; do
  printf '  %s\n    sha256 %s\n' "$(basename "$b")" "$(cut -d' ' -f1 < "$b.sha256")"
done
echo
echo "Transfer to the air-gapped machine and run install.ps1 (Windows) or install.sh (Linux)."
