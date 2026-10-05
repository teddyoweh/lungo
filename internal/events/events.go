// Package events carries progress from long operations to whoever is watching:
// a spinner in the CLI or a live log in the desktop app.
package events

import (
	"fmt"
	"sync"
	"time"
)

// Levels.
const (
	Step  = "step" // a new phase starts ("Creating VM")
	Info  = "info" // detail inside a phase
	Done  = "done" // a phase finished
	Warn  = "warn"
	Error = "error"
	Link  = "link" // a URL the user should open (Tailscale login)
	Log   = "log"  // raw output streamed from the machine
)

// Event is one progress update.
type Event struct {
	Op      string    `json:"op,omitempty"`
	Machine string    `json:"machine,omitempty"`
	Level   string    `json:"level"`
	Message string    `json:"message"`
	URL     string    `json:"url,omitempty"`
	Time    time.Time `json:"time"`
}

// Reporter receives events. Implementations must be safe for concurrent use.
type Reporter interface {
	Emit(Event)
}

// Func adapts a function to a Reporter.
type Func func(Event)

func (f Func) Emit(e Event) {
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	f(e)
}

// Discard drops every event.
var Discard Reporter = Func(func(Event) {})

// Tagged stamps every event with an operation and machine name before passing it on.
func Tagged(r Reporter, op, machine string) Reporter {
	if r == nil {
		r = Discard
	}
	return Func(func(e Event) {
		if e.Op == "" {
			e.Op = op
		}
		if e.Machine == "" {
			e.Machine = machine
		}
		r.Emit(e)
	})
}

// Helpers so call sites read like sentences.
func Stepf(r Reporter, f string, a ...any)  { emit(r, Step, f, a...) }
func Infof(r Reporter, f string, a ...any)  { emit(r, Info, f, a...) }
func Donef(r Reporter, f string, a ...any)  { emit(r, Done, f, a...) }
func Warnf(r Reporter, f string, a ...any)  { emit(r, Warn, f, a...) }
func Logf(r Reporter, f string, a ...any)   { emit(r, Log, f, a...) }
func Errorf(r Reporter, f string, a ...any) { emit(r, Error, f, a...) }

// OpenURL asks the watcher to show (and usually open) a link.
func OpenURL(r Reporter, msg, url string) {
	if r != nil {
		r.Emit(Event{Level: Link, Message: msg, URL: url, Time: time.Now()})
	}
}

func emit(r Reporter, level, f string, a ...any) {
	if r == nil {
		return
	}
	r.Emit(Event{Level: level, Message: fmt.Sprintf(f, a...), Time: time.Now()})
}

// Recorder keeps events in memory (tests, desktop history).
type Recorder struct {
	mu     sync.Mutex
	Events []Event
}

func (r *Recorder) Emit(e Event) {
	r.mu.Lock()
	r.Events = append(r.Events, e)
	r.mu.Unlock()
}
