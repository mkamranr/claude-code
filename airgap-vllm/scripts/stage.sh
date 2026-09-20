#!/usr/bin/env bash
# Build an air-gap bundle on an internet-connected machine.
#
#   ./scripts/stage.sh [claude-code-version]
#
# Verifies the Claude Code release with GPG and SHA256, cross-compiles the
# bridge for Windows x64, and emits a zip plus a checksum manifest. Aborts on
# any verification failure rather than shipping an unverified artifact.
set -euo pipefail

VERSION="${1:-2.1.252}"
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

echo "==> Importing the release signing key"
curl -fsSL "$KEY_URL" | gpg --quiet --import
if ! gpg --fingerprint security@anthropic.com 2>/dev/null \
     | tr -d ' \n' | grep -qi "$FINGERPRINT"; then
  echo "FATAL: signing key fingerprint does not match $FINGERPRINT" >&2
  exit 1
fi
echo "    fingerprint verified"

echo "==> Downloading and verifying manifest for $VERSION"
curl -fsSL -o "$work/manifest.json"     "$REPO/$VERSION/manifest.json"
curl -fsSL -o "$work/manifest.json.sig" "$REPO/$VERSION/manifest.json.sig"
gpg --verify "$work/manifest.json.sig" "$work/manifest.json" 2>&1 | grep -q "Good signature" || {
  echo "FATAL: manifest signature check failed" >&2
  exit 1
}
echo "    manifest signature good"

expected="$(python3 -c "
import json,sys
m=json.load(open('$work/manifest.json'))
print(m['platforms']['$PLATFORM']['checksum'])
")"
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

echo "==> Cross-compiling the bridge for windows/amd64"
( cd "$here" && CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
    go build -trimpath -ldflags "-s -w" -o "$work/claude-vllm.exe" ./cmd/claude-vllm )

cp "$here/install.ps1" "$work/install.ps1"
cp "$here/README.md"   "$work/README.md"

echo "==> Writing SHA256SUMS"
( cd "$work" && shasum -a 256 claude.exe claude-vllm.exe install.ps1 README.md > SHA256SUMS )

mkdir -p "$out"
bundle="$out/claude-vllm-airgap-$VERSION.zip"
rm -f "$bundle"
( cd "$work" && zip -q -r "$bundle" claude.exe claude-vllm.exe install.ps1 README.md SHA256SUMS )

echo
echo "Bundle ready: $bundle"
shasum -a 256 "$bundle"
echo
echo "Transfer it to the air-gapped machine, then run install.ps1 from PowerShell."
