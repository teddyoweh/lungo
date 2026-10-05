package main

import (
	"os"
	"testing"
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
