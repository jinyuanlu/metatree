package command

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/jinyuanlu/metatree/internal/dashboard"
	"github.com/jinyuanlu/metatree/internal/gitio"
	"github.com/jinyuanlu/metatree/internal/tmuxio"
)

var slugifyRe = regexp.MustCompile(`[^a-z0-9-]+`)
var dashRunRe = regexp.MustCompile(`-+`)

// Slugify normalizes a user-typed branch name to mt's allowed shape.
// Returns an error if the result is empty (e.g. all special chars).
//
// Decision: Slugify knows nothing about prefixes (per spec-go.md §18 #3).
// The branch_prefix is applied at call sites.
func Slugify(input string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(input))
	s = slugifyRe.ReplaceAllString(s, "-")
	s = dashRunRe.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		return "", fmt.Errorf("invalid branch name: %q", input)
	}
	return s, nil
}

// RunNew creates a worktree and launches the chosen agent in a pane on
// the dashboard. Idempotent on (repo, branch). See spec-go.md §11 for the
// git-crypt path; mt.sh's cmd_new for the bash reference.
//
// Env overrides MT_REPO and MT_BRANCH let cmd_new run non-interactively
// (used by RunSwitch's "+ Create new" path and by integration tests).
func RunNew(env *Env, args []string) error {
	backend := env.Config.DefaultBackend
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--with":
			if i+1 < len(args) {
				backend = args[i+1]
				i++
			}
		}
	}

	if _, err := exec.LookPath("fzf"); err != nil {
		// fzf only required for the interactive repo picker
		if envGet("MT_REPO") == "" {
			return ExitWith(1, "fzf not found; install: https://github.com/junegunn/fzf")
		}
	}

	repos, err := gitio.DiscoverRepos(env.Config.ReposDirs, env.Config.Repos)
	if err != nil {
		return ExitWith(1, "discover repos: %v", err)
	}
	if len(repos) == 0 {
		if len(env.Config.ReposDirs) == 0 && len(env.Config.Repos) == 0 {
			return ExitWith(1,
				"no repos_dirs configured. run `mt setup --repos-dirs ~/your-folder` "+
					"(or just `mt setup` for an interactive prompt)")
		}
		return ExitWith(1,
			"no git repos found under repos_dirs (%s). run `mt setup` to change",
			strings.Join(env.Config.ReposDirs, ", "))
	}

	repo, err := chooseRepo(repos, env)
	if err != nil {
		return err
	}

	rawBranch, err := chooseBranch(env)
	if err != nil {
		return err
	}
	branch, err := Slugify(rawBranch)
	if err != nil {
		return ExitWith(1, "%v", err)
	}

	fullBranch := env.Config.BranchPrefix + "/" + branch
	worktreePath := filepath.Join(repo, env.Config.WorktreeSubdir, branch)
	title := filepath.Base(repo) + ":" + branch

	// Ensure dashboard exists (also auto-binds + applies chrome on first run)
	mtPath, _ := selfPath()
	if notice, err := dashboard.Ensure(env.Config, mtPath); err != nil {
		return ExitWith(1, "%v", err)
	} else if notice != "" {
		fmt.Fprintln(env.Stderr, notice)
	}

	// Idempotency: if a pane already exists for this title, focus it.
	if existing, _ := dashboard.FindPane(env.Target(), title); existing != "" {
		if err := tmuxio.SelectPane(existing); err != nil {
			return ExitWith(1, "select existing pane: %v", err)
		}
		return tmuxio.AttachOrSwitch(env.Target())
	}

	// Worktree creation. Reuse if path exists AND is git-registered;
	// otherwise add fresh. Git-crypt repos need the --no-checkout dance.
	if err := ensureWorktree(repo, fullBranch, worktreePath); err != nil {
		return err
	}

	// Copy gitignored runtime files (.env, .envrc, …) from parent to
	// worktree before direnv-allow so a freshly-copied .envrc gets
	// auto-approved by the existing block below — zero new code there.
	copyRuntimeFiles(env, repo, worktreePath)

	// Auto-direnv-allow: skip if .envrc is still git-crypt-encrypted.
	if env.Config.AutoDirenvAllow {
		envrc := filepath.Join(worktreePath, ".envrc")
		if _, err := exec.LookPath("direnv"); err == nil {
			if fi := fileExists(envrc); fi && !gitio.IsGitCryptEncrypted(envrc) {
				_ = exec.Command("direnv", "allow", worktreePath).Run()
			}
		}
	}

	// Build the agent command
	var cmd string
	switch backend {
	case "claude":
		cmd = env.Config.ClaudeCmd
	case "ollama":
		cmd = strings.ReplaceAll(env.Config.OllamaCmd, "{model}", env.Config.OllamaModel)
	default:
		return ExitWith(1, "unknown backend: %s (use claude or ollama)", backend)
	}
	if _, err := exec.LookPath(strings.Fields(cmd)[0]); err != nil {
		switch backend {
		case "claude":
			return ExitWith(1, "claude not found; install: https://claude.com/claude-code")
		case "ollama":
			return ExitWith(1, "ollama not found; install: https://ollama.com")
		}
	}

	// Decide split-vs-reuse based on @mt-managed pane count, NOT pane_title.
	// pane_title gets clobbered by Claude's OSC 2; @mt-managed survives.
	managedCount, err := dashboard.ManagedCount(env.Target())
	if err != nil {
		return ExitWith(1, "count managed panes: %v", err)
	}

	if managedCount == 0 {
		// First mt session: reuse the bare-shell pane tmux gave us at
		// session creation. send-keys "cd && exec cmd".
		panes, err := tmuxio.ListPanes(env.Target())
		if err != nil || len(panes) == 0 {
			return ExitWith(1, "list panes: %v", err)
		}
		first := panes[0].ID
		// Note: we deliberately do NOT use `exec <cmd>` here. In bash and
		// zsh, `exec <name>` is a special-builtin form that does not
		// trigger alias expansion on its argument — so `exec claude`
		// would bypass the user's `alias claude='claude --mcp-config ...'`
		// and load the bare binary. Run `<cmd>` plainly so the alias
		// expands, then `; exit` so the pane still closes when the
		// agent exits (matching the previous `exec`-based behavior).
		keys := fmt.Sprintf("cd %s && %s; exit", worktreePath, cmd)
		if err := tmuxio.SendKeys(first, keys); err != nil {
			return ExitWith(1, "send-keys: %v", err)
		}
		if err := dashboard.MarkPane(first, title); err != nil {
			return ExitWith(1, "mark pane: %v", err)
		}
	} else {
		// Subsequent sessions: split-window with the agent as the cmd.
		//
		// tmux split-window dispatches its cmd through /bin/sh -c, which
		// is non-interactive and never sources the user's rcfile. Wrap
		// in `$SHELL -ic 'exec <cmd>'` so aliases for claude_cmd /
		// ollama_cmd expand here the same way they do in pane #1 (which
		// is fed via send-keys into a live interactive shell). See
		// wrapAgentCmd in launch.go for the full rationale.
		launchCmd, recognized := wrapAgentCmd(cmd)
		if !recognized {
			fmt.Fprintf(env.Stderr,
				"mt: $SHELL=%s not recognized; aliases in claude_cmd/ollama_cmd will not expand.\n"+
					"    To inject flags reliably, set the literal command in ~/.config/mt/config.toml — e.g.\n"+
					"        claude_cmd = \"claude --mcp-config $HOME/.claude/mcp.json\"\n",
				shellOrUnset())
		}
		newPane, err := tmuxio.SplitPane(env.Target(), worktreePath, launchCmd)
		if err != nil {
			return ExitWith(1, "split-window: %v", err)
		}
		if err := dashboard.MarkPane(newPane, title); err != nil {
			return ExitWith(1, "mark pane: %v", err)
		}
		_ = tmuxio.TileLayout(env.Target())
	}

	return tmuxio.AttachOrSwitch(env.Target())
}

// ensureWorktree creates the worktree if it doesn't exist; reuses if it does
// AND git knows about it. Handles git-crypt repos via the --no-checkout
// pattern.
func ensureWorktree(repo, fullBranch, worktreePath string) error {
	if fileExists(worktreePath) {
		// Verify git knows about this path (resolve symlinks both sides)
		resolved, err := filepath.EvalSymlinks(worktreePath)
		if err != nil {
			return ExitWith(1, "resolve %s: %v", worktreePath, err)
		}
		wts, err := gitio.DiscoverWorktrees(nil, []string{repo})
		if err != nil {
			return ExitWith(1, "list worktrees: %v", err)
		}
		for _, w := range wts {
			if w.Path == resolved {
				return nil // exists and registered — reuse
			}
		}
		return ExitWith(1, "path exists: %s", worktreePath)
	}

	if gitio.GitCryptInUse(repo) {
		// --no-checkout, copy key, checkout. Per spec-go.md §11.
		if err := gitio.WorktreeAddNoCheckout(repo, fullBranch, worktreePath, ""); err != nil {
			return ExitWith(1, "%v", err)
		}
		if err := gitio.InstallGitCryptKey(repo, worktreePath); err != nil {
			// key copy failed (e.g., source key missing) — log and continue
			// per spec-go.md §11 "git-crypt failure chain"
			fmt.Fprintf(stderrOf(),
				"mt: warning: git-crypt key install failed (%v); files may be encrypted\n",
				err)
		}
		// checkout via git directly (smudge filter applies if key was copied)
		if err := exec.Command("git", "-C", worktreePath, "checkout", "HEAD", "--", ".").Run(); err != nil {
			fmt.Fprintf(stderrOf(),
				"mt: warning: post-key checkout failed (%v); files may be encrypted\n",
				err)
		}
		return nil
	}

	if err := gitio.WorktreeAdd(repo, fullBranch, worktreePath, ""); err != nil {
		return ExitWith(1, "%v", err)
	}
	return nil
}

// chooseRepo: env-var override (MT_REPO) wins; else fzf-pick. Matches by
// EvalSymlinks-resolved equality (handles macOS /tmp ↔ /private/tmp).
func chooseRepo(repos []string, env *Env) (string, error) {
	if mtRepo := envGet("MT_REPO"); mtRepo != "" {
		mtResolved, err := filepath.EvalSymlinks(mtRepo)
		if err != nil {
			return "", ExitWith(1, "MT_REPO not accessible: %s", mtRepo)
		}
		for _, r := range repos {
			rResolved, err := filepath.EvalSymlinks(r)
			if err != nil {
				continue
			}
			if rResolved == mtResolved {
				return r, nil
			}
		}
		return "", ExitWith(1, "MT_REPO not in discovered repos: %s", mtRepo)
	}
	choice, err := fzfPickRaw(strings.Join(repos, "\n"), "repo> ")
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 130 {
			return "", ExitWith(0, "")
		}
		return "", ExitWith(1, "fzf: %v", err)
	}
	return strings.TrimSpace(choice), nil
}

// chooseBranch: env-var override (MT_BRANCH) wins; else interactive prompt.
func chooseBranch(env *Env) (string, error) {
	if mtBranch := envGet("MT_BRANCH"); mtBranch != "" {
		return mtBranch, nil
	}
	return readBranchName(env)
}

// fzfPickRaw passes input through fzf with no transformation. Used for repo
// picking where the line itself is the value.
func fzfPickRaw(input, prompt string) (string, error) {
	cmd := exec.Command("fzf", "--prompt="+prompt, "--height=40%")
	cmd.Stdin = strings.NewReader(input)
	cmd.Stderr = stderrOf()
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(out), "\n"), nil
}

// RunRm picks a worktree (fzf), removes it (worktree, branch if no
// upstream, pane). Refuses on dirty unless --force. See mt.sh cmd_rm.
//
// MT_RM_TITLE env override skips fzf for testing.
func RunRm(env *Env, args []string) error {
	force := false
	for _, a := range args {
		if a == "--force" {
			force = true
		}
	}
	if _, err := exec.LookPath("fzf"); err != nil && envGet("MT_RM_TITLE") == "" {
		return ExitWith(1, "fzf not found")
	}

	wts, err := gitio.DiscoverWorktrees(env.Config.ReposDirs, env.Config.Repos)
	if err != nil {
		return ExitWith(1, "discover worktrees: %v", err)
	}
	if len(wts) == 0 {
		return ExitWith(1, "no worktrees to remove")
	}

	// Build entries: "title  path"
	type entry struct {
		title, path, repo string
	}
	var entries []entry
	for _, wt := range wts {
		repoName := filepath.Base(wt.Repo)
		branch := filepath.Base(wt.Path)
		entries = append(entries, entry{
			title: repoName + ":" + branch,
			path:  wt.Path,
			repo:  wt.Repo,
		})
	}

	chosen := entry{}
	if t := envGet("MT_RM_TITLE"); t != "" {
		for _, e := range entries {
			if e.title == t {
				chosen = e
				break
			}
		}
		if chosen.title == "" {
			return ExitWith(1, "MT_RM_TITLE not in worktree list: %s", t)
		}
	} else {
		var lines []string
		for _, e := range entries {
			lines = append(lines, fmt.Sprintf("%-40s  %s", e.title, e.path))
		}
		pick, err := fzfPickRaw(strings.Join(lines, "\n"), "rm> ")
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 130 {
				return nil
			}
			return ExitWith(1, "fzf: %v", err)
		}
		title := strings.TrimSpace(strings.SplitN(pick, " ", 2)[0])
		for _, e := range entries {
			if e.title == title {
				chosen = e
				break
			}
		}
	}

	if err := gitio.WorktreeRemove(chosen.repo, chosen.path, force); err != nil {
		return ExitWith(1, "worktree has uncommitted changes; use 'mt rm --force' to bypass")
	}

	branch := filepath.Base(chosen.path)
	fullBranch := env.Config.BranchPrefix + "/" + branch
	// Try prefixed first, then bare (handles claude-style worktrees)
	if err := gitio.DeleteBranchIfNoUpstream(chosen.repo, fullBranch); err != nil {
		_ = gitio.DeleteBranchIfNoUpstream(chosen.repo, branch)
	}

	// Kill the pane if alive
	if paneID, _ := dashboard.FindPane(env.Target(), chosen.title); paneID != "" {
		_ = tmuxio.KillPane(paneID)
		_ = tmuxio.TileLayout(env.Target())
	}
	return nil
}

// RunPrune bulk-removes all dead worktrees. Interactive y/N confirm by
// default; --force skips confirm AND bypasses git's dirty-tree refusal.
func RunPrune(env *Env, args []string) error {
	force := false
	for _, a := range args {
		if a == "--force" {
			force = true
		}
	}

	wts, err := gitio.DiscoverWorktrees(env.Config.ReposDirs, env.Config.Repos)
	if err != nil {
		return ExitWith(1, "discover worktrees: %v", err)
	}

	type dead struct{ title, path, repo string }
	var deads []dead
	for _, wt := range wts {
		repoName := filepath.Base(wt.Repo)
		branch := filepath.Base(wt.Path)
		title := repoName + ":" + branch
		paneID, _ := dashboard.FindPane(env.Target(), title)
		if paneID == "" {
			deads = append(deads, dead{title, wt.Path, wt.Repo})
		}
	}

	if len(deads) == 0 {
		return ExitWith(1, "no dead worktrees to prune")
	}

	fmt.Fprintf(env.Stdout, "Dead worktrees (no live pane on %s):\n", env.Target())
	for _, d := range deads {
		fmt.Fprintf(env.Stdout, "  %-40s  %s\n", d.title, d.path)
	}
	fmt.Fprintln(env.Stdout)

	if !force {
		fmt.Fprint(env.Stderr, "Remove all of these (worktree + branch)? [y/N] ")
		var ans string
		_, _ = fmt.Fscanln(env.Stdin, &ans)
		if !strings.EqualFold(ans, "y") {
			return ExitWith(1, "aborted")
		}
	}

	removed, skipped := 0, 0
	for _, d := range deads {
		if err := gitio.WorktreeRemove(d.repo, d.path, force); err != nil {
			fmt.Fprintf(env.Stdout, "  skipped:  %s  (dirty — use 'mt prune --force' to bypass)\n", d.title)
			skipped++
			continue
		}
		branch := filepath.Base(d.path)
		fullBranch := env.Config.BranchPrefix + "/" + branch
		if err := gitio.DeleteBranchIfNoUpstream(d.repo, fullBranch); err != nil {
			_ = gitio.DeleteBranchIfNoUpstream(d.repo, branch)
		}
		fmt.Fprintf(env.Stdout, "  removed:  %s\n", d.title)
		removed++
	}
	fmt.Fprintf(env.Stdout, "\n%d removed, %d skipped\n", removed, skipped)
	return nil
}

// copyRuntimeFiles runs the worktree_copy_files step and prints any
// non-empty user-facing Summary to env.Stderr. Extracted from RunNew so
// the call site is unit-testable without standing up tmux + dashboard.
func copyRuntimeFiles(env *Env, repo, worktreePath string) {
	if len(env.Config.WorktreeCopyFiles) == 0 {
		return
	}
	rpt := gitio.CopyRuntimeFiles(repo, worktreePath, env.Config.WorktreeCopyFiles)
	if s := rpt.Summary(); s != "" {
		fmt.Fprintln(env.Stderr, s)
	}
}

// helpers ---

func envGet(k string) string { return strings.TrimSpace(os.Getenv(k)) }

// stderrOf returns a writer for diagnostic messages emitted from
// non-Run-receiving helpers. Most callers should write to env.Stderr;
// this exists for the few internal helpers that don't carry an Env.
func stderrOf() *os.File { return os.Stderr }

func fileExists(p string) bool {
	if _, err := os.Stat(p); err == nil {
		return true
	} else {
		return !errors.Is(err, fs.ErrNotExist)
	}
}
