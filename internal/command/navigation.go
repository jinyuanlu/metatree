package command

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

// RunSwitch shows a fzf popup of live mt-managed panes plus a "+ Create
// new" entry, focuses the chosen pane (and zooms with -z). Dead worktrees
// are deliberately excluded (per spec.md / the bash port's commit e57ceca).
//
// Layout in fzf:
//
//	<title>                  [live]  <pane_id>
//	+ Create new worktree... [new ]  <NEW>
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

	// Build fzf input. Each row is "<display>|<marker>|<key>".
	var lines []string
	for _, p := range managed {
		lines = append(lines, fmt.Sprintf("%s|live|%s", p.MtManaged, p.ID))
	}
	lines = append(lines, "+ Create new worktree...|new|<NEW>")

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
	marker, key := parts[1], parts[2]

	switch marker {
	case "live":
		paneID := tmuxio.PaneID(key)
		if err := tmuxio.SelectPane(paneID); err != nil {
			return ExitWith(1, "select pane: %v", err)
		}
		if zoom {
			_ = tmuxio.ZoomPane(paneID)
		}
		return tmuxio.AttachOrSwitch(env.Target())
	case "new":
		// fall through to interactive cmd_new with default backend
		return RunNew(env, []string{"--with", env.Config.DefaultBackend})
	default:
		return ExitWith(1, "unrecognized switch entry marker: %s", marker)
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
		// "<title-padded>  [<marker>]  <key>" — reading the chosen line and
		// splitting on whitespace recovers marker and key from the right.
		display := fmt.Sprintf("%-40s  [%-4s]  %s", parts[0], parts[1], parts[2])
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
