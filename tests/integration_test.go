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
	"io/fs"
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
worktree_base = "head"
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
// + @mt-managed (per spec.md §2.7.2 + CONTRIBUTING.md §10).
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
	// Run mt to completion. It exits non-zero when tmux attach fails
	// (no TTY in CI) — that's expected. Killing on "worktree dir
	// lands" raced past MarkPane on faster runners and the test then
	// observed a pane without @mt-managed. cmd.Run also joins I/O
	// goroutines, so any captured output is safe to read after.
	_ = cmd.Run()

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

// TestIntegration_NewWorktreeCopiesEnvFile — end-to-end proof that
// worktree_copy_files in config causes `mt new` to copy gitignored runtime
// files from the parent repo into the freshly-created worktree.
func TestIntegration_NewWorktreeCopiesEnvFile(t *testing.T) {
	f := setup(t)

	// Overwrite the fixture config to enable worktree_copy_files. Keep
	// everything else identical to setup()'s defaults so the rest of the
	// test path (tmux, claude_cmd=cat) still works.
	mustWrite(t, f.configPath, fmt.Sprintf(`repos = ["%s"]
tmux_session = "mt-itest"
tmux_window = "dashboard"
branch_prefix = "itest"
worktree_subdir = ".worktrees"
default_backend = "claude"
claude_cmd = "cat"
auto_direnv_allow = false
auto_status_chrome = false
worktree_base = "head"
worktree_copy_files = [".env"]
`, f.repoPath))

	// Drop a gitignored .env into the parent repo (not committed —
	// it's the kind of file the feature exists to copy).
	envContent := []byte("DATABASE_URL=local")
	mustWrite(t, filepath.Join(f.repoPath, ".env"), string(envContent))

	cmd := exec.Command(f.mtBin, "new", "--with", "claude")
	cmd.Env = append(os.Environ(),
		"MT_CONFIG="+f.configPath,
		"TMUX_TMPDIR="+f.tmuxSock,
		"TMUX=",
		"MT_REPO="+f.repoPath,
		"MT_BRANCH=copytest",
	)
	// mt exits non-zero when tmux attach fails (no TTY in tests); that's
	// after the copy step we care about. Run to completion — matches the
	// pattern in TestNewCreatesWorktreeAndPane.
	_ = cmd.Run()

	copied, err := os.ReadFile(filepath.Join(f.repoPath, ".worktrees", "copytest", ".env"))
	if err != nil {
		t.Fatalf("worktree .env not created: %v", err)
	}
	if !bytes.Equal(copied, envContent) {
		t.Errorf("worktree .env contents mismatch:\n got:  %q\n want: %q", copied, envContent)
	}
}

// TestIntegration_NewWorktreeBranchesFromOriginDefault — end-to-end proof
// that worktree_base = "origin-default" causes `mt new` to fetch and branch
// from the REMOTE tip, not the parent's stale local main.
func TestIntegration_NewWorktreeBranchesFromOriginDefault(t *testing.T) {
	f := setup(t)

	// Bare remote one dir up from the parent so it's discoverable but not
	// itself a candidate for repo scanning.
	bare := filepath.Join(f.tmp, "remote.git")
	mustRun(t, "", "git", "init", "--bare", "-q", "-b", "main", bare)

	// Wire the parent repo to the bare remote and push the existing main.
	mustRun(t, f.repoPath, "git", "remote", "add", "origin", bare)
	mustRun(t, f.repoPath, "git", "push", "-q", "-u", "origin", "main")

	// Add a SECOND commit on the bare's main via a scratch clone, so the
	// bare's main is now one commit AHEAD of f.repoPath's local main.
	scratch := filepath.Join(f.tmp, "scratch")
	mustRun(t, "", "git", "clone", "-q", bare, scratch)
	mustRun(t, scratch, "git", "config", "user.email", "remote@example.invalid")
	mustRun(t, scratch, "git", "config", "user.name", "remote-test")
	mustRun(t, scratch, "git", "config", "commit.gpgsign", "false")
	mustWrite(t, filepath.Join(scratch, "REMOTE-ADDED"), "from-remote\n")
	mustRun(t, scratch, "git", "add", "REMOTE-ADDED")
	mustRun(t, scratch, "git", "commit", "-q", "-m", "second commit on remote")
	mustRun(t, scratch, "git", "push", "-q", "origin", "main")

	// Capture the bare's tip sha for the assertion.
	tipBytes, err := exec.Command("git", "-C", bare, "rev-parse", "main").Output()
	if err != nil {
		t.Fatalf("rev-parse bare main: %v", err)
	}
	wantTip := strings.TrimSpace(string(tipBytes))

	// Sanity: f.repoPath's local main must NOT yet equal the bare tip
	// (otherwise the test would trivially pass via the wrong code path).
	localBytes, err := exec.Command("git", "-C", f.repoPath, "rev-parse", "main").Output()
	if err != nil {
		t.Fatalf("rev-parse local main: %v", err)
	}
	if strings.TrimSpace(string(localBytes)) == wantTip {
		t.Fatal("test setup broken: local main already equals bare tip — fetch can't be proven")
	}

	// Rewrite the fixture config: worktree_base = origin-default.
	mustWrite(t, f.configPath, fmt.Sprintf(`repos = ["%s"]
tmux_session = "mt-itest"
tmux_window = "dashboard"
branch_prefix = "itest"
worktree_subdir = ".worktrees"
default_backend = "claude"
claude_cmd = "cat"
auto_direnv_allow = false
auto_status_chrome = false
worktree_base = "origin-default"
`, f.repoPath))

	cmd := exec.Command(f.mtBin, "new", "--with", "claude")
	cmd.Env = append(os.Environ(),
		"MT_CONFIG="+f.configPath,
		"TMUX_TMPDIR="+f.tmuxSock,
		"TMUX=",
		"MT_REPO="+f.repoPath,
		"MT_BRANCH=fromremote",
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	_ = cmd.Run() // tmux attach exits non-zero in CI; side effects are what we verify.

	// Worktree HEAD must equal the bare tip — proves the fetch landed.
	wtPath := filepath.Join(f.repoPath, ".worktrees", "fromremote")
	gotBytes, err := exec.Command("git", "-C", wtPath, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse worktree HEAD: %v (stderr: %s)", err, stderr.String())
	}
	got := strings.TrimSpace(string(gotBytes))
	if got != wantTip {
		t.Errorf("worktree HEAD mismatch:\n got:  %s\n want: %s (bare tip)\nstderr:\n%s",
			got, wantTip, stderr.String())
	}

	// Stderr must announce origin/main as the source.
	wantLine := "mt: branched itest/fromremote from origin/main@"
	if !strings.Contains(stderr.String(), wantLine) {
		t.Errorf("stderr missing %q:\n%s", wantLine, stderr.String())
	}
}

// TestIntegration_RespawnExistingWorktreeSkipsBranchedFromLine — running
// `mt new` against a branch whose worktree already exists (the respawn
// path used by `mt switch`'s dead rows) must not re-fetch from origin
// nor print the misleading "branched X from Y@sha" line. Instead it
// prints "mt: resuming X" and reuses the worktree as-is.
func TestIntegration_RespawnExistingWorktreeSkipsBranchedFromLine(t *testing.T) {
	f := setup(t)

	// First create: full happy-path, prints "branched from".
	first := exec.Command(f.mtBin, "new", "--with", "claude")
	first.Env = append(os.Environ(),
		"MT_CONFIG="+f.configPath,
		"TMUX_TMPDIR="+f.tmuxSock,
		"TMUX=",
		"MT_REPO="+f.repoPath,
		"MT_BRANCH=respawn-me",
	)
	var firstErr bytes.Buffer
	first.Stderr = &firstErr
	_ = first.Run()

	if _, err := os.Stat(filepath.Join(f.repoPath, ".worktrees", "respawn-me")); err != nil {
		t.Fatalf("initial worktree not created: %v\nstderr:\n%s", err, firstErr.String())
	}

	// Kill the tmux server so the pane created by first run is gone — that
	// makes the worktree "dead" from the switch picker's POV.
	killCmd := exec.Command("tmux", "kill-server")
	killCmd.Env = append(os.Environ(), "TMUX_TMPDIR="+f.tmuxSock)
	_ = killCmd.Run()

	// Second create: same branch. This is what dead-row dispatch does
	// internally — set MT_REPO + MT_BRANCH and call RunNew.
	second := exec.Command(f.mtBin, "new", "--with", "claude")
	second.Env = append(os.Environ(),
		"MT_CONFIG="+f.configPath,
		"TMUX_TMPDIR="+f.tmuxSock,
		"TMUX=",
		"MT_REPO="+f.repoPath,
		"MT_BRANCH=respawn-me",
	)
	var secondErr bytes.Buffer
	second.Stderr = &secondErr
	_ = second.Run()

	got := secondErr.String()
	if !strings.Contains(got, "mt: resuming fixture-repo:respawn-me") {
		t.Errorf("expected resume notice in stderr; got:\n%s", got)
	}
	if strings.Contains(got, "branched itest/respawn-me from") {
		t.Errorf("respawn must not claim it branched the worktree; got:\n%s", got)
	}
}

// TestAuthInvariantNoCredentialRefs — production Go code (cmd/, internal/)
// must not contain string references to credentials.json or ANTHROPIC_ env
// vars. Test code (this file, others under tests/) is exempt because tests
// have to mention the strings they assert are absent.
//
// Per spec.md §1.4 and CONTRIBUTING.md §17 anti-pattern #1.
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
// `--mcp-config $HOME/.claude/mcp.json`), every agent pane mt creates
// must honor that alias.
//
// Before the fix: tmux split-window dispatched the cmd through
// `/bin/sh -c`, which is non-interactive and never sourced ~/.bashrc,
// so aliases were silently dropped. wrapAgentCmd now wraps in
// `$SHELL -ic '<cmd>'` so the user's interactive rcfile sources and
// the alias expands.
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

	// Spawn an agent pane. We rely on the same start+wait+kill pattern as
	// TestNewCreatesWorktreeAndPane. Inherit the parent env (we need
	// tmux/git on PATH); override HOME, SHELL, and prepend binDir so the
	// stub `claude` resolves before any real one.
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
		// tmux attach fails (no TTY in tests), but that's after MarkPane
		// has run. cmd.Wait() also joins the stderr-copy goroutine —
		// required for the deferred reader.
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
worktree_base = "head"
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
// mt doesn't know how to wrap (nu, pwsh, dash, …), agent-pane creation
// emits the documented warning to stderr that points the user at
// claude_cmd in config.toml.
//
// The warning is the only signal the user has that aliases are silently
// not expanding, so its presence is load-bearing — keep this test
// strict on the substring.
//
// Spawns two panes in succession (asymmetry guard). If someone later
// reintroduces a "first pane" code path that bypasses wrapAgentCmd, a
// single-pane test would still pass on the second pane and silently
// regress the first. Counting two hits across two invocations catches
// that.
func TestUnknownShellWarningEmitted(t *testing.T) {
	f := setup(t)

	runMtNew := func(branch string) string {
		cmd := exec.Command(f.mtBin, "new", "--with", "claude")
		cmd.Env = append(os.Environ(),
			"MT_CONFIG="+f.configPath,
			"TMUX_TMPDIR="+f.tmuxSock,
			"TMUX=",
			"MT_REPO="+f.repoPath,
			"MT_BRANCH="+branch,
			"SHELL=/usr/local/bin/nu",
		)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		_ = cmd.Run() // run to completion (Run = Start + Wait)
		return stderr.String()
	}

	out1 := runMtNew("first")
	out2 := runMtNew("second")
	combined := out1 + out2

	hits := strings.Count(combined, "$SHELL=/usr/local/bin/nu not recognized")
	if hits != 2 {
		t.Errorf("expected unknown-shell warning to fire on BOTH agent panes (asymmetry guard); got %d hits\n--- stderr 1 ---\n%s\n--- stderr 2 ---\n%s",
			hits, out1, out2)
	}
	if !strings.Contains(combined, "claude_cmd") || !strings.Contains(combined, "config.toml") {
		t.Errorf("warning should point at claude_cmd in config.toml, got:\n%s", combined)
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

// runMtBare runs the mt binary with a clean env (no fixture MT_CONFIG)
// so we exercise the EnsureConfig path. HOME is overridden to isolate
// the test from the developer's real ~/.metatree.
func runMtBare(t *testing.T, fakeHome string, args ...string) (string, string, int) {
	t.Helper()
	if testMtBin == "" {
		t.Fatal("testMtBin unset; TestMain didn't build the mt binary")
	}
	cmd := exec.Command(testMtBin, args...)
	// Strip MT_CONFIG and TMUX from inherited env, set HOME, give an
	// isolated TMUX_TMPDIR so subcommands that touch tmux don't poison
	// the developer's session.
	env := os.Environ()
	env = withEnv(env, "HOME", fakeHome)
	env = withEnv(env, "MT_CONFIG", "") // unset — will be removed below
	env = withEnv(env, "TMUX", "")
	tmuxSock := filepath.Join(fakeHome, "tmux-sock")
	if err := os.MkdirAll(tmuxSock, 0o755); err != nil {
		t.Fatalf("mkdir tmux sock: %v", err)
	}
	env = withEnv(env, "TMUX_TMPDIR", tmuxSock)
	// Strip empty MT_CONFIG entirely (Setenv("MT_CONFIG", "") doesn't
	// equal "unset" in Go on darwin/linux):
	clean := make([]string, 0, len(env))
	for _, kv := range env {
		if kv == "MT_CONFIG=" {
			continue
		}
		clean = append(clean, kv)
	}
	cmd.Env = clean
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	rc := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			rc = ee.ExitCode()
		} else {
			t.Fatalf("mt run: %v", err)
		}
	}
	t.Cleanup(func() {
		killCmd := exec.Command("tmux", "kill-server")
		killCmd.Env = append(os.Environ(), "TMUX_TMPDIR="+tmuxSock, "TMUX=")
		_ = killCmd.Run()
	})
	return stdout.String(), stderr.String(), rc
}

// TestFirstRunAutoSeedsFromProbe — the zero-prompt happy path. Create
// $HOME/Developer; running any non-meta subcommand should write
// ~/.metatree/config.toml with that path in repos_dirs.
func TestFirstRunAutoSeedsFromProbe(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "Developer"), 0o755); err != nil {
		t.Fatalf("mkdir Developer: %v", err)
	}

	_, _, _ = runMtBare(t, home, "diagnose")

	cfgPath := filepath.Join(home, ".metatree", "config.toml")
	b, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("expected %s to exist: %v", cfgPath, err)
	}
	if !strings.Contains(string(b), filepath.Join(home, "Developer")) {
		t.Errorf("config does not include $HOME/Developer in repos_dirs:\n%s", b)
	}
}

// TestFirstRunMigratesLegacyConfig — when ~/.config/mt/config.toml
// exists but ~/.metatree/config.toml doesn't, the legacy file is
// auto-copied with a stderr notice.
func TestFirstRunMigratesLegacyConfig(t *testing.T) {
	home := t.TempDir()
	legacy := filepath.Join(home, ".config", "mt", "config.toml")
	if err := os.MkdirAll(filepath.Dir(legacy), 0o755); err != nil {
		t.Fatalf("mkdir legacy: %v", err)
	}
	legacyContent := "repos_dirs = [\"~/legacy-marker\"]\n"
	if err := os.WriteFile(legacy, []byte(legacyContent), 0o600); err != nil {
		t.Fatalf("write legacy: %v", err)
	}

	_, stderr, _ := runMtBare(t, home, "diagnose")

	canonical := filepath.Join(home, ".metatree", "config.toml")
	b, err := os.ReadFile(canonical)
	if err != nil {
		t.Fatalf("expected %s after migration: %v", canonical, err)
	}
	if !strings.Contains(string(b), "legacy-marker") {
		t.Errorf("migrated config missing legacy content:\n%s", b)
	}
	if !strings.Contains(stderr, "migrated config") {
		t.Errorf("expected migration notice on stderr, got:\n%s", stderr)
	}

	// Second invocation: no migration notice (idempotent).
	_, stderr2, _ := runMtBare(t, home, "diagnose")
	if strings.Contains(stderr2, "migrated config") {
		t.Errorf("second run should be a no-op; got migration notice:\n%s", stderr2)
	}
}

// TestSetupReposDirsFlagWritesConfig — non-interactive setup writes
// the requested repos_dirs and exits 0 without prompting.
func TestSetupReposDirsFlagWritesConfig(t *testing.T) {
	home := t.TempDir()
	_, _, rc := runMtBare(t, home, "setup", "--repos-dirs", "~/work,~/oss")
	if rc != 0 {
		t.Fatalf("setup --repos-dirs rc=%d", rc)
	}
	cfgPath := filepath.Join(home, ".metatree", "config.toml")
	b, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	for _, want := range []string{"~/work", "~/oss"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("config missing %q:\n%s", want, b)
		}
	}
}

// TestSetupClaudeCmdStoresLiteral — the structural fix for the
// alias-expansion bug. When the user sets claude_cmd via flag, the
// literal string lands in config and is later run verbatim, bypassing
// every shell-alias-doesn't-expand failure mode.
func TestSetupClaudeCmdStoresLiteral(t *testing.T) {
	home := t.TempDir()
	literal := "claude --mcp-config ~/.claude/mcp.json"
	_, _, rc := runMtBare(t, home, "setup", "--claude-cmd", literal)
	if rc != 0 {
		t.Fatalf("setup --claude-cmd rc=%d", rc)
	}
	b, err := os.ReadFile(filepath.Join(home, ".metatree", "config.toml"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(b), "mcp-config") {
		t.Errorf("claude_cmd not stored literally:\n%s", b)
	}
}

// TestSetupPrint — --print emits the resolved config to stdout
// without writing anything to disk.
func TestSetupPrint(t *testing.T) {
	home := t.TempDir()
	stdout, _, rc := runMtBare(t, home, "setup", "--print")
	if rc != 0 {
		t.Fatalf("setup --print rc=%d", rc)
	}
	if !strings.Contains(stdout, "tmux_session") {
		t.Errorf("--print output missing tmux_session:\n%s", stdout)
	}
	if _, err := os.Stat(filepath.Join(home, ".metatree", "config.toml")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("--print should not write config; found one anyway")
	}
}
