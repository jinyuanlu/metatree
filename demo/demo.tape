# mt demo — drives a fresh `mt new` end-to-end against a throwaway fixture.
# Run via `demo/record.sh` which sets up fixtures and PATH/MT_CONFIG.

Output demo.gif

Set FontSize 14
Set Width 1200
Set Height 700
Set Padding 25
Set TypingSpeed 55ms
Set Theme "Catppuccin Mocha"

# Hide the noisy "tmux kill-server / clear" frame.
Hide
Type "tmux kill-server 2>/dev/null; clear"
Enter
Sleep 600ms
Show

# Intro line so the viewer knows what they're about to see.
Type "# mt: one tmux window, every repo, Claude or Ollama"
Enter
Sleep 1500ms

Type "mt new"
Enter

# fzf picker appears with the one fixture repo. Enter selects.
Sleep 1500ms
Enter

# branch-name prompt.
Sleep 800ms
Type "fix-readme"
Sleep 400ms
Enter

# worktree creates, "mt: branched ... from origin/main@..." prints,
# pane splits, fake agent renders.
Sleep 9s
