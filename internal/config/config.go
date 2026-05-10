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
	ClaudeCmd        string   `toml:"claude_cmd"`
	OllamaCmd        string   `toml:"ollama_cmd"`
	AutoDirenvAllow  flexBool `toml:"auto_direnv_allow"`
	AutoStatusChrome flexBool `toml:"auto_status_chrome"`

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

// Default returns a Config with every field populated to the values
// documented in mt.sh (the bash reference implementation). Mutating the
// returned pointer is safe — it's a fresh value every call.
func Default() *Config {
	home, _ := os.UserHomeDir()
	codeDir := filepath.Join(home, "Code")

	return &Config{
		ReposDirs:        []string{codeDir},
		Repos:            nil,
		TmuxSession:      "mt",
		TmuxWindow:       "dashboard",
		BranchPrefix:     "mt",
		WorktreeSubdir:   ".worktrees",
		DefaultBackend:   "claude",
		OllamaModel:      "llama3:8b",
		ClaudeCmd:        "claude",
		OllamaCmd:        "ollama run {model}",
		AutoDirenvAllow:  true,
		AutoStatusChrome: true,
	}
}

// Path returns the resolved configuration path. $MT_CONFIG wins if set;
// otherwise we fall back to ~/.config/mt/config.toml. We do not stat the
// path here — Load() owns the "missing-file is fine" decision.
func Path() string {
	if p := os.Getenv("MT_CONFIG"); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "mt", "config.toml")
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
	return cfg, nil
}
