# mt demo — drives an end-to-end multi-repo workflow:
#   1) mt new → first agent in acme-api/fix-cookie (branched from origin/main).
#   2) prefix+g switch popup → + Create new → second agent in mira-web.
#      Tiled view of two agents emerges.
#   3) prefix+g → pick legacy-svc:old-fix [dead] → respawn into the
#      pre-existing worktree, "mt: resuming ..." prints, third pane joins.
#
# Fixtures + binary built by demo/record.sh.

Output demo.gif

Set FontSize 14
Set Width 1200
Set Height 700
Set Padding 25
Set TypingSpeed 55ms
Set Theme "Catppuccin Mocha"

# Hide the noisy kill-server / clear frame.
Hide
Type "tmux kill-server 2>/dev/null; clear"
Enter
Sleep 600ms
Show

# Intro line so the viewer knows the pitch.
Type "# mt: one tmux window, every repo, Claude or Ollama"
Enter
Sleep 1500ms

# === Step 1 — mt new acme-api/fix-cookie =====================================
Type "mt new"
Enter
Sleep 1200ms
# fzf repo picker: type "ac" to filter to acme-api.
Type "ac"
Sleep 400ms
Enter
Sleep 700ms
# branch name prompt
Type "fix-cookie"
Sleep 300ms
Enter
# "mt: branched ... from origin/main@..." prints, fake-claude renders.
Sleep 5s

# === Step 2 — prefix+g, + Create new, mira-web/add-export ===================
Ctrl+B
Type "g"
Sleep 1800ms
# Picker rows (with pre-seeded dead worktree, before any new pane created):
#   acme-api:fix-cookie  [live]
#   legacy-svc:old-fix   [dead]
#   + Create new...      [new ]
# Type "new" to filter to + Create new (only row containing "new").
Type "new"
Sleep 700ms
Enter
Sleep 1200ms
Type "mi"
Sleep 400ms
Enter
Sleep 700ms
Type "add-export"
Sleep 300ms
Enter
# Second agent renders; tile layout gives two side-by-side panes.
Sleep 5s

# === Step 3 — prefix+g, pick [dead] legacy-svc:old-fix ======================
Ctrl+B
Type "g"
Sleep 2s
# Picker now has 2 live + 1 dead + new (4 rows):
#   acme-api:fix-cookie    [live]
#   mira-web:add-export    [live]
#   legacy-svc:old-fix     [dead]
#   + Create new...        [new ]
# Type "dead" to filter — only the dead row's marker contains it.
Type "dead"
Sleep 900ms
Enter
# "mt: resuming legacy-svc:old-fix" prints, third pane joins the tiled view.
Sleep 6s
