package engine

import (
	"testing"
	"time"

	"skybuild/internal/claudeacct"
	"skybuild/internal/model"
)

func TestPickNext(t *testing.T) {
	accts := []*model.ClaudeAccount{{ID: "a"}, {ID: "b"}, {ID: "c", Disabled: true}, {ID: "d"}}
	has := func(id string) bool { return id != "x" }
	soon := time.Now().Add(time.Hour)
	past := time.Now().Add(-time.Minute)
	cases := []struct {
		name   string
		st     map[string]claudeacct.Status
		active string
		want   string
	}{
		{"next ok", map[string]claudeacct.Status{"a": {State: "limited", ResetsAt: soon}, "b": {State: "ok"}}, "a", "b"},
		{"skips limited and disabled, wraps", map[string]claudeacct.Status{"b": {State: "limited", ResetsAt: soon}, "d": {State: "invalid"}, "a": {State: "ok"}}, "b", "a"},
		{"reset passed counts as usable", map[string]claudeacct.Status{"b": {State: "limited", ResetsAt: past}}, "a", "b"},
		{"unchecked is usable", map[string]claudeacct.Status{}, "d", "a"},
		{"skips failed checks", map[string]claudeacct.Status{"b": {State: "error"}, "d": {State: "ok"}}, "a", "d"},
		{"none left", map[string]claudeacct.Status{"b": {State: "limited", ResetsAt: soon}, "d": {State: "invalid"}}, "a", ""},
	}
	for _, c := range cases {
		got := pickNext(accts, c.st, c.active, has)
		id := ""
		if got != nil {
			id = got.ID
		}
		if id != c.want {
			t.Errorf("%s: got %q want %q", c.name, id, c.want)
		}
	}
}

func TestIsLimitMessage(t *testing.T) {
	yes := []string{
		"You've hit your weekly limit · resets Oct 6, 11am (UTC)",
		"5-hour limit reached ∙ resets 3pm",
		"Claude AI usage limit reached|1759363200",
		"You've reached your Opus limit",
	}
	no := []string{"Claude needs your permission to use Bash", "Claude is waiting for your input", ""}
	for _, s := range yes {
		if !IsLimitMessage(s) {
			t.Errorf("should match: %q", s)
		}
	}
	for _, s := range no {
		if IsLimitMessage(s) {
			t.Errorf("should not match: %q", s)
		}
	}
}

func TestScreenStateLimitPrompt(t *testing.T) {
	screen := "❯ hi\n  ⎿  You've hit your weekly limit · resets Oct 6, 11am (UTC)\n" +
		"line a\nline b\nline c\nline d\nline e\nline f\nline g\nline h\nline i\nline j\nline k\nline l\nline m\n" +
		"   What do you want to do?\n   ❯ 1. Stop and wait for limit to reset\n     2. Wait here, then continue automatically at Oct 6, 11am\n" +
		"     3. Switch to usage credits\n     4. Upgrade your plan\n   Enter to confirm · Esc to cancel"
	state, msg := screenState(screen)
	if state != "waiting" || !IsLimitMessage(msg) {
		t.Fatalf("state=%q msg=%q", state, msg)
	}
	// The limit line scrolled away: still recognised from the prompt's options.
	state, msg = screenState("   What do you want to do?\n   ❯ 1. Stop and wait for limit to reset\n   Enter to confirm · Esc to cancel")
	if state != "waiting" || !IsLimitMessage(msg) {
		t.Fatalf("state=%q msg=%q", state, msg)
	}
	// A permission prompt is not a limit.
	state, msg = screenState("Do you want to proceed?\n ❯ 1. Yes\n   2. No\n Esc to cancel")
	if state != "waiting" || IsLimitMessage(msg) {
		t.Fatalf("state=%q msg=%q", state, msg)
	}
}

func TestSmartPick(t *testing.T) {
	r := func(id string, usable bool, score float64) claudeacct.Ranked {
		return claudeacct.Ranked{ID: id, Usable: usable, Score: score}
	}
	cases := []struct {
		name   string
		ranked []claudeacct.Ranked
		active string
		want   string
	}{
		{"active unusable → best", []claudeacct.Ranked{r("b", true, 2), r("a", false, 0)}, "a", "b"},
		{"clearly better → move", []claudeacct.Ranked{r("b", true, 2), r("a", true, 1)}, "a", "b"},
		{"slightly better → stay", []claudeacct.Ranked{r("b", true, 1.1), r("a", true, 1)}, "a", "a"},
		{"already best", []claudeacct.Ranked{r("a", true, 3), r("b", true, 1)}, "a", "a"},
		{"nothing usable", []claudeacct.Ranked{r("a", false, 0), r("b", false, 0)}, "a", ""},
		{"no active yet", []claudeacct.Ranked{r("b", true, 1), r("a", true, 0.5)}, "", "b"},
	}
	for _, c := range cases {
		if got := smartPick(c.ranked, c.active); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}
