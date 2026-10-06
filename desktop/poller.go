package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"skybuild/internal/engine"
	"skybuild/internal/paths"
)

// poller keeps sessions and machine status fresh while the app is open, and notifies when
// a Claude session needs you or finishes.
type poller struct {
	a *App

	mu         sync.Mutex
	last       SessionsView      // what the UI has: this computer's sessions, then the machines'
	remote     SessionsView      // the machines' part
	local      []engine.Session  // this computer's part
	states     map[string]string // session key → last Claude state
	primed     bool
	keep       map[string][]string // shell sessions this window's panes point at, per machine
	quietSince time.Time           // start, or the last wake: no tidying right after
	// When a refresh of the machines and one of this computer started (zero: none running).
	// A refresh that runs far too long no longer holds the next one back: see begin.
	remoteSince atomic.Int64
	localSince  atomic.Int64
	anchored    atomic.Bool // the sessions here answer to this app for folder access
}

// begin starts a refresh unless one is already running, and reports whether it did. One
// that has been running longer than stuck is given up on (it is written to the log, with
// what every part of the app was doing): a refresh that hangs must not stop all the next.
func begin(since *atomic.Int64, stuck time.Duration, what string) (int64, bool) {
	now := time.Now().UnixNano()
	for {
		was := since.Load()
		if was != 0 && time.Duration(now-was) < stuck {
			return 0, false
		}
		if since.CompareAndSwap(was, now) {
			if was != 0 {
				noteErr("%s refresh running for %s: starting another", what, time.Duration(now-was).Round(time.Second))
				go writeStacks(what+" refresh stalled", false)
			}
			return now, true
		}
	}
}

// end marks a refresh finished, unless a newer one has taken over since.
func end(since *atomic.Int64, started int64) { since.CompareAndSwap(started, 0) }

func newPoller(a *App) *poller {
	empty := SessionsView{Sessions: []engine.Session{}, Errors: map[string]string{}}
	return &poller{a: a, states: map[string]string{}, last: empty, remote: empty, quietSince: time.Now()}
}

func (p *poller) run(ctx context.Context) {
	sessionsTick := time.NewTicker(5 * time.Second)
	machinesTick := time.NewTicker(60 * time.Second)
	claudeTick := time.NewTicker(60 * time.Second)
	reapTick := time.NewTicker(5 * time.Minute)
	localTick := time.NewTicker(2 * time.Second)
	accountsTick := time.NewTicker(3 * time.Second)
	defer accountsTick.Stop()
	accountsSeen := accountFilesStamp()
	defer reapTick.Stop()
	defer localTick.Stop()
	go p.watchWake(ctx)
	go p.refreshLocal(ctx)
	defer sessionsTick.Stop()
	defer machinesTick.Stop()
	defer claudeTick.Stop()
	go p.refresh(ctx)
	go p.a.RefreshMachines()
	for {
		select {
		case <-ctx.Done():
			return
		case <-sessionsTick.C:
			go p.refresh(ctx)
		case <-machinesTick.C:
			go p.a.RefreshMachines()
		case <-claudeTick.C:
			if p.a.win.primary.Load() { // once, however many windows are open
				go p.claude(ctx)
			}
		case <-localTick.C:
			go p.refreshLocal(ctx)
		case <-accountsTick.C:
			// The accounts changed somewhere else (the background agent switched, a usage check
			// came in, the CLI): show it now, and look at the sessions again.
			if st := accountFilesStamp(); st != accountsSeen {
				accountsSeen = st
				p.a.emitClaude()
				go p.refresh(ctx)
			}
		case <-reapTick.C:
			p.mu.Lock()
			keep, settled := p.keep, time.Since(p.quietSince) > 4*time.Minute
			p.mu.Unlock()
			// Not right after a start or a wake: panes are still finding their shells again.
			if p.a.win.primary.Load() && settled {
				go p.a.eng.ReapShells(ctx, p.a.win.keepAll(keep)) // every window's shells, not only this one's
			}
		}
	}
}

// sessions returns the machines' sessions as last polled (not this computer's: account
// rotation and limits are about the machines).
func (p *poller) sessions() []engine.Session {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.remote.Sessions
}

// publishedSessions is every session the UI was last told about, this computer's included.
func (p *poller) publishedSessions() []engine.Session {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]engine.Session(nil), p.last.Sessions...)
}

// refreshLocal lists the sessions on this computer. It is cheap (no network), so it runs
// often: a local pane's folder, title and Claude state follow within a couple of seconds.
func (p *poller) refreshLocal(ctx context.Context) {
	started, ok := begin(&p.localSince, 20*time.Second, "local")
	if !ok {
		return
	}
	defer end(&p.localSince, started)
	if !p.anchored.Load() {
		p.anchored.Store(engine.AnchorLocalSessions(ctx, ownProgram()))
	}
	list, err := p.a.eng.LocalSessions(ctx)
	if err != nil {
		noteErr("local sessions: %v", err)
		return
	}
	p.mu.Lock()
	same := reflect.DeepEqual(list, p.local) || (len(list) == 0 && len(p.local) == 0)
	p.local = list
	p.mu.Unlock()
	if !same {
		p.publish()
	}
}

// publish sends the UI everything known: this computer's sessions and the machines'.
func (p *poller) publish() SessionsView {
	p.mu.Lock()
	all := make([]engine.Session, 0, len(p.local)+len(p.remote.Sessions))
	all = append(append(all, p.local...), p.remote.Sessions...)
	v := SessionsView{Sessions: all, Errors: p.remote.Errors, At: time.Now()}
	if v.Errors == nil {
		v.Errors = map[string]string{}
	}
	p.last = v
	p.mu.Unlock()
	p.notify(all)
	wruntime.EventsEmit(p.a.ctx, "sessions", v)
	return v
}

// claude runs a Claude account rotation pass: scheduled usage checks, and a switch to the
// next account when the active one is limited.
func (p *poller) claude(ctx context.Context) {
	c, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	res, err := p.a.eng.ClaudeTick(c, p.sessions(), nil)
	if err != nil {
		return
	}
	if res.Switched {
		p.a.announceSwitch(res)
	}
	p.a.emitClaude()
}

// refresh polls every running machine's sessions (one at a time) and emits "sessions".
func (p *poller) refresh(ctx context.Context) SessionsView {
	started, ok := begin(&p.remoteSince, 60*time.Second, "machines")
	if !ok {
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.last
	}
	defer end(&p.remoteSince, started)
	ms, _ := p.a.eng.Machines()
	if len(ms) == 0 {
		return p.store(SessionsView{Sessions: []engine.Session{}, Errors: map[string]string{}, At: time.Now()})
	}
	c, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	list, errs := p.a.eng.AllSessions(c)
	if list == nil {
		list = []engine.Session{}
	}
	if errs == nil {
		errs = map[string]string{}
	}
	v := SessionsView{Sessions: list, Errors: errs, At: time.Now()}
	p.a.eng.TendTerminals(ctx, errs) // keep each machine ready for a pane to open instantly
	if p.a.win.primary.Load() {
		go p.relogin(ctx, list)
	}
	return p.store(v)
}

// relogin moves sessions still on a login the machines have switched off (an account at its
// limit) onto the current one, with their conversations, as soon as each is idle.
func (p *poller) relogin(ctx context.Context, list []engine.Session) {
	due := false
	for _, s := range list {
		if engine.ReloginCandidate(s) != "" {
			due = true
			break
		}
	}
	if !due {
		return
	}
	moved := p.a.eng.MoveToNewLogin(ctx, list)
	if len(moved) == 0 {
		return
	}
	wruntime.EventsEmit(p.a.ctx, "claude:moved", map[string]any{"count": len(moved), "names": engine.LoginSummary(moved), "account": p.a.activeAccountName()})
	go func() {
		time.Sleep(1500 * time.Millisecond)
		p.refresh(ctx)
	}()
}

// store takes a poll of the machines and publishes it together with this computer's sessions.
func (p *poller) store(v SessionsView) SessionsView {
	p.mu.Lock()
	p.remote = v
	p.mu.Unlock()
	return p.publish()
}

func (p *poller) notify(list []engine.Session) {
	p.mu.Lock()
	defer p.mu.Unlock()
	// Every window shows the in-app notice; only the primary sends the native notification,
	// so three open windows don't ping three times.
	native := p.a.win.primary.Load()
	seen := map[string]bool{}
	for _, s := range list {
		k := s.Key()
		seen[k] = true
		prev, had := p.states[k]
		p.states[k] = s.State
		if !p.primed || !had || s.Agent == "" || prev == s.State { // any agent: Claude, Codex, Grok, Mantis
			continue
		}
		switch {
		case s.State == "waiting":
			body := s.Message
			if body == "" {
				body = engine.AgentName(s.Agent) + " is waiting for you"
			}
			if native {
				notify(sessionLabel(s)+" needs you", body)
			}
			wruntime.EventsEmit(p.a.ctx, "session:attention", s)
		case s.State == "idle" && prev == "working":
			if native {
				notify(sessionLabel(s)+" finished", engine.AgentName(s.Agent)+" is done and waiting for your next message")
			}
			wruntime.EventsEmit(p.a.ctx, "session:attention", s)
		}
	}
	for k := range p.states {
		if !seen[k] {
			delete(p.states, k)
		}
	}
	p.primed = true
}

// sessionLabel names a session in a notification: what Claude calls it, and where it runs.
func sessionLabel(s engine.Session) string {
	name := s.Title
	if name == "" {
		name = s.Name
	}
	if engine.IsLocal(s.Machine) {
		return name + " on this computer"
	}
	return name + " on " + s.Machine
}

// accountFilesStamp changes whenever the Claude accounts' state changes on disk: which one
// machines use (config.json) and what each was last seen to have left.
func accountFilesStamp() string {
	stamp := ""
	for _, f := range []string{paths.Config(), filepath.Join(paths.State(), "claude-accounts.json")} {
		if st, err := os.Stat(f); err == nil {
			stamp += fmt.Sprintf("%d:%d;", st.ModTime().UnixNano(), st.Size())
		}
	}
	return stamp
}
