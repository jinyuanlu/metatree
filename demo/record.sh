#!/usr/bin/env bash
# Records the README demo GIF.
#
# Sets up a throwaway fixture under /tmp/mt-demo: three fake repos with
# bare "remotes", a pre-existing stale worktree on one of them (so the
# very first `mt switch` shows a `[dead]` row), a fake claude script,
# and a scratch config. Then builds a fresh mt binary and runs vhs
# against demo/demo.tape. Output: demo.gif at the repo root.
#
# Requires: vhs (brew install vhs), go, git, tmux.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DEMO_DIR=/tmp/mt-demo
rm -rf "$DEMO_DIR"
mkdir -p "$DEMO_DIR/bin" "$DEMO_DIR/repos"

echo "==> building mt binary"
go build -o "$DEMO_DIR/bin/mt" "$ROOT/cmd/mt"

setup_repo() {
  local name="$1"
  git init -q -b main --bare "$DEMO_DIR/${name}.git"
  git init -q -b main "$DEMO_DIR/repos/$name"
  (
    cd "$DEMO_DIR/repos/$name"
    git config user.email "demo@example.com"
    git config user.name  "Demo"
    git config commit.gpgsign false
    echo "# $name" > README.md
    git add README.md
    git commit -q -m "initial commit"
    git remote add origin "$DEMO_DIR/${name}.git"
    git push -q origin main
    # Point origin/HEAD at main so DefaultBranch resolves via symbolic-ref.
    git remote set-head origin main 2>/dev/null || true
  )
}

echo "==> setting up 3 fixture repos with bare remotes"
setup_repo acme-api
setup_repo mira-web
setup_repo legacy-svc

echo "==> pre-creating stale worktree on legacy-svc (shows as [dead])"
# No agent will ever be bound to this worktree, so the very first
# invocation of `mt switch` will show it as a [dead] row that the demo
# can resume.
git -C "$DEMO_DIR/repos/legacy-svc" worktree add -q -b mt/old-fix \
  "$DEMO_DIR/repos/legacy-svc/.worktrees/old-fix" main

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
# Block so the pane stays populated for the rest of the recording.
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

echo "==> rendering GIF (this takes ~30s)"
cd "$ROOT"
# Kill any leftover server before recording so the demo starts clean.
tmux kill-server 2>/dev/null || true
PATH="$DEMO_DIR/bin:$PATH" MT_CONFIG="$DEMO_DIR/config.toml" \
  vhs demo/demo.tape

echo "==> done: $ROOT/demo.gif"
ls -la "$ROOT/demo.gif"
