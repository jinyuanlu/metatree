#!/usr/bin/env bash
# Records the README demo GIF.
#
# Sets up a throwaway fixture under /tmp/mt-demo (fake repo + bare "remote"
# + fake claude script + scratch config), builds a fresh mt binary, and
# runs vhs against demo/demo.tape. Output: demo.gif at the repo root.
#
# Requires: vhs (brew install vhs), go, git, tmux.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DEMO_DIR=/tmp/mt-demo
rm -rf "$DEMO_DIR"
mkdir -p "$DEMO_DIR/bin" "$DEMO_DIR/repos"

echo "==> building mt binary"
go build -o "$DEMO_DIR/bin/mt" "$ROOT/cmd/mt"

echo "==> setting up fixture repo + bare remote"
git init -q -b main --bare "$DEMO_DIR/remote.git"

git init -q -b main "$DEMO_DIR/repos/my-app"
cd "$DEMO_DIR/repos/my-app"
git config user.email "demo@example.com"
git config user.name  "Demo"
git config commit.gpgsign false
echo "# my-app" > README.md
git add README.md
git commit -q -m "initial commit"
git remote add origin "$DEMO_DIR/remote.git"
git push -q origin main
# point origin/HEAD at main so DefaultBranch picks it up via symbolic-ref
git remote set-head origin main 2>/dev/null || true
cd "$ROOT"

echo "==> writing fake claude agent"
cat > "$DEMO_DIR/bin/fake-claude" <<'BASH'
#!/usr/bin/env bash
clear
printf '\n'
printf '   \033[1;35m■ Claude\033[0m  \033[2m(demo agent)\033[0m\n\n'
printf '   cwd:    %s\n' "$(pwd)"
printf '   branch: %s\n' "$(git branch --show-current 2>/dev/null || echo '?')"
printf '\n'
printf '   Ready. Tell me what to build.\n'
printf '\n   > '
# Block forever so the pane stays populated during the GIF recording.
sleep 3600
BASH
chmod +x "$DEMO_DIR/bin/fake-claude"

echo "==> writing scratch mt config"
cat > "$DEMO_DIR/config.toml" <<EOF
repos_dirs          = ["$DEMO_DIR/repos"]
claude_cmd          = "$DEMO_DIR/bin/fake-claude"
worktree_base       = "origin-default"
worktree_copy_files = []
auto_status_chrome  = "true"
auto_direnv_allow   = "false"
EOF

echo "==> rendering GIF (this takes ~20s)"
cd "$ROOT"
# Kill any leftover server before recording so the demo starts clean.
tmux kill-server 2>/dev/null || true
PATH="$DEMO_DIR/bin:$PATH" MT_CONFIG="$DEMO_DIR/config.toml" \
  vhs demo/demo.tape

echo "==> done: $ROOT/demo.gif"
ls -la "$ROOT/demo.gif"
