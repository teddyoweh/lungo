package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"slices"
	"testing"
	"time"
)

// Two windows: the first holds the primary role, the second takes it over when the first closes.
func TestWindowPrimaryTakeover(t *testing.T) {
	t.Setenv("SKYBUILD_HOME", t.TempDir())
	t.Setenv("SKY_HEADLESS", "")
	t.Setenv("SKY_WINDOW", "")
	a := newWindow()
	if a.id != mainWindow {
		t.Fatalf("first window id = %q, want %q", a.id, mainWindow)
	}
	a.start()
	t.Setenv("SKY_WINDOW", "w2")
	b := newWindow()
	b.tryPrimary()
	if !a.primary.Load() || b.primary.Load() {
		t.Fatalf("primary: first=%v second=%v, want true/false", a.primary.Load(), b.primary.Load())
	}
	if got := a.pids(); len(got) != 1 || got[0] != os.Getpid() {
		t.Fatalf("pids = %v, want this process", got)
	}
	a.close()
	b.tryPrimary()
	if !b.primary.Load() {
		t.Fatal("second window didn't take over after the first closed")
	}
	if got := b.pids(); len(got) != 0 {
		t.Fatalf("pids after close = %v, want none", got)
	}
	_ = b.lock.Unlock()
}

// A hidden dev instance must not take the role from the user's window.
func TestHeadlessNeverPrimary(t *testing.T) {
	t.Setenv("SKYBUILD_HOME", t.TempDir())
	t.Setenv("SKY_HEADLESS", "1")
	w := newWindow()
	w.tryPrimary()
	if w.primary.Load() {
		t.Fatal("headless instance became primary")
	}
}

// otherWindow pretends another window is running: a process that stands in for it, with
// the record a window leaves. It reports the stand-in, which is gone once asked to quit.
func otherWindow(t *testing.T, id string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	b, _ := json.Marshal(map[string]any{"id": id, "pid": cmd.Process.Pid})
	_ = os.MkdirAll(windowsDir(), 0o700)
	if err := os.WriteFile((&window{}).file(cmd.Process.Pid), b, 0o600); err != nil {
		t.Fatal(err)
	}
	updateSaved(func(s *savedWindows) { s.IDs = append(s.IDs, id) })
	return cmd
}

func restoringHome(t *testing.T) {
	t.Setenv("SKYBUILD_HOME", t.TempDir())
	t.Setenv("SKY_HEADLESS", "")
	t.Setenv("SKY_TMUX_SOCKET", "")
	t.Setenv("SKY_WINDOW", "")
	was := isLungo
	isLungo = func(int) bool { return true }
	t.Cleanup(func() { isLungo = was })
}

// A plain launch opens as the first window to bring back that isn't open yet.
func TestAdoptWindow(t *testing.T) {
	restoringHome(t)
	if w := newWindow(); w.id != mainWindow || !w.launched {
		t.Fatalf("first launch ever: id %q launched %v, want main, true", w.id, w.launched)
	}
	updateSaved(func(s *savedWindows) { s.IDs = []string{"wbbbbbb"} })
	if got := adoptID(); got != "wbbbbbb" {
		t.Fatalf("adopt with one saved window: %q", got)
	}
	otherWindow(t, "wbbbbbb")
	updateSaved(func(s *savedWindows) { s.IDs = []string{"wbbbbbb", "wcccccc"} })
	if got := adoptID(); got != "wcccccc" {
		t.Fatalf("adopt with the first saved one open: %q, want the second", got)
	}
}

// The close button forgets a window while others stay open; the last window stays.
func TestCloseForgetsWindow(t *testing.T) {
	restoringHome(t)
	t.Setenv("SKY_WINDOW", "wbbbbbb")
	w := newWindow()
	w.start()
	defer w.close()
	w.setSessions([]string{"demo/shell-1234"})
	w.closing.Store(true)

	w.leaving() // the last window
	if s := readSaved(); !slices.Contains(s.IDs, "wbbbbbb") {
		t.Fatalf("last window closed: saved %v, want it kept", s.IDs)
	}

	otherWindow(t, mainWindow)
	w.leaving()
	s := readSaved()
	if slices.Contains(s.IDs, "wbbbbbb") || !slices.Contains(s.Forgotten, "wbbbbbb") || !slices.Contains(s.IDs, mainWindow) {
		t.Fatalf("closed with another open: %+v, want it forgotten and main kept", s)
	}
	if _, err := os.Stat(stateFile("wbbbbbb")); err == nil {
		t.Fatal("a forgotten window's sessions are still on record")
	}
}

// Quitting keeps every window and takes the others along.
func TestQuitKeepsWindows(t *testing.T) {
	restoringHome(t)
	t.Setenv("SKY_WINDOW", mainWindow)
	w := newWindow()
	w.start()
	defer w.close()
	other := otherWindow(t, "wbbbbbb")
	w.leaving()
	if s := readSaved(); !slices.Contains(s.IDs, mainWindow) || !slices.Contains(s.IDs, "wbbbbbb") || len(s.Forgotten) != 0 {
		t.Fatalf("after quitting: %+v, want both kept", s)
	}
	done := make(chan error, 1)
	go func() { done <- other.Wait() }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the other window wasn't asked to quit")
	}
}

// Shells any window points at, open or to come back, are kept from tidying.
func TestKeepAllWindowsShells(t *testing.T) {
	restoringHome(t)
	t.Setenv("SKY_WINDOW", mainWindow)
	w := newWindow()
	w.start()
	defer w.close()
	updateSaved(func(s *savedWindows) { s.IDs = append(s.IDs, "wbbbbbb") })
	(&window{id: "wbbbbbb"}).setSessions([]string{"@local/shell-aaaa", "demo/claude-x"})
	keep := w.keepAll(map[string][]string{"demo": {"shell-bbbb"}})
	if !slices.Contains(keep["@local"], "shell-aaaa") || !slices.Contains(keep["demo"], "shell-bbbb") {
		t.Fatalf("keep = %v", keep)
	}
}
