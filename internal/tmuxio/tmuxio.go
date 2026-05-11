// Package tmuxio is the only package in mt that knows how to spawn `tmux`.
//
// One Go function per tmux verb mt uses. Each function builds an
// *exec.Cmd with explicit args (no shell quoting), and returns typed
// values plus a wrapped error that names the verb attempted.
//
// The contract is locked in CONTRIBUTING.md §9. The pane-identity rule from
// §10 is enforced at the type level: callers learn about a pane only
// through the Pane struct, whose MtManaged field is the stable
// identity. pane_title is informational and never returned.
//
// Anti-pattern compliance (CONTRIBUTING.md §17):
//   - #1 (no os/exec outside tmuxio/gitio): satisfied — all exec.Cmd
//     construction lives here.
//   - #2 (never parse pane_title for identity): the format string used
//     by ListPanes deliberately omits pane_title.
//   - #6 (no goroutines): everything in this package is sequential.
package tmuxio

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// ErrUnavailable signals "tmux server unreachable, can't autostart."
// Callers can wrap or test for this with errors.Is to give a friendlier
// message ("start with: tmux new -d -s …") instead of the raw exec error.
var ErrUnavailable = errors.New("tmux server unavailable")

// PaneID is a tmux pane identifier such as "%17". Always starts with '%'.
type PaneID string

// SessionName is a tmux session name (left of the colon in "session:window").
type SessionName string

// WindowName is a tmux window name (right of the colon in "session:window").
type WindowName string

// Pane is the typed view of a tmux pane that mt cares about.
//
// Note: pane_title is intentionally absent. mt-managed identity goes
// through the MtManaged field (sourced from the @mt-managed user
// option), never through pane_title. See CONTRIBUTING.md §10.
type Pane struct {
	ID         PaneID
	MtManaged  string // @mt-managed user option, "" if unset
	CurrentCmd string // pane_current_command
	Active     bool
}

// listPanesFormat is the -F format used by ListPanes.
//
// Fields, in order, separated by '|':
//  1. #{pane_id}                  e.g. "%17"
//  2. #{?pane_active,1,0}         "1" if active, "0" otherwise
//  3. #{pane_current_command}     e.g. "zsh", "claude"
//  4. #{@mt-managed}              empty if unset
//
// Deliberately omits pane_title — see CONTRIBUTING.md §10/§17 #2.
const listPanesFormat = "#{pane_id}|#{?pane_active,1,0}|#{pane_current_command}|#{@mt-managed}"

// run executes a tmux command with the given args and returns trimmed
// stdout. On non-zero exit it returns an error wrapped with the verb.
func run(args ...string) (string, error) {
	cmd := exec.Command("tmux", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			return "", fmt.Errorf("tmux %s: %w", strings.Join(args, " "), err)
		}
		return "", fmt.Errorf("tmux %s: %w: %s", strings.Join(args, " "), err, msg)
	}
	return strings.TrimRight(stdout.String(), "\n"), nil
}

// runQuiet is like run but discards output and returns only the error.
func runQuiet(args ...string) error {
	_, err := run(args...)
	return err
}

// ServerRunning reports whether a tmux server is currently reachable.
//
// Implemented via `tmux has-session` with no target — tmux exits 0 if
// any session exists, non-zero (with "no server running") otherwise.
func ServerRunning() bool {
	cmd := exec.Command("tmux", "has-session")
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run() == nil
}

// HasSession reports whether the named session exists.
func HasSession(name SessionName) bool {
	cmd := exec.Command("tmux", "has-session", "-t="+string(name))
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run() == nil
}

// InsideTmux reports whether the current process is running inside a
// tmux client (i.e. $TMUX is set and non-empty).
func InsideTmux() bool {
	return os.Getenv("TMUX") != ""
}

// CurrentSession returns the session name of the attached tmux client.
// Only meaningful when InsideTmux() is true.
func CurrentSession() (SessionName, error) {
	out, err := run("display-message", "-p", "#{session_name}")
	if err != nil {
		return "", err
	}
	return SessionName(out), nil
}

// CurrentWindow returns the window name of the attached tmux client.
// Only meaningful when InsideTmux() is true.
func CurrentWindow() (WindowName, error) {
	out, err := run("display-message", "-p", "#{window_name}")
	if err != nil {
		return "", err
	}
	return WindowName(out), nil
}

// EnsureSession creates the named session (with the given initial
// window) if it does not already exist. Idempotent — re-running on an
// existing session is a no-op.
//
// If tmux exits with "no server running" and we cannot start one,
// the returned error wraps ErrUnavailable.
func EnsureSession(session SessionName, window WindowName) error {
	if HasSession(session) {
		return nil
	}
	if err := runQuiet("new-session", "-d", "-s", string(session), "-n", string(window)); err != nil {
		return fmt.Errorf("tmux new-session -d -s %s -n %s: %w", session, window, errors.Join(err, ErrUnavailable))
	}
	return nil
}

// EnsureWindow ensures the named window exists in the named session.
// Idempotent. The session must already exist (call EnsureSession first).
func EnsureWindow(session SessionName, window WindowName) error {
	out, err := run("list-windows", "-t", string(session), "-F", "#W")
	if err != nil {
		return fmt.Errorf("tmux list-windows -t %s: %w", session, err)
	}
	for _, w := range strings.Split(out, "\n") {
		if w == string(window) {
			return nil
		}
	}
	if err := runQuiet("new-window", "-t", string(session)+":", "-n", string(window)); err != nil {
		return fmt.Errorf("tmux new-window -t %s: -n %s: %w", session, window, err)
	}
	return nil
}

// ListPanes returns every pane on the given target ("session:window" or
// any tmux target spec). Output is parsed from the -F format defined by
// listPanesFormat.
//
// A pane's MtManaged field is the empty string when the @mt-managed
// user option is unset (which is the normal state for the bare-shell
// pane created by tmux new-session, and for any pane the user splits
// manually).
func ListPanes(target string) ([]Pane, error) {
	out, err := run("list-panes", "-t", target, "-F", listPanesFormat)
	if err != nil {
		return nil, fmt.Errorf("tmux list-panes -t %s: %w", target, err)
	}
	if out == "" {
		return nil, nil
	}
	lines := strings.Split(out, "\n")
	panes := make([]Pane, 0, len(lines))
	for _, line := range lines {
		if line == "" {
			continue
		}
		// SplitN with n=4 keeps any embedded '|' inside the @mt-managed
		// value (unlikely but possible) attached to that final field.
		parts := strings.SplitN(line, "|", 4)
		if len(parts) < 4 {
			// Defensive: tmux output is well-formed in practice, but a
			// short line means we can't trust the row — skip it rather
			// than index out of range.
			continue
		}
		panes = append(panes, Pane{
			ID:         PaneID(parts[0]),
			Active:     parts[1] == "1",
			CurrentCmd: parts[2],
			MtManaged:  parts[3],
		})
	}
	return panes, nil
}

// SplitPane splits the target pane/window, sets cwd, runs cmd, and
// returns the new pane id. cmd is passed as a single argv element to
// tmux split-window (tmux runs it via the user's shell).
//
// cwd may be empty to inherit the parent pane's working directory.
func SplitPane(target, cwd, cmd string) (PaneID, error) {
	args := []string{"split-window", "-t", target, "-P", "-F", "#{pane_id}"}
	if cwd != "" {
		args = append(args, "-c", cwd)
	}
	if cmd != "" {
		args = append(args, cmd)
	}
	out, err := run(args...)
	if err != nil {
		return "", fmt.Errorf("tmux split-window -t %s: %w", target, err)
	}
	return PaneID(out), nil
}

// SendKeys sends the given keystrokes to the pane, followed by Enter.
// keys is sent as a single tmux argument (no shell parsing).
func SendKeys(id PaneID, keys string) error {
	if err := runQuiet("send-keys", "-t", string(id), keys, "Enter"); err != nil {
		return fmt.Errorf("tmux send-keys -t %s: %w", id, err)
	}
	return nil
}

// SetPaneTitle sets the visible pane border title via select-pane -T.
// This is informational only — agents like Claude Code overwrite it
// via OSC 2; do not use it for identity (see CONTRIBUTING.md §10).
func SetPaneTitle(id PaneID, title string) error {
	if err := runQuiet("select-pane", "-t", string(id), "-T", title); err != nil {
		return fmt.Errorf("tmux select-pane -t %s -T: %w", id, err)
	}
	return nil
}

// SetPaneOption sets a per-pane user option (-p). The canonical use is
// SetPaneOption(id, "@mt-managed", title) — the stable mt-identity
// marker that pane contents cannot clobber.
func SetPaneOption(id PaneID, key, value string) error {
	if err := runQuiet("set-option", "-p", "-t", string(id), key, value); err != nil {
		return fmt.Errorf("tmux set-option -p -t %s %s: %w", id, key, err)
	}
	return nil
}

// SelectPane focuses the given pane.
func SelectPane(id PaneID) error {
	if err := runQuiet("select-pane", "-t", string(id)); err != nil {
		return fmt.Errorf("tmux select-pane -t %s: %w", id, err)
	}
	return nil
}

// ZoomPane toggles zoom on the given pane (resize-pane -Z).
func ZoomPane(id PaneID) error {
	if err := runQuiet("resize-pane", "-t", string(id), "-Z"); err != nil {
		return fmt.Errorf("tmux resize-pane -t %s -Z: %w", id, err)
	}
	return nil
}

// KillPane removes the given pane.
func KillPane(id PaneID) error {
	if err := runQuiet("kill-pane", "-t", string(id)); err != nil {
		return fmt.Errorf("tmux kill-pane -t %s: %w", id, err)
	}
	return nil
}

// SetWindowOption sets a per-window option scoped to target.
func SetWindowOption(target, key, value string) error {
	if err := runQuiet("set-window-option", "-t", target, key, value); err != nil {
		return fmt.Errorf("tmux set-window-option -t %s %s: %w", target, key, err)
	}
	return nil
}

// SetSessionOption sets a per-session option.
func SetSessionOption(session SessionName, key, value string) error {
	if err := runQuiet("set-option", "-t", string(session), key, value); err != nil {
		return fmt.Errorf("tmux set-option -t %s %s: %w", session, key, err)
	}
	return nil
}

// TileLayout applies the "tiled" layout to the target window.
func TileLayout(target string) error {
	if err := runQuiet("select-layout", "-t", target, "tiled"); err != nil {
		return fmt.Errorf("tmux select-layout -t %s tiled: %w", target, err)
	}
	return nil
}

// ListPrefixKeys returns the raw lines from `tmux list-keys -T prefix`.
// Used by the dashboard to detect whether mt's prefix bindings are
// already installed on the running server.
func ListPrefixKeys() ([]string, error) {
	out, err := run("list-keys", "-T", "prefix")
	if err != nil {
		return nil, fmt.Errorf("tmux list-keys -T prefix: %w", err)
	}
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

// BindPrefix binds `key` in the prefix table to `command`. The command
// is passed as a single tmux argument; tmux parses it the same way it
// parses any bind-key right-hand side.
func BindPrefix(key, command string) error {
	if err := runQuiet("bind-key", key, command); err != nil {
		return fmt.Errorf("tmux bind-key %s: %w", key, err)
	}
	return nil
}

// DisplayPopup runs `command` inside a centered popup of the given
// width/height (tmux percentage strings like "80%" are valid).
//
// Requires tmux 3.2+.
func DisplayPopup(width, height, command string) error {
	if err := runQuiet("display-popup", "-w", width, "-h", height, "-E", command); err != nil {
		return fmt.Errorf("tmux display-popup -w %s -h %s -E: %w", width, height, err)
	}
	return nil
}

// AttachOrSwitch attaches the running terminal to target. Inside an
// existing tmux client it switches the client; otherwise it attaches.
//
// Note: attach is a foreground operation that takes over the terminal;
// callers that want to keep returning control to the user should only
// invoke this from the top-level command (never from a goroutine — see
// CONTRIBUTING.md §17 #6, which is moot here anyway).
func AttachOrSwitch(target string) error {
	if InsideTmux() {
		if err := runQuiet("switch-client", "-t", target); err != nil {
			return fmt.Errorf("tmux switch-client -t %s: %w", target, err)
		}
		return nil
	}
	cmd := exec.Command("tmux", "attach", "-t", target)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("tmux attach -t %s: %w", target, err)
	}
	return nil
}
