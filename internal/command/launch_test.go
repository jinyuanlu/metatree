package command

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/jinyuanlu/metatree/internal/config"
)

func TestWrapAgentCmd_RecognizedShells(t *testing.T) {
	cases := []struct{ shell, base string }{
		{"/bin/bash", "bash"},
		{"/bin/zsh", "zsh"},
		{"/usr/local/bin/zsh", "zsh"},
		{"/opt/homebrew/bin/zsh", "zsh"},
		{"/opt/homebrew/bin/fish", "fish"},
		{"/usr/bin/fish", "fish"},
	}
	for _, c := range cases {
		t.Run(c.shell, func(t *testing.T) {
			t.Setenv("SHELL", c.shell)
			got, ok := wrapAgentCmd("claude")
			if !ok {
				t.Fatalf("recognized=false for SHELL=%s", c.shell)
			}
			if !strings.HasPrefix(got, c.shell+" -ic ") {
				t.Errorf("missing %q -ic prefix in %q", c.shell, got)
			}
			if !strings.Contains(got, "'claude'") {
				t.Errorf("missing wrapped 'claude' in %q", got)
			}
			// Critical: must NOT contain `exec` as the wrapped command's
			// first word — `exec <name>` suppresses alias expansion in
			// bash/zsh, defeating the entire reason this wrapper exists.
			if strings.Contains(got, "'exec ") {
				t.Errorf("wrapped cmd must not start with `exec` "+
					"(suppresses alias expansion); got %q", got)
			}
		})
	}
}

func TestWrapAgentCmd_UnknownShellPassesThrough(t *testing.T) {
	for _, shell := range []string{
		"/usr/local/bin/nu",
		"/usr/bin/pwsh",
		"/bin/dash",
		"/bin/sh",
		"/bin/busybox",
		"",
	} {
		t.Run(shell, func(t *testing.T) {
			t.Setenv("SHELL", shell)
			got, ok := wrapAgentCmd("claude")
			if ok {
				t.Errorf("recognized=true for unsupported SHELL=%q", shell)
			}
			if got != "claude" {
				t.Errorf("expected pass-through %q, got %q", "claude", got)
			}
		})
	}
}

func TestWrapAgentCmd_QuoteEscaping(t *testing.T) {
	t.Setenv("SHELL", "/bin/zsh")
	got, _ := wrapAgentCmd(`claude --foo 'bar' baz`)
	want := `/bin/zsh -ic 'claude --foo '\''bar'\'' baz'`
	if got != want {
		t.Errorf("\ngot:  %q\nwant: %q", got, want)
	}
}

func TestWrapAgentCmd_MultiwordCmdPreserved(t *testing.T) {
	t.Setenv("SHELL", "/bin/bash")
	got, _ := wrapAgentCmd("ollama run llama3:8b")
	if !strings.Contains(got, "'ollama run llama3:8b'") {
		t.Errorf("multi-word cmd not preserved: %q", got)
	}
}

func TestShellSingleQuote(t *testing.T) {
	cases := []struct{ in, want string }{
		{"hello", "'hello'"},
		{"", "''"},
		{"it's me", `'it'\''s me'`},
		{"'", `''\'''`},
		{"' '", `''\'' '\'''`},
		{"a'b'c", `'a'\''b'\''c'`},
	}
	for _, c := range cases {
		if got := shellSingleQuote(c.in); got != c.want {
			t.Errorf("\nin:   %q\ngot:  %q\nwant: %q", c.in, got, c.want)
		}
	}
}

func TestShellOrUnset(t *testing.T) {
	t.Setenv("SHELL", "/bin/zsh")
	if got := shellOrUnset(); got != "/bin/zsh" {
		t.Errorf("got %q, want /bin/zsh", got)
	}
	t.Setenv("SHELL", "")
	if got := shellOrUnset(); got != "(unset)" {
		t.Errorf("got %q, want (unset)", got)
	}
}

// newCopyEnv builds an *Env wired to a captured stderr buffer with the
// given worktree_copy_files list. Used by the three TestNew_CopyRuntimeFiles
// cases below to drive copyRuntimeFiles without standing up tmux.
func newCopyEnv(t *testing.T, copyFiles []string) (*Env, *bytes.Buffer) {
	t.Helper()
	cfg := config.Default()
	cfg.WorktreeCopyFiles = copyFiles
	var stderr bytes.Buffer
	return &Env{
		Config: cfg,
		Stdout: &bytes.Buffer{},
		Stderr: &stderr,
	}, &stderr
}

func TestNew_CopyRuntimeFiles_EmptyListSkipsStep(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	// Populate src with .env so we'd see a copy if the guard failed.
	if err := os.WriteFile(filepath.Join(src, ".env"), []byte("FOO=bar"), 0o644); err != nil {
		t.Fatalf("write .env: %v", err)
	}

	env, stderr := newCopyEnv(t, nil)
	copyRuntimeFiles(env, src, dst)

	out := stderr.String()
	if strings.Contains(out, "mt: copied") || strings.Contains(out, "mt: copy") {
		t.Errorf("expected silent skip for empty copy list, got stderr:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(dst, ".env")); !os.IsNotExist(err) {
		t.Errorf("expected no copy to happen, but dst/.env exists (err=%v)", err)
	}
}

func TestNew_CopyRuntimeFiles_HappyPathPrintsSummary(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	want := []byte("FOO=bar")
	if err := os.WriteFile(filepath.Join(src, ".env"), want, 0o644); err != nil {
		t.Fatalf("write .env: %v", err)
	}

	env, stderr := newCopyEnv(t, []string{".env"})
	copyRuntimeFiles(env, src, dst)

	out := stderr.String()
	if !strings.Contains(out, "mt: copied .env") {
		t.Errorf("expected 'mt: copied .env' on stderr, got:\n%s", out)
	}
	got, err := os.ReadFile(filepath.Join(dst, ".env"))
	if err != nil {
		t.Fatalf("read dst/.env: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("copied bytes mismatch:\n got:  %q\n want: %q", got, want)
	}
}

func TestNew_CopyRuntimeFiles_NothingCopyableIsSilent(t *testing.T) {
	src := t.TempDir() // empty — neither .env nor .envrc present
	dst := t.TempDir()

	env, stderr := newCopyEnv(t, []string{".env", ".envrc"})
	copyRuntimeFiles(env, src, dst)

	out := stderr.String()
	if strings.Contains(out, "mt: copy") || strings.Contains(out, "mt: copied") {
		t.Errorf("expected silent output when all sources missing, got:\n%s", out)
	}
}

// ----------------------------------------------------------------------
// resolveWorktreeBase wire-up
// ----------------------------------------------------------------------

func gitOrFatal(t *testing.T, cwd string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	if cwd != "" {
		cmd.Dir = cwd
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, cwd, err, out)
	}
}

// newBaseRepo initializes a fresh git repo at <parent>/repo with a single
// committed file on main, returns its path. No origin remote.
func newBaseRepo(t *testing.T, parent string) string {
	t.Helper()
	repo := filepath.Join(parent, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", repo, err)
	}
	gitOrFatal(t, "", "init", "-q", "-b", "main", repo)
	gitOrFatal(t, repo, "config", "user.email", "wireup-test@example.invalid")
	gitOrFatal(t, repo, "config", "user.name", "wireup-test")
	gitOrFatal(t, repo, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(repo, "README"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	gitOrFatal(t, repo, "add", "README")
	gitOrFatal(t, repo, "commit", "-q", "-m", "init")
	return repo
}

// newLifecycleEnv builds an *Env with cfg overrides applied, wired to a
// captured stderr buffer. Used by the resolveWorktreeBase tests below.
func newLifecycleEnv(t *testing.T, mutate func(*config.Config)) (*Env, *bytes.Buffer) {
	t.Helper()
	cfg := config.Default()
	if mutate != nil {
		mutate(cfg)
	}
	var stderr bytes.Buffer
	return &Env{
		Config: cfg,
		Stdout: &bytes.Buffer{},
		Stderr: &stderr,
	}, &stderr
}

func TestNew_WorktreeBase_PrintsBranchedFromLine(t *testing.T) {
	repo := newBaseRepo(t, t.TempDir())
	env, stderr := newLifecycleEnv(t, func(c *config.Config) {
		c.WorktreeBase = "head"
	})

	ref, err := resolveWorktreeBase(env, repo, "mt/feature")
	if err != nil {
		t.Fatalf("resolveWorktreeBase: %v", err)
	}
	if ref != "HEAD" {
		t.Errorf("got ref=%q, want HEAD", ref)
	}

	out := stderr.String()
	re := regexp.MustCompile(`^mt: branched mt/feature from HEAD@[0-9a-f]{8}\n$`)
	if !re.MatchString(out) {
		t.Errorf("stderr does not match `mt: branched <branch> from HEAD@<8hex>`:\n%s", out)
	}
}

func TestNew_WorktreeBase_MT_BASE_EnvOverride(t *testing.T) {
	repo := newBaseRepo(t, t.TempDir())
	// Config asks for origin-default (which would error: no origin remote),
	// but MT_BASE=head forces head-mode and skips the fetch.
	env, stderr := newLifecycleEnv(t, func(c *config.Config) {
		c.WorktreeBase = "origin-default"
	})
	t.Setenv("MT_BASE", "head")

	ref, err := resolveWorktreeBase(env, repo, "mt/feature")
	if err != nil {
		t.Fatalf("MT_BASE=head should bypass origin-default; got err: %v", err)
	}
	if ref != "HEAD" {
		t.Errorf("got ref=%q, want HEAD", ref)
	}
	out := stderr.String()
	if !strings.Contains(out, "from HEAD@") {
		t.Errorf("MT_BASE env override didn't take effect; stderr:\n%s", out)
	}
	if strings.Contains(out, "origin") {
		t.Errorf("MT_BASE=head should never mention origin; stderr:\n%s", out)
	}
}

func TestNew_WorktreeBase_HardError_NoOrigin(t *testing.T) {
	repo := newBaseRepo(t, t.TempDir()) // no origin remote
	env, _ := newLifecycleEnv(t, func(c *config.Config) {
		c.WorktreeBase = "origin-default"
	})

	_, err := resolveWorktreeBase(env, repo, "mt/feature")
	if err == nil {
		t.Fatal("expected hard error when worktree_base=origin-default and no origin remote")
	}
	msg := err.Error()
	if !strings.Contains(msg, `no "origin" remote`) {
		t.Errorf("error missing 'no \"origin\" remote': %s", msg)
	}
	if !strings.Contains(msg, "git remote add origin") || !strings.Contains(msg, "worktree_base") {
		t.Errorf("error missing remediation hint: %s", msg)
	}
}

func TestNew_WorktreeBase_LiteralRef(t *testing.T) {
	repo := newBaseRepo(t, t.TempDir())
	// Create a local `develop` branch pointing at HEAD.
	gitOrFatal(t, repo, "-C", repo, "branch", "develop")
	// Capture develop's short sha to compare against the stderr line.
	out, err := exec.Command("git", "-C", repo, "rev-parse", "--short=8", "develop").Output()
	if err != nil {
		t.Fatalf("rev-parse develop: %v", err)
	}
	wantSha := strings.TrimSpace(string(out))

	env, stderr := newLifecycleEnv(t, func(c *config.Config) {
		c.WorktreeBase = "develop"
	})
	ref, err := resolveWorktreeBase(env, repo, "mt/feature")
	if err != nil {
		t.Fatalf("resolveWorktreeBase: %v", err)
	}
	if ref != "develop" {
		t.Errorf("got ref=%q, want develop", ref)
	}
	line := stderr.String()
	want := "mt: branched mt/feature from develop@" + wantSha
	if !strings.Contains(line, want) {
		t.Errorf("stderr missing %q:\n%s", want, line)
	}
}

func TestNew_WorktreeBase_HeadModePrintedBeforeCopyLine(t *testing.T) {
	repo := newBaseRepo(t, t.TempDir())
	if err := os.WriteFile(filepath.Join(repo, ".env"), []byte("FOO=bar"), 0o644); err != nil {
		t.Fatalf("write .env: %v", err)
	}
	dst := t.TempDir()

	env, stderr := newLifecycleEnv(t, func(c *config.Config) {
		c.WorktreeBase = "head"
		c.WorktreeCopyFiles = []string{".env"}
	})
	if _, err := resolveWorktreeBase(env, repo, "mt/feature"); err != nil {
		t.Fatalf("resolveWorktreeBase: %v", err)
	}
	copyRuntimeFiles(env, repo, dst)

	out := stderr.String()
	branchedAt := strings.Index(out, "mt: branched")
	copiedAt := strings.Index(out, "mt: copied")
	if branchedAt < 0 {
		t.Fatalf("missing `mt: branched` line:\n%s", out)
	}
	if copiedAt < 0 {
		t.Fatalf("missing `mt: copied` line:\n%s", out)
	}
	if branchedAt > copiedAt {
		t.Errorf("`mt: branched` must precede `mt: copied`; got:\n%s", out)
	}
}
