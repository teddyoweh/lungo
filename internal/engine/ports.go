package engine

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"skybuild/internal/model"
	"skybuild/internal/osx"
	"skybuild/internal/sshx"
)

// Processes that listen on every machine and are never what you want to open.
var boringProcs = map[string]bool{
	"sshd": true, "systemd-resolve": true, "tailscaled": true, "containerd": true, "chronyd": true,
	"cupsd": true, "rpcbind": true, "systemd": true, "google_guest_ag": true, "google_osconfig": true,
	"rapportd": true, "ControlCe": true, "launchd": true, "sharingd": true, "mDNSRespo": true,
	"amazon-ssm-agen": true, "waagent": true, "python3-waagent": true,
}

var ssProc = regexp.MustCompile(`users:\(\("([^"]+)"`)

// Ports lists TCP ports something is listening on inside the machine: dev servers,
// databases, anything you might want to open locally.
func (e *Engine) Ports(ctx context.Context, name string) ([]model.Port, error) {
	m, err := e.Machine(name)
	if err != nil {
		return nil, err
	}
	t := e.Target(m)
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := sshx.Run(ctx, t, `if command -v ss >/dev/null; then echo SS; ss -ltnpH 2>/dev/null; else echo LSOF; lsof -nP -iTCP -sTCP:LISTEN 2>/dev/null; fi`)
	if err != nil {
		return nil, err
	}
	return parsePorts(out), nil
}

func parsePorts(out string) []model.Port {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) == 0 {
		return nil
	}
	mode := strings.TrimSpace(lines[0])
	seen := map[int]bool{}
	var ports []model.Port
	for _, line := range lines[1:] {
		f := strings.Fields(line)
		var addr, proc string
		switch mode {
		case "SS": // LISTEN 0 511 0.0.0.0:3000 0.0.0.0:* users:(("node",pid=1,fd=2))
			if len(f) < 4 {
				continue
			}
			addr = f[3]
			if mm := ssProc.FindStringSubmatch(line); mm != nil {
				proc = mm[1]
			}
		default: // node 123 me 20u IPv4 0x… 0t0 TCP *:3000 (LISTEN)
			if len(f) < 9 || f[0] == "COMMAND" {
				continue
			}
			proc, addr = f[0], f[8]
		}
		i := strings.LastIndex(addr, ":")
		if i < 0 {
			continue
		}
		port, err := strconv.Atoi(addr[i+1:])
		if err != nil || port == 22 || port == 53 || seen[port] || boringProcs[proc] {
			continue
		}
		if proc == "" && port >= 32768 {
			continue // someone else's ephemeral listener (we only see our own process names)
		}
		host := strings.Trim(addr[:i], "[]")
		if strings.HasPrefix(host, "100.") || strings.HasPrefix(host, "fd7a:") { // Tailscale's own listeners
			continue
		}
		if host == "127.0.0.53" || host == "127.0.0.54" {
			continue
		}
		seen[port] = true
		ports = append(ports, model.Port{Port: port, Address: host, Process: proc})
	}
	sort.Slice(ports, func(i, j int) bool { return ports[i].Port < ports[j].Port })
	return ports
}

// Tunnel forwards a local port to a port inside a machine.
type Tunnel struct {
	ID         string    `json:"id"`
	Machine    string    `json:"machine"`
	RemotePort int       `json:"remotePort"`
	LocalPort  int       `json:"localPort"`
	URL        string    `json:"url"`
	Started    time.Time `json:"started"`
	cmd        *exec.Cmd
	done       chan struct{}
}

// Done is closed when the tunnel's ssh process exits.
func (t *Tunnel) Done() <-chan struct{} { return t.done }

type tunnels struct {
	mu   sync.Mutex
	list map[string]*Tunnel
}

func newTunnels() *tunnels { return &tunnels{list: map[string]*Tunnel{}} }

// Forward opens localhost:<local> → <machine>:<remote>. local 0 picks the same number if
// free, else the next free one. The tunnel lives until Close or the process exits.
func (e *Engine) Forward(ctx context.Context, name string, remote, local int) (*Tunnel, error) {
	m, err := e.Machine(name)
	if err != nil {
		return nil, err
	}
	id := fmt.Sprintf("%s:%d", name, remote)
	e.tunnels.mu.Lock()
	if t, ok := e.tunnels.list[id]; ok {
		e.tunnels.mu.Unlock()
		return t, nil
	}
	e.tunnels.mu.Unlock()
	if local == 0 {
		local = freePort(remote)
	}
	t := e.Target(m)
	args := append(t.TunnelOptions(), "-N", "-o", "ExitOnForwardFailure=yes",
		"-L", fmt.Sprintf("127.0.0.1:%d:127.0.0.1:%d", local, remote), t.Dest())
	cmd := exec.Command(osx.Which("ssh"), args...)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	tun := &Tunnel{ID: id, Machine: name, RemotePort: remote, LocalPort: local,
		URL: fmt.Sprintf("http://localhost:%d", local), Started: time.Now(), cmd: cmd, done: make(chan struct{})}
	go func() {
		cmd.Wait()
		close(tun.done)
		e.tunnels.mu.Lock()
		if e.tunnels.list[id] == tun {
			delete(e.tunnels.list, id)
		}
		e.tunnels.mu.Unlock()
	}()
	// Wait until the local side accepts connections (or ssh gives up).
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-tun.done:
			return nil, fmt.Errorf("could not forward port %d on %s (is ssh working? try `sky ssh %s`)", remote, name, name)
		default:
		}
		if c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", local), 300*time.Millisecond); err == nil {
			c.Close()
			e.tunnels.mu.Lock()
			e.tunnels.list[id] = tun
			e.tunnels.mu.Unlock()
			return tun, nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	cmd.Process.Kill()
	return nil, fmt.Errorf("timed out forwarding port %d on %s", remote, name)
}

// Tunnels lists open forwards.
func (e *Engine) Tunnels() []*Tunnel {
	e.tunnels.mu.Lock()
	defer e.tunnels.mu.Unlock()
	out := make([]*Tunnel, 0, len(e.tunnels.list))
	for _, t := range e.tunnels.list {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// CloseTunnel stops a forward by ID ("machine:port").
func (e *Engine) CloseTunnel(id string) {
	e.tunnels.mu.Lock()
	t := e.tunnels.list[id]
	delete(e.tunnels.list, id)
	e.tunnels.mu.Unlock()
	if t != nil && t.cmd.Process != nil {
		t.cmd.Process.Kill()
	}
}

// CloseAllTunnels stops every forward (on exit).
func (e *Engine) CloseAllTunnels() {
	for _, t := range e.Tunnels() {
		e.CloseTunnel(t.ID)
	}
}

func freePort(want int) int {
	for p := want; p < want+200 && p < 65536; p++ {
		if l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p)); err == nil {
			l.Close()
			return p
		}
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return want
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// runOn runs a command on a machine.
func runOn(ctx context.Context, e *Engine, m *model.Machine, cmd string) (string, error) {
	return sshx.Run(ctx, e.Target(m), cmd)
}

// Exec runs a shell command on a machine and returns its output.
func (e *Engine) Exec(ctx context.Context, name, cmd string) (string, error) {
	m, err := e.Machine(name)
	if err != nil {
		return "", err
	}
	return runOn(ctx, e, m, cmd)
}
