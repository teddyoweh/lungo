package main

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"skybuild/internal/events"
	"skybuild/internal/osx"
)

// OpInfo is a long operation the UI tracks: created, streamed through "op" events and
// finished with "op:end".
type OpInfo struct {
	ID       string    `json:"id"`
	Kind     string    `json:"kind"`
	Title    string    `json:"title"`
	Machine  string    `json:"machine"`
	Started  time.Time `json:"started"`
	Ended    time.Time `json:"ended,omitempty"`
	Running  bool      `json:"running"`
	Error    string    `json:"error,omitempty"`
	LastLine string    `json:"lastLine,omitempty"`
}

// OpEvent is one progress line of an operation.
type OpEvent struct {
	Op      string    `json:"op"`
	Machine string    `json:"machine,omitempty"`
	Level   string    `json:"level"`
	Message string    `json:"message"`
	URL     string    `json:"url,omitempty"`
	Time    time.Time `json:"time"`
}

// OpEnd closes an operation.
type OpEnd struct {
	Op     string `json:"op"`
	Error  string `json:"error,omitempty"`
	Result any    `json:"result,omitempty"`
}

type opRunner struct {
	ctx  context.Context
	mu   sync.Mutex
	ops  map[string]*op
	next atomic.Int64
}

type op struct {
	info   OpInfo
	cancel context.CancelFunc
}

func newOpRunner(ctx context.Context) *opRunner { return &opRunner{ctx: ctx, ops: map[string]*op{}} }

// start runs fn in the background and returns its op id at once.
func (o *opRunner) start(kind, title, machine string, fn func(context.Context, events.Reporter) (any, error)) string {
	id := fmt.Sprintf("op%d", o.next.Add(1))
	ctx, cancel := context.WithCancel(o.ctx)
	x := &op{info: OpInfo{ID: id, Kind: kind, Title: title, Machine: machine, Started: time.Now(), Running: true}, cancel: cancel}
	o.mu.Lock()
	o.ops[id] = x
	o.prune()
	o.mu.Unlock()
	wruntime.EventsEmit(o.ctx, "op:start", x.info)

	r := events.Func(func(e events.Event) {
		if e.Level == events.Link && e.URL != "" {
			osx.OpenURL(e.URL)
		}
		if e.Level != events.Log {
			o.mu.Lock()
			x.info.LastLine = e.Message
			o.mu.Unlock()
		}
		wruntime.EventsEmit(o.ctx, "op", OpEvent{Op: id, Machine: e.Machine, Level: e.Level, Message: e.Message, URL: e.URL, Time: e.Time})
	})
	go func() {
		defer cancel()
		var res any
		var err error
		func() {
			defer func() {
				if p := recover(); p != nil {
					err = fmt.Errorf("internal error: %v", p)
				}
			}()
			res, err = fn(ctx, r)
		}()
		end := OpEnd{Op: id, Result: res}
		o.mu.Lock()
		x.info.Running, x.info.Ended = false, time.Now()
		if err != nil {
			end.Error = err.Error()
			if ctx.Err() == context.Canceled {
				end.Error = "cancelled"
			}
			x.info.Error = end.Error
		}
		o.mu.Unlock()
		wruntime.EventsEmit(o.ctx, "op:end", end)
	}()
	return id
}

func (o *opRunner) cancel(id string) {
	o.mu.Lock()
	x := o.ops[id]
	o.mu.Unlock()
	if x != nil {
		x.cancel()
	}
}

func (o *opRunner) list() []OpInfo {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]OpInfo, 0, len(o.ops))
	for _, x := range o.ops {
		out = append(out, x.info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started.After(out[j].Started) })
	return out
}

// prune forgets finished operations older than ten minutes (caller holds the lock).
func (o *opRunner) prune() {
	for id, x := range o.ops {
		if !x.info.Running && time.Since(x.info.Ended) > 10*time.Minute {
			delete(o.ops, id)
		}
	}
}
