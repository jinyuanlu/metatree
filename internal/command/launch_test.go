package command

import (
	"strings"
	"testing"
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
