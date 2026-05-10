package command

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/jinyuanlu/metatree/internal/config"
	"github.com/jinyuanlu/metatree/internal/dashboard"
	"github.com/jinyuanlu/metatree/internal/mtlog"
	"github.com/jinyuanlu/metatree/internal/tmuxio"
)

// Version, Commit, and BuildDate are set at build time via -ldflags. Wire
// them through main.go. They surface via `mt --version`, `mt diagnose`,
// and (after install) the install.sh confirmation line — so a user can
// always trace their binary to a specific commit.
var (
	Version   = "dev"
	Commit    = "none"
	BuildDate = "unknown"
)

// PrintVersion writes a single-line build identity. Format is stable —
// install.sh and any future scripts can grep `mt v` to extract the tag.
func PrintVersion(w io.Writer) {
	fmt.Fprintf(w, "mt %s (commit %s, built %s)\n", Version, shortCommit(Commit), BuildDate)
}

// shortCommit truncates a 40-char SHA to 7. Leaves anything else
// (including "none" and pre-shortened values) untouched.
func shortCommit(c string) string {
	if len(c) >= 40 {
		return c[:7]
	}
	return c
}

// usageText is the canonical help string. Same order and shape as the
// bash `usage()` block in mt.sh (preserved for muscle memory).
const usageText = `mt — tmux-native dashboard for Claude Code and Ollama across worktrees

usage:
  mt                 attach to (or create) the dashboard window
  mt show            (same as bare mt)
  mt new [--with claude|ollama]    create a worktree + launch agent in a pane
  mt ls              list worktrees: title, path, backend, state (live|dead)
  mt rm [--force]    pick a worktree, remove it (worktree, branch, pane all)
  mt switch [-z]     fzf jump to any pane (live or dead — dead ones revive)
  mt prune [--force] remove all dead worktrees in one shot (interactive confirm)
  mt bind            install tmux keybindings (prefix+g/G/N/R) for in-agent use
  mt setup           configure repos_dirs and claude_cmd (interactive or flags)
  mt upgrade         download the latest release and replace this binary
  mt diagnose        print state for debugging (versions, config, bindings, log)
  mt --version       print build identity (tag, commit, build date)
  mt --help

config: ~/.metatree/config.toml — created by 'mt setup' (or auto-seeded
        on first run if a common dev folder exists under $HOME).
        Legacy ~/.config/mt/config.toml is auto-migrated once.
docs:   https://github.com/jinyuanlu/metatree
`

// RunHelp prints the usage text and returns nil.
func RunHelp(env *Env, args []string) error {
	fmt.Fprint(env.Stdout, usageText)
	return nil
}

// RunBind installs the popup bindings on the running tmux server, prints
// the resulting state, and exits.
func RunBind(env *Env, args []string) error {
	if !tmuxio.ServerRunning() {
		return ExitWith(1, "tmux server not running; start with: tmux new -d -s %s",
			env.SessionName)
	}
	mtPath, err := selfPath()
	if err != nil {
		return ExitWith(1, "could not resolve mt's own path: %v", err)
	}
	if err := dashboard.InstallBindings(mtPath); err != nil {
		return ExitWith(1, "install bindings: %v", err)
	}
	fmt.Fprintf(env.Stdout, `mt keybindings set on the running tmux server (absolute-path form):

  prefix + g   →  %s switch -z      ← high-frequency
  prefix + G   →  %s switch
  prefix + N   →  %s new
  prefix + R   →  %s rm

Reach them from inside Claude or Ollama — tmux intercepts the prefix
before the agent sees the keystrokes. The popup overlays the screen,
runs fzf, and disappears the moment you press enter.

These bindings live on the running tmux server only. To persist across
restarts, add to ~/.tmux.conf:

  bind-key g display-popup -w 80%% -h 60%% -E "%s switch -z"
  bind-key G display-popup -w 80%% -h 60%% -E "%s switch"
  bind-key N display-popup -w 80%% -h 60%% -E "%s new"
  bind-key R display-popup -w 80%% -h 60%% -E "%s rm"

Then run:  tmux source ~/.tmux.conf

Requires tmux 3.2+ (display-popup).
`, mtPath, mtPath, mtPath, mtPath, mtPath, mtPath, mtPath, mtPath)
	return nil
}

// RunDiagnose dumps state for issue reports. See spec-go.md §13 (bind/
// diagnose are in meta.go) and the bash cmd_diagnose function for the
// section ordering.
func RunDiagnose(env *Env, args []string) error {
	mtPath, _ := selfPath()
	if mtPath == "" {
		mtPath = "(unknown)"
	}

	out := env.Stdout
	fmt.Fprintln(out, "mt diagnose — copy/paste this entire block when reporting issues.")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "VERSIONS")
	fmt.Fprintf(out, "  mt path:    %s\n", mtPath)
	fmt.Fprintf(out, "  mt version: %s (commit %s, built %s)\n",
		Version, shortCommit(Commit), BuildDate)
	fmt.Fprintf(out, "  tmux:       %s\n", versionLine("tmux", "-V"))
	fmt.Fprintf(out, "  fzf:        %s\n", versionLine("fzf", "--version"))
	fmt.Fprintf(out, "  git-crypt:  %s\n", versionLine("git-crypt", "--version"))
	fmt.Fprintf(out, "  direnv:     %s\n", versionLine("direnv", "--version"))
	fmt.Fprintln(out)

	fmt.Fprintln(out, "CONFIG")
	fmt.Fprintf(out, "  MT_CONFIG:        %s\n", config.Path())
	exists := "no"
	if _, err := os.Stat(config.Path()); err == nil {
		exists = "yes"
	}
	fmt.Fprintf(out, "  exists:           %s\n", exists)
	fmt.Fprintf(out, "  tmux_session:     %s\n", env.Config.TmuxSession)
	fmt.Fprintf(out, "  tmux_window:      %s\n", env.Config.TmuxWindow)
	fmt.Fprintf(out, "  default_backend:  %s\n", env.Config.DefaultBackend)
	if len(env.Config.ReposDirs) == 0 {
		fmt.Fprintln(out, "  repos_dirs:       (none — run `mt setup`)")
	} else {
		fmt.Fprintf(out, "  repos_dirs:       %s\n", strings.Join(env.Config.ReposDirs, " "))
	}
	fmt.Fprintf(out, "  repos:            %s\n", strings.Join(env.Config.Repos, " "))
	fmt.Fprintf(out, "  claude_cmd:       %s\n", env.Config.ClaudeCmd)
	if r := LastFirstRunResult(); r != "" {
		fmt.Fprintf(out, "  first-run note:   %s this invocation\n", r)
	}
	fmt.Fprintln(out)

	fmt.Fprintln(out, "TMUX STATE")
	if env.InsideTmux {
		fmt.Fprintf(out, "  in_tmux:          yes (session = %s, window = %s)\n",
			env.SessionName, env.WindowName)
	} else {
		fmt.Fprintln(out, "  in_tmux:          no (running from a plain shell)")
	}
	hasSession := "no"
	if tmuxio.HasSession(env.SessionName) {
		hasSession = "yes"
	}
	fmt.Fprintf(out, "  has-session %s:  %s\n", env.SessionName, hasSession)
	fmt.Fprintln(out)

	fmt.Fprintln(out, "KEYBINDINGS (look for display-popup; if empty, run 'mt bind')")
	keys, _ := tmuxio.ListPrefixKeys()
	popupKeys := 0
	for _, k := range keys {
		if strings.Contains(k, "display-popup") {
			fmt.Fprintln(out, "  "+k)
			popupKeys++
		}
	}
	if popupKeys == 0 {
		fmt.Fprintln(out, "  (no display-popup bindings on this server)")
	}
	fmt.Fprintln(out)

	fmt.Fprintln(out, "LOG")
	fmt.Fprintf(out, "  path: %s\n", mtlog.Path())
	fmt.Fprintln(out, "  recent (last 10 lines):")
	tail := mtlog.Tail(10)
	if len(tail) == 0 {
		fmt.Fprintln(out, "    (log empty or missing)")
	}
	for _, ln := range tail {
		fmt.Fprintln(out, "    "+ln)
	}
	fmt.Fprintln(out)

	fmt.Fprintln(out, "QUICK CHECKS")
	fmt.Fprintln(out, "  - bindings missing → run 'mt bind'")
	fmt.Fprintln(out, "  - bindings present but prefix+g does nothing → tmux server may have restarted; re-run 'mt bind'")
	fmt.Fprintln(out, "  - popup flashes and closes → run 'mt switch' from a plain shell to see the actual error")

	return nil
}

// versionLine runs `cmd args...` and returns the first line of its
// combined output, or a placeholder when the binary isn't available.
func versionLine(cmd string, args ...string) string {
	if _, err := exec.LookPath(cmd); err != nil {
		return cmd + " NOT installed"
	}
	out, err := exec.Command(cmd, args...).CombinedOutput()
	if err != nil {
		return cmd + " (error: " + err.Error() + ")"
	}
	if i := strings.IndexByte(string(out), '\n'); i >= 0 {
		return strings.TrimSpace(string(out)[:i])
	}
	return strings.TrimSpace(string(out))
}

// selfPath returns the absolute path to the running mt binary. Used by
// `mt bind` so popup bindings have an absolute path that doesn't depend
// on tmux's inherited PATH.
func selfPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return exe, nil
}
