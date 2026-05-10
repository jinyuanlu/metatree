package command

import (
	"os"
	"path/filepath"
	"strings"
)

// wrapAgentCmd shapes the agent invocation so that the user's interactive
// shell aliases for `claude_cmd` / `ollama_cmd` expand when the pane is
// created via `tmux split-window`.
//
// Why this exists: tmux dispatches the command argument of `split-window`
// through `/bin/sh -c`, which is non-interactive and never sources
// ~/.zshrc, ~/.bashrc, or config.fish. Any alias the user defined for
// `claude` (e.g. `alias claude='claude --mcp-config ~/.claude/mcp.json'`)
// is silently dropped — the agent launches without the flags the user
// expects.
//
// For recognized POSIX-aliasable shells (bash, zsh, fish) we wrap as:
//
//	<shell> -ic '<cmd>'
//
// Critical detail: we do NOT prefix the inner command with `exec`. In
// both bash and zsh, `exec <name>` is a special-builtin form that
// suppresses alias expansion on its argument — so `exec claude` would
// invoke the literal `claude` binary on PATH, defeating the very
// expansion we are trying to enable. (See `exec` in bash(1): the `exec`
// builtin treats its argument as a literal command name without
// re-running alias substitution.) The trailing `bash -ic '<cmd>'` form
// is fine: when `<cmd>` finishes, the wrapping shell exits because `-c`
// shells exit with the command's status, and tmux closes the pane.
//
// For unrecognized shells (nu, pwsh, dash, busybox ash, empty $SHELL) we
// pass cmd through unwrapped — same behavior as today, no regression —
// and signal recognized=false so the caller can warn the user once and
// point them at `claude_cmd` in config.toml.
func wrapAgentCmd(cmd string) (wrapped string, recognized bool) {
	shell := os.Getenv("SHELL")
	switch filepath.Base(shell) {
	case "bash", "zsh", "fish":
		return shell + " -ic " + shellSingleQuote(cmd), true
	default:
		return cmd, false
	}
}

// shellSingleQuote wraps s in POSIX single quotes. Embedded single
// quotes are escaped via the standard close-quote / backslash-quote /
// open-quote dance — see the replacement target literal below. Works
// identically in bash, zsh, and fish.
func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// shellOrUnset returns $SHELL or the literal string "(unset)" — used for
// the unrecognized-shell warning so the message is unambiguous when the
// user has no SHELL set at all.
func shellOrUnset() string {
	if s := os.Getenv("SHELL"); s != "" {
		return s
	}
	return "(unset)"
}
