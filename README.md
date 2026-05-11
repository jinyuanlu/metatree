# `mt` — one tmux window, every repo, Claude or Ollama

A control-plane CLI that spawns Claude Code or Ollama sessions in fresh git worktrees, presented as a single tiled tmux dashboard so multiple repos are visible and operable on one page. Single static Go binary, no daemon, no TUI, no install dance.

![mt: spawn an agent in one repo, prefix+g to a switch popup, + Create new for a second repo (tiled view), prefix+g again to revive a dead worktree — three agents in one tmux window](demo.gif)

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

Installs the Go binary to `~/.local/bin/mt`. The installer is short, audit-friendly bash that fetches the right prebuilt binary for your OS+arch from GitHub Releases and verifies its checksum. The last line it prints (`installed: mt vX.Y.Z (commit …, built …)`) is the build identity — paste it into any bug report so we can map back to a specific commit. Re-check at any time with `mt --version`.

Power users can build from source instead:

```sh
go install github.com/jinyuanlu/metatree/cmd/mt@latest
```

Requires: `tmux` 3.2+ (for `display-popup`), `git` ≥ 2.5, `fzf`. Already on every developer Mac. Ollama and Claude Code are optional — install whichever backend you use.

## Quick start

From zero to first agent in three commands:

```sh
curl -fsSL https://raw.githubusercontent.com/jinyuanlu/metatree/master/install.sh | bash
mt bind              # one-time tmux keybindings (prefix+g / N / R)
mt new               # pick repo, name branch, agent launches in fresh worktree
```

After that, bare `mt` reattaches yesterday's dashboard. `prefix + g` switches between agents from inside an active Claude/Ollama prompt — the killer move (see [Daily cheat sheet](#daily-cheat-sheet)).

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
mt setup                 # configure repos_dirs + claude_cmd (or use flags)
mt upgrade               # download the latest release; replace this binary
mt --help
```

## First run

The first time you run any `mt` command, it bootstraps a config:

- If you have any of `~/Developer`, `~/Code`, `~/dev`, `~/src`, `~/Projects` (etc.) under your home, mt seeds `repos_dirs` from those and writes `~/.metatree/config.toml` silently. Zero prompts.
- If none exist and stdin is a terminal, you get **one** prompt: "Where do you keep your code?" — hit Enter to accept the bracketed default, or type a comma-separated list.
- If you already had `~/.config/mt/config.toml` from before v1.1, it's auto-copied to `~/.metatree/config.toml` once with a stderr notice. The legacy file isn't deleted; clean it up when you're ready.

You can re-enter setup at any time with `mt setup` (interactive) or non-interactively:

```
mt setup --repos-dirs ~/work,~/oss
mt setup --claude-cmd "claude --mcp-config ~/.claude/mcp.json"
mt setup --print          # dump resolved config to stdout
mt setup --reset          # forget current; rewrite from defaults
```

Setting `claude_cmd` literally (with all the flags you want) is the recommended way to load MCP — no shell aliases, no surprises.

Two `mt new` behaviors worth knowing about up front (both fully described in [Config](#config) below):

- **Runtime files are auto-copied** — `.env`, `.envrc`, `.npmrc` carry from the parent repo into each fresh worktree so it runs out of the box. Customize via `worktree_copy_files`.
- **Worktrees branch from `origin/<default>`** — not from the parent's stale HEAD. Override per-invocation with `MT_BASE=head mt new` (handy for stacking on a feature branch), or globally with `worktree_base = "head"`.

## Upgrading

```sh
mt upgrade           # fetch latest release, verify checksum, atomic replace
mt upgrade --check   # report current vs latest, no download
```

Your `~/.metatree/config.toml` is never touched — that's the whole point of the directory split.

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

`~/.metatree/config.toml`. Created by `mt setup` (or auto-seeded on first run if a common dev folder exists). Empty file is valid; everything defaults.

```toml
# directories to scan for git repos (depth ≤ 4).
# Set via `mt setup --repos-dirs ~/work,~/oss` or edit by hand.
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

# Gitignored runtime files copied from the parent repo into each new
# worktree, so the worktree runs out of the box. Root-only filenames
# in v1 (no subdirs, no .., no absolute paths). Set to [] to disable.
# Pre-existing destinations and git-crypt-encrypted sources are skipped;
# writes are atomic (temp file + rename).
worktree_copy_files = [".env", ".envrc", ".npmrc"]

# Where `mt new` branches FROM. Three modes:
#   "origin-default"  fetch origin's default branch (main/master/develop/
#                     trunk via symbolic-ref, 10s timeout) and branch from
#                     origin/<default>. Falls back through stale origin
#                     ref → local <default> → parent HEAD on fetch failure.
#   "head"            branch from the parent repo's current HEAD (legacy).
#   any other string  treated as a literal git ref (e.g. "develop",
#                     "upstream/main"). No fetch.
# Per-invocation override: MT_BASE=head mt new (handy for stacking on a
# feature branch). See spec.md §2.6.3 for the full contract.
worktree_base = "origin-default"

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

This is the affordance Conductor users ask for. It's `prefix + g` on a tmux popup with `fzf` — no daemon, no Electron, works in Terminal.app.

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
- **Single static Go binary.** No TUI framework, no terminal-emulator requirements, no runtime to install. The binary is small enough to vendor; the source is small enough to read in one sitting (see [`CONTRIBUTING.md`](CONTRIBUTING.md) for the architecture).

If those aren't your priorities, [`claude-squad`](https://github.com/smtg-ai/claude-squad), [`cmux`](https://github.com/craigsc/cmux), [`ccmanager`](https://github.com/kbwo/ccmanager), [`muxtree`](https://dev.to/b-d055/introducing-muxtree-dead-simple-worktree-tmux-sessions-for-ai-coding-2kf2) are all good neighbors.

<details>
<summary><b>How <code>claude_cmd</code> is launched</b> — implementation detail; expand only if your alias-driven flags aren't reaching the agent</summary>

`mt` runs `claude` (and `ollama`) through your interactive shell so that any alias you've defined for the agent is honored — the typical case is `alias claude='claude --mcp-config ~/.claude/mcp.json'`, and that flag survives every `mt new`. This applies to both the first pane and every subsequent pane.

Concretely:

- **First pane.** `mt` types `cd <worktree> && <claude_cmd>; exit` into the existing dashboard pane shell. The shell expands the alias before running; `; exit` closes the pane when the agent exits.
- **Subsequent panes.** `mt` runs `tmux split-window` with the agent command wrapped as `$SHELL -ic '<claude_cmd>'`. The wrapping shell reads your rcfile (`.bashrc` / `.zshrc` / `config.fish`), expands the alias, and exits when the agent exits — closing the pane.

We deliberately do **not** prefix the inner command with `exec`: in both bash and zsh, `exec <name>` is a special-builtin form that suppresses alias expansion on its argument, which would silently drop the `--mcp-config` flag from your alias and load the bare `claude` binary.

The wrap is enabled when `$SHELL` is `bash`, `zsh`, or `fish`. For other shells (nushell, PowerShell, dash, …), `mt` passes the command through unchanged and prints a one-line warning so you know aliases won't expand. Bake the literal command into config to recover:

```toml
# ~/.metatree/config.toml
claude_cmd = "claude --mcp-config $HOME/.claude/mcp.json"
```

That string is honored verbatim, regardless of shell.

</details>

## Auth invariant (Claude Code)

`mt` is a control-plane CLI: it creates the worktree, launches `claude` in a tmux pane, and exits. After that, `claude` owns its OAuth refresh cycle entirely — the same way `cd <repo> && claude` typed at a shell prompt does.

`mt` never:

- reads or writes `~/.claude/.credentials.json`
- sets `ANTHROPIC_API_KEY` or `ANTHROPIC_TOKEN`
- stays in the process tree of `claude`

This means parallel sessions cannot race on credentials by way of `mt`. Anthropic has shipped fixes for the underlying refresh-token race, so this is now hygiene rather than a moat — but `mt` cannot regress credentials safety regardless of upstream changes.

The Ollama backend has no analog: local model servers have no shared credentials.

## Spec & contributing

- **[`spec.md`](spec.md)** — the **product** spec: commands, behaviors, acceptance criteria, the auth invariant (§1.4), the dashboard topology (§2.5), the failure-modes table (§2.8). The source of truth for what `mt` does.
- **[`CONTRIBUTING.md`](CONTRIBUTING.md)** — what you need to land code: package layout, error handling, testing tiers, distribution, anti-patterns. Faster than reading the code.
- **[`TODOS.md`](TODOS.md)** — deferred work and v2 candidates with the design context captured (so future-you doesn't re-derive it).

`mt.sh` is the bash reference implementation, maintained in lockstep with the Go binary as an executable cross-implementation regression check. The Go port is the production implementation users install as of v1.0; if the two ever disagree, `spec.md` wins.

## What you see on the dashboard

`mt` configures two pieces of native tmux chrome on its dashboard window so you always know where you are, even when an agent has overwritten the terminal title:

- **Pane border (top of each pane)** — shows `<index> <repo>:<branch> [<base-ref>] (<command>)`, e.g. `0 acme-api:fix-cookie [origin/main] (bash)`. The `<repo>:<branch>` part is the stable mt marker (a tmux per-pane user option, immune to OSC escape sequences from inside the agent); `[<base-ref>]` is the ref the worktree was branched from (omitted on respawned worktrees).
- **Status bar (bottom right)** — shows the active pane's `<repo>:<branch>` marker, the actual filesystem cwd (`#{pane_current_path}`, truncated to 50 chars), and a clock. Refreshes every 2 seconds.

Both are scoped to mt's session/window only — your other tmux sessions are untouched. Disable with `auto_status_chrome = "false"` in `~/.metatree/config.toml`.

**MCP status**: Claude Code's MCP state isn't exposed to tmux. Any "live" indicator would mean polling `claude mcp list` on a tmux refresh interval, which adds latency and load. If Claude ships a way to read MCP state from a file in the future (`~/.claude/mcp-status.json` or similar), `mt` can pick it up via `#{?mcp_ok,✓,✗}` in the status line — until then, use Claude's own `/doctor` to check.

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

The Go port ships with three test tiers. With [`just`](https://github.com/casey/just) installed (`brew install just`):

```sh
just              # list recipes
just build        # go build -o ./bin/mt-go ./cmd/mt
just test-go      # Go unit + integration tests with -race
just lint         # gofmt + go vet
just test         # bash smoke (tests mt.sh, the bash reference implementation)
```

Without `just`:

```sh
# Go unit + integration (the load-bearing tier)
go test -race ./...

# Build the binary
go build -o ./bin/mt-go ./cmd/mt

# Bash smoke (cross-implementation regression against mt.sh)
bash tests/smoke.sh

# Bats tests for mt.sh-specific helpers (requires bats-core: brew install bats-core)
bats tests/bats/
```

The Go test suite covers four layers: `internal/config` (TOML schema, defaults), `internal/mtlog` (invocation log), `internal/tmuxio` (tmux wrappers, real fixtures), `internal/gitio` (worktree discovery, real git fixtures), plus `tests/integration_test.go` driving the built binary end-to-end. CI runs `gofmt -l`, `go vet`, `go test -race ./...`, the smoke against the Go binary, and an auth-invariant grep on every push and PR. See [`.github/workflows/ci.yml`](.github/workflows/ci.yml).

`tests/smoke.sh` (19 sections) is the executable specification, run against both implementations as the cross-implementation regression suite. `tests/bats/` covers bash-only behaviors not exercised by smoke (e.g. `mt.sh`'s `copy_runtime_files` helper).

## License

MIT — see [`LICENSE`](LICENSE).
