package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestValidName(t *testing.T) {
	for _, ok := range []string{"box", "api-box-2", "a"} {
		if err := ValidName(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "Box", "-a", "a-", "1box", "a_b", "a.b"} {
		if ValidName(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestUpdateRoundTrip(t *testing.T) {
	t.Setenv("SKYBUILD_HOME", t.TempDir())
	if err := Update(func(c *Config) error { c.Settings.RemoteUser = "me"; return nil }); err != nil {
		t.Fatal(err)
	}
	c, err := Load()
	if err != nil || c.Settings.User() != "me" {
		t.Fatalf("%v %+v", err, c.Settings)
	}
}

// A newer build's fields survive a load and save by this build.
func TestUnknownFieldsSurvive(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SKYBUILD_HOME", dir)
	raw := `{"version":1,"machines":[],"links":[],"futureThing":{"a":[1,2]},"settings":{"remoteUser":"me","futureSetting":"keep","docker":false}}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Update(func(c *Config) error { c.Settings.Terminal = "Warp"; return nil }); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "config.json"))
	var got map[string]json.RawMessage
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if string(got["futureThing"]) == "" {
		t.Errorf("top-level unknown field dropped:\n%s", b)
	}
	var s map[string]any
	_ = json.Unmarshal(got["settings"], &s)
	if s["futureSetting"] != "keep" || s["terminal"] != "Warp" || s["remoteUser"] != "me" || s["docker"] != false {
		t.Errorf("settings after save: %v", s)
	}
	// And a known field written by this build wins over a stale carried copy.
	c, _ := Load()
	if c.Settings.Terminal != "Warp" || c.Settings.DockerOn() {
		t.Errorf("reload: %+v", c.Settings)
	}
}
