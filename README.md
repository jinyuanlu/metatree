# `mt` — one tmux window, every repo, Claude or Ollama

A control-plane CLI that spawns Claude Code or Ollama sessions in fresh git worktrees, presented as a single tiled tmux dashboard so multiple repos are visible and operable on one page. About 250 lines of bash. No daemon, no TUI, no install dance.

```
┌─────────────────────┬───────────────────────┐
│ acme-api:fix-cookie │ bytemark-web:pr-fixes │
│ (claude, paused)    │ (claude, paused)      │
├─────────────────────┴───────────────────────┤
│ sideproject:llama-spike  (ollama, idle)     │
└─────────────────────────────────────────────┘
```

## The aha

Most parallel-agent tools optimize the moment you spawn the 4th Claude session. That's not the pain.

The pain is the next morning, when you can't remember which of your 11 tmux windows held the bug fix you were halfway through.

**`mt`'s value is at 07:30 the next day** — bare `mt` reattaches the exact dashboard from yesterday. Every active repo visible at once. Every paused agent ready to resume. No walk through tmux windows trying to remember.

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/<user>/metatree/main/install.sh | bash
```

Installs `mt` to `~/.local/bin/`. Make sure that directory is on your `PATH`.

Requires: `bash`, `tmux` 3.x, `git` ≥ 2.5, `fzf`. Already on every developer Mac. Ollama and Claude Code are optional — install whichever backend you use.

## Usage

```
mt                              # attach to (or create) the dashboard
mt new                          # pick a repo, name a branch, launch claude in a new pane
mt new --with ollama            # ...with a local Ollama session instead
mt ls                           # list worktrees: title, path, backend, state
mt ls | grep feat-              # pipeable
mt rm                           # pick a worktree, remove it (refuses on uncommitted changes)
mt rm --force                   # ...unless you mean it
mt --help
```

## A weekday with `mt`

```
07:30  mt          dashboard reattaches with yesterday's panes:
                   acme-api:fix-cookie | bytemark-web:pr-fixes
                   sideproject:llama-spike (ollama)
                   prefix+arrow into the bug-fix pane, resume.
08:00  agent is already in the right repo, on the right branch. type. push.
10:00  mt new      fzf → "repoC" → branch "spike-A". 4th pane lights up in ~3s.
11:00  mt new --with ollama → "sideproject" → "compare-mistral". 5 panes, retiled.
12:30  one pane is awaiting input. prefix+z to zoom, answer, prefix+z out.
14:00  mt rm       fzf → acme-api:fix-cookie. worktree, branch, pane all gone.
17:00  prefix+d    detach. tomorrow: mt → survivors are still there.
```

## Config

`~/.config/mt/config.toml`. Empty file is valid; everything defaults.

```toml
# directories to scan for git repos (depth ≤ 4)
repos_dirs = ["~/Code"]

# OR explicit list (overrides repos_dirs if set)
# repos = ["~/Code/acme-api", "~/Code/bytemark-web"]

# tmux session and window holding the dashboard
tmux_session = "mt"
tmux_window  = "dashboard"

# branches are created as <prefix>/<name>
branch_prefix   = "mt"
worktree_subdir = ".worktrees"

# agent backends
default_backend = "claude"
ollama_model    = "llama3:8b"

# command templates (overridable; defaults just work)
claude_cmd = "claude"
ollama_cmd = "ollama run {model}"
```

## Tmux is the entire UI

`mt` adds zero UI primitives. Everything you see and do, you do through tmux:

- `prefix + arrow` — switch panes
- `prefix + z` — zoom one pane to fullscreen, again to unzoom
- `prefix + space` — re-tile (rarely needed; `mt` re-tiles on every change)
- `prefix + d` — detach. `tmux attach -t mt` (or just `mt`) reattaches.

If you can't express it as a tmux command, `mt` doesn't do it.

## Why not [other tool]

There are several adjacent tools. `mt`'s wedge:

- **`mt` is one tmux window with tiled panes**, not one window per session. The dashboard is the unit of persistence — every repo is visible at once, no `prefix + w` listings to scroll. Most alternatives use one tmux window per agent.
- **Dual backend (Claude Code + Ollama) with the same UX.** Most tools are Claude-only or Codex-only.
- **About 250 lines of bash.** No Go binary, no TUI framework, no terminal-emulator requirements. Source it from your rc, audit it before installing, modify it without a build step.

If those aren't your priorities, [`claude-squad`](https://github.com/smtg-ai/claude-squad), [`cmux`](https://github.com/craigsc/cmux), [`ccmanager`](https://github.com/kbwo/ccmanager), [`muxtree`](https://dev.to/b-d055/introducing-muxtree-dead-simple-worktree-tmux-sessions-for-ai-coding-2kf2) are all good neighbors.

## Auth invariant (Claude Code)

`mt` is a control-plane CLI: it creates the worktree, launches `claude` in a tmux pane, and exits. After that, `claude` owns its OAuth refresh cycle entirely — the same way `cd <repo> && claude` typed at a shell prompt does.

`mt` never:

- reads or writes `~/.claude/.credentials.json`
- sets `ANTHROPIC_API_KEY` or `ANTHROPIC_TOKEN`
- stays in the process tree of `claude`

This means parallel sessions cannot race on credentials by way of `mt`. Anthropic has shipped fixes for the underlying refresh-token race, so this is now hygiene rather than a moat — but `mt` cannot regress credentials safety regardless of upstream changes.

The Ollama backend has no analog: local model servers have no shared credentials.

## Spec

The full design rationale, domain model, acceptance criteria, and failure modes live in [`spec.md`](spec.md). Worth reading if you're forking or want to understand the auth invariant in detail.

## Testing

```sh
# end-to-end integration test (no dependencies beyond bash + tmux + git)
bash tests/smoke.sh

# pure-function unit tests (requires: brew install bats-core)
bats tests/mt.bats
```

`tests/smoke.sh` is the load-bearing test. It builds a temporary git fixture under `/tmp/mt-smoke.$$`, runs a headless tmux server in an isolated socket directory, and exercises the full `mt new → mt ls → mt rm` cycle non-interactively (via `MT_REPO`, `MT_BRANCH`, `MT_RM_TITLE` env-var hooks). Asserts: worktree creation, branch creation, pane creation with correct title, idempotency, dirty-tree refusal, `--force` bypass, and the auth invariant (no non-comment references to `credentials.json` or `ANTHROPIC_*` in `mt.sh`).

`tests/mt.bats` covers the pure functions (`slugify`, `pane_title`, `expand_tilde`, `load_config`) — fast, dependency-free regression catches.

## License

MIT — see [`LICENSE`](LICENSE).
