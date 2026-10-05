package engine

import (
	"testing"
	"time"

	"skybuild/internal/config"
	"skybuild/internal/model"
)

func TestParseIdle(t *testing.T) {
	for in, want := range map[string]int{"off": 0, "Off": 0, "0": 0, "1h": 60, "2h": 120, "4h": 240, "8h": 480, "90m": 90, "1h30m": 90, " 5m ": 5} {
		got, err := ParseIdle(in)
		if err != nil || got != want {
			t.Errorf("%q: %d %v, want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "soon", "2", "4m", "30s", "-1h", "200h"} {
		if got, err := ParseIdle(bad); err == nil {
			t.Errorf("%q accepted as %d minutes", bad, got)
		}
	}
}

func TestStopNote(t *testing.T) {
	loc := time.FixedZone("EDT", -4*3600)
	now := time.Date(2026, 10, 4, 8, 12, 0, 0, loc)
	stopped := time.Date(2026, 10, 4, 7, 10, 30, 0, time.UTC) // 03:10 where the user is
	for _, c := range []struct {
		st   stopState
		want string
	}{
		{stopState{Checked: true, BySelf: true, Minutes: 120, StoppedAt: stopped}, "Stopped after 2h idle at 03:10"},
		{stopState{Checked: true, BySelf: true, Minutes: 90, StoppedAt: stopped.Add(-48 * time.Hour)}, "Stopped after 1h 30m idle on Fri at 03:10"},
		{stopState{Checked: true, BySelf: true, Minutes: 60, StoppedAt: stopped.Add(-30 * 24 * time.Hour)}, "Stopped after 1h idle on Sep 4 at 03:10"},
		{stopState{Checked: true, BySelf: true, Minutes: 120}, "Stopped after 2h idle"}, // the cloud gave no time
		// Stopped from outside (sky stop, the cloud console): nothing to say.
		{stopState{Checked: true, BySelf: false, Minutes: 120, StoppedAt: stopped}, ""},
		// It shut itself down, but stop-when-idle was off: not the watchdog's doing.
		{stopState{Checked: true, BySelf: true, Minutes: 0, StoppedAt: stopped}, ""},
	} {
		if got := stopNote(c.st, now); got != c.want {
			t.Errorf("%+v:\n got  %q\n want %q", c.st, got, c.want)
		}
	}
}

func TestIdleInfo(t *testing.T) {
	now := time.Now()
	on := config.IdleStop{Minutes: 120}
	if got := idleInfo(&model.Machine{Name: "box", Provider: model.ProviderGCP, OS: "linux"}, on, now); !got.Offered || got.Minutes != 120 || got.Why != "" {
		t.Errorf("gcp: %+v", got)
	}
	// A setting left over for a machine it can't apply to reads as off.
	if got := idleInfo(&model.Machine{Name: "mini", Provider: model.ProviderSSH, OS: "darwin"}, on, now); got.Offered || got.Minutes != 0 || got.Why == "" {
		t.Errorf("your own machine: %+v", got)
	}
	if got := idleInfo(&model.Machine{Name: "az", Provider: model.ProviderAzure, OS: "linux"}, on, now); got.Offered || got.Minutes != 0 || got.Why == "" {
		t.Errorf("azure: %+v", got)
	}
}

func TestStartsOnDemand(t *testing.T) {
	t.Setenv("SKYBUILD_HOME", t.TempDir())
	e := New()
	err := config.Update(func(c *config.Config) error {
		c.Put(&model.Machine{Name: "idle-off", Provider: model.ProviderGCP, OS: "linux", Status: model.StatusStopped})
		c.Put(&model.Machine{Name: "idle-on", Provider: model.ProviderGCP, OS: "linux", Status: model.StatusStopped})
		c.Put(&model.Machine{Name: "up", Provider: model.ProviderGCP, OS: "linux", Status: model.StatusRunning})
		c.Put(&model.Machine{Name: "mini", Provider: model.ProviderSSH, OS: "darwin", Status: model.StatusStopped})
		c.SetIdle("idle-on", config.IdleStop{Minutes: 60})
		c.SetIdle("up", config.IdleStop{Minutes: 60})
		c.SetIdle("mini", config.IdleStop{Minutes: 60}) // not something sky would write, but it must not matter
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]bool{"idle-on": true, "idle-off": false, "up": false, "mini": false, "nobody": false} {
		if got := e.StartsOnDemand(name); got != want {
			t.Errorf("%s: %v, want %v", name, got, want)
		}
	}
	// Your own machine is refused; so is anything too short to be safe.
	if err := e.SetIdle(t.Context(), "mini", config.IdleStop{Minutes: 60}, nil); err == nil {
		t.Error("stop when idle was accepted for a machine sky didn't create")
	}
	if err := e.SetIdle(t.Context(), "idle-off", config.IdleStop{Minutes: 2}, nil); err == nil {
		t.Error("a two-minute limit was accepted")
	}
	// Turning it on for a stopped machine only saves the setting (nothing to reach).
	if err := e.SetIdle(t.Context(), "idle-off", config.IdleStop{Minutes: 240}, nil); err != nil {
		t.Fatal(err)
	}
	if info, _ := e.Idle("idle-off"); info.Minutes != 240 || !e.StartsOnDemand("idle-off") {
		t.Errorf("after turning it on: %+v", info)
	}
	if err := e.SetIdle(t.Context(), "idle-off", config.IdleStop{}, nil); err != nil {
		t.Fatal(err)
	}
	if e.StartsOnDemand("idle-off") {
		t.Error("still starts on demand after turning it off")
	}
}

// What the watchdog was installed with is compared with the setting, so a machine that
// missed a change (it was stopped then) is put right when it is next seen running.
func TestIdleDrift(t *testing.T) {
	t.Setenv("SKYBUILD_HOME", t.TempDir())
	e := New()
	_ = config.Update(func(c *config.Config) error {
		c.Put(&model.Machine{Name: "box", Provider: model.ProviderGCP, OS: "linux", Status: model.StatusRunning})
		c.Put(&model.Machine{Name: "plain", Provider: model.ProviderGCP, OS: "linux", Status: model.StatusRunning})
		c.Put(&model.Machine{Name: "mini", Provider: model.ProviderSSH, OS: "darwin", Status: model.StatusRunning})
		c.SetIdle("box", config.IdleStop{Minutes: 120})
		return nil
	})
	linux := func(st *IdleState) Health { return Health{OS: "linux", Idle: st} }
	for _, c := range []struct {
		name  string
		h     Health
		drift bool
	}{
		{"box", linux(&IdleState{Limit: 120}), false},
		{"box", linux(&IdleState{Limit: 60}), true},
		{"box", linux(&IdleState{Limit: 120, DryRun: true}), true},
		{"box", linux(nil), true}, // on, but nothing installed
		{"box", Health{OS: "linux", Error: "unreachable"}, false},
		{"plain", linux(nil), false},
		{"plain", linux(&IdleState{Limit: 120}), true}, // off, but still installed
		{"mini", Health{OS: "darwin"}, false},
	} {
		if got := e.IdleDrift(c.name, c.h); got != c.drift {
			t.Errorf("%s with %+v: drift=%v, want %v", c.name, c.h.Idle, got, c.drift)
		}
	}
}
