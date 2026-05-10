# `mt` (metatree) — Specification

> Binary name `mt`. Project directory `metatree`. Rename if either conflicts with anything in your existing toolchain.

A control-plane CLI for spawning agent sessions (Claude Code or Ollama) in fresh git worktrees, presented as a single-window tmux dashboard so multiple repos are visible and operable on one page.

---

## 1. Basic principle

### 1.1 What `mt` is

One tmux window. Every repo you work on. A Claude Code or Ollama session in each pane, all visible at once, switched with native tmux bindings. About 250 lines of bash.

That is the whole product:

- **One page, many repos.** A single `mt:dashboard` tmux window holds one tiled pane per active session. You see the bug-fix agent in `repoA:fix-cookie`, the feature agent in `repoB:add-export`, and the local model in `repoC:rewrite-tests` simultaneously. `prefix + z` zooms one to fullscreen; `prefix + space` re-tiles. No window switching.
- **Two backends, same UX.** Claude Code for cloud-quality reasoning, Ollama for local cost / privacy / model comparison. Pick at session creation; switch by opening another pane.
- **Tmux is the entire UI.** No TUI framework, no daemon, no terminal-emulator requirements. If a feature can't be expressed as `tmux split-window` or `tmux select-layout tiled`, it isn't in V1.

`mt` is the polished version of the shell function you would have written for yourself. Boring tools — bash, tmux, fzf, git — composed into something you'd reach for daily.

### 1.2 Tmux is the entire UI

`mt` adds **zero** UI primitives. Everything the user sees and does, they do through tmux:

- Pane creation: `mt` calls `tmux split-window`. That's the only "rendering" `mt` does.
- Switching focus: `prefix + arrow`, `prefix + o`, `prefix + q <n>`. Native tmux.
- Zoom one pane to fullscreen: `prefix + z`. Native tmux.
- Re-tile after closing a pane: `prefix + space`. Native tmux.
- See everything at once: the dashboard window's tiled layout, refreshed by tmux on every event.
- Detach / reattach: `prefix + d`, then `tmux attach -t mt`. Native tmux.

No TUI library, no ncurses, no Bubble Tea, no Ink, no web view. If a feature can't be expressed as "shell out to tmux," it doesn't exist in V1.

This implies: works in any terminal that runs tmux — Terminal.app, iTerm2, plain xterm, SSH-over-Telegram. No 256-color requirement, no Unicode glyphs, no Nerd Font, no mouse mode mandated.

### 1.3 Backend abstraction

Two backends, chosen per session:

- **Claude Code** (`mt new --with claude`). Cloud agent. Owns its own OAuth lifecycle in-process. The auth invariant in §1.4 applies.
- **Ollama** (`mt new --with ollama`). Local model server. No remote credentials, no shared state. The auth invariant is moot here; the constraint exists solely because Claude's cloud model demands it.

Same dashboard, same pane mechanics, same `mt new / mt ls / mt rm / mt show` surface. The only thing that varies is the command launched inside the pane (`claude` vs `ollama run <model>`). Default backend is configured in `~/.config/mt/config.toml`; the `--with` flag overrides per invocation.

Mixing backends on one dashboard is a feature, not an accident: a Claude pane reasoning about an architectural change next to two Ollama panes drafting test cases is the kind of workflow this layout exists to support.

### 1.4 Auth invariant (Claude Code backend)

For every Claude Code session `s` produced by `mt`, the following **MUST** hold:

```
parent(s.claude_process) ∈ {tmux_pane, login_shell}
∧ mt ∉ ancestors(s.claude_process)
∧ mt does not read or write ~/.claude/.credentials.json
∧ mt does not set ANTHROPIC_* env vars in s.environment
```

Equivalently: a `claude` process launched by `mt` is observationally indistinguishable from `cd <path> && claude` typed at a shell prompt.

**Why this matters.** Anthropic shipped fixes for the OAuth refresh-token race in early 2026, so subprocess-wrapping orchestrators are no longer guaranteed-broken. But `mt` doesn't depend on that fix being correct or remaining correct. By staying out of the credential lifecycle entirely, `mt` cannot regress credentials safety regardless of upstream changes:

- Token-refresh races are impossible by construction. Each `claude` owns its refresh cycle in-process, identical to manual usage.
- Fleet failure modes reduce to single-session failure modes.
- Mid-session reauth failures only occur if `claude` itself triggers them — a scenario the user already knows how to handle and which `mt` cannot make worse.
- `mt` inherits Anthropic's official auth contract. Future changes to that contract require zero changes to `mt`.

This invariant is normative (see §2.12 — three permanent rejections derive from it). It is the reason `mt` is a control-plane CLI rather than a session manager: any feature that would force `mt` to inspect or mutate Claude Code's credentials would violate it.

The invariant has no analog for Ollama — local model servers have no shared credentials to race on.

### 1.5 Process tree contract

```
mt                           ← exits within ~100ms
  ├─ git worktree add ...    ← synchronous, exits
  └─ tmux split-window ...   ← sends to tmux server, exits

tmux server (long-lived, pre-existing or autostarted)
  └─ session "mt"
      └─ window "dashboard"
          ├─ pane: claude    ← session A; owns its own auth lifecycle
          ├─ pane: claude    ← session B
          └─ pane: ollama    ← session C
```

`mt` is a control-plane CLI. It does not host long-lived state. Ten seconds after invocation, no `mt`-named process exists in the system.

### 1.6 Daily workflow (concrete profile)

The features in §2 are dimensioned for one user shape. Naming it explicitly so a reader can self-identify in ten seconds:

**Profile: Jordan.**

- Independent contractor or small-team developer. **3–7 active repos** at any time, mix of client work and side projects.
- macOS, Terminal.app default profile, tmux 3.4+, fzf, git. **No exotic terminal.**
- Claude Max subscription. Also runs Ollama locally for cheap exploratory work and model comparison.
- Was bitten by parallel-Claude reauth in late 2025; even after the upstream fix shipped, the anxiety lingers.
- **Tolerates** `tmux` keys (knows `prefix + arrow`, `prefix + z`, `prefix + d`).
- **Will not** install a Go binary, configure a TUI, or learn a new keybinding scheme just to manage worktrees.
- Two-monitor setup at the desk; just the laptop on the road. Workflow has to survive both.

**A weekday without `mt`.**

```
07:30  laptop open. tmux attach. 11 windows from last night.
       which one was the bug fix? prefix+w, eyeball titles, guess wrong twice.
08:00  bug ticket on acme-api. cd ~/Code/acme-api. git checkout -b fix-cookie.
       claude. wait 4s for the prompt.
09:15  PR feedback on bytemark-web. new tmux window. cd. checkout. claude. wait.
10:00  what was the bug agent doing? prefix+w, scroll, eyeball, hunt.
11:00  side-project spike with a local model. new window. ollama run llama3.
12:30  three Claudes running. one has been waiting on a clarifying question
       for 20 minutes. nobody told me. I only notice because I happened to
       prefix+w through the list.
14:00  bug done. cd ~. git worktree remove ../acme-api/.worktrees/fix-cookie.
       wrong path. retry. git branch -D fix-cookie. close the window manually.
17:00  prefix+d. eight live windows. no idea which had pending state.
       tomorrow's me will figure it out.
```

The pain is not any single step. It is the **mental tax of remembering where everything is**, plus the silent failure of *"this agent is waiting on me and I'm not looking at it."* The cost compounds across days: Tuesday inherits Monday's confusion.

**The same day with `mt`.**

```
07:30  mt          dashboard reattaches with yesterday's three panes,
                   already tiled, state intact:
                     ┌─────────────────────┬───────────────────────┐
                     │ acme-api:fix-cookie │ bytemark-web:pr-fixes │
                     │ (claude, paused)    │ (claude, paused)      │
                     ├─────────────────────┴───────────────────────┤
                     │ sideproject:llama-spike  (ollama, idle)     │
                     └─────────────────────────────────────────────┘
                   prefix+arrow into the bug-fix pane, resume typing.
08:00  agent is already in the right repo, on the right branch. type. push.
09:15  prefix+arrow → bytemark-web pane. address PR comment. push.
10:00  new spike. mt new → fzf → "repoC" → branch "spike-A"
                   4th pane lights up in ~3s; claude prompt visible.
11:00  parallel ollama experiment. mt new --with ollama
                   → fzf → "sideproject" → "compare-mistral".
                   5 panes; layout re-tiles automatically.
12:30  one pane's border goes yellow — agent is awaiting input.
                   prefix+z to zoom, answer, prefix+z out.
14:00  bug fix done. mt rm → fzf → acme-api:fix-cookie → confirm.
                   worktree removed, branch deleted, pane killed, layout
                   re-tiled. one keystroke covers what was four commands.
17:00  prefix+d. tomorrow: mt → survivors are still there, exactly as left.
```

**The aha moment.**

The aha is **not** at 10:00 when `mt new` spawns the 4th agent in three seconds. Every parallel-agent tool can do that. Spawn cost is a solved problem.

**The aha is at 07:30 the next morning, when bare `mt` reattaches the exact dashboard from yesterday — every active repo visible at once, every paused agent ready to resume, no walk through tmux windows trying to remember which one was the bug fix.** The pain `mt` removes is not the spawn cost; it is the **morning context-load**.

This reframes what the product actually is:

- **The dashboard is the unit of persistence.** §2.5 (one window, many panes) is not a layout choice — it is the artifact that survives across days, holds state, and makes resumption possible. Other tools open new windows or sessions per agent; those windows scatter across `prefix + w` listings overnight. `mt`'s tiled single window can't scatter.
- **`mt show` is the most-invoked command, not `mt new`.** §2.3 spends words on `new` because it's the interesting path, but in actual use `show` runs ~100 times for every `new`. First-action-of-the-day reliability is what the daily flow demands; §2.8 names "tmux server died overnight" as a failure mode that must succeed silently.
- **The auth invariant (§1.4) earns its keep across days, not within them.** A single session running for an hour is unlikely to hit a refresh; ten sessions running for ten days are guaranteed to. The invariant matters precisely because Jordan's dashboard outlives any single agent.

This profile drives the UX decisions in §2.3 (bare `mt` autocreates the dashboard, `mt rm` refuses to destroy uncommitted work, `mt ls` is pipeable) and §2.7 (no reauth across 10 sessions, fast pane creation under 5 seconds, Terminal.app default profile works with no config).

---

## 2. Version 1 plan

### 2.1 Scope

V1 ships **exactly one feature**: fast creation of a worktree across any tracked repository, with an agent session (Claude Code or Ollama) launched in a tmux pane on a single shared dashboard window. All active sessions are visible simultaneously; the user navigates between them with native tmux pane bindings.

### 2.2 Non-goals (explicit)

V1 will NOT include any of the following. They are deferred to later versions or rejected:

- A custom TUI dashboard (tmux's tiled layout + window switcher is the dashboard)
- Cross-machine state sync
- Agent-to-agent coordination, supervisor patterns, message passing
- PR / merge / rebase automation
- Crash recovery, autorestart, health monitoring
- Web dashboard or mobile companion
- Sandboxing (Docker, Lima, etc.)
- Auto-merge on CI pass
- Built-in token / cost tracking
- Hooks for `post_create`, `.env` copying, dependency installation
- Backends other than Claude Code and Ollama (no Codex, Aider, Gemini, Cursor)
- Mouse-driven UI, custom keybindings, status-line modifications, themes
- Any terminal feature beyond ANSI default (no truecolor requirement, no Sixel, no Kitty graphics)

If a capability isn't listed in §2.3, it doesn't ship in V1.

### 2.3 Commands

Four commands. No flags beyond `--help` and a single `--with` selector for backend choice.

| Command                                | Behavior                                                                                                                                                                                                                                              |
| -------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `mt new [--with claude\|ollama]`       | Pick a repo (fzf), prompt for branch name, create the worktree, split a new pane on the `mt:dashboard` window, and launch the chosen agent in it. Default backend taken from config. **Idempotent**: existing pane with same `(repo, branch)` → focus it. |
| `mt ls`                                | List all worktrees across tracked repos. Shows `(repo, branch, path, backend, pane_state)` where `pane_state ∈ {live, dead}`. Pipeable: `mt ls \| grep feat-` to filter.                                                                              |
| `mt rm [--force]`                      | Pick a worktree (fzf), remove it via `git worktree remove`, kill its tmux pane if present, re-tile the dashboard. **Refuses if the worktree has uncommitted changes** — propagates `git`'s warning. `--force` bypasses by passing `--force` through to `git worktree remove`. |
| `mt show`                              | Attach to (or create) the `mt:dashboard` window. **Autocreates the tmux session and window if missing** — works as the first command of the day even if `tmux` was killed overnight. No-op if already focused there.                                  |
| `mt switch [-z]`                       | fzf jump to any pane on the dashboard by `<repo>:<branch>` substring. `-z` zooms the chosen pane. Designed to be invoked from a `tmux display-popup` so it's reachable from inside an active agent. |
| `mt bind`                              | Install tmux keybindings (`prefix+g`, `prefix+G`, `prefix+N`, `prefix+R`) on the running tmux server, each wrapping an `mt` subcommand in `display-popup`. Non-persistent — print the lines to add to `~/.tmux.conf` for permanence. Requires tmux 3.2+. |

Bare `mt` with no subcommand is equivalent to `mt show`. This is the "one page, everything visible" entry point and the first action of every workday.

The keybinding layer added by `mt bind` is what makes multi-repo switching tractable from inside an active agent: tmux intercepts the prefix keystroke before the agent sees it, opens `mt switch` (or `mt new`/`mt rm`) in an overlay, and returns focus to the original pane (or the chosen one). Without this, switching between repos requires leaving the agent, which is the friction §1.6 promises to remove.

### 2.4 Domain model

```
Repo       = { Path: AbsPath, Name: String }
            -- Name is basename(Path); not stored, derived

BranchName = String (slugified; matches /^[a-z0-9][a-z0-9-]*$/)

Backend    = Claude | Ollama Model
            -- Ollama carries the model tag (e.g. "llama3:8b"); Claude does not.

Worktree   = { Repo: Repo, Branch: BranchName }
            -- Path = Repo.Path / ".worktrees" / Branch (derived, never stored)

PaneTitle  = Repo.Name + ":" + Branch  (pure function of inputs)
            -- Disambiguated with a short hash if two repos share a basename.

Session    = { Worktree: Worktree, Backend: Backend, Pane: PaneTitle }
            -- tmux pane id queried on demand by title; not in the model
```

Illegal states made unrepresentable:

- `Worktree.Path` cannot disagree with `(Repo.Path, Branch)` — it's a function of them, not a field.
- Two distinct `Session`s for the same `(Repo, Branch)` cannot exist — `PaneTitle` is deterministic, and `mt` checks for an existing pane with that title before splitting; the second `mt new` call focuses the existing pane.
- Raw user input never enters the model. `BranchName` is post-slugification at the boundary.
- A `Session` without a `Backend` cannot exist — backend is chosen at creation time, not later.

### 2.5 Topology: one window, many panes

There is **one** dashboard, identified by the tmux address `mt:dashboard`:

- `mt` is the tmux session name.
- `dashboard` is the only window in that session under V1.
- Each agent session lives in its own tiled pane.
- Layout is `tiled` by default (`tmux select-layout tiled` after every split / kill). Predictable, no manual layout management.
- Pane title is set to `PaneTitle` via `tmux select-pane -T`, visible in the pane border.

When the user wants to focus on one session, they zoom (`prefix + z`). When done, they unzoom. When they want to see everything, they look at the dashboard. There is no "list of windows to switch between" — that's the model `mt` rejects.

Practical pane-count ceiling: ~9 simultaneous live panes on a typical laptop screen before pane content becomes unreadable. Past that, zoom-then-cycle (`prefix + z`, `prefix + ;`) is the answer. V2 may introduce a second window as a spillover; V1 will not.

### 2.6 Configuration

Single file: `~/.config/mt/config.toml`. Empty file is valid (all fields default).

```toml
# directories to scan for git repos (depth ≤ 3)
repos_dirs = ["~/Code"]

# OR explicit list (overrides repos_dirs if set)
# repos = ["~/Code/soma", "~/Code/axon", "~/Code/quant"]

# tmux session holding the dashboard window
tmux_session = "mt"

# the single dashboard window name
tmux_window = "dashboard"

# prefix applied to created branches: <prefix>/<branch>
branch_prefix = "mt"

# subdirectory under each repo for worktrees
worktree_subdir = ".worktrees"

# default agent backend: "claude" or "ollama"
default_backend = "claude"

# default ollama model when backend is ollama
ollama_model = "llama3:8b"

# command templates — overridable, but defaults below "just work"
# {path} is the worktree directory; {model} is ollama_model
claude_cmd = "claude"
ollama_cmd = "ollama run {model}"

# When a new worktree contains an .envrc and direnv is on PATH, run
# `direnv allow` so the agent's pane doesn't see "blocked .envrc" warnings.
# Set to "false" if you point mt at freshly-cloned third-party repos —
# auto-allowing an untrusted .envrc is a code-execution risk.
auto_direnv_allow = "true"
```

### 2.7 Acceptance criteria

V1 is complete when, on a clean install:

1. `mt new` from invocation to agent prompt visible in a new pane: **≤ 5 seconds** wall-clock (excluding the agent's own startup time).
2. `mt new` invoked twice with the same `(repo, branch)` produces exactly one worktree, exactly one pane, and the second invocation focuses the existing pane.
3. After `tmux kill-server`, `mt new` recreates session and dashboard window correctly with no manual cleanup.
4. Ten `mt new --with claude` invocations across ten repos produce ten Claude Code panes on one dashboard window, **none of which triggers a reauth prompt**.
5. `mt new --with ollama` and `mt new --with claude` can coexist as panes on the same dashboard window without interfering.
6. `mt rm` removes the worktree, the branch (if no upstream), the tmux pane — no filesystem or tmux orphans — and re-applies `tiled` layout.
7. `pgrep mt` returns no PIDs ten seconds after any invocation completes.
8. `~/.claude/.credentials.json` is byte-identical before and after a `mt new --with claude` invocation. (Confirms §1.4.)
9. Works in Terminal.app default profile with no configuration changes (tmux 3.x, bash 3.2+, fzf, git ≥ 2.5). No truecolor, no Nerd Font, no special keybindings required.

### 2.8 Failure modes

V1 fails loudly and quickly. No retries, no fallbacks, no recovery logic.

| Condition                                     | Response                                                          | Exit code |
| --------------------------------------------- | ----------------------------------------------------------------- | --------- |
| Repo path doesn't exist                       | `repo not found: <path>`                                          | 1         |
| Branch name slugifies to empty                | `invalid branch name: <input>`                                    | 1         |
| `git worktree add` fails                      | propagate git's stderr verbatim                                   | git's     |
| tmux server unreachable & not autostartable   | `tmux unavailable; start with: tmux new -d -s mt`                 | 1         |
| `claude` not in PATH (when backend = claude)  | `claude not found; install: https://claude.com/claude-code`       | 1         |
| `ollama` not in PATH (when backend = ollama)  | `ollama not found; install: https://ollama.com`                   | 1         |
| Pane with same title already exists           | focus it (success path)                                           | 0         |
| Worktree path collision (different branch)    | `path exists: <path>` and abort                                   | 1         |
| Two repos share basename → pane title clash   | suffix shorter title with `-<sha8(repo_path)>` deterministically  | 0         |
| `mt new` invoked but no repos discovered      | `no repos found; configure repos_dirs in ~/.config/mt/config.toml` | 1         |
| `mt rm` on worktree with uncommitted changes (no `--force`) | propagate `git worktree remove`'s refusal verbatim, suggest `mt rm --force` | git's     |
| `mt show` invoked with no `mt:dashboard` window | create session `mt`, create window `dashboard`, attach            | 0         |

### 2.9 Implementation notes

- **Language**: Bash + fzf. Target ≤ 250 lines.
- **Why bash for V1**: every dependency (`git`, `tmux`, `fzf`, `bash`) is already on a developer Mac. No install step beyond sourcing one file. No version skew. If V1 outgrows bash, port to Go — the domain model in §2.4 translates one-to-one.
- **tmux primitives used**: `new-session -d`, `new-window`, `split-window -t`, `select-layout tiled`, `select-pane -T`, `display-message -p` (for queries), `kill-pane`, `switch-client`. Nothing more exotic than that.
- **Pane lookup**: list panes via `tmux list-panes -t mt:dashboard -F '#{pane_id} #{pane_title}'` and grep for the deterministic `PaneTitle`. No state file.
- **Testing**: bats for unit logic; a tmpfs fixture repo for integration tests covering the new/ls/rm cycle on both backends. Ollama tests use `ollama_cmd = "cat"` to avoid pulling models in CI.
- **No background daemons.** `mt` only runs synchronously in response to user invocation.
- **Working-set cost.** Each active Claude Code pane consumes roughly 200MB of RAM; nine simultaneous panes is ~1.8GB before the OS, browser, and editor. Comfortable on 16GB+, tight on 8GB Macbook Air. Document, don't enforce — let the user feel the ceiling.

### 2.10 Distribution

`mt` ships as a single shell script. Three install paths, in order of public-friendliness:

- **V1 — `curl` installer.** A short `install.sh` at the project's GitHub repo writes `mt.sh` to `~/.local/bin/mt`, makes it executable, and prints a one-line `source` instruction the user can add to their shell rc to enable bare `mt` (no subcommand) → `mt show` aliasing.
  ```sh
  curl -fsSL https://raw.githubusercontent.com/<user>/metatree/main/install.sh | bash
  ```
  Auditable: the install script is short enough to read before piping. No external dependencies fetched at install time.
- **V1.1 — Homebrew tap.** `brew tap <user>/mt && brew install mt`. Tap repo is a separate concern from the code repo; deferred until V1 has external users.
- **Personal path — source from rc.** For the author's own setup: `source ~/Code/metatree/mt.sh` from `.zshrc` / `.bashrc`. Skips the public install entirely, gives instant edits.

The `mt.sh` script itself is the deliverable; everything else is packaging. A V2 Go rewrite (see §2.11 #5) is gated only on bash exceeding 250 lines, not on distribution mechanics — `curl | bash` and `brew install` work for either.

### 2.11 V2 backlog (not commitments)

In rough priority order:

1. Persistent worktree registry across machines, synced as a flat file (Tailscale + Syncthing or dotfiles repo).
2. Spillover window when pane count exceeds N.
3. Detection of stale worktrees (upstream branch merged or deleted, no commits in N days).
4. `post_create` hook for `.env` copy / `npm install` / `mise install`.
5. Go rewrite if `mt.sh` crosses 250 lines.
6. Per-pane status decoration (idle / running / awaiting input) — only if expressible via `pane_title` updates the agent itself emits. No polling.

### 2.12 Out of scope, permanently

These are explicitly rejected, not deferred:

- Wrapping Claude Code as a subprocess. Violates §1.4.
- Reading or modifying `~/.claude/.credentials.json`. Violates §1.4.
- Setting `ANTHROPIC_API_KEY` or `ANTHROPIC_TOKEN` for the spawned session. Violates §1.4.
- Any feature requiring a long-lived `mt` daemon. Violates §1.5.
- Replacing tmux with a custom TUI. Violates §1.2.
- Requiring a non-default terminal (Kitty, WezTerm, Ghostty) or specific font. Violates §1.2.

---

## 3. Open questions

To resolve before implementation:

1. **Branch prefix default.** `mt/<name>` is safe but verbose in `git branch` listings. Alternatives: no prefix (collisions with user branches), `agent/<name>`, user-configurable only.
2. **Worktree location.** `<repo>/.worktrees/<branch>` keeps everything local but pollutes the repo dir. Alternative: `~/.local/share/mt/worktrees/<repo>/<branch>` — cleaner, but `cd ..` from a worktree no longer lands in the repo. Lean toward in-repo for V1.
3. **Pane title visibility.** Tmux defaults hide pane borders' titles unless `pane-border-status` is set. Two options: (a) `mt` sets `pane-border-status top` on the dashboard window only, scoped via `set-window-option`, leaving global tmux config untouched; (b) document the user-side opt-in. Option (a) is preferred — non-invasive and self-contained.
4. **Ollama model selection per session.** V1 reads `ollama_model` from config and uses it for all Ollama panes. Alternative: prompt for model on `mt new --with ollama`. Lean toward config-only for V1; prompt is a V2 nicety.
5. **Pane death policy.** When the agent exits (intentionally or via crash), the pane goes blank or shows a shell. Options: (a) close pane automatically and re-tile; (b) leave it as a dead pane until `mt rm`. Option (b) is safer — closing on exit could destroy unsaved scrollback. Document the choice.
6. **Pane state signaling (idle / awaiting / running).** §1.6's daily flow assumes Jordan can tell at a glance which pane is waiting for her — colored border, status icon, something. Currently in §2.11 V2 backlog #6 because doing it without a polling daemon requires the agent itself to emit pane-title or border updates (OSC 0/2 sequences, or `printf '\033]2;...\007'`). Open question: does Claude Code already emit these on prompt? Does Ollama? If yes, V1 can read them for free and §2.11 #6 promotes to V1.5 with a one-line spec change. If no, this stays V2. Worth ten minutes of empirical testing before locking the answer.
