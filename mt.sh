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

# ---------------------------------------------------------------------------
# helpers
# ---------------------------------------------------------------------------
die() { printf 'mt: %s\n' "$*" >&2; exit 1; }

slugify() {
  printf '%s' "$1" | tr '[:upper:]' '[:lower:]' \
    | sed -E 's/[^a-z0-9-]+/-/g; s/-+/-/g; s/^-//; s/-$//'
}

expand_tilde() { printf '%s' "${1/#\~/$HOME}"; }

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
  tmux set-window-option -t "$MT_TMUX_SESSION:$MT_TMUX_WINDOW" pane-border-status top 2>/dev/null || true
}

find_pane() {
  tmux list-panes -t "$MT_TMUX_SESSION:$MT_TMUX_WINDOW" \
    -F '#{pane_id} #{pane_title}' 2>/dev/null \
    | awk -v t="$1" '$2 == t {print $1; exit}'
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
    git -C "$repo" worktree list --porcelain | grep -q "^worktree $worktree_path$" \
      || die "path exists: $worktree_path"
  else
    git -C "$repo" worktree add -b "$full_branch" "$worktree_path" || exit $?
    # Pre-approve direnv so the agent's pane doesn't see "blocked .envrc".
    # Skipped when: feature disabled, no .envrc, direnv missing, or call fails.
    if [[ "$MT_AUTO_DIRENV_ALLOW" == "true" ]] \
       && [[ -f "$worktree_path/.envrc" ]] \
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
  # dashboard. We detect "bare-shell" by checking whether ANY existing pane
  # has an mt-shaped title (`<repo>:<branch>`). If none do, the dashboard
  # only contains the pane tmux gave us at session creation — reuse it.
  local mt_pane_count
  mt_pane_count=$(tmux list-panes -t "$MT_TMUX_SESSION:$MT_TMUX_WINDOW" \
    -F '#{pane_title}' 2>/dev/null \
    | grep -cE '^[^:[:space:]]+:[a-z0-9-]+$' || true)

  if [[ "$mt_pane_count" -eq 0 ]]; then
    local first_pane
    first_pane=$(tmux list-panes -t "$MT_TMUX_SESSION:$MT_TMUX_WINDOW" -F '#{pane_id}' | head -1)
    tmux send-keys -t "$first_pane" "cd $worktree_path && exec $cmd" Enter
    tmux select-pane -t "$first_pane" -T "$title"
  else
    tmux split-window -t "$MT_TMUX_SESSION:$MT_TMUX_WINDOW" -c "$worktree_path" "$cmd"
    tmux select-pane -t "$MT_TMUX_SESSION:$MT_TMUX_WINDOW" -T "$title"
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
  mt              attach to (or create) the dashboard window
  mt show         (same as bare mt)
  mt new [--with claude|ollama]   create a worktree + launch agent in a pane
  mt ls           list worktrees: title, path, backend, state (live|dead)
  mt rm [--force] pick a worktree, remove it (worktree, branch, pane all)
  mt --help

config: ~/.config/mt/config.toml — empty file is valid (all fields default)
docs:   https://github.com/jinyuanlu/metatree
EOF
}

main() {
  load_config
  case "${1:-show}" in
    new)       shift; cmd_new "$@";;
    ls)        cmd_ls;;
    rm)        shift; cmd_rm "$@";;
    show)      cmd_show;;
    -h|--help) usage;;
    *)         usage; exit 1;;
  esac
}

main "$@"
