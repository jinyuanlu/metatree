package dashboard

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jinyuanlu/metatree/internal/tmuxio"
)

// TestMarkPane_SetsBaseRefOption verifies that MarkPane writes the base ref
// to the @mt-base per-pane user option when baseRef is non-empty, and skips
// it when baseRef is empty. This is the pane-border-format input that
// shows "[origin/main]" alongside repo:branch.
func TestMarkPane_SetsBaseRefOption(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not on PATH; skipping integration test")
	}

	var b [3]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	dir := filepath.Join("/tmp", "mt-dash-"+hex.EncodeToString(b[:]))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Setenv("TMUX_TMPDIR", dir)
	if err := os.Unsetenv("TMUX"); err != nil {
		t.Fatalf("unsetenv TMUX: %v", err)
	}
	t.Cleanup(func() {
		_ = exec.Command("tmux", "kill-server").Run()
		_ = os.RemoveAll(dir)
	})

	session := tmuxio.SessionName("mt-test")
	window := tmuxio.WindowName("dash-test")
	if err := tmuxio.EnsureSession(session, window); err != nil {
		t.Fatalf("EnsureSession: %v", err)
	}
	if err := tmuxio.EnsureWindow(session, window); err != nil {
		t.Fatalf("EnsureWindow: %v", err)
	}

	panes, err := tmuxio.ListPanes(string(session) + ":" + string(window))
	if err != nil || len(panes) == 0 {
		t.Fatalf("ListPanes: %v (n=%d)", err, len(panes))
	}
	paneID := panes[0].ID

	t.Run("with baseRef, both options are set", func(t *testing.T) {
		if err := MarkPane(paneID, "myrepo:fix-cookie", "origin/main"); err != nil {
			t.Fatalf("MarkPane: %v", err)
		}
		gotManaged := showPaneOption(t, paneID, MtManagedKey)
		if gotManaged != "myrepo:fix-cookie" {
			t.Errorf("@mt-managed = %q, want %q", gotManaged, "myrepo:fix-cookie")
		}
		gotBase := showPaneOption(t, paneID, MtBaseKey)
		if gotBase != "origin/main" {
			t.Errorf("@mt-base = %q, want %q", gotBase, "origin/main")
		}
	})

	t.Run("with empty baseRef, @mt-base is not set", func(t *testing.T) {
		// Fresh pane so prior @mt-base from the first subtest doesn't leak.
		newID, err := tmuxio.SplitPane(string(session)+":"+string(window), dir, "sleep 30")
		if err != nil {
			t.Fatalf("SplitPane: %v", err)
		}
		if err := MarkPane(newID, "myrepo:other", ""); err != nil {
			t.Fatalf("MarkPane: %v", err)
		}
		gotBase := showPaneOption(t, newID, MtBaseKey)
		if gotBase != "" {
			t.Errorf("@mt-base on second pane = %q, want empty (baseRef was '')", gotBase)
		}
	})
}

func showPaneOption(t *testing.T, id tmuxio.PaneID, key string) string {
	t.Helper()
	out, err := exec.Command("tmux", "show-options", "-p", "-v", "-t", string(id), key).Output()
	if err != nil {
		// Unset options return non-zero on some tmux versions; treat as empty.
		return ""
	}
	return strings.TrimSpace(string(out))
}
