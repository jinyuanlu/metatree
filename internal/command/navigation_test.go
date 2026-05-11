package command

import (
	"testing"

	"github.com/jinyuanlu/metatree/internal/gitio"
	"github.com/jinyuanlu/metatree/internal/tmuxio"
)

// TestBuildSwitchRows_LiveBeforeDeadBeforeNew checks the ordering contract
// behind `mt switch`: live agents at the top so the picker stays low-noise,
// dead worktrees grouped below, "+ Create new" always last.
func TestBuildSwitchRows_LiveBeforeDeadBeforeNew(t *testing.T) {
	managed := []tmuxio.Pane{
		{ID: "%10", MtManaged: "repo-a:fix-cookie"},
		{ID: "%11", MtManaged: "repo-b:auth"},
	}
	wts := []gitio.Worktree{
		// repo-a main worktree — must be filtered (Path == Repo).
		{Repo: "/r/repo-a", Path: "/r/repo-a"},
		// repo-a:fix-cookie — live, must be filtered.
		{Repo: "/r/repo-a", Path: "/r/repo-a/.worktrees/fix-cookie"},
		// repo-a:stale — dead, must appear.
		{Repo: "/r/repo-a", Path: "/r/repo-a/.worktrees/stale"},
		// repo-c:zombie — dead, must appear.
		{Repo: "/r/repo-c", Path: "/r/repo-c/.worktrees/zombie"},
	}

	got := buildSwitchRows(managed, wts)

	wantMarkers := []string{"live", "live", "dead", "dead", "new"}
	if len(got) != len(wantMarkers) {
		t.Fatalf("rows = %d, want %d: %+v", len(got), len(wantMarkers), got)
	}
	for i, want := range wantMarkers {
		if got[i].marker != want {
			t.Errorf("row %d marker = %q, want %q", i, got[i].marker, want)
		}
	}

	// dead rows must carry repo + branch so RunSwitch can dispatch into
	// RunNew via MT_REPO + MT_BRANCH without an extra git lookup.
	if got[2].title != "repo-a:stale" || got[2].repo != "/r/repo-a" || got[2].branch != "stale" {
		t.Errorf("dead row 1 wrong: %+v", got[2])
	}
	if got[3].title != "repo-c:zombie" || got[3].repo != "/r/repo-c" || got[3].branch != "zombie" {
		t.Errorf("dead row 2 wrong: %+v", got[3])
	}
}

// TestBuildSwitchRows_NoWorktreesStillHasNew — picker is never empty.
func TestBuildSwitchRows_NoWorktreesStillHasNew(t *testing.T) {
	got := buildSwitchRows(nil, nil)
	if len(got) != 1 || got[0].marker != "new" {
		t.Fatalf("rows = %+v, want single [new]", got)
	}
}
