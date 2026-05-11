package command

import (
	"bytes"
	"os"
	"path/filepath"
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
