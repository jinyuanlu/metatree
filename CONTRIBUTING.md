# Contributing to `mt`

> What you need to know to land code in `mt` — package layout, error
> handling, testing tiers, distribution, anti-patterns. Companion to
> [`spec.md`](spec.md), which defines the **product** (commands, behaviors,
> acceptance criteria, auth invariant). The product spec is the source of
> truth for behavior; this document is the source of truth for how the Go
> implementation realizes that behavior. If the two disagree, `spec.md` wins.

---

## 1. Why Go

`mt.sh` accumulated bugs that were exclusively language-shape: `set -e`
interactions with pipefail and SIGPIPE, `local` lifetime under EXIT traps,
ad-hoc TOML parsing, IFS edge cases, macOS path symlink mismatches under
`pwd -P`. None of these are interesting product bugs. They are the language
fighting the implementer.

Go is the spec's documented escape hatch (see `spec.md` §2.11 V2 backlog #5).
The bash prototype proved the design; the Go port hardens the implementation.

### Goals of the port

1. **Same product.** Every command, flag, behavior, and exit code in `spec.md`
   is preserved. `tests/smoke.sh` is the executable specification — the Go
   binary must pass all 19 sections without modification to the test.
2. **Same UX.** Same install URL, same config file, same tmux interactions.
   Existing `mt bind`, `mt diagnose` outputs stay structurally identical.
3. **Better internals.** Typed errors, explicit boundaries, table-driven
   tests, no shell-level surprises.
4. **Same audit posture.** The repository stays short enough to read in
   one sitting; new contributors should be able to make a typo fix without
   reading the whole tree.
5. **Same distribution speed.** `curl | bash` install completes in under
   two seconds, fetching a single static binary from GitHub Releases.

### Non-goals

- TUI framework, custom rendering, or any escape from spec.md §1.2 ("tmux is
  the entire UI"). Same auth invariant from §1.4 — Go implementation does
  not change the credentials posture.
- Bug-for-bug compatibility with bash mt.sh at runtime. The Go binary is
  the production implementation that users install; `mt.sh` is maintained
  in lockstep as the bash reference implementation, at the behavior level
  (not bug-for-bug) for spec parity and cross-implementation regression
  testing via the bash smoke suite.
- Any new product feature. The port ships at strict feature parity with the
  bash version. New features go in v2.x after the port lands.

---

## 2. Reading order

If you are new to the codebase, read in this order:

1. [`spec.md`](spec.md) — the product. Section §1.6 (daily workflow) is the
   user model. Sections §2.3 (commands) and §2.4 (domain model) are the
   reference for behaviors and types.
2. This document — the architecture.
3. `cmd/mt/main.go` — the entry point. Dispatches to one function per
   subcommand. Reading it gives you the surface at a glance.
4. `internal/command/<name>.go` — pick any one. Each file is a single
   subcommand and reads top-to-bottom.

You should be able to add a new subcommand after reading these four things.

---

## 3. Layering

Three layers, dependency arrow points down. **No upward calls.**

```
                    ┌─────────────────────────────┐
                    │         cmd/mt              │   entry point
                    │  argv → command → exit code │
                    └──────────────┬──────────────┘
                                   ▼
            ┌──────────────────────┴──────────────────────┐
            │              internal/command               │   subcommand impls
            │  Run(env *Env, args []string) error          │   grouped 3 files
            │   ls │ new │ rm │ switch │ prune │ bind ...   │
            └────────────────┬─────────────┬────────────────┘
                             ▼             ▼
       ┌─────────────────────┴───┐   ┌─────┴────────────────────┐
       │   internal/dashboard    │   │       internal/config    │
       │  pane registry, chrome, │   │  TOML parsing, defaults  │
       │  binding installation   │   │                          │
       └────────┬────────┬───────┘   └──────────────────────────┘
                ▼        ▼
         ┌──────┴──┐ ┌───┴────────┐
         │ tmuxio  │ │  gitio     │   external I/O wrappers
         │ shell-  │ │  shell-out │   typed return values,
         │ out to  │ │  to git    │   no string parsing in
         │ tmux    │ │            │   layers above
         └─────────┘ └────────────┘
                              │
                       ┌──────┴───────┐
                       │   mtlog      │   invocation log
                       │              │   write-only, best-effort
                       └──────────────┘
```

**Invariants of this layering:**

- `tmuxio` and `gitio` are the *only* packages that call `os/exec`.
  Commands never invoke shell directly.
- `dashboard` is the *only* package that knows about `@mt-managed`,
  pane-border-format, status-right format, and the prefix-key bindings.
- `command/*.go` files orchestrate; they hold no business logic that can be
  unit-tested independently.
- `config` has no dependencies. `mtlog` has only `os` and `time`. Both can be
  imported by anything.

If a contributor finds themselves wanting `command/` to call `tmuxio`
directly, that's a signal to extract a method on `dashboard` instead. The
test suite enforces this with `go vet` style import-graph assertions.

---

## 4. Package layout

```
metatree/
├── cmd/
│   └── mt/
│       └── main.go                  # argv parse, dispatch, exit codes
├── internal/
│   ├── config/
│   │   ├── config.go                # TOML schema, Load(), Default()
│   │   └── config_test.go
│   ├── mtlog/
│   │   ├── mtlog.go                 # Path(), Write(), Tail(n)
│   │   └── mtlog_test.go
│   ├── tmuxio/
│   │   ├── tmuxio.go                # tmux command wrappers
│   │   └── tmuxio_test.go           # uses real tmux fixture
│   ├── gitio/
│   │   ├── gitio.go                 # worktree discovery, parent_repo_of
│   │   └── gitio_test.go
│   ├── dashboard/
│   │   ├── dashboard.go             # ensure, chrome, bindings
│   │   ├── pane.go                  # @mt-managed registry
│   │   └── dashboard_test.go
│   └── command/
│       ├── env.go                   # Env type passed to every Run
│       ├── lifecycle.go             # new, rm, prune (worktree state changes)
│       ├── navigation.go            # switch, show, ls (read or focus existing)
│       └── meta.go                  # bind, diagnose, help (about mt itself)
├── tests/
│   ├── smoke.sh                     # carried over from bash, runs against Go binary
│   ├── mt.bats                      # carried over, optional
│   ├── bats/                        # bash-only behaviors (e.g. copy_runtime_files)
│   └── integration_test.go          # Go-level integration tests (real tmux)
├── .github/
│   └── workflows/
│       └── release.yml              # goreleaser on tag
├── .goreleaser.yaml
├── go.mod
├── go.sum
├── install.sh                       # rewritten to fetch binary
├── mt.sh                            # bash reference impl, maintained in lockstep
├── spec.md                          # product spec
├── CONTRIBUTING.md                  # this file
├── Justfile
├── LICENSE
└── README.md
```

### Package responsibilities

| Package      | Job                                                                  |
| ------------ | -------------------------------------------------------------------- |
| `cmd/mt`     | argv → subcommand dispatch → exit code; nothing else                 |
| `config`     | TOML schema, defaults, load (single source of truth for config)      |
| `mtlog`      | append-only invocation log; best-effort, never fails the caller      |
| `tmuxio`     | one wrapper per tmux verb mt uses; only package that calls tmux      |
| `gitio`      | worktree discovery, parent_repo_of, git-crypt setup; only git caller |
| `dashboard`  | chrome, popup bindings, pane registry (`@mt-managed`)                |
| `command/`   | three files (lifecycle, navigation, meta); orchestrate, no I/O       |

If a package starts doing the job of another, split it — don't add
indirection. Boundary tests in CI assert the import graph stays clean.

---

## 5. Domain model

The product spec's domain model (`spec.md` §2.4) realized as Go types.

```go
package mt    // top-level type names live here, in cmd/mt or in a tiny
              // shared package; they are *value* types, no behavior.

type Backend int
const (
    BackendClaude Backend = iota
    BackendOllama
)

type Repo struct {
    Path string  // absolute, symlink-resolved (filepath.EvalSymlinks)
}

func (r Repo) Name() string { return filepath.Base(r.Path) }

type Branch string  // post-slugified, matches /^[a-z0-9][a-z0-9-]*$/

type Worktree struct {
    Repo   Repo
    Branch Branch
    Path   string  // canonical worktree directory; not always Repo.Path/.worktrees/Branch
}

func (w Worktree) PaneTitle() string {
    return w.Repo.Name() + ":" + string(w.Branch)
}

type Session struct {
    Worktree Worktree
    Backend  Backend
    PaneID   string  // tmux pane id, e.g. "%42". Empty when dead.
}

func (s Session) Live() bool { return s.PaneID != "" }
```

### Illegal states made unrepresentable (mirrors spec.md §2.4)

- `Branch` is its own type; raw user input is only ever converted via
  `branch.Slugify(input) (Branch, error)`. If the input slugifies to empty,
  the function returns an error — no empty Branches enter the system.
- `PaneTitle()` is a method, not a field. The visible pane border title may
  drift (Claude emits OSC 2), but `PaneTitle()` always returns the canonical
  identity. The pane registry stores this in `@mt-managed`, the source of
  truth for "is this an mt-managed pane and what is it called."
- `Backend` is an enum, not a string. Invalid backends fail at parse time.
- `Worktree.Path` defaults to `Repo.Path/.worktrees/Branch` for `mt new`-created
  worktrees but is always read from `git worktree list` for discovery. Paths
  from disk go through `filepath.EvalSymlinks` so all comparisons happen in
  the same canonical form (no `/tmp` vs `/private/tmp` mismatches).

---

## 6. Errors and exit codes

Bash uses `die "msg"; exit $?` and propagates whatever the last command
returned. Go uses typed errors with explicit exit codes.

```go
package command

// ExitError carries an exit code through error returns. main.go uses
// errors.As to recover the code; bare errors map to exit 1.
type ExitError struct {
    Code int
    Err  error
}

func (e *ExitError) Error() string { return e.Err.Error() }
func (e *ExitError) Unwrap() error { return e.Err }

// Helpers
func ExitWith(code int, format string, args ...any) error {
    return &ExitError{Code: code, Err: fmt.Errorf(format, args...)}
}
```

### Standard exit codes

Mirror the bash spec.md §2.8 failure-modes table.

| Code | Meaning                                              | Used by                          |
| ---- | ---------------------------------------------------- | -------------------------------- |
| 0    | success                                              | every command on the happy path  |
| 1    | generic mt error (config invalid, no repos, ...)     | most user-actionable failures    |
| 2    | usage error (unknown subcommand, bad flags)          | `cmd/mt/main.go` argv parsing    |
| —    | propagated from `git`                                | git-level failures               |

### Error wrapping

```go
return fmt.Errorf("worktree add: %w", err)
```

Every error crossing a package boundary is wrapped with one verb of context
("worktree add", "tmux split", "config parse"). The chain of wrapping forms
a stack trace from the user-visible message to the root cause.

main.go formats the chain as:
```
mt: worktree add: external filter 'git-crypt smudge' failed: exit status 1
```

### No bare panics in product code

`panic` is reserved for genuinely impossible states (e.g., a switch on
exhaustive enum where the default branch should be unreachable). Every
exit-able failure is an `error` return.

---

## 7. Configuration

Same TOML schema as bash spec §2.6 — schema is a contract, untouched.

```go
package config

type Config struct {
    ReposDirs         []string `toml:"repos_dirs"`
    Repos             []string `toml:"repos"`
    TmuxSession       string   `toml:"tmux_session"`
    TmuxWindow        string   `toml:"tmux_window"`
    BranchPrefix      string   `toml:"branch_prefix"`
    WorktreeSubdir    string   `toml:"worktree_subdir"`
    DefaultBackend    string   `toml:"default_backend"`
    OllamaModel       string   `toml:"ollama_model"`
    ClaudeCmd         string   `toml:"claude_cmd"`
    OllamaCmd         string   `toml:"ollama_cmd"`
    WorktreeCopyFiles []string `toml:"worktree_copy_files"`
    WorktreeBase      string   `toml:"worktree_base"`
    AutoDirenvAllow   bool     `toml:"auto_direnv_allow"`
    AutoStatusChrome  bool     `toml:"auto_status_chrome"`

    Path string `toml:"-"`  // resolved config path (informational)
}

func Default() *Config       // returns config with all defaults populated
func Load() (*Config, error) // overlays file (if any) onto Default; runs
                              // validateWorktreeCopyFiles before returning
```

- Bool fields accept TOML strings (`"true"`/`"false"`) for backwards-compat
  with the bash version's quoted booleans, AND TOML native booleans (`true`/`false`).
  `BurntSushi/toml` handles native; we add a custom unmarshal hook for the
  string form. Test the hybrid in `config_test.go`.
- A missing config file is not an error — defaults win.
- `MT_CONFIG` env var overrides the path. Same behavior as bash.

### 7.1 `WorktreeCopyFiles` — gitignored runtime file copy

Mirrors `spec.md` §2.6.2. Default: `[".env", ".envrc", ".npmrc"]`. The list tells `mt new` which gitignored runtime files to copy from the parent repo into each fresh worktree so the worktree runs out of the box without manual setup.

Validation lives in `validateWorktreeCopyFiles` (called from `Load` after TOML decode). Each entry must be non-empty post-`TrimSpace`, must not contain `/` or `..`, and must not be absolute (`filepath.IsAbs == false`). Duplicates are silently dropped, first-occurrence order preserved. The list is normative at runtime: nothing further is parsed or interpreted, the strings are joined directly to `Repo.Path` and to the destination worktree directory.

Runtime contract (executed in the `mt new` path after `git worktree add` and before the agent launches):

- Source missing → silent skip. Source-is-symlink → resolved via `cp -L` semantics. Source-is-git-crypt-encrypted → skip, recorded for the summary.
- Destination exists → skip, recorded. The copy never overwrites a hand-edited file.
- Each write is `<dst>.tmp` followed by `os.Rename` so an interrupted `mt new` leaves no half-file.
- Per-file errors are non-fatal. The batch continues, errors are accumulated into a single `mt: copy errors: …` line on stderr. The happy path is silent or one line; see `spec.md` §2.6.2 for the exact stderr idiom.

Empty list (`worktree_copy_files = []`) is honored as "feature disabled" — distinct from an omitted key, which falls through to the default. Subdirectory paths, shell hooks, and post-create commands are deferred to v2 (`TODOS.md`, `spec.md` §2.11 V2 backlog #4).

### 7.2 `WorktreeBase`: remote-default-branch base

Mirrors `spec.md` §2.6.3. The `Config.WorktreeBase` field (TOML string, default `"origin-default"`) controls which ref each `mt new`-created branch is rooted at. See the product spec for the user-facing contract; this section covers the Go-side primitives that realize it.

Three primitives live in `internal/gitio`:

```go
package gitio

// DefaultBranch resolves origin's default branch using
//   git symbolic-ref refs/remotes/origin/HEAD
// then probing origin/main, origin/master, origin/develop, origin/trunk
// in order. Returns the bare branch name (e.g. "main"), without the
// "origin/" prefix. Errors if no candidate resolves AND symbolic-ref
// is unset, the caller treats that as the no-recognizable-default
// hard error spelled out in spec.md §2.6.3.
func DefaultBranch(repo string) (string, error)

// FetchBranch runs `git fetch origin <branch>` with GIT_TERMINAL_PROMPT=0
// and the supplied timeout. Returns a non-nil error on any non-success
// (network error, auth required, timeout, branch missing upstream); the
// caller chooses which fallback leg to take.
func FetchBranch(repo, branch string, timeout time.Duration) error

// ResolveWorktreeBase encapsulates the full spec.md §2.6.3 decision tree.
// cfgValue is the post-MT_BASE-override config value ("origin-default",
// "head", or a literal ref). The returned ResolveResult names the ref
// actually used, the resolved SHA, and a fallback reason (empty on the
// happy path).
func ResolveWorktreeBase(repo, cfgValue string, timeout time.Duration) (ResolveResult, error)

type ResolveResult struct {
    Ref          string // e.g. "origin/main", "main", "HEAD", or a user-supplied literal
    SHA          string // full 40-char OID; Format() truncates to 8 for display
    FallbackNote string // "", "stale, fetch failed", "no origin/main, used local",
                        // "fetch failed, no local main", etc.
}

// Format builds the §2.6.3 stderr line:
//   mt: branched <branchName> from <Ref>@<sha8>[ (<FallbackNote>)]
func (r ResolveResult) Format(branchName string) string
```

The existing `WorktreeAdd` signature is extended with an explicit start-point argument so the resolved ref is plumbed through instead of relying on the parent repo's HEAD:

```go
// WorktreeAdd creates a worktree branched from startPoint. Empty
// startPoint preserves the legacy semantics (branch from current HEAD)
// and is what the "head" mode uses. Equivalent to:
//   git worktree add -b <branchName> <path> <startPoint>
func WorktreeAdd(repo, branchName, path, startPoint string) error
```

`internal/command/lifecycle.go` (`RunNew`) becomes the single caller:

1. Read `cfg.WorktreeBase`, override with `MT_BASE` if set.
2. If the effective value is `"origin-default"` and the repo has no `origin` remote, emit the hard error from spec.md §2.6.3 and exit 1 before any worktree work.
3. Call `gitio.ResolveWorktreeBase` to pick the ref + take any fallback.
4. Call `gitio.WorktreeAdd(repo, branch, path, result.Ref)`.
5. Print `result.Format(branchName)` to stderr.
6. Continue with `worktree_copy_files`, agent launch, etc.

Test coverage (Tier 1 + Tier 2):

- Unit tests for `DefaultBranch` against a fixture repo with each of the four probe names; assert symbolic-ref wins over probe order.
- Unit tests for `ResolveWorktreeBase` exercising every leg: happy fetch, stale-ref fallback, local-branch fallback, parent-HEAD fallback, literal ref pass-through, `"head"` short-circuit.
- Integration test asserting the stderr line is emitted on every `mt new` and matches one of the four documented format variants.
- Integration test asserting `MT_BASE=head` overrides the config for a single invocation.
- Integration test asserting the no-`origin` hard-error exits 1 with the documented remediation message.

The auth invariant (§1.4) is unaffected: `ResolveWorktreeBase` runs synchronously inside the `mt` process, before any `tmux split-window`. `mt` exits long before any agent launches.

### Why we use `BurntSushi/toml`

It's the standard Go TOML library, mature, widely audited, and `go mod tidy`
adds it as the only non-stdlib dependency. We don't write our own parser
this time.

---

## 8. Invocation log

Same on-disk format as bash:
```
[2026-05-10T15:24:33Z] pid=12345 INVOKE cmd=switch args=switch -z tmux=mt:dashboard in_tmux=yes cwd=/...
[2026-05-10T15:24:34Z] pid=12345 EXIT cmd=switch rc=0
```

```go
package mtlog

func Path() string                // ~/.local/state/mt/mt.log or $MT_LOG
func Write(line string)           // append, best-effort, no error returned
func Tail(n int) []string         // last n lines, used by mt diagnose
```

In Go, the bash EXIT trap becomes `defer mtlog.Write("EXIT ...")` in main.
No globals required — the deferred closure captures the cmd name from the
local scope cleanly. (One of several places where Go just sidesteps a bash
quirk we hit.)

---

## 9. Tmux interaction (`tmuxio`)

One Go function per tmux verb mt uses. Each function:

1. Builds an `*exec.Cmd` with explicit args (no shell quoting).
2. Returns typed values: structs, slices, IDs as named string types.
3. Returns `error` (wrapped with what we tried) instead of swallowing.
4. **Never** parses pane_title where mt-identity matters. Always reads
   `@mt-managed` for that — it's the source of truth (see §10).

Public API (representative):

```go
package tmuxio

type PaneID string
type SessionName string
type WindowName string

type Pane struct {
    ID         PaneID
    MtManaged  string  // @mt-managed user option, "" if unset
    CurrentCmd string  // pane_current_command
    Active     bool
}

func ServerRunning() bool
func HasSession(SessionName) bool
func InsideTmux() bool                       // $TMUX is set
func CurrentSession() (SessionName, error)   // when InsideTmux()
func CurrentWindow() (WindowName, error)

func EnsureSession(SessionName, WindowName) error
func EnsureWindow(SessionName, WindowName) error

func ListPanes(target string) ([]Pane, error)
func SplitPane(target, cwd, cmd string) (PaneID, error)
func SendKeys(PaneID, keys string) error
func SetPaneTitle(PaneID, title string) error
func SetPaneOption(PaneID, key, value string) error  // sets @mt-managed
func SelectPane(PaneID) error
func ZoomPane(PaneID) error
func KillPane(PaneID) error

func SetWindowOption(target, key, value string) error
func SetSessionOption(SessionName, key, value string) error
func TileLayout(target string) error

func ListPrefixKeys() ([]string, error)
func BindPrefix(key, command string) error

func DisplayPopup(width, height, command string) error  // for mt bind
func AttachOrSwitch(target string) error
```

Errors include the args used:
```go
return fmt.Errorf("tmux split-window -t %s: %w", target, err)
```

---

## 10. Pane registry (`dashboard.Pane`)

The single biggest bash bug we shipped was using `pane_title` to detect
mt-managed panes. Claude emits OSC 2 to set the title to its cwd, clobbering
ours. Fix in bash: a tmux per-pane user option `@mt-managed` set on every
pane mt creates. The Go port preserves this exactly.

```go
package dashboard

// Mark a fresh pane as mt-managed.
func MarkPane(id tmuxio.PaneID, title string) error

// Find the pane with the given mt-identity, or "" if none.
func FindPane(target string, title string) (tmuxio.PaneID, error)

// All mt-managed panes on the dashboard.
func ListManaged(target string) ([]tmuxio.Pane, error)
```

`@mt-managed` is the **only** string mt looks at to make decisions about
panes. `pane_title` is purely informational (and visible to the user) —
it can change at any time and that's fine.

The bare-shell pane that `EnsureSession` creates with the dashboard is
intentionally **never** marked `@mt-managed`. It is the dashboard's
anchor — present so the tmux server survives when every agent pane
exits. Picker and listing code already filter on `@mt-managed != ""`,
so the anchor is invisible to `mt ls` / `mt switch`.

`dashboard.Ensure` (via `ensureAnchor`) makes the anchor a **load-bearing
invariant**, not a convention: on every `mt` invocation it lists panes
on the window and, if every pane is `@mt-managed`, splits a fresh
unmarked pane to restore the anchor. So even if the user kills the
anchor with `tmux kill-pane` or `exit`s it, the next `mt` call brings
it back before doing any other work.

---

## 11. Worktree discovery (`gitio`)

```go
package gitio

type Worktree struct {
    Repo string  // canonical (EvalSymlinks) parent repo path
    Path string  // canonical worktree path
}

// All worktrees git knows about across the configured repos. Catches
// .worktrees/<branch> (mt convention) AND .claude/worktrees/<task>
// (Claude --worktree convention) AND any others the user creates manually.
// The main worktree (the repo itself) is always excluded.
func DiscoverWorktrees(reposDirs, explicitRepos []string) ([]Worktree, error)

// Find the parent repo for any worktree path. Robust against nested
// conventions (.claude/worktrees/X is two levels deep, not one).
func ParentRepoOf(worktreePath string) (string, error)

// Wrappers for the two destructive ops mt issues.
func WorktreeAddNoCheckout(repo, branchName, path string) error
func WorktreeAdd(repo, branchName, path string) error
func WorktreeRemove(repo, path string, force bool) error
func DeleteBranchIfNoUpstream(repo, branchName string) error
func GitCryptInUse(repo string) bool
func InstallGitCryptKey(repo, worktreePath string) error  // pre-checkout key copy
```

All paths returned by gitio are passed through `filepath.EvalSymlinks` so
comparison with config paths (which are typically not symlink-resolved) works
consistently. This was a recurring source of bash bugs.

---

## 12. Dashboard chrome and bindings

```go
package dashboard

// Ensure the session+window exist. Idempotent. Configures pane-border-format
// and status-right (scoped to mt's session/window only). Auto-installs popup
// keybindings if not present on this tmux server. Prints the "installed
// bindings" notice on first install per server (to stderr).
func Ensure(cfg *config.Config) error

// Install the four popup bindings on the running tmux server.
// Idempotent — re-running is cheap.
func InstallBindings() error

// Chrome (pane border format + status-right) — applied on Ensure unless
// cfg.AutoStatusChrome is false.
func ApplyChrome(target tmuxio.SessionName, window tmuxio.WindowName) error
```

`Ensure` is what every command calls before doing anything dashboard-related.
It's the one place that knows the full setup recipe.

---

## 13. Command contract

Every subcommand is a single file `internal/command/<name>.go` exporting:

```go
package command

// Env carries everything a command needs. Built once in main.go.
type Env struct {
    Config       *config.Config
    InsideTmux   bool
    SessionName  tmuxio.SessionName  // overridden from current tmux when inside
    WindowName   tmuxio.WindowName
    Stdout       io.Writer
    Stderr       io.Writer
    Stdin        io.Reader  // for cmd_new branch prompt
}

// Each subcommand exposes one function.
func RunNew(env *Env, args []string) error
func RunLs(env *Env, args []string) error
// ... and so on
```

main.go:

```go
func main() {
    env, err := buildEnv()
    if err != nil { fail(err, 1) }

    cmdName, args := parseArgv(os.Args[1:])
    mtlog.Write(invokeLine(env, cmdName, args))
    defer func() { mtlog.Write(exitLine(cmdName)) }()

    err = dispatch(env, cmdName, args)
    if err != nil { fail(err, exitCodeOf(err)) }
}
```

### Agent launch wrapping (spec.md §2.6.1)

`RunNew` (`internal/command/lifecycle.go`) creates every agent pane the
same way: `tmuxio.SplitPane` with the cmd run through `wrapAgentCmd`
(`internal/command/launch.go`). For recognized shells (bash/zsh/fish)
the wrap yields `$SHELL -ic '<cmd>'`; for unrecognized shells the cmd
is passed through unwrapped and `RunNew` MUST emit a one-line stderr
warning pointing at `claude_cmd` in `~/.config/mt/config.toml`.

The bare-shell pane created by `EnsureSession` is the **dashboard
anchor** — unmarked, persistent, and never repurposed for an agent.
Keeping it alive is what prevents the
only-pane-in-only-session-in-only-server collapse cascade when every
agent exits. `dashboard.Ensure` enforces this on every invocation via
`ensureAnchor` (see §10), so a user `exit`ing the anchor pane is
self-healing on the next `mt` call.

**Auto-resume on revive.** When the worktree already exists (the user
picked a `[dead]` entry in `mt switch` or re-ran `mt new`) and the
backend is `claude`, `RunNew` appends ` --continue` to the agent cmd
*if* Claude has a saved session for that worktree path (any `.jsonl`
under `~/.claude/projects/<encoded>/`, where the encoding maps `/`
and `.` to `-`; see `claude_resume.go`). The append happens before
`wrapAgentCmd`, so an aliased `claude` still expands and picks up the
flag. No saved session ⇒ start fresh, no flag injected.

**Do not prefix the inner command with `exec`.** Bash and zsh treat
`exec <name>` as a special-builtin form that bypasses alias expansion
on its argument — using it silently defeats the entire contract. The
`-c` shell exits with the inner command's status when it finishes, so
the pane closes correctly without `exec`.

The wrap construction lives in `command/launch.go`, not `tmuxio/`,
because `tmuxio` is forbidden from knowing what shell command it
invokes (anti-pattern §17.1). `tmuxio.SplitPane` keeps its opaque-cmd
contract — `command/` decides what goes in.

### Adding a new subcommand

1. Add `func Run<Name>(env *Env, args []string) error` to the appropriate
   file in `internal/command/`:
   - `lifecycle.go` for state-changing commands (new, rm, prune-style)
   - `navigation.go` for read/focus commands (switch, ls, show-style)
   - `meta.go` for tool-introspection commands (bind, diagnose, help-style)
2. Add a case to `dispatch()` in `cmd/mt/main.go`.
3. Add the usage line to the help text in `meta.go`.
4. Add an integration test in `tests/integration_test.go`.
5. Add a smoke section to `tests/smoke.sh` (or assert no regression in
   existing sections).

Three command files instead of nine — each groups commands by purpose.
Easier to navigate than per-command files; each function is still its own
unit, just sitting next to its purpose-mates.

---

## 14. Testing strategy

Three tiers, mirroring `tests/smoke.sh` from the bash version.

### Tier 1 — unit tests (Go)

`*_test.go` next to every package.

- **Pure helpers**: slugify, parseBoolDefault, format builders. Table-driven.
- **`config`**: TOML edge cases (empty, comments, mixed types, bool string forms).
- **`mtlog`**: write+tail roundtrip, missing dir creation, $MT_LOG override.
- **`gitio.ParentRepoOf`**: feed a real fixture (created in `t.TempDir()`),
  assert correct parent for both mt-style and claude-style worktrees.

Target: ~80% coverage on packages that don't shell out.

### Tier 2.5 — Named integration tests (commit-1 checklist)

Each row commits to a named test in `tests/integration_test.go`. The
implementer doesn't decide what to write; they decide when to write it.

| Test name                          | Asserts                                                | Smoke parallel       |
|------------------------------------|--------------------------------------------------------|----------------------|
| `TestNewCreatesWorktreeAndPane`    | happy path: worktree + branch + pane + @mt-managed     | §3                   |
| `TestNewIdempotent`                | second `mt new` on same (repo, branch) focuses existing | §5                   |
| `TestNewClaudeStyleWorktreeRevive` | discovers `.claude/worktrees/<task>` via git           | (new — gap-fill)     |
| `TestNewInGitCryptRepo`            | --no-checkout + key copy + checkout cycle              | §14                  |
| `TestNewOSCClobberSurvival`        | pane_title hijacked → next `new` still splits          | §18                  |
| `TestRmCleanWorktree`              | removes worktree + branch + pane                       | §7                   |
| `TestRmRefusesDirty`               | dirty worktree → refuses without --force               | §8                   |
| `TestRmForceBypass`                | `--force` removes dirty worktree                       | §9                   |
| `TestPruneAllDead`                 | dead-only sweep, live preserved                        | §15                  |
| `TestBuildSwitchRows_*`            | picker rows: live → dead → "+Create" (low-noise order) | §16                  |
| `TestSwitchEmptyShowsCreate`       | empty dashboard → "+Create" entry only                 | (new — gap-fill)     |
| `TestSwitchAutoDetectsSession`     | `$TMUX` set → uses calling session, not config         | (regression case)    |
| `TestPathSymlinkResolution`        | macOS /tmp ↔ /private/tmp paths compare correctly      | (regression case)    |
| `TestBindInstallsAllFour`          | g/G/N/R bound, all using absolute mt path              | §12                  |
| `TestShowAutoCreatesAndBinds`      | fresh server → session + bindings + attach in one shot | §1, §17              |
| `TestDiagnosePrintsAllSections`    | VERSIONS / CONFIG / TMUX / KEYBINDINGS / LOG sections  | §17 partial          |

Each commit lands its slice of this table alongside the feature code.
Implementer doesn't decide what to write; they decide when to write it.

### Tier 2 — integration tests (Go, real tmux + git)

`tests/integration_test.go`. Use `t.TempDir()` for fixture isolation, real
`tmux` with a short `TMUX_TMPDIR` (the bash smoke test discovered the macOS
104-char socket limit; the Go test inherits that fix).

Examples:

- `TestNewCreatesWorktreeAndPane` — real fixture repo, run `mt new`, assert
  worktree+branch+pane exist and the pane has `@mt-managed` set.
- `TestIntegration_RespawnExistingWorktreeSkipsBranchedFromLine` — create
  a worktree, kill the tmux server, run `mt new` against the same branch
  (the dispatch path used by `mt switch`'s `[dead]` rows). Assert stderr
  carries `mt: resuming ...` and does NOT claim it branched anything.
- `TestPrefixGBindingFromInsidePane` — use `tmux send-keys` to simulate
  `prefix + g`, assert the popup launched and the right pane is focused.

### Tier 3 — smoke (carried over from bash)

`tests/smoke.sh` is preserved as-is. It was the executable specification of
the bash implementation; it becomes the executable specification of the Go
port. The 19 sections are unchanged. The Go binary either passes them or
the port isn't done.

`Justfile` gains:
```
test:           cargo-style — run all three tiers
test-unit:      go test ./...
test-integ:     go test -tags=integration ./tests/...
test-smoke:     bash tests/smoke.sh   # runs against MT=./mt-go after build
```

---

## 15. Distribution

Same `curl | bash` UX as the bash version; the install script fetches a
prebuilt binary instead of a shell script.

### `install.sh`

```sh
#!/usr/bin/env bash
set -euo pipefail
DEST="${MT_INSTALL_DIR:-$HOME/.local/bin}"
case "$(uname -s)-$(uname -m)" in
  Darwin-arm64)   ASSET=mt-darwin-arm64  ;;
  Darwin-x86_64)  ASSET=mt-darwin-amd64  ;;
  Linux-x86_64)   ASSET=mt-linux-amd64   ;;
  Linux-aarch64)  ASSET=mt-linux-arm64   ;;
  *) echo "unsupported: $(uname -s)-$(uname -m)" >&2; exit 1 ;;
esac
URL="https://github.com/jinyuanlu/metatree/releases/latest/download/${ASSET}.tar.gz"
mkdir -p "$DEST"
curl -fsSL "$URL" | tar -xz -C "$DEST"
chmod +x "$DEST/mt"
echo "installed: $DEST/mt"
```

Audit-friendly: short enough to read before piping into bash.

### `.goreleaser.yaml`

```yaml
project_name: mt
builds:
  - main: ./cmd/mt
    binary: mt
    goos:    [darwin, linux]
    goarch:  [amd64, arm64]
    flags:   ["-trimpath"]
    ldflags: ["-s -w -X main.version={{.Version}}"]
archives:
  - name_template: "mt-{{.Os}}-{{.Arch}}"
    format: tar.gz
checksum:
  name_template: "checksums.txt"
release:
  prerelease: auto
```

### `.github/workflows/release.yml`

```yaml
name: release
on:
  push:
    tags: ["v*"]
permissions:
  contents: write
jobs:
  release:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with: { fetch-depth: 0 }
      - uses: actions/setup-go@v5
        with: { go-version: "1.25" }
      - uses: goreleaser/goreleaser-action@v6
        with:
          args: release --clean
        env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
```

### `.github/workflows/ci.yml`

Runs on every push and PR. Required to merge.

```yaml
name: ci
on: [push, pull_request]
jobs:
  test:
    runs-on: macos-latest        # tmux + macOS-specific path quirks
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version: "1.25" }
      - run: brew install tmux fzf
      - name: gofmt
        run: |
          out=$(gofmt -l .)
          [ -z "$out" ] || { echo "$out"; exit 1; }
      - name: vet
        run: go vet ./...
      - name: unit + integration (with -race)
        run: go test -race ./...
      - name: smoke
        run: bash tests/smoke.sh
      - name: auth invariant — no credential refs in source
        run: |
          if grep -rE '(ANTHROPIC_|credentials\.json)' --include='*.go' . \
             | grep -vE '^[^:]+:[[:space:]]*//' >/dev/null; then
            echo "ERROR: non-comment reference to credentials.json or ANTHROPIC_ in Go source"
            grep -rnE '(ANTHROPIC_|credentials\.json)' --include='*.go' . \
              | grep -vE '^[^:]+:[[:space:]]*//'
            exit 1
          fi
```

The auth-invariant grep step is mandatory. It's the central promise of `mt`
(see `spec.md` §1.4); the smoke test catches it post-build, this catches it
at PR time before any binary exists. Defense in depth.

### Power-user fallback

```sh
go install github.com/jinyuanlu/metatree/cmd/mt@latest
```

For users who'd rather build from source. Documented in README's install
section as the second path.

### Homebrew tap (later)

Deferred until v1.1, per `spec.md` §2.10. Goreleaser can generate the
formula automatically once we add a `homebrew_tap` repo.

---

## 16. Migration plan

> **Status: completed.** The Go port shipped as v1.0 following the plan
> below. The "freeze `mt.sh`" policy in commit 0 was subsequently relaxed:
> `mt.sh` is now maintained in lockstep with the Go binary as a parallel
> reference implementation. See §19 for the current versioning posture and
> §1 (Non-goals) for the updated parity contract. The table below is
> preserved as historical record of the port itself.

Eight commits, each shipping a verifiable artifact.

| Commit | What ships | Verifiable by |
|--------|------------|---------------|
| 0 | freeze `mt.sh`: README banner, no further commits to it; bug fixes go to Go only | git log shows mt.sh untouched after this commit |
| 1 | skeleton + `config` + `mtlog` + `mt --help` + `mt diagnose` + `ci.yml` | `go test ./...`, manual `mt diagnose`, CI green |
| 2 | `mt ls` (read-only, exercises gitio + dashboard) | smoke §2 |
| 3 | `mt show` + `mt bind` (tmux state, bindings) | smoke §1, §12, §17 |
| 4 | `mt new` + `mt rm` (worktree lifecycle) | smoke §3, §5, §6, §7, §8, §9 |
| 5 | `mt switch` + `mt prune` + auto-bind | smoke §11, §15, §16 |
| 6 | git-crypt support + chrome + OSC handling | smoke §13, §14, §18 |
| 7 | goreleaser, install.sh swap, `mt.sh` deprecated | release tag, end-to-end install |

After commit 7:

- `mt.sh` gets a deprecation banner: stays in repo for 30 days then deletes
  in v2.0.0.
- README updates to point at the binary install.
- `Justfile` `just install` swaps from copying `mt.sh` to building the Go
  binary.
- Smoke runs against the Go binary by default.

If any commit fails its verification step, the port stops at that commit
and we debug. No commit ships broken.

---

## 17. Anti-patterns to avoid

Lessons from porting the bash. If you see these in a PR, push back.

1. **Calling `os/exec` outside `tmuxio` or `gitio`.** Commands and dashboard
   should never know what shell command they're invoking.
2. **Parsing `pane_title` to identify mt panes.** Always read `@mt-managed`.
   The whole reason that user option exists is to be the stable identity;
   `pane_title` is informational only.
3. **Path string comparison without `filepath.EvalSymlinks`.** macOS `/tmp`
   is a symlink to `/private/tmp`; git's worktree paths come back canonical
   while config paths typically aren't. Always normalize before comparing.
4. **Storing state in a file that mt itself writes and reads.** mt's whole
   thesis is "no daemon, no state file." All state lives in tmux (panes,
   options) and git (worktrees). The invocation log is write-only.
5. **A `--quiet` flag.** mt is already quiet by default; if you want to
   suppress an info line, restructure so it doesn't print in the first place.
6. **Goroutines.** mt is a control-plane CLI that exits in <100ms. There's
   nothing to parallelize. Concurrency adds a class of bugs for zero user-
   visible benefit. Keep it sequential.
7. **A `command` struct hierarchy.** Subcommands are one function each. A
   `Cmd` interface with `Name()`, `Description()`, `Run()` methods is more
   ceremony than the surface justifies. Do the boring thing.
8. **Wrapping every error with `fmt.Errorf`.** Wrap at package boundaries
   (one verb of context). Don't wrap inside a package — let the underlying
   error speak.
9. **Mocks for `tmuxio` in unit tests.** If a test needs tmux state, it
   belongs in the integration tier with a real tmux fixture. The mock
   surface is the integration test surface; faithfully simulating tmux's
   100-character socket limit and OSC behavior is not worth it.
10. **Adding subcommands not in `spec.md`.** New subcommand = new product
    surface = new line in `spec.md`. Update the product spec first; the
    Go implementation tracks it.

---

## 18. Decisions (locked before commit 1)

These were "open questions" in earlier drafts. Each is locked here so the
implementation has a contract, not five guesses.

1. **Top-level type names.** No shared `internal/mt` package. `Backend`
   and `Branch` (the two cross-cutting types) are duplicated where needed.
   Cross-package imports of fundamental types create fragile coupling and
   the duplication is trivial.

2. **Subcommand argv parsing.** Stdlib `flag` + manual dispatch in
   `cmd/mt/main.go`. No `cobra`. Nine subcommands, a few with simple flags
   (`--with`, `-z`, `--force`); manual dispatch is shorter than cobra's
   ceremony.

3. **Branch name validation.** A `branch.Slugify(input) (Branch, error)`
   function with explicit validation. Returns an error if the input
   slugifies to empty. The Slugify function knows nothing about
   `branch_prefix` — that's a config concern applied at the call site, not
   a property of the type.

4. **Config bool string-form support.** Accept both native TOML bools
   (`auto_direnv_allow = true`) AND string-quoted bools
   (`auto_direnv_allow = "true"`) **forever**. Config is a contract;
   breaking it for cosmetic reasons is rude. README's example config shows
   native bools; the parser tolerates both.

5. **Error message format.** One-line colon-separated:
   `mt: <verb>: <verb>: <root>`. Matches the bash convention and CLI norms.
   No multi-line stack-style indentation; that's what `mt diagnose` is for.

---

## 19. Versioning

The Go port ships as **v1.0.0**. Bash mt.sh has been pre-1.0 (no tag).
v1.0.0 is the first tagged release; everything before is "pre-release bash
prototype."

| Version | Marker                                                         |
| ------- | -------------------------------------------------------------- |
| v1.0.0  | Go binary at feature parity with bash mt.sh; both maintained in lockstep. |
| v1.1.0  | Homebrew tap.                                                  |
| v1.2.0  | First post-port feature (TBD; not pre-committed).              |
| v2.0.0  | TBD — original "remove mt.sh" plan superseded by lockstep maintenance (see §16 note). |

`spec.md` and this guide get updated together. Breaking changes to either
require a major version bump.

---

## GSTACK REVIEW REPORT

| Review | Trigger | Why | Runs | Status | Findings |
|--------|---------|-----|------|--------|----------|
| CEO Review | `/plan-ceo-review` | Scope & strategy | 0 | — | not run |
| Codex Review | `/codex review` | Independent 2nd opinion | 0 | — | not run |
| Eng Review | `/plan-eng-review` | Architecture & tests (required) | 1 | issues_open | 4 amendments locked, 1 critical gap |
| Design Review | `/plan-design-review` | UI/UX gaps | 0 | — | not run (CLI tool, not applicable) |
| DX Review | `/plan-devex-review` | Developer experience gaps | 0 | — | not run |

- **UNRESOLVED:** 0 (all D1–D6 decisions locked into the plan)
- **CRITICAL GAP:** git-crypt key-missing failure mode is silent in plan §11; should log a warning to mtlog and surface via stderr. Tracked as part of plan §11 amendment, no code yet.
- **VERDICT:** ENG REVIEW CLEARED — 6 decisions locked, plan amended with named test list, CI gates, mt.sh freeze policy, and converted-to-decisions §18. Ready to implement starting at commit 0 (mt.sh freeze).
