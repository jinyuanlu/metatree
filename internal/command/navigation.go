package command

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jinyuanlu/metatree/internal/dashboard"
	"github.com/jinyuanlu/metatree/internal/gitio"
	"github.com/jinyuanlu/metatree/internal/tmuxio"
)

// RunShow attaches to (or creates) the dashboard. Bare `mt` calls this.
func RunShow(env *Env, args []string) error {
	mtPath, _ := selfPath()
	notice, err := dashboard.Ensure(env.Config, mtPath)
	if err != nil {
		return ExitWith(1, "%v", err)
	}
	if notice != "" {
		fmt.Fprintln(env.Stderr, notice)
	}
	return tmuxio.AttachOrSwitch(env.Target())
}

// RunLs lists every worktree git knows about across configured repos, with
// live/dead state from the dashboard. Pipeable.
func RunLs(env *Env, args []string) error {
	wts, err := gitio.DiscoverWorktrees(env.Config.ReposDirs, env.Config.Repos)
	if err != nil {
		return ExitWith(1, "discover worktrees: %v", err)
	}
	for _, wt := range wts {
		repoName := filepath.Base(wt.Repo)
		branch := filepath.Base(wt.Path)
		title := repoName + ":" + branch

		state := "dead"
		backend := "-"

		paneID, err := dashboard.FindPane(env.Target(), title)
		if err == nil && paneID != "" {
			state = "live"
			panes, err := tmuxio.ListPanes(env.Target())
			if err == nil {
				for _, p := range panes {
					if p.ID == paneID {
						backend = classifyBackend(p.CurrentCmd)
						break
					}
				}
			}
		}
		fmt.Fprintf(env.Stdout, "%-40s  %-50s  %-8s  %s\n",
			title, wt.Path, backend, state)
	}
	return nil
}

func classifyBackend(cmd string) string {
	switch {
	case strings.HasPrefix(cmd, "claude"):
		return "claude"
	case strings.HasPrefix(cmd, "ollama"):
		return "ollama"
	case cmd == "":
		return "?"
	default:
		return cmd
	}
}

// buildSwitchRows produces the live → dead → new ordering for RunSwitch's
// fzf picker. Pure so it can be unit-tested without tmux. Dead rows are
// derived from worktrees not bound to any live pane; the main worktree
// of each repo (Path == Repo) is excluded — it's the repo itself, not an
// mt-spawned tree.
func buildSwitchRows(managed []tmuxio.Pane, wts []gitio.Worktree) []switchRow {
	liveTitles := make(map[string]struct{}, len(managed))
	rows := make([]switchRow, 0, len(managed)+len(wts)+1)
	for _, p := range managed {
		liveTitles[p.MtManaged] = struct{}{}
		rows = append(rows, switchRow{title: p.MtManaged, marker: "live", paneID: p.ID})
	}
	for _, wt := range wts {
		if wt.Path == wt.Repo {
			continue
		}
		repoName := filepath.Base(wt.Repo)
		branch := filepath.Base(wt.Path)
		title := repoName + ":" + branch
		if _, live := liveTitles[title]; live {
			continue
		}
		rows = append(rows, switchRow{title: title, marker: "dead", repo: wt.Repo, branch: branch})
	}
	rows = append(rows, switchRow{title: "+ Create new worktree...", marker: "new"})
	return rows
}

// switchRow is one entry in the fzf picker built by RunSwitch.
type switchRow struct {
	title  string
	marker string // "live" | "dead" | "new"
	paneID tmuxio.PaneID
	repo   string // dead only
	branch string // dead only
}

// RunSwitch shows a fzf popup of every worktree mt could land you in:
// first the live mt-managed panes, then dead worktrees (git-known but no
// pane bound), then a "+ Create new" entry. Picking live focuses the
// pane; picking dead respawns the configured agent in the existing
// worktree (via MT_REPO+MT_BRANCH into RunNew); picking new runs the
// interactive cmd_new. -z / --zoom zooms after live selection.
//
// Live is shown first because it's the high-signal case — your active
// agents stay at the top even when there are many stale worktrees.
func RunSwitch(env *Env, args []string) error {
	zoom := false
	for _, a := range args {
		if a == "-z" || a == "--zoom" {
			zoom = true
		}
	}

	if _, err := exec.LookPath("fzf"); err != nil {
		return ExitWith(1, "fzf not found; install: https://github.com/junegunn/fzf")
	}

	mtPath, _ := selfPath()
	if _, err := dashboard.Ensure(env.Config, mtPath); err != nil {
		return ExitWith(1, "%v", err)
	}

	managed, err := dashboard.ListManaged(env.Target())
	if err != nil {
		return ExitWith(1, "list managed panes: %v", err)
	}

	wts, _ := gitio.DiscoverWorktrees(env.Config.ReposDirs, env.Config.Repos)
	rows := buildSwitchRows(managed, wts)

	// fzf carries only "<title>|<marker>|<row-index>" through the pipe;
	// we keep the rich row data in memory and look it up by index after.
	var lines []string
	for i, r := range rows {
		lines = append(lines, fmt.Sprintf("%s|%s|%d", r.title, r.marker, i))
	}

	choice, err := fzfPick(strings.Join(lines, "\n"), "switch> ")
	if err != nil {
		// fzf exits non-zero on user-cancel; that's a clean exit, not an error
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 130 {
			return nil
		}
		return ExitWith(1, "fzf: %v", err)
	}
	if choice == "" {
		return nil
	}

	parts := strings.Split(choice, "|")
	if len(parts) < 3 {
		return ExitWith(1, "could not parse fzf selection: %s", choice)
	}
	idx, err := strconv.Atoi(parts[2])
	if err != nil || idx < 0 || idx >= len(rows) {
		return ExitWith(1, "bad switch row index: %s", parts[2])
	}
	chosen := rows[idx]

	switch chosen.marker {
	case "live":
		if err := tmuxio.SelectPane(chosen.paneID); err != nil {
			return ExitWith(1, "select pane: %v", err)
		}
		if zoom {
			_ = tmuxio.ZoomPane(chosen.paneID)
		}
		return tmuxio.AttachOrSwitch(env.Target())
	case "dead":
		// Respawn: MT_REPO + MT_BRANCH skip both fzf prompts in RunNew,
		// and the existing-worktree branch in ensureWorktree reuses the
		// directory without re-fetching from origin.
		prevRepo, hadRepo := os.LookupEnv("MT_REPO")
		prevBranch, hadBranch := os.LookupEnv("MT_BRANCH")
		defer func() {
			if hadRepo {
				_ = os.Setenv("MT_REPO", prevRepo)
			} else {
				_ = os.Unsetenv("MT_REPO")
			}
			if hadBranch {
				_ = os.Setenv("MT_BRANCH", prevBranch)
			} else {
				_ = os.Unsetenv("MT_BRANCH")
			}
		}()
		if err := os.Setenv("MT_REPO", chosen.repo); err != nil {
			return ExitWith(1, "setenv MT_REPO: %v", err)
		}
		if err := os.Setenv("MT_BRANCH", chosen.branch); err != nil {
			return ExitWith(1, "setenv MT_BRANCH: %v", err)
		}
		return RunNew(env, []string{"--with", env.Config.DefaultBackend})
	case "new":
		return RunNew(env, []string{"--with", env.Config.DefaultBackend})
	default:
		return ExitWith(1, "unrecognized switch entry marker: %s", chosen.marker)
	}
}

// fzfPick pipes input into `fzf --prompt=<prompt>` with a custom display
// format. Returns the selected line (with the |-delimited fields preserved
// for the caller to parse).
//
// fzf's --with-nth limits what's *shown* to the user; the full line is
// still what gets returned. So we transform the input into "display\thidden"
// where display is human-readable and hidden carries pane_id/marker.
func fzfPick(input, prompt string) (string, error) {
	// Transform "<title>|<marker>|<key>" → human-friendly display lines.
	// fzf gives us the original line back when chosen if we use a tab-
	// delimited "key\tdisplay" trick — but simpler: we render to a single
	// formatted line and have caller parse it back.
	var transformed []string
	for _, ln := range strings.Split(input, "\n") {
		if ln == "" {
			continue
		}
		parts := strings.Split(ln, "|")
		if len(parts) < 3 {
			continue
		}
		// Display "<title-padded>  [<marker>]"; the row key (parts[2]) is
		// hidden after the tab so the caller can recover it via Split.
		display := fmt.Sprintf("%-40s  [%-4s]", parts[0], parts[1])
		// preserve the original |-delimited row at the end (separated by tab)
		// so the caller can split() and recover marker+key without ambiguity.
		transformed = append(transformed, display+"\t"+ln)
	}
	cmd := exec.Command("fzf",
		"--prompt="+prompt,
		"--height=40%",
		"--with-nth=1",   // show only the display column
		"--delimiter=\t", // hidden side starts after the tab
	)
	cmd.Stdin = strings.NewReader(strings.Join(transformed, "\n"))
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	chosen := strings.TrimRight(string(out), "\n")
	// chosen is "<display>\t<title>|<marker>|<key>" — return the second half.
	if i := strings.IndexByte(chosen, '\t'); i >= 0 {
		return chosen[i+1:], nil
	}
	return chosen, nil
}

// readBranchName prompts the user for a branch name (stderr prompt) and
// reads from env.Stdin. Used by `mt new` interactive path.
func readBranchName(env *Env) (string, error) {
	fmt.Fprint(env.Stderr, "branch name: ")
	scanner := bufio.NewScanner(env.Stdin)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return "", err
		}
		return "", fmt.Errorf("no input")
	}
	return strings.TrimSpace(scanner.Text()), nil
}
