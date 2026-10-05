package engine

import (
	"strings"
	"testing"
	"time"
)

// What the health command printed on a Compute Engine VM (Ubuntu 24.04, 2 vCPU, 8 GB, a
// separate 50 GB volume at /home).
const healthLinux = `os=Linux
now=1791097202
cpus=2
load=0.14 0.10 0.09 2/237 248341
uptime=22501.44 42886.92
mem MemTotal:        8124744 kB
mem MemFree:         5215404 kB
mem MemAvailable:    6543412 kB
df Filesystem     1024-blocks    Used Available Capacity Mounted on
df /dev/sdb          51290592  435952  50838256       1% /home
df /dev/root         29378688 8819752  20542552      31% /
`

// And on a Mac mini (macOS 26, 10 cores, 16 GB).
const healthMac = `os=Darwin
now=1791097202
cpus=10
load={ 1.80 1.59 1.60 }
memtotal=17179869184
boot={ sec = 1791070915, usec = 540599 } Sat Oct  3 19:41:55 2026
pressure=1
vm Mach Virtual Memory Statistics: (page size of 16384 bytes)
vm Pages free:                                     4881.
vm Pages active:                                 237954.
vm Pages inactive:                               237393.
vm Pages speculative:                              1063.
vm Pages throttled:                                   0.
vm Pages wired down:                             189871.
vm Pages purgeable:                                7898.
vm "Translation faults":                      285896081.
vm Pages copy-on-write:                        28866437.
vm Pages zero filled:                         149002017.
vm Pages reactivated:                          28502794.
vm Pages purged:                                3256720.
vm File-backed pages:                            175476.
vm Anonymous pages:                              300934.
vm Pages stored in compressor:                  1110542.
vm Pages occupied by compressor:                 342708.
vm Decompressions:                             42698421.
vm Compressions:                               64495867.
vm Pageins:                                    16535583.
vm Pageouts:                                     174210.
vm Swapins:                                     4449915.
vm Swapouts:                                    5629448.
df Filesystem   1024-blocks      Used Available Capacity  Mounted on
df /dev/disk3s5   239362496 185559360  28836652    87%    /System/Volumes/Data
`

func TestParseHealthLinux(t *testing.T) {
	h, err := parseHealth(healthLinux)
	if err != nil {
		t.Fatal(err)
	}
	if h.OS != "linux" || h.CPUs != 2 || h.Load != [3]float64{0.14, 0.10, 0.09} || h.CPUPct != 7 {
		t.Errorf("cpu: %+v", h)
	}
	// free(1) on the same machine: used = total - available.
	if h.MemTotal != 8124744*1024 || h.MemUsed != (8124744-6543412)*1024 || h.MemPct != 19 {
		t.Errorf("memory: total %d used %d pct %d", h.MemTotal, h.MemUsed, h.MemPct)
	}
	if h.Disk.Mount != "/home" || h.Disk.Pct != 1 || h.Disk.Used != 435952*1024 || h.Disk.Free != 50838256*1024 || h.Disk.Total != 51290592*1024 {
		t.Errorf("data volume: %+v", h.Disk)
	}
	if h.Root == nil || h.Root.Mount != "/" || h.Root.Pct != 31 { // df rounds 30.04% up, so do we
		t.Errorf("root: %+v", h.Root)
	}
	if h.UptimeSec != 22501 || len(h.Warnings) != 0 || h.Idle != nil {
		t.Errorf("uptime %d warnings %v idle %+v", h.UptimeSec, h.Warnings, h.Idle)
	}
}

func TestParseHealthMac(t *testing.T) {
	h, err := parseHealth(healthMac)
	if err != nil {
		t.Fatal(err)
	}
	if h.OS != "darwin" || h.CPUs != 10 || h.Load != [3]float64{1.80, 1.59, 1.60} || h.CPUPct != 18 {
		t.Errorf("cpu: %+v", h)
	}
	// App memory (anonymous less purgeable) + wired + compressed, in 16 KB pages.
	want := uint64(300934-7898+189871+342708) * 16384
	if h.MemTotal != 17179869184 || h.MemUsed != want || h.MemPct != 79 || h.Pressure != 1 {
		t.Errorf("memory: total %d used %d (want %d) pct %d pressure %d", h.MemTotal, h.MemUsed, want, h.MemPct, h.Pressure)
	}
	if h.Disk.Mount != "/System/Volumes/Data" || h.Disk.Pct != 87 || h.Root != nil {
		t.Errorf("disk: %+v root %+v", h.Disk, h.Root)
	}
	if h.UptimeSec != 1791097202-1791070915 {
		t.Errorf("uptime %d", h.UptimeSec)
	}
	// 79% used with normal pressure is how a Mac runs: no warning.
	if len(h.Warnings) != 0 {
		t.Errorf("warnings: %v", h.Warnings)
	}
}

func TestParseHealthWarnings(t *testing.T) {
	full := strings.NewReplacer(
		"df /dev/sdb          51290592  435952  50838256       1% /home", "df /dev/sdb          51290592  47000000  4290592      92% /home",
		"mem MemAvailable:    6543412 kB", "mem MemAvailable:     400000 kB",
	).Replace(healthLinux)
	h, err := parseHealth(full)
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Warnings) != 2 || !strings.HasPrefix(h.Warnings[0], "Disk 92% full (4.1 GB free on /home)") || !strings.HasPrefix(h.Warnings[1], "Memory 95% used") {
		t.Errorf("warnings: %q", h.Warnings)
	}
	// The system disk filling up is its own warning.
	root := strings.Replace(healthLinux, "df /dev/root         29378688 8819752  20542552      31% /", "df /dev/root         29378688 27500000  1862304      94% /", 1)
	if h, _ = parseHealth(root); len(h.Warnings) != 1 || !strings.HasPrefix(h.Warnings[0], "System disk 94% full") {
		t.Errorf("warnings: %q", h.Warnings)
	}
	// On a Mac the pressure level decides, not the share in use.
	if h, _ = parseHealth(strings.Replace(healthMac, "pressure=1", "pressure=2", 1)); len(h.Warnings) != 1 || !strings.HasPrefix(h.Warnings[0], "Memory pressure is high") {
		t.Errorf("warnings: %q", h.Warnings)
	}
}

// A home directory on the system disk makes df answer "/" twice: one disk, not two.
func TestParseHealthOneDisk(t *testing.T) {
	out := strings.Replace(healthLinux, "df /dev/sdb          51290592  435952  50838256       1% /home", "df /dev/root         29378688 8819752  20542552      31% /", 1)
	h, err := parseHealth(out)
	if err != nil {
		t.Fatal(err)
	}
	if h.Disk.Mount != "/" || h.Root != nil {
		t.Errorf("disk %+v root %+v", h.Disk, h.Root)
	}
}

func TestParseHealthIdle(t *testing.T) {
	out := healthLinux + `conf LIMIT_MIN=120
conf DRY_RUN=0
idle at=1791097100
idle active=0
idle since=1791093500
idle limit=120
idle dry=0
idle reason=
`
	h, err := parseHealth(out)
	if err != nil {
		t.Fatal(err)
	}
	if h.Idle == nil || h.Idle.Limit != 120 || h.Idle.DryRun || h.Idle.Active || h.Idle.At.Unix() != 1791097100 || h.Idle.Since.Unix() != 1791093500 {
		t.Fatalf("idle: %+v", h.Idle)
	}
	// Installed but not run yet: the setting is known, the decision isn't.
	h, _ = parseHealth(healthLinux + "conf LIMIT_MIN=60\nconf DRY_RUN=1\n")
	if h.Idle == nil || h.Idle.Limit != 60 || !h.Idle.DryRun || !h.Idle.At.IsZero() {
		t.Errorf("idle before the first run: %+v", h.Idle)
	}
	active := strings.Replace(out, "idle active=0", "idle active=1", 1)
	active = strings.Replace(active, "idle reason=", "idle reason=tmux client attached", 1)
	if h, _ = parseHealth(active); h.Idle == nil || !h.Idle.Active || h.Idle.Reason != "tmux client attached" {
		t.Errorf("active: %+v", h.Idle)
	}
}

func TestParseHealthGarbage(t *testing.T) {
	for _, out := range []string{"", "zsh: command not found: nproc\n", "os=Linux\ncpus=0\n"} {
		if _, err := parseHealth(out); err == nil {
			t.Errorf("%q should not parse", out)
		}
	}
}

func TestBytesAndUptime(t *testing.T) {
	for n, want := range map[uint64]string{435952 * 1024: "426 MB", 1619283968: "1.5 GB", 50838256 * 1024: "48 GB", 3 << 40: "3.0 TB"} {
		if got := Bytes(n); got != want {
			t.Errorf("Bytes(%d) = %q, want %q", n, got, want)
		}
	}
	for sec, want := range map[int64]string{59: "0m", 22501: "6h 15m", 26287: "7h 18m", 3*86400 + 4*3600: "3d 4h"} {
		if got := Uptime(sec); got != want {
			t.Errorf("Uptime(%d) = %q, want %q", sec, got, want)
		}
	}
}

func TestMonthCost(t *testing.T) {
	day := func(d, h int) time.Time { return time.Date(2026, 10, d, h, 0, 0, 0, time.UTC) }
	start, end := day(1, 0), day(1, 0).AddDate(0, 1, 0) // 31 days, 744 hours
	const hourly, disk = 0.10, 7.44                     // the volume costs a cent an hour

	// Ten days in, running the whole time: 240 hours, and the same pace to the end.
	soFar, month := monthCost(hourly, disk, 240, true, start, start, day(11, 0), end)
	if !near(soFar, 240*0.10+2.40) || !near(month, 744*0.10+7.44) {
		t.Errorf("always on: so far %.2f, month %.2f", soFar, month)
	}
	// Ten days in, up a quarter of the time (60 of 240 hours): the rest goes the same way.
	soFar, month = monthCost(hourly, disk, 60, false, start, start, day(11, 0), end)
	if !near(soFar, 60*0.10+2.40) || !near(month, (60+504*0.25)*0.10+7.44) {
		t.Errorf("a quarter of the time: so far %.2f, month %.2f", soFar, month)
	}
	// Created on the 21st at noon, three hours ago, running: too little to go on, so it is
	// taken to stay on. The volume is only paid for from the day it existed.
	from, now := day(21, 12), day(21, 15)
	soFar, month = monthCost(hourly, disk, 3, true, start, from, now, end)
	left := end.Sub(now).Hours()
	if !near(soFar, 3*0.10+0.03) || !near(month, (3+left)*0.10+disk*end.Sub(from).Hours()/744) {
		t.Errorf("new machine: so far %.2f, month %.2f", soFar, month)
	}
	// The same, but stopped: nothing more is expected.
	if _, month = monthCost(hourly, disk, 3, false, start, from, now, end); !near(month, 3*0.10+disk*end.Sub(from).Hours()/744) {
		t.Errorf("new machine, stopped: month %.2f", month)
	}
	// More hours than have passed can't be: it is capped.
	if soFar, _ = monthCost(hourly, 0, 500, true, start, start, day(11, 0), end); !near(soFar, 24) {
		t.Errorf("capped: %.2f", soFar)
	}
}

func near(a, b float64) bool { return a-b < 0.005 && b-a < 0.005 }

func TestRuns(t *testing.T) {
	t0 := time.Date(2026, 10, 3, 20, 0, 0, 0, time.UTC)
	at := func(min int) time.Time { return t0.Add(time.Duration(min) * time.Minute) }

	runs, write := addRun(nil, at(0), at(10))
	if !write || len(runs) != 1 {
		t.Fatalf("first sighting: %+v %v", runs, write)
	}
	// Half a minute later: the same run, not worth writing.
	if _, write = addRun(runs, at(0).Add(time.Second), at(10).Add(30*time.Second)); write {
		t.Error("a sighting half a minute later should not be written")
	}
	// Hours later, the boot time off by a second: still the same run, now longer.
	runs, write = addRun(runs, at(0).Add(-time.Second), at(300))
	if !write || len(runs) != 1 || !runs[0].End.Equal(at(300)) {
		t.Fatalf("same run, later: %+v %v", runs, write)
	}
	// It stopped and was started again: a second run.
	runs, write = addRun(runs, at(600), at(630))
	if !write || len(runs) != 2 {
		t.Fatalf("second run: %+v", runs)
	}
	if got := hoursWithin(runs, at(0), at(700)); !near(got, 5.5) {
		t.Errorf("all of it: %.2f hours, want 5.5", got)
	}
	if got := hoursWithin(runs, at(240), at(615)); !near(got, 1.25) {
		t.Errorf("a window: %.2f hours, want 1.25", got)
	}
	// Runs that overlap (a clock was set) are not counted twice.
	if got := hoursWithin([]upRun{{at(0), at(120)}, {at(60), at(180)}}, at(0), at(600)); !near(got, 3) {
		t.Errorf("overlap: %.2f hours, want 3", got)
	}
	// Old runs are dropped once a new sighting is written.
	old := []upRun{{at(-100 * 24 * 60), at(-99 * 24 * 60)}}
	if runs, _ = addRun(old, at(0), at(5)); len(runs) != 1 || !runs[0].Start.Equal(at(0)) {
		t.Errorf("pruning: %+v", runs)
	}
}
