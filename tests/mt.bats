#!/usr/bin/env bats
# unit tests for mt.sh pure functions.
#
# install bats:  brew install bats-core
# run tests:     bats tests/mt.bats

setup() {
  MT_DIR="$(cd "$(dirname "$BATS_TEST_FILENAME")/.." && pwd)"
  # source mt.sh in a way that lets us call its functions without invoking main
  # trick: replace `main "$@"` with no-op by setting a guard, then source.
  MT_NO_MAIN=1 source <(sed 's|^main "\$@"$|true|' "$MT_DIR/mt.sh")
}

# --- slugify ---

@test "slugify: lowercase passthrough" {
  run slugify "fix-cookie-expiry"
  [ "$status" -eq 0 ]
  [ "$output" = "fix-cookie-expiry" ]
}

@test "slugify: uppercase to lowercase" {
  run slugify "Fix-COOKIE-Expiry"
  [ "$output" = "fix-cookie-expiry" ]
}

@test "slugify: spaces become dashes" {
  run slugify "fix the bug"
  [ "$output" = "fix-the-bug" ]
}

@test "slugify: special chars stripped" {
  run slugify "fix!cookie@expiry#5"
  [ "$output" = "fix-cookie-expiry-5" ]
}

@test "slugify: collapses runs of dashes" {
  run slugify "fix---cookie"
  [ "$output" = "fix-cookie" ]
}

@test "slugify: strips leading/trailing dashes" {
  run slugify "---fix-cookie---"
  [ "$output" = "fix-cookie" ]
}

@test "slugify: empty input → empty output" {
  run slugify ""
  [ "$output" = "" ]
}

@test "slugify: only special chars → empty" {
  run slugify "!@#\$%"
  [ "$output" = "" ]
}

# --- pane_title ---

@test "pane_title: repo basename + branch" {
  run pane_title "/Users/test/Code/acme-api" "fix-cookie"
  [ "$output" = "acme-api:fix-cookie" ]
}

@test "pane_title: trailing slash on repo path" {
  run pane_title "/Users/test/Code/acme-api/" "fix-cookie"
  [ "$output" = "acme-api:fix-cookie" ]
}

# --- expand_tilde ---

@test "expand_tilde: leading ~ expands to HOME" {
  run expand_tilde "~/Code"
  [ "$output" = "$HOME/Code" ]
}

@test "expand_tilde: no tilde, passthrough" {
  run expand_tilde "/abs/path"
  [ "$output" = "/abs/path" ]
}

@test "expand_tilde: tilde mid-string, no expansion" {
  run expand_tilde "/foo/~/bar"
  [ "$output" = "/foo/~/bar" ]
}

# --- load_config ---

@test "load_config: missing file is OK (defaults preserved)" {
  MT_CONFIG="/tmp/mt-nonexistent-$$.toml"
  load_config
  [ "$MT_TMUX_SESSION" = "mt" ]
  [ "$MT_DEFAULT_BACKEND" = "claude" ]
}

@test "load_config: empty file is OK" {
  MT_CONFIG="$(mktemp)"
  : > "$MT_CONFIG"
  load_config
  [ "$MT_TMUX_SESSION" = "mt" ]
  rm -f "$MT_CONFIG"
}

@test "load_config: parses string values" {
  MT_CONFIG="$(mktemp)"
  cat >"$MT_CONFIG" <<'EOF'
tmux_session = "myproj"
branch_prefix = "feat"
default_backend = "ollama"
ollama_model = "mistral:7b"
EOF
  load_config
  [ "$MT_TMUX_SESSION" = "myproj" ]
  [ "$MT_BRANCH_PREFIX" = "feat" ]
  [ "$MT_DEFAULT_BACKEND" = "ollama" ]
  [ "$MT_OLLAMA_MODEL" = "mistral:7b" ]
  rm -f "$MT_CONFIG"
}

@test "load_config: parses array values" {
  MT_CONFIG="$(mktemp)"
  cat >"$MT_CONFIG" <<'EOF'
repos_dirs = ["~/code", "~/work"]
EOF
  load_config
  [ "${#MT_REPOS_DIRS[@]}" -eq 2 ]
  [ "${MT_REPOS_DIRS[0]}" = "~/code" ]
  [ "${MT_REPOS_DIRS[1]}" = "~/work" ]
  rm -f "$MT_CONFIG"
}

@test "load_config: explicit repos overrides repos_dirs" {
  MT_CONFIG="$(mktemp)"
  cat >"$MT_CONFIG" <<'EOF'
repos = ["~/Code/a", "~/Code/b", "~/Code/c"]
EOF
  load_config
  [ "${#MT_REPOS[@]}" -eq 3 ]
  [ "${MT_REPOS[1]}" = "~/Code/b" ]
  rm -f "$MT_CONFIG"
}

@test "load_config: comments and blank lines ignored" {
  MT_CONFIG="$(mktemp)"
  cat >"$MT_CONFIG" <<'EOF'
# this is a comment
tmux_session = "kept"

# another comment
branch_prefix = "kept"
EOF
  load_config
  [ "$MT_TMUX_SESSION" = "kept" ]
  [ "$MT_BRANCH_PREFIX" = "kept" ]
  rm -f "$MT_CONFIG"
}

@test "load_config: unknown keys silently ignored" {
  MT_CONFIG="$(mktemp)"
  cat >"$MT_CONFIG" <<'EOF'
tmux_session = "kept"
not_a_real_key = "should not crash"
EOF
  run load_config
  [ "$status" -eq 0 ]
  [ "$MT_TMUX_SESSION" = "kept" ]
  rm -f "$MT_CONFIG"
}
