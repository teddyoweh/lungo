package main

import (
	"sync/atomic"
	"testing"
	"time"
)

// A refresh that is running holds the next back; one that ran far too long doesn't.
func TestBeginGivesUpOnAStuckRefresh(t *testing.T) {
	var since atomic.Int64
	first, ok := begin(&since, time.Hour, "test")
	if !ok {
		t.Fatal("the first refresh didn't start")
	}
	if _, ok := begin(&since, time.Hour, "test"); ok {
		t.Fatal("a second refresh started while the first runs")
	}
	end(&since, first)
	if _, ok := begin(&since, time.Hour, "test"); !ok {
		t.Fatal("no refresh after the first ended")
	}
	// Stuck: the one running started long ago.
	since.Store(time.Now().Add(-2 * time.Hour).UnixNano())
	stuck := since.Load()
	next, ok := begin(&since, time.Hour, "test")
	if !ok {
		t.Fatal("a stuck refresh still holds the next back")
	}
	end(&since, stuck) // the stuck one finishing late doesn't clear the new one
	if since.Load() != next {
		t.Fatal("a late finish cleared the running refresh")
	}
}
