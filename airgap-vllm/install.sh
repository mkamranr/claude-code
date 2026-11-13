#!/usr/bin/env bash
# Install Claude Code and the vLLM bridge on an air-gapped Linux or macOS machine.
#
#   ./install.sh [--prefix DIR] [--no-modify-path] [--skip-signature-check]
#   ./install.sh --uninstall [--prefix DIR]
#
# Verifies SHA256SUMS, re-verifies claude's provenance (Anthropic's signed
# release manifest via gpg, and on macOS Anthropic's Developer ID signature),
# and installs claude and claude-vllm to ~/.local/bin. No network, no root.
#
# Written for bash 3.2, the version macOS ships.
set -euo pipefail

FINGERPRINT="31DDDE24DDFAB679F42D7BD2BAA929FF1A7ECACE"
# Anthropic PBC's Apple Developer Team ID, from the Developer ID Application
# certificate that signs the macOS claude binary.
APPLE_TEAM_ID="Q6L2SF6YDW"
MARK_BEGIN="# >>> claude-vllm >>>"
MARK_END="# <<< claude-vllm <<<"

prefix="$HOME/.local/bin"
modify_path=1
skip_sig=0
uninstall=0

usage() {
  sed -n '2,9p' "$0" | sed 's/^# \{0,1\}//'
  exit "${1:-0}"
}

while [ $# -gt 0 ]; do
  case "$1" in
    --prefix)         [ $# -ge 2 ] || usage 2; prefix="$2"; shift 2 ;;
    --prefix=*)       prefix="${1#--prefix=}"; shift ;;
    --no-modify-path) modify_path=0; shift ;;
    --skip-signature-check|--skip-gpg) skip_sig=1; shift ;;
    --uninstall)      uninstall=1; shift ;;
    -h|--help)        usage 0 ;;
    *) echo "unknown option: $1" >&2; usage 2 ;;
  esac
done

source_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
os="$(uname -s)"
green() { printf '\033[32m%s\033[0m\n' "$*"; }
warn()  { printf 'WARNING: %s\n' "$*" >&2; }
die()   { printf 'FATAL: %s\n' "$*" >&2; exit 1; }

rc_files() {
  local f
  for f in "$HOME/.bashrc" "$HOME/.bash_profile" "$HOME/.zshrc" "$HOME/.profile"; do
    if [ -f "$f" ]; then printf '%s\n' "$f"; fi
  done
}

# Remove the block this script added, leaving the rest of the file untouched.
strip_path_block() {
  local f="$1" tmp
  grep -qF "$MARK_BEGIN" "$f" || return 0
  tmp="$(mktemp)"
  awk -v b="$MARK_BEGIN" -v e="$MARK_END" '
    $0 == b {skip=1; next}
    $0 == e {skip=0; next}
    !skip' "$f" > "$tmp"
  cat "$tmp" > "$f"
  rm -f "$tmp"
  echo "  removed PATH entry from $f"
}

if [ "$uninstall" = 1 ]; then
  echo "==> Uninstalling from $prefix"
  for bin in claude claude-vllm; do
    if [ -e "$prefix/$bin" ]; then
      rm -f "$prefix/$bin"
      echo "  removed $prefix/$bin"
    fi
  done
  while IFS= read -r f; do strip_path_block "$f"; done < <(rc_files)
  echo
  green "Uninstalled."
  echo "Your settings in ~/.claude-vllm and Claude Code's data in ~/.claude were kept."
  echo "Delete them by hand if you no longer need them."
  exit 0
fi

# The bundle records which Claude Code build it carries; refuse a mismatch
# here rather than let claude fail later with an opaque exec error.
platform="$(cat "$source_dir/release/platform" 2>/dev/null || true)"
[ -n "$platform" ] || die "release/platform is missing from the bundle"
arch="$(uname -m)"
case "$platform" in
  linux-x64|linux-x64-musl)
    [ "$os" = "Linux" ] || die "this bundle is for Linux; this machine runs $os"
    case "$arch" in x86_64|amd64) ;; *) die "this bundle is for x86_64; this machine is $arch" ;; esac ;;
  darwin-arm64)
    [ "$os" = "Darwin" ] || die "this bundle is for macOS; this machine runs $os"
    [ "$arch" = "arm64" ] || die "this bundle is for Apple silicon (arm64); this Mac is $arch, use the macos-x64 bundle" ;;
  darwin-x64)
    [ "$os" = "Darwin" ] || die "this bundle is for macOS; this machine runs $os"
    if [ "$arch" = "arm64" ]; then
      warn "this is the Intel bundle on an Apple silicon Mac; it will run under Rosetta 2. The macos-arm64 bundle is native."
    fi ;;
  *) die "unknown platform in release/platform: $platform" ;;
esac

if command -v sha256sum >/dev/null; then
  sha() { sha256sum "$1" | awk '{print $1}'; }
elif command -v shasum >/dev/null; then
  sha() { shasum -a 256 "$1" | awk '{print $1}'; }
else
  die "need sha256sum or shasum to verify the bundle"
fi

echo "==> Verifying bundle checksums"
[ -f "$source_dir/SHA256SUMS" ] || die "SHA256SUMS not found next to install.sh"
re='^([0-9a-fA-F]{64}) [ *](.+)$'
while IFS= read -r line; do
  [[ "$line" =~ $re ]] || continue
  expected="$(printf '%s' "${BASH_REMATCH[1]}" | tr 'A-F' 'a-f')"
  file="${BASH_REMATCH[2]}"
  [ -f "$source_dir/$file" ] || die "listed in SHA256SUMS but missing: $file"
  actual="$(sha "$source_dir/$file")"
  if [ "$actual" != "$expected" ]; then
    die "checksum mismatch for $file
  expected $expected
  actual   $actual
Re-transfer the bundle; do not proceed with a mismatched file."
  fi
  echo "  ok  $file"
done < "$source_dir/SHA256SUMS"

# SHA256SUMS proves the bundle arrived intact. The checks below prove claude
# is the binary Anthropic published.
if [ "$skip_sig" = 1 ]; then
  echo "==> Skipping signature checks (--skip-signature-check). SHA256 was still verified above."
else
  if [ "$os" = "Darwin" ]; then
    echo "==> Verifying claude's Apple code signature"
    if command -v codesign >/dev/null; then
      codesign --verify --strict \
        -R="anchor apple generic and certificate leaf[subject.OU] = \"$APPLE_TEAM_ID\"" \
        "$source_dir/claude" 2>/dev/null \
        || die "claude is not validly signed by Anthropic's Developer ID (Team $APPLE_TEAM_ID)"
      echo "  ok  signed by Developer ID Application: Anthropic PBC ($APPLE_TEAM_ID)"
    else
      warn "codesign is not available, so claude's Apple signature was not checked."
    fi
  fi

  if command -v gpg >/dev/null; then
    echo "==> Verifying claude against Anthropic's signed release manifest"
    rel="$source_dir/release"
    for f in claude-code.asc manifest.json manifest.json.sig; do
      [ -f "$rel/$f" ] || die "release/$f is missing from the bundle"
    done
    gnupg="$(mktemp -d)"
    trap 'rm -rf "$gnupg"' EXIT
    chmod 700 "$gnupg"
    GNUPGHOME="$gnupg" gpg --quiet --import "$rel/claude-code.asc" 2>/dev/null
    fprs="$(GNUPGHOME="$gnupg" gpg --list-keys --with-colons | awk -F: '$1=="fpr"{print $10}')"
    grep -qx "$FINGERPRINT" <<< "$fprs" || die "release/claude-code.asc does not carry fingerprint $FINGERPRINT"
    status="$(GNUPGHOME="$gnupg" gpg --status-fd=1 --verify "$rel/manifest.json.sig" "$rel/manifest.json" 2>/dev/null || true)"
    grep -qE "^\[GNUPG:\] VALIDSIG ${FINGERPRINT}( |$)" <<< "$status" \
      || die "release/manifest.json is not validly signed by $FINGERPRINT"
    echo "  ok  manifest signed by $FINGERPRINT"
    claude_sum="$(sha "$source_dir/claude")"
    grep -qF "\"$claude_sum\"" "$rel/manifest.json" \
      || die "claude's SHA256 ($claude_sum) is not in the signed release manifest"
    echo "  ok  claude matches the signed manifest"
  elif [ "$os" = "Darwin" ]; then
    echo "  (gpg not installed; the Apple signature above is the provenance check)"
  else
    warn "gpg is not installed, so claude's release signature was not checked."
    warn "SHA256SUMS was verified. Install gpg, or pass --skip-signature-check to silence this."
  fi
fi

echo "==> Installing to $prefix"
mkdir -p "$prefix"
for bin in claude claude-vllm; do
  [ -f "$source_dir/$bin" ] || die "$bin is missing from the bundle"
  # Copy to a temporary name and rename, so a running claude is not
  # overwritten in place ("Text file busy").
  cp "$source_dir/$bin" "$prefix/.$bin.new"
  chmod 0755 "$prefix/.$bin.new"
  if [ "$os" = "Darwin" ] && command -v xattr >/dev/null; then
    # A browser-downloaded archive marks everything extracted from it as
    # quarantined, and Gatekeeper then blocks claude-vllm, which is not
    # notarized. The file's hash was verified above, so clear the flag.
    xattr -d com.apple.quarantine "$prefix/.$bin.new" 2>/dev/null || true
  fi
  mv -f "$prefix/.$bin.new" "$prefix/$bin"
  echo "  installed $bin"
done

case ":$PATH:" in
  *":$prefix:"*) echo "  $prefix already on PATH" ;;
  *)
    if [ "$modify_path" = 1 ]; then
      targets="$(rc_files)"
      if [ -z "$targets" ]; then
        # zsh is the macOS default shell and does not read ~/.profile.
        if [ "$os" = "Darwin" ]; then targets="$HOME/.zshrc"; else targets="$HOME/.profile"; fi
        touch "$targets"
      fi
      while IFS= read -r f; do
        if ! grep -qF "$MARK_BEGIN" "$f"; then
          printf '\n%s\nexport PATH="%s:$PATH"\n%s\n' "$MARK_BEGIN" "$prefix" "$MARK_END" >> "$f"
          echo "  added $prefix to PATH in $f"
        fi
      done <<< "$targets"
      echo "  open a new terminal, or run: export PATH=\"$prefix:\$PATH\""
    else
      warn "$prefix is not on your PATH. Add it yourself (--no-modify-path was given)."
    fi
    ;;
esac

# This variable forces Cloud-gateway sign-in and breaks every request. The
# launcher strips it from the claude child process, but flag it here too.
if [ -n "${CLAUDE_CODE_USE_GATEWAY:-}" ]; then
  warn "CLAUDE_CODE_USE_GATEWAY is set in your environment. Unset it: it forces Cloud sign-in and fails every request."
fi

echo
green "Installed. Next steps:"
echo "  1. Open a new terminal"
echo "  2. claude-vllm setup     # point at your vLLM server and pick a model"
echo "  3. claude-vllm doctor    # verify connectivity and tool calling"
echo "  4. claude-vllm           # start Claude Code"
