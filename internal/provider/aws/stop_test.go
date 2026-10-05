package aws

import (
	"testing"
	"time"
)

func TestStopFromReason(t *testing.T) {
	for _, c := range []struct {
		r      stopReason
		bySelf bool
		at     string
	}{
		{stopReason{"User initiated (2026-10-04 07:10:11 GMT)", "Client.InstanceInitiatedShutdown"}, true, "2026-10-04T07:10:11Z"},
		{stopReason{"User initiated (2026-10-04 07:10:11 GMT)", "Client.UserInitiatedShutdown"}, false, "2026-10-04T07:10:11Z"},
		{stopReason{"User initiated shutdown", "Client.InstanceInitiatedShutdown"}, true, ""}, // no time given: none claimed
		{stopReason{}, false, ""},
	} {
		got := stopFromReason(c.r)
		at := ""
		if !got.At.IsZero() {
			at = got.At.UTC().Format(time.RFC3339)
		}
		if got.BySelf != c.bySelf || at != c.at {
			t.Errorf("%+v: %+v", c.r, got)
		}
	}
}
