package provider

import (
	"context"
	"time"

	"skybuild/internal/model"
)

// StopInfo is what a cloud knows about the last time a machine stopped.
type StopInfo struct {
	At     time.Time // when it stopped; zero when the cloud doesn't say
	BySelf bool      // the machine shut itself down, rather than being stopped through the cloud
}

// StopInspector is implemented by adapters whose cloud can tell the two apart. sky uses it to
// say "stopped after 2h idle" about a machine its own watchdog shut down, while the machine
// is off and can't be asked.
type StopInspector interface {
	StopInfo(ctx context.Context, m *model.Machine) (StopInfo, error)
}
