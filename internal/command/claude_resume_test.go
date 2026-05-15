package command

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEncodeClaudeProject(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "simple absolute path",
			in:   "/Users/admin/Code/metatree",
			want: "-Users-admin-Code-metatree",
		},
		{
			name: "dotfile dir (slash-dot becomes double dash)",
			in:   "/Users/admin/Code/dotfiles/.claude",
			want: "-Users-admin-Code-dotfiles--claude",
		},
		{
			name: "claude-style worktree (.claude/worktrees)",
			in:   "/Users/admin/Code/F-Agent/.claude/worktrees/add-field",
			want: "-Users-admin-Code-F-Agent--claude-worktrees-add-field",
		},
		{
			name: "mt-style worktree (.worktrees)",
			in:   "/Users/admin/Code/metatree/.worktrees/feature",
			want: "-Users-admin-Code-metatree--worktrees-feature",
		},
		{
			name: "hyphens already present pass through",
			in:   "/a-b/c-d",
			want: "-a-b-c-d",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := encodeClaudeProject(tc.in)
			if got != tc.want {
				t.Errorf("encodeClaudeProject(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestHasClaudeSession exercises the on-disk probe by faking a $HOME
// with a Claude project dir containing a single .jsonl file. We don't
// touch the real home — t.TempDir + HOME override keeps the test
// hermetic.
func TestHasClaudeSession(t *testing.T) {
	worktree := "/Users/test/Code/proj/.worktrees/feat"

	// case 1: no claude dir at all
	fake1 := t.TempDir()
	t.Setenv("HOME", fake1)
	if hasClaudeSession(worktree) {
		t.Errorf("expected false when ~/.claude/projects does not exist")
	}

	// case 2: project dir exists but is empty
	fake2 := t.TempDir()
	t.Setenv("HOME", fake2)
	if err := os.MkdirAll(claudeProjectDir(fake2, worktree), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if hasClaudeSession(worktree) {
		t.Errorf("expected false for empty project dir")
	}

	// case 3: project dir has a non-jsonl file (still false)
	other := filepath.Join(claudeProjectDir(fake2, worktree), "config.json")
	if err := os.WriteFile(other, []byte("{}"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if hasClaudeSession(worktree) {
		t.Errorf("expected false when only non-jsonl files present")
	}

	// case 4: project dir has a .jsonl session — true
	session := filepath.Join(claudeProjectDir(fake2, worktree), "abc.jsonl")
	if err := os.WriteFile(session, []byte(""), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if !hasClaudeSession(worktree) {
		t.Errorf("expected true with at least one .jsonl present")
	}
}
