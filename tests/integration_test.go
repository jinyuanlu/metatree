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
// TestMain compiles the mt binary fresh on every `go test` invocation,
// so a stale `bin/mt-go` cannot silently produce ghost-failures (or
// ghost-passes) by running an out-of-date build against current tests.
// We saw this exact failure mode once and the next mistake of that
// shape gets caught here, not via "rebuild and retry."
package tests

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// testMtBin is the absolute path to a freshly-built mt binary, set by
// TestMain. Tests reference this instead of any in-tree path so they
// can never run against a stale build.
var testMtBin string

func TestMain(m *testing.M) {
	if _, err := exec.LookPath("go"); err != nil {
		fmt.Fprintln(os.Stderr, "tests: go toolchain required to build mt fresh; skipping")
		os.Exit(0)
	}
	tmp, err := os.MkdirTemp("", "mt-itest-bin-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "tests: mkdir tmp:", err)
		os.Exit(2)
	}
	defer os.RemoveAll(tmp)
	bin := filepath.Join(tmp, "mt")
	build := exec.Command("go", "build", "-o", bin, "../cmd/mt")
	build.Stderr = os.Stderr
	build.Stdout = os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "tests: go build mt failed:", err)
		os.Exit(2)
	}
	testMtBin = bin
	os.Exit(m.Run())
}

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

	// TestMain builds mt fresh into testMtBin on every `go test` run, so
	// the binary under test always matches the source tree. No stale-build
	// foot-gun.
	if testMtBin == "" {
		t.Fatal("testMtBin unset; TestMain didn't build the mt binary")
	}
	mtBin := testMtBin

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

// TestAliasExpansionInSubsequentPane verifies the fix for the silent
// MCP-loss bug: when the user has aliased `claude` (e.g. to inject
// `--mcp-config $HOME/.claude/mcp.json`), every pane mt creates must
// honor that alias — including the second-and-later panes that go
// through `tmux split-window` (path 2 in lifecycle.go).
//
// Before the fix: tmux split-window dispatched the cmd through
// `/bin/sh -c`, which is non-interactive and never sourced ~/.bashrc,
// so aliases were silently dropped on pane #2+. The first pane (fed
// via send-keys into a live shell) was unaffected, so the bug was
// asymmetric and easy to miss.
//
// This test sets up two stubs:
//   - bin/claude       writes "PATH-CLAUDE: <argv>" to a log file
//   - bin/agent-stub   writes "ALIASED: <argv>"     to the same log
//
// And a fixture rcfile that aliases `claude` → `agent-stub --injected-flag`.
// If alias expansion works in pane #2, the log contains "ALIASED:
// --injected-flag". If the fix regresses, the log contains
// "PATH-CLAUDE:" with no flag.
//
// Skipped automatically on systems without bash, which is essentially
// no system mt targets — but keep the skip so a stripped CI image
// doesn't spuriously fail.
func TestAliasExpansionInSubsequentPane(t *testing.T) {
	bashPath, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available; skipping alias-expansion integration test")
	}

	f := setup(t)

	// Build the two stubs. The stub stays alive (sleep 30) so the pane
	// process exists long enough for `tmux list-panes` and friends.
	binDir := filepath.Join(f.tmp, "bin")
	mustWrite(t, filepath.Join(binDir, "claude"),
		"#!/bin/sh\n"+
			fmt.Sprintf(`echo "PATH-CLAUDE: $*" >> %q`+"\n", filepath.Join(f.tmp, "argv.log"))+
			"sleep 30\n")
	mustWrite(t, filepath.Join(binDir, "agent-stub"),
		"#!/bin/sh\n"+
			fmt.Sprintf(`echo "ALIASED: $*" >> %q`+"\n", filepath.Join(f.tmp, "argv.log"))+
			"sleep 30\n")
	for _, name := range []string{"claude", "agent-stub"} {
		if err := os.Chmod(filepath.Join(binDir, name), 0o755); err != nil {
			t.Fatalf("chmod %s: %v", name, err)
		}
	}

	// Fake $HOME with rcfiles that alias `claude`. Write the alias to
	// every bash startup file we can think of: tmux may start the pane
	// shell as a login shell (reads .bash_profile), my fix uses bash -ic
	// (reads .bashrc), and Apple Terminal-style configs sometimes source
	// .profile. Belt-and-braces: same alias in all three, plus a
	// sentinel so a debug failure is diagnosable.
	fakeHome := filepath.Join(f.tmp, "home")
	if err := os.MkdirAll(fakeHome, 0o755); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	rcBody := fmt.Sprintf(
		"echo SOURCED-%%s >> %q\n"+
			"alias claude='%s --injected-flag'\n",
		filepath.Join(f.tmp, "rc.log"),
		filepath.Join(binDir, "agent-stub"))
	for _, name := range []string{".bashrc", ".bash_profile", ".profile"} {
		mustWrite(t, filepath.Join(fakeHome, name),
			fmt.Sprintf(rcBody, name))
	}

	// Spawn pane #1 (path 1: send-keys into existing shell). Branch "first".
	// We rely on the same start+wait+kill pattern as TestNewCreatesWorktreeAndPane.
	// Inherit the parent env (we need tmux/git on PATH); override HOME, SHELL,
	// and prepend binDir so the stub `claude` resolves before any real one.
	runMtNew := func(branch string) {
		cmd := exec.Command(f.mtBin, "new", "--with", "claude")
		env := append([]string{}, os.Environ()...)
		// Override HOME and SHELL so bash sources our fixture .bashrc.
		env = withEnv(env, "HOME", fakeHome)
		env = withEnv(env, "SHELL", bashPath)
		env = withEnv(env, "PATH", binDir+":"+os.Getenv("PATH"))
		// zsh-style ZDOTDIR could redirect rcfile lookup; clear it.
		env = withEnv(env, "ZDOTDIR", "")
		// mt fixture overrides
		env = append(env,
			"MT_CONFIG="+f.configPath,
			"TMUX_TMPDIR="+f.tmuxSock,
			"TMUX=",
			"MT_REPO="+f.repoPath,
			"MT_BRANCH="+branch,
		)
		cmd.Env = env
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		defer func() {
			if s := stderr.String(); s != "" {
				t.Logf("[mt new %s] stderr:\n%s", branch, s)
			}
		}()
		// Run mt to completion (no Kill). It exits with rc=1 quickly when
		// tmux attach fails (no TTY in tests), but that's *after* the
		// MarkPane step that managedCount depends on. Killing on
		// "worktree dir lands" raced past MarkPane on slower runners and
		// silently regressed pane #2 to path 1. cmd.Wait() also joins the
		// stderr-copy goroutine — required for the deferred reader.
		if err := cmd.Run(); err == nil {
			// mt is expected to exit non-zero (tmux attach fails); a
			// rare zero return isn't itself a failure for this test.
			_ = err
		}
	}

	// Override claude_cmd to the bare name `claude` so the alias has
	// something to expand (the default config in setup() points it at
	// "cat" for the other tests).
	mustWrite(t, f.configPath, fmt.Sprintf(`repos = ["%s"]
tmux_session = "mt-itest"
tmux_window = "dashboard"
branch_prefix = "itest"
worktree_subdir = ".worktrees"
default_backend = "claude"
claude_cmd = "claude"
auto_direnv_allow = false
auto_status_chrome = false
`, f.repoPath))

	runMtNew("first")
	runMtNew("second")

	// Wait for both stubs to have written to the log. Two lines expected.
	logPath := filepath.Join(f.tmp, "argv.log")
	var logBytes []byte
	for i := 0; i < 50; i++ {
		b, err := os.ReadFile(logPath)
		if err == nil && bytes.Count(b, []byte("\n")) >= 2 {
			logBytes = b
			break
		}
		mustSleep(t, 100)
	}
	if len(logBytes) == 0 {
		t.Fatalf("argv.log never received both stub writes; got: %q", logBytes)
	}
	log := string(logBytes)

	// The alias must have fired in BOTH panes. Two ALIASED lines, zero
	// PATH-CLAUDE lines, and the injected flag must appear twice.
	aliasedCount := strings.Count(log, "ALIASED: --injected-flag")
	pathCount := strings.Count(log, "PATH-CLAUDE:")
	if aliasedCount != 2 || pathCount != 0 {
		// On failure, dump rc.log + tmux pane state so the failure mode
		// is diagnosable from CI logs without re-running locally.
		rcLog, _ := os.ReadFile(filepath.Join(f.tmp, "rc.log"))
		t.Logf("rc.log:\n%s", rcLog)
		listPanes := exec.Command("tmux", "list-panes",
			"-t", "mt-itest:dashboard",
			"-F", "#{pane_id} cmd=#{pane_current_command}")
		listPanes.Env = append(os.Environ(), "TMUX_TMPDIR="+f.tmuxSock, "TMUX=")
		paneCmds, _ := listPanes.Output()
		t.Logf("tmux panes:\n%s", paneCmds)
		t.Errorf("alias expansion broken in some pane.\n"+
			"  expected: 2x ALIASED, 0x PATH-CLAUDE\n"+
			"  got:      %dx ALIASED, %dx PATH-CLAUDE\n"+
			"  argv.log:\n%s", aliasedCount, pathCount, log)
	}
}

// TestUnknownShellWarningEmitted verifies that when $SHELL is something
// mt doesn't know how to wrap (nu, pwsh, dash, …), pane #2 creation
// emits the documented warning to stderr that points the user at
// claude_cmd in config.toml.
//
// The warning is the only signal the user has that aliases are silently
// not expanding, so its presence is load-bearing — keep this test
// strict on the substring.
func TestUnknownShellWarningEmitted(t *testing.T) {
	f := setup(t)

	// First pane primes the dashboard (path 1, no warning expected).
	cmd1 := exec.Command(f.mtBin, "new", "--with", "claude")
	cmd1.Env = append(os.Environ(),
		"MT_CONFIG="+f.configPath,
		"TMUX_TMPDIR="+f.tmuxSock,
		"TMUX=",
		"MT_REPO="+f.repoPath,
		"MT_BRANCH=first",
		"SHELL=/usr/local/bin/nu",
	)
	// Run to completion (not Kill-mid-flight). MarkPane must finish before
	// cmd2 fires, otherwise managedCount==0 and cmd2 falls back to path 1
	// (no split-window, no warning). See runMtNew above.
	_ = cmd1.Run()

	// Second pane goes through path 2 — the warning should fire here.
	cmd2 := exec.Command(f.mtBin, "new", "--with", "claude")
	cmd2.Env = append(os.Environ(),
		"MT_CONFIG="+f.configPath,
		"TMUX_TMPDIR="+f.tmuxSock,
		"TMUX=",
		"MT_REPO="+f.repoPath,
		"MT_BRANCH=second",
		"SHELL=/usr/local/bin/nu",
	)
	var stderr bytes.Buffer
	cmd2.Stderr = &stderr
	_ = cmd2.Run() // run to completion (Run = Start + Wait)

	out := stderr.String()
	if !strings.Contains(out, "$SHELL=/usr/local/bin/nu not recognized") {
		t.Errorf("expected unknown-shell warning naming nu, got stderr:\n%s", out)
	}
	if !strings.Contains(out, "claude_cmd") || !strings.Contains(out, "config.toml") {
		t.Errorf("warning should point at claude_cmd in config.toml, got:\n%s", out)
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

// withEnv returns env with key=value, replacing any existing entry for key.
func withEnv(env []string, key, value string) []string {
	prefix := key + "="
	out := env[:0]
	for _, kv := range env {
		if !strings.HasPrefix(kv, prefix) {
			out = append(out, kv)
		}
	}
	return append(out, prefix+value)
}

func mustSleep(t *testing.T, ms int) {
	t.Helper()
	cmd := exec.Command("sleep", fmt.Sprintf("0.%03d", ms))
	_ = cmd.Run()
}
