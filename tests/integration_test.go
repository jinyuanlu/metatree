// Package tests holds end-to-end integration tests that drive the built
// `mt` binary against real tmux + git fixtures.
//
// These tests are slower than unit tests (per-test tmux server startup,
// ~50–100ms each) but catch interaction bugs the per-package unit tests
// can't see — the kind of bugs that drove the bash port to ship 19 smoke
// sections.
//
// Run via:  go test -race ./tests/...
//
// Skipped automatically when tmux/git/mt aren't available on PATH or when
// the build hasn't produced ./bin/mt-go yet.
package tests

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const binRel = "../bin/mt-go"

// fixture holds the per-test scratch state: tmpdir, tmux socket dir, and
// a ready-to-use mt config file. Cleanup kills the tmux server.
type fixture struct {
	t          *testing.T
	tmp        string // /tmp/mt-itest-<hex>
	tmuxSock   string // short path under /tmp to stay under macOS UDS limit
	configPath string
	repoPath   string
	mtBin      string // absolute path to the mt-go binary under test
}

func setup(t *testing.T) *fixture {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed; skipping integration test")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed; skipping integration test")
	}

	mtBin, err := filepath.Abs(binRel)
	if err != nil {
		t.Fatalf("abs %s: %v", binRel, err)
	}
	if _, err := os.Stat(mtBin); errors.Is(err, fs.ErrNotExist) {
		t.Skipf("%s not built; run `go build -o ./bin/mt-go ./cmd/mt` first", mtBin)
	}

	// Short path so macOS UDS limit (~104) doesn't bite. mktemp under /tmp.
	tmp, err := os.MkdirTemp("/tmp", "mt-itest-")
	if err != nil {
		t.Fatalf("mkdir tmp: %v", err)
	}

	repo := filepath.Join(tmp, "fixture-repo")
	mustRun(t, "", "git", "init", "-q", "-b", "main", repo)
	mustWrite(t, filepath.Join(repo, "README.md"), "# fixture\n")
	mustRun(t, repo, "git", "add", "README.md")
	mustRun(t, repo, "git",
		"-c", "user.name=test", "-c", "user.email=t@t",
		"commit", "-q", "-m", "init")

	socketDir := filepath.Join(tmp, "tmux")
	if err := os.MkdirAll(socketDir, 0o755); err != nil {
		t.Fatalf("mkdir socket: %v", err)
	}

	configPath := filepath.Join(tmp, "config.toml")
	mustWrite(t, configPath, fmt.Sprintf(`repos = ["%s"]
tmux_session = "mt-itest"
tmux_window = "dashboard"
branch_prefix = "itest"
worktree_subdir = ".worktrees"
default_backend = "claude"
claude_cmd = "cat"
auto_direnv_allow = false
auto_status_chrome = false
`, repo))

	t.Cleanup(func() {
		// kill any tmux server we spawned, then remove tmp dir
		killCmd := exec.Command("tmux", "kill-server")
		killCmd.Env = append(os.Environ(), "TMUX_TMPDIR="+socketDir)
		_ = killCmd.Run()
		_ = os.RemoveAll(tmp)
	})

	return &fixture{
		t:          t,
		tmp:        tmp,
		tmuxSock:   socketDir,
		configPath: configPath,
		repoPath:   repo,
		mtBin:      mtBin,
	}
}

// mt runs mt with the fixture's config + isolated tmux socket. Returns
// stdout, stderr, exit code.
func (f *fixture) mt(extraEnv map[string]string, args ...string) (string, string, int) {
	f.t.Helper()
	cmd := exec.Command(f.mtBin, args...)
	cmd.Env = append(os.Environ(),
		"MT_CONFIG="+f.configPath,
		"TMUX_TMPDIR="+f.tmuxSock,
		"TMUX=", // never inherit the developer's tmux env
	)
	for k, v := range extraEnv {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	rc := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			rc = exitErr.ExitCode()
		} else {
			f.t.Fatalf("mt run: %v", err)
		}
	}
	return stdout.String(), stderr.String(), rc
}

// listPanes returns the tmux pane IDs on the dashboard window, with their
// @mt-managed values. Empty MtManaged = bare-shell or external pane.
type pane struct {
	ID, MtManaged string
}

func (f *fixture) listPanes(target string) []pane {
	f.t.Helper()
	cmd := exec.Command("tmux", "list-panes",
		"-t", target, "-F", "#{pane_id}|#{@mt-managed}")
	cmd.Env = append(os.Environ(), "TMUX_TMPDIR="+f.tmuxSock, "TMUX=")
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var panes []pane
	for _, ln := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if ln == "" {
			continue
		}
		parts := strings.SplitN(ln, "|", 2)
		if len(parts) == 2 {
			panes = append(panes, pane{ID: parts[0], MtManaged: parts[1]})
		}
	}
	return panes
}

// TestHelp — sanity check that mt --help works.
func TestHelp(t *testing.T) {
	f := setup(t)
	out, _, rc := f.mt(nil, "--help")
	if rc != 0 {
		t.Fatalf("--help returned rc=%d", rc)
	}
	if !strings.Contains(out, "tmux-native dashboard") {
		t.Fatalf("--help output unexpected:\n%s", out)
	}
}

// TestDiagnoseAllSections — diagnose prints every named section.
func TestDiagnoseAllSections(t *testing.T) {
	f := setup(t)
	out, _, rc := f.mt(nil, "diagnose")
	if rc != 0 {
		t.Fatalf("diagnose returned rc=%d, stderr+stdout: %s", rc, out)
	}
	for _, want := range []string{"VERSIONS", "CONFIG", "TMUX STATE", "KEYBINDINGS", "LOG"} {
		if !strings.Contains(out, want) {
			t.Errorf("diagnose missing section %q", want)
		}
	}
}

// TestNewCreatesWorktreeAndPane — happy path: worktree + branch + pane
// + @mt-managed (per spec.md §2.7.2 + spec-go.md §10).
func TestNewCreatesWorktreeAndPane(t *testing.T) {
	f := setup(t)

	// Run `mt new` non-interactively via MT_REPO/MT_BRANCH overrides.
	// The binary will block on tmux attach; run it in the background and
	// kill it after the side effects are done.
	cmd := exec.Command(f.mtBin, "new", "--with", "claude")
	cmd.Env = append(os.Environ(),
		"MT_CONFIG="+f.configPath,
		"TMUX_TMPDIR="+f.tmuxSock,
		"TMUX=",
		"MT_REPO="+f.repoPath,
		"MT_BRANCH=feature",
	)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	// give it time to create worktree+pane (well over the spec.md §2.7.1 ≤5s)
	for i := 0; i < 50; i++ {
		if _, err := os.Stat(filepath.Join(f.repoPath, ".worktrees", "feature")); err == nil {
			break
		}
		mustSleep(t, 100)
	}
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()

	// 1) worktree dir exists
	if _, err := os.Stat(filepath.Join(f.repoPath, ".worktrees", "feature")); err != nil {
		t.Fatalf("worktree not created: %v", err)
	}
	// 2) branch exists with prefix
	branches, _ := exec.Command("git", "-C", f.repoPath, "branch", "--list", "itest/feature").CombinedOutput()
	if !strings.Contains(string(branches), "itest/feature") {
		t.Errorf("branch itest/feature not created; got: %s", branches)
	}
	// 3) pane has @mt-managed marker
	panes := f.listPanes("mt-itest:dashboard")
	found := false
	for _, p := range panes {
		if p.MtManaged == "fixture-repo:feature" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("no pane with @mt-managed=fixture-repo:feature; panes: %+v", panes)
	}
}

// TestAuthInvariantNoCredentialRefs — production Go code (cmd/, internal/)
// must not contain string references to credentials.json or ANTHROPIC_ env
// vars. Test code (this file, others under tests/) is exempt because tests
// have to mention the strings they assert are absent.
//
// Per spec.md §1.4 and spec-go.md §17 anti-pattern #1.
func TestAuthInvariantNoCredentialRefs(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	// Scan production directories only.
	for _, dir := range []string{"cmd", "internal"} {
		full := filepath.Join(root, dir)
		cmd := exec.Command("grep", "-rE", `(ANTHROPIC_|credentials\.json)`,
			"--include=*.go", full)
		out, _ := cmd.CombinedOutput()
		for _, ln := range strings.Split(string(out), "\n") {
			if ln == "" {
				continue
			}
			// Strip "<file>:" prefix to get the line content
			content := ln
			if i := strings.Index(ln, ":"); i >= 0 {
				content = ln[i+1:]
			}
			trimmed := strings.TrimSpace(content)
			// Allow comment-only mentions (they explain what we DON'T do)
			if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "*") {
				continue
			}
			t.Errorf("non-comment reference to credentials.json or ANTHROPIC_ in %s:\n  %s", dir, ln)
		}
	}
}

// helpers —

func mustRun(t *testing.T, cwd string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	if cwd != "" {
		cmd.Dir = cwd
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func mustSleep(t *testing.T, ms int) {
	t.Helper()
	cmd := exec.Command("sleep", fmt.Sprintf("0.%03d", ms))
	_ = cmd.Run()
}
