package syncer

import (
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"skybuild/internal/config"
	"skybuild/internal/model"
)

// sh runs the watchdog's own functions followed by a call, the way the machine would.
func sh(t *testing.T, call string) (string, int) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX shell")
	}
	c := exec.Command("sh", "-c", idleFuncs+"\n"+call)
	out, err := c.CombinedOutput()
	code := 0
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("sh: %v", err)
		}
		code = ee.ExitCode()
	}
	return strings.TrimSpace(string(out)), code
}

func TestIdleDecide(t *testing.T) {
	const now = 1_000_000
	for _, c := range []struct {
		name                string
		stamp, boot, limit  int
		dry, reason, expect string
	}{
		{"something is going on", now - 9000, now - 90000, 120, "0", "tmux client attached", "0 0 1000000 active: tmux client attached"},
		{"active wins even past the limit in a dry run", now - 9000, now - 90000, 120, "1", "ssh login", "0 0 1000000 active: ssh login"},
		{"idle, under the limit", now - 30*60, now - 90000, 120, "0", "", "0 30 998200 idle 30m of 120m"},
		{"one minute short", now - 119*60 - 59, now - 90000, 120, "0", "", "0 119 992801 idle 119m of 120m"},
		{"idle for exactly the limit", now - 120*60, now - 90000, 120, "0", "", "1 120 992800 idle 120m, limit 120m: stopping"},
		{"long idle", now - 500*60, now - 90000, 60, "0", "", "1 500 970000 idle 500m, limit 60m: stopping"},
		{"dry run only says so", now - 500*60, now - 90000, 60, "1", "", "0 500 970000 idle 500m, limit 60m: would stop (dry run)"},
		// The stamp is from before the machine was stopped; it booted four minutes ago.
		{"just started", now - 86400, now - 240, 60, "0", "", "0 4 999760 idle 4m of 60m"},
		{"started, then idle for the limit", now - 86400, now - 3600, 60, "0", "", "1 60 996400 idle 60m, limit 60m: stopping"},
		{"no stamp yet", 0, now - 600, 60, "0", "", "0 10 999400 idle 10m of 60m"},
	} {
		call := "decide " + strconv.Itoa(now) + " " + strconv.Itoa(c.stamp) + " " + strconv.Itoa(c.boot) + " " + strconv.Itoa(c.limit) + " " + c.dry + " '" + c.reason + "'"
		got, code := sh(t, call)
		if code != 0 || got != c.expect {
			t.Errorf("%s:\n got  %q (exit %d)\n want %q", c.name, got, code, c.expect)
		}
	}
}

func TestIdleLoadBusy(t *testing.T) {
	for _, c := range []struct {
		load  string
		cores int
		busy  bool
	}{
		{"0.00", 2, false},
		{"0.15", 2, false}, // the bar itself is not above the bar
		{"0.16", 2, true},
		{"0.10", 1, false},
		{"0.19", 4, false}, // 5% of four cores is 0.2
		{"0.21", 4, true},
		{"0.39", 8, false},
		{"0.45", 16, false}, // 5% of sixteen would be 0.8, but the bar stops at 0.5
		{"0.51", 16, true},
		{"1.00", 64, true}, // one busy core counts however large the machine is
		{"0.40", 64, false},
	} {
		_, code := sh(t, "load_busy "+c.load+" "+strconv.Itoa(c.cores))
		if (code == 0) != c.busy {
			t.Errorf("load %s on %d cores: busy=%v, want %v", c.load, c.cores, code == 0, c.busy)
		}
	}
}

// The script is installed through a here-document, so it must parse and must never contain
// the line that ends one.
func TestIdleScripts(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX shell")
	}
	install := idleInstall(config.IdleStop{Minutes: 120})
	for name, script := range map[string]string{"watchdog": IdleWatchdog, "install": install, "remove": idleRemove} {
		c := exec.Command("sh", "-n")
		c.Stdin = strings.NewReader(script)
		if out, err := c.CombinedOutput(); err != nil {
			t.Errorf("%s does not parse: %v\n%s", name, err, out)
		}
	}
	for _, body := range []string{IdleWatchdog, idleService, idleTimer, idleConf(config.IdleStop{Minutes: 5, DryRun: true})} {
		if strings.Contains(body, "SKY_IDLE_EOF") {
			t.Error("an installed file contains the here-document's end marker")
		}
		if !strings.HasSuffix(body, "\n") {
			t.Error("an installed file doesn't end with a newline, so the end marker would not be on its own line")
		}
	}
	if !strings.Contains(install, "LIMIT_MIN=120\nDRY_RUN=0\n") {
		t.Errorf("the setting is missing from the install script:\n%s", install)
	}
	if strings.Contains(IdleWatchdog, "kill-server") || strings.Contains(IdleWatchdog, "kill-session") {
		t.Error("the watchdog must only ask tmux questions")
	}
}

func TestIdleDigest(t *testing.T) {
	a := idleDigest(config.IdleStop{Minutes: 120})
	if a != idleDigest(config.IdleStop{Minutes: 120}) {
		t.Error("the digest is not stable")
	}
	if a == idleDigest(config.IdleStop{Minutes: 60}) || a == idleDigest(config.IdleStop{Minutes: 120, DryRun: true}) {
		t.Error("a different setting must reinstall")
	}
}

func TestIdleOffered(t *testing.T) {
	for _, c := range []struct {
		m    model.Machine
		want bool
	}{
		{model.Machine{Provider: model.ProviderGCP, OS: "linux"}, true},
		{model.Machine{Provider: model.ProviderAWS}, true},
		{model.Machine{Provider: model.ProviderAzure, OS: "linux"}, false},
		{model.Machine{Provider: model.ProviderSSH, OS: "darwin"}, false},
		{model.Machine{Provider: model.ProviderSSH, OS: "linux"}, false},
	} {
		ok, why := IdleOffered(&c.m)
		if ok != c.want || (!ok && why == "") {
			t.Errorf("%s/%s: offered=%v why=%q", c.m.Provider, c.m.OS, ok, why)
		}
	}
}

func TestIdleLimit(t *testing.T) {
	for minutes, want := range map[int]string{5: "5m", 60: "1h", 90: "1h 30m", 120: "2h", 480: "8h"} {
		if got := IdleLimit(minutes); got != want {
			t.Errorf("%d: %q, want %q", minutes, got, want)
		}
	}
}
