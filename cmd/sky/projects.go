package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"skybuild/internal/engine"
	"skybuild/internal/events"
	"skybuild/internal/paths"
)

func projectCommands() []*cobra.Command {
	return []*cobra.Command{sendCmd(), bringCmd(), projectsCmd()}
}

// openOn starts a session in dir on a machine and attaches to it: Claude by default, a plain
// shell with shell=true.
func openOn(cmd *cobra.Command, machine, dir string, shell bool) error {
	s, err := eng.NewSession(cmd.Context(), machine, engine.SessionOptions{Dir: dir, Claude: !shell})
	if err != nil {
		return err
	}
	argv, err := eng.AttachArgs(machine, s.Name)
	if err != nil {
		return err
	}
	return execSSH(argv)
}

func sendCmd() *cobra.Command {
	var o engine.HandoffOptions
	var noOpen, shell bool
	c := &cobra.Command{
		Use:   "send [dir] <machine>",
		Short: "Continue a repo on a machine: same branch, commits and uncommitted work",
		Long: `Put the git repo in dir (default: here) on a machine exactly as it is on this computer:
the branch, every commit (pushed or not) and your uncommitted work, plus .env files and anything
listed in .skyinclude. Nothing changes in the local repo.

On the machine the repo is cloned from its origin if it isn't there yet (so only your new work
travels from here). If the machine's copy has uncommitted work it is stashed first, and commits
only it had are kept on a sky-backup/ branch, so nothing is lost.

Afterwards a Claude session opens in that folder on the machine.`,
		Example: `  sky send box                     # this repo → box, then Claude there
  sky send ~/code/api box --shell   # open a shell instead
  sky send . box --to ~/work/api --no-open`,
		Args: cobra.RangeArgs(1, 2),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			names, d := completeMachines(cmd, nil, toComplete)
			return names, d
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, machine := ".", args[0]
			if len(args) == 2 {
				dir, machine = args[0], args[1]
			}
			var res engine.HandoffResult
			err := progress(func(r events.Reporter) error {
				var err error
				res, err = eng.ProjectSend(cmd.Context(), machine, dir, o, r)
				return err
			})
			if err != nil {
				return err
			}
			if jsonOut {
				return printJSON(res)
			}
			if noOpen || !isTTY() {
				fmt.Println(hint("  open it: sky claude " + machine + " " + res.RemoteDir))
				return nil
			}
			return openOn(cmd, machine, res.RemoteDir, shell)
		},
	}
	c.Flags().StringVar(&o.To, "to", "", "folder on the machine (default: where it already is, or ~/code/<name>)")
	c.Flags().BoolVar(&noOpen, "no-open", false, "don't open a session there afterwards")
	c.Flags().BoolVar(&shell, "shell", false, "open a shell there instead of Claude")
	return c
}

func bringCmd() *cobra.Command {
	var o engine.HandoffOptions
	c := &cobra.Command{
		Use:   "bring <machine> [name|path]",
		Short: "Bring a repo back from a machine: same branch, commits and uncommitted work",
		Long: `The reverse of sky send: the repo on the machine arrives on this computer exactly as it is
there. Uncommitted work in the local copy is stashed first; commits only the local copy had are
kept on a sky-backup/ branch.

With no name, the repo you are in is brought back from the machine.`,
		Example: `  sky bring box                # the repo I'm in
  sky bring box api            # the project called api
  sky bring box ~/work/api --to ~/code/api`,
		Args:              cobra.RangeArgs(1, 2),
		ValidArgsFunction: completeMachines,
		RunE: func(cmd *cobra.Command, args []string) error {
			machine := args[0]
			remote := ""
			if len(args) == 2 {
				remote = args[1]
			}
			if remote == "" || !strings.ContainsAny(remote[:1], "~/") {
				wd, _ := os.Getwd()
				found, local, err := eng.ProjectLocate(cmd.Context(), machine, remote, wd)
				if err != nil {
					return err
				}
				remote = found
				if o.To == "" {
					o.To = local
				}
			}
			var res engine.HandoffResult
			err := progress(func(r events.Reporter) error {
				var err error
				res, err = eng.ProjectBring(cmd.Context(), machine, remote, o, r)
				return err
			})
			if err != nil {
				return err
			}
			if jsonOut {
				return printJSON(res)
			}
			fmt.Println(hint("  cd " + paths.Tilde(res.LocalDir)))
			return nil
		},
	}
	c.Flags().StringVar(&o.To, "to", "", "folder on this computer (default: where it already is, or your code folder)")
	return c
}

func sideText(s *engine.ProjectSide) string {
	if s == nil {
		return sDim.Render("–")
	}
	branch := s.Branch
	if branch == "" {
		branch = "detached " + s.Head
	}
	parts := []string{branch}
	if n := s.Changed + s.Untracked; n > 0 {
		parts = append(parts, sAmber.Render(fmt.Sprintf("%d changed", n)))
	}
	if s.Ahead > 0 {
		parts = append(parts, fmt.Sprintf("%d unpushed", s.Ahead))
	}
	if s.Behind > 0 {
		parts = append(parts, fmt.Sprintf("%d behind", s.Behind))
	}
	return strings.Join(parts, sDim.Render(" · "))
}

func relationText(cp engine.ProjectCopy) string {
	switch cp.State {
	case "synced":
		return sGreen.Render("● ") + cp.Text
	case "local-ahead", "only-local":
		return sAccent.Render("→ ") + cp.Text
	case "remote-ahead", "only-remote":
		return sAccent.Render("← ") + cp.Text
	case "diverged":
		return sAmber.Render("≠ ") + cp.Text
	}
	return sDim.Render(cp.Text)
}

func projectsCmd() *cobra.Command {
	var all bool
	c := &cobra.Command{
		Use:   "projects",
		Short: "Your repos, here and on machines, and which side is ahead",
		RunE: func(cmd *cobra.Command, _ []string) error {
			list, err := eng.Projects(cmd.Context(), true)
			if err != nil {
				return err
			}
			if jsonOut {
				return printJSON(list)
			}
			var rows [][]string
			hidden := 0
			label := func(p engine.ProjectView) string {
				if p.Folder != "" {
					return sBold.Render(p.Name) + sDim.Render(" ("+p.Folder+")")
				}
				return sBold.Render(p.Name)
			}
			for _, p := range list.Projects {
				if len(p.Copies) == 0 {
					if !all && !p.Saved && len(rows) >= 12 {
						hidden++
						continue
					}
					rows = append(rows, []string{label(p), sideText(p.Local), sDim.Render("–"), sDim.Render("only on this Mac")})
					continue
				}
				for i, cp := range p.Copies {
					name, local := label(p), sideText(p.Local)
					if i > 0 {
						name, local = "", ""
					}
					there := sDim.Render("–")
					if cp.Side != nil {
						there = cp.Machine + sDim.Render(":") + " " + sideText(cp.Side)
					}
					rows = append(rows, []string{name, local, there, relationText(cp)})
				}
			}
			if len(rows) == 0 {
				fmt.Println(hint("No git repos found in " + strings.Join(list.Roots, ", ") + ". Send one from its folder: sky send <machine>"))
				return nil
			}
			table(os.Stdout, []string{"project", "this mac", "on machines", ""}, rows)
			if hidden > 0 {
				fmt.Println("\n  " + hint(fmt.Sprintf("%d more repos only on this Mac (sky projects --all)", hidden)))
			}
			for m, e := range list.Errors {
				fmt.Println(sAmber.Render("  ! ") + m + ": " + e)
			}
			return nil
		},
	}
	c.Flags().BoolVar(&all, "all", false, "list every local repo")
	return c
}
