package engine

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"skybuild/internal/config"
	"skybuild/internal/events"
	"skybuild/internal/model"
	"skybuild/internal/paths"
	"skybuild/internal/sshx"
	"skybuild/internal/syncer"
	"skybuild/internal/tailnet"
)

// Candidate is a machine sky can see but hasn't been added yet: an entry in your ssh config
// or a device on your tailnet.
type Candidate struct {
	Name        string `json:"name"`   // suggested machine name
	Source      string `json:"source"` // ssh | tailscale
	Alias       string `json:"alias,omitempty"`
	Host        string `json:"host"`
	User        string `json:"user,omitempty"`
	OS          string `json:"os,omitempty"` // macOS, linux, windows
	Online      bool   `json:"online"`
	TailscaleIP string `json:"tailscaleIp,omitempty"`
	Detail      string `json:"detail"`
	OtherSync   string `json:"otherSync,omitempty"` // another tool already syncs it
}

// Spec turns a candidate into what Add needs.
func (c Candidate) Spec(name string, takeOver bool) AddSpec {
	if name == "" {
		name = c.Name
	}
	return AddSpec{Name: name, Alias: c.Alias, Host: c.Host, User: c.User, TakeOver: takeOver, TailscaleIP: c.TailscaleIP}
}

// Hosts in an ssh config that are services, not machines to work on.
var notMachines = regexp.MustCompile(`(?i)(^|\.)(github\.com|gitlab\.com|bitbucket\.org|ssh\.dev\.azure\.com|vs-ssh\.visualstudio\.com|heroku\.com|huggingface\.co)$`)

var nameChars = regexp.MustCompile(`[^a-z0-9-]+`)

// suggestName makes a valid, unused machine name from a host or alias.
func suggestName(raw string, taken map[string]bool) string {
	n := strings.ToLower(raw)
	if i := strings.Index(n, "."); i > 0 {
		n = n[:i]
	}
	n = strings.Trim(nameChars.ReplaceAllString(n, "-"), "-")
	if n == "" || n[0] >= '0' && n[0] <= '9' {
		n = "machine-" + n
	}
	n = strings.Trim(n, "-")
	if len(n) > 30 {
		n = strings.Trim(n[:30], "-")
	}
	base := n
	for i := 2; taken[n] || config.ValidName(n) != nil; i++ {
		n = fmt.Sprintf("%s-%d", base, i)
		if i > 50 {
			break
		}
	}
	taken[n] = true
	return n
}

// Discover lists machines that could be added with one step: entries in your ssh config and
// devices on your tailnet that aren't sky machines yet.
func (e *Engine) Discover(ctx context.Context) []Candidate {
	have, _ := e.Machines()
	taken := map[string]bool{}
	known := map[string]bool{} // hosts, aliases and tailnet addresses already added
	for _, m := range have {
		taken[m.Name] = true
		for _, k := range []string{m.Host, m.SSHAlias, m.TailscaleIP, m.PublicIP, m.Name} {
			if k != "" {
				known[strings.ToLower(k)] = true
			}
		}
	}
	var out []Candidate
	var ports []string
	hostSeen := map[string]bool{}

	for _, alias := range sshx.ConfigHosts() {
		if known[strings.ToLower(alias)] {
			continue
		}
		c, cancel := context.WithTimeout(ctx, 3*time.Second)
		res, err := sshx.Resolve(c, alias)
		cancel()
		if err != nil || notMachines.MatchString(res.Host) || known[strings.ToLower(res.Host)] {
			continue
		}
		hostSeen[strings.ToLower(res.Host)] = true
		port := res.Port
		if port == 0 {
			port = 22
		}
		out = append(out, Candidate{Name: suggestName(alias, taken), Source: "ssh", Alias: alias, Host: res.Host, User: res.User,
			Detail: fmt.Sprintf("%s@%s · from your ssh config", res.User, res.Host)})
		ports = append(ports, strconv.Itoa(port))
	}
	// Is each ssh host up right now?
	var wg sync.WaitGroup
	for i := range out {
		wg.Add(1)
		go func(c *Candidate, port string) {
			defer wg.Done()
			if conn, err := net.DialTimeout("tcp", net.JoinHostPort(c.Host, port), 900*time.Millisecond); err == nil {
				conn.Close()
				c.Online = true
			}
			if o := otherSync(ctx, &model.Machine{Host: c.Host}, c.Alias); o != nil {
				c.OtherSync = o.Name
			}
		}(&out[i], ports[i])
	}
	wg.Wait()

	if st, err := tailnet.Local(ctx); err == nil && st.BackendState == "Running" {
		var peers []Candidate
		for _, p := range st.Peer {
			ip := p.IPv4()
			osName := p.OS
			if ip == "" || known[ip] || strings.EqualFold(osName, "iOS") || strings.EqualFold(osName, "android") || strings.EqualFold(osName, "tvOS") {
				continue
			}
			short := strings.SplitN(p.Name(), ".", 2)[0]
			if short == "" {
				short = p.HostName
			}
			if known[strings.ToLower(short)] || hostSeen[strings.ToLower(p.Name())] {
				continue
			}
			state := "online"
			if !p.Online {
				state = "offline"
			}
			peers = append(peers, Candidate{Name: suggestName(short, taken), Source: "tailscale", Host: ip, TailscaleIP: ip, OS: osName,
				Online: p.Online, Detail: fmt.Sprintf("%s · on your tailnet · %s", p.Name(), state)})
		}
		sort.Slice(peers, func(i, j int) bool {
			if peers[i].Online != peers[j].Online {
				return peers[i].Online
			}
			return peers[i].Name < peers[j].Name
		})
		out = append(out, peers...)
	}
	return out
}

// other is another tool on this computer that already keeps a machine in sync.
type other struct {
	Name  string
	Undo  string
	plist string
	label string
}

// otherSync finds claude-sync-mini (a launchd job that syncs the ssh host `macmini`) when the
// machine being added is that host. Two tools writing the same login files would fight.
func otherSync(ctx context.Context, m *model.Machine, alias string) *other {
	if runtime.GOOS != "darwin" {
		return nil
	}
	matches, _ := filepath.Glob(filepath.Join(paths.Home(), "Library", "LaunchAgents", "*claude-sync-mini*.plist"))
	if len(matches) == 0 {
		return nil
	}
	same := alias == "macmini"
	if !same {
		c, cancel := context.WithTimeout(ctx, 3*time.Second)
		res, err := sshx.Resolve(c, "macmini")
		cancel()
		same = err == nil && res.Host != "macmini" && m.Host != "" && strings.EqualFold(res.Host, m.Host)
	}
	if !same {
		return nil
	}
	plist := matches[0]
	label := strings.TrimSuffix(filepath.Base(plist), ".plist")
	return &other{Name: "claude-sync-mini", plist: plist, label: label,
		Undo: fmt.Sprintf("mv %s.off %s && launchctl bootstrap gui/$(id -u) %s", paths.Tilde(plist), paths.Tilde(plist), paths.Tilde(plist))}
}

// turnOff stops the job now and keeps it from loading at the next login.
func (o *other) turnOff() error {
	_ = exec.Command("launchctl", "bootout", "gui/"+strconv.Itoa(os.Getuid())+"/"+o.label).Run()
	return os.Rename(o.plist, o.plist+".off")
}

// OtherSync names another tool that already syncs a machine ("" when there is none).
func (e *Engine) OtherSync(ctx context.Context, name string) string {
	m, err := e.Machine(name)
	if err != nil {
		return ""
	}
	if o := otherSync(ctx, m, m.SSHAlias); o != nil {
		return o.Name
	}
	return ""
}

// TakeOver makes sky the one tool that syncs a machine: it turns the other tool off, turns
// sky's sync items on and runs a sync.
func (e *Engine) TakeOver(ctx context.Context, name string, r events.Reporter) error {
	m, err := e.Machine(name)
	if err != nil {
		return err
	}
	if o := otherSync(ctx, m, m.SSHAlias); o != nil {
		events.Stepf(r, "Turning off %s", o.Name)
		if err := o.turnOff(); err != nil {
			return fmt.Errorf("couldn't turn off %s: %w", o.Name, err)
		}
		events.Infof(r, "To bring it back: %s", o.Undo)
	}
	if err := e.SetSyncItems(name, syncer.DefaultItems()); err != nil {
		return err
	}
	if m, err = e.Machine(name); err != nil {
		return err
	}
	events.Stepf(r, "Syncing your setup to %s", name)
	res := e.SyncMachine(ctx, m, syncer.Options{Interactive: true}, r)
	if len(res.Errors) > 0 {
		return fmt.Errorf("sync had %d problem(s); `sky sync %s` retries", len(res.Errors), name)
	}
	events.Donef(r, "sky keeps %s in sync from now on", name)
	return nil
}
