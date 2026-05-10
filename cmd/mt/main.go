// Command mt is the tmux-native control plane for Claude Code and Ollama
// across git worktrees. See spec.md for the product, spec-go.md for the
// implementation.
package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/jinyuanlu/metatree/internal/command"
	"github.com/jinyuanlu/metatree/internal/config"
	"github.com/jinyuanlu/metatree/internal/mtlog"
)

// version is set by goreleaser via -ldflags '-X main.version=<tag>'.
var version = "dev"

func main() {
	command.Version = version

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "mt:", err)
		os.Exit(1)
	}

	env := command.New(cfg)

	subcmd := "show"
	args := []string{}
	if len(os.Args) > 1 {
		subcmd = os.Args[1]
		args = os.Args[2:]
	}

	mtlog.Write(fmt.Sprintf(
		"INVOKE cmd=%s args=%s tmux=%s in_tmux=%s cwd=%s",
		subcmd, strings.Join(os.Args[1:], " "),
		env.Target(), tmuxIndicator(env.InsideTmux), getenvOrEmpty("PWD"),
	))

	rc := dispatch(env, subcmd, args)
	mtlog.Write(fmt.Sprintf("EXIT cmd=%s rc=%d", subcmd, rc))
	os.Exit(rc)
}

func dispatch(env *command.Env, subcmd string, args []string) int {
	var err error
	switch subcmd {
	case "new":
		err = command.RunNew(env, args)
	case "ls":
		err = command.RunLs(env, args)
	case "rm":
		err = command.RunRm(env, args)
	case "switch", "sw":
		err = command.RunSwitch(env, args)
	case "prune":
		err = command.RunPrune(env, args)
	case "bind":
		err = command.RunBind(env, args)
	case "diagnose", "debug":
		err = command.RunDiagnose(env, args)
	case "show":
		err = command.RunShow(env, args)
	case "-h", "--help":
		err = command.RunHelp(env, args)
	default:
		_ = command.RunHelp(env, args)
		return 2
	}

	if err == nil {
		return 0
	}

	// Recover the exit code if it was wrapped via ExitWith.
	var ee *command.ExitError
	if errors.As(err, &ee) {
		// Code 0 with a message means "early-exit-no-error" (e.g. user
		// cancelled fzf); print nothing.
		if ee.Code != 0 {
			fmt.Fprintln(os.Stderr, "mt:", ee.Err.Error())
		}
		return ee.Code
	}
	fmt.Fprintln(os.Stderr, "mt:", err)
	return 1
}

func tmuxIndicator(inside bool) string {
	if inside {
		return "yes"
	}
	return ""
}

func getenvOrEmpty(k string) string { return os.Getenv(k) }
