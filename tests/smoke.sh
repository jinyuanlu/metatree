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

section "12. mt bind installs tmux keybindings using absolute mt path"
"$MT" bind >/dev/null 2>&1
# verify each binding exists on the running server AND uses absolute path
for key in g G N R; do
  tmux list-keys -T prefix | grep -qE "^bind-key\s+-T prefix\s+$key\b.*display-popup" \
    || fail "binding for prefix+$key not installed"
done
# the binding string must contain the absolute path to mt.sh, not bare "mt"
binding_g=$(tmux list-keys -T prefix | grep -E "^bind-key\s+-T prefix\s+g\b" || true)
echo "$binding_g" | grep -qF "$MT" \
  || fail "prefix+g binding does not use absolute mt path. got: $binding_g"
pass "prefix+g/G/N/R bound; binding uses absolute path ($MT)"

section "13. auto-direnv-allow skips encrypted .envrc (git-crypt safety)"
ENCRYPTED_FIXTURE="$TMP/with-encrypted-envrc"
git init -q -b main "$ENCRYPTED_FIXTURE"
# Write a fake git-crypt-encrypted .envrc: starts with the magic bytes
# "\x00GITCRYPT\x00" followed by binary garbage.
printf '\x00GITCRYPT\x00\x42\x99\x12\xaa\xbb\xccrest is binary garbage' \
  > "$ENCRYPTED_FIXTURE/.envrc"
(
  cd "$ENCRYPTED_FIXTURE"
  git add .envrc
  git -c user.name=test -c user.email=t@t commit -q -m "encrypted envrc"
)

ENC_CONFIG="$TMP/with-encrypted-config.toml"
cat >"$ENC_CONFIG" <<EOF
repos = ["$ENCRYPTED_FIXTURE"]
tmux_session = "mt-smoke"
tmux_window  = "dashboard"
branch_prefix = "smoke"
worktree_subdir = ".worktrees"
default_backend = "claude"
claude_cmd = "cat"
auto_direnv_allow = "true"
EOF

# stub direnv (reuses the stub from section 10) — log to a fresh file
export MT_DIRENV_LOG="$TMP/direnv-encrypted.log"
: > "$MT_DIRENV_LOG"

PATH="$STUB_BIN:$PATH" MT_CONFIG="$ENC_CONFIG" \
  MT_REPO="$ENCRYPTED_FIXTURE" MT_BRANCH="enc-test" "$MT" new --with claude >/dev/null 2>&1 &
MT_PID=$!
sleep 1
kill "$MT_PID" 2>/dev/null || true
wait "$MT_PID" 2>/dev/null || true

# direnv must NOT have been called for the encrypted .envrc — otherwise we'd
# be authorizing direnv to source binary garbage as a shell script.
[[ ! -s "$MT_DIRENV_LOG" ]] \
  || fail "direnv was invoked on encrypted .envrc — should be skipped. log: $(cat "$MT_DIRENV_LOG")"
pass "direnv NOT invoked on git-crypt-encrypted .envrc"

# restore for the credentials check
unset MT_DIRENV_LOG
export MT_CONFIG="$CONFIG"

section "14. real git-crypt: mt new decrypts .envrc in the new worktree"
if ! command -v git-crypt >/dev/null 2>&1; then
  pass "(skipped — git-crypt not installed)"
else
  GC_FIXTURE="$TMP/with-git-crypt"
  git init -q -b main "$GC_FIXTURE"
  (
    cd "$GC_FIXTURE"
    git-crypt init >/dev/null 2>&1
    echo '.envrc filter=git-crypt diff=git-crypt' > .gitattributes
    echo 'export REAL_DECRYPTED=success' > .envrc
    git add .gitattributes .envrc
    git -c user.name=t -c user.email=t@t commit -q -m "init"
  )

  # confirm .envrc is encrypted in the parent repo's object DB.
  # Bash's $(...) strips null bytes, so use xxd to compare hex strings.
  obj_dump="$TMP/obj-envrc.bin"
  git -C "$GC_FIXTURE" show HEAD:.envrc > "$obj_dump"
  obj_hex=$(head -c 10 "$obj_dump" | xxd -p | head -1)
  expected_hex="00474954435259505400"
  [[ "$obj_hex" == "$expected_hex"* ]] \
    || fail "test fixture: .envrc not git-crypt-encrypted (first 10 bytes: $obj_hex)"

  GC_CONFIG="$TMP/git-crypt-config.toml"
  cat >"$GC_CONFIG" <<EOF
repos = ["$GC_FIXTURE"]
tmux_session = "mt-smoke"
tmux_window  = "dashboard"
branch_prefix = "smoke"
worktree_subdir = ".worktrees"
default_backend = "claude"
claude_cmd = "cat"
auto_direnv_allow = "true"
EOF

  MT_CONFIG="$GC_CONFIG" MT_REPO="$GC_FIXTURE" MT_BRANCH="gc-decrypt" \
    "$MT" new --with claude >/dev/null 2>&1 &
  MT_PID=$!
  sleep 1
  kill "$MT_PID" 2>/dev/null || true
  wait "$MT_PID" 2>/dev/null || true

  WT="$GC_FIXTURE/.worktrees/gc-decrypt"
  [[ -d "$WT" ]] || fail "git-crypt worktree was not created"
  pass "worktree created at $WT"

  [[ -f "$WT/.envrc" ]] || fail ".envrc missing in worktree"
  # Compare first 10 bytes against git-crypt magic to test encryption status
  wt_hex=$(head -c 10 "$WT/.envrc" | xxd -p | head -1)
  if [[ "$wt_hex" == "$expected_hex"* ]]; then
    fail ".envrc in worktree is still encrypted (smudge filter did not run)"
  fi
  pass ".envrc in worktree is decrypted (first bytes: $wt_hex)"

  grep -qF 'REAL_DECRYPTED=success' "$WT/.envrc" \
    || fail "decrypted content does not match original"
  pass ".envrc content matches original ($(cat "$WT/.envrc"))"
fi

section "15. mt prune removes dead worktrees in bulk"
# create two worktrees in our acme-api fixture, kill one pane to make it dead
MT_REPO="$FIXTURE_REPO" MT_BRANCH="prune-keep" "$MT" new --with claude >/dev/null 2>&1 &
sleep 0.4; kill $! 2>/dev/null || true; wait $! 2>/dev/null || true
MT_REPO="$FIXTURE_REPO" MT_BRANCH="prune-die"  "$MT" new --with claude >/dev/null 2>&1 &
sleep 0.4; kill $! 2>/dev/null || true; wait $! 2>/dev/null || true

# kill the second pane to mark it dead
dead_pane=$(tmux list-panes -t mt-smoke:dashboard -F '#{pane_id} #{pane_title}' 2>/dev/null \
  | awk '$2 == "acme-api:prune-die" {print $1}' || true)
if [[ -n "$dead_pane" ]]; then
  tmux kill-pane -t "$dead_pane" || true
fi

# prune --force should remove the dead one but leave the live one
"$MT" prune --force >/dev/null 2>&1

[[ ! -d "$FIXTURE_REPO/.worktrees/prune-die" ]] \
  || fail "mt prune did not remove dead worktree"
pass "mt prune removed the dead worktree"

[[ -d "$FIXTURE_REPO/.worktrees/prune-keep" ]] \
  || fail "mt prune incorrectly removed the live worktree"
pass "mt prune left the live worktree intact"

section "16. mt switch lists dead worktrees with [dead] marker and revives them"
# kill the prune-keep pane to create a dead worktree to revive
keep_pane=$(tmux list-panes -t mt-smoke:dashboard -F '#{pane_id} #{pane_title}' 2>/dev/null \
  | awk '$2 == "acme-api:prune-keep" {print $1}' || true)
if [[ -n "$keep_pane" ]]; then
  tmux kill-pane -t "$keep_pane" || true
fi

# capture what mt switch shows fzf — use the show-stub
SHOW_STUB="$TMP/stub-show-rev"
mkdir -p "$SHOW_STUB"
cat > "$SHOW_STUB/fzf" <<'EOF'
#!/usr/bin/env bash
cat > /tmp/mt-switch-input.$$
exit 1
EOF
chmod +x "$SHOW_STUB/fzf"

PATH="$SHOW_STUB:$PATH" "$MT" switch >/dev/null 2>&1 || true
input_file=$(ls -t /tmp/mt-switch-input.* 2>/dev/null | head -1)
if [[ -n "$input_file" && -s "$input_file" ]]; then
  grep -qE 'acme-api:prune-keep.*\[dead\]' "$input_file" \
    || fail "mt switch did not list acme-api:prune-keep with [dead] marker"
  pass "mt switch lists dead worktree with [dead] marker"
  rm -f "$input_file"
fi

# Now revive: stub fzf to pick the dead entry
REV_STUB="$TMP/stub-revive"
mkdir -p "$REV_STUB"
cat > "$REV_STUB/fzf" <<'EOF'
#!/usr/bin/env bash
awk '/prune-keep/ && /\[dead\]/ { print; exit }'
EOF
chmod +x "$REV_STUB/fzf"

PATH="$REV_STUB:$PATH" "$MT" switch >/dev/null 2>&1 &
sleep 1
kill $! 2>/dev/null || true; wait $! 2>/dev/null || true

# the revived pane should now exist with the right title
tmux list-panes -t mt-smoke:dashboard -F '#{pane_title}' 2>/dev/null \
  | grep -qx "acme-api:prune-keep" \
  || fail "mt switch did not revive the dead worktree as a new pane"
pass "mt switch revived dead worktree → new pane acme-api:prune-keep"

section "17. mt logs every invocation; mt diagnose prints state cleanly"
TEST_LOG="$TMP/mt.log"
MT_LOG="$TEST_LOG" "$MT" --help >/dev/null 2>&1 || true
MT_LOG="$TEST_LOG" "$MT" ls >/dev/null 2>&1 || true

[[ -s "$TEST_LOG" ]] || fail "mt did not write to MT_LOG=$TEST_LOG"
grep -q 'INVOKE cmd=--help' "$TEST_LOG" \
  || fail "log missing INVOKE entry for --help (got: $(cat "$TEST_LOG"))"
grep -q 'EXIT cmd=--help' "$TEST_LOG" \
  || fail "log missing EXIT entry for --help"
pass "log captures INVOKE + EXIT for each invocation"

# diagnose runs cleanly and includes the key sections
diag_out=$(MT_LOG="$TEST_LOG" "$MT" diagnose 2>&1)
echo "$diag_out" | grep -q 'VERSIONS' || fail "diagnose missing VERSIONS section"
echo "$diag_out" | grep -q 'KEYBINDINGS' || fail "diagnose missing KEYBINDINGS section"
echo "$diag_out" | grep -q 'LOG' || fail "diagnose missing LOG section"
pass "mt diagnose prints VERSIONS / KEYBINDINGS / LOG sections"

section "18. mt new from a pane whose title was OSC-clobbered still splits"
# Real bug from screenshot: Claude Code emits OSC 2 to set pane_title to its
# cwd. mt's old detection (regex on pane_title) read mt_pane_count=0 →
# bare-shell-reuse path → send-keys into Claude's input. Fix uses the
# @mt-managed user option which can't be touched by escape sequences.
GC_FIX="$TMP/osc-test-repo"
git init -q -b main "$GC_FIX"
(cd "$GC_FIX" && echo a > a && git add a && git -c user.name=t -c user.email=t@t commit -q -m init)

OSC_CONFIG="$TMP/osc-config.toml"
cat >"$OSC_CONFIG" <<EOF
repos = ["$GC_FIX"]
tmux_session = "mt-smoke"
tmux_window  = "dashboard"
branch_prefix = "osc"
worktree_subdir = ".worktrees"
default_backend = "claude"
claude_cmd = "cat"
auto_direnv_allow = "false"
EOF

# create pane, then simulate OSC overwrite of pane_title
MT_CONFIG="$OSC_CONFIG" MT_REPO="$GC_FIX" MT_BRANCH="osc-pane-a" \
  "$MT" new --with claude >/dev/null 2>&1 &
sleep 0.5; kill $! 2>/dev/null || true; wait $! 2>/dev/null || true

osc_pane=$(tmux list-panes -t mt-smoke:dashboard \
  -F '#{pane_id}|#{@mt-managed}' 2>/dev/null \
  | awk -F'|' '$2 == "osc-test-repo:osc-pane-a" {print $1; exit}' || true)
[[ -n "$osc_pane" ]] || fail "could not locate osc-pane-a after creation"

# overwrite pane_title via OSC 2 (simulating Claude's behavior)
tmux respawn-pane -k -t "$osc_pane" "bash -c 'printf \"\\033]2;hijacked\\007\"; exec sleep 9999'" >/dev/null 2>&1
sleep 0.3

# pane_title should now be hijacked, but @mt-managed should be preserved
hijacked_title=$(tmux display-message -p -t "$osc_pane" '#{pane_title}' 2>/dev/null)
preserved_marker=$(tmux display-message -p -t "$osc_pane" '#{@mt-managed}' 2>/dev/null)
[[ "$hijacked_title" == "hijacked" ]] \
  || fail "OSC overwrite test setup broken: pane_title=$hijacked_title"
[[ "$preserved_marker" == "osc-test-repo:osc-pane-a" ]] \
  || fail "@mt-managed got clobbered (should be stable): $preserved_marker"
pass "pane_title hijacked but @mt-managed preserved"

# now run a SECOND mt new — must split (not send-keys), since @mt-managed = 1 pane
panes_before=$(tmux list-panes -t mt-smoke:dashboard -F '#{pane_id}' | wc -l | tr -d ' ')
MT_CONFIG="$OSC_CONFIG" MT_REPO="$GC_FIX" MT_BRANCH="osc-pane-b" \
  "$MT" new --with claude >/dev/null 2>&1 &
sleep 0.5; kill $! 2>/dev/null || true; wait $! 2>/dev/null || true
panes_after=$(tmux list-panes -t mt-smoke:dashboard -F '#{pane_id}' | wc -l | tr -d ' ')

[[ "$panes_after" -gt "$panes_before" ]] \
  || fail "mt new did not split — bare-shell-reuse fired despite OSC-clobbered title (panes: $panes_before → $panes_after)"
pass "mt new split a new pane (panes: $panes_before → $panes_after) instead of falling back to send-keys"

section "19. credentials.json is untouched (auth invariant §1.4)"

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
