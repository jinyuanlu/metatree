package command

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/jinyuanlu/metatree/internal/config"
)

// firstRunResult tells diagnose whether the *current* invocation
// performed a migration or first-run setup. RunDiagnose reads this so
// it can label cfg.Path with `(via legacy migration)` etc. — purely
// cosmetic, but useful when triaging a peer's bug report.
//
// Only the most recent EnsureConfig() call's signal is kept; the value
// is not persisted across invocations.
var firstRunResult = "" // "", "migrated", "auto-setup", "interactive-setup"

// LastFirstRunResult returns the signal recorded by the most recent
// EnsureConfig call in this process. Empty string means "neither
// migration nor first-run setup happened — config existed already."
func LastFirstRunResult() string { return firstRunResult }

// EnsureConfig is the single entry point for "make sure a config file
// exists at config.Path() before we run any subcommand that depends on
// it." Resolution order:
//
//  1. config exists at canonical Path() → return (no-op).
//  2. legacy ~/.config/mt/config.toml exists → migrate it (parse, then
//     Save() to canonical) and print a one-line stderr notice.
//  3. probe DiscoverReposDirs(); if non-empty, write a fresh config
//     with those dirs and return zero-prompt.
//  4. stdin is a TTY → run an interactive one-question prompt.
//  5. otherwise → return ErrNoConfig so the caller prints a clear
//     "run mt setup from a terminal" message.
//
// The function is safe to call repeatedly — once the canonical file
// exists, subsequent calls return immediately.
//
// stdin/stdout/stderr are taken via the parameters so tests can drive
// the interactive branch with bytes.Buffers.
func EnsureConfig(stdin io.Reader, stdout, stderr io.Writer) error {
	firstRunResult = ""
	canonical := config.Path()
	if _, err := os.Stat(canonical); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("first-run: stat %s: %w", canonical, err)
	}

	// 2. Legacy migration. Read+decode (not just copy) so a malformed
	//    legacy file fails loudly here rather than confusing the user
	//    later in the lifecycle.
	legacy := config.LegacyPath()
	if canonical != legacy { // MT_CONFIG override could collapse them
		if _, err := os.Stat(legacy); err == nil {
			cfg := config.Default()
			if _, err := toml.DecodeFile(legacy, cfg); err != nil {
				return fmt.Errorf("first-run: decode legacy %s: %w", legacy, err)
			}
			if err := config.Save(cfg, canonical); err != nil {
				return fmt.Errorf("first-run: %w", err)
			}
			fmt.Fprintf(stderr,
				"mt: migrated config from %s → %s (legacy file kept; remove when ready).\n",
				legacy, canonical)
			firstRunResult = "migrated"
			return nil
		}
	}

	// 3. Zero-question first run if the probe finds anything.
	cfg := config.Default()
	probed := config.DiscoverReposDirs()
	if len(probed) > 0 {
		cfg.ReposDirs = probed
		if err := config.Save(cfg, canonical); err != nil {
			return fmt.Errorf("first-run: %w", err)
		}
		fmt.Fprintf(stderr,
			"mt: first-run — using %s as repos_dirs and `claude` as claude_cmd.\n"+
				"    Run `mt setup` to change either (e.g. add --mcp-config flags).\n",
			strings.Join(probed, ", "))
		firstRunResult = "auto-setup"
		return nil
	}

	// 4. One-question interactive setup if we have a TTY.
	if isTerminal(stdin) {
		fmt.Fprintln(stderr, "mt: first-run setup. Where do you keep your code?")
		fmt.Fprint(stderr, "  repos_dirs (comma-separated, ~ allowed) [~/Code]: ")
		dirs, err := readReposDirs(stdin, []string{"~/Code"})
		if err != nil {
			return fmt.Errorf("first-run: read input: %w", err)
		}
		cfg.ReposDirs = dirs
		if err := config.Save(cfg, canonical); err != nil {
			return fmt.Errorf("first-run: %w", err)
		}
		fmt.Fprintf(stderr, "mt: saved %s. Run `mt setup` to change.\n", canonical)
		firstRunResult = "interactive-setup"
		return nil
	}

	// 5. No TTY, no probe match, no legacy — bail with a precise message.
	return ErrNoConfig
}

// ErrNoConfig is returned by EnsureConfig when there's no config, no
// legacy file to migrate, no common code folder under $HOME, and stdin
// isn't a terminal so we can't ask. main.go translates this to a
// clear "run mt setup from a terminal" exit message.
var ErrNoConfig = errors.New("no config and no candidate dirs to seed it; run `mt setup` from a terminal")

// readReposDirs reads one line from r, splits on commas, trims each
// piece, and returns the result. Empty input falls back to fallback.
// Tilde stays unexpanded — the runtime expands it via gitio.expandTilde
// at use sites, which keeps the on-disk file portable across users.
func readReposDirs(r io.Reader, fallback []string) ([]string, error) {
	br := bufio.NewReader(r)
	line, err := br.ReadString('\n')
	if err != nil && err != io.EOF {
		return nil, err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		out := make([]string, len(fallback))
		copy(out, fallback)
		return out, nil
	}
	parts := strings.Split(line, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if v := strings.TrimSpace(p); v != "" {
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		out = append(out, fallback...)
	}
	return out, nil
}

// isTerminal reports whether r is a *os.File pointing at a TTY. Any
// other reader (bytes.Buffer in tests, a pipe, etc.) returns false —
// EnsureConfig falls through to the explicit-setup error in that case,
// which is exactly what we want for non-interactive callers like CI.
func isTerminal(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

// expandHomePath does NOT live here — gitio.expandTilde is the single
// source of truth for ~ expansion. Kept here as a comment so future
// readers don't try to add a duplicate; if you need it, import gitio.
var _ = filepath.Join // keep filepath imported for clarity in future edits
