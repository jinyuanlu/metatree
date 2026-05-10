#!/usr/bin/env bash
# install.sh — fetch mt.sh and install it to ~/.local/bin/mt
#
# usage:
#   curl -fsSL https://raw.githubusercontent.com/<user>/metatree/main/install.sh | bash
#
# what this does:
#   1. downloads mt.sh from the metatree repo to ~/.local/bin/mt
#   2. makes it executable
#   3. prints a one-line PATH check
#
# what this does NOT do:
#   - does not install tmux, fzf, git, claude, or ollama
#   - does not modify your shell rc
#   - does not run anything as root
#
# safe to read before piping. ≤ 50 lines.

set -euo pipefail

REPO_URL="${MT_REPO_URL:-https://raw.githubusercontent.com/<user>/metatree/main}"
INSTALL_DIR="${MT_INSTALL_DIR:-$HOME/.local/bin}"
DEST="$INSTALL_DIR/mt"

echo "mt installer"
echo "  source: $REPO_URL/mt.sh"
echo "  dest:   $DEST"
echo

mkdir -p "$INSTALL_DIR"

if command -v curl >/dev/null 2>&1; then
  curl -fsSL "$REPO_URL/mt.sh" -o "$DEST"
elif command -v wget >/dev/null 2>&1; then
  wget -qO "$DEST" "$REPO_URL/mt.sh"
else
  echo "error: need curl or wget" >&2
  exit 1
fi

chmod +x "$DEST"

echo "installed: $DEST"
echo

case ":$PATH:" in
  *":$INSTALL_DIR:"*)
    echo "PATH already includes $INSTALL_DIR — try: mt --help"
    ;;
  *)
    echo "add to your shell rc:"
    echo "  export PATH=\"$INSTALL_DIR:\$PATH\""
    echo "then: mt --help"
    ;;
esac
