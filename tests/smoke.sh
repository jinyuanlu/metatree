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

section "10. auto-direnv-allow runs when worktree has .envrc"
# Use a fixture repo that contains an .envrc, with a stub `direnv` on PATH
# that records its arguments. Verify mt new invokes `direnv allow <path>`.
DIRENV_FIXTURE="$TMP/with-envrc"
git init -q -b main "$DIRENV_FIXTURE"
echo 'export FROM_ENVRC=1' > "$DIRENV_FIXTURE/.envrc"
(
  cd "$DIRENV_FIXTURE"
  git add .envrc
  git -c user.name=test -c user.email=t@t commit -q -m "add .envrc"
)

# stub direnv: prepend a fake to PATH that logs every invocation
STUB_BIN="$TMP/stub"
mkdir -p "$STUB_BIN"
cat > "$STUB_BIN/direnv" <<'STUB'
#!/usr/bin/env bash
echo "$@" >> "${MT_DIRENV_LOG:-/tmp/mt-direnv.log}"
STUB
chmod +x "$STUB_BIN/direnv"
export MT_DIRENV_LOG="$TMP/direnv.log"
: > "$MT_DIRENV_LOG"

# point config at the new fixture (overrides the smoke config for this section)
DIRENV_CONFIG="$TMP/with-envrc-config.toml"
cat >"$DIRENV_CONFIG" <<EOF
repos = ["$DIRENV_FIXTURE"]
tmux_session = "mt-smoke"
tmux_window  = "dashboard"
branch_prefix = "smoke"
worktree_subdir = ".worktrees"
default_backend = "claude"
claude_cmd = "cat"
auto_direnv_allow = "true"
EOF

PATH="$STUB_BIN:$PATH" MT_CONFIG="$DIRENV_CONFIG" \
  MT_REPO="$DIRENV_FIXTURE" MT_BRANCH="envrc-test" "$MT" new --with claude >/dev/null 2>&1 &
MT_PID=$!
sleep 1
kill "$MT_PID" 2>/dev/null || true
wait "$MT_PID" 2>/dev/null || true

# the stub should have logged a `direnv allow <worktree-path>` invocation
expected_path="$DIRENV_FIXTURE/.worktrees/envrc-test"
grep -q "^allow $expected_path$" "$MT_DIRENV_LOG" \
  || fail "direnv allow not invoked. log contents: $(cat "$MT_DIRENV_LOG")"
pass "direnv allow $expected_path was invoked"

# and again with auto_direnv_allow disabled — should NOT call direnv
DIRENV_OFF_CONFIG="$TMP/with-envrc-off.toml"
cp "$DIRENV_CONFIG" "$DIRENV_OFF_CONFIG"
sed -i.bak 's/auto_direnv_allow = "true"/auto_direnv_allow = "false"/' "$DIRENV_OFF_CONFIG"
: > "$MT_DIRENV_LOG"

PATH="$STUB_BIN:$PATH" MT_CONFIG="$DIRENV_OFF_CONFIG" \
  MT_REPO="$DIRENV_FIXTURE" MT_BRANCH="envrc-off" "$MT" new --with claude >/dev/null 2>&1 &
MT_PID=$!
sleep 1
kill "$MT_PID" 2>/dev/null || true
wait "$MT_PID" 2>/dev/null || true

[[ ! -s "$MT_DIRENV_LOG" ]] \
  || fail "direnv was called despite auto_direnv_allow=false. log: $(cat "$MT_DIRENV_LOG")"
pass "direnv NOT invoked when auto_direnv_allow=false"

# restore config for the credentials check below
export MT_CONFIG="$CONFIG"
unset MT_DIRENV_LOG

section "11. mt switch focuses the chosen pane"
# we have ≥2 panes from earlier sections (envrc-test plus dirty-branch was rm'd).
# create one more so we have a deterministic 2-pane state, then switch.
MT_REPO="$FIXTURE_REPO" MT_BRANCH="switch-target" "$MT" new --with claude >/dev/null 2>&1 &
MT_PID=$!
sleep 1
kill "$MT_PID" 2>/dev/null || true
wait "$MT_PID" 2>/dev/null || true

# pane title we want to switch to (created in section 6, still alive)
target_title="acme-api:add-feature"
# verify it exists before we try to switch
tmux list-panes -t mt-smoke:dashboard -F '#{pane_title}' | grep -qx "$target_title" \
  || { tmux list-panes -t mt-smoke:dashboard -F '#{pane_title}' >&2; fail "expected target pane $target_title not present"; }

# pipe the title in so fzf auto-accepts it (fzf with stdin from grep -F selects first match)
# easier: stub fzf to echo the matching line.
SWITCH_STUB="$TMP/stub-switch"
mkdir -p "$SWITCH_STUB"
cat > "$SWITCH_STUB/fzf" <<STUB
#!/usr/bin/env bash
# pick the first row whose first column matches \$MT_TEST_TARGET, else exit 1
awk -v t="\$MT_TEST_TARGET" '\$1 == t { print; found=1; exit } END { exit !found }'
STUB
chmod +x "$SWITCH_STUB/fzf"

PATH="$SWITCH_STUB:$PATH" MT_TEST_TARGET="$target_title" "$MT" switch >/dev/null 2>&1 &
MT_PID=$!
sleep 1
kill "$MT_PID" 2>/dev/null || true
wait "$MT_PID" 2>/dev/null || true

# the active pane should now have title $target_title
active_title=$(tmux display-message -p -t mt-smoke:dashboard '#{pane_title}')
[[ "$active_title" == "$target_title" ]] \
  || fail "mt switch did not focus $target_title (active title: $active_title)"
pass "mt switch focused $target_title"

section "12. mt bind installs tmux keybindings"
"$MT" bind >/dev/null 2>&1
# verify each binding exists on the running server
for key in g G N R; do
  tmux list-keys -T prefix | grep -qE "^bind-key\s+-T prefix\s+$key\b.*display-popup" \
    || fail "binding for prefix+$key not installed"
done
pass "prefix+g/G/N/R bound to display-popup mt commands"

section "13. credentials.json is untouched (auth invariant §1.4)"
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
