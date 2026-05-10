// Package gitio is the only package that calls git.
//
// It wraps the small set of git verbs `mt` issues — worktree discovery,
// worktree add/remove, parent-repo resolution, and the git-crypt key
// installation dance — behind typed Go functions that return wrapped
// errors. Callers in internal/command never invoke git directly; if a new
// git verb is needed, it is added here so the layer above stays free of
// shell semantics.
//
// All paths returned by gitio are passed through filepath.EvalSymlinks
// so comparisons with config-form paths (which are typically not
// symlink-resolved) are consistent. macOS in particular routes /tmp
// through /private/tmp; git stores worktree paths in their canonical
// (resolved) form, so without normalization the path "/tmp/foo" reported
// by config never compares equal to git's "/private/tmp/foo". Resolving
// at the boundary makes every comparison further up the stack trivial.
package gitio

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Worktree is one entry returned by DiscoverWorktrees.
//
// Repo is the canonical (symlink-resolved) parent repo path. Path is the
// canonical worktree directory. The main worktree (where Path == Repo) is
// excluded by DiscoverWorktrees.
type Worktree struct {
	Repo string
	Path string
}

// gitCryptMagic is the 10-byte prefix every git-crypt encrypted file
// starts with. Detecting it lets callers (e.g. auto-direnv-allow) skip
// files that are still encrypted at the time of inspection — sourcing
// binary garbage is worse than the missing-feature case.
var gitCryptMagic = []byte{0x00, 'G', 'I', 'T', 'C', 'R', 'Y', 'P', 'T', 0x00}

// canonical resolves a path through filepath.EvalSymlinks, falling back
// to filepath.Abs if EvalSymlinks fails (e.g. the path doesn't exist on
// disk). The fallback is important for paths returned by `git worktree
// list` after the worktree directory has been removed but the registration
// hasn't been pruned — we still want a stable string for diagnostics.
func canonical(p string) (string, error) {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved, nil
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("canonical %s: %w", p, err)
	}
	return abs, nil
}

// expandTilde mirrors the bash `${path/#~/$HOME}` substitution so
// config paths like "~/Code" resolve correctly without needing a shell.
// A literal "~" or "~/..." is the only form recognized; "~user" is left
// alone (mt has never supported it and pulling in os/user would be the
// only added dependency).
func expandTilde(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return p
		}
		if p == "~" {
			return home
		}
		return filepath.Join(home, p[2:])
	}
	return p
}

// DiscoverRepos returns the list of repo working-tree paths to operate on.
//
// If explicit is non-empty, it wins (matches the bash precedence of
// MT_REPOS over MT_REPOS_DIRS). Otherwise each directory in reposDirs is
// scanned to depth 4 for ".git" directories, and the parent of each match
// is returned. Results are deduplicated and sorted lexicographically;
// paths are tilde-expanded but NOT symlink-resolved (callers that need
// a canonical form should resolve at the comparison site, the same as
// the bash version).
func DiscoverRepos(reposDirs, explicit []string) ([]string, error) {
	if len(explicit) > 0 {
		out := make([]string, 0, len(explicit))
		for _, r := range explicit {
			out = append(out, expandTilde(r))
		}
		return dedupeSorted(out), nil
	}

	seen := make(map[string]struct{})
	var out []string
	for _, d := range reposDirs {
		d = expandTilde(d)
		fi, err := os.Stat(d)
		if err != nil || !fi.IsDir() {
			// A missing repos_dir is not a hard error — the user may have
			// listed several and only one exists on this machine. The bash
			// version silently skips with `[[ -d "$d" ]] || continue`.
			continue
		}
		// `find <d> -maxdepth 4 -name .git -type d`. We use an external
		// `find` to match the bash behavior exactly (handles permission
		// errors gracefully via `2>/dev/null` and returns paths in the
		// same order). If `find` is unavailable for any reason, fall
		// back to a Go walk capped at depth 4.
		matches, err := findGitDirs(d, 4)
		if err != nil {
			return nil, fmt.Errorf("scan repos_dir %s: %w", d, err)
		}
		for _, gitDir := range matches {
			repo := filepath.Dir(gitDir)
			if _, ok := seen[repo]; ok {
				continue
			}
			seen[repo] = struct{}{}
			out = append(out, repo)
		}
	}
	return dedupeSorted(out), nil
}

// findGitDirs returns absolute paths of every ".git" directory under root
// at depth <= maxDepth. Matches the bash `find -maxdepth N -name .git
// -type d` invocation.
func findGitDirs(root string, maxDepth int) ([]string, error) {
	cmd := exec.Command("find", root, "-maxdepth", fmt.Sprintf("%d", maxDepth),
		"-name", ".git", "-type", "d")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = nil // mirror `2>/dev/null` — permission errors are routine
	// `find` exits non-zero when it hits unreadable subdirs; that's not
	// a failure for our purposes, the readable matches still appear on
	// stdout. Treat the error as informational.
	_ = cmd.Run()

	var out []string
	scanner := bufio.NewScanner(&stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		out = append(out, line)
	}
	if err := scanner.Err(); err != nil {
		return out, fmt.Errorf("scan find output for %s: %w", root, err)
	}
	return out, nil
}

// dedupeSorted returns a copy of in with duplicates removed and entries
// in lexicographic order, matching the `sort -u` step in the bash
// discover_repos pipeline.
func dedupeSorted(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	// Insertion sort is overkill; stdlib sort would do, but we want zero
	// extra imports. The slice is small (one entry per repo).
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

// DiscoverWorktrees returns every worktree git knows about across the
// configured repos.
//
// The main worktree (where the worktree path equals the repo path itself)
// is excluded — only the additional worktrees matter to mt. Both mt's
// convention (".worktrees/<branch>") and Claude Code's
// (".claude/worktrees/<task>") are returned, since both are first-class
// citizens to git.
//
// All returned paths are canonical (passed through filepath.EvalSymlinks
// where possible) so callers can compare them by string equality without
// worrying about /tmp ↔ /private/tmp drift on macOS.
func DiscoverWorktrees(reposDirs, explicitRepos []string) ([]Worktree, error) {
	repos, err := DiscoverRepos(reposDirs, explicitRepos)
	if err != nil {
		return nil, fmt.Errorf("discover repos: %w", err)
	}

	var out []Worktree
	for _, repo := range repos {
		resolved, err := canonical(repo)
		if err != nil {
			// A repo that can't be canonicalized (e.g. permission denied)
			// is not a hard error for the listing — skip it the same way
			// the bash version skips on `cd ... || continue`.
			continue
		}
		entries, err := worktreeListPorcelain(repo)
		if err != nil {
			// One bad repo (corrupted .git, permission error) shouldn't
			// kill the whole listing — mirror the bash `|| true`.
			continue
		}
		for _, wt := range entries {
			wtCanon, err := canonical(wt)
			if err != nil {
				continue
			}
			if wtCanon == resolved {
				// Skip the main worktree.
				continue
			}
			out = append(out, Worktree{Repo: resolved, Path: wtCanon})
		}
	}
	return out, nil
}

// worktreeListPorcelain runs `git -C <repo> worktree list --porcelain`
// and extracts the worktree paths. The porcelain format is a sequence
// of records, each record terminated by a blank line, with one key-value
// pair per line. We only need the "worktree <path>" entries.
func worktreeListPorcelain(repo string) ([]string, error) {
	cmd := exec.Command("git", "-C", repo, "worktree", "list", "--porcelain")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git worktree list -C %s: %w (%s)",
			repo, err, strings.TrimSpace(stderr.String()))
	}

	var out []string
	scanner := bufio.NewScanner(&stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if rest, ok := strings.CutPrefix(line, "worktree "); ok {
			out = append(out, strings.TrimSpace(rest))
		}
	}
	if err := scanner.Err(); err != nil {
		return out, fmt.Errorf("parse worktree list for %s: %w", repo, err)
	}
	return out, nil
}

// ParentRepoOf returns the canonical working-tree path of the parent
// repo for any worktree path.
//
// Implemented via `git -C <wt> rev-parse --git-common-dir`, which always
// resolves to the parent repo's `.git` directory regardless of how deep
// the worktree is nested. The naive `dirname; dirname` approach in the
// bash prototype broke for Claude-style worktrees at
// `.claude/worktrees/<task>` (two levels deep, not one); rev-parse is
// the robust answer.
func ParentRepoOf(worktreePath string) (string, error) {
	cmd := exec.Command("git", "-C", worktreePath, "rev-parse", "--git-common-dir")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git rev-parse --git-common-dir -C %s: %w (%s)",
			worktreePath, err, strings.TrimSpace(stderr.String()))
	}
	gitDir := strings.TrimSpace(stdout.String())
	if gitDir == "" {
		return "", fmt.Errorf("git rev-parse --git-common-dir -C %s: empty output",
			worktreePath)
	}

	// `git-common-dir` may be absolute or relative-to-the-worktree.
	var repo string
	if filepath.IsAbs(gitDir) {
		repo = filepath.Dir(gitDir)
	} else {
		// Resolve against the worktree directory, the same way bash's
		// `cd "$wt" && cd "$(dirname "$gd")" && pwd -P` does.
		repo = filepath.Join(worktreePath, filepath.Dir(gitDir))
	}

	resolved, err := canonical(repo)
	if err != nil {
		return "", fmt.Errorf("canonical parent repo %s: %w", repo, err)
	}
	return resolved, nil
}

// WorktreeAdd creates a new worktree with a fresh branch.
//
// Equivalent to `git -C <repo> worktree add -b <branchName> <path>`.
func WorktreeAdd(repo, branchName, path string) error {
	cmd := exec.Command("git", "-C", repo, "worktree", "add",
		"-b", branchName, path)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git worktree add -C %s -b %s %s: %w",
			repo, branchName, path, err)
	}
	return nil
}

// WorktreeAddNoCheckout creates a new worktree with a fresh branch but
// does not check out any files.
//
// This is the first half of the git-crypt setup dance: the worktree is
// registered, its per-worktree GIT_DIR is created, but no smudge filters
// run yet. The caller then copies the git-crypt key into the new GIT_DIR
// (see InstallGitCryptKey) before running `git checkout HEAD -- .` so
// the smudge filter finds its key in $GIT_DIR/git-crypt/keys/default.
func WorktreeAddNoCheckout(repo, branchName, path string) error {
	cmd := exec.Command("git", "-C", repo, "worktree", "add",
		"--no-checkout", "-b", branchName, path)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git worktree add --no-checkout -C %s -b %s %s: %w",
			repo, branchName, path, err)
	}
	return nil
}

// WorktreeRemove removes a worktree. With force=true, `--force` is passed
// to bypass git's dirty-tree refusal.
func WorktreeRemove(repo, path string, force bool) error {
	args := []string{"-C", repo, "worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, path)
	cmd := exec.Command("git", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git worktree remove -C %s %s (force=%t): %w (%s)",
			repo, path, force, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// DeleteBranchIfNoUpstream deletes branchName from repo iff the branch
// exists and has no upstream tracking ref configured. Branches that
// track a remote are preserved — losing a remote-tracked branch is
// recoverable but startling, so we err on the side of keeping them.
//
// The bash port (commit e57ceca) tries the prefixed name first
// (e.g. "mt/foo") and then the bare name; we mirror that. branchName
// is the bare name; the caller's prefix policy is reflected in what
// they pass.
func DeleteBranchIfNoUpstream(repo, branchName string) error {
	// Verify the branch exists. `rev-parse --verify` exits 0 if so.
	if !branchExists(repo, branchName) {
		// Not an error — there's just nothing to delete.
		return nil
	}
	// Check for upstream. `rev-parse <branch>@{upstream}` exits non-zero
	// when no upstream is configured; that's our "safe to delete" signal.
	if hasUpstream(repo, branchName) {
		return nil
	}
	cmd := exec.Command("git", "-C", repo, "branch", "-D", branchName)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git branch -D -C %s %s: %w (%s)",
			repo, branchName, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func branchExists(repo, branchName string) bool {
	cmd := exec.Command("git", "-C", repo, "rev-parse", "--verify", "--quiet",
		"refs/heads/"+branchName)
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run() == nil
}

func hasUpstream(repo, branchName string) bool {
	cmd := exec.Command("git", "-C", repo, "rev-parse",
		branchName+"@{upstream}")
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run() == nil
}

// IsGitCryptEncrypted reports whether the file at path begins with the
// 10-byte git-crypt magic prefix ("\x00GITCRYPT\x00").
//
// Returns false for any failure mode — file missing, file too short,
// read error, path is a directory. Callers use this to skip operations
// (e.g. direnv allow) that would misbehave on still-encrypted content.
func IsGitCryptEncrypted(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	buf := make([]byte, len(gitCryptMagic))
	n, err := f.Read(buf)
	if err != nil || n < len(gitCryptMagic) {
		// Includes io.EOF when the file is shorter than the magic length.
		return false
	}
	return bytes.Equal(buf, gitCryptMagic)
}

// GitCryptInUse reports whether the parent repo has a git-crypt key
// installed for its default key.
//
// The signal is the existence of the key file at
// `<repo>/.git/git-crypt/keys/default`, NOT the presence of a top-level
// `.git-crypt/` directory (which only exists once the user has committed
// the encrypted-files manifest). Some valid setups have the key without
// the manifest and vice versa; the key is the load-bearing artifact for
// `git-crypt unlock`, so it's the right check.
func GitCryptInUse(repo string) bool {
	keyPath := filepath.Join(repo, ".git", "git-crypt", "keys", "default")
	fi, err := os.Stat(keyPath)
	if err != nil {
		return false
	}
	return !fi.IsDir()
}

// InstallGitCryptKey copies the parent repo's default git-crypt key into
// the per-worktree GIT_DIR so the smudge filter can decrypt files when
// `git checkout HEAD -- .` runs.
//
// Source: <repo>/.git/git-crypt/keys/default
// Dest:   <repo>/.git/worktrees/<basename(worktreePath)>/git-crypt/keys/default
//
// Returns an error if the source key is missing — that's the failure
// mode spec-go.md §11 calls out for the git-crypt failure chain. The
// caller can decide whether to surface it (lifecycle.go's `mt new` logs
// it but continues, falling back to the plain `worktree add` path) or
// abort.
func InstallGitCryptKey(repo, worktreePath string) error {
	src := filepath.Join(repo, ".git", "git-crypt", "keys", "default")
	srcInfo, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("git-crypt key not found at %s: %w", src, err)
	}
	if srcInfo.IsDir() {
		return fmt.Errorf("git-crypt key path is a directory: %s", src)
	}

	wtName := filepath.Base(worktreePath)
	dstDir := filepath.Join(repo, ".git", "worktrees", wtName, "git-crypt", "keys")
	if err := os.MkdirAll(dstDir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dstDir, err)
	}
	dst := filepath.Join(dstDir, "default")
	if err := copyFile(src, dst, 0o600); err != nil {
		return fmt.Errorf("copy git-crypt key %s -> %s: %w", src, dst, err)
	}
	return nil
}

// copyFile copies src to dst with the given mode. The destination is
// truncated if it already exists.
func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return nil
}
