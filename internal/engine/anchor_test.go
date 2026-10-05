package engine

import (
	"os"
	"path/filepath"
	"testing"
)

// The link left at an app's old path: made where nothing is, pointed at the app's program,
// left alone where a real app still is, and taken away again without touching anything else.
func TestSessionAnchor(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SKYBUILD_HOME", filepath.Join(home, ".skybuild"))
	if err := os.MkdirAll(filepath.Dir(anchorNote()), 0o700); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(home, "Applications", "New.app", "Contents", "MacOS", "New")
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, []byte("program"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(home, "Applications", "Old.app", "Contents", "MacOS", "Old")

	if got := anchorRoot(old); got != filepath.Join(home, "Applications", "Old.app") {
		t.Errorf("anchorRoot = %q", got)
	}
	if got := anchorRoot("/usr/local/bin/tool"); got != "" {
		t.Errorf("anchorRoot of a bare program = %q", got)
	}

	if err := leaveAnchor(old, exe); err != nil {
		t.Fatal(err)
	}
	if to, err := os.Readlink(old); err != nil || to != exe {
		t.Fatalf("link at the old path: %q, %v", to, err)
	}
	if b, err := os.ReadFile(old); err != nil || string(b) != "program" {
		t.Errorf("the old path doesn't lead to the program: %q, %v", b, err)
	}
	if readNote() != filepath.Dir(filepath.Dir(filepath.Dir(old))) {
		t.Errorf("anchor not noted: %q", readNote())
	}
	// The app moved again: the link follows.
	exe2 := filepath.Join(home, "Applications", "Newer.app", "Contents", "MacOS", "Newer")
	if err := leaveAnchor(old, exe2); err != nil {
		t.Fatal(err)
	}
	if to, _ := os.Readlink(old); to != exe2 {
		t.Errorf("link not updated: %q", to)
	}

	dropAnchor("")
	if exists(filepath.Join(home, "Applications", "Old.app")) || readNote() != "" {
		t.Error("the anchor wasn't removed")
	}
	if !exists(exe) {
		t.Error("removing the anchor took the app with it")
	}

	// A real app at the old path is never touched.
	if err := os.MkdirAll(filepath.Dir(old), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(old, []byte("old program"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "Applications", "Old.app", "Contents", "Info.plist"), []byte("plist"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := leaveAnchor(old, exe); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(old); string(b) != "old program" {
		t.Error("a real app at the old path was replaced")
	}
	_ = os.WriteFile(anchorNote(), []byte(filepath.Join(home, "Applications", "Old.app")+"\n"), 0o600)
	dropAnchor("")
	if !exists(old) {
		t.Error("dropAnchor removed a real app")
	}
}
