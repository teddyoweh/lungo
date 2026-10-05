package sshx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"skybuild/internal/model"
)

func TestWriteConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SKYBUILD_HOME", filepath.Join(home, ".skybuild"))
	os.MkdirAll(filepath.Join(home, ".ssh"), 0o700)
	os.WriteFile(filepath.Join(home, ".ssh", "config"), []byte("Host old\n  HostName 1.2.3.4\n"), 0o600)
	ms := []*model.Machine{
		{Name: "box", User: "teddy", PublicIP: "34.1.2.3", TailscaleIP: "100.64.0.9", CreatedAt: time.Now()},
		{Name: "mini", SSHAlias: "macmini"},
	}
	if err := WriteConfig(ms, func(m *model.Machine) Route { return Route{Host: m.Address(true)} }); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(home, ".skybuild", "ssh_config"))
	s := string(b)
	if !strings.Contains(s, "Host box\n  HostName 100.64.0.9\n  User teddy") || strings.Contains(s, "Host mini") {
		t.Errorf("ssh_config:\n%s", s)
	}
	u, _ := os.ReadFile(filepath.Join(home, ".ssh", "config"))
	if !strings.HasPrefix(string(u), includeMarker+"\nInclude ~/.skybuild/ssh_config\n") || !strings.Contains(string(u), "Host old") {
		t.Errorf("~/.ssh/config:\n%s", u)
	}
	// Idempotent.
	WriteConfig(ms, func(m *model.Machine) Route { return Route{Host: m.Address(true)} })
	u2, _ := os.ReadFile(filepath.Join(home, ".ssh", "config"))
	if strings.Count(string(u2), "Include") != 1 {
		t.Error("Include added twice")
	}
}

func TestEnsureKey(t *testing.T) {
	t.Setenv("SKYBUILD_HOME", t.TempDir())
	p, pub, err := EnsureKey()
	if err != nil || !strings.HasPrefix(pub, "ssh-ed25519 ") {
		t.Fatalf("%v %q", err, pub)
	}
	st, _ := os.Stat(p)
	if st.Mode().Perm() != 0o600 {
		t.Errorf("key mode %v", st.Mode())
	}
	_, pub2, _ := EnsureKey()
	if pub2 != pub {
		t.Error("key regenerated")
	}
}
