package aws

import (
	"context"
	"regexp"
	"time"

	"skybuild/internal/model"
	"skybuild/internal/provider"
)

// selfStopped is the reason EC2 gives for an instance that shut itself down; a stop asked for
// through the API is "Client.UserInitiatedShutdown".
const selfStopped = "Client.InstanceInitiatedShutdown"

// "User initiated (2026-10-04 07:10:11 GMT)"
var transitionTime = regexp.MustCompile(`\((\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}) GMT\)`)

type stopReason struct {
	Reason string `json:"reason"` // StateTransitionReason
	Code   string `json:"code"`   // StateReason.Code
}

// StopInfo reads why and when the instance last changed state.
func (a *AWS) StopInfo(ctx context.Context, m *model.Machine) (provider.StopInfo, error) {
	var r stopReason
	err := a.json(ctx, m.Account, m.Region, &r, "ec2", "describe-instances", "--instance-ids", m.InstanceID,
		"--query", "Reservations[0].Instances[0].{reason:StateTransitionReason,code:StateReason.Code}")
	if err != nil {
		return provider.StopInfo{}, err
	}
	return stopFromReason(r), nil
}

func stopFromReason(r stopReason) provider.StopInfo {
	info := provider.StopInfo{BySelf: r.Code == selfStopped}
	if t := transitionTime.FindStringSubmatch(r.Reason); t != nil {
		info.At, _ = time.ParseInLocation("2006-01-02 15:04:05", t[1], time.UTC)
	}
	return info
}
