package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"skybuild/internal/config"
	"skybuild/internal/engine"
	"skybuild/internal/events"
	"skybuild/internal/model"
	"skybuild/internal/syncer"
)

// ---------- health ----------

// HealthReport is `sky health --json`: each machine with what is known about it.
type HealthReport struct {
	Machine string          `json:"machine"`
	Status  string          `json:"status"`
	Health  *engine.Health  `json:"health,omitempty"`
	Idle    engine.IdleInfo `json:"idle"`
	Cost    *engine.Cost    `json:"cost,omitempty"`
}

func healthCmd() *cobra.Command {
	var cached bool
	c := &cobra.Command{
		Use:   "health [machine]",
		Short: "Show processor, memory, disk and uptime of your machines",
		Long: `Show how machines are doing: the load against the cores, memory in use, how full the data
volume (/home) and the system disk are, and how long each has been up. With a machine's name
it also shows what the machine costs and what its stop-when-idle watchdog last decided.`,
		Example: `  sky health
  sky health box`,
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeMachines,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if len(args) == 1 {
				if _, err := eng.Machine(args[0]); err != nil {
					return err
				}
			}
			var ms []*model.Machine
			var err error
			if cached {
				ms, err = eng.Machines()
			} else {
				ms, err = eng.Refresh(ctx, args...)
			}
			if err != nil {
				return err
			}
			eng.NoteStops(ctx)
			health := eng.HealthAll(ctx)
			var reports []HealthReport
			for _, m := range ms {
				if len(args) == 1 && m.Name != args[0] {
					continue
				}
				r := HealthReport{Machine: m.Name, Status: m.Status}
				if h, ok := health[m.Name]; ok {
					r.Health = &h
				}
				r.Idle, _ = eng.Idle(m.Name)
				if c, ok := eng.Cost(m, time.Now()); ok {
					r.Cost = &c
				}
				reports = append(reports, r)
			}
			if jsonOut {
				return printJSON(reports)
			}
			if len(reports) == 0 {
				fmt.Println(hint("No machines yet. Create one with `sky new`."))
				return nil
			}
			if len(args) == 1 {
				healthDetail(reports[0])
				return nil
			}
			healthTable(reports)
			return nil
		},
	}
	c.Flags().BoolVar(&cached, "cached", false, "skip asking the clouds whether each machine is running")
	return c
}

// pct renders a share with its bar: amber from 80%, red once it is nearly full.
func pct(p int) string {
	style := sGreen
	switch {
	case p >= engine.NearlyFull:
		style = sRed
	case p >= 80:
		style = sAmber
	}
	n := min(max((p*6+50)/100, 0), 6)
	if p > 0 && n == 0 {
		n = 1
	}
	return style.Render(strings.Repeat("█", n)) + sDim.Render(strings.Repeat("░", 6-n)) + fmt.Sprintf(" %3d%%", p)
}

func idleWord(i engine.IdleInfo, h *engine.Health) string {
	switch {
	case !i.Offered:
		return sDim.Render("—")
	case i.Minutes == 0:
		return sDim.Render("off")
	}
	s := syncer.IdleLimit(i.Minutes)
	if i.DryRun {
		s += " dry run"
	}
	if h == nil || h.Idle == nil || h.Idle.At.IsZero() {
		return s
	}
	if h.Idle.Active {
		return s + sDim.Render(" · active")
	}
	return s + sDim.Render(fmt.Sprintf(" · idle %s", syncer.IdleLimit(int(time.Since(h.Idle.Since).Minutes()))))
}

func healthTable(reports []HealthReport) {
	var rows [][]string
	var notes []string
	for _, r := range reports {
		h := r.Health
		if h == nil || h.Error != "" {
			what := statusDot(r.Status)
			if h != nil {
				what = sRed.Render("●") + " no answer"
				notes = append(notes, sAmber.Render("  ! ")+r.Machine+": "+h.Error)
			} else if r.Idle.Note != "" {
				notes = append(notes, sDim.Render("  · "+r.Machine+": "+r.Idle.Note))
			}
			rows = append(rows, []string{sBold.Render(r.Machine), what, "", "", "", "", idleWord(r.Idle, nil)})
			continue
		}
		root := sDim.Render("—")
		if h.Root != nil {
			root = pct(h.Root.Pct)
		}
		rows = append(rows, []string{
			sBold.Render(r.Machine),
			pct(h.CPUPct) + sDim.Render(fmt.Sprintf("  load %.2f", h.Load[0])),
			pct(h.MemPct) + sDim.Render(fmt.Sprintf("  %s of %s", engine.Bytes(h.MemUsed), engine.Bytes(h.MemTotal))),
			pct(h.Disk.Pct) + sDim.Render(fmt.Sprintf("  %s free", engine.Bytes(h.Disk.Free))),
			root,
			engine.Uptime(h.UptimeSec),
			idleWord(r.Idle, h),
		})
		for _, w := range h.Warnings {
			notes = append(notes, sAmber.Render("  ! ")+r.Machine+": "+w)
		}
	}
	table(os.Stdout, []string{"name", "cpu", "memory", "disk", "system", "up", "idle stop"}, rows)
	if len(notes) > 0 {
		fmt.Println()
		fmt.Println(strings.Join(notes, "\n"))
	}
}

func healthDetail(r HealthReport) {
	row := func(k, v string) { fmt.Printf("  %-9s%s\n", k, v) }
	fmt.Println()
	fmt.Println("  " + sBold.Render(r.Machine) + "  " + statusDot(r.Status))
	fmt.Println()
	h := r.Health
	switch {
	case h == nil:
		if r.Idle.Note != "" {
			row("Stopped", r.Idle.Note)
		}
	case h.Error != "":
		row("", sAmber.Render("No answer: ")+h.Error)
	default:
		row("CPU", pct(h.CPUPct)+sDim.Render(fmt.Sprintf("  load %.2f %.2f %.2f on %d cores", h.Load[0], h.Load[1], h.Load[2], h.CPUs)))
		row("Memory", pct(h.MemPct)+sDim.Render(fmt.Sprintf("  %s of %s", engine.Bytes(h.MemUsed), engine.Bytes(h.MemTotal))))
		row("Disk", pct(h.Disk.Pct)+sDim.Render(fmt.Sprintf("  %s used, %s free on %s", engine.Bytes(h.Disk.Used), engine.Bytes(h.Disk.Free), h.Disk.Mount)))
		if h.Root != nil {
			row("System", pct(h.Root.Pct)+sDim.Render(fmt.Sprintf("  %s used, %s free on %s", engine.Bytes(h.Root.Used), engine.Bytes(h.Root.Free), h.Root.Mount)))
		}
		row("Up", engine.Uptime(h.UptimeSec))
		for _, w := range h.Warnings {
			row("", sAmber.Render("! ")+w)
		}
	}
	if r.Idle.Offered {
		row("Idle", idleSentence(r.Idle, h))
	}
	if c := r.Cost; c != nil {
		s := fmt.Sprintf("$%.3f/h running", c.Hourly)
		if c.DiskMonthly > 0 {
			s += fmt.Sprintf(" + %s/mo for the volume", money(c.DiskMonthly))
		}
		s += fmt.Sprintf(" · ≈ %s this month", money(c.Month))
		detail := fmt.Sprintf("%s so far, seen running %s", money(c.SoFar), hours(c.UpHours))
		if c.Basis != "" {
			detail += ", at " + c.Basis + " prices"
		}
		row("Cost", s+sDim.Render("  ("+detail+")"))
	}
	fmt.Println()
}

// hours writes a number of hours: a decimal while they are few.
func hours(h float64) string {
	if h < 10 {
		return fmt.Sprintf("%.1f h", h)
	}
	return fmt.Sprintf("%.0f h", h)
}

// idleSentence says what stop-when-idle is doing on a machine, from the setting and (when
// the machine is running) its watchdog's last decision.
func idleSentence(i engine.IdleInfo, h *engine.Health) string {
	if i.Minutes == 0 {
		return sDim.Render("never stops by itself (sky idle " + i.Machine + " 2h turns that on)")
	}
	s := "stops after " + syncer.IdleLimit(i.Minutes) + " with nothing going on"
	if i.DryRun {
		s += " (dry run: it only logs)"
	}
	if h == nil || h.Idle == nil || h.Idle.At.IsZero() {
		return s
	}
	if h.Idle.Active {
		return s + sDim.Render(" · active now: "+h.Idle.Reason)
	}
	at := h.Idle.Since.Add(time.Duration(h.Idle.Limit) * time.Minute)
	return s + sDim.Render(fmt.Sprintf(" · idle since %s, stops at %s", h.Idle.Since.Local().Format("15:04"), at.Local().Format("15:04")))
}

// ---------- stop when idle ----------

func idleCmd() *cobra.Command {
	var dry bool
	c := &cobra.Command{
		Use:   "idle [machine] [off|1h|2h|4h|8h]",
		Short: "Stop a cloud machine by itself when nothing has happened on it for a while",
		Long: `Stop a machine when it has been idle, so it isn't billed while nobody uses it.

The machine checks itself every two minutes, so this works with your laptop closed. It counts
as busy while a tmux session is open somewhere, Claude is working, someone is logged in over
ssh, or the 15-minute load is above a few percent of the cores. Once it has been idle for the
limit it shuts down; only the volume is billed while it is stopped. Opening a session on it
in the app starts it again; from the CLI, ` + "`sky start <machine>`" + `.

It is for machines sky created on Google Cloud or AWS. Off by default.`,
		Example: `  sky idle                 # every machine's setting
  sky idle box 2h          # stop box after two hours idle
  sky idle box off
  sky idle box             # the setting, and what the machine last decided
  sky idle box 1h --dry-run   # it only logs "would stop"`,
		Args:              cobra.MaximumNArgs(2),
		ValidArgsFunction: completeMachines,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			switch len(args) {
			case 0:
				return idleList(ctx)
			case 1:
				return idleShow(ctx, args[0])
			}
			minutes, err := engine.ParseIdle(args[1])
			if err != nil {
				return err
			}
			if dry && minutes == 0 {
				return errors.New("--dry-run goes with a limit, e.g. sky idle " + args[0] + " 2h --dry-run")
			}
			return progress(func(r events.Reporter) error {
				return eng.SetIdle(ctx, args[0], config.IdleStop{Minutes: minutes, DryRun: dry}, r)
			})
		},
	}
	c.Flags().BoolVar(&dry, "dry-run", false, "the machine only logs that it would stop (for trying it out)")
	return c
}

func idleList(ctx context.Context) error {
	eng.NoteStops(ctx)
	all, err := eng.IdleAll()
	if err != nil {
		return err
	}
	if jsonOut {
		return printJSON(all)
	}
	if len(all) == 0 {
		fmt.Println(hint("No machines yet. Create one with `sky new`."))
		return nil
	}
	var rows [][]string
	for _, i := range all {
		setting, note := sDim.Render("off"), i.Note
		switch {
		case !i.Offered:
			setting, note = sDim.Render("—"), i.Why
		case i.Minutes > 0:
			setting = "after " + syncer.IdleLimit(i.Minutes)
			if i.DryRun {
				setting += sAmber.Render(" dry run")
			}
		}
		rows = append(rows, []string{sBold.Render(i.Machine), setting, sDim.Render(note)})
	}
	table(os.Stdout, []string{"name", "stops when idle", ""}, rows)
	fmt.Println("\n  " + hint("sky idle <machine> <off|1h|2h|4h|8h>"))
	return nil
}

func idleShow(ctx context.Context, name string) error {
	if _, err := eng.Refresh(ctx, name); err != nil {
		return err
	}
	eng.NoteStops(ctx)
	info, err := eng.Idle(name)
	if err != nil {
		return err
	}
	var h *engine.Health
	var log []string
	if m, _ := eng.Machine(name); m != nil && m.Status == model.StatusRunning && info.Offered {
		if got, err := eng.Health(ctx, name); err == nil {
			h = &got
		}
		log, _ = eng.IdleLog(ctx, name, 6)
	}
	if jsonOut {
		var watchdog *engine.IdleState
		if h != nil {
			watchdog = h.Idle
		}
		return printJSON(map[string]any{"idle": info, "watchdog": watchdog, "log": log})
	}
	fmt.Println()
	if !info.Offered {
		fmt.Println("  " + sBold.Render(name) + "  " + info.Why)
		fmt.Println()
		return nil
	}
	fmt.Println("  " + sBold.Render(name) + "  " + idleSentence(info, h))
	if info.Note != "" {
		fmt.Println("  " + strings.Repeat(" ", len(name)) + "  " + info.Note + sDim.Render(" · sky start "+name))
	}
	if len(log) > 0 {
		fmt.Println()
		for _, l := range log {
			fmt.Println(sDim.Render("  │ " + l))
		}
	}
	fmt.Println()
	return nil
}

// startAndTend is `sky start`: once the machine is up, its watchdog is brought in line with
// the stop-when-idle setting right away, in case that changed while it was stopped.
func startAndTend(ctx context.Context, name string, r events.Reporter) error {
	if err := eng.Start(ctx, name, r); err != nil {
		return err
	}
	if what, err := eng.TendIdle(ctx, name); err != nil {
		events.Warnf(r, "Stop when idle: %v", err)
	} else if what != "" {
		events.Infof(r, "%s", what)
	}
	return nil
}
