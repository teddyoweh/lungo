package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"skybuild/internal/engine"
	"strings"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	"skybuild/internal/events"
	"skybuild/internal/folders"
	"skybuild/internal/model"
	"skybuild/internal/paths"
	"skybuild/internal/syncer"
)

func syncCommands() []*cobra.Command {
	return append([]*cobra.Command{syncCmd(), keysCmd(), pushCmd(), pullCmd(), cloneCmd(), linkCmd()}, projectCommands()...)
}

// ---------- sync ----------

func syncCmd() *cobra.Command {
	var opts syncer.Options
	var only string
	var background bool
	c := &cobra.Command{
		Use:   "sync [machines…]",
		Short: "Push your Claude Code setup, logins, keys and CLI credentials to machines",
		Long: `Push this computer's developer setup to your machines. This computer is the source of truth.

What syncs (toggle per machine with ` + "`sky sync items <machine>`" + `):
` + itemList() + `
Runs on every machine when no names are given.`,
		Example: `  sky sync                 # everything, everywhere
  sky sync box -n          # dry run: show what would change
  sky sync --only github,claude-login`,
		ValidArgsFunction: completeMachines,
		RunE: func(cmd *cobra.Command, args []string) error {
			if only != "" {
				opts.Only = strings.Split(only, ",")
			}
			if background {
				return backgroundSync(cmd, args)
			}
			opts.Interactive = isTTY()
			var results []syncer.Result
			err := progress(func(r events.Reporter) error {
				events.Stepf(r, "Syncing")
				results = eng.Sync(cmd.Context(), args, opts, r)
				return nil
			})
			if err != nil {
				return err
			}
			if jsonOut {
				return printJSON(results)
			}
			failed := 0
			for _, res := range results {
				switch {
				case res.Skipped != "":
					fmt.Printf("  %s %s %s\n", sDim.Render("○"), res.Machine, sDim.Render("skipped: "+res.Skipped))
				case len(res.Errors) > 0:
					failed++
					fmt.Printf("  %s %s %s\n", sAmber.Render("●"), sBold.Render(res.Machine), sAmber.Render(fmt.Sprintf("%d change(s), %d problem(s)", len(res.Changes), len(res.Errors))))
				case len(res.Changes) == 0:
					fmt.Printf("  %s %s %s\n", sGreen.Render("●"), sBold.Render(res.Machine), sDim.Render("already in sync"))
				default:
					fmt.Printf("  %s %s %s\n", sGreen.Render("●"), sBold.Render(res.Machine), fmt.Sprintf("%d change(s)", len(res.Changes)))
				}
			}
			if len(results) == 0 {
				fmt.Println(hint("No machines to sync."))
			}
			if failed > 0 {
				return fmt.Errorf("sync had problems on %d machine(s)", failed)
			}
			return nil
		},
	}
	c.Flags().BoolVarP(&opts.DryRun, "dry-run", "n", false, "show what would change")
	c.Flags().StringVar(&only, "only", "", "comma-separated items: "+strings.Join(syncer.DefaultItems(), ","))
	c.Flags().BoolVar(&opts.Relogin, "relogin", false, "mint a new Claude token first (browser approval)")
	c.Flags().BoolVar(&background, "background", false, "quiet run for the background agent")
	_ = c.Flags().MarkHidden("background")
	c.AddCommand(syncItemsCmd(), &cobra.Command{
		Use:               "takeover <machine>",
		Short:             "Make sky the one tool that syncs a machine (turns off claude-sync-mini for it)",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeMachines,
		RunE: func(cmd *cobra.Command, args []string) error {
			return progress(func(r events.Reporter) error { return eng.TakeOver(cmd.Context(), args[0], r) })
		},
	})
	return c
}

func itemList() string {
	var b strings.Builder
	for _, it := range syncer.Items() {
		fmt.Fprintf(&b, "  %-13s %s\n", it.ID, it.Description)
	}
	return b.String()
}

func backgroundSync(cmd *cobra.Command, args []string) error {
	_ = paths.Ensure()
	logPath := filepath.Join(paths.Logs(), "sync.log")
	if st, err := os.Stat(logPath); err == nil && st.Size() > 2_000_000 {
		b, _ := os.ReadFile(logPath)
		_ = os.WriteFile(logPath, b[len(b)-400_000:], 0o600)
	}
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	r := events.Func(func(e events.Event) {
		if e.Level == events.Info || e.Level == events.Warn || e.Level == events.Error {
			fmt.Fprintf(f, "%s %s %s\n", e.Time.Format("2006-01-02 15:04:05"), e.Machine, e.Message)
		}
	})
	_, _ = eng.Refresh(cmd.Context())
	if res, err := eng.ClaudeTick(cmd.Context(), nil, r); err == nil && res.Switched {
		fmt.Fprintf(f, "%s claude switched %s → %s (%s), resumed %d\n", time.Now().Format("2006-01-02 15:04:05"), res.From, res.To, res.Reason, res.Restarted)
	}
	eng.Sync(cmd.Context(), args, syncer.Options{}, r)
	// Sessions still on a login the machines have moved off (the app does this too, faster,
	// while it is open; each session is moved once, by whichever gets there first).
	if sessions, _ := eng.AllSessions(cmd.Context()); len(sessions) > 0 {
		if moved := eng.MoveToNewLogin(cmd.Context(), sessions); len(moved) > 0 {
			fmt.Fprintf(f, "%s claude: moved %s to the machines' current login\n", time.Now().Format("2006-01-02 15:04:05"), engine.LoginSummary(moved))
		}
	}
	return nil
}

func syncItemsCmd() *cobra.Command {
	var set string
	c := &cobra.Command{
		Use:               "items <machine>",
		Short:             "Choose what syncs to a machine",
		Example:           "  sky sync items box                       # pick interactively\n  sky sync items box --set claude,github   # exactly these\n  sky sync items box --set none            # nothing",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeMachines,
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := eng.Machine(args[0])
			if err != nil {
				return err
			}
			chosen := m.Sync
			switch {
			case set == "none":
				chosen = []string{}
			case set == "all":
				chosen = syncer.DefaultItems()
			case set != "":
				chosen = strings.Split(set, ",")
			case isTTY():
				var opts []huh.Option[string]
				for _, it := range syncer.Items() {
					o := huh.NewOption(fmt.Sprintf("%-13s %s", it.Label, sDim.Render(it.Description)), it.ID)
					for _, s := range m.Sync {
						if s == it.ID {
							o = o.Selected(true)
						}
					}
					opts = append(opts, o)
				}
				if err := huh.NewForm(huh.NewGroup(huh.NewMultiSelect[string]().Title("What syncs to " + m.Name).Options(opts...).Value(&chosen))).WithTheme(formTheme()).Run(); err != nil {
					return err
				}
			default:
				fmt.Println(strings.Join(m.Sync, ","))
				return nil
			}
			if err := eng.SetSyncItems(m.Name, chosen); err != nil {
				return err
			}
			if len(chosen) == 0 {
				fmt.Println(sGreen.Render("✓ ") + "nothing syncs to " + m.Name)
			} else {
				fmt.Println(sGreen.Render("✓ ") + m.Name + " syncs: " + strings.Join(chosen, ", "))
			}
			return nil
		},
	}
	c.Flags().StringVar(&set, "set", "", "comma-separated items, or all / none")
	return c
}

// ---------- push / pull / clone ----------

func splitRemote(s string) (string, string, bool) {
	name, path, ok := strings.Cut(s, ":")
	if !ok || name == "" || strings.ContainsAny(name, `/\`) || (len(name) == 1 && os.PathSeparator == '\\') {
		return "", "", false
	}
	return name, path, true
}

func folderFlags(c *cobra.Command, o *folders.Options) {
	c.Flags().BoolVar(&o.Delete, "delete", false, "delete files on the other side that aren't in the source")
	c.Flags().StringSliceVarP(&o.Excludes, "exclude", "x", nil, "skip matching names (repeatable)")
	c.Flags().BoolVar(&o.Lean, "lean", false, "skip node_modules, .venv, build output and caches")
	c.Flags().BoolVarP(&o.DryRun, "dry-run", "n", false, "show what would be copied")
}

func pushCmd() *cobra.Command {
	var o folders.Options
	c := &cobra.Command{
		Use:   "push <local> <machine>:<path>",
		Short: "Copy a local folder or file to a machine",
		Example: `  sky push . box:~/code/app --lean
  sky push ~/.config/nvim box:~/.config/nvim`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, remote, ok := splitRemote(args[1])
			if !ok {
				return errors.New("destination must look like machine:path")
			}
			if remote == "" {
				remote = "~/code/" + filepath.Base(folders.Expand(args[0]))
			}
			return progress(func(r events.Reporter) error { return eng.Push(cmd.Context(), name, args[0], remote, o, r) })
		},
	}
	folderFlags(c, &o)
	return c
}

func pullCmd() *cobra.Command {
	var o folders.Options
	c := &cobra.Command{
		Use:     "pull <machine>:<path> [local]",
		Short:   "Copy a folder or file from a machine",
		Example: `  sky pull box:~/code/app/dist ./dist`,
		Args:    cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, remote, ok := splitRemote(args[0])
			if !ok {
				return errors.New("source must look like machine:path")
			}
			local := "."
			if len(args) == 2 {
				local = args[1]
			} else {
				local = filepath.Base(strings.TrimRight(remote, "/"))
			}
			return progress(func(r events.Reporter) error { return eng.Pull(cmd.Context(), name, remote, local, o, r) })
		},
	}
	folderFlags(c, &o)
	return c
}

func cloneCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "clone <machine> <repo> [dir]",
		Short:   "Clone a GitHub repo onto a machine (uses your synced gh login)",
		Example: `  sky clone box spawnlabs/api          # → ~/code/api`,
		Args:    cobra.RangeArgs(2, 3),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := ""
			if len(args) == 3 {
				dir = args[2]
			}
			var path string
			err := progress(func(r events.Reporter) error {
				var err error
				path, err = eng.Clone(cmd.Context(), args[0], args[1], dir, r)
				return err
			})
			if err == nil && !jsonOut {
				fmt.Println(hint("  sky claude " + args[0] + " " + path))
			}
			return err
		},
	}
}

// ---------- links ----------

func linkCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "link",
		Short: "Saved folder pairs you can sync with one command or keep live",
	}
	var l model.Link
	var pull bool
	add := &cobra.Command{
		Use:     "add <local> <machine>:<path>",
		Short:   "Save a folder pair (push by default)",
		Example: `  sky link add ~/code/site box:~/code/site --watch`,
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, remote, ok := splitRemote(args[1])
			if !ok {
				return errors.New("destination must look like machine:path")
			}
			l.Local, l.Machine, l.Remote = args[0], name, remote
			if pull {
				l.Direction = "pull"
			}
			got, err := eng.AddLink(l)
			if err != nil {
				return err
			}
			fmt.Printf("%s link %s: %s %s %s:%s\n", sGreen.Render("✓"), sBold.Render(got.ID), paths.Tilde(got.Local), arrow(got.Direction), got.Machine, got.Remote)
			return nil
		},
	}
	add.Flags().BoolVar(&pull, "pull", false, "copy from the machine to here instead")
	add.Flags().BoolVar(&l.Delete, "delete", false, "mirror deletions")
	add.Flags().BoolVar(&l.Watch, "watch", false, "include in `sky link watch`")
	add.Flags().StringSliceVarP(&l.Excludes, "exclude", "x", nil, "skip matching names")

	ls := &cobra.Command{
		Use:   "ls",
		Short: "List folder pairs",
		RunE: func(cmd *cobra.Command, _ []string) error {
			links, err := eng.Links()
			if err != nil {
				return err
			}
			if jsonOut {
				return printJSON(links)
			}
			if len(links) == 0 {
				fmt.Println(hint("No links yet: sky link add <folder> <machine>:<path>"))
				return nil
			}
			var rows [][]string
			for _, l := range links {
				last := "never"
				if !l.LastSync.IsZero() {
					last = ago(l.LastSync)
				}
				w := ""
				if l.Watch {
					w = sAccent.Render(" watch")
				}
				rows = append(rows, []string{sBold.Render(l.ID), paths.Tilde(l.Local), arrow(l.Direction), l.Machine + ":" + l.Remote + w, sDim.Render(last)})
			}
			table(os.Stdout, []string{"id", "local", "", "machine", "synced"}, rows)
			return nil
		},
	}
	rm := &cobra.Command{
		Use:   "rm <id>",
		Short: "Forget a folder pair (no files are deleted)",
		Args:  cobra.ExactArgs(1),
		RunE:  func(cmd *cobra.Command, args []string) error { return eng.RemoveLink(args[0]) },
	}
	run := &cobra.Command{
		Use:   "sync [id…]",
		Short: "Copy every folder pair (or the given ones) now",
		RunE: func(cmd *cobra.Command, args []string) error {
			links, err := eng.Links()
			if err != nil {
				return err
			}
			want := map[string]bool{}
			for _, a := range args {
				want[a] = true
			}
			return progress(func(r events.Reporter) error {
				for _, l := range links {
					if len(want) == 0 || want[l.ID] {
						if err := eng.RunLink(cmd.Context(), l.ID, r); err != nil {
							return err
						}
					}
				}
				return nil
			})
		},
	}
	watch := &cobra.Command{
		Use:   "watch [id…]",
		Short: "Keep pushing folder pairs as files change (Ctrl-C to stop)",
		RunE: func(cmd *cobra.Command, args []string) error {
			r := newRenderer(verbose)
			defer r.end(false)
			fmt.Println(hint("Watching for changes. Ctrl-C to stop."))
			start := time.Now()
			err := eng.WatchLinks(cmd.Context(), args, r)
			if err == nil {
				fmt.Println(hint(fmt.Sprintf("Stopped after %s", time.Since(start).Round(time.Second))))
			}
			return err
		},
	}
	c.AddCommand(add, ls, rm, run, watch)
	return c
}

func arrow(dir string) string {
	if dir == "pull" {
		return sAccent.Render("←")
	}
	return sAccent.Render("→")
}
