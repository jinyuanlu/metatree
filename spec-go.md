# `mt` — Go Implementation Specification

> Companion to [`spec.md`](spec.md), which defines the **product** (commands,
> behaviors, acceptance criteria, auth invariant). This document defines
> **how the Go implementation realizes that product** — package layout,
> error handling, testing, distribution.
>
> The product spec is the source of truth for behavior. If this document and
> `spec.md` ever disagree, `spec.md` wins.

---

## 1. Why Go

`mt.sh` reached ~640 lines and accumulated bugs that were exclusively
language-shape: `set -e` interactions with pipefail and SIGPIPE, `local` lifetime
under EXIT traps, ad-hoc TOML parsing, IFS edge cases, macOS path symlink
mismatches under `pwd -P`. None of these are interesting product bugs. They are
the language fighting the implementer.

Go is the spec's documented escape hatch (see `spec.md` §2.11 V2 backlog #5).
At ~640 lines of bash we are clearly past the boundary.

### Goals of the port

1. **Same product.** Every command, flag, behavior, and exit code in `spec.md`
   is preserved. `tests/smoke.sh` is the executable specification — the Go
   binary must pass all 19 sections without modification to the test.
2. **Same UX.** Same install URL, same config file, same tmux interactions.
   Existing `mt bind`, `mt diagnose` outputs stay structurally identical.
3. **Better internals.** Typed errors, explicit boundaries, table-driven
   tests, no shell-level surprises.
4. **Same audit posture.** The repository remains short enough to read in
   one sitting (target: ≤2,000 lines of Go for the entire core).
5. **Same distribution speed.** `curl | bash` install completes in under
   two seconds, fetching a single static binary from GitHub Releases.

### Non-goals

- TUI framework, custom rendering, or any escape from spec.md §1.2 ("tmux is
  the entire UI"). Same auth invariant from §1.4 — Go implementation does
  not change the credentials posture.
- Compatibility with bash mt.sh as a runtime peer. The Go binary replaces it.
  `mt.sh` is preserved in the repo as a reference implementation only,
  marked deprecated, removed in v2.0.0.
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
   subcommand. Reading it gives you the surface in 60 lines.
4. `internal/command/<name>.go` — pick any one. Each file is a single
   subcommand and reads top-to-bottom.

You should be able to add a new subcommand after reading these four things.

---

## 3. Layering

Three layers, dependency arrow points down. **No upward calls.**

```
                    ┌─────────────────────────────┐
                    │         cmd/mt              │   entry point, ~80 lines
                    │  argv → command → exit code │
                    └──────────────┬──────────────┘
                                   ▼
            ┌──────────────────────┴──────────────────────┐
            │              internal/command               │   one file per subcommand
            │  Run(env *Env, args []string) error          │
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
                       │   mtlog      │   invocation log (~50 lines)
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
│       ├── help.go
│       ├── diagnose.go
│       ├── ls.go
│       ├── show.go
│       ├── new.go
│       ├── rm.go
│       ├── prune.go
│       ├── switch.go                # filename: switchcmd.go (switch is reserved)
│       └── bind.go
├── tests/
│   ├── smoke.sh                     # carried over from bash, runs against Go binary
│   ├── mt.bats                      # carried over, optional
│   └── integration_test.go          # Go-level integration tests (real tmux)
├── docs/
│   └── porting-from-bash.md         # one-time porting log, removed in v2.0
├── .github/
│   └── workflows/
│       └── release.yml              # goreleaser on tag
├── .goreleaser.yaml
├── go.mod
├── go.sum
├── install.sh                       # rewritten to fetch binary
├── mt.sh                            # bash reference impl, deprecated
├── spec.md                          # product spec
├── spec-go.md                       # this file
├── Justfile
├── LICENSE
└── README.md
```

### Package size budget (soft, expressed in lines of Go)

| Package      | Target  | Hard ceiling | Reason                                      |
| ------------ | ------- | ------------ | ------------------------------------------- |
| `cmd/mt`     |     80  |    120       | argv → dispatch → exit code; trivially flat |
| `config`     |    150  |    250       | TOML schema, Default, Load                  |
| `mtlog`      |     60  |    100       | append-only file writer + tail              |
| `tmuxio`     |    250  |    400       | one wrapper per tmux verb mt uses           |
| `gitio`      |    150  |    250       | discover_worktrees, parent_repo_of, status  |
| `dashboard`  |    300  |    500       | chrome, bindings, pane registry             |
| `command/*`  |    600  |   1000       | nine subcommands, ~60-110 lines each        |
| **Total**    | **~1,600** | **~2,600** | smoke.sh + mt.bats stay outside this        |

If a package crosses its hard ceiling, the package needs a split — not more
indirection.

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
    ReposDirs        []string  `toml:"repos_dirs"`
    Repos            []string  `toml:"repos"`
    TmuxSession      string    `toml:"tmux_session"`
    TmuxWindow       string    `toml:"tmux_window"`
    BranchPrefix     string    `toml:"branch_prefix"`
    WorktreeSubdir   string    `toml:"worktree_subdir"`
    DefaultBackend   string    `toml:"default_backend"`
    OllamaModel      string    `toml:"ollama_model"`
    ClaudeCmd        string    `toml:"claude_cmd"`
    OllamaCmd        string    `toml:"ollama_cmd"`
    AutoDirenvAllow  bool      `toml:"auto_direnv_allow"`
    AutoStatusChrome bool      `toml:"auto_status_chrome"`

    Path string `toml:"-"`  // resolved config path (informational)
}

func Default() *Config       // returns config with all defaults populated
func Load() (*Config, error) // overlays file (if any) onto Default
```

- Bool fields accept TOML strings (`"true"`/`"false"`) for backwards-compat
  with the bash version's quoted booleans, AND TOML native booleans (`true`/`false`).
  `BurntSushi/toml` handles native; we add a custom unmarshal hook for the
  string form. Test the hybrid in `config_test.go`.
- A missing config file is not an error — defaults win.
- `MT_CONFIG` env var overrides the path. Same behavior as bash.

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

// Count of mt-managed panes — used by `new` to decide split-vs-reuse.
func ManagedCount(target string) (int, error)
```

`@mt-managed` is the **only** string mt looks at to make decisions about
panes. `pane_title` is purely informational (and visible to the user) —
it can change at any time and that's fine.

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

### Adding a new subcommand

1. Create `internal/command/<name>.go` with `func Run<Name>(env *Env, args []string) error`.
2. Add a case to `dispatch()` in `cmd/mt/main.go`.
3. Add the usage line to `command/help.go`.
4. Add an integration test in `tests/integration_test.go`.
5. Add a smoke section to `tests/smoke.sh` (or assert no regression in
   existing sections).

That's the entire surface change. Per-command files are the unit of
extension.

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

### Tier 2 — integration tests (Go, real tmux + git)

`tests/integration_test.go`. Use `t.TempDir()` for fixture isolation, real
`tmux` with a short `TMUX_TMPDIR` (the bash smoke test discovered the macOS
104-char socket limit; the Go test inherits that fix).

Examples:

- `TestNewCreatesWorktreeAndPane` — real fixture repo, run `mt new`, assert
  worktree+branch+pane exist and the pane has `@mt-managed` set.
- `TestSwitchExcludesDeadEntries` — create a pane, kill it, run `mt switch`,
  capture fzf input via PATH-prepended stub, assert no `[dead]` rows.
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

Audit-friendly: ~30 lines. The user can read it before piping.

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

Six commits, each a working binary.

| Commit | What ships | Verifiable by |
|--------|------------|---------------|
| 1 | skeleton + `config` + `mtlog` + `mt --help` + `mt diagnose` | `go test ./...`, manual `mt diagnose` |
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

## 18. Open questions

To resolve before commit 1:

1. **Top-level type names.** Some live in `cmd/mt`, some in a tiny shared
   `internal/mt` package. The minimal shared set: `Backend`, `Branch`. The
   rest are package-local. Lean toward "no shared package; duplicate the two
   small ones if needed" since cross-package imports of fundamental types
   create fragile coupling.

2. **Subcommand argv parsing.** stdlib `flag` is awkward for subcommands but
   has zero deps. `cobra` is the convention but adds 30K LOC to the binary
   and a generation pipeline. Lean toward stdlib + manual dispatch — we
   have ten subcommands, three with flags. The bash version managed without
   a CLI framework; Go can too.

3. **Branch name validation.** The bash version slugifies aggressively. Go
   version: a `branch.Slugify(input) (Branch, error)` function with explicit
   validation rules. Should bare `mt-style/` prefix get auto-prepended in
   the Go API or stay a config concern? Lean toward "config concern only" —
   the type does not know about prefixes.

4. **Config "raw" string fields for booleans.** Bash passed booleans as
   quoted strings (`auto_direnv_allow = "true"`). Native TOML supports both.
   The Go decoder handles both via a custom unmarshaller, but should we
   migrate users to native bools and deprecate strings? Lean toward
   "both forever" — config is a contract, breaking it for cosmetic reasons
   is rude.

5. **Error message format.** Bash's `mt: <msg>` prefix is preserved. Multi-
   line wrapped errors get formatted as `mt: <verb>: <verb>: <root>`. Decide
   whether to pretty-print errors with stack-style indentation, or keep the
   one-line colon-separated form. Lean toward one-line — matches the bash
   convention and CLI norms.

---

## 19. Versioning

The Go port ships as **v1.0.0**. Bash mt.sh has been pre-1.0 (no tag).
v1.0.0 is the first tagged release; everything before is "pre-release bash
prototype."

| Version | Marker                                                         |
| ------- | -------------------------------------------------------------- |
| v1.0.0  | Go binary at feature parity with bash mt.sh. mt.sh deprecated. |
| v1.1.0  | Homebrew tap.                                                  |
| v1.2.0  | First post-port feature (TBD; not pre-committed).              |
| v2.0.0  | mt.sh removed from repo. Distribution is binary-only.          |

`spec.md` and `spec-go.md` get updated together. Breaking changes to either
require a major version bump.
