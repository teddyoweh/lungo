package main

import (
	"testing"
	"time"
)

// Which attempts to attach may start a stopped machine: a pane that is new, or one the user
// just touched. A pane that has been open for a while and retries on its own may not, however
// often it tries.
func TestWanted(t *testing.T) {
	now := time.Date(2026, 10, 4, 8, 0, 0, 0, time.UTC)
	never := time.Time{}
	for _, c := range []struct {
		name           string
		asked, touched time.Time
		want           bool
	}{
		{"a pane opened just now", now, never, true},
		{"a pane opened four minutes ago, still finding out the machine is stopped", now.Add(-4 * time.Minute), never, true},
		{"a pane open since last night, retrying after the computer woke", now.Add(-9 * time.Hour), never, false},
		{"the same pane, clicked a moment ago", now.Add(-9 * time.Hour), now.Add(-10 * time.Second), true},
		{"the same pane, touched yesterday", now.Add(-30 * time.Hour), now.Add(-20 * time.Hour), false},
		{"a pane the app has never seen ask (it can't happen, and must not start anything)", never, never, false},
	} {
		if got := wanted(now, c.asked, c.touched); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

// Times are kept without the monotonic clock, which stands still while the computer sleeps:
// a pane opened before a night's sleep must count as nine hours old, not as minutes.
func TestWallClock(t *testing.T) {
	a := wall()
	if a != a.Round(0) || a.String() != a.Round(0).String() {
		t.Errorf("wall() carries a monotonic reading: %v", a)
	}
}
