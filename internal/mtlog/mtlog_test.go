package mtlog

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// isolateEnv clears the env vars that influence Path() resolution so a
// test can set exactly what it needs without leakage from the surrounding
// shell. t.Setenv automatically restores the previous values at test end.
func isolateEnv(t *testing.T) {
	t.Helper()
	t.Setenv("MT_LOG", "")
	t.Setenv("XDG_STATE_HOME", "")
	// Keep HOME stable across tests; default Path() needs it for the
	// fallback branch.
	if home, err := os.UserHomeDir(); err == nil {
		t.Setenv("HOME", home)
	}
}

func TestPathDefault(t *testing.T) {
	isolateEnv(t)
	// Force a known HOME so we can assert the suffix deterministically.
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	// On macOS, HOME is the resolution input — Path uses os.UserHomeDir()
	// which respects HOME on POSIX.

	got := Path()
	wantSuffix := filepath.Join(".local", "state", "mt", "mt.log")
	if !strings.HasSuffix(got, wantSuffix) {
		t.Fatalf("Path() = %q, want suffix %q", got, wantSuffix)
	}
	// And it should be rooted under our temp HOME.
	if !strings.HasPrefix(got, tmp) {
		t.Fatalf("Path() = %q, want prefix %q", got, tmp)
	}
}

func TestPathXDGOverride(t *testing.T) {
	isolateEnv(t)
	t.Setenv("XDG_STATE_HOME", "/tmp/foo")

	got := Path()
	want := "/tmp/foo/mt/mt.log"
	if got != want {
		t.Fatalf("Path() = %q, want %q", got, want)
	}
}

func TestPathMTLogOverride(t *testing.T) {
	isolateEnv(t)
	t.Setenv("XDG_STATE_HOME", "/tmp/foo")
	t.Setenv("MT_LOG", "/tmp/custom.log")

	got := Path()
	want := "/tmp/custom.log"
	if got != want {
		t.Fatalf("Path() = %q, want %q (MT_LOG must win over XDG)", got, want)
	}
}

func TestWriteAppends(t *testing.T) {
	isolateEnv(t)
	logPath := filepath.Join(t.TempDir(), "mt.log")
	t.Setenv("MT_LOG", logPath)

	want := []string{"first line", "second line", "third line"}
	for _, l := range want {
		Write(l)
	}

	got := Tail(3)
	if len(got) != len(want) {
		t.Fatalf("Tail(3) returned %d lines, want %d: %v", len(got), len(want), got)
	}
	for i, line := range got {
		if !strings.HasSuffix(line, want[i]) {
			t.Errorf("line %d = %q, want suffix %q", i, line, want[i])
		}
	}
}

func TestWriteMakesDir(t *testing.T) {
	isolateEnv(t)
	// Two missing directory levels — MkdirAll should create both.
	logPath := filepath.Join(t.TempDir(), "does", "not", "exist", "mt.log")
	t.Setenv("MT_LOG", logPath)

	Write("hello")

	if _, err := os.Stat(logPath); err != nil {
		t.Fatalf("expected log file to exist at %s, got %v", logPath, err)
	}
	got := Tail(1)
	if len(got) != 1 || !strings.HasSuffix(got[0], "hello") {
		t.Fatalf("Tail(1) = %v, want [...hello]", got)
	}
}

func TestWriteSilentOnReadOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX-only permission semantics")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses read-only directory protection")
	}
	isolateEnv(t)

	parent := t.TempDir()
	roDir := filepath.Join(parent, "ro")
	if err := os.Mkdir(roDir, 0o555); err != nil {
		t.Fatalf("setup: mkdir ro: %v", err)
	}
	// Restore writable mode so t.TempDir cleanup can remove the tree.
	t.Cleanup(func() { _ = os.Chmod(roDir, 0o755) })

	logPath := filepath.Join(roDir, "subdir", "mt.log")
	t.Setenv("MT_LOG", logPath)

	// Must not panic and must not leak an error. We invoke through a
	// recover-guarded shim to assert no panic propagates either.
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("Write panicked under read-only dir: %v", r)
			}
		}()
		Write("should be silently dropped")
	}()

	got := Tail(1)
	if len(got) != 0 {
		t.Fatalf("Tail(1) = %v, want empty (file should not have been created)", got)
	}
}

func TestWriteFormat(t *testing.T) {
	isolateEnv(t)
	logPath := filepath.Join(t.TempDir(), "mt.log")
	t.Setenv("MT_LOG", logPath)

	const content = "INVOKE cmd=switch args=switch -z"
	Write(content)

	got := Tail(1)
	if len(got) != 1 {
		t.Fatalf("Tail(1) returned %d lines, want 1", len(got))
	}

	// [YYYY-MM-DDTHH:MM:SSZ] pid=N <content>
	pattern := `^\[\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z\] pid=\d+ ` + regexp.QuoteMeta(content) + `$`
	re := regexp.MustCompile(pattern)
	if !re.MatchString(got[0]) {
		t.Fatalf("line %q does not match %s", got[0], pattern)
	}
}

func TestTailFewerLinesThanRequested(t *testing.T) {
	isolateEnv(t)
	logPath := filepath.Join(t.TempDir(), "mt.log")
	t.Setenv("MT_LOG", logPath)

	Write("a")
	Write("b")
	Write("c")

	got := Tail(10)
	if len(got) != 3 {
		t.Fatalf("Tail(10) on 3-line file returned %d lines, want 3: %v", len(got), got)
	}
}

func TestTailMissingFile(t *testing.T) {
	isolateEnv(t)
	logPath := filepath.Join(t.TempDir(), "absent.log")
	t.Setenv("MT_LOG", logPath)

	got := Tail(5)
	if len(got) != 0 {
		t.Fatalf("Tail on missing file = %v, want empty slice", got)
	}
}

func TestTailReturnsLastN(t *testing.T) {
	isolateEnv(t)
	logPath := filepath.Join(t.TempDir(), "mt.log")
	t.Setenv("MT_LOG", logPath)

	for _, l := range []string{"one", "two", "three", "four", "five"} {
		Write(l)
	}

	got := Tail(2)
	if len(got) != 2 {
		t.Fatalf("Tail(2) returned %d lines, want 2", len(got))
	}
	if !strings.HasSuffix(got[0], "four") {
		t.Errorf("Tail(2)[0] = %q, want suffix %q", got[0], "four")
	}
	if !strings.HasSuffix(got[1], "five") {
		t.Errorf("Tail(2)[1] = %q, want suffix %q", got[1], "five")
	}
}

func TestTailZeroOrNegative(t *testing.T) {
	isolateEnv(t)
	logPath := filepath.Join(t.TempDir(), "mt.log")
	t.Setenv("MT_LOG", logPath)

	Write("a")
	Write("b")

	for _, n := range []int{0, -1, -10} {
		if got := Tail(n); len(got) != 0 {
			t.Errorf("Tail(%d) = %v, want empty", n, got)
		}
	}
}
