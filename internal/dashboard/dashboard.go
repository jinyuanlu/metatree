// Package dashboard owns mt's tmux session+window state: the dashboard
// itself, its chrome (pane-border-format + status-right), the popup
// keybindings, and the @mt-managed pane registry.
//
// dashboard composes tmuxio (which it owns the right to call); it does NOT
// shell out directly. See CONTRIBUTING.md §10 (pane registry) and §12 (dashboard
// chrome and bindings).
package dashboard

import (
	"errors"
	"fmt"
	"strings"

	"github.com/jinyuanlu/metatree/internal/config"
	"github.com/jinyuanlu/metatree/internal/tmuxio"
)

// MtManagedKey is the per-pane user option mt sets to identify its panes.
// Stable across OSC 2 escape sequences emitted by Claude or other agents,
// which would otherwise clobber pane_title. See CONTRIBUTING.md §10.
const MtManagedKey = "@mt-managed"

// MtBaseKey is the per-pane user option mt sets to record which ref the
// worktree was branched from (e.g. "origin/main", "develop"). Rendered
// into the pane border so a glance at the dashboard shows base lineage
// alongside repo:branch.
const MtBaseKey = "@mt-base"

// PaneBorderFormat shows the pane index, mt marker, optional base-ref, and
// running command. Active pane reverses colors. The marker falls back to "-"
// for non-mt panes (bare shells, externally-split panes). The base-ref
// `[<ref>]` is hidden when @mt-base is unset or equal to "HEAD" (legacy
// worktree_base = "head" mode — no informative ref to show).
const paneBorderFormat = `#{?pane_active,#[reverse] , }#{pane_index} ` +
	`#{?@mt-managed,#{@mt-managed},-}` +
	`#{?@mt-base,#{?#{==:#{@mt-base},HEAD},, [#{@mt-base}]},}` +
	` (#{pane_current_command})#[default]`

// StatusRight shows active pane's mt marker, its cwd (truncated), and time.
// Refreshes via the status-interval option (set to 2s).
const statusRightFormat = `#[fg=cyan]#{?@mt-managed,#{@mt-managed},(no mt pane)}` +
	`#[default]  ·  #{=50:pane_current_path}  ·  %H:%M`

// Ensure creates the session and window if missing, applies chrome (if
// enabled in config), and auto-installs popup bindings when they're not
// already present on the running tmux server.
//
// `mtPath` is the absolute path to the running mt binary; it's baked into
// the popup bindings so they work regardless of tmux's inherited PATH.
//
// Returns a "first-install" notice string when the bindings were just
// installed (caller should print it to stderr); empty string otherwise.
func Ensure(cfg *config.Config, mtPath string) (string, error) {
	session := tmuxio.SessionName(cfg.TmuxSession)
	window := tmuxio.WindowName(cfg.TmuxWindow)

	if err := tmuxio.EnsureSession(session, window); err != nil {
		if errors.Is(err, tmuxio.ErrUnavailable) {
			return "", fmt.Errorf(
				"tmux unavailable; start with: tmux new -d -s %s", session)
		}
		return "", fmt.Errorf("ensure session %s: %w", session, err)
	}
	if err := tmuxio.EnsureWindow(session, window); err != nil {
		return "", fmt.Errorf("ensure window %s:%s: %w", session, window, err)
	}

	if cfg.AutoStatusChrome {
		if err := applyChrome(session, window); err != nil {
			// chrome is cosmetic; don't fail the whole command for it
			_ = err
		}
	}

	notice := ""
	installed, err := bindingsInstalled()
	if err == nil && !installed {
		if err := InstallBindings(mtPath); err == nil {
			notice = "mt: installed prefix+g/G/N/R bindings on this tmux server"
		}
	}
	return notice, nil
}

// applyChrome sets pane-border-format on the window and status-right on the
// session. Scoped to mt's own dashboard — does not modify global tmux state.
func applyChrome(session tmuxio.SessionName, window tmuxio.WindowName) error {
	target := string(session) + ":" + string(window)
	if err := tmuxio.SetWindowOption(target, "pane-border-status", "top"); err != nil {
		return err
	}
	if err := tmuxio.SetWindowOption(target, "pane-border-format", paneBorderFormat); err != nil {
		return err
	}
	if err := tmuxio.SetSessionOption(session, "status-right", statusRightFormat); err != nil {
		return err
	}
	if err := tmuxio.SetSessionOption(session, "status-right-length", "120"); err != nil {
		return err
	}
	return tmuxio.SetSessionOption(session, "status-interval", "2")
}

// bindingsInstalled reports whether prefix+g is bound to a display-popup
// command on the running tmux server. We use prefix+g specifically because
// it's our highest-frequency binding; if it's there, the others are too.
func bindingsInstalled() (bool, error) {
	keys, err := tmuxio.ListPrefixKeys()
	if err != nil {
		return false, err
	}
	for _, k := range keys {
		// "bind-key    -T prefix g       display-popup ..."
		if strings.Contains(k, " g ") && strings.Contains(k, "display-popup") {
			return true, nil
		}
	}
	return false, nil
}

// InstallBindings sets the four popup bindings on the running tmux server.
// Idempotent — re-running just re-sets each binding to the same value.
//
// Bindings use the absolute path to mt so they survive PATH inheritance
// problems (the canonical bug from bash commit c68c228).
func InstallBindings(mtPath string) error {
	type binding struct{ key, cmd string }
	bs := []binding{
		{"g", "display-popup -w 80% -h 60% -E " + quote(mtPath+" switch -z")},
		{"G", "display-popup -w 80% -h 60% -E " + quote(mtPath+" switch")},
		{"N", "display-popup -w 80% -h 60% -E " + quote(mtPath+" new")},
		{"R", "display-popup -w 80% -h 60% -E " + quote(mtPath+" rm")},
	}
	for _, b := range bs {
		if err := tmuxio.BindPrefix(b.key, b.cmd); err != nil {
			return fmt.Errorf("bind-key %s: %w", b.key, err)
		}
	}
	return nil
}

// MarkPane sets the visible pane border title (informational; may be
// clobbered by the agent's OSC 2), the @mt-managed user option (stable
// source of truth), and the @mt-base option (the resolved ref the new
// worktree was branched from, rendered into the pane border). baseRef
// may be empty for panes created without a resolved base; the option is
// not set in that case. See CONTRIBUTING.md §10.
func MarkPane(id tmuxio.PaneID, title, baseRef string) error {
	// pane-title for visible border; ignore errors (cosmetic)
	_ = tmuxio.SetPaneTitle(id, title)
	if err := tmuxio.SetPaneOption(id, MtManagedKey, title); err != nil {
		return fmt.Errorf("set %s on %s: %w", MtManagedKey, id, err)
	}
	if baseRef != "" {
		if err := tmuxio.SetPaneOption(id, MtBaseKey, baseRef); err != nil {
			return fmt.Errorf("set %s on %s: %w", MtBaseKey, id, err)
		}
	}
	return nil
}

// FindPane returns the pane ID with the given mt-identity, or empty string
// if none. Reads @mt-managed (NOT pane_title) — agents like Claude rewrite
// pane_title via OSC 2 with their cwd, so it's not a reliable identifier.
func FindPane(target, title string) (tmuxio.PaneID, error) {
	panes, err := tmuxio.ListPanes(target)
	if err != nil {
		return "", err
	}
	for _, p := range panes {
		if p.MtManaged == title {
			return p.ID, nil
		}
	}
	return "", nil
}

// ListManaged returns only the panes mt has marked with @mt-managed. Bare
// shells and externally-split panes are excluded.
func ListManaged(target string) ([]tmuxio.Pane, error) {
	panes, err := tmuxio.ListPanes(target)
	if err != nil {
		return nil, err
	}
	out := make([]tmuxio.Pane, 0, len(panes))
	for _, p := range panes {
		if p.MtManaged != "" {
			out = append(out, p)
		}
	}
	return out, nil
}

// ManagedCount counts mt-managed panes on the given target. Used by `mt new`
// to decide between bare-shell-reuse (0) and split-window (>0).
func ManagedCount(target string) (int, error) {
	managed, err := ListManaged(target)
	if err != nil {
		return 0, err
	}
	return len(managed), nil
}

// quote wraps a string in double-quotes for tmux command-string contexts.
// tmux's bind-key second arg is a quoted command — paths with spaces get
// preserved by the quoting.
func quote(s string) string {
	return `"` + s + `"`
}
