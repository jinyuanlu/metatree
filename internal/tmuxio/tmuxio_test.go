// Integration tests for tmuxio.
//
// These tests run a real tmux server inside an isolated TMUX_TMPDIR.
// Per CONTRIBUTING.md §17 #9, we do NOT mock tmux — the mock surface is the
// integration test surface, and faithfully simulating tmux's socket
// limit and OSC behavior is not worth it.
//
// macOS Unix domain socket paths max out around 104 bytes. We therefore
// build TMUX_TMPDIR under /tmp/mt-tmuxio-<hex> rather than t.TempDir(),
// whose default location (e.g. /var/folders/.../GoTmp...) blows past
// the limit and causes tmux to fail with "error too long for Unix
// domain socket".
package tmuxio

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fixture isolates one test from the user's tmux server (and from
// other tests) by pointing TMUX_TMPDIR at a fresh short directory.
// The returned cleanup kills the server and removes the dir.
type fixture struct {
	dir     string
	prevEnv string
	prevSet bool
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not on PATH; skipping integration test")
	}

	// 6 random hex chars → /tmp/mt-tmuxio-XXXXXX is well under the
	// macOS 104-byte AF_UNIX path limit.
	var b [3]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	dir := filepath.Join("/tmp", "mt-tmuxio-"+hex.EncodeToString(b[:]))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}

	prev, ok := os.LookupEnv("TMUX_TMPDIR")
	if err := os.Setenv("TMUX_TMPDIR", dir); err != nil {
		t.Fatalf("setenv: %v", err)
	}

	// Ensure tests never accidentally inherit "we're inside tmux"
	// behavior from the developer's outer shell.
	if err := os.Unsetenv("TMUX"); err != nil {
		t.Fatalf("unsetenv TMUX: %v", err)
	}

	return &fixture{dir: dir, prevEnv: prev, prevSet: ok}
}

func (f *fixture) cleanup(t *testing.T) {
	t.Helper()
	// Kill any server we spawned. Best-effort: if no server is
	// running, tmux exits non-zero and we don't care.
	_ = exec.Command("tmux", "kill-server").Run()

	if f.prevSet {
		_ = os.Setenv("TMUX_TMPDIR", f.prevEnv)
	} else {
		_ = os.Unsetenv("TMUX_TMPDIR")
	}
	_ = os.RemoveAll(f.dir)
}

// ---------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------

func TestEnsureSessionIdempotent(t *testing.T) {
	f := newFixture(t)
	defer f.cleanup(t)

	const sess = SessionName("mt-test")
	const win = WindowName("dash")

	if err := EnsureSession(sess, win); err != nil {
		t.Fatalf("first EnsureSession: %v", err)
	}
	if !HasSession(sess) {
		t.Fatalf("HasSession(%s) = false after EnsureSession", sess)
	}
	// Second call must be a no-op (no error, session still exists).
	if err := EnsureSession(sess, win); err != nil {
		t.Fatalf("second EnsureSession: %v", err)
	}
	if !HasSession(sess) {
		t.Fatalf("HasSession(%s) = false after idempotent EnsureSession", sess)
	}
}

func TestEnsureWindowIdempotent(t *testing.T) {
	f := newFixture(t)
	defer f.cleanup(t)

	const sess = SessionName("mt-test")
	const win = WindowName("dash")

	if err := EnsureSession(sess, win); err != nil {
		t.Fatalf("EnsureSession: %v", err)
	}
	// Window already exists — must no-op cleanly.
	if err := EnsureWindow(sess, win); err != nil {
		t.Fatalf("EnsureWindow on existing window: %v", err)
	}
	// New window — must create it.
	if err := EnsureWindow(sess, "second"); err != nil {
		t.Fatalf("EnsureWindow second: %v", err)
	}
	if err := EnsureWindow(sess, "second"); err != nil {
		t.Fatalf("EnsureWindow second (idempotent): %v", err)
	}
}

func TestListPanesEmpty(t *testing.T) {
	f := newFixture(t)
	defer f.cleanup(t)

	const sess = SessionName("mt-test")
	const win = WindowName("dash")
	if err := EnsureSession(sess, win); err != nil {
		t.Fatalf("EnsureSession: %v", err)
	}

	panes, err := ListPanes(string(sess) + ":" + string(win))
	if err != nil {
		t.Fatalf("ListPanes: %v", err)
	}
	if len(panes) != 1 {
		t.Fatalf("len(panes) = %d, want 1; got %+v", len(panes), panes)
	}
	p := panes[0]
	if p.ID == "" {
		t.Errorf("pane ID is empty")
	}
	if !strings.HasPrefix(string(p.ID), "%") {
		t.Errorf("pane ID %q does not start with %%", p.ID)
	}
	if !p.Active {
		t.Errorf("sole pane should be Active")
	}
	if p.MtManaged != "" {
		t.Errorf("fresh pane should have MtManaged=\"\", got %q", p.MtManaged)
	}
}

func TestListPanesWithMtManaged(t *testing.T) {
	f := newFixture(t)
	defer f.cleanup(t)

	const sess = SessionName("mt-test")
	const win = WindowName("dash")
	target := string(sess) + ":" + string(win)
	if err := EnsureSession(sess, win); err != nil {
		t.Fatalf("EnsureSession: %v", err)
	}

	panes, err := ListPanes(target)
	if err != nil {
		t.Fatalf("ListPanes: %v", err)
	}
	if len(panes) != 1 {
		t.Fatalf("len(panes) = %d, want 1", len(panes))
	}
	id := panes[0].ID

	const marker = "demo/feat-x"
	if err := SetPaneOption(id, "@mt-managed", marker); err != nil {
		t.Fatalf("SetPaneOption: %v", err)
	}

	panes, err = ListPanes(target)
	if err != nil {
		t.Fatalf("ListPanes (after set): %v", err)
	}
	if len(panes) != 1 {
		t.Fatalf("len(panes) = %d, want 1", len(panes))
	}
	if got := panes[0].MtManaged; got != marker {
		t.Errorf("MtManaged = %q, want %q", got, marker)
	}
}

func TestSplitAndKillPane(t *testing.T) {
	f := newFixture(t)
	defer f.cleanup(t)

	const sess = SessionName("mt-test")
	const win = WindowName("dash")
	target := string(sess) + ":" + string(win)
	if err := EnsureSession(sess, win); err != nil {
		t.Fatalf("EnsureSession: %v", err)
	}

	// Use a long-running command (sleep) so the pane stays alive long
	// enough for ListPanes to observe it. cwd intentionally empty —
	// inherits the parent pane's cwd, which is fine for this test.
	newID, err := SplitPane(target, "", "sleep 30")
	if err != nil {
		t.Fatalf("SplitPane: %v", err)
	}
	if !strings.HasPrefix(string(newID), "%") {
		t.Errorf("new pane id %q does not start with %%", newID)
	}

	panes, err := ListPanes(target)
	if err != nil {
		t.Fatalf("ListPanes after split: %v", err)
	}
	if len(panes) != 2 {
		t.Fatalf("after split: len(panes) = %d, want 2 — %+v", len(panes), panes)
	}

	if err := KillPane(newID); err != nil {
		t.Fatalf("KillPane: %v", err)
	}

	panes, err = ListPanes(target)
	if err != nil {
		t.Fatalf("ListPanes after kill: %v", err)
	}
	if len(panes) != 1 {
		t.Fatalf("after kill: len(panes) = %d, want 1 — %+v", len(panes), panes)
	}
}

func TestSetPaneTitle(t *testing.T) {
	f := newFixture(t)
	defer f.cleanup(t)

	const sess = SessionName("mt-test")
	const win = WindowName("dash")
	target := string(sess) + ":" + string(win)
	if err := EnsureSession(sess, win); err != nil {
		t.Fatalf("EnsureSession: %v", err)
	}

	panes, err := ListPanes(target)
	if err != nil || len(panes) != 1 {
		t.Fatalf("ListPanes: panes=%v err=%v", panes, err)
	}
	id := panes[0].ID

	const title = "demo-title"
	if err := SetPaneTitle(id, title); err != nil {
		t.Fatalf("SetPaneTitle: %v", err)
	}

	// Read pane_title back via display-message — pane_title is
	// informational (we don't return it from ListPanes), so we use
	// the underlying tmux verb directly to assert the side effect.
	out, err := exec.Command("tmux", "display-message", "-p", "-t", string(id), "#{pane_title}").Output()
	if err != nil {
		t.Fatalf("display-message: %v", err)
	}
	got := strings.TrimRight(string(out), "\n")
	if got != title {
		t.Errorf("pane_title = %q, want %q", got, title)
	}
}

func TestSetPaneOptionMtManaged(t *testing.T) {
	f := newFixture(t)
	defer f.cleanup(t)

	const sess = SessionName("mt-test")
	const win = WindowName("dash")
	target := string(sess) + ":" + string(win)
	if err := EnsureSession(sess, win); err != nil {
		t.Fatalf("EnsureSession: %v", err)
	}

	panes, err := ListPanes(target)
	if err != nil || len(panes) != 1 {
		t.Fatalf("ListPanes: panes=%v err=%v", panes, err)
	}
	id := panes[0].ID

	const value = "myrepo/feat-bar"
	if err := SetPaneOption(id, "@mt-managed", value); err != nil {
		t.Fatalf("SetPaneOption: %v", err)
	}

	panes, err = ListPanes(target)
	if err != nil {
		t.Fatalf("ListPanes: %v", err)
	}
	var found bool
	for _, p := range panes {
		if p.ID == id {
			found = true
			if p.MtManaged != value {
				t.Errorf("MtManaged = %q, want %q", p.MtManaged, value)
			}
		}
	}
	if !found {
		t.Errorf("pane %s not found in ListPanes output: %+v", id, panes)
	}
}

func TestBindPrefix(t *testing.T) {
	f := newFixture(t)
	defer f.cleanup(t)

	// list-keys / bind-key are server-scoped, not session-scoped, but
	// a server isn't running until we touch one. Spin up a session so
	// there's a server to bind on.
	if err := EnsureSession("mt-test", "dash"); err != nil {
		t.Fatalf("EnsureSession: %v", err)
	}

	if err := BindPrefix("x", "display-message hi"); err != nil {
		t.Fatalf("BindPrefix: %v", err)
	}

	keys, err := ListPrefixKeys()
	if err != nil {
		t.Fatalf("ListPrefixKeys: %v", err)
	}
	var found bool
	for _, k := range keys {
		// tmux's list-keys output for our binding looks like:
		//   bind-key    -T prefix x    display-message hi
		if strings.Contains(k, " x ") && strings.Contains(k, "display-message") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("bound key not present in list-keys output; got:\n%s", strings.Join(keys, "\n"))
	}
}

func TestInsideTmuxFromEnv(t *testing.T) {
	// This test does not need the fixture (no tmux server interaction),
	// but we still skip if tmux isn't on PATH so the suite has a single
	// gating predicate.
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not on PATH; skipping integration test")
	}

	prev, prevSet := os.LookupEnv("TMUX")
	defer func() {
		if prevSet {
			_ = os.Setenv("TMUX", prev)
		} else {
			_ = os.Unsetenv("TMUX")
		}
	}()

	if err := os.Unsetenv("TMUX"); err != nil {
		t.Fatalf("unsetenv: %v", err)
	}
	if InsideTmux() {
		t.Errorf("InsideTmux() = true with TMUX unset")
	}

	if err := os.Setenv("TMUX", "/tmp/tmux-1000/default,1234,0"); err != nil {
		t.Fatalf("setenv: %v", err)
	}
	if !InsideTmux() {
		t.Errorf("InsideTmux() = false with TMUX set")
	}

	if err := os.Setenv("TMUX", ""); err != nil {
		t.Fatalf("setenv empty: %v", err)
	}
	if InsideTmux() {
		t.Errorf("InsideTmux() = true with TMUX empty string")
	}
}
