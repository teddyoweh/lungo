package gcp

import (
	"encoding/json"
	"testing"
	"time"
)

func TestStopFromOps(t *testing.T) {
	// As `gcloud compute operations list --format=json(operationType,insertTime,endTime)` prints them.
	guest := `[{"endTime":"2026-10-03T23:57:23.373-07:00","insertTime":"2026-10-03T23:57:23.373-07:00","operationType":"compute.instances.guestTerminate"}]`
	api := `[{"endTime":"2026-10-03T17:44:42.040-07:00","insertTime":"2026-10-03T17:43:49.371-07:00","operationType":"stop"}]`
	for _, c := range []struct {
		raw    string
		bySelf bool
		at     string
	}{
		{guest, true, "2026-10-04T06:57:23Z"},
		{api, false, "2026-10-04T00:44:42Z"},
	} {
		var ops []operation
		if err := json.Unmarshal([]byte(c.raw), &ops); err != nil {
			t.Fatal(err)
		}
		got, ok := stopFromOps(ops)
		if !ok || got.BySelf != c.bySelf || got.At.UTC().Format(time.RFC3339) != c.at {
			t.Errorf("%s: %+v ok=%v", c.raw, got, ok)
		}
	}
	if _, ok := stopFromOps(nil); ok {
		t.Error("no operations should mean no answer")
	}
}
