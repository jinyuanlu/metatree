#!/usr/bin/env bash
# install.sh — fetch the mt binary from GitHub Releases.
#
# usage:
#   curl -fsSL https://raw.githubusercontent.com/jinyuanlu/metatree/master/install.sh | bash
#
# env:
#   MT_INSTALL_DIR   override install dir (default: ~/.local/bin)
#
# Audit-friendly: short enough to read before piping into bash.

set -euo pipefail

REPO="jinyuanlu/metatree"
INSTALL_DIR="${MT_INSTALL_DIR:-$HOME/.local/bin}"

case "$(uname -s)-$(uname -m)" in
  Darwin-arm64)             ASSET=mt-darwin-arm64 ;;
  Darwin-x86_64)            ASSET=mt-darwin-amd64 ;;
  Linux-x86_64)             ASSET=mt-linux-amd64  ;;
  Linux-aarch64|Linux-arm64) ASSET=mt-linux-arm64 ;;
  *) echo "unsupported: $(uname -s)-$(uname -m)" >&2; exit 1 ;;
esac

BASE="https://github.com/${REPO}/releases/latest/download"
TARBALL="${ASSET}.tar.gz"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

echo "mt installer  ($ASSET → $INSTALL_DIR/mt)"

curl -fsSL "$BASE/$TARBALL" -o "$TMP/$TARBALL"

# Verify checksum if we have curl + sha256sum (or shasum on macOS). Skip with
# a warning otherwise — graceful degradation, not a hard fail.
if curl -fsSL "$BASE/checksums.txt" -o "$TMP/checksums.txt" 2>/dev/null; then
  if command -v sha256sum >/dev/null 2>&1; then
    (cd "$TMP" && grep " $TARBALL\$" checksums.txt | sha256sum -c -)
  elif command -v shasum >/dev/null 2>&1; then
    (cd "$TMP" && grep " $TARBALL\$" checksums.txt | shasum -a 256 -c -)
  else
    echo "  (no sha256 tool found; skipping checksum verification)"
  fi
else
  echo "  (no checksums.txt in release; skipping verification)"
fi

mkdir -p "$INSTALL_DIR"
tar -xz -C "$INSTALL_DIR" -f "$TMP/$TARBALL" mt
chmod +x "$INSTALL_DIR/mt"

# Print the build identity from the freshly-installed binary. Anyone
# triaging "is the fix in?" can paste this line into a bug report and
# we can map the commit SHA back to a PR. Falls back gracefully if
# --version is missing (older binaries).
identity="$("$INSTALL_DIR/mt" --version 2>/dev/null || echo "$INSTALL_DIR/mt (version unknown)")"
echo "installed: $identity"
case ":$PATH:" in
  *":$INSTALL_DIR:"*) echo "PATH ok — try: mt --help" ;;
  *) echo "add to your shell rc:  export PATH=\"$INSTALL_DIR:\$PATH\"" ;;
esac
