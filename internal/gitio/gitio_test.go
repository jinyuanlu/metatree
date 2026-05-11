package gitio

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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

	if err := WorktreeRemove(repo, wt, false); err == nil {
		t.Fatalf("WorktreeRemove(force=false) on dirty tree returned nil; want error")
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
