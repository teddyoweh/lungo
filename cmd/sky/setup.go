package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"skybuild/internal/claudeacct"
	"skybuild/internal/config"
	"skybuild/internal/engine"
	"skybuild/internal/events"
	"skybuild/internal/osx"
	"skybuild/internal/paths"
	"skybuild/internal/sshx"
	"skybuild/internal/syncer"
)

func setupCommands() []*cobra.Command {
	return []*cobra.Command{accountsCmd(), loginCmd(), tailscaleCmd(), agentCmd(), configCmd(), doctorCmd(), appCmd()}
}

// ---------- accounts / login ----------

func accountsCmd() *cobra.Command {
	c := accountsBase()
	c.AddCommand(claudeAccountsCmd())
	return c
}

func accountsBase() *cobra.Command {
	return &cobra.Command{
		Use:   "accounts",
		Short: "Show which clouds, Tailscale, Claude and GitHub are connected",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			st := eng.ProviderStatuses(ctx)
			if jsonOut {
				return printJSON(st)
			}
			var rows [][]string
			for _, s := range st {
				state, detail := sGreen.Render("● connected"), s.Identity
				switch {
				case !s.Installed:
					state, detail = sDim.Render("○ no "+s.CLI), s.Install
				case !s.LoggedIn:
					state, detail = sAmber.Render("● sign in"), "sky login "+s.ID
				default:
					if n := len(s.Accounts); n > 0 {
						detail += sDim.Render(fmt.Sprintf("  %d account(s)", n))
					}
				}
				rows = append(rows, []string{sBold.Render(s.Label), state, detail})
			}
			ts := eng.TailscaleStatus(ctx)
			tsState, tsDetail := sGreen.Render("● connected"), ts.Self+sDim.Render("  "+ts.Tailnet)
			switch {
			case !ts.Installed:
				tsState, tsDetail = sDim.Render("○ not installed"), "https://tailscale.com/download (optional, recommended)"
			case !ts.Connected:
				tsState, tsDetail = sAmber.Render("● "+strings.ToLower(ts.State)), "open Tailscale and connect"
			}
			if ts.HasAuthKey {
				tsDetail += sDim.Render("  auth key saved")
			}
			rows = append(rows, []string{sBold.Render("Tailscale"), tsState, tsDetail})
			cState, cDetail := sAmber.Render("● add one"), "sky accounts claude add"
			if pool, _ := eng.ClaudeAccounts(); len(pool) > 0 {
				cState = sGreen.Render("● connected")
				cDetail = fmt.Sprintf("%d account(s)", len(pool))
				for _, v := range pool {
					if v.Active {
						cDetail = "machines use " + v.Account.Name() + sDim.Render(fmt.Sprintf("  %d account(s) · sky accounts claude", len(pool)))
					}
				}
			} else if syncer.LocalAccount(paths.Home()) == nil {
				cState, cDetail = sAmber.Render("● sign in"), "run `claude` and sign in"
			}
			rows = append(rows, []string{sBold.Render("Claude"), cState, cDetail})
			gh := sDim.Render("○ no gh")
			ghDetail := "https://cli.github.com"
			if osx.Has("gh") {
				out, err := osx.Run(ctx, "gh", "api", "user", "--jq", ".login")
				if err == nil {
					gh, ghDetail = sGreen.Render("● connected"), strings.TrimSpace(out)
				} else {
					gh, ghDetail = sAmber.Render("● sign in"), "gh auth login"
				}
			}
			rows = append(rows, []string{sBold.Render("GitHub"), gh, ghDetail})
			table(os.Stdout, []string{"", "status", ""}, rows)
			return nil
		},
	}
}

func loginCmd() *cobra.Command {
	var token, name string
	c := &cobra.Command{
		Use:   "login <gcp|aws|azure|claude>",
		Short: "Sign in to a cloud, or add a Claude account for your machines",
		Example: `  sky login gcp
  sky login claude --name "Work Max"        # browser approval, token valid for a year
  sky login claude --token sk-ant-oat01-… --name Personal`,
		Args:      cobra.ExactArgs(1),
		ValidArgs: []string{"gcp", "aws", "azure", "claude"},
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if args[0] != "claude" {
				return progress(func(r events.Reporter) error { return eng.Login(ctx, args[0], r) })
			}
			if token == "-" {
				line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
				token = strings.TrimSpace(line)
			}
			return addClaudeAccount(ctx, name, token)
		},
	}
	c.Flags().StringVar(&token, "token", "", "paste a token from `claude setup-token` (- reads stdin)")
	c.Flags().StringVar(&name, "name", "", "what to call this Claude account (e.g. \"Work Max\")")
	return c
}

// ---------- tailscale ----------

func tailscaleCmd() *cobra.Command {
	c := &cobra.Command{
		Use:               "tailscale [machine]",
		Aliases:           []string{"ts"},
		Short:             "Join a machine to your tailnet (or show Tailscale status)",
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeMachines,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				ts := eng.TailscaleStatus(cmd.Context())
				if jsonOut {
					return printJSON(ts)
				}
				if !ts.Installed {
					fmt.Println("Tailscale isn't installed: https://tailscale.com/download")
					return nil
				}
				fmt.Printf("%s %s on %s (%s)\n", statusWord(ts.Connected), ts.Self, ts.Tailnet, ts.IP)
				if ts.HasAuthKey {
					fmt.Println(hint("  auth key saved: new machines join without a browser step"))
				} else {
					fmt.Println(hint("  new machines show an approve link; save an auth key with `sky tailscale key` to skip it"))
				}
				return nil
			}
			return progress(func(r events.Reporter) error { return eng.JoinTailscale(cmd.Context(), args[0], r) })
		},
	}
	key := &cobra.Command{
		Use:   "key [tskey-…]",
		Short: "Save a Tailscale auth key so new machines join unattended (- reads stdin)",
		Long: `Save a reusable auth key from https://login.tailscale.com/admin/settings/keys in your
keychain. Machines then join the tailnet without the approve-in-browser step.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			clear, _ := cmd.Flags().GetBool("clear")
			if clear {
				return eng.SetTailscaleAuthKey("")
			}
			k := ""
			if len(args) == 1 && args[0] != "-" {
				k = args[0]
			} else {
				fmt.Fprint(os.Stderr, "Paste the auth key: ")
				line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
				k = strings.TrimSpace(line)
			}
			if err := eng.SetTailscaleAuthKey(k); err != nil {
				return err
			}
			fmt.Println(sGreen.Render("✓ ") + "saved in your keychain")
			return nil
		},
	}
	key.Flags().Bool("clear", false, "remove the saved key")
	c.AddCommand(key)
	return c
}

func statusWord(ok bool) string {
	if ok {
		return sGreen.Render("● connected")
	}
	return sAmber.Render("● disconnected")
}

// ---------- background agent ----------

const agentLabel = "dev.skybuild.sync"

func agentCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "agent",
		Short: "Background sync: keep machines in sync every few minutes",
	}
	install := &cobra.Command{
		Use:   "install",
		Short: "Run `sky sync` in the background on a timer",
		RunE: func(cmd *cobra.Command, _ []string) error {
			bin, err := stableBinary()
			if err != nil {
				return err
			}
			s, _ := eng.Settings()
			if err := installAgent(bin, s.Interval()); err != nil {
				return err
			}
			fmt.Printf("%s background sync every %s (log: %s)\n", sGreen.Render("✓"), s.Interval(), paths.Tilde(filepath.Join(paths.Logs(), "sync.log")))
			return nil
		},
	}
	uninstall := &cobra.Command{
		Use:   "uninstall",
		Short: "Stop background sync",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := uninstallAgent(); err != nil {
				return err
			}
			fmt.Println(sGreen.Render("✓ ") + "background sync removed")
			return nil
		},
	}
	status := &cobra.Command{
		Use:   "status",
		Short: "Show background sync status and recent log",
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Println(agentState())
			b, err := os.ReadFile(filepath.Join(paths.Logs(), "sync.log"))
			if err == nil {
				lines := strings.Split(strings.TrimSpace(string(b)), "\n")
				if len(lines) > 15 {
					lines = lines[len(lines)-15:]
				}
				fmt.Println(sDim.Render(strings.Join(lines, "\n")))
			}
			return nil
		},
	}
	c.AddCommand(install, uninstall, status)
	return c
}

// stableBinary returns a path background jobs can run. macOS won't let launchd jobs read
// ~/Documents, ~/Desktop or ~/Downloads, so a binary there is copied to ~/.local/bin.
func stableBinary() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	exe, _ = filepath.EvalSymlinks(exe)
	h := paths.Home()
	protected := false
	for _, d := range []string{"Documents", "Desktop", "Downloads"} {
		if strings.HasPrefix(exe, filepath.Join(h, d)+string(filepath.Separator)) {
			protected = true
		}
	}
	if strings.Contains(exe, "go-build") || protected {
		dst := filepath.Join(h, ".local", "bin", "sky")
		if runtime.GOOS == "windows" {
			dst += ".exe"
		}
		b, err := os.ReadFile(exe)
		if err != nil {
			return "", err
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(dst+".tmp", b, 0o755); err != nil {
			return "", err
		}
		if err := os.Rename(dst+".tmp", dst); err != nil {
			return "", err
		}
		return dst, nil
	}
	return exe, nil
}

func installAgent(bin string, every time.Duration) error {
	switch runtime.GOOS {
	case "darwin":
		plist := filepath.Join(paths.Home(), "Library", "LaunchAgents", agentLabel+".plist")
		content := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key><array><string>%s</string><string>sync</string><string>--background</string></array>
  <key>StartInterval</key><integer>%d</integer>
  <key>RunAtLoad</key><true/>
  <key>ProcessType</key><string>Background</string>
  <key>EnvironmentVariables</key><dict><key>PATH</key><string>%s/.local/bin:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin</string></dict>
  <key>StandardErrorPath</key><string>%s</string>
</dict>
</plist>
`, agentLabel, bin, int(every.Seconds()), paths.Home(), filepath.Join(paths.Logs(), "agent.err"))
		if err := os.MkdirAll(filepath.Dir(plist), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(plist, []byte(content), 0o644); err != nil {
			return err
		}
		uid := strconv.Itoa(os.Getuid())
		exec.Command("launchctl", "bootout", "gui/"+uid+"/"+agentLabel).Run()
		if out, err := exec.Command("launchctl", "bootstrap", "gui/"+uid, plist).CombinedOutput(); err != nil {
			return fmt.Errorf("launchctl: %s", strings.TrimSpace(string(out)))
		}
		return nil
	case "linux":
		dir := filepath.Join(paths.Home(), ".config", "systemd", "user")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		svc := fmt.Sprintf("[Unit]\nDescription=skybuild sync\n\n[Service]\nType=oneshot\nExecStart=%s sync --background\n", bin)
		timer := fmt.Sprintf("[Unit]\nDescription=skybuild sync timer\n\n[Timer]\nOnBootSec=1min\nOnUnitActiveSec=%ds\n\n[Install]\nWantedBy=timers.target\n", int(every.Seconds()))
		if err := os.WriteFile(filepath.Join(dir, "skybuild-sync.service"), []byte(svc), 0o644); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, "skybuild-sync.timer"), []byte(timer), 0o644); err != nil {
			return err
		}
		exec.Command("systemctl", "--user", "daemon-reload").Run()
		if out, err := exec.Command("systemctl", "--user", "enable", "--now", "skybuild-sync.timer").CombinedOutput(); err != nil {
			return fmt.Errorf("systemctl: %s", strings.TrimSpace(string(out)))
		}
		return nil
	case "windows":
		mins := int(every.Minutes())
		if mins < 1 {
			mins = 1
		}
		out, err := exec.Command("schtasks", "/Create", "/F", "/SC", "MINUTE", "/MO", strconv.Itoa(mins), "/TN", "skybuild-sync",
			"/TR", fmt.Sprintf(`"%s" sync --background`, bin)).CombinedOutput()
		if err != nil {
			return fmt.Errorf("schtasks: %s", strings.TrimSpace(string(out)))
		}
		return nil
	}
	return errors.New("background sync isn't supported on " + runtime.GOOS)
}

func uninstallAgent() error {
	switch runtime.GOOS {
	case "darwin":
		exec.Command("launchctl", "bootout", "gui/"+strconv.Itoa(os.Getuid())+"/"+agentLabel).Run()
		os.Remove(filepath.Join(paths.Home(), "Library", "LaunchAgents", agentLabel+".plist"))
	case "linux":
		exec.Command("systemctl", "--user", "disable", "--now", "skybuild-sync.timer").Run()
		dir := filepath.Join(paths.Home(), ".config", "systemd", "user")
		os.Remove(filepath.Join(dir, "skybuild-sync.service"))
		os.Remove(filepath.Join(dir, "skybuild-sync.timer"))
	case "windows":
		exec.Command("schtasks", "/Delete", "/F", "/TN", "skybuild-sync").Run()
	}
	return nil
}

func agentState() string {
	switch runtime.GOOS {
	case "darwin":
		if exec.Command("launchctl", "print", "gui/"+strconv.Itoa(os.Getuid())+"/"+agentLabel).Run() == nil {
			return sGreen.Render("●") + " background sync installed"
		}
	case "linux":
		if exec.Command("systemctl", "--user", "is-active", "--quiet", "skybuild-sync.timer").Run() == nil {
			return sGreen.Render("●") + " background sync installed"
		}
	case "windows":
		if exec.Command("schtasks", "/Query", "/TN", "skybuild-sync").Run() == nil {
			return sGreen.Render("●") + " background sync installed"
		}
	}
	return sDim.Render("○ background sync not installed (sky agent install)")
}

// ---------- config ----------

func configCmd() *cobra.Command {
	keys := []string{"user", "provider", "tailscale", "docker", "tmux", "terminal", "interval", "sync-paths", "skip-credentials", "skip-mcp"}
	return &cobra.Command{
		Use:   "config [key] [value]",
		Short: "Show or change settings",
		Long: "Keys: " + strings.Join(keys, ", ") + `

  user              username created on new machines (default: yours)
  provider          default cloud: gcp, aws or azure
  tailscale         join new machines to the tailnet (true/false)
  docker            install Docker on new machines (true/false)
  tmux              SSH logins land in tmux (true/false)
  terminal          app for ` + "`sky connect`" + `: ` + strings.Join(osx.Terminals(), ", ") + `
  interval          seconds between background syncs
  sync-paths        extra files/folders under ~ to sync, comma-separated
  skip-credentials  credential paths not to sync, comma-separated
  skip-mcp          MCP servers not to copy, comma-separated`,
		Args:      cobra.MaximumNArgs(2),
		ValidArgs: keys,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := eng.Settings()
			if err != nil {
				return err
			}
			get := func(k string) string {
				switch k {
				case "user":
					return s.User()
				case "provider":
					return s.DefaultProvider
				case "tailscale":
					return strconv.FormatBool(s.TailscaleOn())
				case "docker":
					return strconv.FormatBool(s.DockerOn())
				case "tmux":
					return strconv.FormatBool(s.AutoTmuxOn())
				case "terminal":
					return s.Terminal
				case "interval":
					return strconv.Itoa(int(s.Interval().Seconds()))
				case "sync-paths":
					return strings.Join(s.SyncPaths, ",")
				case "skip-credentials":
					return strings.Join(s.SkipCredentials, ",")
				case "skip-mcp":
					return strings.Join(s.SkipMCP, ",")
				}
				return ""
			}
			if len(args) == 0 {
				if jsonOut {
					return printJSON(s)
				}
				var rows [][]string
				for _, k := range keys {
					rows = append(rows, []string{k, get(k)})
				}
				table(os.Stdout, []string{"setting", "value"}, rows)
				fmt.Println("\n  " + hint("config file: "+paths.Tilde(paths.Config())))
				return nil
			}
			if len(args) == 1 {
				fmt.Println(get(args[0]))
				return nil
			}
			v := args[1]
			list := func() []string {
				if v == "" {
					return nil
				}
				return strings.Split(v, ",")
			}
			b := func() (*bool, error) {
				x, err := strconv.ParseBool(v)
				return &x, err
			}
			var perr error
			err = eng.UpdateSettings(func(s *config.Settings) {
				switch args[0] {
				case "user":
					s.RemoteUser = v
				case "provider":
					s.DefaultProvider = v
				case "tailscale":
					s.Tailscale, perr = b()
				case "docker":
					s.Docker, perr = b()
				case "tmux":
					s.AutoTmux, perr = b()
				case "terminal":
					s.Terminal = v
				case "interval":
					s.SyncInterval, perr = strconv.Atoi(v)
				case "sync-paths":
					s.SyncPaths = list()
				case "skip-credentials":
					s.SkipCredentials = list()
				case "skip-mcp":
					s.SkipMCP = list()
				default:
					perr = fmt.Errorf("unknown key %q", args[0])
				}
			})
			if err == nil {
				err = perr
			}
			if err == nil {
				fmt.Println(sGreen.Render("✓ ") + args[0] + " = " + v)
			}
			return err
		},
	}
}

// ---------- doctor ----------

func doctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check everything sky depends on",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
			defer cancel()
			ok := func(good bool, name, detail string) {
				mark := sGreen.Render("✓")
				if !good {
					mark = sAmber.Render("!")
				}
				fmt.Printf("  %s %-20s %s\n", mark, name, sDim.Render(detail))
			}
			fmt.Println()
			ok(osx.Has("ssh"), "ssh", osx.Which("ssh"))
			ok(osx.Has("rsync"), "rsync", firstNon(osx.Which("rsync"), "missing: folder copies fall back to tar"))
			_, pub, err := sshx.EnsureKey()
			ok(err == nil, "sky key", firstNon(paths.Tilde(paths.DefaultKey()), fmt.Sprint(err)))
			_ = pub
			inc, _ := os.ReadFile(paths.UserSSHConfig())
			ok(strings.Contains(string(inc), "skybuild"), "ssh config", "~/.ssh/config includes ~/.skybuild/ssh_config")
			ts := eng.TailscaleStatus(ctx)
			ok(ts.Connected, "tailscale", firstNon(ts.Self, "not connected (optional)"))
			for _, s := range eng.ProviderStatuses(ctx) {
				d := s.Identity
				if !s.LoggedIn {
					d = s.Hint
				}
				ok(s.LoggedIn, s.Label, d)
			}
			v, err := osx.Run(ctx, "claude", "--version")
			ok(err == nil, "claude", strings.TrimSpace(v))
			acct := syncer.LocalAccount(paths.Home())
			if acct != nil {
				_, terr := syncer.Token(ctx, acct, false, false)
				ok(terr == nil, "claude token", firstNon(map[bool]string{true: "ready for machines"}[terr == nil], "sky login claude"))
			}
			_, gerr := osx.Run(ctx, "gh", "auth", "status")
			ok(gerr == nil, "gh", map[bool]string{true: "signed in", false: "gh auth login"}[gerr == nil])
			fmt.Println("  " + agentState())
			fmt.Println()
			return nil
		},
	}
}

func firstNon(a, b string) string {
	if a != "" && a != "rsync" {
		return a
	}
	return b
}

// ---------- app ----------

func appCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "app",
		Short: "Open the Lungo desktop app",
		RunE: func(cmd *cobra.Command, _ []string) error {
			switch runtime.GOOS {
			case "darwin":
				// Skybuild.app is the app's old name, for a copy installed before the rename.
				for _, name := range []string{"Lungo.app", "Skybuild.app"} {
					for _, p := range []string{filepath.Join("/Applications", name), filepath.Join(paths.Home(), "Applications", name)} {
						if _, err := os.Stat(p); err == nil {
							return exec.Command("open", p).Run()
						}
					}
				}
				return exec.Command("open", "-a", "Lungo").Run()
			case "windows":
				return exec.Command("cmd", "/c", "start", "", "Lungo").Run()
			default:
				return exec.Command("skybuild-desktop").Start()
			}
		},
	}
}

// ---------- Claude accounts ----------

func addClaudeAccount(ctx context.Context, name, token string) error {
	if name == "" && isTTY() {
		fmt.Fprint(os.Stderr, "Name for this Claude account (e.g. Work Max): ")
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		name = strings.TrimSpace(line)
	}
	if token == "" && !jsonOut {
		fmt.Println(hint("  Your browser opens claude.ai. Make sure it's signed in as the account you're adding."))
	}
	return progress(func(r events.Reporter) error {
		if _, err := eng.AddClaudeAccount(ctx, name, token, r); err != nil {
			return err
		}
		_, err := eng.ClaudeTick(ctx, nil, r) // switch right away if the active account is limited
		return err
	})
}

func bar(frac float64, width int) string {
	n := int(frac*float64(width) + 0.5)
	if n > width {
		n = width
	}
	if n < 0 {
		n = 0
	}
	style := sGreen
	switch {
	case frac >= 1:
		style = sRed
	case frac >= 0.8:
		style = sAmber
	}
	return style.Render(strings.Repeat("█", n)) + sDim.Render(strings.Repeat("░", width-n))
}

func window(st claudeacct.Status, name string) string {
	w := st.Windows[name]
	if w == nil {
		return sDim.Render("–")
	}
	s := fmt.Sprintf("%s %3.0f%%", bar(w.Utilization, 8), w.Utilization*100)
	if w.Utilization >= 1 && !w.ResetsAt.IsZero() {
		s += sDim.Render(" until " + w.ResetsAt.Local().Format("Mon 3:04PM"))
	}
	return s
}

func printClaudeAccounts(views []engine.ClaudeAccountView) {
	if len(views) == 0 {
		fmt.Println(hint("No Claude accounts yet. Add one with `sky accounts claude add`."))
		return
	}
	var rows [][]string
	for i, v := range views {
		state := sGreen.Render("● ready")
		switch v.Status.State {
		case claudeacct.StateLimited:
			if v.Status.Usable() {
				state = sAmber.Render("● reset, recheck")
			} else {
				state = sRed.Render("● limited")
			}
		case claudeacct.StateWarning:
			state = sAmber.Render("● near limit")
		case claudeacct.StateInvalid:
			state = sRed.Render("● token rejected")
		case claudeacct.StateError:
			state = sAmber.Render("● check failed")
		case claudeacct.StateUnknown:
			state = sDim.Render("○ not checked")
		}
		if v.Account.Disabled {
			state = sDim.Render("○ off")
		}
		name := sBold.Render(v.Account.Name())
		if v.Active {
			name += sAccent.Render("  ← machines")
		}
		checked := ""
		if !v.Status.CheckedAt.IsZero() {
			checked = sDim.Render(ago(v.Status.CheckedAt))
		}
		_ = checked
		rows = append(rows, []string{sDim.Render(strconv.Itoa(i + 1)), name, state, window(v.Status, "five_hour"), window(v.Status, "seven_day"), sDim.Render(v.Reason)})
	}
	table(os.Stdout, []string{"#", "account", "status", "5-hour", "weekly", "why"}, rows)
}

func claudeAccountsCmd() *cobra.Command {
	var check bool
	c := &cobra.Command{
		Use:   "claude",
		Short: "Claude accounts for your machines: usage, order and auto-switching",
		Long: `Machines sign in with the active Claude account. When it hits its 5-hour or weekly
limit, sky moves machines to the next account in order and resumes sessions that were stuck on
the limit. Usage comes from a tiny Claude Code request with each account's token.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var views []engine.ClaudeAccountView
			var err error
			if check {
				err = progress(func(r events.Reporter) error {
					events.Stepf(r, "Checking Claude accounts")
					views, err = eng.CheckClaudeAccounts(cmd.Context(), r)
					return err
				})
			} else {
				views, err = eng.ClaudeAccounts()
			}
			if err != nil {
				return err
			}
			if jsonOut {
				return printJSON(views)
			}
			printClaudeAccounts(views)
			s, _ := eng.Settings()
			fmt.Println()
			mode := "smart order: the account with the most allowance about to expire is used first"
			if !s.ClaudeSmart() {
				mode = "your order (sky accounts claude strategy smart to let sky rank them)"
			}
			if s.ClaudeAutoOn() {
				fmt.Println("  " + hint("auto-switch on · "+mode))
			} else {
				fmt.Println("  " + hint("auto-switch off (sky accounts claude auto on) · "+mode))
				for _, v := range views {
					if v.Suggest {
						fmt.Println("  " + sAccent.Render("suggested: ") + "sky accounts claude use " + strconv.Quote(v.Account.Name()) + hint("  ("+v.Reason+")"))
					}
				}
			}
			return nil
		},
	}
	c.Flags().BoolVar(&check, "check", false, "check usage now")

	var addToken string
	add := &cobra.Command{
		Use:   "add [name]",
		Short: "Add a Claude account (browser approval, or --token from `claude setup-token`)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := ""
			if len(args) == 1 {
				name = args[0]
			}
			if addToken == "-" {
				line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
				addToken = strings.TrimSpace(line)
			}
			return addClaudeAccount(cmd.Context(), name, addToken)
		},
	}
	add.Flags().StringVar(&addToken, "token", "", "a token from `claude setup-token` (- reads stdin)")

	use := &cobra.Command{
		Use:   "use <account>",
		Short: "Make an account the active one and sign every machine in with it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return progress(func(r events.Reporter) error { return eng.UseClaudeAccount(cmd.Context(), args[0], r) })
		},
	}
	rm := &cobra.Command{
		Use:   "rm <account>",
		Short: "Forget an account and its token",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := eng.RemoveClaudeAccount(args[0]); err != nil {
				return err
			}
			fmt.Println(sGreen.Render("✓ ") + "removed " + args[0])
			return nil
		},
	}
	move := &cobra.Command{
		Use:   "move <account> <up|down>",
		Short: "Change the switching order",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			d := 1
			if args[1] == "up" {
				d = -1
			}
			if err := eng.MoveClaudeAccount(args[0], d); err != nil {
				return err
			}
			views, _ := eng.ClaudeAccounts()
			printClaudeAccounts(views)
			return nil
		},
	}
	rename := &cobra.Command{
		Use:   "rename <account> <new name>",
		Short: "Rename an account",
		Args:  cobra.ExactArgs(2),
		RunE:  func(cmd *cobra.Command, args []string) error { return eng.RenameClaudeAccount(args[0], args[1]) },
	}
	enable := &cobra.Command{
		Use:   "enable <account>",
		Short: "Put an account back into rotation",
		Args:  cobra.ExactArgs(1),
		RunE:  func(cmd *cobra.Command, args []string) error { return eng.SetClaudeAccountEnabled(args[0], true) },
	}
	disable := &cobra.Command{
		Use:   "disable <account>",
		Short: "Take an account out of rotation (keeps its token)",
		Args:  cobra.ExactArgs(1),
		RunE:  func(cmd *cobra.Command, args []string) error { return eng.SetClaudeAccountEnabled(args[0], false) },
	}
	auto := &cobra.Command{
		Use:       "auto <on|off>",
		Short:     "Turn automatic switching on or off",
		Args:      cobra.ExactArgs(1),
		ValidArgs: []string{"on", "off"},
		RunE: func(cmd *cobra.Command, args []string) error {
			on := args[0] == "on"
			if err := eng.SetClaudeAutoSwitch(on); err != nil {
				return err
			}
			fmt.Println(sGreen.Render("✓ ") + "auto-switch " + args[0])
			return nil
		},
	}
	strategy := &cobra.Command{
		Use:       "strategy <smart|order>",
		Short:     "smart: use whichever account has the most allowance about to expire; order: your list order",
		Args:      cobra.ExactArgs(1),
		ValidArgs: []string{"smart", "order"},
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := eng.SetClaudeStrategy(args[0]); err != nil {
				return err
			}
			fmt.Println(sGreen.Render("✓ ") + "strategy " + args[0])
			return nil
		},
	}
	tick := &cobra.Command{
		Use:   "check",
		Short: "Check every account now and switch if the active one is limited",
		RunE: func(cmd *cobra.Command, _ []string) error {
			var views []engine.ClaudeAccountView
			var res engine.TickResult
			err := progress(func(r events.Reporter) error {
				events.Stepf(r, "Checking Claude accounts")
				var err error
				if views, err = eng.CheckClaudeAccounts(cmd.Context(), r); err != nil {
					return err
				}
				res, err = eng.ClaudeTick(cmd.Context(), nil, r)
				return err
			})
			if err != nil {
				return err
			}
			views, _ = eng.ClaudeAccounts()
			printClaudeAccounts(views)
			switch {
			case res.Switched:
				fmt.Printf("\n  %s switched machines from %s to %s (%s); resumed %d session(s)\n", sGreen.Render("●"), res.From, res.To, res.Reason, res.Restarted)
			case res.AllLimited && !res.Next.IsZero():
				fmt.Printf("\n  %s every account is limited; the first resets %s\n", sAmber.Render("●"), res.Next.Local().Format("Mon 3:04 PM"))
			}
			return nil
		},
	}
	c.AddCommand(add, use, rm, move, rename, enable, disable, auto, strategy, tick)
	return c
}
