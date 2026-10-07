package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"skybuild/internal/bootstrap"
	"skybuild/internal/config"
	"skybuild/internal/events"
	"skybuild/internal/model"
	"skybuild/internal/paths"
	"skybuild/internal/provider"
	"skybuild/internal/secret"
	"skybuild/internal/sshx"
	"skybuild/internal/syncer"
	"skybuild/internal/tailnet"
)

// DefaultDiskGB is the data volume size when none is given.
const DefaultDiskGB = 100

func hasAuthKey() bool { return secret.Has(secret.TailscaleAuthKey) }

// SetTailscaleAuthKey stores (or with "" removes) the key used to join machines unattended.
func (e *Engine) SetTailscaleAuthKey(key string) error {
	key = strings.TrimSpace(key)
	if key != "" && !strings.HasPrefix(key, "tskey-") {
		return errors.New("that doesn't look like a Tailscale auth key (they start with tskey-)")
	}
	return secret.Set(secret.TailscaleAuthKey, key)
}

// FillSpec completes a partial spec from saved defaults and the provider's catalogue.
func (e *Engine) FillSpec(s *model.Spec) error {
	c, err := config.Load()
	if err != nil {
		return err
	}
	if s.Provider == "" {
		s.Provider = c.Settings.DefaultProvider
	}
	if s.Provider == "" {
		s.Provider = model.ProviderGCP
	}
	p, err := e.Provider(s.Provider)
	if err != nil {
		return err
	}
	d := c.Settings.For(s.Provider)
	if s.Account == "" {
		s.Account = d.Account
	}
	if s.Region == "" && s.Zone == "" {
		s.Region, s.Zone = d.Region, d.Zone
	}
	if s.Region == "" && s.Zone == "" {
		s.Region = p.DefaultRegion()
	}
	if s.Size == "" {
		s.Size = d.Size
	}
	if s.Size == "" {
		s.Size = provider.DefaultSize(p.Sizes()).ID
	}
	if s.DiskGB == 0 {
		s.DiskGB = d.DiskGB
	}
	if s.DiskGB == 0 {
		s.DiskGB = DefaultDiskGB
	}
	if s.User == "" {
		s.User = c.Settings.User()
	}
	return nil
}

// Create builds a cloud machine end to end: VM and volume, setup script, Tailscale,
// SSH config and the first sync. It reports each phase to r.
func (e *Engine) Create(ctx context.Context, s model.Spec, r events.Reporter) (*model.Machine, error) {
	if err := config.ValidName(s.Name); err != nil {
		return nil, err
	}
	if m, _ := e.Machine(s.Name); m != nil {
		return nil, fmt.Errorf("you already have a machine named %s", s.Name)
	}
	if err := e.FillSpec(&s); err != nil {
		return nil, err
	}
	p, err := e.Provider(s.Provider)
	if err != nil {
		return nil, err
	}
	if s.Account == "" {
		return nil, fmt.Errorf("pick a %s account first", p.Label())
	}
	settings, _ := e.Settings()

	_, pub, err := sshx.EnsureKey()
	if err != nil {
		return nil, fmt.Errorf("creating sky's ssh key: %w", err)
	}
	s.PublicKey = pub
	devices := map[string]string{
		model.ProviderGCP:   bootstrap.DevicesGCP,
		model.ProviderAWS:   bootstrap.DevicesAWS,
		model.ProviderAzure: bootstrap.DevicesAzure,
	}[s.Provider]
	s.Bootstrap = bootstrap.Render(bootstrap.Options{
		Name: s.Name, User: s.User, PublicKey: pub, DataDevices: devices, DataGB: s.DiskGB,
		Docker: s.Docker, Tailscale: s.Tailscale, AutoTmux: settings.AutoTmuxOn(),
	})

	m, err := p.Create(ctx, s, r)
	if err != nil {
		return nil, err
	}
	m.Sync = syncer.DefaultItems()
	sshx.ForgetHost(m.Name) // a recycled IP must not trip host-key checks
	if err := e.save(m); err != nil {
		return m, err
	}
	_ = config.Update(func(c *config.Config) error {
		c.Settings.Remember(s.Provider, config.Defaults{Account: s.Account, Region: m.Region, Zone: m.Zone, Size: s.Size, DiskGB: s.DiskGB})
		return nil
	})

	if err := e.finishSetup(ctx, m, s.Tailscale, r); err != nil {
		return m, err
	}
	return m, nil
}

// finishSetup waits for the machine, streams the setup log, joins Tailscale and syncs.
func (e *Engine) finishSetup(ctx context.Context, m *model.Machine, joinTailscale bool, r events.Reporter) error {
	events.Stepf(r, "Waiting for SSH on %s", m.PublicIP)
	wctx, cancel := context.WithTimeout(ctx, 8*time.Minute)
	err := sshx.WaitReady(wctx, sshx.For(m, m.PublicIP), func(n int, err error) {
		if n%6 == 0 {
			events.Infof(r, "Still booting (%ds)…", n*5)
		}
	})
	cancel()
	if err != nil {
		return err
	}
	if err := e.watchSetup(ctx, m, r); err != nil {
		return err
	}
	sshx.CloseMaster(m.Name) // the next connection logs in fresh, with the zsh setup just installed
	m.Status = model.StatusRunning
	if joinTailscale {
		if err := e.JoinTailscale(ctx, m.Name, r); err != nil {
			events.Warnf(r, "Tailscale: %v", err)
		}
		if fresh, err := e.Machine(m.Name); err == nil {
			*m = *fresh
		}
	}
	if err := e.save(m); err != nil {
		return err
	}
	events.Stepf(r, "Syncing your Claude Code, GitHub and CLI logins")
	res := e.SyncMachine(ctx, m, syncer.Options{Interactive: true}, r)
	if len(res.Errors) > 0 {
		events.Warnf(r, "Sync finished with %d problem(s); run `sky sync %s` to retry", len(res.Errors), m.Name)
	}
	events.Donef(r, "%s is ready. Connect with: ssh %s", m.Name, m.Name)
	return nil
}

// watchSetup streams the machine's setup log until it reports ready.
func (e *Engine) watchSetup(ctx context.Context, m *model.Machine, r events.Reporter) error {
	events.Stepf(r, "Setting up the machine (packages, Node, Docker, GitHub CLI, Claude Code)")
	cmd := fmt.Sprintf(`sudo bash -c 'touch %[1]s; tail -n +1 -F %[1]s 2>/dev/null & T=$!; for i in $(seq 1 900); do [ -f %[2]s ] && break; sleep 2; done; sleep 1; kill $T; [ -f %[2]s ]'`,
		bootstrap.LogPath, bootstrap.ReadyPath)
	wctx, cancel := context.WithTimeout(ctx, 35*time.Minute)
	defer cancel()
	err := sshx.Stream(wctx, sshx.For(m, m.PublicIP), cmd, func(line string) {
		if strings.HasPrefix(line, "[sky] ") {
			msg := strings.TrimPrefix(line, "[sky] ")
			if msg != "Ready" {
				events.Infof(r, "%s", msg)
			}
			return
		}
		if strings.TrimSpace(line) != "" {
			events.Logf(r, "%s", line)
		}
	})
	if err != nil {
		return fmt.Errorf("setup did not finish: %w (log: ssh %s sudo cat %s)", err, m.Name, bootstrap.LogPath)
	}
	events.Donef(r, "Machine set up")
	return nil
}

// JoinTailscale puts a machine on the tailnet and switches its SSH entry to the 100.x address.
func (e *Engine) JoinTailscale(ctx context.Context, name string, r events.Reporter) error {
	m, err := e.Machine(name)
	if err != nil {
		return err
	}
	local, err := tailnet.Local(ctx)
	if err != nil {
		return fmt.Errorf("Tailscale isn't installed on this computer, so the machine stays on its public IP (%v)", err)
	}
	if local.BackendState != "Running" {
		return fmt.Errorf("Tailscale on this computer is %s; connect it, then run `sky tailscale %s`", strings.ToLower(local.BackendState), name)
	}
	events.Stepf(r, "Joining %s to your tailnet", name)
	ip, dns, err := tailnet.Join(ctx, sshx.For(m, m.Address(false)), m.Name, secret.Get(secret.TailscaleAuthKey), r)
	if err != nil {
		return err
	}
	m.TailscaleIP, m.TailscaleName = ip, dns
	e.tsMu.Lock()
	e.tsChecked = time.Time{} // ask again: this computer is on the tailnet now
	e.tsMu.Unlock()
	if err := e.save(m); err != nil {
		return err
	}
	events.Donef(r, "On the tailnet as %s (%s)", dns, ip)
	return nil
}

// AddSpec describes a machine you already have.
type AddSpec struct {
	Name        string `json:"name"`
	Host        string `json:"host"`
	User        string `json:"user"`
	Port        int    `json:"port"`
	KeyPath     string `json:"keyPath"`     // empty: sky's key, falling back to your usual keys
	Alias       string `json:"alias"`       // an entry from ~/.ssh/config to start from
	Setup       bool   `json:"setup"`       // Linux: install the toolchain like a new machine (needs passwordless sudo)
	Tailscale   bool   `json:"tailscale"`   // Linux: join the tailnet
	TakeOver    bool   `json:"takeOver"`    // turn off another tool that already syncs this machine
	TailscaleIP string `json:"tailscaleIp"` // its tailnet address when already known
}

const authorizeKey = `mkdir -p ~/.ssh && chmod 700 ~/.ssh && touch ~/.ssh/authorized_keys && (grep -qxF %[1]s ~/.ssh/authorized_keys || echo %[1]s >> ~/.ssh/authorized_keys) && chmod 600 ~/.ssh/authorized_keys`

// Add registers a machine you already have: a Mac, a home server, a VPS. It finds a way in
// (an ssh alias, sky's key, or your own keys), authorizes sky's key so later connections
// don't depend on yours, learns its Tailscale address so it works away from home, makes sure
// sessions can run there, and syncs your setup.
func (e *Engine) Add(ctx context.Context, a AddSpec, r events.Reporter) (*model.Machine, error) {
	if err := config.ValidName(a.Name); err != nil {
		return nil, err
	}
	if m, _ := e.Machine(a.Name); m != nil {
		return nil, fmt.Errorf("you already have a machine named %s", a.Name)
	}
	if a.Alias == "" && a.Host == "" {
		return nil, errors.New("give an address (user@host) or an entry from your ssh config")
	}
	_, pub, err := sshx.EnsureKey()
	if err != nil {
		return nil, err
	}
	m := &model.Machine{Name: a.Name, Provider: model.ProviderSSH, Host: a.Host, User: a.User, Port: a.Port,
		KeyPath: a.KeyPath, Sync: syncer.DefaultItems(), TailscaleIP: a.TailscaleIP}

	// 1. Find a way in.
	var way sshx.Target
	aliasOnly := false
	if a.Alias != "" {
		events.Stepf(r, "Connecting through your ssh entry %s", a.Alias)
		way = sshx.Target{Name: a.Name, Alias: a.Alias}
		if !sshx.Reachable(ctx, way) {
			return nil, fmt.Errorf("`ssh %s` doesn't work without a password right now. Check it in a terminal (is the machine on, and on this network or your tailnet?)", a.Alias)
		}
		res, err := sshx.Resolve(ctx, a.Alias)
		if err != nil || res.Proxied {
			aliasOnly = true // only the alias knows how to get there (jump host, proxy)
		} else {
			m.Host, m.User, m.Port = res.Host, res.User, res.Port
		}
	} else {
		if m.User == "" {
			m.User = config.LocalUser()
		}
		events.Stepf(r, "Connecting to %s@%s", m.User, m.Host)
		found := false
		for _, key := range []string{a.KeyPath, "", sshx.NoKey} { // the given key, sky's, then your own
			m.KeyPath = key
			if way = sshx.For(m, m.Host); sshx.Reachable(ctx, way) {
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("couldn't sign in to %s@%s with a key.\n"+
				"  On a Mac, turn on System Settings → General → Sharing → Remote Login first.\n"+
				"  Then authorize sky's key once (it asks for that machine's password):\n"+
				"    ssh-copy-id -i %s.pub %s@%s\n"+
				"  If the username is different there: sky add %s <user>@%s",
				m.User, m.Host, paths.DefaultKey(), m.User, m.Host, a.Name, m.Host)
		}
	}

	// 2. Authorize sky's own key, so sky never depends on your key being unlocked.
	t := way
	if _, err := sshx.Run(ctx, way, fmt.Sprintf(authorizeKey, sshx.Quote(pub))); err != nil {
		return nil, fmt.Errorf("couldn't add sky's key on %s: %w", a.Name, err)
	}
	if aliasOnly {
		m.SSHAlias = a.Alias
	} else {
		m.KeyPath = ""
		if direct := sshx.For(m, m.Host); sshx.Reachable(ctx, direct) {
			t = direct
		} else if a.Alias != "" {
			m.SSHAlias, m.Host, aliasOnly = a.Alias, "", true // the alias carries options we can't reproduce
		} else {
			m.KeyPath = way.KeyPath
			if m.KeyPath == "" {
				m.KeyPath = sshx.NoKey
			}
		}
	}

	// 3. Learn what it is and where else it can be reached.
	facts, _ := sshx.Run(ctx, t, `uname -s; whoami; hostname; (tailscale ip -4 2>/dev/null | head -1) || true`)
	lines := strings.Split(strings.TrimSpace(facts), "\n")
	for len(lines) < 4 {
		lines = append(lines, "")
	}
	m.OS = strings.ToLower(strings.TrimSpace(lines[0]))
	if m.User == "" {
		m.User = strings.TrimSpace(lines[1])
	}
	if ip := strings.TrimSpace(lines[3]); strings.HasPrefix(ip, "100.") && !aliasOnly {
		m.TailscaleIP = ip
	}
	m.Status = model.StatusRunning
	kind := map[string]string{"darwin": "Mac", "linux": "Linux machine"}[m.OS]
	if kind == "" {
		kind = "machine"
	}
	where := "reachable on this network"
	if m.TailscaleIP != "" {
		where = "reachable anywhere through your tailnet (" + m.TailscaleIP + ")"
	}
	events.Donef(r, "%s is a %s, %s", a.Name, kind, where)

	// 4. One tool should keep it in sync, not two.
	if other := otherSync(ctx, m, a.Alias); other != nil {
		if a.TakeOver {
			events.Stepf(r, "Taking over from %s", other.Name)
			if err := other.turnOff(); err != nil {
				events.Warnf(r, "Couldn't turn off %s: %v", other.Name, err)
				m.Sync = []string{}
			} else {
				events.Infof(r, "%s is off. To bring it back: %s", other.Name, other.Undo)
			}
		} else {
			m.Sync = []string{}
			events.Warnf(r, "%s already keeps this machine in sync, so sky's sync is off for it. Add it with take-over (or turn that tool off, then `sky sync items %s`) to let sky do it", other.Name, a.Name)
		}
	}
	if err := e.save(m); err != nil {
		return nil, err
	}
	if m.TailscaleIP != "" {
		e.tsMu.Lock()
		e.tsChecked = time.Time{}
		e.tsMu.Unlock()
		t = e.Target(m)
	}

	// 5. Make sure sessions can run there.
	switch {
	case m.OS == "darwin":
		e.setupMac(ctx, t, r)
	case a.Setup && m.OS == "linux":
		settings, _ := e.Settings()
		script := bootstrap.Render(bootstrap.Options{Name: a.Name, User: m.User, PublicKey: pub, Existing: true,
			Docker: true, Tailscale: a.Tailscale, AutoTmux: settings.AutoTmuxOn()})
		events.Stepf(r, "Installing the toolchain on %s", a.Name)
		err := sshx.StreamInput(ctx, t, "sudo bash -s", strings.NewReader(script), func(line string) {
			if strings.HasPrefix(line, "[sky] ") {
				events.Infof(r, "%s", strings.TrimPrefix(line, "[sky] "))
			}
		})
		if err != nil {
			events.Warnf(r, "Setup reported a problem: %v", err)
		}
	}
	if a.Tailscale && m.OS == "linux" && m.TailscaleIP == "" {
		if err := e.JoinTailscale(ctx, a.Name, r); err != nil {
			events.Warnf(r, "Tailscale: %v", err)
		}
	}

	// 6. Bring your setup over.
	if len(m.Sync) > 0 {
		events.Stepf(r, "Syncing your Claude Code, GitHub and CLI logins")
		res := e.SyncMachine(ctx, m, syncer.Options{Interactive: true}, r)
		if len(res.Errors) > 0 {
			events.Warnf(r, "Sync finished with %d problem(s); `sky sync %s` retries", len(res.Errors), a.Name)
		}
	}
	events.Donef(r, "%s is ready. Connect with: ssh %s", a.Name, firstNonEmpty(m.SSHAlias, a.Name))
	return e.Machine(a.Name)
}

// setupMac makes a Mac ready for sessions: tmux (through Homebrew, no admin rights needed)
// and tmux defaults if it has none. It doesn't touch the Mac's shell setup.
func (e *Engine) setupMac(ctx context.Context, t sshx.Target, r events.Reporter) {
	out, _ := sshx.Run(ctx, t, `command -v tmux >/dev/null && echo has-tmux; command -v brew >/dev/null && echo has-brew; [ -f ~/.tmux.conf ] && echo has-conf; true`)
	if !strings.Contains(out, "has-tmux") {
		if !strings.Contains(out, "has-brew") {
			events.Warnf(r, "tmux isn't installed there, and sessions need it. Install Homebrew (https://brew.sh) on that Mac, then run `brew install tmux`")
			return
		}
		events.Stepf(r, "Installing tmux with Homebrew (sessions run in it)")
		c, cancel := context.WithTimeout(ctx, 10*time.Minute)
		defer cancel()
		if _, err := sshx.Run(c, t, "HOMEBREW_NO_AUTO_UPDATE=1 HOMEBREW_NO_ENV_HINTS=1 brew install tmux >/dev/null 2>&1 && tmux -V"); err != nil {
			events.Warnf(r, "Couldn't install tmux: %v", err)
			return
		}
	}
	if !strings.Contains(out, "has-conf") {
		_, _ = sshx.RunInput(ctx, t, "cat > ~/.tmux.conf", strings.NewReader(bootstrap.TmuxConf))
	}
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
