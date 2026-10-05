package main

import (
	"context"

	"skybuild/internal/engine"
	"skybuild/internal/events"
)

// DiscoverMachines lists machines sky can already see (ssh config, tailnet) but hasn't added.
func (a *App) DiscoverMachines() []engine.Candidate {
	c := a.eng.Discover(a.ctx)
	if c == nil {
		c = []engine.Candidate{}
	}
	return c
}

// OtherSync names another tool that already syncs a machine ("" when none).
func (a *App) OtherSync(name string) string { return a.eng.OtherSync(a.ctx, name) }

// TakeOverSync makes sky the one tool that syncs a machine.
func (a *App) TakeOverSync(name string) string {
	return a.ops.start("sync", "Take over syncing "+name, name, func(ctx context.Context, r events.Reporter) (any, error) {
		err := a.eng.TakeOver(ctx, name, r)
		a.emitMachines()
		return nil, err
	})
}
