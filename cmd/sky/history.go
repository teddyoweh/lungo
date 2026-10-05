package main

import (
	"errors"
	"fmt"
	"os"
	"path"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"skybuild/internal/engine"
)

// Claude's past conversations and search across everything: `sky history`, `sky resume`,
// `sky search`.

func historyCommands() []*cobra.Command {
	return []*cobra.Command{historyCmd(), resumeCmd(), searchCmd()}
}

// machineArg reads a machine name off the command line. This computer is "local" (or
// "here"), unless a machine really has that name.
func machineArg(name string) string {
	switch name {
	case engine.LocalMachine:
		return name
	case "local", "here":
		if _, err := eng.Machine(name); err != nil {
			return engine.LocalMachine
		}
	}
	return name
}

func machineName(machine string) string {
	if engine.IsLocal(machine) {
		return "local"
	}
	return machine
}

// completeMachinesAndLocal completes a machine name, or "local" for this computer.
func completeMachinesAndLocal(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	names, d := completeMachines(cmd, args, toComplete)
	if len(args) == 0 {
		names = append(names, "local")
	}
	return names, d
}

// tildeDir shortens a home folder to ~ wherever the path is from (a Mac or a Linux machine).
func tildeDir(p string) string {
	for _, home := range []string{"/Users/", "/home/"} {
		if strings.HasPrefix(p, home) {
			if i := strings.Index(p[len(home):], "/"); i >= 0 {
				return "~" + p[len(home)+i:]
			}
			return "~"
		}
	}
	return p
}

func cut(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func historyCmd() *cobra.Command {
	var all bool
	var limit int
	c := &cobra.Command{
		Use:   "history [machine]",
		Short: "List past Claude conversations, here and on your machines, newest first",
		Example: `  sky history               # everywhere
  sky history box           # on one machine
  sky history local -n 100  # on this computer
  sky resume box 3f2a9c1e   # pick one up again (the start of its ID is enough)`,
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeMachinesAndLocal,
		RunE: func(cmd *cobra.Command, args []string) error {
			var list []engine.Conversation
			errs := map[string]string{}
			if len(args) == 1 {
				l, err := eng.History(cmd.Context(), machineArg(args[0]), engine.HistoryOptions{})
				if err != nil {
					return err
				}
				list = l
			} else {
				list, errs = eng.AllHistory(cmd.Context(), engine.HistoryOptions{})
			}
			if !all {
				typed := list[:0:0]
				for _, c := range list {
					if !c.Auto {
						typed = append(typed, c)
					}
				}
				list = typed
			}
			if jsonOut {
				if list == nil {
					list = []engine.Conversation{}
				}
				return printJSON(list)
			}
			total := len(list)
			if limit > 0 && len(list) > limit {
				list = list[:limit]
			}
			if total == 0 {
				fmt.Println(hint("No conversations yet. Start one with `sky claude <machine> [dir]`."))
			} else {
				var rows [][]string
				for _, c := range list {
					title := c.Title
					if title == "" {
						title = sDim.Render("(no title)")
					}
					state := ""
					switch {
					case c.Live != "":
						state = sGreen.Render(" ● " + c.Live)
					case c.Running:
						state = sDim.Render(" ● open elsewhere")
					}
					msgs := ""
					if c.Messages > 0 {
						msgs = fmt.Sprintf("%d", c.Messages)
					}
					rows = append(rows, []string{sDim.Render(ago(c.Updated)), machineName(c.Machine), cut(title, 56) + state, cut(tildeDir(c.Dir), 34), sDim.Render(cut(c.Branch, 20)), sDim.Render(msgs), sDim.Render(shortID(c.ID))})
				}
				table(os.Stdout, []string{"active", "machine", "conversation", "dir", "branch", "msgs", "id"}, rows)
				more := ""
				if total > len(list) {
					more = fmt.Sprintf("%d more (-n 0 shows all) · ", total-len(list))
				}
				fmt.Println("\n  " + hint(more+"sky resume <machine> <id> picks one up again · ● = open in a session now"))
			}
			for m, e := range errs {
				fmt.Println(sAmber.Render("  ! ") + machineName(m) + ": " + e)
			}
			return nil
		},
	}
	c.Flags().BoolVar(&all, "all", false, "also list conversations started by scripts (the SDK, claude -p)")
	c.Flags().IntVarP(&limit, "number", "n", 40, "how many to show (0: all)")
	return c
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func resumeCmd() *cobra.Command {
	var detach bool
	c := &cobra.Command{
		Use:   "resume <machine> <conversation>",
		Short: "Pick a past Claude conversation up again, in a new session in its folder",
		Long:  "Starts a tmux session in the folder the conversation ran in and runs `claude --resume` there. The conversation is named by its ID or the start of it, as `sky history` shows it. A conversation that is open in a session right now is attached to instead.",
		Example: `  sky resume box 3f2a9c1e
  sky resume local 3f2a9c1e -d   # start it and come back later`,
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: completeMachinesAndLocal,
		RunE: func(cmd *cobra.Command, args []string) error {
			machine := machineArg(args[0])
			list, err := eng.History(cmd.Context(), machine, engine.HistoryOptions{ID: args[1], Limit: 20, Auto: 20})
			if err != nil {
				return err
			}
			switch len(list) {
			case 0:
				return fmt.Errorf("no conversation on %s has an ID starting with %q (see `sky history %s`)", machineName(machine), args[1], machineName(machine))
			case 1:
			default:
				var ids []string
				for _, c := range list {
					ids = append(ids, shortID(c.ID)+"… "+cut(c.Title, 40))
				}
				return fmt.Errorf("%d conversations start with %q, give more of the ID:\n  %s", len(list), args[1], strings.Join(ids, "\n  "))
			}
			conv := list[0]
			attach := func(session string, o engine.AttachOptions) error {
				var argv []string
				if engine.IsLocal(machine) {
					argv, err = eng.LocalAttachArgs(session, o)
				} else {
					argv, err = eng.AttachArgsIn(machine, session, o)
				}
				if err != nil {
					return err
				}
				return execSSH(argv)
			}
			// `sky attach` knows machines; a session on this computer is attached to by resuming again.
			attachHint := func(session string) string {
				if engine.IsLocal(machine) {
					return "  attach: sky resume local " + shortID(conv.ID)
				}
				return "  attach: sky attach " + machine + " " + session
			}
			if conv.Live != "" {
				if detach || !isTTY() {
					fmt.Printf("%s Already open in %s on %s\n", sGreen.Render("●"), sBold.Render(conv.Live), machineName(machine))
					fmt.Println(hint(attachHint(conv.Live)))
					return nil
				}
				return attach(conv.Live, engine.AttachOptions{})
			}
			// A session named after the folder, like the ones `sky claude` and the app make.
			var sessions []engine.Session
			if engine.IsLocal(machine) {
				sessions, _ = eng.LocalSessions(cmd.Context())
			} else {
				sessions, _ = eng.Sessions(cmd.Context(), machine)
			}
			taken := map[string]bool{}
			for _, s := range sessions {
				taken[s.Name] = true
			}
			base := path.Base(conv.Dir)
			if base == "" || base == "/" || base == "." {
				base = "home"
			}
			name := engine.SessionName("claude-" + base)
			for i, n := 2, name; taken[name]; i++ {
				name = fmt.Sprintf("%s-%d", n, i)
			}
			o := engine.AttachOptions{Dir: conv.Dir, Resume: conv.ID}
			if detach || !isTTY() {
				if err := eng.StartSession(cmd.Context(), machine, name, o); err != nil {
					return err
				}
				fmt.Printf("%s Resumed in %s on %s: %s\n", sGreen.Render("●"), sBold.Render(name), machineName(machine), cut(conv.Title, 60))
				fmt.Println(hint(attachHint(name)))
				return nil
			}
			return attach(name, o)
		},
	}
	c.Flags().BoolVarP(&detach, "detach", "d", false, "start it without attaching")
	return c
}

func searchCmd() *cobra.Command {
	var machines []string
	var all bool
	var timeout time.Duration
	c := &cobra.Command{
		Use:   "search <words…>",
		Short: "Search every open session and every past Claude conversation, on all machines",
		Long:  "Looks for the words (as typed, in either case) on the screen and in the scrollback of every tmux session, and in what you and Claude said in every past conversation, on every machine that is up and on this computer.",
		Example: `  sky search "connection refused"
  sky search migrate users table --machine box
  sky search TODO --machine local --json`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			query := strings.TrimSpace(strings.Join(args, " "))
			if len([]rune(query)) < engine.SearchMin {
				return errors.New("give at least two characters to search for")
			}
			o := engine.SearchOptions{Timeout: timeout}
			for _, m := range machines {
				o.Machines = append(o.Machines, machineArg(m))
			}
			if !all {
				o.Auto = -1
			}
			res := eng.Search(cmd.Context(), query, o)
			if jsonOut {
				return printJSON(res)
			}
			if len(res.Hits) == 0 {
				fmt.Println(hint(fmt.Sprintf("Nothing found for %q.", query)))
			}
			group, last := "", ""
			for _, h := range res.Hits {
				g := "Past conversations"
				switch {
				case h.Kind == "live":
					g = "Open sessions"
				case h.Auto:
					g = "Runs started by scripts"
				}
				if g != group {
					if group != "" {
						fmt.Println()
					}
					fmt.Println(sBold.Render(g))
					group, last = g, ""
				}
				// One heading per session or conversation, its hits under it.
				key := h.Machine + "\n" + h.Session + "\n" + h.ID
				if key != last {
					last = key
					title := h.Title
					if title == "" {
						title = h.Session
					}
					if title == "" {
						title = "(no title)"
					}
					where := []string{machineName(h.Machine)}
					if h.Dir != "" {
						where = append(where, tildeDir(h.Dir))
					}
					if h.Kind == "live" {
						if engine.IsLocal(h.Machine) {
							where = append(where, "session "+h.Session)
						} else {
							where = append(where, "sky attach "+h.Machine+" "+h.Session)
						}
						if h.Matches > 3 {
							where = append(where, fmt.Sprintf("%d lines match", h.Matches))
						}
					} else {
						when := h.Updated
						if when.IsZero() {
							when = h.At
						}
						where = append(where, ago(when), "sky resume "+machineName(h.Machine)+" "+shortID(h.ID))
						if h.Live != "" {
							where = append(where, sGreen.Render("● open in "+h.Live))
						}
					}
					fmt.Println("  " + sAccent.Render(cut(title, 70)) + sDim.Render("  "+strings.Join(where, " · ")))
				}
				if h.Kind == "live" && h.Before != "" {
					fmt.Println("      " + sDim.Render(cut(h.Before, 110)))
				}
				who := ""
				switch h.Role {
				case "user":
					who = sDim.Render("you: ")
				case "assistant":
					who = sDim.Render("claude: ")
				case "title":
					who = sDim.Render("title: ")
				}
				fmt.Println("    " + who + highlight(h.Snippet, query))
				if h.Kind == "live" && h.After != "" {
					fmt.Println("      " + sDim.Render(cut(h.After, 110)))
				}
			}
			for m, e := range res.Errors {
				fmt.Println(sAmber.Render("  ! ") + machineName(m) + ": " + e)
			}
			return nil
		},
	}
	c.Flags().StringSliceVarP(&machines, "machine", "m", nil, "only look on this machine (repeatable; \"local\" is this computer)")
	c.Flags().BoolVar(&all, "all", false, "also look in conversations started by scripts (the SDK, claude -p)")
	c.Flags().DurationVar(&timeout, "timeout", 0, "how long each machine gets (default 12s)")
	_ = c.RegisterFlagCompletionFunc("machine", func(cmd *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		return completeMachinesAndLocal(cmd, nil, toComplete)
	})
	return c
}

// highlight marks every place the query shows in a snippet (ASCII letters in either case, as
// the search matched them).
func highlight(s, query string) string {
	if query == "" {
		return s
	}
	low, q := asciiLower(s), asciiLower(query)
	var b strings.Builder
	for {
		i := strings.Index(low, q)
		if i < 0 {
			break
		}
		b.WriteString(s[:i])
		b.WriteString(sAmber.Bold(true).Render(s[i : i+len(q)]))
		s, low = s[i+len(q):], low[i+len(q):]
	}
	b.WriteString(s)
	return b.String()
}

func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}
