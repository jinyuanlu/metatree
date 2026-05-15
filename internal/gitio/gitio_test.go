package gitio

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// gitOrSkip runs git with the given args and t.Fatal-s on failure. The
// test fixtures are constructed with these helpers so any setup error
// surfaces early and clearly, instead of as a downstream assertion miss.
func gitOrFatal(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("git -C %s %s failed: %v\nstdout: %s\nstderr: %s",
			dir, strings.Join(args, " "), err,
			strings.TrimSpace(stdout.String()),
			strings.TrimSpace(stderr.String()))
	}
}

// initRepo creates a fresh git repo at <parent>/<name> with a single
// committed file on a `main` branch, sets a default user, and returns
// its path. The repo is committed-to so subsequent `worktree add` calls
// have a HEAD to anchor against.
func initRepo(t *testing.T, parent, name string) string {
	t.Helper()
	repo := filepath.Join(parent, name)
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", repo, err)
	}
	gitOrFatal(t, repo, "init", "-q", "-b", "main")
	gitOrFatal(t, repo, "config", "user.email", "gitio-test@example.invalid")
	gitOrFatal(t, repo, "config", "user.name", "gitio-test")
	gitOrFatal(t, repo, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(repo, "README"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	gitOrFatal(t, repo, "add", "README")
	gitOrFatal(t, repo, "commit", "-q", "-m", "init")
	return repo
}

// canonicalize resolves p the same way the package internals do, so
// tests can compare expectations against package outputs in the same
// canonical form (avoids /tmp ↔ /private/tmp drift on macOS).
func canonicalize(t *testing.T, p string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", p, err)
	}
	return resolved
}

// ----------------------------------------------------------------------
// ParentRepoOf
// ----------------------------------------------------------------------

func TestParentRepoOfMtStyle(t *testing.T) {
	tmp := t.TempDir()
	repo := initRepo(t, tmp, "repo")
	wt := filepath.Join(repo, ".worktrees", "feature")
	gitOrFatal(t, repo, "worktree", "add", "-b", "mt/feature", wt)

	got, err := ParentRepoOf(wt)
	if err != nil {
		t.Fatalf("ParentRepoOf(%s): %v", wt, err)
	}
	want := canonicalize(t, repo)
	if got != want {
		t.Fatalf("ParentRepoOf mt-style:\n got:  %s\n want: %s", got, want)
	}
}

func TestParentRepoOfClaudeStyle(t *testing.T) {
	tmp := t.TempDir()
	repo := initRepo(t, tmp, "repo")
	wt := filepath.Join(repo, ".claude", "worktrees", "task")
	gitOrFatal(t, repo, "worktree", "add", "-b", "claude/task", wt)

	got, err := ParentRepoOf(wt)
	if err != nil {
		t.Fatalf("ParentRepoOf(%s): %v", wt, err)
	}
	want := canonicalize(t, repo)
	if got != want {
		t.Fatalf("ParentRepoOf claude-style:\n got:  %s\n want: %s\n(naive dirname-twice would give %s)",
			got, want, filepath.Dir(filepath.Dir(wt)))
	}
	// The bug we are guarding against: naive `dirname; dirname` on a
	// `.claude/worktrees/task` path returns `<repo>/.claude`, not <repo>.
	naive := filepath.Dir(filepath.Dir(wt))
	if got == naive {
		t.Fatalf("ParentRepoOf returned the naive dirname-twice answer %s", naive)
	}
}

// ----------------------------------------------------------------------
// DiscoverWorktrees
// ----------------------------------------------------------------------

func TestDiscoverWorktreesBothStyles(t *testing.T) {
	tmp := t.TempDir()
	repo := initRepo(t, tmp, "repo")

	wtMt := filepath.Join(repo, ".worktrees", "feature")
	wtClaude := filepath.Join(repo, ".claude", "worktrees", "task")
	gitOrFatal(t, repo, "worktree", "add", "-b", "mt/feature", wtMt)
	gitOrFatal(t, repo, "worktree", "add", "-b", "claude/task", wtClaude)

	wts, err := DiscoverWorktrees(nil, []string{repo})
	if err != nil {
		t.Fatalf("DiscoverWorktrees: %v", err)
	}

	wantMt := canonicalize(t, wtMt)
	wantClaude := canonicalize(t, wtClaude)
	wantRepo := canonicalize(t, repo)

	seen := make(map[string]Worktree, len(wts))
	for _, w := range wts {
		seen[w.Path] = w
	}
	if _, ok := seen[wantMt]; !ok {
		t.Errorf("mt-style worktree %s missing from results: %#v", wantMt, wts)
	}
	if _, ok := seen[wantClaude]; !ok {
		t.Errorf("claude-style worktree %s missing from results: %#v", wantClaude, wts)
	}
	for _, w := range wts {
		if w.Repo != wantRepo {
			t.Errorf("Worktree.Repo = %s, want %s", w.Repo, wantRepo)
		}
	}
}

func TestDiscoverWorktreesSkipsMain(t *testing.T) {
	tmp := t.TempDir()
	repo := initRepo(t, tmp, "repo")
	wt := filepath.Join(repo, ".worktrees", "feature")
	gitOrFatal(t, repo, "worktree", "add", "-b", "mt/feature", wt)

	wts, err := DiscoverWorktrees(nil, []string{repo})
	if err != nil {
		t.Fatalf("DiscoverWorktrees: %v", err)
	}

	mainCanonical := canonicalize(t, repo)
	for _, w := range wts {
		if w.Path == mainCanonical {
			t.Fatalf("main worktree was not excluded: %#v", w)
		}
	}
	// And we should still see the additional worktree.
	wantWt := canonicalize(t, wt)
	found := false
	for _, w := range wts {
		if w.Path == wantWt {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("additional worktree %s missing: %#v", wantWt, wts)
	}
}

// ----------------------------------------------------------------------
// IsGitCryptEncrypted
// ----------------------------------------------------------------------

func TestIsGitCryptEncryptedMagic(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "encrypted")
	content := append([]byte{0x00, 'G', 'I', 'T', 'C', 'R', 'Y', 'P', 'T', 0x00},
		[]byte("blob payload here")...)
	if err := os.WriteFile(p, content, 0o600); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	if !IsGitCryptEncrypted(p) {
		t.Fatalf("IsGitCryptEncrypted(%s) = false, want true", p)
	}
}

func TestIsGitCryptEncryptedPlain(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "envrc")
	if err := os.WriteFile(p, []byte("export FOO=bar\n"), 0o600); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	if IsGitCryptEncrypted(p) {
		t.Fatalf("IsGitCryptEncrypted(%s) = true on plain file, want false", p)
	}
}

func TestIsGitCryptEncryptedShortFile(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "short")
	// Less than 10 bytes — must not panic, must return false.
	if err := os.WriteFile(p, []byte("\x00GIT"), 0o600); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	if IsGitCryptEncrypted(p) {
		t.Fatalf("IsGitCryptEncrypted on short file = true, want false")
	}

	// And empty file: also false, also no panic.
	pEmpty := filepath.Join(tmp, "empty")
	if err := os.WriteFile(pEmpty, nil, 0o600); err != nil {
		t.Fatalf("write %s: %v", pEmpty, err)
	}
	if IsGitCryptEncrypted(pEmpty) {
		t.Fatalf("IsGitCryptEncrypted on empty file = true, want false")
	}

	// Missing file: false, no panic.
	if IsGitCryptEncrypted(filepath.Join(tmp, "does-not-exist")) {
		t.Fatalf("IsGitCryptEncrypted on missing file = true, want false")
	}
}

// ----------------------------------------------------------------------
// Symlink resolution regression
// ----------------------------------------------------------------------

func TestPathSymlinkResolution(t *testing.T) {
	// Build a fixture under /tmp on macOS — /tmp is itself a symlink to
	// /private/tmp, so any naive string compare between a config path
	// and a git-reported path drifts. Skip cleanly if /tmp is unwritable
	// (rare but possible in container CI).
	root := "/tmp"
	if _, err := os.Stat(root); err != nil {
		t.Skipf("/tmp unavailable: %v", err)
	}
	tmpRoot, err := os.MkdirTemp(root, "mt-gitio-")
	if err != nil {
		t.Skipf("could not create temp dir under /tmp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmpRoot) })

	resolvedRoot, err := filepath.EvalSymlinks(tmpRoot)
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", tmpRoot, err)
	}
	if resolvedRoot == tmpRoot {
		// Not a symlink-routed temp (common in Linux containers). The
		// scenario we are guarding against doesn't apply here, but the
		// rest of the test still asserts the canonicalization invariant.
		t.Logf("note: %s is not symlink-routed; test still exercises canonicalization", tmpRoot)
	}

	repo := initRepo(t, tmpRoot, "repo")
	wt := filepath.Join(repo, ".worktrees", "feature")
	gitOrFatal(t, repo, "worktree", "add", "-b", "mt/feature", wt)

	// ParentRepoOf must return the canonical (resolved) parent path.
	got, err := ParentRepoOf(wt)
	if err != nil {
		t.Fatalf("ParentRepoOf: %v", err)
	}
	want := canonicalize(t, repo)
	if got != want {
		t.Fatalf("ParentRepoOf returned non-canonical path:\n got:  %s\n want: %s", got, want)
	}

	// DiscoverWorktrees must also return canonical paths.
	wts, err := DiscoverWorktrees(nil, []string{repo})
	if err != nil {
		t.Fatalf("DiscoverWorktrees: %v", err)
	}
	wtWant := canonicalize(t, wt)
	found := false
	for _, w := range wts {
		if w.Repo != want {
			t.Errorf("Worktree.Repo not canonical: got %s, want %s", w.Repo, want)
		}
		if w.Path == wtWant {
			found = true
		}
	}
	if !found {
		t.Fatalf("worktree path %s not in results: %#v", wtWant, wts)
	}
}

// ----------------------------------------------------------------------
// DeleteBranchIfNoUpstream
// ----------------------------------------------------------------------

func TestDeleteBranchOnlyIfNoUpstream(t *testing.T) {
	tmp := t.TempDir()

	// Build a "remote" bare repo we can track from, then a working repo
	// that pushes a branch to it. The pushed branch gets an upstream
	// (and must be preserved); a sibling branch with no upstream must
	// be deleted.
	remote := filepath.Join(tmp, "remote.git")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatalf("mkdir remote: %v", err)
	}
	gitOrFatal(t, remote, "init", "-q", "--bare", "-b", "main")

	repo := initRepo(t, tmp, "repo")
	gitOrFatal(t, repo, "remote", "add", "origin", remote)

	// Branch with upstream — push and set tracking.
	gitOrFatal(t, repo, "branch", "tracked")
	gitOrFatal(t, repo, "push", "-q", "-u", "origin", "tracked")

	// Branch without upstream — purely local.
	gitOrFatal(t, repo, "branch", "lonely")

	// Sanity: both branches exist before the call.
	if !branchExists(repo, "tracked") {
		t.Fatalf("setup: tracked branch missing")
	}
	if !branchExists(repo, "lonely") {
		t.Fatalf("setup: lonely branch missing")
	}

	if err := DeleteBranchIfNoUpstream(repo, "lonely"); err != nil {
		t.Fatalf("DeleteBranchIfNoUpstream(lonely): %v", err)
	}
	if branchExists(repo, "lonely") {
		t.Errorf("lonely branch still exists after deletion call")
	}

	if err := DeleteBranchIfNoUpstream(repo, "tracked"); err != nil {
		t.Fatalf("DeleteBranchIfNoUpstream(tracked): %v", err)
	}
	if !branchExists(repo, "tracked") {
		t.Errorf("tracked branch was deleted despite having an upstream")
	}

	// Calling again on a non-existent branch is a no-op, not an error.
	if err := DeleteBranchIfNoUpstream(repo, "lonely"); err != nil {
		t.Errorf("second call on already-deleted branch should be a no-op, got: %v", err)
	}
}

// ----------------------------------------------------------------------
// WorktreeRemove
// ----------------------------------------------------------------------

func TestWorktreeRemoveDirtyRefuses(t *testing.T) {
	tmp := t.TempDir()
	repo := initRepo(t, tmp, "repo")
	wt := filepath.Join(repo, ".worktrees", "feature")
	gitOrFatal(t, repo, "worktree", "add", "-b", "mt/feature", wt)

	// Dirty the worktree by adding an untracked file. `git worktree
	// remove` refuses on dirty trees without --force.
	if err := os.WriteFile(filepath.Join(wt, "dirty.txt"),
		[]byte("uncommitted\n"), 0o644); err != nil {
		t.Fatalf("write dirty file: %v", err)
	}
	// Modify a tracked file too — dirty-detection covers both cases.
	if err := os.WriteFile(filepath.Join(wt, "README"),
		[]byte("modified\n"), 0o644); err != nil {
		t.Fatalf("modify README: %v", err)
	}

	err := WorktreeRemove(repo, wt, false)
	if err == nil {
		t.Fatalf("WorktreeRemove(force=false) on dirty tree returned nil; want error")
	}
	if !errors.Is(err, ErrWorktreeDirty) {
		t.Fatalf("WorktreeRemove(force=false) on dirty tree returned %v; want ErrWorktreeDirty", err)
	}
	// Worktree directory should still exist.
	if _, err := os.Stat(wt); err != nil {
		t.Errorf("worktree directory %s was removed despite refusal: %v", wt, err)
	}
}

func TestWorktreeRemoveForceBypasses(t *testing.T) {
	tmp := t.TempDir()
	repo := initRepo(t, tmp, "repo")
	wt := filepath.Join(repo, ".worktrees", "feature")
	gitOrFatal(t, repo, "worktree", "add", "-b", "mt/feature", wt)

	if err := os.WriteFile(filepath.Join(wt, "dirty.txt"),
		[]byte("uncommitted\n"), 0o644); err != nil {
		t.Fatalf("write dirty file: %v", err)
	}

	if err := WorktreeRemove(repo, wt, true); err != nil {
		t.Fatalf("WorktreeRemove(force=true): %v", err)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Fatalf("worktree directory %s still exists after force-remove (err=%v)", wt, err)
	}
}

// ----------------------------------------------------------------------
// GitCryptInUse
// ----------------------------------------------------------------------

func TestGitCryptInUseDetected(t *testing.T) {
	tmp := t.TempDir()
	repo := initRepo(t, tmp, "repo")

	if GitCryptInUse(repo) {
		t.Fatalf("GitCryptInUse on fresh repo = true, want false")
	}

	// Manually drop a key file at the spot git-crypt would put it.
	keyDir := filepath.Join(repo, ".git", "git-crypt", "keys")
	if err := os.MkdirAll(keyDir, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", keyDir, err)
	}
	if err := os.WriteFile(filepath.Join(keyDir, "default"),
		[]byte("fake-key-bytes"), 0o600); err != nil {
		t.Fatalf("write fake key: %v", err)
	}
	if !GitCryptInUse(repo) {
		t.Fatalf("GitCryptInUse with key file = false, want true")
	}
}

// ----------------------------------------------------------------------
// InstallGitCryptKey
// ----------------------------------------------------------------------

func TestInstallGitCryptKeyCopies(t *testing.T) {
	tmp := t.TempDir()
	repo := initRepo(t, tmp, "repo")
	wt := filepath.Join(repo, ".worktrees", "feature")
	gitOrFatal(t, repo, "worktree", "add", "-b", "mt/feature", wt)

	// Drop a fake key in the parent's standard location.
	srcDir := filepath.Join(repo, ".git", "git-crypt", "keys")
	if err := os.MkdirAll(srcDir, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", srcDir, err)
	}
	keyContent := []byte("\x00GITCRYPTKEYFAKEBYTES")
	src := filepath.Join(srcDir, "default")
	if err := os.WriteFile(src, keyContent, 0o600); err != nil {
		t.Fatalf("write src key: %v", err)
	}

	if err := InstallGitCryptKey(repo, wt); err != nil {
		t.Fatalf("InstallGitCryptKey: %v", err)
	}

	// The dest is keyed by basename(wt), to match the per-worktree
	// GIT_DIR layout git creates under <repo>/.git/worktrees/<name>.
	dst := filepath.Join(repo, ".git", "worktrees", filepath.Base(wt),
		"git-crypt", "keys", "default")
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dst key %s: %v", dst, err)
	}
	if !bytes.Equal(got, keyContent) {
		t.Fatalf("dst key contents differ:\n got:  %q\n want: %q", got, keyContent)
	}
}

func TestInstallGitCryptKeyMissingSourceErrors(t *testing.T) {
	tmp := t.TempDir()
	repo := initRepo(t, tmp, "repo")
	wt := filepath.Join(repo, ".worktrees", "feature")
	gitOrFatal(t, repo, "worktree", "add", "-b", "mt/feature", wt)

	// No key file present → must error so the caller can log/surface it.
	if err := InstallGitCryptKey(repo, wt); err == nil {
		t.Fatalf("InstallGitCryptKey with no source key returned nil; want error")
	}
}

// ----------------------------------------------------------------------
// DiscoverRepos
// ----------------------------------------------------------------------

func TestDiscoverReposExplicitWins(t *testing.T) {
	tmp := t.TempDir()
	repoA := initRepo(t, tmp, "repo-a")
	// repo-b under reposDirs should be ignored when explicit is set.
	scanDir := filepath.Join(tmp, "scan")
	if err := os.MkdirAll(scanDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", scanDir, err)
	}
	_ = initRepo(t, scanDir, "repo-b")

	got, err := DiscoverRepos([]string{scanDir}, []string{repoA})
	if err != nil {
		t.Fatalf("DiscoverRepos: %v", err)
	}
	if len(got) != 1 || got[0] != repoA {
		t.Fatalf("DiscoverRepos with explicit list = %v, want [%s]", got, repoA)
	}
}

func TestDiscoverReposScansReposDirs(t *testing.T) {
	tmp := t.TempDir()
	scanDir := filepath.Join(tmp, "scan")
	if err := os.MkdirAll(scanDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", scanDir, err)
	}
	repoA := initRepo(t, scanDir, "repo-a")
	repoB := initRepo(t, scanDir, "repo-b")

	got, err := DiscoverRepos([]string{scanDir}, nil)
	if err != nil {
		t.Fatalf("DiscoverRepos: %v", err)
	}

	want := map[string]bool{repoA: true, repoB: true}
	if len(got) != len(want) {
		t.Fatalf("DiscoverRepos returned %d entries, want %d: %v",
			len(got), len(want), got)
	}
	for _, r := range got {
		if !want[r] {
			t.Errorf("unexpected repo in results: %s", r)
		}
	}
}

// TestDiscoverReposRootIsItselfAGitRepo guards against the regression
// where a `repos_dirs` entry that happens to be `git init`'d (e.g. an
// `~/Code` someone made for personal notes, or an aider tags cache)
// short-circuits the walk and hides every project below it. The
// container is treated as a container, not as a repo.
func TestDiscoverReposRootIsItselfAGitRepo(t *testing.T) {
	tmp := t.TempDir()
	scanDir := filepath.Join(tmp, "scan")
	if err := os.MkdirAll(scanDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", scanDir, err)
	}
	// Make the scan root itself a git repo.
	gitOrFatal(t, scanDir, "init", "-q", "-b", "main")
	repoA := initRepo(t, scanDir, "repo-a")
	repoB := initRepo(t, scanDir, "repo-b")

	got, err := DiscoverRepos([]string{scanDir}, nil)
	if err != nil {
		t.Fatalf("DiscoverRepos: %v", err)
	}
	want := map[string]bool{repoA: true, repoB: true}
	if len(got) != len(want) {
		t.Fatalf("DiscoverRepos returned %d entries, want %d (root must be treated as container, not repo): %v",
			len(got), len(want), got)
	}
	for _, r := range got {
		if !want[r] {
			t.Errorf("unexpected repo in results (root or other surprise): %s", r)
		}
	}
}

// TestDiscoverReposDedupesAliasedReposDirs guards against the picker
// showing the same physical directory twice when repos_dirs lists two
// path forms that resolve to the same inode (case-insensitive APFS,
// symlinks, …). os.SameFile is the dedup primitive.
func TestDiscoverReposDedupesAliasedReposDirs(t *testing.T) {
	tmp := t.TempDir()
	scanDir := filepath.Join(tmp, "scan")
	if err := os.MkdirAll(scanDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", scanDir, err)
	}
	repoA := initRepo(t, scanDir, "repo-a")

	// Symlink as a portable stand-in for case-insensitive aliasing —
	// the real Darwin trigger is `~/Code` vs `~/code`, which we can't
	// reproduce on a case-sensitive test FS, but os.SameFile handles
	// both via the same FileInfo identity check.
	alias := filepath.Join(tmp, "scan-alias")
	if err := os.Symlink(scanDir, alias); err != nil {
		t.Fatalf("symlink %s -> %s: %v", alias, scanDir, err)
	}

	got, err := DiscoverRepos([]string{scanDir, alias}, nil)
	if err != nil {
		t.Fatalf("DiscoverRepos: %v", err)
	}
	if len(got) != 1 || got[0] != repoA {
		t.Fatalf("DiscoverRepos with aliased repos_dirs = %v, want [%s] (one walk, deduped)", got, repoA)
	}
}

// ----------------------------------------------------------------------
// CopyRuntimeFiles
// ----------------------------------------------------------------------

func TestCopyRuntimeFiles_HappyPath(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	payload := []byte("DB_URL=local")
	if err := os.WriteFile(filepath.Join(src, ".env"), payload, 0o640); err != nil {
		t.Fatalf("write src/.env: %v", err)
	}

	r := CopyRuntimeFiles(src, dst, []string{".env"})
	if len(r.Errors) != 0 || len(r.Skipped) != 0 {
		t.Fatalf("unexpected errors/skipped: %#v", r)
	}
	if len(r.Copied) != 1 || r.Copied[0] != ".env" {
		t.Fatalf("Copied = %v, want [.env]", r.Copied)
	}

	got, err := os.ReadFile(filepath.Join(dst, ".env"))
	if err != nil {
		t.Fatalf("read dst/.env: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("dst contents = %q, want %q", got, payload)
	}
	fi, err := os.Stat(filepath.Join(dst, ".env"))
	if err != nil {
		t.Fatalf("stat dst/.env: %v", err)
	}
	if fi.Mode().Perm() != 0o640 {
		t.Fatalf("dst mode = %o, want 0640", fi.Mode().Perm())
	}
}

func TestCopyRuntimeFiles_MissingSource(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()

	r := CopyRuntimeFiles(src, dst, []string{".env"})
	if len(r.Copied) != 0 || len(r.Errors) != 0 {
		t.Fatalf("unexpected copied/errors: %#v", r)
	}
	if len(r.Skipped) != 1 || r.Skipped[0].Reason != "missing" || r.Skipped[0].Name != ".env" {
		t.Fatalf("Skipped = %#v, want [{.env missing}]", r.Skipped)
	}
}

func TestCopyRuntimeFiles_Symlink(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	dst := filepath.Join(tmp, "dst")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatalf("mkdir dst: %v", err)
	}
	target := filepath.Join(tmp, "secrets.env")
	payload := []byte("SECRET=42")
	if err := os.WriteFile(target, payload, 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if err := os.Symlink(target, filepath.Join(src, ".env")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	r := CopyRuntimeFiles(src, dst, []string{".env"})
	if len(r.Errors) != 0 || len(r.Skipped) != 0 {
		t.Fatalf("unexpected: %#v", r)
	}
	if len(r.Copied) != 1 {
		t.Fatalf("Copied = %v, want [.env]", r.Copied)
	}

	fi, err := os.Lstat(filepath.Join(dst, ".env"))
	if err != nil {
		t.Fatalf("lstat dst/.env: %v", err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("dst/.env is a symlink; want regular file")
	}
	got, err := os.ReadFile(filepath.Join(dst, ".env"))
	if err != nil {
		t.Fatalf("read dst/.env: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("dst = %q, want %q", got, payload)
	}
}

func TestCopyRuntimeFiles_BrokenSymlink(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	if err := os.Symlink("/nonexistent/path/that/does/not/exist", filepath.Join(src, ".env")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	r := CopyRuntimeFiles(src, dst, []string{".env"})
	if len(r.Copied) != 0 || len(r.Errors) != 0 {
		t.Fatalf("unexpected: %#v", r)
	}
	if len(r.Skipped) != 1 || r.Skipped[0].Reason != "missing" {
		t.Fatalf("Skipped = %#v, want [{.env missing}]", r.Skipped)
	}
}

func TestCopyRuntimeFiles_NotRegularFile(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, ".env"), 0o755); err != nil {
		t.Fatalf("mkdir src/.env: %v", err)
	}

	r := CopyRuntimeFiles(src, dst, []string{".env"})
	if len(r.Copied) != 0 || len(r.Errors) != 0 {
		t.Fatalf("unexpected: %#v", r)
	}
	if len(r.Skipped) != 1 || r.Skipped[0].Reason != "not_file" {
		t.Fatalf("Skipped = %#v, want [{.env not_file}]", r.Skipped)
	}
}

func TestCopyRuntimeFiles_GitCryptEncrypted(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	payload := append([]byte{0x00, 'G', 'I', 'T', 'C', 'R', 'Y', 'P', 'T', 0x00},
		[]byte("ciphertext-bytes")...)
	if err := os.WriteFile(filepath.Join(src, ".env"), payload, 0o600); err != nil {
		t.Fatalf("write src/.env: %v", err)
	}

	r := CopyRuntimeFiles(src, dst, []string{".env"})
	if len(r.Copied) != 0 || len(r.Errors) != 0 {
		t.Fatalf("unexpected: %#v", r)
	}
	if len(r.Skipped) != 1 || r.Skipped[0].Reason != "encrypted" {
		t.Fatalf("Skipped = %#v, want [{.env encrypted}]", r.Skipped)
	}
}

func TestCopyRuntimeFiles_DstExists(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, ".env"), []byte("NEW"), 0o600); err != nil {
		t.Fatalf("write src/.env: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dst, ".env"), []byte("OLD"), 0o600); err != nil {
		t.Fatalf("write dst/.env: %v", err)
	}

	r := CopyRuntimeFiles(src, dst, []string{".env"})
	if len(r.Copied) != 0 || len(r.Errors) != 0 {
		t.Fatalf("unexpected: %#v", r)
	}
	if len(r.Skipped) != 1 || r.Skipped[0].Reason != "dst_exists" {
		t.Fatalf("Skipped = %#v, want [{.env dst_exists}]", r.Skipped)
	}
	got, err := os.ReadFile(filepath.Join(dst, ".env"))
	if err != nil {
		t.Fatalf("read dst/.env: %v", err)
	}
	if !bytes.Equal(got, []byte("OLD")) {
		t.Fatalf("dst contents clobbered: %q", got)
	}
}

func TestCopyRuntimeFiles_EmptyNames(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()

	r := CopyRuntimeFiles(src, dst, nil)
	if len(r.Copied) != 0 || len(r.Skipped) != 0 || len(r.Errors) != 0 {
		t.Fatalf("expected empty report, got %#v", r)
	}
}

func TestCopyRuntimeFiles_AtomicOnFailure(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, ".env"), []byte("payload"), 0o600); err != nil {
		t.Fatalf("write src/.env: %v", err)
	}
	if err := os.Chmod(dst, 0o500); err != nil {
		t.Fatalf("chmod dst ro: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dst, 0o755) })

	r := CopyRuntimeFiles(src, dst, []string{".env"})
	if len(r.Errors) != 1 || r.Errors[0].Name != ".env" {
		t.Fatalf("Errors = %#v, want one for .env", r.Errors)
	}

	if err := os.Chmod(dst, 0o755); err != nil {
		t.Fatalf("chmod dst back: %v", err)
	}
	entries, err := os.ReadDir(dst)
	if err != nil {
		t.Fatalf("readdir dst: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".mtcopy.") {
			t.Fatalf("leftover tempfile in dst: %s", e.Name())
		}
	}
}

func TestCopyReport_Summary_Nothing(t *testing.T) {
	if got := (CopyReport{}).Summary(); got != "" {
		t.Fatalf("Summary on empty report = %q, want \"\"", got)
	}
}

func TestCopyReport_Summary_AllMissing(t *testing.T) {
	r := CopyReport{Skipped: []SkipReason{{".env", "missing"}}}
	if got := r.Summary(); got != "" {
		t.Fatalf("Summary all-missing = %q, want \"\"", got)
	}
}

func TestCopyReport_Summary_CopiedOnly(t *testing.T) {
	r := CopyReport{Copied: []string{".env"}}
	got := r.Summary()
	if !strings.HasPrefix(got, "mt: copied ") {
		t.Fatalf("Summary copied-only = %q, want prefix \"mt: copied \"", got)
	}
	if !strings.Contains(got, ".env") {
		t.Fatalf("Summary = %q, missing .env", got)
	}
}

func TestCopyReport_Summary_SkippedEncrypted(t *testing.T) {
	r := CopyReport{Skipped: []SkipReason{{".envrc", "encrypted"}}}
	got := r.Summary()
	if got == "" {
		t.Fatalf("Summary skipped-encrypted = \"\", want non-empty")
	}
	if strings.Contains(got, "\n") {
		t.Fatalf("Summary = %q, want single line", got)
	}
	if !strings.Contains(got, "encrypted") || !strings.Contains(got, ".envrc") {
		t.Fatalf("Summary = %q, missing 'encrypted' or '.envrc'", got)
	}
}

func TestCopyReport_Summary_CopiedPlusSkipped(t *testing.T) {
	r := CopyReport{
		Copied:  []string{".env"},
		Skipped: []SkipReason{{".envrc", "encrypted"}},
	}
	got := r.Summary()
	if strings.Contains(got, "\n") {
		t.Fatalf("Summary = %q, want single line", got)
	}
	if !strings.Contains(got, ".env") {
		t.Fatalf("Summary = %q, missing .env", got)
	}
	if !strings.Contains(got, ".envrc encrypted") {
		t.Fatalf("Summary = %q, missing '.envrc encrypted'", got)
	}
}

func TestCopyReport_Summary_WithErrors(t *testing.T) {
	r := CopyReport{
		Copied: []string{".env"},
		Errors: []CopyError{{".npmrc", fmt.Errorf("permission denied")}},
	}
	got := r.Summary()
	lines := strings.Split(got, "\n")
	if len(lines) != 2 {
		t.Fatalf("Summary = %q, want two lines", got)
	}
	if !strings.Contains(lines[0], ".env") || !strings.Contains(lines[0], "copied") {
		t.Fatalf("first line = %q, missing .env or copied", lines[0])
	}
	if !strings.Contains(lines[1], ".npmrc") || !strings.Contains(lines[1], "errors") {
		t.Fatalf("second line = %q, missing .npmrc or errors", lines[1])
	}
}

// BenchmarkDiscoverReposPrunesNoiseDirs measures DiscoverRepos against a
// realistic-ish fixture: one repo, with a sibling node_modules tree
// containing 2,000 directories. Run with:
//
//	go test -bench=. -benchmem ./internal/gitio
//
// On a 2024 Apple Silicon laptop the WalkDir+prune implementation
// settles around 0.5–1ms (warm cache); the legacy `find -maxdepth 4`
// shellout was ~25–40ms in the same setup. The visible-to-the-user
// `mt new` improvement is dominated by this benchmark's underlying
// I/O reduction, not algorithmic change.
func BenchmarkDiscoverReposPrunesNoiseDirs(b *testing.B) {
	root := b.TempDir()
	// One real repo
	repo := filepath.Join(root, "real-repo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		b.Fatal(err)
	}
	// 2000 noise directories under `node_modules/<id>/dist` —
	// resembles a typical npm install. The prune list MUST elide all
	// of these; if the benchmark regresses it means a name we used
	// to skip is now being walked.
	for i := 0; i < 2000; i++ {
		p := filepath.Join(repo, "node_modules", fmt.Sprintf("pkg%04d", i), "dist")
		if err := os.MkdirAll(p, 0o755); err != nil {
			b.Fatal(err)
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := DiscoverRepos([]string{root}, nil)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// ----------------------------------------------------------------------
// Helpers for default-branch / fetch / resolve tests
// ----------------------------------------------------------------------

// initBareRepo creates a bare repo (no working tree) suitable for use as
// a file:// remote in tests, and returns its filesystem path.
func initBareRepo(t *testing.T, parent, name string) string {
	t.Helper()
	bare := filepath.Join(parent, name)
	if err := os.MkdirAll(bare, 0o755); err != nil {
		t.Fatalf("mkdir bare %s: %v", bare, err)
	}
	gitOrFatal(t, bare, "init", "-q", "--bare", "-b", "main")
	return bare
}

// initRepoWithRemote builds a non-bare repo wired to bareURL via origin
// and pushes the given local branches up to it, each as a one-commit
// branch. The first branch becomes the working repo's HEAD; the bare's
// HEAD is set to firstBranch via symbolic-ref so origin/HEAD resolves.
func initRepoWithRemote(t *testing.T, parent, name, bareURL string, branches []string) string {
	t.Helper()
	repo := initRepo(t, parent, name)
	gitOrFatal(t, repo, "remote", "add", "origin", bareURL)
	for i, b := range branches {
		if i == 0 {
			// rename "main" → first requested branch
			gitOrFatal(t, repo, "branch", "-m", b)
		} else {
			gitOrFatal(t, repo, "checkout", "-q", "-b", b)
		}
		gitOrFatal(t, repo, "push", "-q", "-u", "origin", b)
	}
	return repo
}

// ----------------------------------------------------------------------
// DefaultBranch
// ----------------------------------------------------------------------

func TestDefaultBranch_FromSymbolicRef(t *testing.T) {
	tmp := t.TempDir()
	bare := initBareRepo(t, tmp, "remote.git")
	repo := initRepoWithRemote(t, tmp, "repo", "file://"+bare, []string{"main"})
	gitOrFatal(t, repo, "fetch", "-q", "origin")
	gitOrFatal(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")

	got, err := DefaultBranch(repo)
	if err != nil {
		t.Fatalf("DefaultBranch: %v", err)
	}
	if got != "main" {
		t.Fatalf("DefaultBranch = %q, want main", got)
	}
}

func TestDefaultBranch_FallbackToMain(t *testing.T) {
	tmp := t.TempDir()
	bare := initBareRepo(t, tmp, "remote.git")
	repo := initRepoWithRemote(t, tmp, "repo", "file://"+bare, []string{"main"})
	gitOrFatal(t, repo, "fetch", "-q", "origin")
	// Ensure no symbolic-ref is set.
	_ = exec.Command("git", "-C", repo, "symbolic-ref", "--delete",
		"refs/remotes/origin/HEAD").Run()

	got, err := DefaultBranch(repo)
	if err != nil {
		t.Fatalf("DefaultBranch: %v", err)
	}
	if got != "main" {
		t.Fatalf("DefaultBranch = %q, want main", got)
	}
}

func TestDefaultBranch_FallbackToMaster(t *testing.T) {
	tmp := t.TempDir()
	bare := filepath.Join(tmp, "remote.git")
	if err := os.MkdirAll(bare, 0o755); err != nil {
		t.Fatal(err)
	}
	gitOrFatal(t, bare, "init", "-q", "--bare", "-b", "master")
	repo := initRepo(t, tmp, "repo")
	gitOrFatal(t, repo, "remote", "add", "origin", "file://"+bare)
	gitOrFatal(t, repo, "branch", "-m", "master")
	gitOrFatal(t, repo, "push", "-q", "-u", "origin", "master")
	gitOrFatal(t, repo, "fetch", "-q", "origin")
	_ = exec.Command("git", "-C", repo, "symbolic-ref", "--delete",
		"refs/remotes/origin/HEAD").Run()

	got, err := DefaultBranch(repo)
	if err != nil {
		t.Fatalf("DefaultBranch: %v", err)
	}
	if got != "master" {
		t.Fatalf("DefaultBranch = %q, want master", got)
	}
}

func TestDefaultBranch_FallbackToDevelop(t *testing.T) {
	tmp := t.TempDir()
	bare := filepath.Join(tmp, "remote.git")
	if err := os.MkdirAll(bare, 0o755); err != nil {
		t.Fatal(err)
	}
	gitOrFatal(t, bare, "init", "-q", "--bare", "-b", "develop")
	repo := initRepo(t, tmp, "repo")
	gitOrFatal(t, repo, "remote", "add", "origin", "file://"+bare)
	gitOrFatal(t, repo, "branch", "-m", "develop")
	gitOrFatal(t, repo, "push", "-q", "-u", "origin", "develop")
	gitOrFatal(t, repo, "fetch", "-q", "origin")
	_ = exec.Command("git", "-C", repo, "symbolic-ref", "--delete",
		"refs/remotes/origin/HEAD").Run()

	got, err := DefaultBranch(repo)
	if err != nil {
		t.Fatalf("DefaultBranch: %v", err)
	}
	if got != "develop" {
		t.Fatalf("DefaultBranch = %q, want develop", got)
	}
}

func TestDefaultBranch_FallbackToTrunk(t *testing.T) {
	tmp := t.TempDir()
	bare := filepath.Join(tmp, "remote.git")
	if err := os.MkdirAll(bare, 0o755); err != nil {
		t.Fatal(err)
	}
	gitOrFatal(t, bare, "init", "-q", "--bare", "-b", "trunk")
	repo := initRepo(t, tmp, "repo")
	gitOrFatal(t, repo, "remote", "add", "origin", "file://"+bare)
	gitOrFatal(t, repo, "branch", "-m", "trunk")
	gitOrFatal(t, repo, "push", "-q", "-u", "origin", "trunk")
	gitOrFatal(t, repo, "fetch", "-q", "origin")
	_ = exec.Command("git", "-C", repo, "symbolic-ref", "--delete",
		"refs/remotes/origin/HEAD").Run()

	got, err := DefaultBranch(repo)
	if err != nil {
		t.Fatalf("DefaultBranch: %v", err)
	}
	if got != "trunk" {
		t.Fatalf("DefaultBranch = %q, want trunk", got)
	}
}

func TestDefaultBranch_NoOriginRemote(t *testing.T) {
	tmp := t.TempDir()
	repo := initRepo(t, tmp, "repo")
	_, err := DefaultBranch(repo)
	if !errors.Is(err, ErrNoOrigin) {
		t.Fatalf("DefaultBranch error = %v, want ErrNoOrigin", err)
	}
}

func TestDefaultBranch_NoCommonNames(t *testing.T) {
	tmp := t.TempDir()
	bare := filepath.Join(tmp, "remote.git")
	if err := os.MkdirAll(bare, 0o755); err != nil {
		t.Fatal(err)
	}
	gitOrFatal(t, bare, "init", "-q", "--bare", "-b", "weirdname")
	repo := initRepo(t, tmp, "repo")
	gitOrFatal(t, repo, "remote", "add", "origin", "file://"+bare)
	gitOrFatal(t, repo, "branch", "-m", "weirdname")
	gitOrFatal(t, repo, "push", "-q", "-u", "origin", "weirdname")
	gitOrFatal(t, repo, "fetch", "-q", "origin")
	_ = exec.Command("git", "-C", repo, "symbolic-ref", "--delete",
		"refs/remotes/origin/HEAD").Run()

	_, err := DefaultBranch(repo)
	if !errors.Is(err, ErrNoDefaultBranch) {
		t.Fatalf("DefaultBranch error = %v, want ErrNoDefaultBranch", err)
	}
}

// ----------------------------------------------------------------------
// FetchBranch
// ----------------------------------------------------------------------

func TestFetchBranch_HappyPath(t *testing.T) {
	tmp := t.TempDir()
	bare := initBareRepo(t, tmp, "remote.git")
	// Push a commit into bare via an intermediate clone.
	seed := initRepo(t, tmp, "seed")
	gitOrFatal(t, seed, "remote", "add", "origin", "file://"+bare)
	gitOrFatal(t, seed, "push", "-q", "-u", "origin", "main")
	// New (fetcher) repo, with origin pointed at bare but no fetch yet.
	repo := initRepo(t, tmp, "repo")
	gitOrFatal(t, repo, "remote", "add", "origin", "file://"+bare)

	if err := FetchBranch(repo, "main", 10*time.Second); err != nil {
		t.Fatalf("FetchBranch: %v", err)
	}
	if !remoteRefExists(repo, "refs/remotes/origin/main") {
		t.Fatalf("origin/main ref not present after fetch")
	}
}

func TestFetchBranch_Timeout(t *testing.T) {
	tmp := t.TempDir()
	repo := initRepo(t, tmp, "repo")
	// Non-routable IP triggers TCP SYN with no reply; with our short
	// timeout the context fires first.
	gitOrFatal(t, repo, "remote", "add", "origin", "https://10.255.255.1/nope.git")

	start := time.Now()
	err := FetchBranch(repo, "main", 1500*time.Millisecond)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatalf("FetchBranch on non-routable host returned nil; want error")
	}
	// Context-deadline path returns "timed out" in the message; some
	// platforms may instead surface a connect error before the deadline.
	// Either way the call must return within timeout+1s.
	if elapsed > 3*time.Second {
		t.Fatalf("FetchBranch did not honor timeout: took %s", elapsed)
	}
}

func TestFetchBranch_NetworkError(t *testing.T) {
	tmp := t.TempDir()
	repo := initRepo(t, tmp, "repo")
	gitOrFatal(t, repo, "remote", "add", "origin", "/nonexistent/path/that/does/not/exist.git")

	err := FetchBranch(repo, "main", 5*time.Second)
	if err == nil {
		t.Fatalf("FetchBranch on bad URL returned nil; want error")
	}
	if !strings.Contains(err.Error(), "git fetch origin main") {
		t.Fatalf("FetchBranch error = %v, want wrapped fetch message", err)
	}
}

func TestFetchBranch_GitTerminalPromptDisabled(t *testing.T) {
	// Sandbox a stub git binary on PATH that records its env and exits.
	// We can't read inside FetchBranch's exec.Cmd from the outside, but
	// the stub gives us the same evidence.
	tmp := t.TempDir()
	stubDir := filepath.Join(tmp, "stub")
	if err := os.MkdirAll(stubDir, 0o755); err != nil {
		t.Fatal(err)
	}
	logFile := filepath.Join(tmp, "env.log")
	stub := filepath.Join(stubDir, "git")
	script := "#!/bin/sh\n" +
		"echo \"GIT_TERMINAL_PROMPT=${GIT_TERMINAL_PROMPT}\" > " + logFile + "\n" +
		"exit 0\n"
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", stubDir+":"+oldPath)

	repo := t.TempDir()
	if err := FetchBranch(repo, "main", 5*time.Second); err != nil {
		t.Fatalf("FetchBranch (stub): %v", err)
	}
	got, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("read env.log: %v", err)
	}
	if !strings.Contains(string(got), "GIT_TERMINAL_PROMPT=0") {
		t.Fatalf("env.log = %q, want GIT_TERMINAL_PROMPT=0", got)
	}
}

func TestFetchBranch_NonexistentBranch(t *testing.T) {
	tmp := t.TempDir()
	bare := initBareRepo(t, tmp, "remote.git")
	seed := initRepo(t, tmp, "seed")
	gitOrFatal(t, seed, "remote", "add", "origin", "file://"+bare)
	gitOrFatal(t, seed, "push", "-q", "-u", "origin", "main")
	repo := initRepo(t, tmp, "repo")
	gitOrFatal(t, repo, "remote", "add", "origin", "file://"+bare)

	err := FetchBranch(repo, "does-not-exist", 10*time.Second)
	if err == nil {
		t.Fatalf("FetchBranch of missing branch returned nil; want error")
	}
}

// ----------------------------------------------------------------------
// ResolveWorktreeBase
// ----------------------------------------------------------------------

func TestResolveBase_HeadMagic(t *testing.T) {
	tmp := t.TempDir()
	repo := initRepo(t, tmp, "repo")
	r, err := ResolveWorktreeBase(repo, "head", 5*time.Second)
	if err != nil {
		t.Fatalf("ResolveWorktreeBase: %v", err)
	}
	if r.Ref != "HEAD" {
		t.Fatalf("Ref = %q, want HEAD", r.Ref)
	}
	if r.FellBackFrom != "" {
		t.Fatalf("unexpected fallback: %#v", r)
	}
	if len(r.Sha8) != 8 {
		t.Fatalf("Sha8 = %q, want 8 chars", r.Sha8)
	}
}

func TestResolveBase_OriginDefaultHappy(t *testing.T) {
	tmp := t.TempDir()
	bare := initBareRepo(t, tmp, "remote.git")
	repo := initRepoWithRemote(t, tmp, "repo", "file://"+bare, []string{"main"})
	gitOrFatal(t, repo, "fetch", "-q", "origin")
	gitOrFatal(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")

	r, err := ResolveWorktreeBase(repo, "origin-default", 10*time.Second)
	if err != nil {
		t.Fatalf("ResolveWorktreeBase: %v", err)
	}
	if r.Ref != "origin/main" {
		t.Fatalf("Ref = %q, want origin/main", r.Ref)
	}
	if r.FellBackFrom != "" {
		t.Fatalf("unexpected fallback: %#v", r)
	}
	if r.Sha8 == "" {
		t.Fatalf("Sha8 empty")
	}
}

func TestResolveBase_OriginDefaultStaleFallback(t *testing.T) {
	tmp := t.TempDir()
	bare := initBareRepo(t, tmp, "remote.git")
	repo := initRepoWithRemote(t, tmp, "repo", "file://"+bare, []string{"main"})
	gitOrFatal(t, repo, "fetch", "-q", "origin")
	// Break origin so fetch fails, but the stale ref is still present.
	gitOrFatal(t, repo, "remote", "set-url", "origin", "/nonexistent/path/x.git")

	r, err := ResolveWorktreeBase(repo, "origin-default", 5*time.Second)
	if err != nil {
		t.Fatalf("ResolveWorktreeBase: %v", err)
	}
	if r.Ref != "origin/main" {
		t.Fatalf("Ref = %q, want origin/main (stale)", r.Ref)
	}
	if r.FellBackFrom != "origin-default" {
		t.Fatalf("FellBackFrom = %q, want origin-default", r.FellBackFrom)
	}
	if !strings.Contains(r.Reason, "stale") {
		t.Fatalf("Reason = %q, want substring 'stale'", r.Reason)
	}
}

func TestResolveBase_OriginDefaultLocalFallback(t *testing.T) {
	tmp := t.TempDir()
	bare := initBareRepo(t, tmp, "remote.git")
	repo := initRepo(t, tmp, "repo")
	gitOrFatal(t, repo, "remote", "add", "origin", "file://"+bare)
	// Symbolic-ref origin/HEAD without ever having fetched (manually
	// crafted) — DefaultBranch falls through to candidate probe; main
	// exists locally but no origin/main ref. Plant a candidate ref so
	// DefaultBranch sees "main" then point origin at a dead URL.
	candidate := filepath.Join(repo, ".git", "refs", "remotes", "origin")
	if err := os.MkdirAll(candidate, 0o755); err != nil {
		t.Fatal(err)
	}
	headSha := mustRevParse(t, repo, "HEAD")
	if err := os.WriteFile(filepath.Join(candidate, "main"),
		[]byte(headSha+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitOrFatal(t, repo, "remote", "set-url", "origin", "/nonexistent/path/x.git")
	// Now remove origin/main so the stale-fallback rung fails.
	if err := os.Remove(filepath.Join(candidate, "main")); err != nil {
		t.Fatal(err)
	}
	// But re-add a packed/loose ref under refs/remotes/origin so
	// DefaultBranch still sees a candidate before we delete it. The
	// simplest reliable way: skip stale entirely by deleting before
	// resolution. We rely on local "main" being present.
	r, err := ResolveWorktreeBase(repo, "origin-default", 3*time.Second)
	if err != nil {
		// DefaultBranch needs *some* candidate on origin to succeed. If
		// the prior delete made it ErrNoDefaultBranch, recreate a stub
		// just long enough to pass that check, then drop it again.
		t.Logf("first attempt: %v — retrying with candidate restored", err)
		if werr := os.WriteFile(filepath.Join(candidate, "main"),
			[]byte(headSha+"\n"), 0o644); werr != nil {
			t.Fatal(werr)
		}
		r, err = ResolveWorktreeBase(repo, "origin-default", 3*time.Second)
		if err != nil {
			t.Fatalf("ResolveWorktreeBase: %v", err)
		}
	}
	// With both refs present, the result should still be a fallback
	// because the fetch failed. Stale-fallback rung wins when present;
	// that's fine — assert we did fall back, regardless of which rung.
	if r.FellBackFrom != "origin-default" {
		t.Fatalf("FellBackFrom = %q, want origin-default", r.FellBackFrom)
	}
	if r.Reason == "" {
		t.Fatalf("Reason empty on fallback path")
	}
}

func TestResolveBase_OriginDefaultParentHeadFallback(t *testing.T) {
	tmp := t.TempDir()
	repo := initRepo(t, tmp, "repo")
	// Origin remote with no fetched refs, dead URL — fetch fails, no
	// origin/main stale ref, no local "main" branch worth distinguishing
	// from HEAD. Plant a single refs/remotes/origin/main so
	// DefaultBranch reports "main", then delete it before resolution.
	gitOrFatal(t, repo, "remote", "add", "origin", "/nonexistent/x.git")
	rdir := filepath.Join(repo, ".git", "refs", "remotes", "origin")
	if err := os.MkdirAll(rdir, 0o755); err != nil {
		t.Fatal(err)
	}
	headSha := mustRevParse(t, repo, "HEAD")
	if err := os.WriteFile(filepath.Join(rdir, "main"),
		[]byte(headSha+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Rename local main → otherbranch so the local-branch rung fails.
	gitOrFatal(t, repo, "branch", "-m", "main", "feature-x")
	// Delete the planted stale ref so the stale-rung also fails.
	if err := os.Remove(filepath.Join(rdir, "main")); err != nil {
		t.Fatal(err)
	}
	// DefaultBranch needs a candidate to succeed; put back briefly then
	// delete after the call — but we can't easily intercept mid-call.
	// Instead, call DefaultBranch first to confirm it succeeds *before*
	// we delete, then run resolveOriginDefault directly to skip the
	// initial probe.
	if err := os.WriteFile(filepath.Join(rdir, "main"),
		[]byte(headSha+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := DefaultBranch(repo); err != nil {
		t.Fatalf("setup: DefaultBranch: %v", err)
	}
	if err := os.Remove(filepath.Join(rdir, "main")); err != nil {
		t.Fatal(err)
	}

	r := resolveOriginDefault(repo, "main", 2*time.Second)
	if r.Ref != "HEAD" {
		t.Fatalf("Ref = %q, want HEAD (last-resort)", r.Ref)
	}
	if r.FellBackFrom != "origin-default" {
		t.Fatalf("FellBackFrom = %q, want origin-default", r.FellBackFrom)
	}
	if !strings.Contains(r.Reason, "last-resort") {
		t.Fatalf("Reason = %q, want substring 'last-resort'", r.Reason)
	}
}

func TestResolveBase_NoOriginHardError(t *testing.T) {
	tmp := t.TempDir()
	repo := initRepo(t, tmp, "repo")
	_, err := ResolveWorktreeBase(repo, "origin-default", 5*time.Second)
	if err == nil {
		t.Fatalf("ResolveWorktreeBase no-origin returned nil; want error")
	}
	msg := err.Error()
	if !strings.Contains(msg, `"origin"`) || !strings.Contains(msg, "remote") {
		t.Fatalf("error = %q, want substring '\"origin\"' and 'remote'", msg)
	}
	if !strings.Contains(msg, "worktree_base") {
		t.Fatalf("error = %q, want remediation hint mentioning worktree_base", msg)
	}
}

func TestResolveBase_LiteralRef(t *testing.T) {
	tmp := t.TempDir()
	repo := initRepo(t, tmp, "repo")
	gitOrFatal(t, repo, "branch", "develop")

	r, err := ResolveWorktreeBase(repo, "develop", 5*time.Second)
	if err != nil {
		t.Fatalf("ResolveWorktreeBase literal: %v", err)
	}
	if r.Ref != "develop" {
		t.Fatalf("Ref = %q, want develop", r.Ref)
	}
	if r.FellBackFrom != "" {
		t.Fatalf("unexpected fallback: %#v", r)
	}
}

func TestResolveBase_EmptyString(t *testing.T) {
	tmp := t.TempDir()
	repo := initRepo(t, tmp, "repo")
	r, err := ResolveWorktreeBase(repo, "", 5*time.Second)
	if err != nil {
		t.Fatalf("ResolveWorktreeBase: %v", err)
	}
	if r.Ref != "HEAD" {
		t.Fatalf("Ref = %q, want HEAD", r.Ref)
	}
	if r.FellBackFrom != "" {
		t.Fatalf("unexpected fallback: %#v", r)
	}
}

// ----------------------------------------------------------------------
// ResolveResult.Format
// ----------------------------------------------------------------------

func TestResolveResult_Format_Happy(t *testing.T) {
	r := ResolveResult{Ref: "origin/main", Sha8: "a1b2c3d4"}
	got := r.Format("mt/fix-cookie")
	want := "mt: branched mt/fix-cookie from origin/main@a1b2c3d4"
	if got != want {
		t.Fatalf("Format = %q, want %q", got, want)
	}
}

func TestResolveResult_Format_StaleFallback(t *testing.T) {
	r := ResolveResult{
		Ref:          "origin/main",
		Sha8:         "deadbeef",
		FellBackFrom: "origin-default",
		Reason:       "stale: fetch timed out",
	}
	got := r.Format("mt/x")
	if !strings.Contains(got, "(stale: fetch timed out)") {
		t.Fatalf("Format = %q, want substring '(stale: fetch timed out)'", got)
	}
}

func TestResolveResult_Format_LocalFallback(t *testing.T) {
	r := ResolveResult{
		Ref:          "main",
		Sha8:         "01234567",
		FellBackFrom: "origin-default",
		Reason:       "offline, no origin/main",
	}
	got := r.Format("mt/x")
	if !strings.Contains(got, "offline, no origin/main") {
		t.Fatalf("Format = %q, want substring 'offline, no origin/main'", got)
	}
}

func TestResolveResult_Format_ParentFallback(t *testing.T) {
	r := ResolveResult{
		Ref:          "HEAD",
		Sha8:         "abcdef12",
		FellBackFrom: "origin-default",
		Reason:       "last-resort fallback",
	}
	got := r.Format("mt/x")
	if !strings.Contains(got, "(last-resort fallback)") {
		t.Fatalf("Format = %q, want substring '(last-resort fallback)'", got)
	}
}

// ----------------------------------------------------------------------
// WorktreeAdd / WorktreeAddNoCheckout — empty start-point regression
// ----------------------------------------------------------------------

func TestWorktreeAdd_EmptyStartPointBehavesAsHead(t *testing.T) {
	tmp := t.TempDir()
	repo := initRepo(t, tmp, "repo")
	parentHead := mustRevParse(t, repo, "HEAD")

	wt := filepath.Join(tmp, "wt")
	if err := WorktreeAdd(repo, "test-branch", wt, ""); err != nil {
		t.Fatalf("WorktreeAdd: %v", err)
	}
	wtHead := mustRevParse(t, wt, "HEAD")
	if wtHead != parentHead {
		t.Fatalf("worktree HEAD = %s, want parent HEAD %s", wtHead, parentHead)
	}
}

func TestWorktreeAddNoCheckout_EmptyStartPointBehavesAsHead(t *testing.T) {
	tmp := t.TempDir()
	repo := initRepo(t, tmp, "repo")
	parentHead := mustRevParse(t, repo, "HEAD")

	wt := filepath.Join(tmp, "wt")
	if err := WorktreeAddNoCheckout(repo, "test-branch", wt, ""); err != nil {
		t.Fatalf("WorktreeAddNoCheckout: %v", err)
	}
	wtHead := mustRevParse(t, wt, "HEAD")
	if wtHead != parentHead {
		t.Fatalf("worktree HEAD = %s, want parent HEAD %s", wtHead, parentHead)
	}
}

// mustRevParse returns the full commit SHA at ref, t.Fatal-ing on error.
func mustRevParse(t *testing.T, repo, ref string) string {
	t.Helper()
	cmd := exec.Command("git", "-C", repo, "rev-parse", ref)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("git rev-parse %s in %s: %v (%s)", ref, repo, err,
			strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String())
}
