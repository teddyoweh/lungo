package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"skybuild/internal/engine"
	"skybuild/internal/osx"
	"skybuild/internal/sshx"
)

func workCommands() []*cobra.Command {
	return []*cobra.Command{sshCmd(), claudeCmd(), codingAgentCmd("codex"), codingAgentCmd("grok"), codingAgentCmd("mantis"), sessionsCmd(), attachCmd(), openCmd(), portsCmd(), connectCmd(), runCmd()}
}

// execSSH hands the terminal to ssh, keeping sky's ssh config current first.
func execSSH(args []string) error {
	_ = eng.SyncSSHConfig()
	c := exec.Command(osx.Which(args[0]), args[1:]...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	err := c.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		os.Exit(ee.ExitCode())
	}
	return err
}

func sshCmd() *cobra.Command {
	var noTmux bool
	c := &cobra.Command{
		Use:   "ssh <name> [command…]",
		Short: "Open a shell on a machine (same as `ssh <name>`), or run a command there",
		Example: `  sky ssh box
  sky ssh box -- df -h /home
  sky ssh box --no-tmux`,
		Args:              cobra.MinimumNArgs(1),
		ValidArgsFunction: completeMachines,
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := eng.Machine(args[0])
			if err != nil {
				return err
			}
			if err := eng.Ready(m); err != nil {
				return err
			}
			_ = eng.SyncSSHConfig()
			t := eng.Target(m)
			if len(args) > 1 {
				return sshx.Interactive(t, strings.Join(args[1:], " "))
			}
			if noTmux {
				return sshx.Interactive(t, "SKY_NO_TMUX=1 exec $SHELL -l")
			}
			return sshx.Interactive(t, "")
		},
	}
	c.Flags().BoolVar(&noTmux, "no-tmux", false, "plain shell, not tmux")
	return c
}

func connectCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "connect <name>",
		Short:             "Open a new terminal window connected to a machine",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeMachines,
		RunE: func(cmd *cobra.Command, args []string) error {
			return eng.Connect(args[0])
		},
	}
}

func runCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "run <name|all> <command…>",
		Short:             "Run a command on one machine, or on all of them",
		Args:              cobra.MinimumNArgs(2),
		ValidArgsFunction: completeMachines,
		RunE: func(cmd *cobra.Command, args []string) error {
			command := strings.Join(args[1:], " ")
			names := []string{args[0]}
			if args[0] == "all" {
				ms, _ := eng.Machines()
				names = nil
				for _, m := range ms {
					names = append(names, m.Name)
				}
			}
			failed := 0
			for _, n := range names {
				out, err := eng.Exec(cmd.Context(), n, command)
				if len(names) > 1 {
					fmt.Println(sBold.Render(n))
				}
				fmt.Print(out)
				if err != nil {
					failed++
					fmt.Println(sRed.Render("✗ ") + err.Error())
				}
			}
			if failed > 0 {
				return fmt.Errorf("failed on %d machine(s)", failed)
			}
			return nil
		},
	}
}

// ---------- Claude sessions ----------

func claudeCmd() *cobra.Command {
	var name, prompt, args string
	var detach, yolo bool
	c := &cobra.Command{
		Use:   "claude <machine> [dir]",
		Short: "Start a Claude Code session on a machine (in tmux, survives disconnects)",
		Example: `  sky claude box                      # in ~
  sky claude box ~/code/api           # in a repo
  sky claude box ~/code/api -p "fix the failing tests" -d   # start it and come back later`,
		Args:              cobra.RangeArgs(1, 2),
		ValidArgsFunction: completeMachines,
		RunE: func(cmd *cobra.Command, a []string) error {
			dir := "~"
			if len(a) == 2 {
				dir = a[1]
			}
			if yolo {
				args = strings.TrimSpace(args + " --dangerously-skip-permissions")
			}
			s, err := eng.NewSession(cmd.Context(), a[0], engine.SessionOptions{Name: name, Dir: dir, Claude: true, Args: args, Prompt: prompt})
			if err != nil {
				return err
			}
			if detach || !isTTY() {
				fmt.Printf("%s Claude is running in %s on %s\n", sGreen.Render("●"), sBold.Render(s.Name), a[0])
				fmt.Println(hint("  attach: sky attach " + a[0] + " " + s.Name))
				return nil
			}
			argv, err := eng.AttachArgs(a[0], s.Name)
			if err != nil {
				return err
			}
			return execSSH(argv)
		},
	}
	c.Flags().StringVarP(&name, "name", "n", "", "session name (default: claude-<folder>)")
	c.Flags().StringVarP(&prompt, "prompt", "p", "", "first message for Claude")
	c.Flags().StringVar(&args, "args", "", "extra flags for claude")
	c.Flags().BoolVarP(&detach, "detach", "d", false, "start it without attaching")
	c.Flags().BoolVar(&yolo, "skip-permissions", false, "pass --dangerously-skip-permissions (only on machines you'd let Claude do anything on)")
	return c
}

// codingAgentCmd starts another coding agent's session the way `sky claude` starts Claude's.
func codingAgentCmd(agent string) *cobra.Command {
	var name, prompt string
	var detach bool
	title := engine.AgentName(agent)
	c := &cobra.Command{
		Use:   agent + " <machine> [dir]",
		Short: "Start a " + title + " session on a machine (in tmux, survives disconnects)",
		Example: fmt.Sprintf(`  sky %[1]s box                      # in ~
  sky %[1]s box ~/code/api           # in a repo`, agent),
		Args:              cobra.RangeArgs(1, 2),
		ValidArgsFunction: completeMachines,
		RunE: func(cmd *cobra.Command, a []string) error {
			dir := "~"
			if len(a) == 2 {
				dir = a[1]
			}
			s, err := eng.NewSession(cmd.Context(), a[0], engine.SessionOptions{Name: name, Dir: dir, Agent: agent, Prompt: prompt})
			if err != nil {
				return err
			}
			if detach || !isTTY() {
				fmt.Printf("%s %s is running in %s on %s\n", sGreen.Render("●"), title, sBold.Render(s.Name), a[0])
				fmt.Println(hint("  attach: sky attach " + a[0] + " " + s.Name))
				return nil
			}
			argv, err := eng.AttachArgs(a[0], s.Name)
			if err != nil {
				return err
			}
			return execSSH(argv)
		},
	}
	c.Flags().StringVarP(&name, "name", "n", "", "session name (default: "+agent+"-<folder>)")
	if agent != "mantis" {
		c.Flags().StringVarP(&prompt, "prompt", "p", "", "first message for "+title)
	}
	c.Flags().BoolVarP(&detach, "detach", "d", false, "start it without attaching")
	return c
}

func stateLabel(s engine.Session) string {
	who := "" // which agent, for those other than Claude
	if s.Agent != "" && s.Agent != "claude" {
		who = sDim.Render(" · " + s.Agent)
	}
	switch s.State {
	case "working":
		return sAccent.Render("◉ working") + who
	case "waiting":
		return sAmber.Render("◉ needs you") + who
	case "idle":
		return sDim.Render("○ idle") + who
	}
	if s.Agent != "" {
		return sDim.Render("○ " + s.Agent)
	}
	return sDim.Render("· " + s.Command)
}

func ago(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

func sessionsCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "sessions [machine]",
		Aliases:           []string{"ps"},
		Short:             "List tmux and Claude sessions on your machines",
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeMachines,
		RunE: func(cmd *cobra.Command, args []string) error {
			var list []engine.Session
			errs := map[string]string{}
			if len(args) == 1 {
				l, err := eng.Sessions(cmd.Context(), args[0])
				if err != nil {
					return err
				}
				list = l
			} else {
				list, errs = eng.AllSessions(cmd.Context())
			}
			if jsonOut {
				return printJSON(list)
			}
			if len(list) == 0 {
				fmt.Println(hint("No sessions. Start one with `sky claude <machine> [dir]`."))
			} else {
				var rows [][]string
				for _, s := range list {
					path := s.Path
					if home := "/Users/"; strings.HasPrefix(path, home) && !strings.HasPrefix(path, "/home/") {
						if i := strings.Index(path[len(home):], "/"); i >= 0 {
							path = "~" + path[len(home)+i:]
						} else {
							path = "~"
						}
					}
					if home := "/home/"; strings.HasPrefix(path, home) {
						if i := strings.Index(path[len(home):], "/"); i >= 0 {
							path = "~" + path[len(home)+i:]
						} else {
							path = "~"
						}
					}
					att := ""
					if s.Attached > 0 {
						att = sGreen.Render(" ⬤")
					}
					title := s.Title
					if len([]rune(title)) > 44 {
						title = string([]rune(title)[:43]) + "…"
					}
					rows = append(rows, []string{s.Machine, sBold.Render(s.Name) + att, title, stateLabel(s), path, sDim.Render(ago(s.Activity))})
				}
				table(os.Stdout, []string{"machine", "session", "title", "state", "dir", "active"}, rows)
				fmt.Println("\n  " + hint("sky attach <machine> <session> · ⬤ = attached somewhere"))
			}
			for m, e := range errs {
				fmt.Println(sAmber.Render("  ! ") + m + ": " + e)
			}
			return nil
		},
	}
}

func attachCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "attach <machine> [session]",
		Aliases:           []string{"a"},
		Short:             "Attach to a session (default: the most recent Claude session)",
		Args:              cobra.RangeArgs(1, 2),
		ValidArgsFunction: completeMachines,
		RunE: func(cmd *cobra.Command, args []string) error {
			session := ""
			if len(args) == 2 {
				session = args[1]
			} else {
				list, err := eng.Sessions(cmd.Context(), args[0])
				if err != nil {
					return err
				}
				for _, s := range list { // newest first
					if s.Claude {
						session = s.Name
						break
					}
				}
				if session == "" && len(list) > 0 {
					session = list[0].Name
				}
				if session == "" {
					session = "main"
				}
			}
			argv, err := eng.AttachArgs(args[0], session)
			if err != nil {
				return err
			}
			return execSSH(argv)
		},
	}
}

// ---------- ports ----------

func portsCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "ports <name>",
		Short:             "List ports something is listening on inside a machine",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeMachines,
		RunE: func(cmd *cobra.Command, args []string) error {
			ports, err := eng.Ports(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if jsonOut {
				return printJSON(ports)
			}
			if len(ports) == 0 {
				fmt.Println(hint("Nothing listening yet. Start a dev server, then `sky open " + args[0] + " <port>`."))
				return nil
			}
			var rows [][]string
			for _, p := range ports {
				scope := "this machine only"
				if p.Address == "0.0.0.0" || p.Address == "*" || p.Address == "::" {
					scope = "all interfaces"
				}
				rows = append(rows, []string{sBold.Render(strconv.Itoa(p.Port)), p.Process, sDim.Render(p.Address + "  " + scope)})
			}
			table(os.Stdout, []string{"port", "process", "listening on"}, rows)
			fmt.Println("\n  " + hint("sky open "+args[0]+" <port> forwards it to localhost and opens your browser"))
			return nil
		},
	}
}

func openCmd() *cobra.Command {
	var local int
	var noOpen bool
	c := &cobra.Command{
		Use:   "open <name> <port>",
		Short: "Forward a machine's port to localhost and open it in your browser",
		Example: `  sky open box 3000          # http://localhost:3000 → box:3000
  sky open box 5432 --local 15432 --no-open`,
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: completeMachines,
		RunE: func(cmd *cobra.Command, args []string) error {
			port, err := strconv.Atoi(args[1])
			if err != nil {
				return errors.New("port must be a number")
			}
			t, err := eng.Forward(cmd.Context(), args[0], port, local)
			if err != nil {
				return err
			}
			defer eng.CloseAllTunnels()
			fmt.Printf("%s %s %s %s:%d\n", sGreen.Render("●"), sCode.Render(t.URL), sDim.Render("→"), args[0], port)
			fmt.Println(hint("  Ctrl-C to stop forwarding"))
			if !noOpen {
				_ = osx.OpenURL(t.URL)
			}
			select {
			case <-cmd.Context().Done():
			case <-t.Done():
				return errors.New("the tunnel closed (machine stopped or network dropped)")
			}
			return nil
		},
	}
	c.Flags().IntVar(&local, "local", 0, "local port (default: same number if free)")
	c.Flags().BoolVar(&noOpen, "no-open", false, "don't open the browser")
	return c
}
