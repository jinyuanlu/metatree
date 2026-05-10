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
curl -fsSL https://raw.githubusercontent.com/jinyuanlu/metatree/master/install.sh | bash
```

Installs `mt` to `~/.local/bin/`. Make sure that directory is on your `PATH`.

Requires: `bash`, `tmux` 3.x, `git` ≥ 2.5, `fzf`. Already on every developer Mac. Ollama and Claude Code are optional — install whichever backend you use.

## Usage

```
mt                       # attach to (or create) the dashboard
mt new                   # pick repo + branch, launch claude in a new pane
mt new --with ollama     # ...with a local Ollama session instead
mt switch                # fzf-jump to any pane (revives dead ones)
mt ls                    # list worktrees (live + dead); pipeable
mt rm                    # pick a worktree to remove (refuses on uncommitted)
mt prune                 # bulk-remove all dead worktrees (with confirm)
mt bind                  # install tmux keybindings — run once after install
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

# Pre-approve direnv on new worktrees that have an .envrc. Defaults to "true"
# because the typical mt user creates worktrees of their own repos. Set to
# "false" if you point mt at freshly-cloned third-party repos.
auto_direnv_allow = "true"
```

## Tmux is the entire UI

`mt` adds zero UI primitives. The dashboard window, the panes, the switching — all native tmux. If a feature can't be expressed as `tmux split-window` or `tmux select-layout tiled`, `mt` doesn't do it.

### Daily cheat sheet

Six keys. That's it. Run `mt bind` once after install, and **`prefix + g` is the killer move** — it works while Claude or Ollama is mid-prompt because tmux intercepts the keystroke before the agent sees it.

| Keys           | What it does                                                   |
| -------------- | -------------------------------------------------------------- |
| `mt`           | attach to (or create) the dashboard                            |
| `prefix + g`   | **fzf-jump to any pane** (live or dead — dead ones revive)     |
| `prefix + N`   | popup `mt new` — create a new worktree                         |
| `prefix + R`   | popup `mt rm` — remove a worktree                              |
| `prefix + z`   | zoom one pane to fullscreen / toggle back                      |
| `prefix + d`   | detach (agents keep running)                                   |

> **What is the "prefix"?** Tmux's modifier key. Default: **`Ctrl+b`** — press it, release, then press the next key. Check yours: `tmux show-options -g prefix`.

The standard tmux keys (arrow nav, `prefix + s` for sessions, `prefix + w` for windows, `prefix + [` for scroll) all still work as you'd expect. Press `prefix + ?` for the full list.

### Switching repos from inside Claude (the daily flow)

You're typing into Claude in `acme-api:fix-cookie`. You want to glance at `bytemark-web:add-export`. **Press `prefix + g`**:

1. A popup appears with fzf, listing every active pane.
2. Type `byte` (or any substring of repo or branch).
3. Press `enter`.
4. The popup vanishes. The matching pane is focused and zoomed.

Claude in the original pane is untouched — it never saw your keystrokes. To go back: `prefix + g` again, type `acme`, enter.

This is the affordance Conductor users ask for. It's `prefix + g` on a tmux popup with fzf — about 10 lines of bash in `mt.sh`, no daemon, no Electron, works in Terminal.app.

### Make the bindings permanent

`mt bind` sets keys on the running tmux server only. To survive restarts, add to `~/.tmux.conf`:

```
bind-key g display-popup -w 80% -h 60% -E "mt switch -z"
bind-key G display-popup -w 80% -h 60% -E "mt switch"
bind-key N display-popup -w 80% -h 60% -E "mt new"
bind-key R display-popup -w 80% -h 60% -E "mt rm"
```

Then `tmux source ~/.tmux.conf`. Requires tmux 3.2+ (for `display-popup`).

### Detach and resume

```sh
prefix + d           # detach. agents keep running in the background.
mt                   # reattach (any new terminal, any time later)
tmux attach -t mt    # equivalent — useful when mt isn't on PATH yet
```

New to tmux entirely? `man tmux` is dense but complete. The first 30 minutes of any tmux tutorial covers everything `mt` needs.

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

## Troubleshooting

`prefix + g` (or any other binding) does nothing? In order:

```sh
mt diagnose
```

Prints versions, current config, tmux state, every binding on this server, and the last 10 log lines. Copy/paste the whole block when reporting an issue.

The two most common causes:

- **Bindings not installed on this tmux server.** Look for `display-popup` lines under `KEYBINDINGS` in `mt diagnose`. If empty, the tmux server was restarted since you ran `mt bind`. Run `mt bind` again. Bindings live in tmux server memory only; they're lost on restart unless you've added the four `bind-key` lines to `~/.tmux.conf`.
- **`mt switch` is dying silently inside the popup** (popup closes too fast to read the error). Reproduce from a plain shell:
  ```sh
  mt switch    # see the actual error message
  ```

Every invocation is logged to `~/.local/state/mt/mt.log` (override with `MT_LOG`). Tail it while pressing `prefix + g` to see exactly what tmux ran:

```sh
tail -f ~/.local/state/mt/mt.log
# then press prefix + g in another terminal
```

If you see an `INVOKE` line with `cmd=switch` and an `EXIT` line with non-zero `rc=`, that's the failure. If you see no `INVOKE` line at all, the binding never fired (probably not installed — see above).

## Testing

If you have [`just`](https://github.com/casey/just) installed (`brew install just`):

```sh
just              # list recipes
just test        # run e2e smoke (the load-bearing test, ~3s)
just test-units  # bats unit tests (needs: brew install bats-core)
just test-all    # smoke + units
just check       # bash -n syntax check
just install     # local install: mt.sh → ~/.local/bin/mt
```

Without `just`:

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
