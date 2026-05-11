#!/usr/bin/env bash
# mt — tmux-native control plane for Claude Code and Ollama across worktrees
# https://github.com/jinyuanlu/metatree
#
# *** FROZEN ***
# This is the bash prototype. Bug fixes and new features go to the Go
# implementation (see spec-go.md). Install the Go binary via:
#   curl -fsSL https://raw.githubusercontent.com/jinyuanlu/metatree/master/install.sh | bash
#
# This file is preserved as a reference until v2.0.0, when it will be removed.
#
# spec: see spec.md (auth invariant §1.4 — mt does not touch ~/.claude/.credentials.json)

set -euo pipefail

# ---------------------------------------------------------------------------
# defaults — overridden by ~/.config/mt/config.toml
# ---------------------------------------------------------------------------
MT_CONFIG="${MT_CONFIG:-$HOME/.config/mt/config.toml}"
MT_TMUX_SESSION="mt"
MT_TMUX_WINDOW="dashboard"
MT_BRANCH_PREFIX="mt"
MT_WORKTREE_SUBDIR=".worktrees"
# Start-point for new worktree branches. One of:
#   "head"           — parent repo's current HEAD (legacy)
#   "origin-default" — latest fetched origin/<default-branch> (recommended)
#   "<literal-ref>"  — any git ref (e.g. "main", "v1.2.3", a sha)
# Overridable per-invocation via $MT_BASE. Validated at config-load time.
MT_WORKTREE_BASE="origin-default"
# Timeout (seconds) for `git fetch origin <branch>` during base resolution.
MT_WORKTREE_FETCH_TIMEOUT="10"
MT_DEFAULT_BACKEND="claude"
MT_OLLAMA_MODEL="llama3:8b"
MT_CLAUDE_CMD="claude"
MT_OLLAMA_CMD="ollama run {model}"
MT_REPOS_DIRS=("$HOME/Code")
MT_REPOS=()
# Gitignored runtime files to copy from the parent repo into a fresh worktree
# so the worktree can actually run. Single-component names only (no
# subdirectories, no absolute paths) — validated at config-load time.
MT_WORKTREE_COPY_FILES=(".env" ".envrc" ".npmrc")
# When a new worktree contains an .envrc and direnv is on PATH, run `direnv
# allow` so the agent's pane doesn't see the "blocked .envrc" warning.
# Set to "false" if you point mt at freshly-cloned third-party repos.
MT_AUTO_DIRENV_ALLOW="true"
# When creating/touching the dashboard, mt sets useful per-pane border
# format and status-right format on its tmux session/window. Set to "false"
# if you'd rather keep your own tmux status line untouched.
MT_AUTO_STATUS_CHROME="true"

# ---------------------------------------------------------------------------
# helpers
# ---------------------------------------------------------------------------
die() { printf 'mt: %s\n' "$*" >&2; exit 1; }

slugify() {
  printf '%s' "$1" | tr '[:upper:]' '[:lower:]' \
    | sed -E 's/[^a-z0-9-]+/-/g; s/-+/-/g; s/^-//; s/-$//'
}

expand_tilde() { printf '%s' "${1/#\~/$HOME}"; }

# log location — XDG state dir, override with $MT_LOG. One line per invocation.
# Useful for debugging tmux-popup invocations whose stdout/stderr you can't see.
#   tail -f ~/.local/state/mt/mt.log
MT_LOG="${MT_LOG:-${XDG_STATE_HOME:-$HOME/.local/state}/mt/mt.log}"

mt_log() {
  mkdir -p "$(dirname "$MT_LOG")" 2>/dev/null || return 0
  printf '[%s] pid=%d %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$$" "$*" \
    >> "$MT_LOG" 2>/dev/null || true
}

# git-crypt encrypted files start with "\x00GITCRYPT\x00" (10 bytes).
# Detecting this lets us skip auto-direnv-allow on still-encrypted files,
# which would otherwise authorize direnv to source binary garbage.
is_git_crypted() {
  [[ -f "$1" ]] || return 1
  local sig
  sig=$(head -c 10 "$1" 2>/dev/null | xxd -p 2>/dev/null)
  [[ "$sig" == "00474954435259505400"* ]]
}

# Resolve the absolute path of this script (for tmux bindings to call mt
# without relying on PATH). Portable across macOS (no readlink -f).
mt_self_path() {
  local src="${BASH_SOURCE[0]}"
  while [[ -L "$src" ]]; do
    local dir; dir=$(cd -P "$(dirname "$src")" >/dev/null 2>&1 && pwd)
    src=$(readlink "$src")
    [[ "$src" != /* ]] && src="$dir/$src"
  done
  printf '%s/%s\n' "$(cd -P "$(dirname "$src")" >/dev/null 2>&1 && pwd)" "$(basename "$src")"
}

# minimal TOML reader: handles `key = "string"` and `key = ["a", "b"]`
load_config() {
  [[ -f "$MT_CONFIG" ]] || return 0
  local line key value items
  while IFS= read -r line || [[ -n "$line" ]]; do
    [[ "$line" =~ ^[[:space:]]*# ]] && continue
    [[ -z "${line// }" ]] && continue
    [[ "$line" =~ ^[[:space:]]*([a-z_]+)[[:space:]]*=[[:space:]]*(.*)$ ]] || continue
    key="${BASH_REMATCH[1]}"
    value="${BASH_REMATCH[2]}"
    if [[ "$value" =~ ^\[ ]]; then
      items=()
      while [[ "$value" =~ \"([^\"]+)\" ]]; do
        items+=("${BASH_REMATCH[1]}")
        value="${value/${BASH_REMATCH[0]}/}"
      done
      case "$key" in
        repos_dirs)          MT_REPOS_DIRS=("${items[@]}");;
        repos)               MT_REPOS=("${items[@]}");;
        worktree_copy_files) MT_WORKTREE_COPY_FILES=("${items[@]}");;
      esac
    else
      value="${value#\"}"; value="${value%\"}"
      case "$key" in
        tmux_session)    MT_TMUX_SESSION="$value";;
        tmux_window)     MT_TMUX_WINDOW="$value";;
        branch_prefix)   MT_BRANCH_PREFIX="$value";;
        worktree_subdir) MT_WORKTREE_SUBDIR="$value";;
        default_backend) MT_DEFAULT_BACKEND="$value";;
        ollama_model)    MT_OLLAMA_MODEL="$value";;
        claude_cmd)      MT_CLAUDE_CMD="$value";;
        ollama_cmd)      MT_OLLAMA_CMD="$value";;
        auto_direnv_allow) MT_AUTO_DIRENV_ALLOW="$value";;
        auto_status_chrome) MT_AUTO_STATUS_CHROME="$value";;
        worktree_base)   MT_WORKTREE_BASE="$value";;
      esac
    fi
  done < "$MT_CONFIG"
}

# ---------------------------------------------------------------------------
# repo discovery
# ---------------------------------------------------------------------------
discover_repos() {
  if [[ ${#MT_REPOS[@]} -gt 0 ]]; then
    local r
    for r in "${MT_REPOS[@]}"; do printf '%s\n' "$(expand_tilde "$r")"; done
    return
  fi
  local d
  for d in "${MT_REPOS_DIRS[@]}"; do
    d=$(expand_tilde "$d")
    [[ -d "$d" ]] || continue
    find "$d" -maxdepth 4 -name .git -type d 2>/dev/null \
      | while read -r git_dir; do dirname "$git_dir"; done
  done | sort -u
}

# Validate MT_WORKTREE_COPY_FILES per spec.md: single-component names only.
# Dedupes in place, preserving first-occurrence order. Die on any invalid
# entry — same rules as internal/config/config.go:validateWorktreeCopyFiles.
validate_worktree_copy_files() {
  [[ ${#MT_WORKTREE_COPY_FILES[@]} -gt 0 ]] || return 0
  local name trimmed seen=() out=() already
  for name in "${MT_WORKTREE_COPY_FILES[@]}"; do
    # Trim leading/trailing whitespace.
    trimmed="${name#"${name%%[![:space:]]*}"}"
    trimmed="${trimmed%"${trimmed##*[![:space:]]}"}"
    [[ -n "$trimmed" ]] \
      || die "invalid worktree_copy_files entry: must not be empty or whitespace"
    [[ "$trimmed" == *".."* ]] \
      && die "invalid worktree_copy_files entry \"$trimmed\": parent-directory references not allowed"
    [[ "$trimmed" == /* ]] \
      && die "invalid worktree_copy_files entry \"$trimmed\": absolute paths not allowed"
    [[ "$trimmed" == */* ]] \
      && die "invalid worktree_copy_files entry \"$trimmed\": subdirectory paths not supported in v1, use a single filename"
    already=0
    local s
    for s in "${seen[@]:-}"; do
      [[ "$s" == "$trimmed" ]] && { already=1; break; }
    done
    [[ $already -eq 1 ]] && continue
    seen+=("$trimmed")
    out+=("$trimmed")
  done
  MT_WORKTREE_COPY_FILES=("${out[@]}")
}

# Validate MT_WORKTREE_BASE: must not be empty or whitespace-only after trim.
# Accepts any other string — git itself reports unresolved refs at use time.
# Mirrors internal/config/config.go:validateWorktreeBase.
validate_worktree_base() {
  local trimmed="${MT_WORKTREE_BASE#"${MT_WORKTREE_BASE%%[![:space:]]*}"}"
  trimmed="${trimmed%"${trimmed##*[![:space:]]}"}"
  [[ -n "$trimmed" ]] \
    || die "invalid worktree_base: must not be empty (use 'head' to disable, 'origin-default' for the default, or a literal git ref)"
  MT_WORKTREE_BASE="$trimmed"
}

# Run command with a SIGTERM-then-SIGKILL timeout (no `timeout` cmd dep —
# macOS doesn't ship one). Returns the command's exit code, or 124 on
# timeout (matches GNU timeout convention). Pure bash 3.2 compatible.
_mt_with_timeout() {
  local secs="$1"; shift
  "$@" &
  local pid=$!
  ( sleep "$secs" && kill -TERM "$pid" 2>/dev/null
    sleep 1 && kill -KILL "$pid" 2>/dev/null ) &
  local killer=$!
  # `wait` without -n (3.2 has no -n); we wait on the specific pid.
  wait "$pid" 2>/dev/null
  local rc=$?
  # Reap the killer; it'll exit on its own once pid is gone.
  kill -KILL "$killer" 2>/dev/null
  wait "$killer" 2>/dev/null
  # If the cmd died by SIGTERM (rc 143) or SIGKILL (137), treat as timeout.
  if [ "$rc" -eq 143 ] || [ "$rc" -eq 137 ]; then return 124; fi
  return "$rc"
}

# Echoes the default branch name on stdout, returns non-zero on failure.
# Tries: git symbolic-ref → main → master → develop → trunk.
# Return codes: 0 ok, 2 no origin remote, 1 origin exists but no candidate.
default_branch() {
  local repo="$1" ref name
  # Primary: symbolic-ref of origin/HEAD (set by `git remote set-head origin -a`).
  ref=$(git -C "$repo" symbolic-ref --quiet --short refs/remotes/origin/HEAD 2>/dev/null)
  if [ -n "$ref" ]; then
    printf '%s' "${ref#origin/}"
    return 0
  fi
  if ! git -C "$repo" remote get-url origin >/dev/null 2>&1; then
    return 2
  fi
  for name in main master develop trunk; do
    if git -C "$repo" show-ref --verify --quiet "refs/remotes/origin/$name"; then
      printf '%s' "$name"
      return 0
    fi
  done
  return 1
}

# Runs `git fetch origin <branch>` with the given timeout (default 10s).
# GIT_TERMINAL_PROMPT=0 so credential prompts fail fast instead of hanging
# invisibly inside a tmux popup. Returns 124 on timeout.
fetch_branch() {
  local repo="$1" branch="$2" timeout="${3:-10}"
  GIT_TERMINAL_PROMPT=0 _mt_with_timeout "$timeout" \
    git -C "$repo" fetch origin "$branch" >/dev/null 2>&1
}

# Echoes the short (8-char) sha of REF in REPO on stdout, or returns
# non-zero if the ref doesn't resolve. Silent on stderr.
_mt_rev_parse_short() {
  local repo="$1" ref="$2"
  git -C "$repo" rev-parse --short=8 "$ref" 2>/dev/null
}

# resolve_worktree_base REPO CFG_VALUE BRANCH_NAME
# Mirrors internal/gitio/gitio.go:ResolveWorktreeBase + ResolveResult.Format.
# On success: prints "<ref>\t<summary>" to stdout, returns 0. The caller
# splits on TAB; <ref> is empty when the caller should omit the start-point
# arg (HEAD-mode); <summary> is the human-facing "mt: branched ..." line.
# On hard error (e.g. origin-default but no origin): prints the remediation
# message to stderr and returns non-zero.
resolve_worktree_base() {
  local repo="$1" cfg="$2" branch_name="$3"
  local sha def origin_ref
  case "$cfg" in
    ""|head)
      sha=$(_mt_rev_parse_short "$repo" HEAD) \
        || { printf 'mt: cannot resolve HEAD in %s\n' "$repo" >&2; return 1; }
      printf '\tmt: branched %s from HEAD@%s' "$branch_name" "$sha"
      return 0
      ;;
    origin-default)
      def=$(default_branch "$repo")
      local rc=$?
      if [ $rc -ne 0 ]; then
        if [ $rc -eq 2 ]; then
          printf 'mt: cannot resolve worktree base "origin-default" — repo %s has no "origin" remote.\n' "$repo" >&2
          printf '    fix: either add one (git remote add origin <url>)\n' >&2
          printf '    or set worktree_base = "head" in ~/.metatree/config.toml\n' >&2
        else
          printf 'mt: cannot resolve worktree base "origin-default" — no default branch found on origin (tried main/master/develop/trunk)\n' >&2
        fi
        return 1
      fi
      origin_ref="origin/$def"
      # Try fetch first.
      if fetch_branch "$repo" "$def" "${MT_WORKTREE_FETCH_TIMEOUT:-10}"; then
        sha=$(_mt_rev_parse_short "$repo" "$origin_ref")
        if [ -n "$sha" ]; then
          printf '%s\tmt: branched %s from %s@%s' "$origin_ref" "$branch_name" "$origin_ref" "$sha"
          return 0
        fi
      fi
      # Fetch failed or ref still missing; walk the fallback ladder.
      sha=$(_mt_rev_parse_short "$repo" "$origin_ref")
      if [ -n "$sha" ]; then
        printf '%s\tmt: branched %s from %s@%s (stale: fetch failed)' "$origin_ref" "$branch_name" "$origin_ref" "$sha"
        return 0
      fi
      sha=$(_mt_rev_parse_short "$repo" "$def")
      if [ -n "$sha" ]; then
        printf '%s\tmt: branched %s from local %s@%s (offline, no %s)' "$def" "$branch_name" "$def" "$sha" "$origin_ref"
        return 0
      fi
      sha=$(_mt_rev_parse_short "$repo" HEAD)
      printf '\tmt: branched %s from HEAD@%s (last-resort fallback)' "$branch_name" "$sha"
      return 0
      ;;
    *)
      sha=$(_mt_rev_parse_short "$repo" "$cfg")
      if [ -n "$sha" ]; then
        printf '%s\tmt: branched %s from %s@%s' "$cfg" "$branch_name" "$cfg" "$sha"
      else
        # Unknown ref: still pass through to `git worktree add` and let
        # git itself produce the failure message (matches Go behavior).
        printf '%s\tmt: branched %s from %s' "$cfg" "$branch_name" "$cfg"
      fi
      return 0
      ;;
  esac
}

# Copies entries in MT_WORKTREE_COPY_FILES from $1 (parent repo) to
# $2 (worktree path). Follows symlinks (cp -Lp), skips git-crypt
# encrypted sources, skips when dst exists. Atomic via tempfile +
# mv. Per-file errors are non-fatal; emits a single stderr summary.
copy_runtime_files() {
  local src="$1" dst="$2"
  local name srcPath dstPath tmp
  local -a copied=() skipped_pairs=() errors_pairs=()

  for name in "${MT_WORKTREE_COPY_FILES[@]}"; do
    srcPath="$src/$name"
    # -e is false for a broken symlink; -L is true. Treat both as missing
    # when neither holds, and as missing when -L but -e is false (dangling).
    if [[ ! -e "$srcPath" && ! -L "$srcPath" ]]; then
      skipped_pairs+=("$name=missing"); continue
    fi
    if [[ -L "$srcPath" && ! -e "$srcPath" ]]; then
      skipped_pairs+=("$name=missing"); continue
    fi
    # -f follows symlinks, so this catches "symlink to a dir/socket/etc.".
    if [[ ! -f "$srcPath" ]]; then
      skipped_pairs+=("$name=not_file"); continue
    fi
    if is_git_crypted "$srcPath"; then
      skipped_pairs+=("$name=encrypted"); continue
    fi
    dstPath="$dst/$name"
    if [[ -e "$dstPath" || -L "$dstPath" ]]; then
      skipped_pairs+=("$name=dst_exists"); continue
    fi
    tmp=$(mktemp "$dst/.mtcopy.XXXXXX" 2>/dev/null) || {
      errors_pairs+=("$name=mktemp_failed"); continue
    }
    # cp -L follows symlinks; -p preserves mode bits (portable on macOS/Linux).
    if ! cp -Lp "$srcPath" "$tmp" 2>/dev/null; then
      rm -f "$tmp"
      errors_pairs+=("$name=cp_failed"); continue
    fi
    if ! mv "$tmp" "$dstPath" 2>/dev/null; then
      rm -f "$tmp"
      errors_pairs+=("$name=rename_failed"); continue
    fi
    copied+=("$name")
  done

  # ---- summary: mirror gitio.CopyReport.Summary() ----
  local -a non_missing=()
  local p n r
  for p in "${skipped_pairs[@]:-}"; do
    [[ -z "$p" ]] && continue
    n="${p%%=*}"; r="${p#*=}"
    [[ "$r" == "missing" ]] && continue
    non_missing+=("$n $r")
  done

  local has_copied=0 has_err=0
  [[ ${#copied[@]} -gt 0 ]] && has_copied=1
  [[ ${#errors_pairs[@]} -gt 0 ]] && has_err=1
  if [[ $has_copied -eq 0 && $has_err -eq 0 && ${#non_missing[@]} -eq 0 ]]; then
    return 0
  fi

  local first second=""
  if [[ $has_copied -eq 1 ]]; then
    first="mt: copied $(_mt_join ', ' "${copied[@]}")"
    if [[ ${#non_missing[@]} -gt 0 ]]; then
      first+=" (skipped: $(_mt_join ', ' "${non_missing[@]}"))"
    fi
  elif [[ ${#non_missing[@]} -gt 0 ]]; then
    local -a paren=()
    for n in "${non_missing[@]}"; do
      paren+=("${n% *} (${n#* })")
    done
    first="mt: copy skipped: $(_mt_join ', ' "${paren[@]}")"
  else
    first="mt: copy:"
  fi

  if [[ $has_err -eq 1 ]]; then
    local -a errparts=()
    for p in "${errors_pairs[@]}"; do
      errparts+=("${p%%=*}: ${p#*=}")
    done
    second="mt: copy errors: $(_mt_join ', ' "${errparts[@]}")"
  fi

  printf '%s\n' "$first" >&2
  [[ -n "$second" ]] && printf '%s\n' "$second" >&2
  return 0
}

# Join $2..$N with separator $1. Pure bash, handles empty input.
_mt_join() {
  local sep="$1"; shift
  [[ $# -eq 0 ]] && return 0
  local out="$1"; shift
  local x
  for x in "$@"; do out+="$sep$x"; done
  printf '%s' "$out"
}

# pane title: `repo_name:branch`. on basename collision, append -<sha8(repo_path)>.
pane_title() {
  local repo_path="$1" branch="$2" repo_name
  repo_name=$(basename "$repo_path")
  printf '%s:%s' "$repo_name" "$branch"
}

# All worktrees git knows about, across all configured repos. Catches both
# mt's convention (<repo>/.worktrees/<branch>) and Claude Code's native
# (<repo>/.claude/worktrees/<task>) and any user-created worktrees. Skips
# the main worktree (the repo itself).
#
# Output format: <resolved_repo_path>\t<worktree_path>\n
discover_worktrees() {
  local repo
  for repo in $(discover_repos); do
    local resolved
    resolved=$(cd "$repo" 2>/dev/null && pwd -P) || continue
    # `|| true` so a single repo failure (e.g. corrupted .git, permission
    # error) doesn't kill the whole listing under set -e/pipefail.
    { git -C "$repo" worktree list --porcelain 2>/dev/null || true; } \
      | awk -v r="$resolved" '
          /^worktree / { wt = $2; next }
          /^HEAD / && wt != "" {
            if (wt != r) print r "\t" wt
            wt = ""
          }
          /^$/ { wt = "" }
        ' || true
  done
}

# Find the parent repo's working tree path for any worktree. Robust across
# mt-style and claude-style worktree paths (won't break on `.claude/worktrees/`
# where `dirname; dirname` gives the wrong answer).
parent_repo_of() {
  local wt="$1" gd
  gd=$(git -C "$wt" rev-parse --git-common-dir 2>/dev/null) || return 1
  # git-common-dir is `<repo>/.git`; dirname gives the repo working dir
  if [[ "$gd" == /* ]]; then
    dirname "$gd"
  else
    # relative path — resolve against worktree's path
    (cd "$wt" && cd "$(dirname "$gd")" && pwd -P)
  fi
}

# ---------------------------------------------------------------------------
# tmux primitives
# ---------------------------------------------------------------------------
ensure_dashboard() {
  if ! tmux has-session -t="$MT_TMUX_SESSION" 2>/dev/null; then
    tmux new-session -d -s "$MT_TMUX_SESSION" -n "$MT_TMUX_WINDOW" 2>/dev/null \
      || die "tmux unavailable; start with: tmux new -d -s $MT_TMUX_SESSION"
  fi
  if ! tmux list-windows -t "$MT_TMUX_SESSION" -F '#W' 2>/dev/null | grep -qx "$MT_TMUX_WINDOW"; then
    tmux new-window -t "$MT_TMUX_SESSION:" -n "$MT_TMUX_WINDOW"
  fi
  _configure_dashboard_chrome

  # Auto-install popup bindings if they're missing on this tmux server.
  # Server restarts wipe bindings; this restores them on the next mt
  # invocation that touches a dashboard. One-line notice on first install
  # so the user knows where prefix+g came from.
  if ! _bindings_installed; then
    if _install_bindings; then
      printf 'mt: installed prefix+g/G/N/R bindings on this tmux server\n' >&2
    fi
  fi
}

# Configure the dashboard's chrome: pane border format and status line.
# Scoped to our session/window only (set-window-option / -t session) so we
# don't clobber the user's other tmux sessions. Skip when the user has
# disabled it via auto_status_chrome=false.
_configure_dashboard_chrome() {
  [[ "${MT_AUTO_STATUS_CHROME:-true}" == "true" ]] || return 0
  local target="$MT_TMUX_SESSION:$MT_TMUX_WINDOW"

  # Pane border (per-window) — show pane index + mt's marker + running command.
  # `@mt-managed` is the stable marker; falls back to '-' when missing
  # (bare shells and externally-split panes). pane_current_command shows
  # what's running so you can tell claude / ollama / shell apart at a glance.
  tmux set-window-option -t "$target" pane-border-status top 2>/dev/null || true
  tmux set-window-option -t "$target" pane-border-format \
    '#{?pane_active,#[reverse] , }#{pane_index} #{?@mt-managed,#{@mt-managed},-} (#{pane_current_command})#[default]' \
    2>/dev/null || true

  # Status-right (per-session) — active pane's mt marker, then cwd, then clock.
  # The cwd is the actual filesystem path (#{pane_current_path}), shortened
  # to keep the status line readable. Answers "which folder am I in?" at
  # all times, regardless of what the agent has done to the prompt.
  tmux set-option -t "$MT_TMUX_SESSION" status-right \
    '#[fg=cyan]#{?@mt-managed,#{@mt-managed},(no mt pane)}#[default]  ·  #{=50:pane_current_path}  ·  %H:%M' \
    2>/dev/null || true
  tmux set-option -t "$MT_TMUX_SESSION" status-right-length 120 2>/dev/null || true
  tmux set-option -t "$MT_TMUX_SESSION" status-interval 2 2>/dev/null || true
}

# Find a pane by mt's stable marker, NOT pane_title (which agents like
# Claude Code routinely overwrite via OSC 2 escape sequences with their cwd).
# The @mt-managed user option is set on every mt-created pane and can't be
# clobbered from inside the pane.
find_pane() {
  tmux list-panes -t "$MT_TMUX_SESSION:$MT_TMUX_WINDOW" \
    -F '#{pane_id}|#{@mt-managed}' 2>/dev/null \
    | awk -F'|' -v t="$1" '$2 == t {print $1; exit}'
}

# Mark a pane as mt-managed: visible pane border title (informational, may
# get overwritten by the agent) AND a stable @mt-managed user option (the
# real source of truth for mt_pane_count, find_pane, and switch listings).
mark_pane() {
  local pane_id="$1" title="$2"
  tmux select-pane -t "$pane_id" -T "$title" 2>/dev/null || true
  tmux set-option -p -t "$pane_id" '@mt-managed' "$title" 2>/dev/null || true
}

attach_dashboard() {
  if [[ -n "${TMUX:-}" ]]; then
    tmux switch-client -t "$MT_TMUX_SESSION:$MT_TMUX_WINDOW"
  else
    tmux attach -t "$MT_TMUX_SESSION:$MT_TMUX_WINDOW"
  fi
}

# ---------------------------------------------------------------------------
# commands
# ---------------------------------------------------------------------------
cmd_show() { ensure_dashboard; attach_dashboard; }

cmd_diagnose() {
  local mt; mt=$(mt_self_path 2>/dev/null || echo "$0")
  local tmux_v; tmux_v=$(tmux -V 2>&1 | head -1 || echo "tmux NOT installed")
  local fzf_v;  fzf_v=$(fzf --version 2>&1 | head -1 || echo "fzf NOT installed")
  local gc_v;   gc_v=$(git-crypt --version 2>&1 | head -1 || echo "git-crypt NOT installed (optional)")
  local de_v;   de_v=$(direnv --version 2>&1 | head -1 || echo "direnv NOT installed (optional)")

  cat <<EOF
mt diagnose — copy/paste this entire block when reporting issues.

VERSIONS
  mt path:    $mt
  tmux:       $tmux_v
  fzf:        $fzf_v
  git-crypt:  $gc_v
  direnv:     $de_v

CONFIG
  MT_CONFIG:        ${MT_CONFIG:-(unset, falls back to ~/.config/mt/config.toml)}
  exists:           $([ -f "${MT_CONFIG:-$HOME/.config/mt/config.toml}" ] && echo yes || echo no)
  tmux_session:     $MT_TMUX_SESSION
  tmux_window:      $MT_TMUX_WINDOW
  default_backend:  $MT_DEFAULT_BACKEND
  repos_dirs:       ${MT_REPOS_DIRS[*]:-}
  repos:            ${MT_REPOS[*]:-}
  worktree_copy_files: ${MT_WORKTREE_COPY_FILES[*]:-}
  worktree_base:    ${MT_WORKTREE_BASE:-}

TMUX STATE
  in_tmux:          ${TMUX:+yes (session = $(tmux display-message -p '#{session_name}' 2>/dev/null))}
  ${TMUX:-(not in tmux — running from a plain shell)}
  has-session $MT_TMUX_SESSION:  $(tmux has-session -t="$MT_TMUX_SESSION" 2>&1 && echo "yes" || echo "no")

KEYBINDINGS (look for display-popup; if empty, run 'mt bind')
$(tmux list-keys -T prefix 2>/dev/null | grep -E 'display-popup' | sed 's/^/  /' || echo "  (no display-popup bindings on this server)")

LOG
  path: $MT_LOG
  recent (last 10 lines):
$(tail -10 "$MT_LOG" 2>/dev/null | sed 's/^/    /' || echo "    (log empty or missing)")

QUICK CHECKS
  - bindings missing → run 'mt bind'
  - bindings present but prefix+g does nothing → tmux server may have restarted; re-run 'mt bind'
  - popup flashes and closes → run the binding's command directly to see the error:
      $(tmux list-keys -T prefix 2>/dev/null | grep -E '\bg\b.*display-popup' | grep -oE '"[^"]+"' | head -1 | tr -d '"')
  - 'mt switch' from a plain shell shows the actual error
EOF
}

# Install (or refresh) the four popup bindings on the running tmux server.
# Returns 0 on success, 1 if tmux call fails. Silent — no output.
_install_bindings() {
  local mt; mt=$(mt_self_path)
  tmux bind-key g display-popup -w 80% -h 60% -E "$mt switch -z" 2>/dev/null || return 1
  tmux bind-key G display-popup -w 80% -h 60% -E "$mt switch"    2>/dev/null
  tmux bind-key N display-popup -w 80% -h 60% -E "$mt new"       2>/dev/null
  tmux bind-key R display-popup -w 80% -h 60% -E "$mt rm"        2>/dev/null
}

# True iff our bindings are already installed on the running tmux server.
_bindings_installed() {
  tmux list-keys -T prefix 2>/dev/null \
    | grep -qE '^bind-key[[:space:]]+-T prefix[[:space:]]+g[[:space:]].*display-popup'
}

cmd_bind() {
  tmux has-session 2>/dev/null \
    || die "tmux server not running; start with: tmux new -d -s $MT_TMUX_SESSION"

  # Use absolute path to mt so the binding works regardless of tmux's
  # inherited PATH. The most common silent failure is `~/.local/bin` not
  # being on PATH when tmux was started; bindings using bare `mt` then
  # do nothing when invoked.
  local mt; mt=$(mt_self_path)

  _install_bindings || die "failed to install bindings (tmux 3.2+ required for display-popup)"

  cat <<EOF
mt keybindings set on the running tmux server (absolute-path form):

  prefix + g   →  $mt switch -z      ← high-frequency
  prefix + G   →  $mt switch
  prefix + N   →  $mt new
  prefix + R   →  $mt rm

Reach them from inside Claude or Ollama — tmux intercepts the prefix
before the agent sees the keystrokes. The popup overlays the screen,
runs fzf, and disappears the moment you press enter.

These bindings live on the running tmux server only. To persist across
restarts, add to ~/.tmux.conf:

  bind-key g display-popup -w 80% -h 60% -E "$mt switch -z"
  bind-key G display-popup -w 80% -h 60% -E "$mt switch"
  bind-key N display-popup -w 80% -h 60% -E "$mt new"
  bind-key R display-popup -w 80% -h 60% -E "$mt rm"

Then run:  tmux source ~/.tmux.conf

Requires tmux 3.2+ (display-popup). Verify the binding is set:
  tmux list-keys | grep -E '^bind-key.*\\bg\\b.*display-popup'
EOF
}

cmd_switch() {
  local zoom=false
  [[ "${1:-}" == "--zoom" || "${1:-}" == "-z" ]] && zoom=true

  command -v fzf >/dev/null 2>&1 || die "fzf not found; install: https://github.com/junegunn/fzf"

  # Build a unified target list:
  #   live  → title | live | pane_id      (selecting it: tmux select-pane)
  #   dead  → title | dead | worktree_path  (selecting it: revive via mt new)
  ensure_dashboard
  local live_entries
  # mt-managed live panes only. Dead worktrees deliberately excluded from
  # the picker — switch is for navigating between active work, not reviving
  # graveyards. Use `mt ls` to see all worktrees, `mt prune` to clean dead.
  live_entries=$(tmux list-panes -t "$MT_TMUX_SESSION:$MT_TMUX_WINDOW" \
    -F '#{@mt-managed}|live|#{pane_id}' 2>/dev/null \
    | grep -v '^|')

  # "+ Create new worktree" — keeps the popup non-empty even when there
  # are no live panes yet, and gives one-keystroke access to mt new from
  # inside an agent.
  local create_entry="+ Create new worktree...|new|<NEW>"

  local entries
  entries=$(printf '%s\n%s\n' "$live_entries" "$create_entry" | grep -v '^$' || true)

  local choice
  choice=$(printf '%s\n' "$entries" \
    | awk -F'|' '{
        if ($2 == "live")    printf "%-40s  [live]  %s\n", $1, $3
        else if ($2 == "new") printf "%-40s  [new ]  %s\n", $1, $3
      }' \
    | fzf --prompt="switch> " --height=40% --with-nth=1,2 --delimiter='[[:space:]]+') \
    || exit 1

  # last whitespace-delimited token is either pane_id (live) or the <NEW> sentinel.
  local key marker
  key=$(printf '%s' "$choice" | awk '{print $NF}')
  marker=$(printf '%s' "$choice" | awk '{print $(NF-1)}')

  case "$marker" in
    '[live]')
      tmux select-pane -t "$key"
      $zoom && tmux resize-pane -t "$key" -Z
      attach_dashboard
      ;;
    '[new'*)
      # "+ Create new worktree" — fall through to interactive cmd_new
      cmd_new --with "$MT_DEFAULT_BACKEND"
      ;;
    *)
      die "unrecognized switch entry marker: $marker"
      ;;
  esac
}

cmd_prune() {
  local force=""
  [[ "${1:-}" == "--force" ]] && force="--force"

  local dead
  dead=$(cmd_ls 2>/dev/null | awk '$NF == "dead"')
  [[ -n "$dead" ]] || die "no dead worktrees to prune"

  echo "Dead worktrees (no live pane on $MT_TMUX_SESSION:$MT_TMUX_WINDOW):"
  echo "$dead" | awk '{ printf "  %-40s  %s\n", $1, $2 }'
  echo ""
  if [[ -z "$force" ]]; then
    printf "Remove all of these (worktree + branch)? [y/N] " >&2
    local ans; read -r ans
    [[ "$ans" =~ ^[yY]$ ]] || die "aborted"
  fi

  local removed=0 skipped=0
  while IFS= read -r line; do
    [[ -z "$line" ]] && continue
    local title path repo branch full_branch try_branch
    title=$(printf '%s' "$line" | awk '{print $1}')
    path=$(printf '%s' "$line" | awk '{print $2}')
    repo=$(parent_repo_of "$path") || { printf "  skipped:  %s  (no parent repo)\n" "$title"; skipped=$((skipped+1)); continue; }
    branch=$(basename "$path")
    full_branch="$MT_BRANCH_PREFIX/$branch"

    if git -C "$repo" worktree remove $force "$path" >/dev/null 2>&1; then
      for try_branch in "$full_branch" "$branch"; do
        if git -C "$repo" rev-parse --verify "$try_branch" >/dev/null 2>&1; then
          if ! git -C "$repo" rev-parse "$try_branch@{upstream}" >/dev/null 2>&1; then
            git -C "$repo" branch -D "$try_branch" >/dev/null 2>&1 || true
          fi
          break
        fi
      done
      printf "  removed:  %s\n" "$title"
      removed=$((removed + 1))
    else
      printf "  skipped:  %s  (dirty — use 'mt prune --force' to bypass)\n" "$title"
      skipped=$((skipped + 1))
    fi
  done <<< "$dead"

  printf "\n%d removed, %d skipped\n" "$removed" "$skipped"
}

cmd_new() {
  local backend="$MT_DEFAULT_BACKEND"
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --with) backend="${2:-}"; shift 2;;
      *) shift;;
    esac
  done
  command -v fzf >/dev/null 2>&1 || die "fzf not found; install: https://github.com/junegunn/fzf"

  local repos
  repos=$(discover_repos)
  [[ -n "$repos" ]] || die "no repos found; configure repos_dirs in $MT_CONFIG"

  local repo
  if [[ -n "${MT_REPO:-}" ]]; then
    # Path may have come from `parent_repo_of` (resolved through symlinks,
    # e.g. /private/tmp/...) while discover_repos returns config-form paths
    # (/tmp/...). Compare resolved forms to match across symlink layers.
    local mt_repo_resolved candidate
    mt_repo_resolved=$(cd "$MT_REPO" 2>/dev/null && pwd -P) || die "MT_REPO not accessible: $MT_REPO"
    while IFS= read -r candidate; do
      [[ -z "$candidate" ]] && continue
      local c_resolved
      c_resolved=$(cd "$candidate" 2>/dev/null && pwd -P) || continue
      if [[ "$c_resolved" == "$mt_repo_resolved" ]]; then
        repo="$candidate"
        break
      fi
    done <<< "$repos"
    [[ -n "${repo:-}" ]] || die "MT_REPO not in discovered repos: $MT_REPO"
  else
    repo=$(printf '%s' "$repos" | fzf --prompt="repo> " --height=40%) || exit 1
  fi

  local raw_branch
  if [[ -n "${MT_BRANCH:-}" ]]; then
    raw_branch="$MT_BRANCH"
  else
    printf 'branch name: ' >&2
    read -r raw_branch
  fi
  local branch
  branch=$(slugify "$raw_branch")
  [[ -n "$branch" ]] || die "invalid branch name: $raw_branch"

  local full_branch="$MT_BRANCH_PREFIX/$branch"
  local worktree_path="$repo/$MT_WORKTREE_SUBDIR/$branch"
  local title; title=$(pane_title "$repo" "$branch")

  ensure_dashboard

  local existing; existing=$(find_pane "$title")
  if [[ -n "$existing" ]]; then
    tmux select-pane -t "$existing"
    attach_dashboard
    return
  fi

  if [[ -d "$worktree_path" ]]; then
    # macOS /tmp resolves through a symlink to /private/tmp; git stores
    # worktree paths in their canonical (resolved) form. Resolve both
    # sides before comparing so revive-on-existing-worktree works cleanly.
    local resolved; resolved=$(cd "$worktree_path" && pwd -P)
    git -C "$repo" worktree list --porcelain \
      | awk '/^worktree / {print $2}' \
      | grep -qFx "$resolved" \
      || die "path exists: $worktree_path"
  else
    # Resolve start-point from worktree_base config (or MT_BASE env override).
    # The helper prints "<ref>\t<summary>" on stdout; empty <ref> means
    # "use parent HEAD" (omit the start-point arg).
    local effective_base="${MT_BASE:-$MT_WORKTREE_BASE}"
    local resolved start_point summary
    resolved=$(resolve_worktree_base "$repo" "$effective_base" "$full_branch") \
      || exit $?
    start_point="${resolved%%	*}"
    summary="${resolved#*	}"
    printf '%s\n' "$summary" >&2

    # If the parent repo uses git-crypt, plain `git worktree add` fails: the
    # smudge filter runs in the new worktree's context where GIT_DIR points
    # at <parent>/.git/worktrees/<name>, but git-crypt looks for its key at
    # $GIT_DIR/git-crypt/keys/default — which doesn't exist (the key lives
    # at the parent's <parent>/.git/git-crypt/keys/default). So we use the
    # --no-checkout pattern, copy the key into the worktree's per-worktree
    # git dir, then check out files (smudge now finds the key, decrypts).
    if [[ -f "$repo/.git/git-crypt/keys/default" ]] \
       && command -v git-crypt >/dev/null 2>&1; then
      if [[ -n "$start_point" ]]; then
        git -C "$repo" worktree add --no-checkout -b "$full_branch" "$worktree_path" "$start_point" \
          || exit $?
      else
        git -C "$repo" worktree add --no-checkout -b "$full_branch" "$worktree_path" \
          || exit $?
      fi
      local wt_name wt_git_dir parent_key
      wt_name=$(basename "$worktree_path")
      wt_git_dir="$repo/.git/worktrees/$wt_name"
      parent_key="$repo/.git/git-crypt/keys/default"
      if [[ -d "$wt_git_dir" && -f "$parent_key" ]]; then
        mkdir -p "$wt_git_dir/git-crypt/keys"
        cp "$parent_key" "$wt_git_dir/git-crypt/keys/default"
      fi
      git -C "$worktree_path" checkout HEAD -- . >/dev/null 2>&1 || true
    else
      if [[ -n "$start_point" ]]; then
        git -C "$repo" worktree add -b "$full_branch" "$worktree_path" "$start_point" || exit $?
      else
        git -C "$repo" worktree add -b "$full_branch" "$worktree_path" || exit $?
      fi
    fi

    # Auto-copy gitignored runtime files so the new worktree can run.
    # Non-fatal; the agent launch continues regardless of copy result.
    if [[ ${#MT_WORKTREE_COPY_FILES[@]} -gt 0 ]]; then
      copy_runtime_files "$repo" "$worktree_path"
    fi

    # Pre-approve direnv so the agent's pane doesn't see "blocked .envrc".
    # Skipped when: feature disabled, no .envrc, .envrc still encrypted
    # (git-crypt unlock failed or absent), direnv missing, or call fails.
    if [[ "$MT_AUTO_DIRENV_ALLOW" == "true" ]] \
       && [[ -f "$worktree_path/.envrc" ]] \
       && ! is_git_crypted "$worktree_path/.envrc" \
       && command -v direnv >/dev/null 2>&1; then
      direnv allow "$worktree_path" >/dev/null 2>&1 || true
    fi
  fi

  local cmd
  case "$backend" in
    claude) cmd="$MT_CLAUDE_CMD";;
    ollama) cmd="${MT_OLLAMA_CMD//\{model\}/$MT_OLLAMA_MODEL}";;
    *)      die "unknown backend: $backend (use claude or ollama)";;
  esac
  command -v "${cmd%% *}" >/dev/null 2>&1 || case "$backend" in
    claude) die "claude not found; install: https://claude.com/claude-code";;
    ollama) die "ollama not found; install: https://ollama.com";;
  esac

  # Reuse the bare-shell pane if this is the first mt-managed session in the
  # dashboard. Detection uses the @mt-managed tmux user option, NOT pane_title
  # (agents like Claude Code overwrite pane_title via OSC 2 escape sequences
  # with their cwd; @mt-managed is set by mt and can't be clobbered).
  local mt_pane_count
  mt_pane_count=$(tmux list-panes -t "$MT_TMUX_SESSION:$MT_TMUX_WINDOW" \
    -F '#{@mt-managed}' 2>/dev/null \
    | grep -c '.' || true)

  if [[ "$mt_pane_count" -eq 0 ]]; then
    local first_pane
    first_pane=$(tmux list-panes -t "$MT_TMUX_SESSION:$MT_TMUX_WINDOW" -F '#{pane_id}' | head -1)
    tmux send-keys -t "$first_pane" "cd $worktree_path && exec $cmd" Enter
    mark_pane "$first_pane" "$title"
  else
    tmux split-window -t "$MT_TMUX_SESSION:$MT_TMUX_WINDOW" -c "$worktree_path" "$cmd"
    local new_pane
    new_pane=$(tmux display-message -p -t "$MT_TMUX_SESSION:$MT_TMUX_WINDOW" '#{pane_id}')
    mark_pane "$new_pane" "$title"
    tmux select-layout -t "$MT_TMUX_SESSION:$MT_TMUX_WINDOW" tiled
  fi
  attach_dashboard
}

cmd_ls() {
  local repo wt branch title pane_id state backend cmd
  while IFS=$'\t' read -r repo wt; do
    [[ -n "$repo" && -n "$wt" ]] || continue
    branch=$(basename "$wt")
    title=$(pane_title "$repo" "$branch")
    # find_pane uses @mt-managed (stable across OSC title changes by agents)
    pane_id=$(find_pane "$title" || true)
    state="dead"; backend="-"
    if [[ -n "$pane_id" ]]; then
      state="live"
      cmd=$(tmux display-message -p -t "$pane_id" '#{pane_current_command}' 2>/dev/null || echo "?")
      case "$cmd" in
        claude*) backend="claude";;
        ollama*) backend="ollama";;
        *)       backend="$cmd";;
      esac
    fi
    printf "%-40s  %-50s  %-8s  %s\n" "$title" "$wt" "$backend" "$state"
  done < <(discover_worktrees)
}

cmd_rm() {
  local force=""
  [[ "${1:-}" == "--force" ]] && force="--force"
  command -v fzf >/dev/null 2>&1 || die "fzf not found"

  local entries
  entries=$(cmd_ls)
  [[ -n "$entries" ]] || die "no worktrees to remove"

  local choice
  if [[ -n "${MT_RM_TITLE:-}" ]]; then
    choice=$(printf '%s' "$entries" | awk -v t="$MT_RM_TITLE" '$1 == t {print; exit}')
    [[ -n "$choice" ]] || die "MT_RM_TITLE not in worktree list: $MT_RM_TITLE"
  else
    choice=$(printf '%s' "$entries" | fzf --prompt="rm> " --height=40%) || exit 1
  fi
  local title path repo branch full_branch
  title=$(printf '%s' "$choice" | awk '{print $1}')
  path=$(printf '%s' "$choice" | awk '{print $2}')
  # Robust: use git to find the parent repo, not dirname-twice (which
  # breaks for claude-style worktrees at `.claude/worktrees/<name>`).
  repo=$(parent_repo_of "$path") || die "could not resolve parent repo for $path"
  branch=$(basename "$path")
  full_branch="$MT_BRANCH_PREFIX/$branch"

  if ! git -C "$repo" worktree remove $force "$path" 2>&1; then
    die "worktree has uncommitted changes; use 'mt rm --force' to bypass"
  fi
  # Try the prefixed branch first (mt's convention). If that doesn't exist,
  # try the bare branch name (claude/external worktrees). Either way only
  # delete if no upstream is set, to avoid losing remote-tracked branches.
  for try_branch in "$full_branch" "$branch"; do
    if git -C "$repo" rev-parse --verify "$try_branch" >/dev/null 2>&1; then
      if ! git -C "$repo" rev-parse "$try_branch@{upstream}" >/dev/null 2>&1; then
        git -C "$repo" branch -D "$try_branch" >/dev/null 2>&1 || true
      fi
      break
    fi
  done
  local pane_id; pane_id=$(find_pane "$title" || true)
  if [[ -n "$pane_id" ]]; then
    tmux kill-pane -t "$pane_id"
    tmux select-layout -t "$MT_TMUX_SESSION:$MT_TMUX_WINDOW" tiled 2>/dev/null || true
  fi
}

usage() {
  cat <<'EOF'
mt — tmux-native dashboard for Claude Code and Ollama across worktrees

usage:
  mt                 attach to (or create) the dashboard window
  mt show            (same as bare mt)
  mt new [--with claude|ollama]    create a worktree + launch agent in a pane
  mt ls              list worktrees: title, path, backend, state (live|dead)
  mt rm [--force]    pick a worktree, remove it (worktree, branch, pane all)
  mt switch [-z]     fzf jump to any pane (live or dead — dead ones revive)
  mt prune [--force] remove all dead worktrees in one shot (interactive confirm)
  mt bind            install tmux keybindings (prefix+g/G/N/R) for in-agent use
  mt diagnose        print state for debugging (versions, config, bindings, log)
  mt --help

config: ~/.config/mt/config.toml — empty file is valid (all fields default)
docs:   https://github.com/jinyuanlu/metatree
EOF
}

main() {
  load_config
  validate_worktree_copy_files
  validate_worktree_base
  # When invoked from inside tmux (e.g. via the prefix+g popup binding),
  # operate on the *calling* session, not whatever the config says. This
  # makes a single binding work across multiple mt sessions (mt, mt-dev,
  # ...) without the user having to rebind per session.
  #
  # Cold boot from a regular shell: $TMUX is unset, so config wins.
  if [[ -n "${TMUX:-}" ]]; then
    local cur_sess cur_win
    cur_sess=$(tmux display-message -p '#{session_name}' 2>/dev/null || true)
    cur_win=$(tmux display-message -p '#{window_name}' 2>/dev/null || true)
    [[ -n "$cur_sess" ]] && MT_TMUX_SESSION="$cur_sess"
    [[ -n "$cur_win" ]] && MT_TMUX_WINDOW="$cur_win"
  fi

  # Globals (not `local`) so the EXIT trap can still see them after main returns.
  MT_INVOKED_CMD="${1:-show}"
  mt_log "INVOKE cmd=$MT_INVOKED_CMD args=$* tmux=${MT_TMUX_SESSION}:${MT_TMUX_WINDOW} in_tmux=${TMUX:+yes} cwd=$PWD"
  trap 'mt_log "EXIT cmd=${MT_INVOKED_CMD:-?} rc=$?"' EXIT

  case "${1:-show}" in
    new)       shift; cmd_new "$@";;
    ls)        cmd_ls;;
    rm)        shift; cmd_rm "$@";;
    switch|sw) shift; cmd_switch "$@";;
    prune)     shift; cmd_prune "$@";;
    bind)      cmd_bind;;
    diagnose|debug) cmd_diagnose;;
    show)      cmd_show;;
    -h|--help) usage;;
    *)         usage; exit 1;;
  esac
}

main "$@"
