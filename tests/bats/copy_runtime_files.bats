#!/usr/bin/env bats
# Unit tests for the worktree_copy_files feature in mt.sh.
#
# Mirrors internal/gitio/gitio.go:CopyRuntimeFiles (Go) and
# internal/config/config.go:validateWorktreeCopyFiles. Both implementations
# must stay in lockstep — bash is the spec/reference.
#
# install bats:  brew install bats-core
# run tests:     bats tests/bats/copy_runtime_files.bats

setup() {
  MT_DIR="$(cd "$(dirname "$BATS_TEST_FILENAME")/../.." && pwd)"
  # Same trick as tests/mt.bats: strip the `main "$@"` line so sourcing
  # doesn't try to dispatch a subcommand.
  MT_NO_MAIN=1 source <(sed 's|^main "\$@"$|true|' "$MT_DIR/mt.sh")
  TMP="$(mktemp -d)"
  SRC="$TMP/src"
  DST="$TMP/dst"
  mkdir -p "$SRC" "$DST"
}

teardown() {
  [[ -n "${TMP:-}" && -d "$TMP" ]] && rm -rf "$TMP"
}

# --- copy_runtime_files ---

@test "copy .env happy path" {
  printf 'FOO=bar\n' > "$SRC/.env"
  MT_WORKTREE_COPY_FILES=(".env")
  run copy_runtime_files "$SRC" "$DST"
  [ "$status" -eq 0 ]
  [ -f "$DST/.env" ]
  [ "$(cat "$DST/.env")" = "FOO=bar" ]
  [[ "$output" == *"copied .env"* ]]
}

@test "missing source is silent" {
  MT_WORKTREE_COPY_FILES=(".env" ".envrc")
  run copy_runtime_files "$SRC" "$DST"
  [ "$status" -eq 0 ]
  [ ! -e "$DST/.env" ]
  [ ! -e "$DST/.envrc" ]
  [ -z "$output" ]
}

@test "symlink is followed and resolved" {
  printf 'REAL=1\n' > "$TMP/real_env"
  ln -s "$TMP/real_env" "$SRC/.env"
  MT_WORKTREE_COPY_FILES=(".env")
  run copy_runtime_files "$SRC" "$DST"
  [ "$status" -eq 0 ]
  [ -f "$DST/.env" ]
  # cp -L produces a regular file, not a symlink.
  [ ! -L "$DST/.env" ]
  [ "$(cat "$DST/.env")" = "REAL=1" ]
}

@test "git-crypt encrypted source is skipped" {
  printf '\x00GITCRYPT\x00rest of file' > "$SRC/.env"
  MT_WORKTREE_COPY_FILES=(".env")
  run copy_runtime_files "$SRC" "$DST"
  [ "$status" -eq 0 ]
  [ ! -e "$DST/.env" ]
  [[ "$output" == *"encrypted"* ]]
}

@test "existing destination is preserved" {
  printf 'OLD\n' > "$DST/.env"
  printf 'NEW\n' > "$SRC/.env"
  MT_WORKTREE_COPY_FILES=(".env")
  run copy_runtime_files "$SRC" "$DST"
  [ "$status" -eq 0 ]
  [ "$(cat "$DST/.env")" = "OLD" ]
}

# --- validate_worktree_copy_files ---

@test "validation rejects absolute path" {
  MT_WORKTREE_COPY_FILES=("/etc/passwd")
  run validate_worktree_copy_files
  [ "$status" -ne 0 ]
  [[ "$output" == *"absolute paths not allowed"* ]]
}

@test "validation rejects subdir" {
  MT_WORKTREE_COPY_FILES=("config/local.env")
  run validate_worktree_copy_files
  [ "$status" -ne 0 ]
  [[ "$output" == *"subdirectory"* ]]
}

@test "validation rejects empty string" {
  MT_WORKTREE_COPY_FILES=("")
  run validate_worktree_copy_files
  [ "$status" -ne 0 ]
  [[ "$output" == *"empty"* ]]
}

@test "validation rejects parent-directory references" {
  MT_WORKTREE_COPY_FILES=("..env")
  run validate_worktree_copy_files
  [ "$status" -ne 0 ]
  [[ "$output" == *"parent-directory"* ]]
}

@test "dedup keeps first occurrence" {
  MT_WORKTREE_COPY_FILES=(".env" ".env" ".env")
  validate_worktree_copy_files
  [ "${#MT_WORKTREE_COPY_FILES[@]}" -eq 1 ]
  [ "${MT_WORKTREE_COPY_FILES[0]}" = ".env" ]
}

@test "dedup preserves first-occurrence order across distinct entries" {
  MT_WORKTREE_COPY_FILES=(".npmrc" ".env" ".npmrc" ".envrc" ".env")
  validate_worktree_copy_files
  [ "${#MT_WORKTREE_COPY_FILES[@]}" -eq 3 ]
  [ "${MT_WORKTREE_COPY_FILES[0]}" = ".npmrc" ]
  [ "${MT_WORKTREE_COPY_FILES[1]}" = ".env" ]
  [ "${MT_WORKTREE_COPY_FILES[2]}" = ".envrc" ]
}
