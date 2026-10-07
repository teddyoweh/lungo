package main

import (
	"os"
	"testing"
	"time"
)

func TestPrefsStore(t *testing.T) {
	t.Setenv("SKYBUILD_HOME", t.TempDir())
	if len(readPrefs()) != 0 {
		t.Fatal("prefs before any were saved")
	}
	// Many settings in one write.
	if changed, err := writePrefs(map[string]string{"sky.meta": `{"@local/shell-1":{"name":"api"}}`, "sky.theme": "dark", "sky.zoom.w123": "2"}); !changed || err != nil {
		t.Fatalf("batch: %v %v", changed, err)
	}
	if m := readPrefs(); m["sky.theme"] != "dark" || m["sky.zoom.w123"] != "2" || len(m) != 3 {
		t.Fatalf("after batch: %v", m)
	}
	st1, _ := os.Stat(prefsFile())
	time.Sleep(20 * time.Millisecond)
	// The same values again: the file stays as it is (windows watch its time).
	if changed, _ := writePrefs(map[string]string{"sky.theme": "dark"}); changed {
		t.Fatal("rewrote unchanged prefs")
	}
	if st2, _ := os.Stat(prefsFile()); !st2.ModTime().Equal(st1.ModTime()) {
		t.Fatal("unchanged prefs file's time moved")
	}
	// An empty value removes a setting; the others stay.
	if changed, _ := writePrefs(map[string]string{"sky.zoom.w123": "", "sky.theme": "light"}); !changed {
		t.Fatal("delete and change didn't write")
	}
	m := readPrefs()
	if _, ok := m["sky.zoom.w123"]; ok || m["sky.theme"] != "light" || m["sky.meta"] == "" {
		t.Fatalf("after delete: %v", m)
	}
}
