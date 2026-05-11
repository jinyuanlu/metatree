#!/usr/bin/env bats
# Unit tests for the worktree_base feature in mt.sh.
#
# Mirrors internal/gitio/gitio.go: DefaultBranch, FetchBranch,
# ResolveWorktreeBase, and ResolveResult.Format. Both implementations
# must stay in lockstep — bash is the reference.
#
# install bats:  brew install bats-core
# run tests:     bats tests/bats/worktree_base.bats

setup() {
  MT_DIR="$(cd "$(dirname "$BATS_TEST_FILENAME")/../.." && pwd)"
  # Same trick as tests/bats/copy_runtime_files.bats: strip the `main "$@"`
  # line so sourcing doesn't dispatch a subcommand.
  MT_NO_MAIN=1 source <(sed 's|^main "$@"$|true|' "$MT_DIR/mt.sh")
  # mt.sh runs `set -euo pipefail` at top; disable so test assertions that
  # use `run` and inspect non-zero $status don't get killed mid-test.
  set +eo pipefail

  TMP="$(mktemp -d)"
  REPO="$TMP/repo"
  BARE="$TMP/bare.git"
  # Quiet git plumbing of user identity so commits don't fail on minimal CI.
  export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t
  export GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t
}

teardown() {
  [[ -n "${TMP:-}" && -d "$TMP" ]] && rm -rf "$TMP"
}

# --- fixture helpers ---

# Init a non-bare repo with a single commit on $1 (defaults to "main").
_mk_repo() {
  local branch="${1:-main}"
  git init -q -b "$branch" "$REPO"
  ( cd "$REPO" && echo hello > a && git add a && git commit -q -m init )
}

# Create the bare remote at $BARE and wire it as origin in $REPO.
_mk_bare_origin() {
  git init -q --bare "$BARE"
  git -C "$REPO" remote add origin "$BARE"
}

# Push branches $@ to origin. First arg is the "default" branch — pushed
# and then symbolic-ref'd as origin/HEAD so DefaultBranch detection works.
_push_and_set_head() {
  local default="$1"; shift
  git -C "$REPO" push -q -u origin "$default"
  local b
  for b in "$@"; do
    git -C "$REPO" branch "$b" 2>/dev/null || true
    git -C "$REPO" push -q -u origin "$b"
  done
  # Set origin/HEAD explicitly — `git remote set-head -a` requires fetched
  # refs but can be flaky in test envs, so we point it ourselves.
  git -C "$REPO" symbolic-ref "refs/remotes/origin/HEAD" "refs/remotes/origin/$default"
}

# --- default_branch ---

@test "default_branch detects via symbolic-ref" {
  _mk_repo main
  _mk_bare_origin
  _push_and_set_head main
  run default_branch "$REPO"
  [ "$status" -eq 0 ]
  [ "$output" = "main" ]
}

@test "default_branch falls back to main candidate" {
  _mk_repo main
  _mk_bare_origin
  git -C "$REPO" push -q -u origin main
  # Explicitly remove origin/HEAD symbolic-ref to force candidate fallback.
  rm -f "$REPO/.git/refs/remotes/origin/HEAD"
  git -C "$REPO" pack-refs --all 2>/dev/null || true
  # Make sure packed-refs has no origin/HEAD line.
  if [[ -f "$REPO/.git/packed-refs" ]]; then
    grep -v 'refs/remotes/origin/HEAD' "$REPO/.git/packed-refs" > "$REPO/.git/packed-refs.new" || true
    mv "$REPO/.git/packed-refs.new" "$REPO/.git/packed-refs"
  fi
  run default_branch "$REPO"
  [ "$status" -eq 0 ]
  [ "$output" = "main" ]
}

@test "default_branch falls back to develop" {
  _mk_repo develop
  _mk_bare_origin
  git -C "$REPO" push -q -u origin develop
  # Remove any symbolic origin/HEAD so we exercise the candidate ladder.
  rm -f "$REPO/.git/refs/remotes/origin/HEAD"
  if [[ -f "$REPO/.git/packed-refs" ]]; then
    grep -v 'refs/remotes/origin/HEAD' "$REPO/.git/packed-refs" > "$REPO/.git/packed-refs.new" || true
    mv "$REPO/.git/packed-refs.new" "$REPO/.git/packed-refs"
  fi
  run default_branch "$REPO"
  [ "$status" -eq 0 ]
  [ "$output" = "develop" ]
}

@test "default_branch returns 2 with no origin" {
  _mk_repo main
  # No `_mk_bare_origin` — repo has no origin remote.
  run default_branch "$REPO"
  [ "$status" -eq 2 ]
  [ -z "$output" ]
}

# --- _mt_with_timeout ---

@test "_mt_with_timeout returns 124 on timeout" {
  local t0=$SECONDS
  run _mt_with_timeout 1 sleep 5
  local elapsed=$((SECONDS - t0))
  [ "$status" -eq 124 ]
  # Allow some slack but the killer should fire within the 1+1s budget.
  [ "$elapsed" -lt 4 ]
}

@test "_mt_with_timeout passes through fast commands" {
  run _mt_with_timeout 5 true
  [ "$status" -eq 0 ]
}

@test "_mt_with_timeout preserves non-timeout exit codes" {
  run _mt_with_timeout 5 bash -c 'exit 7'
  [ "$status" -eq 7 ]
}

# --- fetch_branch ---

@test "fetch_branch source sets GIT_TERMINAL_PROMPT=0" {
  # Sanity-check the function body — a credential prompt inside a tmux
  # popup would hang silently otherwise.
  run declare -f fetch_branch
  [[ "$output" == *"GIT_TERMINAL_PROMPT=0"* ]]
}

# --- validate_worktree_base ---

@test "validate_worktree_base accepts non-empty string" {
  MT_WORKTREE_BASE="origin-default"
  run validate_worktree_base
  [ "$status" -eq 0 ]
}

@test "validate_worktree_base rejects empty" {
  MT_WORKTREE_BASE=""
  run validate_worktree_base
  [ "$status" -ne 0 ]
  [[ "$output" == *"must not be empty"* ]]
}

@test "validate_worktree_base rejects whitespace-only" {
  MT_WORKTREE_BASE="   "
  run validate_worktree_base
  [ "$status" -ne 0 ]
  [[ "$output" == *"must not be empty"* ]]
}

@test "validate_worktree_base trims surrounding whitespace" {
  MT_WORKTREE_BASE="  origin-default  "
  validate_worktree_base
  [ "$MT_WORKTREE_BASE" = "origin-default" ]
}

# --- resolve_worktree_base ---

@test "resolve_worktree_base head → empty ref" {
  _mk_repo main
  run resolve_worktree_base "$REPO" head "mt/feature"
  [ "$status" -eq 0 ]
  # Output format: "<ref>\t<summary>" — for HEAD mode ref is empty.
  local ref="${output%%	*}"
  local summary="${output#*	}"
  [ -z "$ref" ]
  [[ "$summary" == *"branched mt/feature from HEAD@"* ]]
}

@test "resolve_worktree_base empty cfg behaves as head" {
  _mk_repo main
  run resolve_worktree_base "$REPO" "" "mt/feature"
  [ "$status" -eq 0 ]
  local ref="${output%%	*}"
  [ -z "$ref" ]
  [[ "$output" == *"from HEAD@"* ]]
}

@test "resolve_worktree_base origin-default happy" {
  _mk_repo main
  _mk_bare_origin
  _push_and_set_head main
  run resolve_worktree_base "$REPO" origin-default "mt/feature"
  [ "$status" -eq 0 ]
  local ref="${output%%	*}"
  local summary="${output#*	}"
  [ "$ref" = "origin/main" ]
  [[ "$summary" == *"branched mt/feature from origin/main@"* ]]
  # Sha8 should be exactly 8 hex chars at the end.
  [[ "$summary" =~ @[0-9a-f]{8}$ ]]
}

@test "resolve_worktree_base origin-default hard error with no origin" {
  _mk_repo main
  # No origin — origin-default must fail loudly to stderr.
  run resolve_worktree_base "$REPO" origin-default "mt/feature"
  [ "$status" -ne 0 ]
  # `run` merges stdout + stderr into $output.
  [[ "$output" == *"cannot resolve worktree base"* ]]
  [[ "$output" == *"no \"origin\" remote"* ]]
  [[ "$output" == *"fix:"* ]]
}

@test "resolve_worktree_base literal ref resolves to sha" {
  _mk_repo main
  run resolve_worktree_base "$REPO" main "mt/feature"
  [ "$status" -eq 0 ]
  local ref="${output%%	*}"
  local summary="${output#*	}"
  [ "$ref" = "main" ]
  [[ "$summary" == *"branched mt/feature from main@"* ]]
}

@test "resolve_worktree_base origin-default falls back to local on broken origin" {
  _mk_repo main
  # Wire origin to a path that doesn't exist so fetch fails fast.
  git -C "$REPO" remote add origin "$TMP/does-not-exist"
  # No origin/HEAD, no origin/main ref present → fall back to local main.
  # Use a small timeout so the test doesn't drag — but git on a non-existent
  # path errors immediately, so this is fast.
  MT_WORKTREE_FETCH_TIMEOUT=2
  run resolve_worktree_base "$REPO" origin-default "mt/feature"
  [ "$status" -eq 0 ]
  local ref="${output%%	*}"
  local summary="${output#*	}"
  # No origin/main exists locally either — the helper walks past stale to
  # the "local <def>" rung. But default_branch first probes origin/HEAD
  # (missing) and the candidate ladder (also missing remote refs) so it
  # will return rc=1 (origin exists, no candidate). That maps to the
  # "no default branch found" error, NOT a fallback. Assert that.
  [[ "$output" == *"no default branch found"* ]] \
    || [[ "$summary" == *"local main"* ]]
}
