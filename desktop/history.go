package main

import (
	"context"
	"sync"
	"time"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"skybuild/internal/engine"
)

// Past conversations and search across everything (engine/history.go, engine/search.go).

// HistoryView is one machine's past conversations.
type HistoryView struct {
	Machine       string                `json:"machine"`
	Conversations []engine.Conversation `json:"conversations"`
	Error         string                `json:"error,omitempty"`
}

// History lists the past Claude conversations on a machine (engine.LocalMachine: this
// computer), newest first. The page asks each machine on its own, so the quick ones show
// first; which conversations are open right now it tells from the sessions it already has.
func (a *App) History(machine string) HistoryView {
	ctx, cancel := context.WithTimeout(a.ctx, 25*time.Second)
	defer cancel()
	v := HistoryView{Machine: machine, Conversations: []engine.Conversation{}}
	list, err := a.eng.History(ctx, machine, engine.HistoryOptions{Sessions: []engine.Session{}})
	if err != nil {
		v.Error = err.Error()
	}
	if list != nil {
		v.Conversations = list
	}
	return v
}

// SearchEvent is what the page hears while a search runs ("search" events): where it is
// looking, then hits as each machine finds them, each machine's end, and the end of it all.
type SearchEvent struct {
	ID       int      `json:"id"`                 // the search it belongs to, as the page numbered it
	Machines []string `json:"machines,omitempty"` // first event: where it is looking
	engine.SearchPart
	End bool `json:"end,omitempty"` // every machine is through
}

// searching is the search that is running: a window has one at a time.
var searching struct {
	sync.Mutex
	stop context.CancelFunc
}

// SearchStart looks for query in every live session and every past conversation, on every
// machine that is up and on this computer. It returns at once; results come as "search"
// events carrying id. A search that was still running is dropped.
func (a *App) SearchStart(id int, query string) {
	ctx, cancel := context.WithCancel(a.ctx)
	searching.Lock()
	if searching.stop != nil {
		searching.stop()
	}
	searching.stop = cancel
	searching.Unlock()
	go func() {
		defer cancel()
		emit := func(e SearchEvent) {
			if ctx.Err() != nil {
				return // dropped: the page has moved on
			}
			e.ID = id
			if e.Hits == nil {
				e.Hits = []engine.SearchHit{}
			}
			wruntime.EventsEmit(a.ctx, "search", e)
		}
		a.eng.Search(ctx, query, engine.SearchOptions{
			Sessions: []engine.Session{}, // the page names sessions from the list it has
			Each:     func(p engine.SearchPart) { emit(SearchEvent{SearchPart: p}) },
			Started:  func(machines []string) { emit(SearchEvent{Machines: machines}) },
		})
		emit(SearchEvent{End: true})
	}()
}

// SearchStop drops the search that is running.
func (a *App) SearchStop() {
	searching.Lock()
	defer searching.Unlock()
	if searching.stop != nil {
		searching.stop()
		searching.stop = nil
	}
}

// SearchShow scrolls a session to the latest place the words show in it (tmux's copy mode).
func (a *App) SearchShow(machine, session, query string) error {
	return a.eng.ShowMatch(a.ctx, machine, session, query)
}
