// Package paths knows where sky keeps its files: ~/.skybuild on every OS.
package paths

import (
	"os"
	"path/filepath"
	"strings"
)

// Home is the user's home directory.
func Home() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return h
}

// Root is sky's directory. SKYBUILD_HOME overrides it (tests, multiple setups).
func Root() string {
	if v := os.Getenv("SKYBUILD_HOME"); v != "" {
		return v
	}
	return filepath.Join(Home(), ".skybuild")
}

func Config() string        { return filepath.Join(Root(), "config.json") }
func Keys() string          { return filepath.Join(Root(), "keys") }
func DefaultKey() string    { return filepath.Join(Keys(), "sky_ed25519") }
func KnownHosts() string    { return filepath.Join(Root(), "known_hosts") }
func SSHConfig() string     { return filepath.Join(Root(), "ssh_config") }
func ControlDir() string    { return filepath.Join(Root(), "cm") }
func State() string         { return filepath.Join(Root(), "state") }
func Logs() string          { return filepath.Join(Root(), "logs") }
func Stage() string         { return filepath.Join(Root(), "stage") }
func UserSSHConfig() string { return filepath.Join(Home(), ".ssh", "config") }

// Ensure creates sky's directories with private permissions.
func Ensure() error {
	for _, d := range []string{Root(), Keys(), KnownHosts(), ControlDir(), State(), Logs(), Stage()} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	return nil
}

// Expand turns a leading ~ into the home directory.
func Expand(p string) string {
	if p == "~" {
		return Home()
	}
	if strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		return filepath.Join(Home(), p[2:])
	}
	return p
}

// Tilde shortens a path under home to ~/… for display.
func Tilde(p string) string {
	h := Home()
	if p == h {
		return "~"
	}
	if strings.HasPrefix(p, h+string(filepath.Separator)) {
		return "~" + string(filepath.Separator) + p[len(h)+1:]
	}
	return p
}
