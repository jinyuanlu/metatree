package command

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/jinyuanlu/metatree/internal/config"
)

// RunSetup is the user-facing config editor. It runs in two modes:
//
//   - Non-interactive: any of --repos-dirs, --claude-cmd, --print, or
//     --reset is passed. No prompts; the request is applied and saved
//     atomically, or printed-and-exited for --print.
//
//   - Interactive: bare `mt setup`. Two prompts (repos_dirs, claude_cmd)
//     each defaulting to the current value (or the discovery probe / a
//     placeholder if there is no current value). Hitting Enter twice
//     writes the file unchanged.
//
// Either way the result lands at config.Path() — i.e. ~/.metatree/config.toml
// unless $MT_CONFIG points elsewhere.
func RunSetup(env *Env, args []string) error {
	var (
		reposFlag  *string
		claudeFlag *string
		printFlag  bool
		resetFlag  bool
	)
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--repos-dirs":
			if i+1 >= len(args) {
				return ExitWith(2, "--repos-dirs requires a value")
			}
			v := args[i+1]
			reposFlag = &v
			i++
		case "--claude-cmd":
			if i+1 >= len(args) {
				return ExitWith(2, "--claude-cmd requires a value")
			}
			v := args[i+1]
			claudeFlag = &v
			i++
		case "--print":
			printFlag = true
		case "--reset":
			resetFlag = true
		case "-h", "--help":
			fmt.Fprint(env.Stdout, setupUsage)
			return nil
		default:
			return ExitWith(2, "unknown setup flag: %s (try `mt setup --help`)", args[i])
		}
	}

	// Load existing config if any; otherwise start from Default(). We
	// bypass EnsureConfig here so `mt setup` works even when no config
	// exists yet — that's literally its job.
	cfg, _ := loadOrDefault()

	if resetFlag {
		cfg = config.Default()
	}

	// Apply non-interactive flags first; they ALWAYS suppress prompts.
	flagsTouched := reposFlag != nil || claudeFlag != nil
	if reposFlag != nil {
		cfg.ReposDirs = parseCSV(*reposFlag)
	}
	if claudeFlag != nil {
		cfg.ClaudeCmd = strings.TrimSpace(*claudeFlag)
		if cfg.ClaudeCmd == "" {
			cfg.ClaudeCmd = config.Default().ClaudeCmd
		}
	}

	if printFlag {
		// Resolve the in-memory cfg through Save+Load for fidelity, but
		// don't actually touch the disk — encode straight to stdout.
		return writeTOMLTo(env.Stdout, cfg)
	}

	if !flagsTouched && !resetFlag {
		// Interactive mode.
		if err := promptInteractively(env, cfg); err != nil {
			return ExitWith(1, "%v", err)
		}
	}

	path := config.Path()
	if err := config.Save(cfg, path); err != nil {
		return ExitWith(1, "%v", err)
	}
	fmt.Fprintf(env.Stderr, "mt: saved %s\n", path)
	return nil
}

const setupUsage = `mt setup — configure mt and persist to ~/.metatree/config.toml

Usage:
  mt setup                                            # interactive (2 prompts)
  mt setup --repos-dirs ~/work,~/oss                  # set repos_dirs only
  mt setup --claude-cmd "claude --mcp-config ~/x"     # set claude_cmd only
  mt setup --repos-dirs ~/work --claude-cmd "claude"  # both, atomic write
  mt setup --print                                    # dump resolved config to stdout
  mt setup --reset                                    # forget current config; rewrite Default

When any --<field> flag is passed, prompts are suppressed for ALL fields
(unset fields keep their current values). Re-runnable any time.
`

// loadOrDefault returns the on-disk config if Path() exists and parses
// cleanly; otherwise a fresh Default(). Any decode error short-circuits
// with a precise message — better than "missing field" surprises mid-
// interactive session. We deliberately do NOT call EnsureConfig here:
// `mt setup` is the canonical way to bootstrap a config from nothing.
func loadOrDefault() (*config.Config, error) {
	if _, err := os.Stat(config.Path()); errors.Is(err, os.ErrNotExist) {
		c := config.Default()
		c.Path = config.Path()
		return c, nil
	}
	return config.Load()
}

// promptInteractively asks the user for repos_dirs and claude_cmd, in
// that order. Each prompt uses the current cfg value as the bracketed
// default; for an empty repos_dirs it falls back to DiscoverReposDirs(),
// then to "~/Code" so there's always something to accept by hitting Enter.
func promptInteractively(env *Env, cfg *config.Config) error {
	fmt.Fprintf(env.Stderr, "mt: current config: %s\n\n", config.Path())

	// repos_dirs prompt
	currentDirs := cfg.ReposDirs
	defaultDirs := currentDirs
	if len(defaultDirs) == 0 {
		defaultDirs = config.DiscoverReposDirs()
	}
	if len(defaultDirs) == 0 {
		defaultDirs = []string{"~/Code"}
	}
	fmt.Fprintf(env.Stderr, "  repos_dirs (comma-separated, ~ allowed) [%s]: ",
		strings.Join(defaultDirs, ", "))
	dirs, err := readLineCSV(env.Stdin, defaultDirs)
	if err != nil {
		return err
	}
	cfg.ReposDirs = dirs

	// claude_cmd prompt — accepted literally. This is the structural
	// fix for the alias-expansion bug: users who want MCP set the full
	// command here and never rely on shell aliases.
	currentClaude := cfg.ClaudeCmd
	if currentClaude == "" {
		currentClaude = config.Default().ClaudeCmd
	}
	fmt.Fprintf(env.Stderr, "  claude_cmd                              [%s]: ", currentClaude)
	claude, err := readLine(env.Stdin)
	if err != nil {
		return err
	}
	if v := strings.TrimSpace(claude); v != "" {
		cfg.ClaudeCmd = v
	} else {
		cfg.ClaudeCmd = currentClaude
	}
	return nil
}

// readLine reads one line from r without the trailing newline. EOF on
// the first byte is treated as an empty line so piped `printf '\n\n'`
// can drive both prompts.
func readLine(r io.Reader) (string, error) {
	br := bufio.NewReader(r)
	line, err := br.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// readLineCSV reads one line, splits/trims on comma, and returns the
// result; an empty line returns a fresh copy of fallback.
func readLineCSV(r io.Reader, fallback []string) ([]string, error) {
	line, err := readLine(r)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(line) == "" {
		out := make([]string, len(fallback))
		copy(out, fallback)
		return out, nil
	}
	return parseCSV(line), nil
}

// parseCSV splits "a, b ,c" into ["a","b","c"], dropping empties.
func parseCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if v := strings.TrimSpace(p); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// writeTOMLTo encodes cfg as TOML to w (used by --print). We round-trip
// through Save+Load to a tempfile and stream the bytes — that way the
// output is byte-identical to what Save would persist, including the
// header comment. Avoids a parallel encoding path that could drift.
func writeTOMLTo(w io.Writer, cfg *config.Config) error {
	tmp, err := os.CreateTemp("", "mt-print-*.toml")
	if err != nil {
		return ExitWith(1, "tempfile: %v", err)
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()
	defer os.Remove(tmpPath)

	if err := config.Save(cfg, tmpPath); err != nil {
		return ExitWith(1, "%v", err)
	}
	b, err := os.ReadFile(tmpPath)
	if err != nil {
		return ExitWith(1, "read tempfile: %v", err)
	}
	if _, err := w.Write(b); err != nil {
		return ExitWith(1, "write stdout: %v", err)
	}
	return nil
}
