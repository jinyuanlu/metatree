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

// version, commit, and date are stamped at build time via -ldflags. See
// .goreleaser.yaml (release builds) and Justfile / .github/workflows/ci.yml
// (local + CI builds). `dev` defaults are visible if any build path
// forgets to inject them — which is itself a useful smoke signal.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	command.Version = version
	command.Commit = commit
	command.BuildDate = date

	// Fast-path: --version / -v / version. Print the build identity and
	// exit before touching config or tmux. Keeps the version readable
	// even when config is broken.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--version", "-v", "version":
			command.PrintVersion(os.Stdout)
			os.Exit(0)
		}
	}

	subcmd := "show"
	args := []string{}
	if len(os.Args) > 1 {
		subcmd = os.Args[1]
		args = os.Args[2:]
	}

	// First-run / migration hook. We run this BEFORE config.Load() so:
	//   - a brand-new machine gets a config seeded from probe or prompt;
	//   - an upgrade from the legacy ~/.config/mt/ layout migrates once;
	//   - and config.Load() always sees a file (or returns Default()).
	//
	// Skip for subcommands that must work without a config: setup
	// (it's literally how you create one), upgrade (just downloads a
	// binary), and the meta read-only inspection (--version, --help).
	if needsConfigBootstrap(subcmd) {
		if err := command.EnsureConfig(os.Stdin, os.Stdout, os.Stderr); err != nil {
			if errors.Is(err, command.ErrNoConfig) {
				fmt.Fprintln(os.Stderr,
					"mt: no config at ~/.metatree/config.toml and no common code folder under $HOME.")
				fmt.Fprintln(os.Stderr,
					"    run `mt setup` from a terminal to configure (one prompt).")
				os.Exit(1)
			}
			fmt.Fprintln(os.Stderr, "mt:", err)
			os.Exit(1)
		}
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "mt:", err)
		os.Exit(1)
	}

	env := command.New(cfg)

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
	case "setup":
		err = command.RunSetup(env, args)
	case "upgrade":
		err = command.RunUpgrade(env, args)
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

// needsConfigBootstrap reports whether the given subcommand should
// trigger first-run setup / legacy migration. setup and upgrade are
// excluded so they remain functional even when there's no config (and
// `mt setup` is literally how the user creates one). --version / -v /
// version / --help / -h short-circuit before reaching this hook.
func needsConfigBootstrap(subcmd string) bool {
	switch subcmd {
	case "setup", "upgrade", "-h", "--help":
		return false
	}
	return true
}
