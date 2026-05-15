package command

import (
	"os"
	"path/filepath"
	"strings"
)

// Auto-resume: when a user picks a dead worktree from `mt switch` (or
// runs `mt new` for a path that already exists), mt re-runs the agent
// command in the worktree's directory. For Claude Code, the user
// expectation is "come back, find my conversation" — which means
// `claude --continue`, not a fresh session.
//
// Claude Code stores per-project session state at
// `~/.claude/projects/<encoded>/*.jsonl`, where <encoded> is the cwd
// with every `/` and `.` mapped to `-`. mt mirrors that encoding here
// and checks for at least one `.jsonl` file; only then does it inject
// `--continue` so a worktree with no prior history still starts fresh.

// encodeClaudeProject mirrors Claude Code's cwd → project-dir encoding:
// every '/' and '.' in the absolute path is replaced with '-'. Other
// characters pass through. The result is the leaf name under
// `~/.claude/projects/` for a given working directory.
//
// Examples:
//
//	/Users/admin/Code/metatree
//	  → -Users-admin-Code-metatree
//
//	/Users/admin/Code/F-Agent/.claude/worktrees/add-field
//	  → -Users-admin-Code-F-Agent--claude-worktrees-add-field
func encodeClaudeProject(absPath string) string {
	s := strings.ReplaceAll(absPath, "/", "-")
	s = strings.ReplaceAll(s, ".", "-")
	return s
}

// claudeProjectDir returns the absolute path of the Claude Code session
// directory for the given working directory, or "" if $HOME can't be
// resolved.
func claudeProjectDir(home, worktreePath string) string {
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".claude", "projects", encodeClaudeProject(worktreePath))
}

// hasClaudeSession reports whether Claude Code has any saved session
// for the given worktree path (at least one `.jsonl` in the encoded
// project directory).
//
// Used by RunNew to decide whether to inject `--continue` on revive.
// Returns false on any error (missing $HOME, unreadable dir) — falling
// back to "start fresh" is the safe behaviour: the worst that happens
// is the user re-types `claude --continue` themselves.
func hasClaudeSession(worktreePath string) bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	dir := claudeProjectDir(home, worktreePath)
	if dir == "" {
		return false
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".jsonl") {
			return true
		}
	}
	return false
}
