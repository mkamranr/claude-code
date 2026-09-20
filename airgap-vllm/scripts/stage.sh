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
tmp_bundle="$out/.claude-vllm-airgap-$VERSION.zip.tmp"
rm -f "$tmp_bundle"
( cd "$work" && zip -q -r "$tmp_bundle" claude.exe claude-vllm.exe install.ps1 README.md SHA256SUMS )
mv "$tmp_bundle" "$bundle"

echo
echo "Bundle ready: $bundle"
shasum -a 256 "$bundle"
echo
echo "Transfer it to the air-gapped machine, then run install.ps1 from PowerShell."
