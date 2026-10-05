package engine

import (
	"context"
	"sync"

	"skybuild/internal/config"
	"skybuild/internal/events"
	"skybuild/internal/model"
	"skybuild/internal/syncer"
)

// SyncMachine pushes this computer's setup to one machine.
func (e *Engine) SyncMachine(ctx context.Context, m *model.Machine, opts syncer.Options, r events.Reporter) syncer.Result {
	s, _ := e.Settings()
	if opts.Claude == nil {
		opts.Claude = e.activeLogin()
	}
	if opts.APIKeys == nil {
		opts.APIKeys = e.managedKeys()
	}
	return syncer.Run(ctx, m, e.Target(m), s, opts, events.Tagged(r, "sync", m.Name))
}

// Sync pushes to the named machines (all of them when names is empty), four at a time.
func (e *Engine) Sync(ctx context.Context, names []string, opts syncer.Options, r events.Reporter) []syncer.Result {
	all, err := e.Machines()
	if err != nil {
		return []syncer.Result{{Errors: []string{err.Error()}}}
	}
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	var targets []*model.Machine
	for _, m := range all {
		if len(want) == 0 || want[m.Name] {
			targets = append(targets, m)
		}
	}
	if opts.Claude == nil {
		opts.Claude = e.activeLogin()
	}
	if opts.APIKeys == nil {
		opts.APIKeys = e.managedKeys()
	}
	results := make([]syncer.Result, len(targets))
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for i, m := range targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i] = e.SyncMachine(ctx, m, opts, r)
		}()
	}
	wg.Wait()
	return results
}

// SetSyncItems chooses what syncs to a machine.
func (e *Engine) SetSyncItems(name string, items []string) error {
	return config.Update(func(c *config.Config) error {
		m := c.Machine(name)
		if m == nil {
			return errNoMachine(name)
		}
		m.Sync = items
		return nil
	})
}

// LastSync returns the stored result of the last sync of a machine (nil if never).
func (e *Engine) LastSync(name string) *syncer.Result { return syncer.LoadState(name).Last }

func forgetSyncState(name string) { syncer.Forget(name) }

type errNoMachine string

func (e errNoMachine) Error() string { return "no machine named " + string(e) }
