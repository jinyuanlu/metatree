package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// withConfigEnv points $MT_CONFIG at path for the duration of the test
// and restores the previous value on cleanup. Tests must use this rather
// than os.Setenv directly so parallel runs don't leak state into each other.
func withConfigEnv(t *testing.T, path string) {
	t.Helper()
	t.Setenv("MT_CONFIG", path)
}

// writeConfig dumps content to a temp file and returns the path.
// The file is cleaned up automatically via t.TempDir().
func writeConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	return path
}

func TestDefault(t *testing.T) {
	cfg := Default()
	if cfg == nil {
		t.Fatal("Default() returned nil")
	}

	// ReposDirs is empty by design — Default() never injects probed
	// values silently; the bracketed default for `mt setup` comes from
	// DiscoverReposDirs(). See TestDiscoverReposDirsProbesCommonDirs.
	if len(cfg.ReposDirs) != 0 {
		t.Errorf("ReposDirs = %v, want empty", cfg.ReposDirs)
	}
	if cfg.TmuxSession != "mt" {
		t.Errorf("TmuxSession = %q, want %q", cfg.TmuxSession, "mt")
	}
	if cfg.TmuxWindow != "dashboard" {
		t.Errorf("TmuxWindow = %q, want %q", cfg.TmuxWindow, "dashboard")
	}
	if cfg.BranchPrefix != "mt" {
		t.Errorf("BranchPrefix = %q, want %q", cfg.BranchPrefix, "mt")
	}
	if cfg.WorktreeSubdir != ".worktrees" {
		t.Errorf("WorktreeSubdir = %q, want %q", cfg.WorktreeSubdir, ".worktrees")
	}
	if cfg.DefaultBackend != "claude" {
		t.Errorf("DefaultBackend = %q, want %q", cfg.DefaultBackend, "claude")
	}
	if cfg.OllamaModel != "llama3:8b" {
		t.Errorf("OllamaModel = %q, want %q", cfg.OllamaModel, "llama3:8b")
	}
	if cfg.ClaudeCmd != "claude" {
		t.Errorf("ClaudeCmd = %q, want %q", cfg.ClaudeCmd, "claude")
	}
	if cfg.OllamaCmd != "ollama run {model}" {
		t.Errorf("OllamaCmd = %q, want %q", cfg.OllamaCmd, "ollama run {model}")
	}
	if !cfg.AutoDirenvAllow.Bool() {
		t.Error("AutoDirenvAllow should default true")
	}
	if !cfg.AutoStatusChrome.Bool() {
		t.Error("AutoStatusChrome should default true")
	}
}

func TestDefaultIsFresh(t *testing.T) {
	// Defensive: mutating one Default() must not leak into the next.
	a := Default()
	a.TmuxSession = "scribbled"
	a.ReposDirs = append(a.ReposDirs, "/tmp/x")

	b := Default()
	if b.TmuxSession != "mt" {
		t.Errorf("Default() returned shared state: TmuxSession = %q", b.TmuxSession)
	}
	if len(b.ReposDirs) != 0 {
		t.Errorf("Default() returned shared ReposDirs slice: %v", b.ReposDirs)
	}
}

// TestDiscoverReposDirsProbesCommonDirs verifies the discovery helper
// (used by `mt setup` as its bracketed default — never injected by
// Default at runtime). Override HOME to a tempdir, create a subset of
// the candidate dirs out of priority order, and confirm the result
// preserves the candidate list order, not filesystem order.
func TestDiscoverReposDirsProbesCommonDirs(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	for _, name := range []string{"dev", "Developer", "src"} {
		if err := os.MkdirAll(filepath.Join(tmp, name), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
	}

	got := DiscoverReposDirs()
	want := []string{
		filepath.Join(tmp, "Developer"),
		filepath.Join(tmp, "dev"),
		filepath.Join(tmp, "src"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DiscoverReposDirs() = %v, want %v", got, want)
	}
}

// TestDiscoverReposDirsEmptyWhenNoneExist confirms the slice is empty
// (not a phantom `~/Code` entry) when none of the candidate dirs exist.
func TestDiscoverReposDirsEmptyWhenNoneExist(t *testing.T) {
	tmp := t.TempDir() // has nothing under it
	t.Setenv("HOME", tmp)

	if got := DiscoverReposDirs(); len(got) != 0 {
		t.Errorf("expected empty discovery in HOME=%s, got %v", tmp, got)
	}
}

// TestSaveLoadRoundTrip verifies that a Config written by Save() reloads
// to a deep-equal value. Catches encoder/decoder drift, missing struct
// tags, and any future field that needs round-trip fidelity. flexBool's
// custom unmarshal makes this non-trivial — bools survive as bools.
func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	withConfigEnv(t, path)

	want := Default()
	want.ReposDirs = []string{"~/work", "~/oss"}
	want.ClaudeCmd = "claude --mcp-config ~/.claude/mcp.json"
	want.Path = path

	if err := Save(want, path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round-trip mismatch:\n  got  = %+v\n  want = %+v", got, want)
	}
}

// TestSaveCreatesParentDirs confirms Save mkdirs intermediate
// directories (~/.metatree on a fresh machine doesn't exist).
func TestSaveCreatesParentDirs(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "a", "b", "c", "config.toml")
	if err := Save(Default(), nested); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := os.Stat(nested); err != nil {
		t.Errorf("Save did not create %s: %v", nested, err)
	}
}

// TestPathDefaultsToMetatree pins the canonical location. If you find
// yourself reaching for this test to "just point it elsewhere," stop —
// the choice was deliberate so config survives `mt upgrade`. See the
// comment on Path() for rationale.
func TestPathDefaultsToMetatree(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("MT_CONFIG", "")

	got := Path()
	want := filepath.Join(tmp, ".metatree", "config.toml")
	if got != want {
		t.Errorf("Path() = %q, want %q", got, want)
	}
}

func TestLoadMissingFile(t *testing.T) {
	// Point env at a path that definitely doesn't exist.
	withConfigEnv(t, filepath.Join(t.TempDir(), "does-not-exist.toml"))

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() with missing file returned error: %v", err)
	}
	want := Default()
	want.Path = Path()

	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("Load() with missing file = %+v, want %+v", cfg, want)
	}
}

func TestLoadEmpty(t *testing.T) {
	path := writeConfig(t, "")
	withConfigEnv(t, path)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() empty file: %v", err)
	}
	want := Default()
	want.Path = path
	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("Load() empty file = %+v, want %+v", cfg, want)
	}
}

func TestLoadNativeBool(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantDirenv bool
		wantChrome bool
	}{
		{"both true", "auto_direnv_allow = true\nauto_status_chrome = true\n", true, true},
		{"both false", "auto_direnv_allow = false\nauto_status_chrome = false\n", false, false},
		{"mixed", "auto_direnv_allow = false\nauto_status_chrome = true\n", false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := writeConfig(t, tc.body)
			withConfigEnv(t, path)

			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.AutoDirenvAllow.Bool() != tc.wantDirenv {
				t.Errorf("AutoDirenvAllow = %v, want %v", cfg.AutoDirenvAllow.Bool(), tc.wantDirenv)
			}
			if cfg.AutoStatusChrome.Bool() != tc.wantChrome {
				t.Errorf("AutoStatusChrome = %v, want %v", cfg.AutoStatusChrome.Bool(), tc.wantChrome)
			}
		})
	}
}

func TestLoadStringBool(t *testing.T) {
	// Bash-era configs quote bools. Must still parse.
	tests := []struct {
		name       string
		body       string
		wantDirenv bool
		wantChrome bool
	}{
		{"quoted true", `auto_direnv_allow = "true"` + "\n" + `auto_status_chrome = "true"` + "\n", true, true},
		{"quoted false", `auto_direnv_allow = "false"` + "\n" + `auto_status_chrome = "false"` + "\n", false, false},
		{"mixed", `auto_direnv_allow = "false"` + "\n" + `auto_status_chrome = "true"` + "\n", false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := writeConfig(t, tc.body)
			withConfigEnv(t, path)

			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.AutoDirenvAllow.Bool() != tc.wantDirenv {
				t.Errorf("AutoDirenvAllow = %v, want %v", cfg.AutoDirenvAllow.Bool(), tc.wantDirenv)
			}
			if cfg.AutoStatusChrome.Bool() != tc.wantChrome {
				t.Errorf("AutoStatusChrome = %v, want %v", cfg.AutoStatusChrome.Bool(), tc.wantChrome)
			}
		})
	}
}

func TestLoadStringBoolInvalid(t *testing.T) {
	// Garbage strings must fail loudly — silent fallback would mask
	// typos like "ture".
	path := writeConfig(t, `auto_direnv_allow = "ture"`+"\n")
	withConfigEnv(t, path)

	_, err := Load()
	if err == nil {
		t.Fatal("Load() with invalid bool string should error")
	}
	if !strings.Contains(err.Error(), "config decode") {
		t.Errorf("error %q does not mention 'config decode'", err.Error())
	}
}

func TestLoadComments(t *testing.T) {
	body := `# top-level comment
# another one

# blank lines above are fine
tmux_session = "alt"  # trailing comment

# no value below
branch_prefix = "feat"
`
	path := writeConfig(t, body)
	withConfigEnv(t, path)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.TmuxSession != "alt" {
		t.Errorf("TmuxSession = %q, want %q", cfg.TmuxSession, "alt")
	}
	if cfg.BranchPrefix != "feat" {
		t.Errorf("BranchPrefix = %q, want %q", cfg.BranchPrefix, "feat")
	}
	// Untouched fields keep defaults.
	if cfg.TmuxWindow != "dashboard" {
		t.Errorf("TmuxWindow = %q, want default", cfg.TmuxWindow)
	}
}

func TestLoadReposDirs(t *testing.T) {
	body := `repos_dirs = ["~/Code", "/srv/projects"]` + "\n"
	path := writeConfig(t, body)
	withConfigEnv(t, path)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []string{"~/Code", "/srv/projects"}
	if !reflect.DeepEqual(cfg.ReposDirs, want) {
		t.Errorf("ReposDirs = %v, want %v", cfg.ReposDirs, want)
	}
	if len(cfg.Repos) != 0 {
		t.Errorf("Repos = %v, want empty (none in file)", cfg.Repos)
	}
}

func TestLoadReposExplicit(t *testing.T) {
	// When `repos = [...]` is set, it's the override channel. We don't
	// enforce the override at parse time (that's discover_repos's job),
	// but we *do* round-trip both fields verbatim.
	body := `repos_dirs = ["~/Code"]
repos = ["~/Code/soma", "~/Code/axon"]
`
	path := writeConfig(t, body)
	withConfigEnv(t, path)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	wantDirs := []string{"~/Code"}
	wantRepos := []string{"~/Code/soma", "~/Code/axon"}
	if !reflect.DeepEqual(cfg.ReposDirs, wantDirs) {
		t.Errorf("ReposDirs = %v, want %v", cfg.ReposDirs, wantDirs)
	}
	if !reflect.DeepEqual(cfg.Repos, wantRepos) {
		t.Errorf("Repos = %v, want %v", cfg.Repos, wantRepos)
	}
}

func TestLoadEnvOverride(t *testing.T) {
	body := `tmux_session = "from-env-path"` + "\n"
	path := writeConfig(t, body)
	withConfigEnv(t, path)

	if got := Path(); got != path {
		t.Errorf("Path() = %q, want %q", got, path)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.TmuxSession != "from-env-path" {
		t.Errorf("TmuxSession = %q, want %q (env-pointed file)", cfg.TmuxSession, "from-env-path")
	}
	if cfg.Path != path {
		t.Errorf("cfg.Path = %q, want %q", cfg.Path, path)
	}
}

func TestPathDefault(t *testing.T) {
	// Unset env to exercise the fallback. t.Setenv("MT_CONFIG", "")
	// doesn't unset — we explicitly remove it instead.
	t.Setenv("MT_CONFIG", "")
	if err := os.Unsetenv("MT_CONFIG"); err != nil {
		t.Fatalf("unset MT_CONFIG: %v", err)
	}

	got := Path()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	want := filepath.Join(home, ".metatree", "config.toml")
	if got != want {
		t.Errorf("Path() = %q, want %q", got, want)
	}
}

func TestLoadFullConfig(t *testing.T) {
	// Round-trip every field at once to catch tag mismatches.
	body := `
repos_dirs = ["/a", "/b"]
repos = ["/c"]
tmux_session = "ts"
tmux_window = "tw"
branch_prefix = "bp"
worktree_subdir = ".wt"
default_backend = "ollama"
ollama_model = "phi3"
claude_cmd = "claude --foo"
ollama_cmd = "ollama serve {model}"
auto_direnv_allow = false
auto_status_chrome = "false"
`
	path := writeConfig(t, body)
	withConfigEnv(t, path)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := &Config{
		ReposDirs:         []string{"/a", "/b"},
		Repos:             []string{"/c"},
		TmuxSession:       "ts",
		TmuxWindow:        "tw",
		BranchPrefix:      "bp",
		WorktreeSubdir:    ".wt",
		DefaultBackend:    "ollama",
		OllamaModel:       "phi3",
		ClaudeCmd:         "claude --foo",
		OllamaCmd:         "ollama serve {model}",
		WorktreeCopyFiles: []string{".env", ".envrc", ".npmrc"},
		AutoDirenvAllow:   false,
		AutoStatusChrome:  false,
		Path:              path,
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("Load full config:\n got  %+v\n want %+v", cfg, want)
	}
}

func TestFlexBoolUnmarshalDirect(t *testing.T) {
	// Direct test of the hook so we don't need a TOML file for every case.
	tests := []struct {
		name    string
		input   any
		want    bool
		wantErr bool
	}{
		{"native true", true, true, false},
		{"native false", false, false, false},
		{"string true", "true", true, false},
		{"string false", "false", false, false},
		{"string True", "True", true, false},
		{"string 1", "1", true, false},
		{"string 0", "0", false, false},
		{"string garbage", "yes", false, true},
		{"int rejected", int64(1), false, true},
		{"float rejected", 1.0, false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var b flexBool
			err := b.UnmarshalTOML(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Errorf("UnmarshalTOML(%v) = nil error, want error", tc.input)
				}
				return
			}
			if err != nil {
				t.Errorf("UnmarshalTOML(%v) error = %v", tc.input, err)
				return
			}
			if bool(b) != tc.want {
				t.Errorf("UnmarshalTOML(%v) = %v, want %v", tc.input, bool(b), tc.want)
			}
		})
	}
}

func TestDefault_WorktreeCopyFiles(t *testing.T) {
	got := Default().WorktreeCopyFiles
	want := []string{".env", ".envrc", ".npmrc"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Default().WorktreeCopyFiles = %v, want %v", got, want)
	}
}

func TestLoad_WorktreeCopyFiles_HappyPath(t *testing.T) {
	path := writeConfig(t, `worktree_copy_files = [".env", ".secret"]`+"\n")
	withConfigEnv(t, path)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []string{".env", ".secret"}
	if !reflect.DeepEqual(cfg.WorktreeCopyFiles, want) {
		t.Errorf("WorktreeCopyFiles = %v, want %v", cfg.WorktreeCopyFiles, want)
	}
}

func TestLoad_WorktreeCopyFiles_RejectsAbsolutePath(t *testing.T) {
	path := writeConfig(t, `worktree_copy_files = ["/etc/passwd"]`+"\n")
	withConfigEnv(t, path)

	_, err := Load()
	if err == nil {
		t.Fatal("Load() with absolute path should error")
	}
	if !strings.Contains(err.Error(), "absolute paths not allowed") {
		t.Errorf("error %q missing 'absolute paths not allowed'", err.Error())
	}
}

func TestLoad_WorktreeCopyFiles_RejectsParentEscape(t *testing.T) {
	path := writeConfig(t, `worktree_copy_files = ["../foo"]`+"\n")
	withConfigEnv(t, path)

	_, err := Load()
	if err == nil {
		t.Fatal("Load() with parent escape should error")
	}
	if !strings.Contains(err.Error(), "parent-directory references") {
		t.Errorf("error %q missing 'parent-directory references'", err.Error())
	}
}

func TestLoad_WorktreeCopyFiles_RejectsSubdirPath(t *testing.T) {
	path := writeConfig(t, `worktree_copy_files = ["config/local.env"]`+"\n")
	withConfigEnv(t, path)

	_, err := Load()
	if err == nil {
		t.Fatal("Load() with subdir path should error")
	}
	if !strings.Contains(err.Error(), "subdirectory paths not supported") {
		t.Errorf("error %q missing 'subdirectory paths not supported'", err.Error())
	}
}

func TestLoad_WorktreeCopyFiles_RejectsEmptyString(t *testing.T) {
	path := writeConfig(t, `worktree_copy_files = [""]`+"\n")
	withConfigEnv(t, path)

	_, err := Load()
	if err == nil {
		t.Fatal("Load() with empty string should error")
	}
	if !strings.Contains(err.Error(), "must not be empty") {
		t.Errorf("error %q missing 'must not be empty'", err.Error())
	}
}

func TestLoad_WorktreeCopyFiles_RejectsWhitespace(t *testing.T) {
	path := writeConfig(t, `worktree_copy_files = ["   "]`+"\n")
	withConfigEnv(t, path)

	_, err := Load()
	if err == nil {
		t.Fatal("Load() with whitespace-only entry should error")
	}
}

func TestLoad_WorktreeCopyFiles_Deduplicates(t *testing.T) {
	path := writeConfig(t, `worktree_copy_files = [".env", ".env", ".env"]`+"\n")
	withConfigEnv(t, path)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []string{".env"}
	if !reflect.DeepEqual(cfg.WorktreeCopyFiles, want) {
		t.Errorf("WorktreeCopyFiles = %v, want %v", cfg.WorktreeCopyFiles, want)
	}
}

func TestLoad_WorktreeCopyFiles_EmptyArray(t *testing.T) {
	path := writeConfig(t, `worktree_copy_files = []`+"\n")
	withConfigEnv(t, path)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.WorktreeCopyFiles) != 0 {
		t.Errorf("WorktreeCopyFiles = %v, want empty", cfg.WorktreeCopyFiles)
	}
	if reflect.DeepEqual(cfg.WorktreeCopyFiles, Default().WorktreeCopyFiles) {
		t.Errorf("explicit empty array should differ from Default() value; got %v", cfg.WorktreeCopyFiles)
	}
}

func TestLoadConfigIsDirectory(t *testing.T) {
	// Pointing at a directory should fail loudly, not silently fall back
	// to defaults — the user clearly meant *that* path.
	dir := t.TempDir()
	withConfigEnv(t, dir)

	_, err := Load()
	if err == nil {
		t.Fatal("Load() pointed at directory should error")
	}
	if !strings.Contains(err.Error(), "config ") {
		t.Errorf("error %q missing 'config ' wrap prefix", err.Error())
	}
}
