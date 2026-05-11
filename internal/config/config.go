// Package config defines the on-disk TOML configuration for mt and provides
// the single source of truth for default values.
//
// The schema mirrors spec.md §2.6 and spec-go.md §7. Bash-era configs use
// quoted booleans (auto_direnv_allow = "true"); Go-era configs prefer native
// booleans (auto_direnv_allow = true). Both forms are accepted so users
// migrating from the bash port don't have to rewrite their config.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// Config is the resolved mt configuration. Values are populated from
// Default(), then overlaid with whatever the on-disk TOML supplies.
//
// Path is informational only ("-" tag excludes it from TOML round-trips).
type Config struct {
	ReposDirs        []string `toml:"repos_dirs"`
	Repos            []string `toml:"repos"`
	TmuxSession      string   `toml:"tmux_session"`
	TmuxWindow       string   `toml:"tmux_window"`
	BranchPrefix     string   `toml:"branch_prefix"`
	WorktreeSubdir   string   `toml:"worktree_subdir"`
	DefaultBackend   string   `toml:"default_backend"`
	OllamaModel      string   `toml:"ollama_model"`
	ClaudeCmd         string   `toml:"claude_cmd"`
	OllamaCmd         string   `toml:"ollama_cmd"`
	WorktreeCopyFiles []string `toml:"worktree_copy_files"`
	AutoDirenvAllow   flexBool `toml:"auto_direnv_allow"`
	AutoStatusChrome  flexBool `toml:"auto_status_chrome"`

	Path string `toml:"-"`
}

// flexBool accepts both native TOML bools (true) and quoted strings ("true")
// so configs written for the bash version of mt continue to parse.
//
// We intentionally do not use a plain bool field with a pointer or
// interface{} workaround: the explicit type makes the back-compat shim
// visible and locally testable.
type flexBool bool

// UnmarshalTOML implements toml.Unmarshaler. It accepts any of:
//   - bool: native TOML boolean (true / false)
//   - string: "true" / "false" / "1" / "0" (case-insensitive via strconv)
//
// Anything else is rejected with a typed error so the caller can wrap it.
func (b *flexBool) UnmarshalTOML(data any) error {
	switch v := data.(type) {
	case bool:
		*b = flexBool(v)
		return nil
	case string:
		parsed, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("invalid bool string %q: %w", v, err)
		}
		*b = flexBool(parsed)
		return nil
	default:
		return fmt.Errorf("expected bool or string, got %T", data)
	}
}

// Bool returns the underlying boolean. Provided so callers don't have to
// remember the flexBool conversion at use sites.
func (b flexBool) Bool() bool { return bool(b) }

// defaultReposDirCandidates lists common locations users keep their
// code on macOS and Linux, in priority order. DiscoverReposDirs probes
// each one and returns those that exist. Order matters for the order
// they appear in `mt setup`'s default-suggestion line — we prefer
// Apple's blessed `~/Developer` first, then conventional capitalized
// variants, then lowercase Unix conventions.
//
// Important: this list is no longer applied silently at runtime. It's
// only consulted by `mt setup` (and the implicit first-run setup) as
// the bracketed default of the repos_dirs prompt. Default() returns
// empty ReposDirs so configuration is always explicit.
var defaultReposDirCandidates = []string{
	"Developer", // Apple's recommended location, gets a special Finder icon
	"Code",      // common for JetBrains/VSCode users
	"code",      // lowercase variant
	"Projects",
	"projects",
	"dev",
	"src",
	"git",
	"repos",
	"workspace",
}

// Default returns a Config with every field populated to the values
// documented in mt.sh (the bash reference implementation). Mutating the
// returned pointer is safe — it's a fresh value every call.
//
// ReposDirs is intentionally empty. Callers that need a default
// suggestion (e.g. mt setup) should call DiscoverReposDirs() explicitly.
// Runtime code that hits an empty ReposDirs falls through to a clear
// "run mt setup" error instead of a silently-broken probe.
func Default() *Config {
	return &Config{
		ReposDirs:        nil,
		Repos:            nil,
		TmuxSession:      "mt",
		TmuxWindow:       "dashboard",
		BranchPrefix:     "mt",
		WorktreeSubdir:   ".worktrees",
		DefaultBackend:   "claude",
		OllamaModel:      "llama3:8b",
		ClaudeCmd:         "claude",
		OllamaCmd:         "ollama run {model}",
		WorktreeCopyFiles: []string{".env", ".envrc", ".npmrc"},
		AutoDirenvAllow:   true,
		AutoStatusChrome:  true,
	}
}

// DiscoverReposDirs returns the subset of defaultReposDirCandidates
// that exist as directories under $HOME, in priority order. Returns
// nil if none match. Used by mt setup as the bracketed default for
// the repos_dirs prompt — never applied at runtime to avoid hidden
// behavior across machines.
func DiscoverReposDirs() []string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil
	}
	var found []string
	for _, name := range defaultReposDirCandidates {
		p := filepath.Join(home, name)
		fi, err := os.Stat(p)
		if err != nil || !fi.IsDir() {
			continue
		}
		found = append(found, p)
	}
	return found
}

// Path returns the resolved canonical configuration path. $MT_CONFIG
// wins if set; otherwise the canonical location is ~/.metatree/config.toml.
// We do not stat the path here — Load() and the firstrun migration own
// the "missing-file is fine" decision.
//
// Why ~/.metatree/ and not ~/.config/mt/? Per-machine first-run setup
// data needs to survive `mt upgrade`, and a single visible directory
// is easier for users to find and back up than an XDG-compliant path
// buried under .config. The legacy ~/.config/mt/config.toml is still
// read once on first run via LegacyPath() — see firstrun.EnsureConfig.
func Path() string {
	if p := os.Getenv("MT_CONFIG"); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".metatree", "config.toml")
}

// LegacyPath returns the pre-v1.1 config location. Read-only consumers:
// firstrun.EnsureConfig migrates this file to Path() on first invocation
// after upgrade; nothing else should touch it.
func LegacyPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "mt", "config.toml")
}

// Save writes cfg to path as TOML, creating parent dirs as needed.
// The file is written via temp+rename so a crash mid-write can't leave
// a half-written config. Permissions: 0o755 on the dir, 0o600 on the
// file (config may eventually contain tokens; default to user-only).
//
// Path is set on cfg as a side effect so callers that immediately use
// cfg can rely on cfg.Path being current.
func Save(cfg *Config, path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("config save: mkdir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".config.toml.*")
	if err != nil {
		return fmt.Errorf("config save: tempfile in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()
	// Best-effort cleanup if anything below fails.
	cleanup := func() { _ = os.Remove(tmpPath) }
	if _, err := fmt.Fprintln(tmp, "# mt configuration — managed by `mt setup`. Edit by hand if you prefer."); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("config save: write header: %w", err)
	}
	if err := toml.NewEncoder(tmp).Encode(cfg); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("config save: encode: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("config save: chmod: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("config save: close tempfile: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		cleanup()
		return fmt.Errorf("config save: rename %s -> %s: %w", tmpPath, path, err)
	}
	cfg.Path = path
	return nil
}

// Load builds a Config by overlaying the on-disk TOML file (if present) on
// top of Default(). A missing file is not an error — defaults win.
//
// Errors from os.Stat other than os.ErrNotExist propagate. TOML decode
// errors are wrapped per the public-API convention.
func Load() (*Config, error) {
	cfg := Default()
	path := Path()
	cfg.Path = path

	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, nil
		}
		return nil, fmt.Errorf("config %s: %w", "stat", err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("config %s: %w", "stat", fmt.Errorf("%s is a directory", path))
	}

	if _, err := toml.DecodeFile(path, cfg); err != nil {
		return nil, fmt.Errorf("config %s: %w", "decode", err)
	}

	deduped, err := validateWorktreeCopyFiles(cfg.WorktreeCopyFiles)
	if err != nil {
		return nil, fmt.Errorf("config %s: %w", "validate", err)
	}
	cfg.WorktreeCopyFiles = deduped

	return cfg, nil
}

func validateWorktreeCopyFiles(names []string) ([]string, error) {
	if names == nil {
		return nil, nil
	}
	seen := make(map[string]struct{}, len(names))
	out := make([]string, 0, len(names))
	for _, name := range names {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" {
			return nil, errors.New("invalid worktree_copy_files entry: must not be empty or whitespace")
		}
		if strings.Contains(trimmed, "..") {
			return nil, fmt.Errorf("invalid worktree_copy_files entry %q: parent-directory references not allowed", trimmed)
		}
		if filepath.IsAbs(trimmed) {
			return nil, fmt.Errorf("invalid worktree_copy_files entry %q: absolute paths not allowed", trimmed)
		}
		if strings.ContainsRune(trimmed, '/') || strings.ContainsRune(trimmed, filepath.Separator) {
			return nil, fmt.Errorf("invalid worktree_copy_files entry %q: subdirectory paths not supported in v1, use a single filename", trimmed)
		}
		if _, dup := seen[trimmed]; dup {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out, nil
}
