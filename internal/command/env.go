// Package command holds the per-subcommand entry points for mt. Each
// subcommand is a Run<Name>(env *Env, args []string) error function. They
// orchestrate calls into config, dashboard, gitio, and tmuxio — they do NOT
// shell out directly. See spec-go.md §13 for the contract.
package command

import (
	"io"
	"os"

	"github.com/jinyuanlu/metatree/internal/config"
	"github.com/jinyuanlu/metatree/internal/tmuxio"
)

// Env carries everything a subcommand needs. Built once in main.go.
type Env struct {
	Config      *config.Config
	InsideTmux  bool
	SessionName tmuxio.SessionName // overridden from current tmux when inside
	WindowName  tmuxio.WindowName

	Stdout io.Writer
	Stderr io.Writer
	Stdin  io.Reader // for mt new branch prompt
}

// New builds an Env from a loaded config + ambient tmux state. Honors the
// auto-detect rule from spec-go.md §13: when invoked from inside tmux,
// the calling session/window override config.
func New(cfg *config.Config) *Env {
	env := &Env{
		Config:      cfg,
		SessionName: tmuxio.SessionName(cfg.TmuxSession),
		WindowName:  tmuxio.WindowName(cfg.TmuxWindow),
		Stdout:      os.Stdout,
		Stderr:      os.Stderr,
		Stdin:       os.Stdin,
	}

	if tmuxio.InsideTmux() {
		env.InsideTmux = true
		if s, err := tmuxio.CurrentSession(); err == nil && s != "" {
			env.SessionName = s
		}
		if w, err := tmuxio.CurrentWindow(); err == nil && w != "" {
			env.WindowName = w
		}
	}
	return env
}

// Target returns "session:window" for tmux command targets.
func (e *Env) Target() string {
	return string(e.SessionName) + ":" + string(e.WindowName)
}
