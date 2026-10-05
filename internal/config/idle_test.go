package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"skybuild/internal/model"
)

func TestIdleRoundTrip(t *testing.T) {
	t.Setenv("SKYBUILD_HOME", t.TempDir())
	err := Update(func(c *Config) error {
		c.Put(&model.Machine{Name: "box", Provider: model.ProviderGCP})
		c.Put(&model.Machine{Name: "other", Provider: model.ProviderGCP})
		c.SetIdle("box", IdleStop{Minutes: 120})
		c.SetIdle("other", IdleStop{Minutes: 60, DryRun: true})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := c.IdleFor("box"); got != (IdleStop{Minutes: 120}) {
		t.Errorf("box: %+v", got)
	}
	if got := c.IdleFor("other"); got != (IdleStop{Minutes: 60, DryRun: true}) {
		t.Errorf("other: %+v", got)
	}
	if got := c.IdleFor("nobody"); got.Minutes != 0 {
		t.Errorf("a machine with no setting should be off: %+v", got)
	}

	// Off removes the entry; removing a machine takes its setting with it.
	if err := Update(func(c *Config) error {
		c.SetIdle("box", IdleStop{})
		c.Remove("other")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	c, _ = Load()
	if len(c.Idle) != 0 {
		t.Errorf("idle after off and remove: %+v", c.Idle)
	}
	b, _ := os.ReadFile(filepath.Join(os.Getenv("SKYBUILD_HOME"), "config.json"))
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(b, &raw)
	if _, there := raw["idle"]; there {
		t.Errorf("an empty idle map should not be written:\n%s", b)
	}
}

// The idle setting sits next to fields this build doesn't know, and both survive a save.
func TestIdleKeepsUnknownFields(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SKYBUILD_HOME", dir)
	raw := `{"version":1,"machines":[{"name":"box","provider":"gcp","user":"me","sync":[]}],"links":[],` +
		`"idle":{"box":{"minutes":240}},"futureThing":{"a":[1,2]},"settings":{"futureSetting":"keep"}}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Update(func(c *Config) error {
		if c.IdleFor("box").Minutes != 240 {
			t.Errorf("loaded: %+v", c.Idle)
		}
		c.SetIdle("box", IdleStop{Minutes: 60})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "config.json"))
	var got struct {
		Idle        map[string]IdleStop `json:"idle"`
		FutureThing json.RawMessage     `json:"futureThing"`
		Settings    map[string]any      `json:"settings"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.Idle["box"].Minutes != 60 || len(got.FutureThing) == 0 || got.Settings["futureSetting"] != "keep" {
		t.Errorf("after save:\n%s", b)
	}
}
