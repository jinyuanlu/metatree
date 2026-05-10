#!/usr/bin/env bash
# mt — tmux-native control plane for Claude Code and Ollama across worktrees
# https://github.com/jinyuanlu/metatree
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
MT_DEFAULT_BACKEND="claude"
MT_OLLAMA_MODEL="llama3:8b"
MT_CLAUDE_CMD="claude"
MT_OLLAMA_CMD="ollama run {model}"
MT_REPOS_DIRS=("$HOME/Code")
MT_REPOS=()
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
        repos_dirs) MT_REPOS_DIRS=("${items[@]}");;
        repos)      MT_REPOS=("${items[@]}");;
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

# pane title: `repo_name:branch`. on basename collision, append -<sha8(repo_path)>.
pane_title() {
  local repo_path="$1" branch="$2" repo_name
  repo_name=$(basename "$repo_path")
  printf '%s:%s' "$repo_name" "$branch"
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
  local live_entries dead_entries
  # mt-managed panes only — skip bare shells and any external panes the user
  # may have split off. Display the @mt-managed value (stable; not OSC-clobbered).
  live_entries=$(tmux list-panes -t "$MT_TMUX_SESSION:$MT_TMUX_WINDOW" \
    -F '#{@mt-managed}|live|#{pane_id}' 2>/dev/null \
    | grep -v '^|')
  # dead = worktrees on disk with no matching live pane on this dashboard
  dead_entries=$(cmd_ls 2>/dev/null \
    | awk '$NF == "dead" { printf "%s|dead|%s\n", $1, $2 }')

  local entries
  entries=$(printf '%s\n%s\n' "$live_entries" "$dead_entries" | grep -v '^$' || true)
  [[ -n "$entries" ]] || die "no panes or worktrees to switch to"

  local choice
  choice=$(printf '%s\n' "$entries" \
    | awk -F'|' '{
        if ($2 == "live") printf "%-40s  [live]  %s\n", $1, $3
        else              printf "%-40s  [dead]  %s\n", $1, $3
      }' \
    | fzf --prompt="switch> " --height=40% --with-nth=1,2 --delimiter='[[:space:]]+') \
    || exit 1

  # last whitespace-delimited token is either pane_id (live) or worktree path (dead)
  local key marker
  key=$(printf '%s' "$choice" | awk '{print $NF}')
  marker=$(printf '%s' "$choice" | awk '{print $(NF-1)}')

  if [[ "$marker" == "[live]" ]]; then
    tmux select-pane -t "$key"
    $zoom && tmux resize-pane -t "$key" -Z
    attach_dashboard
  else
    # revive: $key is the worktree path, derive repo + branch and re-launch
    local wt_path="$key"
    local branch repo
    branch=$(basename "$wt_path")
    repo=$(dirname "$(dirname "$wt_path")")
    MT_REPO="$repo" MT_BRANCH="$branch" cmd_new --with "$MT_DEFAULT_BACKEND"
  fi
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
    local title path repo branch full_branch
    title=$(printf '%s' "$line" | awk '{print $1}')
    path=$(printf '%s' "$line" | awk '{print $2}')
    repo=$(dirname "$(dirname "$path")")
    branch=$(basename "$path")
    full_branch="$MT_BRANCH_PREFIX/$branch"

    if git -C "$repo" worktree remove $force "$path" >/dev/null 2>&1; then
      git -C "$repo" branch -D "$full_branch" >/dev/null 2>&1 || true
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
    repo=$(printf '%s' "$repos" | grep -Fx "$MT_REPO" | head -1)
    [[ -n "$repo" ]] || die "MT_REPO not in discovered repos: $MT_REPO"
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
    # If the parent repo uses git-crypt, plain `git worktree add` fails: the
    # smudge filter runs in the new worktree's context where GIT_DIR points
    # at <parent>/.git/worktrees/<name>, but git-crypt looks for its key at
    # $GIT_DIR/git-crypt/keys/default — which doesn't exist (the key lives
    # at the parent's <parent>/.git/git-crypt/keys/default). So we use the
    # --no-checkout pattern, copy the key into the worktree's per-worktree
    # git dir, then check out files (smudge now finds the key, decrypts).
    if [[ -f "$repo/.git/git-crypt/keys/default" ]] \
       && command -v git-crypt >/dev/null 2>&1; then
      git -C "$repo" worktree add --no-checkout -b "$full_branch" "$worktree_path" \
        || exit $?
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
      git -C "$repo" worktree add -b "$full_branch" "$worktree_path" || exit $?
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
  for repo in $(discover_repos); do
    [[ -d "$repo/$MT_WORKTREE_SUBDIR" ]] || continue
    for wt in "$repo/$MT_WORKTREE_SUBDIR"/*; do
      [[ -d "$wt" ]] || continue
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
    done
  done
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
  repo=$(dirname "$(dirname "$path")")
  branch=$(basename "$path")
  full_branch="$MT_BRANCH_PREFIX/$branch"

  if ! git -C "$repo" worktree remove $force "$path" 2>&1; then
    die "worktree has uncommitted changes; use 'mt rm --force' to bypass"
  fi
  if ! git -C "$repo" rev-parse "$full_branch@{upstream}" >/dev/null 2>&1; then
    git -C "$repo" branch -D "$full_branch" 2>/dev/null || true
  fi
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
