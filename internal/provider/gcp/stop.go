package gcp

import (
	"context"
	"fmt"
	"time"

	"skybuild/internal/model"
	"skybuild/internal/provider"
)

// guestTerminate is how Compute Engine records an instance that shut itself down ("Instance
// terminated by guest OS shutdown"); a stop asked for through the API is a plain "stop".
const guestTerminate = "compute.instances.guestTerminate"

type operation struct {
	OperationType string
	InsertTime    string
	EndTime       string
}

// StopInfo finds the instance's last stop in the zone's operations. Operations are kept for
// a couple of weeks; for an older stop the instance itself still knows when, but not how.
func (g *GCP) StopInfo(ctx context.Context, m *model.Machine) (provider.StopInfo, error) {
	var ops []operation
	filter := fmt.Sprintf(`targetLink~"/instances/%s$" AND (operationType=stop OR operationType=%s)`, m.InstanceID, guestTerminate)
	err := g.cli.JSON(ctx, &ops, "compute", "operations", "list", "--project", m.Account, "--zones", m.Zone,
		"--filter", filter, "--sort-by", "~insertTime", "--limit", "1", "--format", "json(operationType,insertTime,endTime)")
	if err != nil {
		return provider.StopInfo{}, err
	}
	if info, ok := stopFromOps(ops); ok {
		return info, nil
	}
	var inst struct{ LastStopTimestamp string }
	if err := g.cli.JSON(ctx, &inst, append([]string{"compute", "instances", "describe", m.InstanceID, "--format=json(lastStopTimestamp)"}, zoneArgs(m)...)...); err != nil {
		return provider.StopInfo{}, err
	}
	at, _ := time.Parse(time.RFC3339, inst.LastStopTimestamp)
	return provider.StopInfo{At: at}, nil
}

// stopFromOps reads the newest stop out of an operations listing (newest first).
func stopFromOps(ops []operation) (provider.StopInfo, bool) {
	if len(ops) == 0 {
		return provider.StopInfo{}, false
	}
	o := ops[0]
	at, err := time.Parse(time.RFC3339, o.EndTime)
	if err != nil {
		at, _ = time.Parse(time.RFC3339, o.InsertTime)
	}
	return provider.StopInfo{At: at, BySelf: o.OperationType == guestTerminate}, true
}
