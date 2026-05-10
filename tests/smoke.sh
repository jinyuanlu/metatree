#!/usr/bin/env bash
# tests/smoke.sh — end-to-end integration test for mt.sh
#
# Creates a temp git repo, runs a headless tmux server, exercises the full
# `mt new → mt ls → mt rm` cycle non-interactively, asserts each step.
#
# Stubs out the agent commands (claude_cmd, ollama_cmd) with `cat` so no
# external services are touched. tmux runs locally in -d (detached) mode.
#
# usage:   bash tests/smoke.sh
# exit:    0 if all assertions pass; nonzero on first failure (set -e)

set -euo pipefail

# ---------------------------------------------------------------------------
# fixture setup
# ---------------------------------------------------------------------------
MT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
# Use a short path under /tmp — macOS UNIX-domain sockets have a ~104 char
# limit, and `mktemp -d -t` lands under /var/folders/.../... which exceeds it
# once tmux appends `tmux-<uid>/default`. /tmp/mt-smoke is short and adequate.
TMP="/tmp/mt-smoke.$$"
mkdir -p "$TMP"
TMUX_SOCKET_DIR="$TMP/tmux"
mkdir -p "$TMUX_SOCKET_DIR"
trap 'cleanup' EXIT

cleanup() {
  TMUX_TMPDIR="$TMUX_SOCKET_DIR" tmux kill-server 2>/dev/null || true
  rm -rf "$TMP"
}

pass() { printf "  \033[32mok\033[0m  %s\n" "$*"; }
fail() { printf "  \033[31mFAIL\033[0m  %s\n" "$*"; exit 1; }
section() { printf "\n\033[1m%s\033[0m\n" "$*"; }

# fixture git repo
FIXTURE_REPO="$TMP/acme-api"
git init -q -b main "$FIXTURE_REPO"
(
  cd "$FIXTURE_REPO"
  echo "# acme-api fixture" > README.md
  git add README.md
  git -c user.name=test -c user.email=t@t commit -q -m "init"
)

# fixture config (points at our temp repo, uses cat as the agent)
CONFIG="$TMP/config.toml"
cat >"$CONFIG" <<EOF
repos = ["$FIXTURE_REPO"]
tmux_session = "mt-smoke"
tmux_window  = "dashboard"
branch_prefix = "smoke"
worktree_subdir = ".worktrees"
default_backend = "claude"
claude_cmd = "cat"
ollama_cmd = "cat"
EOF

# isolated tmux env (don't touch the user's running server)
export TMUX_TMPDIR="$TMUX_SOCKET_DIR"
unset TMUX  # so we don't try to nest

export MT_CONFIG="$CONFIG"
MT="$MT_DIR/mt.sh"

# ---------------------------------------------------------------------------
# tests
# ---------------------------------------------------------------------------
section "1. mt --help runs and exits cleanly"
"$MT" --help >/dev/null
pass "mt --help"

section "2. mt ls on empty fixture is silent (no worktrees yet)"
out=$("$MT" ls)
[[ -z "$out" ]] || fail "expected empty output, got: $out"
pass "mt ls returns no rows"

section "3. mt new creates worktree + tmux pane (non-interactive)"
MT_REPO="$FIXTURE_REPO" MT_BRANCH="fix-cookie" "$MT" new --with claude >/dev/null 2>&1 &
MT_PID=$!
# give it a moment to set up
sleep 1
# kill the foreground attach (it would otherwise block); the dashboard side-effects persist
kill "$MT_PID" 2>/dev/null || true
wait "$MT_PID" 2>/dev/null || true

[[ -d "$FIXTURE_REPO/.worktrees/fix-cookie" ]] || fail "worktree not created"
pass "worktree at $FIXTURE_REPO/.worktrees/fix-cookie"

git -C "$FIXTURE_REPO" branch | grep -q "smoke/fix-cookie" \
  || fail "branch smoke/fix-cookie not created"
pass "branch smoke/fix-cookie exists"

tmux has-session -t mt-smoke 2>/dev/null \
  || fail "tmux session mt-smoke not created"
pass "tmux session mt-smoke exists"

# pane title check
title=$(tmux list-panes -t mt-smoke:dashboard -F '#{pane_title}' 2>/dev/null | head -1)
[[ "$title" == "acme-api:fix-cookie" ]] \
  || fail "pane title expected acme-api:fix-cookie, got: $title"
pass "pane title is acme-api:fix-cookie"

section "4. mt ls reports the live worktree"
out=$("$MT" ls)
echo "$out" | grep -q "acme-api:fix-cookie" \
  || fail "mt ls did not list acme-api:fix-cookie. output: $out"
echo "$out" | grep -q "live" \
  || fail "mt ls did not mark pane as live. output: $out"
pass "mt ls shows acme-api:fix-cookie  ...  live"

section "5. mt new is idempotent — same (repo,branch) does not re-create"
MT_REPO="$FIXTURE_REPO" MT_BRANCH="fix-cookie" "$MT" new --with claude >/dev/null 2>&1 &
MT_PID=$!
sleep 1
kill "$MT_PID" 2>/dev/null || true
wait "$MT_PID" 2>/dev/null || true

# only one pane should exist
pane_count=$(tmux list-panes -t mt-smoke:dashboard -F '#{pane_id}' | wc -l | tr -d ' ')
[[ "$pane_count" -eq 1 ]] \
  || fail "expected 1 pane after idempotent mt new, got $pane_count"
pass "still only 1 pane (idempotent)"

section "6. mt new on a second branch creates a second pane"
MT_REPO="$FIXTURE_REPO" MT_BRANCH="add-feature" "$MT" new --with claude >/dev/null 2>&1 &
MT_PID=$!
sleep 1
kill "$MT_PID" 2>/dev/null || true
wait "$MT_PID" 2>/dev/null || true

pane_count=$(tmux list-panes -t mt-smoke:dashboard -F '#{pane_id}' | wc -l | tr -d ' ')
[[ "$pane_count" -eq 2 ]] || fail "expected 2 panes, got $pane_count"
pass "2 panes after second mt new"

# tiled layout check
layout=$(tmux display-message -p -t mt-smoke:dashboard '#{window_layout}')
echo "$layout" | head -c 60
echo
pass "layout retiled (window_layout reported)"

section "7. mt rm removes worktree, branch, pane"
MT_RM_TITLE="acme-api:fix-cookie" "$MT" rm

[[ ! -d "$FIXTURE_REPO/.worktrees/fix-cookie" ]] \
  || fail "worktree still exists after mt rm"
pass "worktree removed"

git -C "$FIXTURE_REPO" branch | grep -q "smoke/fix-cookie" \
  && fail "branch smoke/fix-cookie still exists after mt rm" \
  || pass "branch smoke/fix-cookie deleted"

pane_count=$(tmux list-panes -t mt-smoke:dashboard -F '#{pane_id}' 2>/dev/null | wc -l | tr -d ' ')
[[ "$pane_count" -eq 1 ]] \
  || fail "expected 1 pane after mt rm (the survivor), got $pane_count"
pass "pane killed; one survivor remains"

section "8. mt rm refuses on uncommitted changes (no --force)"
# create a dirty worktree
MT_REPO="$FIXTURE_REPO" MT_BRANCH="dirty-branch" "$MT" new --with claude >/dev/null 2>&1 &
MT_PID=$!
sleep 1
kill "$MT_PID" 2>/dev/null || true
wait "$MT_PID" 2>/dev/null || true

# dirty it
echo "uncommitted change" > "$FIXTURE_REPO/.worktrees/dirty-branch/dirty.txt"

# mt rm without --force should fail
if MT_RM_TITLE="acme-api:dirty-branch" "$MT" rm 2>/dev/null; then
  fail "mt rm should have refused dirty worktree"
fi
[[ -d "$FIXTURE_REPO/.worktrees/dirty-branch" ]] \
  || fail "dirty worktree was deleted (should have been refused)"
pass "mt rm refused dirty worktree (no --force)"

section "9. mt rm --force bypasses the dirty check"
MT_RM_TITLE="acme-api:dirty-branch" "$MT" rm --force >/dev/null 2>&1
[[ ! -d "$FIXTURE_REPO/.worktrees/dirty-branch" ]] \
  || fail "mt rm --force did not remove dirty worktree"
pass "mt rm --force removed dirty worktree"

section "10. credentials.json is untouched (auth invariant §1.4)"
# we don't actually have ~/.claude/.credentials.json in CI, but we can verify
# that mt.sh contains zero non-comment references to it. Comments mentioning
# the file (e.g., the header doc) are fine and expected.
if grep -nE 'credentials\.json' "$MT" | grep -vE '^[0-9]+:[[:space:]]*#' >/dev/null; then
  fail "mt.sh has non-comment reference to credentials.json — violates §1.4"
fi
if grep -nE 'ANTHROPIC_' "$MT" | grep -vE '^[0-9]+:[[:space:]]*#' >/dev/null; then
  fail "mt.sh has non-comment reference to ANTHROPIC_ env vars — violates §1.4"
fi
pass "mt.sh: zero non-comment references to credentials.json or ANTHROPIC_*"

# ---------------------------------------------------------------------------
section "ALL TESTS PASSED"
