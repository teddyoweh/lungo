package claudeacct

import (
	"testing"
	"time"
)

func win(util float64, in time.Duration, now time.Time) *Window {
	return &Window{Utilization: util, ResetsAt: now.Add(in)}
}

func TestRankMostToLoseFirst(t *testing.T) {
	now := time.Now()
	st := map[string]Status{
		// 70% left, resets in 2 days: a lot expiring soon.
		"soon": {State: StateOK, Windows: map[string]*Window{"seven_day": win(0.30, 48*time.Hour, now)}},
		// 90% left but resets in 6 days: can wait.
		"later": {State: StateOK, Windows: map[string]*Window{"seven_day": win(0.10, 144*time.Hour, now)}},
		// never used: saving it loses nothing.
		"fresh": {State: StateOK},
		// limited until tomorrow.
		"limited": {State: StateLimited, LimitType: "seven_day", ResetsAt: now.Add(20 * time.Hour), Windows: map[string]*Window{"seven_day": win(1.01, 20*time.Hour, now)}},
	}
	ids := []string{"fresh", "limited", "later", "soon"}
	got := Rank(ids, st, map[string]string{}, nil, now)
	want := []string{"soon", "later", "fresh", "limited"}
	for i, w := range want {
		if got[i].ID != w {
			t.Fatalf("rank %d = %s, want %s (%+v)", i, got[i].ID, w, got)
		}
	}
	if got[3].Usable || !got[0].Usable {
		t.Errorf("usable flags: %+v", got)
	}
}

func TestRankPlanSizeAndFiveHour(t *testing.T) {
	now := time.Now()
	same := map[string]*Window{"seven_day": win(0.5, 72*time.Hour, now)}
	st := map[string]Status{"big": {State: StateOK, Windows: same}, "small": {State: StateOK, Windows: same}}
	got := Rank([]string{"small", "big"}, st, map[string]string{"big": "Max 20x", "small": "Pro"}, nil, now)
	if got[0].ID != "big" {
		t.Errorf("bigger plan with the same share left should go first: %+v", got)
	}
	// An account about to hit its 5-hour limit yields to one that isn't.
	st = map[string]Status{
		"full5": {State: StateOK, Windows: map[string]*Window{"seven_day": win(0.3, 48*time.Hour, now), "five_hour": win(0.99, time.Hour, now)}},
		"ok":    {State: StateOK, Windows: map[string]*Window{"seven_day": win(0.4, 60*time.Hour, now), "five_hour": win(0.1, 3*time.Hour, now)}},
	}
	got = Rank([]string{"full5", "ok"}, st, nil, nil, now)
	if got[0].ID != "ok" {
		t.Errorf("nearly-full 5-hour window should be avoided: %+v", got)
	}
	// Skipped (turned off) accounts sink to the bottom.
	got = Rank([]string{"full5", "ok"}, st, nil, map[string]bool{"ok": true}, now)
	if got[0].ID != "full5" || got[1].Usable {
		t.Errorf("skip: %+v", got)
	}
}
