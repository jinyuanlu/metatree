package command

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/jinyuanlu/metatree/internal/config"
	"github.com/jinyuanlu/metatree/internal/dashboard"
	"github.com/jinyuanlu/metatree/internal/mtlog"
	"github.com/jinyuanlu/metatree/internal/tmuxio"
)

// Version is set at build time via -ldflags '-X main.version=...'.
// Wire it through main.go.
var Version = "dev"

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
  mt diagnose        print state for debugging (versions, config, bindings, log)
  mt --help

config: ~/.config/mt/config.toml — empty file is valid (all fields default)
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
	fmt.Fprintf(out, "  mt version: %s\n", Version)
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
	fmt.Fprintf(out, "  repos_dirs:       %s\n", strings.Join(env.Config.ReposDirs, " "))
	fmt.Fprintf(out, "  repos:            %s\n", strings.Join(env.Config.Repos, " "))
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
