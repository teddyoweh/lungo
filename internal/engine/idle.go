package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"skybuild/internal/config"
	"skybuild/internal/events"
	"skybuild/internal/model"
	"skybuild/internal/paths"
	"skybuild/internal/provider"
	"skybuild/internal/syncer"
)

// Stop when idle: a cloud machine shuts itself down after a while with nothing going on, and
// is started again when a session is opened on it. The watching happens on the machine (see
// syncer.IdleWatchdog); this file is the setting, the note about why a machine is stopped,
// and the rule for starting it again.

// IdleChoices are the limits the app offers, in minutes. The CLI takes any duration.
var IdleChoices = []int{60, 120, 240, 480}

// IdleInfo is a machine's stop-when-idle setting and, while it is stopped, what is known
// about why.
type IdleInfo struct {
	Machine   string    `json:"machine"`
	Offered   bool      `json:"offered"`
	Why       string    `json:"why,omitempty"` // why it can't be turned on for this machine
	Minutes   int       `json:"minutes"`       // 0 = off
	DryRun    bool      `json:"dryRun,omitempty"`
	StoppedAt time.Time `json:"stoppedAt,omitzero"` // when it shut itself down, if that is why it is stopped
	Note      string    `json:"note,omitempty"`     // "Stopped after 2h idle at 03:10"
}

// ParseIdle reads a limit as typed: "off", "2h", "90m", "1h30m". It returns minutes.
func ParseIdle(s string) (int, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "off", "0", "never", "no", "none":
		return 0, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%q isn't a time: use off, 1h, 2h, 4h, 8h (or any duration like 90m)", s)
	}
	minutes := int(d.Minutes())
	switch {
	case minutes < syncer.IdleMinMinutes:
		return 0, fmt.Errorf("the shortest limit is %d minutes", syncer.IdleMinMinutes)
	case minutes > 7*24*60:
		return 0, errors.New("the longest limit is 7 days")
	}
	return minutes, nil
}

// Idle is one machine's setting.
func (e *Engine) Idle(name string) (IdleInfo, error) {
	m, err := e.Machine(name)
	if err != nil {
		return IdleInfo{}, err
	}
	c, err := config.Load()
	if err != nil {
		return IdleInfo{}, err
	}
	return idleInfo(m, c.IdleFor(name), time.Now()), nil
}

// IdleAll is every machine's setting. It reads only what this computer already knows.
func (e *Engine) IdleAll() ([]IdleInfo, error) {
	c, err := config.Load()
	if err != nil {
		return nil, err
	}
	out := make([]IdleInfo, 0, len(c.Machines))
	now := time.Now()
	for _, m := range c.Machines {
		out = append(out, idleInfo(m, c.IdleFor(m.Name), now))
	}
	return out, nil
}

func idleInfo(m *model.Machine, s config.IdleStop, now time.Time) IdleInfo {
	info := IdleInfo{Machine: m.Name, Minutes: s.Minutes, DryRun: s.DryRun}
	info.Offered, info.Why = syncer.IdleOffered(m)
	if !info.Offered {
		info.Minutes, info.DryRun = 0, false
	}
	if m.Status == model.StatusStopped {
		if st := loadStop(m.Name); st.Checked {
			if info.Note = stopNote(st, now); info.Note != "" {
				info.StoppedAt = st.StoppedAt
			}
		}
	}
	return info
}

// SetIdle changes a machine's setting and, when the machine is running, puts the watchdog
// in place (or takes it away) right now. A machine that is stopped gets it the next time it
// is synced after starting.
func (e *Engine) SetIdle(ctx context.Context, name string, s config.IdleStop, r events.Reporter) error {
	m, err := e.Machine(name)
	if err != nil {
		return err
	}
	if s.Minutes <= 0 {
		s = config.IdleStop{}
	} else {
		if ok, why := syncer.IdleOffered(m); !ok {
			return errors.New(why)
		}
		if s.Minutes < syncer.IdleMinMinutes {
			return fmt.Errorf("the shortest limit is %d minutes", syncer.IdleMinMinutes)
		}
	}
	if err := config.Update(func(c *config.Config) error {
		if c.Machine(name) == nil {
			return errNoMachine(name)
		}
		c.SetIdle(name, s)
		return nil
	}); err != nil {
		return err
	}
	said := name + " never stops by itself"
	if s.Minutes > 0 {
		said = fmt.Sprintf("%s stops after %s with nothing going on", name, syncer.IdleLimit(s.Minutes))
		if s.DryRun {
			said += " (dry run: it only logs that it would)"
		}
	}
	if m.Status != model.StatusRunning {
		events.Donef(r, "%s. It is %s now; this takes effect once it is running and synced", said, firstNonEmpty(m.Status, "not running"))
		return nil
	}
	events.Stepf(r, "Updating %s", name)
	if _, err := syncer.ApplyIdle(ctx, e.Target(m), s, false); err != nil {
		return fmt.Errorf("saved, but %s couldn't be updated now (the next sync does it): %w", name, err)
	}
	events.Donef(r, "%s", said)
	return nil
}

// TendIdle makes a running machine's watchdog match its setting. It is what sync does for
// this, on its own: for right after a start, and for a machine whose watchdog was found to
// disagree with the setting.
func (e *Engine) TendIdle(ctx context.Context, name string) (string, error) {
	m, err := e.Machine(name)
	if err != nil {
		return "", err
	}
	if !m.IsCloud() || m.Status != model.StatusRunning {
		return "", nil
	}
	c, err := config.Load()
	if err != nil {
		return "", err
	}
	want := c.IdleFor(name)
	if ok, _ := syncer.IdleOffered(m); !ok {
		want = config.IdleStop{}
	}
	return syncer.ApplyIdle(ctx, e.Target(m), want, false)
}

// IdleDrift reports whether what a machine's watchdog was installed with differs from the
// setting, given the machine's latest health.
func (e *Engine) IdleDrift(name string, h Health) bool {
	c, err := config.Load()
	if err != nil || h.Error != "" || h.OS != "linux" {
		return false
	}
	m := c.Machine(name)
	if m == nil || !m.IsCloud() {
		return false
	}
	want := c.IdleFor(name)
	if ok, _ := syncer.IdleOffered(m); !ok {
		want = config.IdleStop{}
	}
	if h.Idle == nil {
		return want.Minutes > 0
	}
	return h.Idle.Limit != want.Minutes || h.Idle.DryRun != want.DryRun
}

// StartsOnDemand reports whether opening a session on a machine may start it: it is stopped
// and stop-when-idle is on for it. A machine that stops by itself has to come back by itself.
func (e *Engine) StartsOnDemand(name string) bool {
	c, err := config.Load()
	if err != nil {
		return false
	}
	m := c.Machine(name)
	if m == nil || m.Status != model.StatusStopped {
		return false
	}
	ok, _ := syncer.IdleOffered(m)
	return ok && c.IdleFor(name).Minutes > 0
}

// ---------- why a machine is stopped ----------
//
// A machine that is off can't say why. Its cloud can: Compute Engine and EC2 both record
// whether an instance was stopped through their API or shut itself down, and when. sky asks
// once per stop and remembers the answer next to its other per-machine state.

type stopState struct {
	Checked   bool      `json:"checked"`
	CheckedAt time.Time `json:"checkedAt"`
	StoppedAt time.Time `json:"stoppedAt,omitzero"`
	BySelf    bool      `json:"bySelf,omitempty"`
	Minutes   int       `json:"minutes,omitempty"` // the limit in force when the stop was noticed
}

// Machine names have no dots, so this can't collide with another machine's state file.
func stopPath(name string) string { return filepath.Join(paths.State(), name+".idle.json") }

func loadStop(name string) stopState {
	var st stopState
	if b, err := os.ReadFile(stopPath(name)); err == nil {
		_ = json.Unmarshal(b, &st)
	}
	return st
}

func saveStop(name string, st stopState) {
	b, _ := json.Marshal(st)
	tmp := stopPath(name) + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, stopPath(name))
	}
}

// stopRecheck is how long an answer about a stop is trusted. The machine could have been
// started and stopped again from somewhere else in the meantime.
const stopRecheck = 30 * time.Minute

// NoteStops looks at the machines as last refreshed: for one that is stopped it finds out
// (once) when and how it stopped, and for one that is running it drops the old answer. It
// reports whether anything changed.
func (e *Engine) NoteStops(ctx context.Context) bool {
	c, err := config.Load()
	if err != nil {
		return false
	}
	changed := false
	known := map[string]bool{}
	for _, m := range c.Machines {
		known[m.Name] = true
		if !m.IsCloud() {
			continue
		}
		st := loadStop(m.Name)
		switch m.Status {
		case model.StatusRunning:
			if st.Checked {
				os.Remove(stopPath(m.Name))
				changed = true
			}
			continue
		case model.StatusStopped:
		default:
			continue // starting, stopping or unknown: wait for it to settle
		}
		if st.Checked && time.Since(st.CheckedAt) < stopRecheck {
			continue
		}
		p, err := e.Provider(m.Provider)
		if err != nil {
			continue
		}
		insp, ok := p.(provider.StopInspector)
		if !ok {
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		info, err := insp.StopInfo(cctx, m)
		cancel()
		if err != nil {
			continue // asked again at the next look
		}
		next := stopState{Checked: true, CheckedAt: time.Now(), StoppedAt: info.At, BySelf: info.BySelf, Minutes: c.IdleFor(m.Name).Minutes}
		if st.Checked && st.StoppedAt.Equal(info.At) {
			next.Minutes = st.Minutes // the same stop: keep the limit as it was then
		}
		saveStop(m.Name, next)
		closeRun(m.Name, info.At)
		if !st.Checked || !st.StoppedAt.Equal(next.StoppedAt) || st.BySelf != next.BySelf {
			changed = true
		}
	}
	// State of machines that are gone.
	for _, pattern := range []string{"*.idle.json", "*.runs.json"} {
		files, _ := filepath.Glob(filepath.Join(paths.State(), pattern))
		for _, f := range files {
			if name, _, _ := strings.Cut(filepath.Base(f), "."); !known[name] {
				os.Remove(f)
			}
		}
	}
	return changed
}

// stopNote is the sentence shown on a stopped machine: "Stopped after 2h idle at 03:10".
// It is only said when the machine shut itself down while the setting was on.
func stopNote(st stopState, now time.Time) string {
	if !st.BySelf || st.Minutes <= 0 {
		return ""
	}
	s := "Stopped after " + syncer.IdleLimit(st.Minutes) + " idle"
	if st.StoppedAt.IsZero() {
		return s
	}
	t := st.StoppedAt.In(now.Location())
	switch ago := now.Sub(t); {
	case ago < 18*time.Hour:
		return s + " at " + t.Format("15:04")
	case ago < 6*24*time.Hour:
		return s + " on " + t.Format("Mon") + " at " + t.Format("15:04")
	}
	return s + " on " + t.Format("Jan 2") + " at " + t.Format("15:04")
}

// IdleLog returns the last lines the machine's watchdog wrote: one per check, saying what it
// found and what it did about it.
func (e *Engine) IdleLog(ctx context.Context, name string, lines int) ([]string, error) {
	m, err := e.Machine(name)
	if err != nil {
		return nil, err
	}
	if err := e.Ready(m); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := runOn(ctx, e, m, fmt.Sprintf("tail -n %d %s 2>/dev/null; true", lines, syncer.IdleLogPath))
	if err != nil {
		return nil, err
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}
