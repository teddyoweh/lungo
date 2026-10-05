package claudeacct

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Ranking answers "which account should machines use right now to get the most out of all
// of them?" It is plain scheduling, no guessing:
//
//   - Whatever is left in an account's weekly window disappears when that window resets.
//     So the account to use first is the one with the most allowance about to expire:
//     (plan size × fraction left) ÷ hours until its reset. An account with a far-off reset,
//     or a week that hasn't started, can wait; its allowance isn't going anywhere yet.
//   - An account at its 5-hour or weekly limit can't be used; it rejoins the ranking the
//     moment it resets, and wins again if it still has the most to lose.
//   - A nearly full 5-hour window is avoided when there's an alternative, so a new session
//     doesn't start on an account that is about to stop.

// Ranked is one account's place in the ranking.
type Ranked struct {
	ID     string
	Usable bool
	Score  float64 // allowance at risk per hour (plan-weighted); higher = use sooner
	Left   float64 // fraction of the weekly window left (0..1)
	Reason string  // why it is where it is, for people
}

// planWeight is how big an account's allowance is relative to Pro.
func planWeight(plan string) float64 {
	switch {
	case strings.Contains(plan, "20x"):
		return 20
	case strings.Contains(plan, "5x"), plan == "Max":
		return 5
	case plan == "Pro":
		return 1
	}
	return 5 // unknown or team plans: assume a mid-size allowance
}

func until(t, now time.Time) string {
	d := t.Sub(now)
	switch {
	case d <= 0:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("in %dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("in %dh", int(d.Hours()+0.5))
	}
	return t.Local().Format("Mon 3 PM")
}

// Score ranks one account. plan is its plan name ("Max 20x"), "" if unknown.
func Score(st Status, plan string, now time.Time) Ranked {
	r := Ranked{Usable: st.Usable() && st.State != StateError, Left: 1}
	if st.State == StateInvalid {
		r.Reason = "Token rejected"
		return r
	}
	if st.State == StateLimited && !st.Usable() {
		r.Usable = false
		r.Reason = fmt.Sprintf("At its %s limit, back %s", WindowLabel(st.LimitType), until(st.ResetsAt, now))
		if w := st.Windows["seven_day"]; w != nil {
			r.Left = max(0, 1-w.Utilization)
		}
		return r
	}
	hours := 168.0 // a week that hasn't started loses nothing by waiting
	week := st.Windows["seven_day"]
	if week != nil {
		r.Left = max(0, 1-week.Utilization)
		if !week.ResetsAt.IsZero() && week.ResetsAt.After(now) {
			hours = max(1, week.ResetsAt.Sub(now).Hours())
		}
	}
	r.Score = planWeight(plan) * r.Left / hours
	five := st.Windows["five_hour"]
	if five != nil && five.Utilization >= 0.97 && five.ResetsAt.After(now) {
		r.Score *= 0.25 // about to hit the 5-hour limit
	}
	switch {
	case week == nil || week.ResetsAt.IsZero():
		r.Reason = "Week not started: nothing to lose by saving it"
	case r.Left <= 0.01:
		r.Reason = "Week used up, resets " + until(week.ResetsAt, now)
	default:
		r.Reason = fmt.Sprintf("%.0f%% of its week left, gone %s", r.Left*100, until(week.ResetsAt, now))
	}
	if five != nil && five.Utilization >= 0.97 && five.ResetsAt.After(now) {
		r.Reason += fmt.Sprintf(" · 5-hour window nearly full, refills %s", until(five.ResetsAt, now))
	}
	return r
}

// Rank orders account IDs: usable ones by score (most to lose first), then unusable ones by
// when they come back. plans maps ID → plan name; skip lists disabled or token-less accounts.
func Rank(ids []string, statuses map[string]Status, plans map[string]string, skip map[string]bool, now time.Time) []Ranked {
	out := make([]Ranked, 0, len(ids))
	for _, id := range ids {
		r := Score(statuses[id], plans[id], now)
		r.ID = id
		if skip[id] {
			r.Usable, r.Score = false, 0
		}
		out = append(out, r)
	}
	order := map[string]int{}
	for i, id := range ids {
		order[id] = i
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Usable != b.Usable {
			return a.Usable
		}
		if a.Usable {
			if a.Score != b.Score {
				return a.Score > b.Score
			}
			return order[a.ID] < order[b.ID]
		}
		ra, rb := statuses[a.ID].ResetsAt, statuses[b.ID].ResetsAt
		if ra.IsZero() != rb.IsZero() {
			return !ra.IsZero()
		}
		if !ra.Equal(rb) {
			return ra.Before(rb)
		}
		return order[a.ID] < order[b.ID]
	})
	return out
}
