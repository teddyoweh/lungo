package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	"skybuild/internal/config"
	"skybuild/internal/engine"
	"skybuild/internal/events"
	"skybuild/internal/model"
	"skybuild/internal/provider"
)

func machineCommands() []*cobra.Command {
	return []*cobra.Command{newCmd(), addCmd(), lsCmd(), healthCmd(), startCmd(), stopCmd(), restartCmd(), resizeCmd(), diskCmd(), idleCmd(), rmCmd(), importCmd()}
}

// completeMachines offers machine names for shell completion.
func completeMachines(cmd *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	ms, _ := eng.Machines()
	var out []string
	for _, m := range ms {
		out = append(out, m.Name)
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

// ---------- overview ----------

func overview(ctx context.Context) error {
	ms, err := eng.Machines()
	if err != nil {
		return err
	}
	if jsonOut {
		return printJSON(ms)
	}
	fmt.Println()
	fmt.Println("  " + sBold.Render("sky") + sDim.Render("  cloud machines for your Claude Code sessions"))
	fmt.Println()
	if len(ms) == 0 {
		fmt.Println("  You don't have any machines yet.")
		fmt.Println()
		fmt.Println("  " + sCode.Render("sky new") + hint("             create one (VM + persistent volume, ~2 min)"))
		fmt.Println("  " + sCode.Render("sky add") + hint(" box u@host  use a machine you already have"))
		fmt.Println("  " + sCode.Render("sky accounts") + hint("        check your Google Cloud / AWS / Azure logins"))
		fmt.Println()
		return nil
	}
	ms, _ = eng.Refresh(ctx)
	machineTable(ms)
	fmt.Println()
	fmt.Println("  " + hint("ssh <name> to connect · sky claude <name> for a Claude session · sky --help for more"))
	fmt.Println()
	return nil
}

func machineTable(ms []*model.Machine) {
	var rows [][]string
	for _, m := range ms {
		where := m.Provider
		if m.Region != "" {
			where += " · " + m.Region
		}
		addr := m.TailscaleName
		if addr == "" {
			addr = m.Address(false)
		}
		if i := strings.Index(addr, "."); i > 0 && m.TailscaleName != "" {
			addr = addr[:i] + sDim.Render(addr[i:])
		}
		size := m.Size
		if m.DiskGB > 0 {
			size += sDim.Render(fmt.Sprintf(" · %d GB", m.DiskGB))
		}
		if !m.IsCloud() {
			size = sDim.Render(m.OS)
		}
		rows = append(rows, []string{sBold.Render(m.Name), statusDot(m.Status), where, size, addr})
	}
	table(os.Stdout, []string{"name", "status", "where", "size", "address"}, rows)
}

func lsCmd() *cobra.Command {
	var cached bool
	c := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List machines with live status",
		RunE: func(cmd *cobra.Command, _ []string) error {
			var ms []*model.Machine
			var err error
			if cached {
				ms, err = eng.Machines()
			} else {
				ms, err = eng.Refresh(cmd.Context())
			}
			if err != nil {
				return err
			}
			if jsonOut {
				return printJSON(ms)
			}
			if len(ms) == 0 {
				fmt.Println(hint("No machines yet. Create one with `sky new`."))
				return nil
			}
			machineTable(ms)
			return nil
		},
	}
	c.Flags().BoolVar(&cached, "cached", false, "skip asking the clouds; show the last known status")
	return c
}

// ---------- new ----------

func newCmd() *cobra.Command {
	var s model.Spec
	var noTS, noDocker, yes bool
	c := &cobra.Command{
		Use:   "new [name]",
		Short: "Create a machine: VM, persistent volume, toolchain, Tailscale, synced logins",
		Example: `  sky new                      # guided
  sky new api-box --size e2-standard-8 --disk 200
  sky new scratch -p aws --account work --region us-west-2`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if len(args) == 1 {
				s.Name = args[0]
			}
			settings, err := eng.Settings()
			if err != nil {
				return err
			}
			s.Tailscale = settings.TailscaleOn() && !noTS
			s.Docker = settings.DockerOn() && !noDocker
			if err := eng.FillSpec(&s); err != nil {
				return err
			}
			interactive := isTTY() && !jsonOut && !yes
			if interactive {
				if err := wizard(ctx, &s, cmd); err != nil {
					return err
				}
			} else if s.Name == "" {
				return errors.New("give the machine a name: sky new <name>")
			}
			if s.Account == "" {
				return fmt.Errorf("no %s account picked; pass --account (see `sky accounts`)", s.Provider)
			}
			var m *model.Machine
			err = progress(func(r events.Reporter) error {
				var err error
				m, err = eng.Create(ctx, s, r)
				return err
			})
			if err != nil {
				if m != nil {
					fmt.Println(hint(fmt.Sprintf("\n  The VM exists. Retry setup with `sky sync %s`, or remove it with `sky rm %s`.", m.Name, m.Name)))
				}
				return err
			}
			if jsonOut {
				return printJSON(m)
			}
			printReady(m)
			return nil
		},
	}
	f := c.Flags()
	f.StringVarP(&s.Provider, "provider", "p", "", "gcp, aws or azure")
	f.StringVar(&s.Account, "account", "", "GCP project, AWS profile or Azure subscription")
	f.StringVar(&s.Account, "project", "", "alias for --account (GCP)")
	f.StringVar(&s.Region, "region", "", "region, e.g. us-central1")
	f.StringVar(&s.Zone, "zone", "", "zone (GCP), e.g. us-central1-a")
	f.StringVar(&s.Size, "size", "", "machine type, e.g. e2-standard-4")
	f.IntVar(&s.DiskGB, "disk", 0, "persistent volume size in GB (holds /home)")
	f.StringVar(&s.User, "user", "", "username on the machine (default: yours)")
	f.BoolVar(&noTS, "no-tailscale", false, "don't join the tailnet")
	f.BoolVar(&noDocker, "no-docker", false, "don't install Docker")
	f.BoolVarP(&yes, "yes", "y", false, "no questions; use flags and saved defaults")
	_ = f.MarkHidden("project")
	return c
}

func printReady(m *model.Machine) {
	short, full := eng.SSHCommands(m)
	lines := [][2]string{
		{short, map[bool]string{true: "connect", false: "connect (lands in tmux)"}[m.OS == "darwin"]},
		{"sky claude " + m.Name + " ~/code/app", "start a Claude session there"},
		{"sky push . " + m.Name + ":~/code/app", "copy a folder up"},
	}
	w := 0
	for _, l := range lines {
		w = max(w, len(l[0]))
	}
	fmt.Println()
	fmt.Println("  " + sGreen.Render("●") + " " + sBold.Render(m.Name) + " is ready")
	fmt.Println()
	for _, l := range lines {
		fmt.Println("    " + sCode.Render(l[0]) + strings.Repeat(" ", w-len(l[0])+3) + hint(l[1]))
	}
	fmt.Println()
	fmt.Println("    " + hint("Without sky: "+full))
	fmt.Println()
}

// wizard fills the spec with guided questions, skipping anything set by flags.
func wizard(ctx context.Context, s *model.Spec, cmd *cobra.Command) error {
	set := func(name string) bool { return cmd.Flags().Changed(name) }
	// Provider
	if !set("provider") {
		statuses := eng.ProviderStatuses(ctx)
		var opts []huh.Option[string]
		for _, st := range statuses {
			label := st.Label
			switch {
			case st.LoggedIn:
				label += sDim.Render("  " + st.Identity)
			case st.Installed:
				label += sAmber.Render("  not signed in")
			default:
				label += sDim.Render("  " + st.CLI + " not installed")
			}
			opts = append(opts, huh.NewOption(label, st.ID))
		}
		if err := huh.NewForm(huh.NewGroup(huh.NewSelect[string]().Title("Where should it run?").Options(opts...).Value(&s.Provider))).
			WithTheme(formTheme()).Run(); err != nil {
			return err
		}
		if err := eng.FillSpec(s); err != nil {
			return err
		}
	}
	p, err := eng.Provider(s.Provider)
	if err != nil {
		return err
	}
	st := p.Status(ctx)
	if !st.Installed {
		return fmt.Errorf("%s needs the %s CLI: %s", st.Label, st.CLI, st.Install)
	}
	if !st.LoggedIn {
		if !confirm("Sign in to "+st.Label+"?", st.Hint+". This opens your browser.") {
			return errors.New("not signed in to " + st.Label)
		}
		if err := progress(func(r events.Reporter) error { return p.Login(ctx, r) }); err != nil {
			return err
		}
		st = p.Status(ctx)
	}
	// Account
	if !set("account") && !set("project") && len(st.Accounts) > 0 {
		var opts []huh.Option[string]
		found := false
		for _, a := range st.Accounts {
			label := a.ID
			if a.Label != "" && a.Label != a.ID {
				label += sDim.Render("  " + a.Label)
			}
			opts = append(opts, huh.NewOption(label, a.ID))
			found = found || a.ID == s.Account
		}
		if !found {
			for _, a := range st.Accounts {
				if a.Default {
					s.Account = a.ID
				}
			}
		}
		title := map[string]string{"gcp": "Project", "aws": "AWS profile", "azure": "Subscription"}[s.Provider]
		if err := huh.NewForm(huh.NewGroup(huh.NewSelect[string]().Title(title).Options(opts...).Value(&s.Account).Filtering(true).Height(min(len(opts)+2, 12)))).
			WithTheme(formTheme()).Run(); err != nil {
			return err
		}
	}
	// Region, size, disk, name
	var regionOpts []huh.Option[string]
	for _, r := range p.Regions() {
		regionOpts = append(regionOpts, huh.NewOption(r.ID+sDim.Render("  "+r.Label), r.ID))
	}
	var sizeOpts []huh.Option[string]
	for _, z := range p.Sizes() {
		label := fmt.Sprintf("%-8s %-16s %2d vCPU  %3.0f GB  ~$%3.0f/mo", z.Label, z.ID, z.CPUs, z.MemoryGB, z.Monthly)
		sizeOpts = append(sizeOpts, huh.NewOption(label+sDim.Render("  "+z.Note), z.ID))
	}
	if s.Region == "" && s.Zone != "" {
		s.Region = s.Zone[:strings.LastIndex(s.Zone, "-")]
	}
	disk := strconv.Itoa(s.DiskGB)
	name := s.Name
	if name == "" {
		name = suggestName()
	}
	var fields []huh.Field
	if !set("region") && !set("zone") && len(regionOpts) > 0 {
		fields = append(fields, huh.NewSelect[string]().Title("Region").Options(regionOpts...).Value(&s.Region).Height(8))
	}
	if !set("size") && len(sizeOpts) > 0 {
		fields = append(fields, huh.NewSelect[string]().Title("Size").Description("You can resize later with `sky resize`").Options(sizeOpts...).Value(&s.Size))
	}
	if !set("disk") {
		fields = append(fields, huh.NewInput().Title("Volume size (GB)").Description("Holds /home: your repos, tools and Claude state. It outlives the VM and can grow later.").
			Value(&disk).Validate(func(v string) error {
			n, err := strconv.Atoi(v)
			if err != nil || n < 20 || n > 16000 {
				return errors.New("a number from 20 to 16000")
			}
			return nil
		}))
	}
	if s.Name == "" {
		fields = append(fields, huh.NewInput().Title("Name").Description("Becomes `ssh <name>` and the hostname").Value(&name).Validate(config.ValidName))
	}
	if len(fields) > 0 {
		if err := huh.NewForm(huh.NewGroup(fields...)).WithTheme(formTheme()).Run(); err != nil {
			return err
		}
	}
	s.Name = name
	s.DiskGB, _ = strconv.Atoi(disk)
	if set("region") || !set("zone") {
		s.Zone = "" // provider picks the zone for the region
	}
	// Summary
	size, _ := provider.SizeByID(p.Sizes(), s.Size)
	diskCost := float64(s.DiskGB) * st.DiskPerGB
	fmt.Println()
	fmt.Printf("  %s  %s · %s · %s\n", sBold.Render(s.Name), p.Label(), s.Account, s.Region)
	fmt.Printf("  %s  %s, %d GB volume, Tailscale %s, Docker %s\n", sDim.Render("    "), s.Size, s.DiskGB, onOff(s.Tailscale), onOff(s.Docker))
	if size.Monthly > 0 {
		fmt.Printf("  %s  ~%s/mo running · ~%s/mo stopped (volume only)\n", sDim.Render("    "), money(size.Monthly+diskCost), money(diskCost))
	}
	fmt.Println()
	if !confirm("Create "+s.Name+"?", "") {
		return errors.New("cancelled")
	}
	return nil
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func suggestName() string {
	ms, _ := eng.Machines()
	taken := map[string]bool{}
	for _, m := range ms {
		taken[m.Name] = true
	}
	for i := 1; ; i++ {
		n := fmt.Sprintf("box-%d", i)
		if !taken[n] {
			return n
		}
	}
}

// ---------- add ----------

func addCmd() *cobra.Command {
	var a engine.AddSpec
	var noTakeOver, list bool
	c := &cobra.Command{
		Use:   "add [name] [user@host | --alias <ssh-alias>]",
		Short: "Add a machine you already have: a Mac, a home server, a VPS",
		Long: `Add a machine you already have. With no arguments, sky lists what it can already see
(entries in your ssh config and devices on your tailnet) and adds the one you pick.

sky authorizes its own key there, learns the machine's Tailscale address so it works away from
home, installs tmux on a Mac if it's missing (sessions run in it), and syncs your setup.`,
		Example: `  sky add                                   # pick from what sky found
  sky add mini --alias macmini              # an entry from ~/.ssh/config
  sky add lab teddy@10.0.0.20 --setup       # Linux: install the toolchain too
  sky add pi pi@raspberrypi.local --port 2222 --key ~/.ssh/pi`,
		Args: cobra.MaximumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if list || (len(args) == 0 && a.Alias == "") {
				found := eng.Discover(ctx)
				if jsonOut {
					return printJSON(found)
				}
				if len(found) == 0 {
					fmt.Println(hint("Nothing found in your ssh config or on your tailnet. Add one by address: sky add <name> <user>@<host>"))
					return nil
				}
				if list || !isTTY() {
					var rows [][]string
					for _, c := range found {
						state := sDim.Render("○ offline")
						if c.Online {
							state = sGreen.Render("● online")
						}
						rows = append(rows, []string{sBold.Render(c.Name), state, c.OS, sDim.Render(c.Detail)})
					}
					table(os.Stdout, []string{"machine", "", "", "found"}, rows)
					fmt.Println("\n  " + hint("sky add  picks one interactively"))
					return nil
				}
				var opts []huh.Option[int]
				for i, c := range found {
					dot := sDim.Render("○")
					if c.Online {
						dot = sGreen.Render("●")
					}
					opts = append(opts, huh.NewOption(fmt.Sprintf("%s %-22s %s", dot, c.Name, sDim.Render(c.Detail)), i))
				}
				pick := 0
				if err := huh.NewForm(huh.NewGroup(huh.NewSelect[int]().Title("Which machine?").Options(opts...).Value(&pick))).WithTheme(formTheme()).Run(); err != nil {
					return err
				}
				c := found[pick]
				take := false
				if c.OtherSync != "" && !noTakeOver {
					take = confirm("Let sky keep it in sync from now on?", c.OtherSync+" syncs this machine today. Two tools writing the same login files would fight, so sky turns "+c.OtherSync+" off (you can turn it back on).")
				}
				a = c.Spec(c.Name, take)
			} else {
				if len(args) >= 1 {
					a.Name = args[0]
				}
				if len(args) == 2 {
					dest := args[1]
					if i := strings.LastIndex(dest, "@"); i > 0 {
						a.User, a.Host = dest[:i], dest[i+1:]
					} else {
						a.Host = dest
					}
					if h, p, ok := strings.Cut(a.Host, ":"); ok {
						a.Host = h
						a.Port, _ = strconv.Atoi(p)
					}
				}
				if a.Name == "" {
					a.Name = a.Alias
				}
				if a.Alias == "" && a.Host == "" {
					return errors.New("give user@host or --alias (or run `sky add` alone to pick from what sky found)")
				}
				a.TakeOver = !noTakeOver
			}
			var m *model.Machine
			err := progress(func(r events.Reporter) error {
				var err error
				m, err = eng.Add(ctx, a, r)
				return err
			})
			if err != nil {
				return err
			}
			if jsonOut {
				return printJSON(m)
			}
			printReady(m)
			return nil
		},
	}
	c.Flags().StringVar(&a.Alias, "alias", "", "start from this Host in ~/.ssh/config")
	c.Flags().IntVar(&a.Port, "port", 0, "SSH port")
	c.Flags().StringVar(&a.KeyPath, "key", "", "private key to sign in with the first time")
	c.Flags().BoolVar(&a.Setup, "setup", false, "Linux: install Node, Docker, gh, tmux, Claude Code (needs passwordless sudo)")
	c.Flags().BoolVar(&a.Tailscale, "tailscale", false, "Linux: join the tailnet")
	c.Flags().BoolVar(&noTakeOver, "no-takeover", false, "leave another sync tool (claude-sync-mini) in charge; sky won't sync this machine")
	c.Flags().BoolVar(&list, "list", false, "only list what sky found")
	return c
}

// ---------- lifecycle ----------

func simple(use, short string, fn func(ctx context.Context, name string, r events.Reporter) error) *cobra.Command {
	return &cobra.Command{
		Use:               use + " <name>",
		Short:             short,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeMachines,
		RunE: func(cmd *cobra.Command, args []string) error {
			return progress(func(r events.Reporter) error { return fn(cmd.Context(), args[0], r) })
		},
	}
}

func startCmd() *cobra.Command {
	return simple("start", "Start a stopped machine", startAndTend)
}

func stopCmd() *cobra.Command {
	return simple("stop", "Stop a machine (the volume stays; only storage is billed)", eng.Stop)
}

func restartCmd() *cobra.Command { return simple("restart", "Stop and start a machine", eng.Restart) }

func resizeCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "resize <name> [size]",
		Short:             "Change a machine's size (restarts it)",
		Args:              cobra.RangeArgs(1, 2),
		ValidArgsFunction: completeMachines,
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := eng.Machine(args[0])
			if err != nil {
				return err
			}
			p, err := eng.Provider(m.Provider)
			if err != nil {
				return fmt.Errorf("%s isn't a cloud machine", m.Name)
			}
			size := ""
			if len(args) == 2 {
				size = args[1]
			} else {
				if !isTTY() {
					return errors.New("give a size")
				}
				var opts []huh.Option[string]
				for _, z := range p.Sizes() {
					label := fmt.Sprintf("%-8s %-16s %2d vCPU  %3.0f GB  ~$%3.0f/mo", z.Label, z.ID, z.CPUs, z.MemoryGB, z.Monthly)
					if z.ID == m.Size {
						label += sDim.Render("  current")
					}
					opts = append(opts, huh.NewOption(label, z.ID))
				}
				size = m.Size
				if err := huh.NewForm(huh.NewGroup(huh.NewSelect[string]().Title("New size for " + m.Name).Options(opts...).Value(&size))).WithTheme(formTheme()).Run(); err != nil {
					return err
				}
			}
			if size == m.Size {
				fmt.Println(hint(m.Name + " is already " + size))
				return nil
			}
			return progress(func(r events.Reporter) error { return eng.Resize(cmd.Context(), m.Name, size, r) })
		},
	}
}

func diskCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "disk <name> <GB>",
		Short:             "Grow a machine's persistent volume",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: completeMachines,
		RunE: func(cmd *cobra.Command, args []string) error {
			gb, err := strconv.Atoi(strings.TrimSuffix(strings.ToUpper(args[1]), "GB"))
			if err != nil {
				return errors.New("size in GB, e.g. sky disk box 200")
			}
			return progress(func(r events.Reporter) error { return eng.GrowDisk(cmd.Context(), args[0], gb, r) })
		},
	}
}

func rmCmd() *cobra.Command {
	var keep, yes bool
	c := &cobra.Command{
		Use:               "rm <name>",
		Aliases:           []string{"delete", "destroy"},
		Short:             "Delete a machine (add --keep-disk to keep its volume for later)",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeMachines,
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := eng.Machine(args[0])
			if err != nil {
				return err
			}
			if !yes {
				what := "the VM and its volume (everything in /home)"
				if keep {
					what = "the VM; the volume stays and comes back with `sky new " + m.Name + "`"
				}
				if !m.IsCloud() {
					what = "it from sky (the machine itself is untouched)"
				}
				if !confirm("Delete "+m.Name+"?", "This removes "+what+".") {
					return errors.New("cancelled")
				}
			}
			return progress(func(r events.Reporter) error { return eng.Delete(cmd.Context(), m.Name, keep, r) })
		},
	}
	c.Flags().BoolVar(&keep, "keep-disk", false, "keep the volume")
	c.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask")
	return c
}

func importCmd() *cobra.Command {
	var account string
	c := &cobra.Command{
		Use:   "import <gcp|aws|azure>",
		Short: "Find machines sky created in a cloud account (e.g. from another computer)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var added []*model.Machine
			err := progress(func(r events.Reporter) error {
				events.Stepf(r, "Looking for sky machines in %s", args[0])
				var err error
				added, err = eng.Import(cmd.Context(), args[0], account, r)
				return err
			})
			if err != nil {
				return err
			}
			if len(added) == 0 {
				fmt.Println(hint("Nothing new found."))
				return nil
			}
			machineTable(added)
			return nil
		},
	}
	c.Flags().StringVar(&account, "account", "", "account/project to search (default: saved default)")
	return c
}
