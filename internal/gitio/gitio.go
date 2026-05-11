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
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
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

	// Resolve + filter the list once. expandTilde is cheap; os.Stat
	// per dir is one syscall, also cheap. We do this serially because
	// it's already O(n) where n = number of repos_dirs (typically ≤3).
	//
	// Dedup via os.SameFile so two entries that point at the same
	// physical directory walk only once. This matters on macOS APFS
	// (case-insensitive), where `["~/Code", "~/code"]` are aliases
	// for the same inode and would otherwise produce two identical
	// walks and duplicated picker entries. SameFile is also symlink-
	// aware, so `["~/Code", "~/symlink-to-Code"]` collapse correctly.
	type job struct {
		dir     string
		info    os.FileInfo
		matches []string
		err     error
	}
	var jobs []*job
	for _, d := range reposDirs {
		d = expandTilde(d)
		fi, err := os.Stat(d)
		if err != nil || !fi.IsDir() {
			// A missing repos_dir is not a hard error — the user may have
			// listed several and only one exists on this machine. The bash
			// version silently skips with `[[ -d "$d" ]] || continue`.
			continue
		}
		dup := false
		for _, prior := range jobs {
			if os.SameFile(prior.info, fi) {
				dup = true
				break
			}
		}
		if dup {
			continue
		}
		jobs = append(jobs, &job{dir: d, info: fi})
	}

	// Walk each repos_dir in its own goroutine. The walks are entirely
	// I/O-bound and independent — running them concurrently drops total
	// wall time to ≈max(per-dir time) instead of sum. For a single
	// repos_dir (the common case) this is a no-op overhead.
	var wg sync.WaitGroup
	for _, j := range jobs {
		wg.Add(1)
		go func(j *job) {
			defer wg.Done()
			j.matches, j.err = findGitDirs(j.dir, 4)
		}(j)
	}
	wg.Wait()

	seen := make(map[string]struct{})
	var out []string
	for _, j := range jobs {
		if j.err != nil {
			return nil, fmt.Errorf("scan repos_dir %s: %w", j.dir, j.err)
		}
		for _, gitDir := range j.matches {
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

// prunedDirs is the set of directory names we never recurse into.
// Every entry is a directory that (a) commonly contains tens of thousands
// of files and (b) is guaranteed to never contain a project's `.git`.
//
// The bash version had no equivalent: it relied on `find` walking the
// whole tree, which on a typical dev machine wastes ~99% of stat I/O on
// dependency caches. Pruning these names is the single biggest win in
// this scanner.
var prunedDirs = map[string]struct{}{
	"node_modules":  {}, // npm/yarn/pnpm
	"vendor":        {}, // Go modules cache, Composer, Bundler
	"target":        {}, // Rust, Maven
	"build":         {}, // generic
	"dist":          {}, // generic
	"out":           {}, // generic
	"Pods":          {}, // CocoaPods
	"__pycache__":   {}, // Python
	"venv":          {},
	".venv":         {},
	"env":           {}, // common Python virtualenv name
	".env":          {}, // not a dir on most projects but cheap to list
	".cache":        {},
	".gradle":       {},
	".next":         {},
	".nuxt":         {},
	".turbo":        {},
	".idea":         {}, // JetBrains
	".vscode":       {}, // VSCode workspace state
	".pytest_cache": {},
	".mypy_cache":   {},
	".ruff_cache":   {},
	".tox":          {},
	"DerivedData":   {}, // Xcode
}

// findGitDirs returns the absolute path of `.git` for every git repo
// under root at depth <= maxDepth. The implementation:
//
//  1. Uses filepath.WalkDir (no os/exec overhead, full prune control).
//  2. Skips dependency caches by name (see prunedDirs).
//  3. Probes each visited dir for a `.git` child via os.Stat, and on
//     a hit returns fs.SkipDir so we don't descend into the repo —
//     the user doesn't care about a repo's internal layout, and
//     submodules' nested .gits would be noise here.
//
// On a real dev tree (~11k dirs, 56 repos), this typically runs in
// 5-15ms warm cache and 50-200ms cold cache, vs 50-2000ms for the
// previous `find -maxdepth 4` shellout.
func findGitDirs(root string, maxDepth int) ([]string, error) {
	rootSep := strings.Count(root, string(filepath.Separator))
	var out []string
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// Permission denied, racy mid-walk delete, etc. Routine in
			// real homedirs (e.g., ~/Library entries). Treat each as
			// "skip whatever we hit" without aborting the whole walk.
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		// Depth check: count separators relative to root. The root itself
		// is depth 0; its children depth 1; …
		depth := strings.Count(path, string(filepath.Separator)) - rootSep
		if depth > maxDepth {
			return fs.SkipDir
		}
		name := d.Name()
		if depth == 0 {
			// The root is a *container* of repos by contract; we never
			// short-circuit on it, even if it happens to be `git init`'d.
			// Otherwise a `~/Code` that someone ran `git init` in (e.g.
			// for personal notes or an aider tags cache) would hide every
			// project beneath it. If a single repo really is the target,
			// that's what `repos = [...]` (the explicit list) is for.
			return nil
		}
		if _, prune := prunedDirs[name]; prune {
			return fs.SkipDir
		}
		// Probe for .git. One os.Stat per visited dir — the dominant cost
		// of the scan, but unavoidable. We do this BEFORE descending so a
		// hit short-circuits the rest of the subtree.
		gitPath := filepath.Join(path, ".git")
		fi, err := os.Stat(gitPath)
		if err == nil && fi.IsDir() {
			out = append(out, gitPath)
			return fs.SkipDir // don't descend into the repo's contents
		}
		return nil
	})
	if walkErr != nil {
		return out, fmt.Errorf("walk %s: %w", root, walkErr)
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

type SkipReason struct {
	Name   string
	Reason string
}

type CopyError struct {
	Name string
	Err  error
}

type CopyReport struct {
	Copied  []string
	Skipped []SkipReason
	Errors  []CopyError
}

// CopyRuntimeFiles copies the named files from src to dst. Single-component
// names only (caller validates). Follows symlinks; output is always a
// regular file. Skips missing, non-regular-file, git-crypt-encrypted, and
// pre-existing destination paths. Writes are atomic temp+rename; per-file
// errors are recorded and the batch continues.
func CopyRuntimeFiles(src, dst string, names []string) CopyReport {
	var r CopyReport
	for _, name := range names {
		copyOne(src, dst, name, &r)
	}
	return r
}

func copyOne(src, dst, name string, r *CopyReport) {
	srcPath := filepath.Join(src, name)
	fi, err := os.Lstat(srcPath)
	if err != nil {
		if os.IsNotExist(err) {
			r.Skipped = append(r.Skipped, SkipReason{name, "missing"})
			return
		}
		r.Errors = append(r.Errors, CopyError{name, err})
		return
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		resolved, err := filepath.EvalSymlinks(srcPath)
		if err != nil {
			r.Skipped = append(r.Skipped, SkipReason{name, "missing"})
			return
		}
		srcPath = resolved
		fi, err = os.Stat(srcPath)
		if err != nil {
			if os.IsNotExist(err) {
				r.Skipped = append(r.Skipped, SkipReason{name, "missing"})
				return
			}
			r.Errors = append(r.Errors, CopyError{name, err})
			return
		}
	}
	if !fi.Mode().IsRegular() {
		r.Skipped = append(r.Skipped, SkipReason{name, "not_file"})
		return
	}
	if IsGitCryptEncrypted(srcPath) {
		r.Skipped = append(r.Skipped, SkipReason{name, "encrypted"})
		return
	}

	dstPath := filepath.Join(dst, name)
	if _, err := os.Stat(dstPath); err == nil {
		r.Skipped = append(r.Skipped, SkipReason{name, "dst_exists"})
		return
	} else if !os.IsNotExist(err) {
		r.Errors = append(r.Errors, CopyError{name, err})
		return
	}

	in, err := os.Open(srcPath)
	if err != nil {
		r.Errors = append(r.Errors, CopyError{name, err})
		return
	}
	defer in.Close()

	tmp, err := os.CreateTemp(dst, ".mtcopy.*")
	if err != nil {
		r.Errors = append(r.Errors, CopyError{name, err})
		return
	}
	tmpPath := tmp.Name()
	if _, err := io.Copy(tmp, in); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		r.Errors = append(r.Errors, CopyError{name, err})
		return
	}
	if err := tmp.Chmod(fi.Mode().Perm()); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		r.Errors = append(r.Errors, CopyError{name, err})
		return
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		r.Errors = append(r.Errors, CopyError{name, err})
		return
	}
	if err := os.Rename(tmpPath, dstPath); err != nil {
		_ = os.Remove(tmpPath)
		r.Errors = append(r.Errors, CopyError{name, err})
		return
	}
	r.Copied = append(r.Copied, name)
}

// Summary returns the user-facing one-or-two-line summary, or "" when
// nothing notable happened (zero copied, zero errors, every skip is
// "missing").
func (r CopyReport) Summary() string {
	var nonMissing []SkipReason
	for _, s := range r.Skipped {
		if s.Reason != "missing" {
			nonMissing = append(nonMissing, s)
		}
	}
	copied := len(r.Copied) > 0
	hasErr := len(r.Errors) > 0
	if !copied && !hasErr && len(nonMissing) == 0 {
		return ""
	}

	var first string
	switch {
	case copied:
		first = "mt: copied " + strings.Join(r.Copied, ", ")
		if len(nonMissing) > 0 {
			first += " (skipped: " + joinSkips(nonMissing) + ")"
		}
	case len(nonMissing) > 0:
		first = "mt: copy skipped: " + joinSkipsParen(nonMissing)
	default:
		first = "mt: copy:"
	}

	if !hasErr {
		return first
	}
	parts := make([]string, 0, len(r.Errors))
	for _, e := range r.Errors {
		parts = append(parts, fmt.Sprintf("%s: %v", e.Name, e.Err))
	}
	return first + "\nmt: copy errors: " + strings.Join(parts, ", ")
}

func joinSkips(skips []SkipReason) string {
	parts := make([]string, 0, len(skips))
	for _, s := range skips {
		parts = append(parts, s.Name+" "+s.Reason)
	}
	return strings.Join(parts, ", ")
}

func joinSkipsParen(skips []SkipReason) string {
	parts := make([]string, 0, len(skips))
	for _, s := range skips {
		parts = append(parts, s.Name+" ("+s.Reason+")")
	}
	return strings.Join(parts, ", ")
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
