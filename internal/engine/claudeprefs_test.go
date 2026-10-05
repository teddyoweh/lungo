package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClaudePreciseScroll(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	path := filepath.Join(dir, "settings.json")
	read := func() string { b, _ := os.ReadFile(path); return string(b) }
	valid := func() map[string]any {
		var m map[string]any
		if err := json.Unmarshal([]byte(read()), &m); err != nil {
			t.Fatalf("not valid JSON: %v\n%s", err, read())
		}
		return m
	}

	// No file yet: turning it off changes nothing, turning it on makes the file.
	if ClaudePreciseScroll() || SetClaudePreciseScroll(false) != nil || read() != "" {
		t.Fatalf("no file: %q", read())
	}
	if err := SetClaudePreciseScroll(true); err != nil || !ClaudePreciseScroll() {
		t.Fatalf("create: %v %q", err, read())
	}

	// A file written by hand keeps its shape: the key goes in first, everything else stays.
	hand := "{\n    \"model\": \"opus\",\n    \"hooks\": {\"Stop\": []},\n\n    \"theme\": \"dark\"\n}\n"
	os.Remove(path)
	os.WriteFile(path, []byte(hand), 0o644)
	if err := SetClaudePreciseScroll(true); err != nil || !ClaudePreciseScroll() {
		t.Fatalf("add: %v", err)
	}
	if got := read(); !strings.Contains(got, "\n    \"model\": \"opus\",\n    \"hooks\": {\"Stop\": []},\n\n    \"theme\": \"dark\"\n}\n") || valid()["model"] != "opus" {
		t.Errorf("the rest of the file changed:\n%s", got)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o644 {
		t.Errorf("mode changed: %v", st.Mode())
	}
	// Taking it out again gives the file back as it was.
	if err := SetClaudePreciseScroll(false); err != nil || ClaudePreciseScroll() || read() != hand {
		t.Errorf("remove: %v\n%q\nwant\n%q", err, read(), hand)
	}

	// Already there with the other value, or as the last key, or alone.
	for _, in := range []string{
		`{"wheelScrollAccelerationEnabled": true, "a": 1}`,
		"{\n  \"a\": 1,\n  \"wheelScrollAccelerationEnabled\": true\n}",
		`{}`,
		`{"wheelScrollAccelerationEnabled":true}`,
	} {
		os.WriteFile(path, []byte(in), 0o600)
		if err := SetClaudePreciseScroll(true); err != nil || !ClaudePreciseScroll() {
			t.Errorf("on from %q: %v → %q", in, err, read())
		}
		before := valid()
		if err := SetClaudePreciseScroll(false); err != nil || ClaudePreciseScroll() {
			t.Errorf("off from %q: %v → %q", in, err, read())
		}
		after := valid()
		if _, still := after["wheelScrollAccelerationEnabled"]; still || len(after) != len(before)-1 {
			t.Errorf("off from %q left %q", in, read())
		}
	}

	// A broken file is not touched.
	os.WriteFile(path, []byte("{ not json"), 0o600)
	if err := SetClaudePreciseScroll(true); err == nil || read() != "{ not json" {
		t.Errorf("a broken file: %v %q", err, read())
	}
}
