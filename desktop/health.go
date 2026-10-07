package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"skybuild/internal/config"
	"skybuild/internal/engine"
	"skybuild/internal/events"
	"skybuild/internal/paths"
	"skybuild/internal/syncer"
)

// Machine health, and machines that stop when idle: the numbers under each machine, the
// setting, the note about why a machine is stopped, and starting one again when a pane is
// opened on it.

// HealthView is what the UI shows about machines beyond their status. It is sent as the
// "health" event after every check.
type HealthView struct {
	Health  map[string]engine.Health   `json:"health"`  // running machines, by name
	Idle    map[string]engine.IdleInfo `json:"idle"`    // every machine's stop-when-idle setting
	Cost    map[string]engine.Cost     `json:"cost"`    // machines whose price is known
	Choices []int                      `json:"choices"` // the limits to offer, in minutes
	At      time.Time                  `json:"at"`
}

// healthKeeper holds the latest check and what is needed to start a stopped machine once,
// for the right reasons. One per app; times are wall-clock (Round(0)) so that hours asleep
// count as hours.
type healthKeeper struct {
	mu      sync.Mutex
	latest  map[string]engine.Health
	polling sync.Mutex

	asked   map[string]time.Time // machine/session → when a pane first asked to open it
	touched map[string]time.Time // machine → when the user last touched a pane on it
	started map[string]time.Time // machine → the last time it was started on demand
	failed  map[string]string    // machine → why that start failed
	tending map[string]bool      // machines whose watchdog is being put right
}

var keeper = &healthKeeper{
	latest: map[string]engine.Health{}, asked: map[string]time.Time{}, touched: map[string]time.Time{},
	started: map[string]time.Time{}, failed: map[string]string{}, tending: map[string]bool{},
}

func wall() time.Time { return time.Now().Round(0) }

const (
	healthEvery = 30 * time.Second
	// A pane counts as just opened for this long after it first asks for its session: long
	// enough for the app to find out that a machine it believed running is in fact stopped.
	paneFresh = 5 * time.Minute
	// How long a click or a key press in a pane stands for wanting its machine.
	touchLasts = 2 * time.Minute
	// After a start that failed, no pane starts the machine again by itself for this long.
	startCooldown = 3 * time.Minute
)

// healthLoop checks every running machine about every 30 seconds while the app is open.
func (a *App) healthLoop(ctx context.Context) {
	a.pollHealth(ctx)
	tick := time.NewTicker(healthEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			go a.pollHealth(ctx)
		}
	}
}

// pollHealth runs one check and sends the result to the UI.
func (a *App) pollHealth(ctx context.Context) HealthView {
	if !keeper.polling.TryLock() {
		return a.healthView()
	}
	defer keeper.polling.Unlock()
	c, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	primary := a.win.primary.Load()
	if primary { // once, however many windows are open
		a.eng.NoteStops(c)
	}
	var health map[string]engine.Health
	if shared, ok := sharedHealth(); !primary && ok {
		health = shared // the window that checks shared it: no second round of ssh
	} else {
		health = a.eng.HealthAll(c)
		if primary {
			shareHealth(health)
		}
	}
	keeper.mu.Lock()
	keeper.latest = health
	keeper.mu.Unlock()
	if primary {
		for name, h := range health {
			if a.eng.IdleDrift(name, h) {
				go a.tendIdle(ctx, name)
			}
		}
	}
	return a.emitHealth()
}

// tendIdle puts a machine's watchdog in line with its setting: a machine that was stopped
// when the setting changed, or one whose watchdog went missing.
func (a *App) tendIdle(ctx context.Context, name string) {
	keeper.mu.Lock()
	if keeper.tending[name] {
		keeper.mu.Unlock()
		return
	}
	keeper.tending[name] = true
	keeper.mu.Unlock()
	defer func() {
		keeper.mu.Lock()
		delete(keeper.tending, name)
		keeper.mu.Unlock()
	}()
	c, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if _, err := a.eng.TendIdle(c, name); err != nil {
		wruntime.LogWarningf(a.ctx, "stop when idle on %s: %v", name, err)
	}
}

func (a *App) healthView() HealthView {
	v := HealthView{Health: map[string]engine.Health{}, Idle: map[string]engine.IdleInfo{}, Cost: map[string]engine.Cost{},
		Choices: engine.IdleChoices, At: time.Now()}
	keeper.mu.Lock()
	for name, h := range keeper.latest {
		v.Health[name] = h
	}
	keeper.mu.Unlock()
	if all, err := a.eng.IdleAll(); err == nil {
		for _, i := range all {
			v.Idle[i.Machine] = i
		}
	}
	ms, _ := a.eng.Machines()
	for _, m := range ms {
		if !m.IsCloud() {
			continue
		}
		if c, ok := a.eng.Cost(m, v.At); ok {
			v.Cost[m.Name] = c
		}
	}
	return v
}

func (a *App) emitHealth() HealthView {
	v := a.healthView()
	if a.ctx != nil {
		wruntime.EventsEmit(a.ctx, "health", v)
	}
	return v
}

// MachineHealth returns the latest check without contacting any machine.
func (a *App) MachineHealth() HealthView { return a.healthView() }

// RefreshHealth checks the machines now.
func (a *App) RefreshHealth() HealthView { return a.pollHealth(a.ctx) }

// SetIdle turns stop-when-idle on (minutes > 0) or off for a machine, and updates the machine
// if it is running.
func (a *App) SetIdle(machine string, minutes int) string {
	title := fmt.Sprintf("Stop %s after %s idle", machine, syncer.IdleLimit(minutes))
	if minutes <= 0 {
		title = "Never stop " + machine + " when idle"
	}
	return a.ops.start("idle", title, machine, func(ctx context.Context, r events.Reporter) (any, error) {
		err := a.eng.SetIdle(ctx, machine, config.IdleStop{Minutes: minutes}, r)
		a.emitHealth()
		go a.pollHealth(a.ctx) // what the machine itself says now
		return nil, err
	})
}

// ---------- starting a stopped machine when a pane is opened on it ----------
//
// A machine that stops by itself has to come back by itself, or the setting is a trap. So
// opening a session on one starts it. But not every attempt to attach is someone wanting the
// machine: panes that lost their connection keep trying on their own, also when the computer
// wakes for a moment in the night or the network comes back, and those must not undo the
// stop. A start therefore needs one of two signs of a person:
//
//   - the pane is new to this run of the app (a pane just opened, or the app just launched
//     with its panes), or
//   - the user clicked or typed in a pane on that machine a moment ago (PaneIntent).
//
// A pane that was already open when the machine went away shows "… is stopped" until it is
// touched or the machine is started by hand.

// wanted reports whether an attempt to attach is a sign of a person wanting the machine: the
// pane first asked a moment ago, or was touched a moment ago. A zero time is "never".
func wanted(now, firstAsked, touched time.Time) bool {
	return now.Sub(firstAsked) < paneFresh || now.Sub(touched) < touchLasts
}

// paneOpening notes the first time a pane asks for a session on a machine.
func (a *App) paneOpening(machine, session string) {
	if engine.IsLocal(machine) {
		return
	}
	now := wall()
	keeper.mu.Lock()
	defer keeper.mu.Unlock()
	if _, ok := keeper.asked[machine+"/"+session]; ok {
		return
	}
	if len(keeper.asked) > 2000 { // shell panes come and go under new names: forget the old ones
		for k, t := range keeper.asked {
			if now.Sub(t) > 24*time.Hour {
				delete(keeper.asked, k)
			}
		}
	}
	keeper.asked[machine+"/"+session] = now
}

// wakeOnAttach is given the reason a pane couldn't be opened. When the reason is a stopped
// machine that starts on demand, it starts the machine (once, however many panes ask) and
// returns "Starting …", which the pane shows while it keeps trying. Otherwise the reason
// comes back unchanged.
func (a *App) wakeOnAttach(machine, session string, err error) error {
	if engine.IsLocal(machine) || !a.eng.StartsOnDemand(machine) {
		return err
	}
	keeper.mu.Lock()
	allowed := wanted(wall(), keeper.asked[machine+"/"+session], keeper.touched[machine])
	keeper.mu.Unlock()
	if out := a.startOnDemand(machine, allowed); out != nil {
		return out
	}
	return err
}

// PaneIntent is called when the user clicks or types in a pane whose machine is stopped: they
// want it. The machine is started if it starts on demand; the result says whether it is on
// its way.
func (a *App) PaneIntent(machine string) bool {
	if engine.IsLocal(machine) || !a.eng.StartsOnDemand(machine) {
		return false
	}
	keeper.mu.Lock()
	keeper.touched[machine] = wall()
	delete(keeper.failed, machine) // asking again by hand is a reason to try again
	keeper.mu.Unlock()
	return a.startOnDemand(machine, true) != nil
}

// startOnDemand starts a machine unless it is already being started. It returns what to
// tell the pane, or nil when nothing is being done (allowed is false, so the pane keeps its
// own reason).
func (a *App) startOnDemand(machine string, allowed bool) error {
	keeper.mu.Lock()
	defer keeper.mu.Unlock()
	starting := fmt.Errorf("Starting %s…", machine)
	for _, op := range a.ops.list() {
		if op.Running && op.Machine == machine && (op.Kind == "start" || op.Kind == "restart" || op.Kind == "resize") {
			return starting
		}
	}
	now := wall()
	if why, ok := keeper.failed[machine]; ok {
		if now.Sub(keeper.started[machine]) < startCooldown {
			return fmt.Errorf("%s didn't start: %s", machine, why)
		}
		delete(keeper.failed, machine)
	}
	if !allowed {
		return nil
	}
	keeper.started[machine] = now
	id := a.StartMachine(machine)
	go a.afterStart(machine, id)
	return starting
}

// afterStart waits for an on-demand start to end: a failure is remembered so panes stop
// asking for a while, and a machine that came up gets its watchdog checked.
func (a *App) afterStart(machine, id string) {
	for {
		time.Sleep(time.Second)
		var info *OpInfo
		for _, op := range a.ops.list() {
			if op.ID == id {
				info = &op
				break
			}
		}
		if info != nil && info.Running {
			continue
		}
		if info != nil && info.Error != "" {
			keeper.mu.Lock()
			keeper.failed[machine] = info.Error
			keeper.mu.Unlock()
			return
		}
		a.tendIdle(a.ctx, machine)
		a.pollHealth(a.ctx)
		return
	}
}

// The machines' health, as the window that checks them last found it, for the others.
func sharedHealthFile() string { return filepath.Join(paths.State(), "health-shared.json") }

func shareHealth(h map[string]engine.Health) {
	if b, err := json.Marshal(h); err == nil {
		_ = os.MkdirAll(paths.State(), 0o700)
		_, _ = paths.WriteFile(sharedHealthFile(), b, 0o600)
	}
}

// sharedHealth is that, when it is recent (two rounds at most).
func sharedHealth() (map[string]engine.Health, bool) {
	fi, err := os.Stat(sharedHealthFile())
	if err != nil || time.Since(fi.ModTime()) > 2*healthEvery+10*time.Second {
		return nil, false
	}
	b, err := os.ReadFile(sharedHealthFile())
	if err != nil {
		return nil, false
	}
	var h map[string]engine.Health
	return h, json.Unmarshal(b, &h) == nil
}
