// Package engine is everything sky can do, as one API. The CLI and the desktop app are both
// thin layers over it, so a machine created in one shows up in the other.
package engine

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"skybuild/internal/config"
	"skybuild/internal/events"
	"skybuild/internal/model"
	"skybuild/internal/osx"
	"skybuild/internal/paths"
	"skybuild/internal/provider"
	"skybuild/internal/provider/aws"
	"skybuild/internal/provider/azure"
	"skybuild/internal/provider/gcp"
	"skybuild/internal/sshx"
	"skybuild/internal/tailnet"
)

// Engine holds the providers and a little cached state.
type Engine struct {
	providers []provider.Provider

	tsMu        sync.Mutex
	tsUp        bool
	tsUserspace bool
	tsChecked   time.Time
	tsAsking    bool       // a renewal is under way
	tsFirst     sync.Mutex // held while the very first answer is fetched

	tunnels *tunnels
}

// New returns an engine with every provider.
func New() *Engine {
	return &Engine{
		providers: []provider.Provider{gcp.New(), aws.New(), azure.New()},
		tunnels:   newTunnels(),
	}
}

// Providers lists the cloud adapters.
func (e *Engine) Providers() []provider.Provider { return e.providers }

// Provider finds one by ID.
func (e *Engine) Provider(id string) (provider.Provider, error) {
	for _, p := range e.providers {
		if p.ID() == id {
			return p, nil
		}
	}
	return nil, fmt.Errorf("unknown provider %q (have: gcp, aws, azure)", id)
}

// ProviderStatuses checks every provider in parallel.
func (e *Engine) ProviderStatuses(ctx context.Context) []model.ProviderStatus {
	out := make([]model.ProviderStatus, len(e.providers))
	var wg sync.WaitGroup
	for i, p := range e.providers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i] = p.Status(ctx)
		}()
	}
	wg.Wait()
	return out
}

// Login signs in to a provider's CLI.
func (e *Engine) Login(ctx context.Context, id string, r events.Reporter) error {
	p, err := e.Provider(id)
	if err != nil {
		return err
	}
	return p.Login(ctx, r)
}

// Machines returns the saved machines.
func (e *Engine) Machines() ([]*model.Machine, error) {
	c, err := config.Load()
	if err != nil {
		return nil, err
	}
	return c.Machines, nil
}

// Machine finds a saved machine.
func (e *Engine) Machine(name string) (*model.Machine, error) {
	c, err := config.Load()
	if err != nil {
		return nil, err
	}
	if m := c.Machine(name); m != nil {
		return m, nil
	}
	var names []string
	for _, m := range c.Machines {
		names = append(names, m.Name)
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("no machine named %q (you have none yet: `sky new`)", name)
	}
	return nil, fmt.Errorf("no machine named %q (have: %s)", name, strings.Join(names, ", "))
}

// Settings returns the current settings.
func (e *Engine) Settings() (config.Settings, error) {
	c, err := config.Load()
	if err != nil {
		return config.Settings{}, err
	}
	return c.Settings, nil
}

// UpdateSettings changes settings under the config lock.
func (e *Engine) UpdateSettings(fn func(*config.Settings)) error {
	return config.Update(func(c *config.Config) error {
		fn(&c.Settings)
		return nil
	})
}

// save stores a machine and refreshes the SSH config.
func (e *Engine) save(m *model.Machine) error {
	var all []*model.Machine
	err := config.Update(func(c *config.Config) error {
		c.Put(m)
		all = c.Machines
		return nil
	})
	if err != nil {
		return err
	}
	return e.writeSSH(all)
}

func (e *Engine) writeSSH(all []*model.Machine) error {
	return sshx.WriteConfig(all, e.route)
}

// route picks how to reach a machine: its Tailscale address when this computer is on the
// tailnet (through `tailscale nc` if Tailscale runs in userspace mode, where the OS has no
// route to 100.x), otherwise its public address.
func (e *Engine) route(m *model.Machine) sshx.Route {
	up := e.tailscaleUp()
	host := m.Address(up)
	if up && host == m.TailscaleIP && host != "" {
		e.tsMu.Lock()
		userspace := e.tsUserspace
		e.tsMu.Unlock()
		if userspace {
			return sshx.Route{Host: host, Proxy: osx.Which("tailscale") + " nc %h %p"}
		}
	}
	return sshx.Route{Host: host}
}

// SyncSSHConfig rewrites ~/.ssh/config entries from the saved machines.
func (e *Engine) SyncSSHConfig() error {
	all, err := e.Machines()
	if err != nil {
		return err
	}
	return e.writeSSH(all)
}

// tailscaleUp says whether this computer is on the tailnet. The answer is kept for 30
// seconds, and an old one is renewed in the background: asking Tailscale can take seconds,
// and every connection to a machine (opening a pane, every poll) asks. Only the very first
// time, with nothing known yet, waits for the answer.
func (e *Engine) tailscaleUp() bool {
	e.tsMu.Lock()
	known, old := !e.tsChecked.IsZero(), time.Since(e.tsChecked) > 30*time.Second
	if known && old && !e.tsAsking {
		e.tsAsking = true
		go func() {
			e.askTailscale()
			e.tsMu.Lock()
			e.tsAsking = false
			e.tsMu.Unlock()
		}()
	}
	up := e.tsUp
	e.tsMu.Unlock()
	if !known {
		e.tsFirst.Lock() // one first ask, however many connections wait for it
		e.tsMu.Lock()
		asked := !e.tsChecked.IsZero()
		e.tsMu.Unlock()
		if !asked {
			e.askTailscale()
		}
		e.tsFirst.Unlock()
		e.tsMu.Lock()
		up = e.tsUp
		e.tsMu.Unlock()
	}
	return up
}

// askTailscale asks Tailscale for this computer's state, outside any lock.
func (e *Engine) askTailscale() {
	s, err := tailnet.Local(context.Background())
	e.tsMu.Lock()
	e.tsUp = err == nil && s.BackendState == "Running"
	e.tsUserspace = err == nil && !s.TUN
	e.tsChecked = time.Now()
	e.tsMu.Unlock()
}

// TailscaleStatus reports this computer's Tailscale state for the UI.
func (e *Engine) TailscaleStatus(ctx context.Context) TailscaleInfo {
	info := TailscaleInfo{Installed: osx.Has("tailscale")}
	if s, err := tailnet.Local(ctx); err == nil {
		info.State = s.BackendState
		info.Connected = s.BackendState == "Running"
		if s.Self != nil {
			info.Self = s.Self.Name()
			info.IP = s.Self.IPv4()
		}
		if s.CurrentTailnet != nil {
			info.Tailnet = s.CurrentTailnet.Name
		}
	}
	info.HasAuthKey = hasAuthKey()
	return info
}

// TailscaleInfo is this computer's tailnet state.
type TailscaleInfo struct {
	Installed  bool   `json:"installed"`
	Connected  bool   `json:"connected"`
	State      string `json:"state"`
	Self       string `json:"self"`
	IP         string `json:"ip"`
	Tailnet    string `json:"tailnet"`
	HasAuthKey bool   `json:"hasAuthKey"`
}

// Ready returns an error that says what to do when a machine can't take connections.
func (e *Engine) Ready(m *model.Machine) error {
	switch m.Status {
	case model.StatusStopped:
		return fmt.Errorf("%s is stopped; start it with `sky start %s`", m.Name, m.Name)
	case model.StatusMissing:
		return fmt.Errorf("%s no longer exists in %s; remove it with `sky rm %s`", m.Name, m.Provider, m.Name)
	}
	return nil
}

// Target is how to reach a machine right now.
func (e *Engine) Target(m *model.Machine) sshx.Target {
	rt := e.route(m)
	t := sshx.For(m, rt.Host)
	t.Proxy = rt.Proxy
	return t
}

// SSHCommands are the two ways to connect, for copy buttons: the short alias and the
// standalone command that works without sky's ssh config.
func (e *Engine) SSHCommands(m *model.Machine) (short, full string) {
	if m.SSHAlias != "" {
		return "ssh " + m.SSHAlias, "ssh " + m.SSHAlias
	}
	t := e.Target(m)
	full = "ssh"
	if t.KeyPath != "" {
		full += " -i " + quoteIfSpace(paths.Tilde(t.KeyPath))
	}
	if t.Port != 22 {
		full += fmt.Sprintf(" -p %d", t.Port)
	}
	if t.Proxy != "" {
		full += " -o ProxyCommand='" + t.Proxy + "'"
	}
	return "ssh " + m.Name, full + " " + t.Dest()
}

// Connect opens a terminal window with an SSH session to the machine.
func (e *Engine) Connect(name string) error {
	m, err := e.Machine(name)
	if err != nil {
		return err
	}
	if err := e.SyncSSHConfig(); err != nil {
		return err
	}
	s, _ := e.Settings()
	short, _ := e.SSHCommands(m)
	return osx.OpenTerminal(s.Terminal, short)
}

// Refresh asks each cloud for current status and addresses (and pings SSH machines),
// saves the result and returns the machines. With no names it refreshes all of them.
func (e *Engine) Refresh(ctx context.Context, names ...string) ([]*model.Machine, error) {
	all, err := e.Machines()
	if err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	var wg sync.WaitGroup
	for _, m := range all {
		if len(want) > 0 && !want[m.Name] {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, cancel := context.WithTimeout(ctx, 40*time.Second)
			defer cancel()
			if m.IsCloud() {
				p, err := e.Provider(m.Provider)
				if err == nil {
					if err := p.Refresh(c, m); err != nil {
						m.Status = model.StatusUnknown
					}
				}
				return
			}
			if sshx.Reachable(c, e.Target(m)) {
				m.Status = model.StatusRunning
			} else {
				m.Status = model.StatusUnreachable
			}
		}()
	}
	wg.Wait()
	err = config.Update(func(c *config.Config) error {
		for _, m := range all {
			if cur := c.Machine(m.Name); cur != nil {
				cur.Status, cur.PublicIP, cur.Size = m.Status, m.PublicIP, m.Size
			}
		}
		all = c.Machines
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
	return all, e.writeSSH(all)
}

func (e *Engine) cloud(name string) (*model.Machine, provider.Provider, error) {
	m, err := e.Machine(name)
	if err != nil {
		return nil, nil, err
	}
	if !m.IsCloud() {
		return m, nil, fmt.Errorf("%s is a machine you added by address; sky can't start, stop or resize it", name)
	}
	p, err := e.Provider(m.Provider)
	return m, p, err
}

// Start boots a stopped machine and waits until SSH answers.
func (e *Engine) Start(ctx context.Context, name string, r events.Reporter) error {
	m, p, err := e.cloud(name)
	if err != nil {
		return err
	}
	events.Stepf(r, "Starting %s", name)
	if err := p.Start(ctx, m); err != nil {
		return err
	}
	sshx.CloseMaster(name)
	if err := e.save(m); err != nil {
		return err
	}
	events.Stepf(r, "Waiting for SSH")
	wctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	if err := sshx.WaitReady(wctx, e.Target(m), nil); err != nil {
		return err
	}
	events.Donef(r, "%s is running", name)
	return nil
}

// Stop shuts a machine down. Its volume stays; only disk storage is billed while stopped.
func (e *Engine) Stop(ctx context.Context, name string, r events.Reporter) error {
	m, p, err := e.cloud(name)
	if err != nil {
		return err
	}
	events.Stepf(r, "Stopping %s", name)
	if err := p.Stop(ctx, m); err != nil {
		return err
	}
	sshx.CloseMaster(name)
	if err := e.save(m); err != nil {
		return err
	}
	events.Donef(r, "%s stopped", name)
	return nil
}

// Restart stops and starts a machine.
func (e *Engine) Restart(ctx context.Context, name string, r events.Reporter) error {
	if err := e.Stop(ctx, name, r); err != nil {
		return err
	}
	return e.Start(ctx, name, r)
}

// Resize changes a machine's type (it restarts if it was running).
func (e *Engine) Resize(ctx context.Context, name, size string, r events.Reporter) error {
	m, p, err := e.cloud(name)
	if err != nil {
		return err
	}
	if err := p.Resize(ctx, m, size, r); err != nil {
		return err
	}
	sshx.CloseMaster(name)
	if err := e.save(m); err != nil {
		return err
	}
	if m.Status == model.StatusRunning || m.Status == model.StatusStarting {
		events.Stepf(r, "Waiting for SSH")
		wctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
		defer cancel()
		if err := sshx.WaitReady(wctx, e.Target(m), nil); err != nil {
			return err
		}
	}
	events.Donef(r, "%s is now %s", name, size)
	return nil
}

// GrowDisk enlarges a machine's data volume and its filesystem. Volumes only grow.
func (e *Engine) GrowDisk(ctx context.Context, name string, gb int, r events.Reporter) error {
	m, p, err := e.cloud(name)
	if err != nil {
		return err
	}
	if gb <= m.DiskGB {
		return fmt.Errorf("volumes can only grow: %s is already %d GB", name, m.DiskGB)
	}
	events.Stepf(r, "Growing %s to %d GB", m.VolumeID, gb)
	if err := p.GrowDisk(ctx, m, gb); err != nil {
		return err
	}
	if err := e.save(m); err != nil {
		return err
	}
	if m.Status == model.StatusRunning {
		events.Stepf(r, "Growing the filesystem")
		time.Sleep(3 * time.Second) // the kernel sees the new size a moment later
		out, err := sshx.Run(ctx, e.Target(m), `sudo sh -c 'for d in /sys/class/block/*/device/rescan; do echo 1 > "$d"; done 2>/dev/null; resize2fs "$(findmnt -no SOURCE /home)"' 2>&1 && df -h /home | tail -1`)
		if err != nil {
			return err
		}
		events.Infof(r, "%s", strings.TrimSpace(out))
	}
	events.Donef(r, "Volume is %d GB", gb)
	return nil
}

// Delete removes a machine. keepDisk leaves the volume so the same name can be recreated
// later with its /home intact.
func (e *Engine) Delete(ctx context.Context, name string, keepDisk bool, r events.Reporter) error {
	m, err := e.Machine(name)
	if err != nil {
		return err
	}
	if m.IsCloud() {
		p, err := e.Provider(m.Provider)
		if err != nil {
			return err
		}
		if m.TailscaleIP != "" && m.Status == model.StatusRunning {
			events.Stepf(r, "Removing %s from the tailnet", name)
			tailnet.Leave(ctx, e.Target(m))
		}
		if err := p.Delete(ctx, m, keepDisk, r); err != nil {
			return err
		}
	}
	sshx.ForgetHost(name)
	forgetSyncState(name)
	var all []*model.Machine
	if err := config.Update(func(c *config.Config) error {
		c.Remove(name)
		all = c.Machines
		return nil
	}); err != nil {
		return err
	}
	if err := e.writeSSH(all); err != nil {
		return err
	}
	events.Donef(r, "%s removed", name)
	return nil
}

func quoteIfSpace(s string) string {
	if strings.ContainsAny(s, " \t") {
		return `"` + s + `"`
	}
	return s
}
