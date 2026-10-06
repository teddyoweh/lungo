package claudeacct

import (
	"encoding/json"
	"testing"
	"time"
)

// Captured from `claude -p … --output-format stream-json --verbose` on a limited account.
const limitedEvent = `{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","resetsAt":1791284400,"rateLimitType":"seven_day","overageStatus":"rejected","isUsingOverage":false,"unifiedWindows":{"five_hour":{"utilization":0,"resetsAt":1791094800},"seven_day":{"utilization":1.01,"resetsAt":1791284400}}},"uuid":"u","session_id":"s"}`

func TestApplyRateLimited(t *testing.T) {
	var ev rateEvent
	if err := json.Unmarshal([]byte(limitedEvent), &ev); err != nil {
		t.Fatal(err)
	}
	var st Status
	applyRate(&st, ev)
	if st.State != StateLimited || st.LimitType != "seven_day" || st.ResetsAt.Unix() != 1791284400 {
		t.Fatalf("status: %+v", st)
	}
	if w := st.Windows["seven_day"]; w == nil || w.Utilization != 1.01 {
		t.Fatalf("windows: %+v", st.Windows)
	}
	// The captured reset has passed by now: usability is about a reset still ahead or gone.
	st.ResetsAt = time.Now().Add(time.Hour)
	if st.Usable() {
		t.Error("limited account in the future should not be usable")
	}
	st.ResetsAt = time.Now().Add(-time.Minute)
	if !st.Usable() {
		t.Error("limited account whose reset has passed should be usable")
	}
}

func TestUsable(t *testing.T) {
	if !(Status{State: StateOK}).Usable() || (Status{State: StateInvalid}).Usable() {
		t.Error("ok/invalid")
	}
	if !(Status{State: StateLimited, ResetsAt: time.Now().Add(-time.Second)}).Usable() {
		t.Error("past reset should be usable")
	}
}

func TestDescribe(t *testing.T) {
	st := Status{State: StateOK, Windows: map[string]*Window{"five_hour": {Utilization: 0.12}, "seven_day": {Utilization: 0.4}}}
	if got := Describe(st); got != "5-hour 12% · weekly 40%" {
		t.Errorf("got %q", got)
	}
}

func TestSameAccount(t *testing.T) {
	at := time.Unix(1791284400, 0)
	a := Status{Windows: map[string]*Window{"seven_day": {Utilization: 0.30, ResetsAt: at}, "five_hour": {Utilization: 0.01}}}
	b := Status{Windows: map[string]*Window{"seven_day": {Utilization: 0.305, ResetsAt: at}, "five_hour": {Utilization: 0.02}}}
	if !SameAccount(a, b) {
		t.Error("same account not matched")
	}
	c := Status{Windows: map[string]*Window{"seven_day": {Utilization: 0.30, ResetsAt: at.Add(time.Hour)}}}
	d := Status{Windows: map[string]*Window{"seven_day": {Utilization: 1.01, ResetsAt: at}}}
	if SameAccount(a, c) || SameAccount(a, d) || SameAccount(a, Status{}) {
		t.Error("different accounts matched")
	}
}

func TestPlan(t *testing.T) {
	if got := Plan(map[string]any{"organizationType": "claude_max", "organizationRateLimitTier": "default_claude_max_20x"}); got != "Max 20x" {
		t.Errorf("got %q", got)
	}
	if got := Plan(map[string]any{"organizationType": "claude_pro"}); got != "Pro" {
		t.Errorf("got %q", got)
	}
}
