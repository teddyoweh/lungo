package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"skybuild/internal/model"
	"skybuild/internal/paths"
	"skybuild/internal/provider"
	"skybuild/internal/sshx"
)

// Health is how a machine is doing right now: processor, memory, disk and how long it has
// been up. One ssh command collects it, on Linux and on macOS.
type Health struct {
	Machine string     `json:"machine"`
	At      time.Time  `json:"at"`
	OS      string     `json:"os"` // linux | darwin
	CPUs    int        `json:"cpus"`
	Load    [3]float64 `json:"load"`   // 1, 5 and 15 minutes
	CPUPct  int        `json:"cpuPct"` // the 1-minute load against the cores; above 100 when work is queueing

	MemTotal uint64 `json:"memTotal"` // bytes
	MemUsed  uint64 `json:"memUsed"`
	MemPct   int    `json:"memPct"`
	Pressure int    `json:"pressure,omitempty"` // macOS memory pressure: 1 normal, 2 warning, 4 critical

	Disk Disk  `json:"disk"`           // the data volume: /home on a sky machine, the user's volume on a Mac
	Root *Disk `json:"root,omitempty"` // the system disk, when it is a separate one worth watching

	UptimeSec int64    `json:"uptimeSec"`
	Warnings  []string `json:"warnings"` // what is nearly full, in words; empty when all is well

	Idle  *IdleState `json:"idle,omitempty"`  // what the stop-when-idle watchdog last decided, when it is installed
	Error string     `json:"error,omitempty"` // the machine didn't answer
}

// Disk is one filesystem.
type Disk struct {
	Mount string `json:"mount"`
	Total uint64 `json:"total"` // bytes
	Used  uint64 `json:"used"`
	Free  uint64 `json:"free"`
	Pct   int    `json:"pct"` // used out of used + free, rounded up, as df counts it
}

// IdleState is the watchdog's view from the machine itself (see syncer.IdleWatchdog).
type IdleState struct {
	Limit  int       `json:"limit"` // minutes, as installed on the machine
	DryRun bool      `json:"dryRun,omitempty"`
	At     time.Time `json:"at,omitzero"`      // when it last looked; zero before its first run
	Active bool      `json:"active"`           // something was going on then
	Reason string    `json:"reason,omitempty"` // what: "tmux client attached"
	Since  time.Time `json:"since,omitzero"`   // idle since
}

// NearlyFull is the share of a disk or of memory from which the UI warns.
const NearlyFull = 90

// healthCmd prints everything as "key=value" and prefixed lines, so one parser reads both
// systems. The disk is asked about by path: $HOME sits on the data volume of a sky machine
// and on the user volume of a Mac.
const healthCmd = `os=$(uname -s); echo "os=$os"; echo "now=$(date +%s)"
if [ "$os" = Darwin ]; then
  echo "cpus=$(sysctl -n hw.ncpu)"; echo "load=$(sysctl -n vm.loadavg)"
  echo "memtotal=$(sysctl -n hw.memsize)"; echo "boot=$(sysctl -n kern.boottime)"
  echo "pressure=$(sysctl -n kern.memorystatus_vm_pressure_level 2>/dev/null)"
  vm_stat | sed 's/^/vm /'
  df -Pk "$HOME" 2>/dev/null | sed 's/^/df /'
else
  echo "cpus=$(nproc 2>/dev/null || getconf _NPROCESSORS_ONLN)"; echo "load=$(cat /proc/loadavg)"
  echo "uptime=$(cat /proc/uptime)"
  grep -E '^(MemTotal|MemAvailable|MemFree):' /proc/meminfo | sed 's/^/mem /'
  df -Pk "$HOME" / 2>/dev/null | sed 's/^/df /'
  [ -r /etc/skybuild/idle.conf ] && grep -E '^(LIMIT_MIN|DRY_RUN)=' /etc/skybuild/idle.conf | sed 's/^/conf /'
  [ -r /var/lib/skybuild/idle/state ] && sed 's/^/idle /' /var/lib/skybuild/idle/state
fi
true`

// Health asks one machine how it is doing.
func (e *Engine) Health(ctx context.Context, name string) (Health, error) {
	m, err := e.Machine(name)
	if err != nil {
		return Health{}, err
	}
	if err := e.Ready(m); err != nil {
		return Health{}, err
	}
	return e.healthOn(ctx, m)
}

func (e *Engine) healthOn(ctx context.Context, m *model.Machine) (Health, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := sshx.Run(ctx, e.Target(m), healthCmd)
	if err != nil {
		return Health{}, err
	}
	h, err := parseHealth(out)
	if err != nil {
		return Health{}, fmt.Errorf("%s: %w", m.Name, err)
	}
	h.Machine, h.At = m.Name, time.Now()
	if m.IsCloud() && h.UptimeSec > 0 {
		recordUp(m.Name, h.At.Add(-time.Duration(h.UptimeSec)*time.Second), h.At)
	}
	return h, nil
}

// HealthAll asks every running machine at once. A machine that doesn't answer is in the
// result with Error set; machines that are stopped are left out.
func (e *Engine) HealthAll(ctx context.Context) map[string]Health {
	all, _ := e.Machines()
	out := map[string]Health{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, m := range all {
		if m.Status != model.StatusRunning {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			h, err := e.healthOn(ctx, m)
			if err != nil {
				h = Health{Machine: m.Name, At: time.Now(), Warnings: []string{}, Error: err.Error()}
			}
			mu.Lock()
			out[m.Name] = h
			mu.Unlock()
		}()
	}
	wg.Wait()
	return out
}

var (
	bootSec  = regexp.MustCompile(`sec = (\d+)`)
	pageSize = regexp.MustCompile(`page size of (\d+) bytes`)
)

// parseHealth reads healthCmd's output.
func parseHealth(out string) (Health, error) {
	h := Health{Warnings: []string{}}
	var now, boot int64
	var memAvail, memFree uint64
	haveAvail := false
	vm := map[string]uint64{}
	page := uint64(4096)
	var disks []Disk
	idle := map[string]string{}
	conf := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case strings.HasPrefix(line, "vm "):
			if m := pageSize.FindStringSubmatch(line); m != nil {
				page, _ = strconv.ParseUint(m[1], 10, 64)
				continue
			}
			k, v, ok := strings.Cut(line[3:], ":")
			if ok {
				vm[strings.Trim(k, `" `)], _ = strconv.ParseUint(strings.Trim(v, " ."), 10, 64)
			}
		case strings.HasPrefix(line, "mem "):
			f := strings.Fields(line[4:]) // "MemTotal: 8124744 kB"
			if len(f) < 2 {
				continue
			}
			kb, _ := strconv.ParseUint(f[1], 10, 64)
			switch f[0] {
			case "MemTotal:":
				h.MemTotal = kb * 1024
			case "MemAvailable:":
				memAvail, haveAvail = kb*1024, true
			case "MemFree:":
				memFree = kb * 1024
			}
		case strings.HasPrefix(line, "df "):
			f := strings.Fields(line[3:]) // device, 1024-blocks, used, available, capacity, mount point
			if len(f) < 6 || f[0] == "Filesystem" {
				continue
			}
			total, e1 := strconv.ParseUint(f[1], 10, 64)
			used, e2 := strconv.ParseUint(f[2], 10, 64)
			free, e3 := strconv.ParseUint(f[3], 10, 64)
			if e1 != nil || e2 != nil || e3 != nil {
				continue
			}
			d := Disk{Mount: strings.Join(f[5:], " "), Total: total * 1024, Used: used * 1024, Free: free * 1024}
			if used+free > 0 {
				d.Pct = int(math.Ceil(float64(used) * 100 / float64(used+free)))
			}
			disks = append(disks, d)
		case strings.HasPrefix(line, "idle "):
			if k, v, ok := strings.Cut(line[5:], "="); ok {
				idle[k] = v
			}
		case strings.HasPrefix(line, "conf "):
			if k, v, ok := strings.Cut(line[5:], "="); ok {
				conf[k] = strings.TrimSpace(v)
			}
		default:
			k, v, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			v = strings.TrimSpace(v)
			switch k {
			case "os":
				h.OS = strings.ToLower(v)
			case "now":
				now, _ = strconv.ParseInt(v, 10, 64)
			case "cpus":
				h.CPUs, _ = strconv.Atoi(v)
			case "load": // "0.07 0.29 0.16 1/247 231297" or "{ 1.89 1.77 1.69 }"
				f := strings.Fields(strings.Trim(v, "{} "))
				for i := 0; i < 3 && i < len(f); i++ {
					h.Load[i], _ = strconv.ParseFloat(strings.Replace(f[i], ",", ".", 1), 64)
				}
			case "uptime": // "21703.04 41396.95"
				if f := strings.Fields(v); len(f) > 0 {
					up, _ := strconv.ParseFloat(f[0], 64)
					h.UptimeSec = int64(up)
				}
			case "boot": // "{ sec = 1791070915, usec = 540599 } Sat Oct  3 19:41:55 2026"
				if m := bootSec.FindStringSubmatch(v); m != nil {
					boot, _ = strconv.ParseInt(m[1], 10, 64)
				}
			case "memtotal":
				h.MemTotal, _ = strconv.ParseUint(v, 10, 64)
			case "pressure":
				h.Pressure, _ = strconv.Atoi(v)
			}
		}
	}
	if h.OS == "" || h.CPUs <= 0 || h.MemTotal == 0 {
		return h, fmt.Errorf("unexpected reply to the health check")
	}
	h.CPUPct = int(math.Round(h.Load[0] * 100 / float64(h.CPUs)))

	switch h.OS {
	case "darwin":
		// What Activity Monitor calls Memory Used: app memory (anonymous pages that can't
		// simply be dropped), wired memory and what the compressor holds.
		app := vm["Anonymous pages"]
		if p := vm["Pages purgeable"]; p < app {
			app -= p
		}
		if _, ok := vm["Anonymous pages"]; !ok {
			app = vm["Pages active"]
		}
		h.MemUsed = (app + vm["Pages wired down"] + vm["Pages occupied by compressor"]) * page
		if boot > 0 && now > boot {
			h.UptimeSec = now - boot
		}
	default:
		avail := memAvail
		if !haveAvail {
			avail = memFree
		}
		if avail < h.MemTotal {
			h.MemUsed = h.MemTotal - avail
		}
	}
	if h.MemUsed > h.MemTotal {
		h.MemUsed = h.MemTotal
	}
	h.MemPct = int(math.Round(float64(h.MemUsed) * 100 / float64(h.MemTotal)))

	if len(disks) > 0 {
		h.Disk = disks[0]
	}
	// The second line is "/", shown when it is a different filesystem. (df answers for the
	// filesystem a path is on, so a home directory on the system disk gives "/" twice.)
	if len(disks) > 1 && disks[1].Mount != disks[0].Mount {
		root := disks[1]
		h.Root = &root
	}

	if v, ok := conf["LIMIT_MIN"]; ok {
		st := IdleState{DryRun: conf["DRY_RUN"] == "1"}
		st.Limit, _ = strconv.Atoi(v)
		if at, _ := strconv.ParseInt(idle["at"], 10, 64); at > 0 {
			st.At = time.Unix(at, 0)
			st.Active = idle["active"] == "1"
			st.Reason = idle["reason"]
			if since, _ := strconv.ParseInt(idle["since"], 10, 64); since > 0 {
				st.Since = time.Unix(since, 0)
			}
		}
		if st.Limit > 0 {
			h.Idle = &st
		}
	}
	h.Warnings = healthWarnings(h)
	return h, nil
}

// healthWarnings says what is nearly full. On macOS the share of memory in use says little
// (the system fills memory on purpose and compresses when it needs room), so there the
// kernel's own pressure level decides.
func healthWarnings(h Health) []string {
	w := []string{}
	if h.Disk.Pct >= NearlyFull {
		w = append(w, fmt.Sprintf("Disk %d%% full (%s free on %s)", h.Disk.Pct, Bytes(h.Disk.Free), h.Disk.Mount))
	}
	if h.Root != nil && h.Root.Pct >= NearlyFull {
		w = append(w, fmt.Sprintf("System disk %d%% full (%s free on %s)", h.Root.Pct, Bytes(h.Root.Free), h.Root.Mount))
	}
	switch {
	case h.OS == "darwin" && h.Pressure >= 2:
		w = append(w, fmt.Sprintf("Memory pressure is high (%d%% used)", h.MemPct))
	case h.OS != "darwin" && h.MemPct >= NearlyFull:
		w = append(w, fmt.Sprintf("Memory %d%% used (%s of %s)", h.MemPct, Bytes(h.MemUsed), Bytes(h.MemTotal)))
	}
	return w
}

// Bytes writes a size for people: "435 MB", "7.7 GB", "1.2 TB" (binary units, like df -h).
func Bytes(n uint64) string {
	const gb = 1 << 30
	switch {
	case n >= 1024*gb:
		return fmt.Sprintf("%.1f TB", float64(n)/(1024*gb))
	case n >= 10*gb:
		return fmt.Sprintf("%.0f GB", float64(n)/gb)
	case n >= gb:
		return fmt.Sprintf("%.1f GB", float64(n)/gb)
	}
	return fmt.Sprintf("%.0f MB", float64(n)/(1<<20))
}

// Uptime writes a duration in seconds the way `uptime` does, shorter: "6h 1m", "3d 4h".
func Uptime(sec int64) string {
	d, h, m := sec/86400, sec%86400/3600, sec%3600/60
	switch {
	case d > 0:
		return fmt.Sprintf("%dd %dh", d, h)
	case h > 0:
		return fmt.Sprintf("%dh %dm", h, m)
	}
	return fmt.Sprintf("%dm", m)
}

// ---------- what it costs ----------

// Cost is what a cloud machine costs. It is only given for a size whose price sky knows.
type Cost struct {
	Hourly      float64 `json:"hourly"`          // compute while it runs, USD
	DiskMonthly float64 `json:"diskMonthly"`     // the data volume, running or not
	UpHours     float64 `json:"upHours"`         // how long it was seen running this month
	SoFar       float64 `json:"soFar"`           // this month up to now: those hours plus the volume
	Month       float64 `json:"month"`           // the whole month, if it keeps running as much as it has
	Basis       string  `json:"basis,omitempty"` // the region the prices are for, when the machine is elsewhere
}

// hoursPerMonth is what the size catalogues use to turn an hourly price into a monthly one.
const hoursPerMonth = 730

// volumePerGB is the estimated USD per GB-month of a data volume.
var volumePerGB = map[string]float64{model.ProviderGCP: 0.10, model.ProviderAWS: 0.08, model.ProviderAzure: 0.075}

// Cost estimates what a machine costs per hour and over this month. The price comes from the
// provider's size catalogue; a size that isn't in it (one picked by hand) has no known price,
// and then there is no estimate rather than a guess.
func (e *Engine) Cost(m *model.Machine, now time.Time) (Cost, bool) {
	p, err := e.Provider(m.Provider)
	if err != nil {
		return Cost{}, false
	}
	size, ok := provider.SizeByID(p.Sizes(), m.Size)
	if !ok || size.Monthly <= 0 {
		return Cost{}, false
	}
	c := Cost{Hourly: size.Monthly / hoursPerMonth, DiskMonthly: float64(m.DiskGB) * volumePerGB[m.Provider]}
	if m.Region != "" && m.Region != p.DefaultRegion() {
		c.Basis = p.DefaultRegion()
	}
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	end := start.AddDate(0, 1, 0)
	from := start
	if m.CreatedAt.After(from) {
		from = m.CreatedAt
	}
	if from.After(now) {
		from = now
	}
	c.UpHours = upHours(m.Name, from, now)
	c.SoFar, c.Month = monthCost(c.Hourly, c.DiskMonthly, c.UpHours, m.Status == model.StatusRunning, start, from, now, end)
	return c, true
}

// monthCost turns hours seen running into this month's bill so far and an estimate for the
// whole month. The rest of the month is assumed to go like the part that has passed: a
// machine that ran a third of the time keeps running a third of the time. With less than
// six hours to go on, it is assumed to stay as it is now.
func monthCost(hourly, diskMonthly, up float64, running bool, start, from, now, end time.Time) (soFar, month float64) {
	total := end.Sub(start).Hours()
	elapsed := now.Sub(from).Hours()
	if up > elapsed {
		up = elapsed
	}
	share := 0.0
	switch {
	case elapsed >= 6:
		share = up / elapsed
	case running:
		share = 1
	}
	disk := func(until time.Time) float64 { return diskMonthly * until.Sub(from).Hours() / total }
	soFar = up*hourly + disk(now)
	month = (up+end.Sub(now).Hours()*share)*hourly + disk(end)
	return soFar, month
}

// ---------- when it was running ----------
//
// The cloud doesn't say how many hours a machine ran this month, so sky keeps its own note:
// every health check says how long the machine has been up, which gives the start of the
// current run however long ago this computer last looked. Only a run that started and ended
// without sky ever seeing the machine up is missed.

type upRun struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

var runsMu sync.Mutex

func runsPath(name string) string { return filepath.Join(paths.State(), name+".runs.json") }

func loadRuns(name string) []upRun {
	var runs []upRun
	if b, err := os.ReadFile(runsPath(name)); err == nil {
		_ = json.Unmarshal(b, &runs)
	}
	return runs
}

func saveRuns(name string, runs []upRun) {
	b, _ := json.Marshal(runs)
	tmp := runsPath(name) + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, runsPath(name))
	}
}

// recordUp notes that the machine has been running from booted until seen.
func recordUp(name string, booted, seen time.Time) {
	runsMu.Lock()
	defer runsMu.Unlock()
	runs := loadRuns(name)
	next, write := addRun(runs, booted, seen)
	if write {
		saveRuns(name, next)
	}
}

// addRun merges a sighting into the runs. The boot time worked out from an uptime moves by a
// second or two between checks, so a run is recognised by a start within two minutes. It
// reports whether the result is worth writing down: a new run, or one that grew by minutes.
func addRun(runs []upRun, booted, seen time.Time) ([]upRun, bool) {
	const slack = 2 * time.Minute
	write := false
	found := false
	for i := range runs {
		if d := runs[i].Start.Sub(booted); d < slack && d > -slack {
			found = true
			if seen.Sub(runs[i].End) >= slack {
				runs[i].End, write = seen, true
			}
			break
		}
	}
	if !found {
		runs, write = append(runs, upRun{Start: booted, End: seen}), true
	}
	if !write {
		return runs, false
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].Start.Before(runs[j].Start) })
	keep := runs[:0]
	for _, r := range runs {
		if seen.Sub(r.End) < 62*24*time.Hour {
			keep = append(keep, r)
		}
	}
	return keep, true
}

// closeRun notes when the machine stopped (the cloud's own time for it): the run it was in
// lasted until then, not just until sky last looked.
func closeRun(name string, stopped time.Time) {
	if stopped.IsZero() {
		return
	}
	runsMu.Lock()
	defer runsMu.Unlock()
	runs := loadRuns(name)
	for i := len(runs) - 1; i >= 0; i-- {
		if runs[i].Start.Before(stopped) {
			if stopped.After(runs[i].End) {
				runs[i].End = stopped
				saveRuns(name, runs)
			}
			return
		}
	}
}

// upHours adds up how long the machine was seen running between from and to.
func upHours(name string, from, to time.Time) float64 {
	runsMu.Lock()
	runs := loadRuns(name)
	runsMu.Unlock()
	return hoursWithin(runs, from, to)
}

func hoursWithin(runs []upRun, from, to time.Time) float64 {
	var sum time.Duration
	var covered time.Time // runs are sorted by start; never count an overlap twice
	for _, r := range runs {
		s, e := r.Start, r.End
		if s.Before(from) {
			s = from
		}
		if s.Before(covered) {
			s = covered
		}
		if e.After(to) {
			e = to
		}
		if e.After(s) {
			sum += e.Sub(s)
			covered = e
		}
	}
	return sum.Hours()
}
