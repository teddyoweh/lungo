package sshx

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"skybuild/internal/model"
	"skybuild/internal/paths"
)

const includeMarker = "# skybuild: `ssh <machine>` for every sky machine"

// Route is where to connect for one machine: a host and, for a userspace tailnet, a proxy.
type Route struct {
	Host  string
	Proxy string
}

// WriteConfig regenerates ~/.skybuild/ssh_config from the machines and makes sure
// ~/.ssh/config includes it. route picks the host (and proxy) for each machine.
func WriteConfig(machines []*model.Machine, route func(*model.Machine) Route) error {
	if err := paths.Ensure(); err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString("# Written by sky. Edits are overwritten; change machines with `sky` instead.\n\n")
	for _, m := range machines {
		if m.SSHAlias != "" {
			continue // already in the user's own ssh config
		}
		rt := route(m)
		host := rt.Host
		if host == "" {
			continue
		}
		key := m.KeyPath
		if key == "" {
			key = paths.DefaultKey()
		}
		fmt.Fprintf(&b, "Host %s\n", m.Name)
		fmt.Fprintf(&b, "  HostName %s\n", host)
		fmt.Fprintf(&b, "  User %s\n", m.User)
		if rt.Proxy != "" {
			fmt.Fprintf(&b, "  ProxyCommand %s\n", rt.Proxy)
		}
		if m.SSHPort() != 22 {
			fmt.Fprintf(&b, "  Port %d\n", m.SSHPort())
		}
		if key != NoKey {
			fmt.Fprintf(&b, "  IdentityFile %s\n", cfgPath(paths.Expand(key)))
			b.WriteString("  IdentitiesOnly yes\n")
		}
		fmt.Fprintf(&b, "  UserKnownHostsFile %s\n", cfgPath(filepath.Join(paths.KnownHosts(), m.Name)))
		b.WriteString("  StrictHostKeyChecking accept-new\n")
		b.WriteString("  ServerAliveInterval 30\n  ServerAliveCountMax 4\n")
		if runtime.GOOS != "windows" {
			b.WriteString("  ControlMaster auto\n")
			fmt.Fprintf(&b, "  ControlPath %s\n", cfgPath(filepath.Join(paths.ControlDir(), "%C")))
			b.WriteString("  ControlPersist 10m\n")
		}
		b.WriteString("\n")
	}
	if err := os.WriteFile(paths.SSHConfig(), []byte(b.String()), 0o600); err != nil {
		return err
	}
	return ensureInclude()
}

// ensureInclude puts `Include ~/.skybuild/ssh_config` at the top of ~/.ssh/config.
// It has to come first: ssh uses the first value it finds for each option. A sky home kept
// apart from the user's (SKYBUILD_HOME elsewhere: a test, a staged demo) stays out of their
// ssh config: its include would stay behind, ahead of the real one.
func ensureInclude() error {
	if r := os.Getenv("SKYBUILD_HOME"); r != "" && filepath.Clean(r) != filepath.Join(paths.Home(), ".skybuild") {
		return nil
	}
	user := paths.UserSSHConfig()
	cur, err := os.ReadFile(user)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	line := "Include " + cfgPath(paths.SSHConfig())
	if strings.Contains(string(cur), line) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(user), 0o700); err != nil {
		return err
	}
	out := includeMarker + "\n" + line + "\n\n" + string(cur)
	return os.WriteFile(user, []byte(out), 0o600)
}

// RemoveInclude takes sky's line back out of ~/.ssh/config.
func RemoveInclude() error {
	user := paths.UserSSHConfig()
	cur, err := os.ReadFile(user)
	if err != nil {
		return nil
	}
	s := strings.Replace(string(cur), includeMarker+"\n", "", 1)
	s = strings.Replace(s, "Include "+cfgPath(paths.SSHConfig())+"\n\n", "", 1)
	s = strings.Replace(s, "Include "+cfgPath(paths.SSHConfig())+"\n", "", 1)
	return os.WriteFile(user, []byte(s), 0o600)
}

// cfgPath writes a path the way ssh_config wants it: ~/ when under home, forward slashes,
// quoted if it has spaces.
func cfgPath(p string) string {
	p = filepath.ToSlash(p)
	home := filepath.ToSlash(paths.Home())
	if strings.HasPrefix(p, home+"/") {
		p = "~/" + p[len(home)+1:]
	}
	if strings.ContainsAny(p, " \t") {
		return `"` + p + `"`
	}
	return p
}
