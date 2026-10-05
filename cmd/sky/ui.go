package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"

	"skybuild/internal/events"
	"skybuild/internal/model"
	"skybuild/internal/osx"
)

var (
	accent = lipgloss.AdaptiveColor{Light: "#2563eb", Dark: "#7aa2f7"}
	green  = lipgloss.AdaptiveColor{Light: "#15803d", Dark: "#9ece6a"}
	amber  = lipgloss.AdaptiveColor{Light: "#b45309", Dark: "#e0af68"}
	red    = lipgloss.AdaptiveColor{Light: "#b91c1c", Dark: "#f7768e"}
	dimc   = lipgloss.AdaptiveColor{Light: "#6b7280", Dark: "#737aa2"}

	sAccent = lipgloss.NewStyle().Foreground(accent)
	sBold   = lipgloss.NewStyle().Bold(true)
	sGreen  = lipgloss.NewStyle().Foreground(green)
	sAmber  = lipgloss.NewStyle().Foreground(amber)
	sRed    = lipgloss.NewStyle().Foreground(red)
	sDim    = lipgloss.NewStyle().Foreground(dimc)
	sCode   = lipgloss.NewStyle().Foreground(accent).Bold(true)
)

func isTTY() bool { return term.IsTerminal(int(os.Stdout.Fd())) }

func width() int {
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 20 {
		return w
	}
	return 100
}

func formTheme() *huh.Theme { return huh.ThemeCharm() }

// statusDot renders a machine status as a coloured dot and word.
func statusDot(s string) string {
	switch s {
	case model.StatusRunning:
		return sGreen.Render("●") + " running"
	case model.StatusStopped:
		return sDim.Render("○ stopped")
	case model.StatusStarting, model.StatusProvisioning:
		return sAmber.Render("◐") + " " + s
	case model.StatusStopping:
		return sAmber.Render("◑") + " stopping"
	case model.StatusMissing:
		return sRed.Render("✕") + " missing"
	case model.StatusUnreachable:
		return sRed.Render("●") + " unreachable"
	case "":
		return sDim.Render("· unknown")
	}
	return sDim.Render("· " + s)
}

// table prints rows with columns padded to their widest cell (ANSI-aware).
func table(w io.Writer, header []string, rows [][]string) {
	widths := make([]int, len(header))
	for i, h := range header {
		widths[i] = lipgloss.Width(h)
	}
	for _, r := range rows {
		for i, c := range r {
			if l := lipgloss.Width(c); i < len(widths) && l > widths[i] {
				widths[i] = l
			}
		}
	}
	line := func(cells []string, style func(string) string) {
		var b strings.Builder
		b.WriteString("  ")
		for i, c := range cells {
			pad := widths[i] - lipgloss.Width(c)
			b.WriteString(style(c))
			if i < len(cells)-1 {
				b.WriteString(strings.Repeat(" ", pad+3))
			}
		}
		fmt.Fprintln(w, strings.TrimRight(b.String(), " "))
	}
	line(header, func(s string) string { return sDim.Render(strings.ToUpper(s)) })
	for _, r := range rows {
		line(r, func(s string) string { return s })
	}
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func money(v float64) string {
	if v >= 100 {
		return fmt.Sprintf("$%.0f", v)
	}
	return fmt.Sprintf("$%.2f", v)
}

// ---------- progress renderer ----------

var spin = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// renderer draws engine events: a spinner on the current step, finished steps with ✓,
// details dimmed underneath, links highlighted and opened.
type renderer struct {
	mu       sync.Mutex
	tty      bool
	verbose  bool
	step     string
	started  time.Time
	lastLog  string
	i        int
	active   bool
	stop     chan struct{}
	stopped  chan struct{}
	opened   map[string]bool
	noBrowse bool
}

func newRenderer(verbose bool) *renderer {
	r := &renderer{tty: isTTY(), verbose: verbose, opened: map[string]bool{}}
	if r.tty {
		r.stop, r.stopped = make(chan struct{}), make(chan struct{})
		go r.loop()
	}
	return r
}

func (r *renderer) loop() {
	t := time.NewTicker(90 * time.Millisecond)
	defer t.Stop()
	defer close(r.stopped)
	for {
		select {
		case <-r.stop:
			return
		case <-t.C:
			r.mu.Lock()
			if r.active {
				r.i++
				r.draw()
			}
			r.mu.Unlock()
		}
	}
}

func (r *renderer) draw() {
	el := time.Since(r.started).Round(time.Second)
	line := sAccent.Render(spin[r.i%len(spin)]) + " " + r.step + sDim.Render(fmt.Sprintf("  %s", el))
	if r.lastLog != "" && !r.verbose {
		room := width() - lipgloss.Width(line) - 6
		if room > 12 {
			l := r.lastLog
			if len([]rune(l)) > room {
				l = string([]rune(l)[:room-1]) + "…"
			}
			line += sDim.Render("  " + l)
		}
	}
	fmt.Print("\r\033[K" + line)
}

func (r *renderer) clear() {
	if r.tty && r.active {
		fmt.Print("\r\033[K")
	}
}

func (r *renderer) println(s string) {
	r.clear()
	fmt.Println(s)
	if r.tty && r.active {
		r.draw()
	}
}

func (r *renderer) finishStep(mark string) {
	if !r.active {
		return
	}
	r.clear()
	el := time.Since(r.started).Round(time.Second)
	fmt.Println(mark + " " + r.step + sDim.Render(fmt.Sprintf("  %s", el)))
	r.active = false
	r.lastLog = ""
}

// Emit implements events.Reporter.
func (r *renderer) Emit(e events.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	prefix := ""
	if e.Machine != "" && e.Op == "sync" {
		prefix = sDim.Render(e.Machine+" ") + ""
	}
	switch e.Level {
	case events.Step:
		r.finishStep(sGreen.Render("✓"))
		r.step, r.started, r.active, r.lastLog = prefix+e.Message, time.Now(), true, ""
		if !r.tty {
			fmt.Println("→ " + prefix + e.Message)
		} else {
			r.draw()
		}
	case events.Done:
		if r.active {
			r.step = prefix + e.Message
			r.finishStep(sGreen.Render("✓"))
		} else {
			r.println(sGreen.Render("✓") + " " + prefix + e.Message)
		}
	case events.Info:
		r.println(sDim.Render("  │ ") + prefix + e.Message)
	case events.Log:
		if r.verbose {
			r.println(sDim.Render("  │ " + e.Message))
		} else {
			r.lastLog = e.Message
		}
	case events.Warn:
		r.println(sAmber.Render("  ! ") + prefix + e.Message)
	case events.Error:
		r.println(sRed.Render("  ✗ ") + prefix + e.Message)
	case events.Link:
		r.println("")
		r.println(sAccent.Render("  → ") + sBold.Render(e.Message))
		r.println("    " + sCode.Render(e.URL))
		if !r.opened[e.URL] && !r.noBrowse {
			r.opened[e.URL] = true
			_ = osx.OpenURL(e.URL)
			r.println(sDim.Render("    (opened in your browser)"))
		}
		r.println("")
	}
}

// end stops the spinner; failed marks the open step with ✗.
func (r *renderer) end(failed bool) {
	r.mu.Lock()
	if failed {
		r.finishStep(sRed.Render("✗"))
	} else {
		r.finishStep(sGreen.Render("✓"))
	}
	r.mu.Unlock()
	if r.tty {
		close(r.stop)
		<-r.stopped
	}
}

// confirm asks a yes/no question (defaults to no without a terminal).
func confirm(title, desc string) bool {
	if !isTTY() {
		return false
	}
	ok := false
	err := huh.NewForm(huh.NewGroup(huh.NewConfirm().Title(title).Description(desc).Affirmative("Yes").Negative("No").Value(&ok))).
		WithTheme(formTheme()).Run()
	return err == nil && ok
}

func hint(s string) string { return sDim.Render(s) }
