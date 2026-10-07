package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/gofrs/flock"

	"skybuild/internal/paths"
)

// Wails gives an app one window, so "New Window" starts another instance of the app. Every
// instance is a window with an id: "main" for the first one ever, a random id for the ones
// opened after it. Each window keeps its own tabs.
//
// Windows behave like a terminal's or a browser's: quitting the app (⌘Q, the Dock, logging
// out, a restart or a crash) brings every window back next time with its tabs; closing a
// window with its close button while others stay open forgets that window (its sessions
// keep running). ⌘Q in any window quits them all.
//
// Work that must happen once however many windows are open (native notifications, the
// Claude account tick, reaping idle shells) belongs to the primary: whichever instance
// holds ~/.skybuild/desktop.lock. When it quits, another window takes over.

const mainWindow = "main"

type window struct {
	id       string
	launched bool // a plain launch of the app, not a window opened by another: it reopens the rest
	lock     *flock.Flock
	primary  atomic.Bool
	stop     chan struct{}
	// How this window is going: its close button was pressed (closing), or another window
	// asked it to quit along (quitByPeer). Neither: the app is quitting.
	closing    atomic.Bool
	quitByPeer atomic.Bool
	updating   atomic.Bool // an update is to be installed as the app quits (see startInstaller)
}

// validID: what a window id can be (it names files).
var validID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

func newWindow() *window {
	id := os.Getenv("SKY_WINDOW")
	for _, a := range os.Args[1:] {
		if v, ok := strings.CutPrefix(a, "--window="); ok {
			id = v
		}
	}
	w := &window{lock: flock.New(filepath.Join(paths.Root(), "desktop.lock")), stop: make(chan struct{})}
	switch {
	case validID.MatchString(id):
		w.id = id
	case restores():
		w.id, w.launched = adoptID(), true
	default:
		w.id = mainWindow
	}
	return w
}

// restores: windows come back after a quit, a crash or a restart. Not for a hidden dev
// instance or a development build on its own tmux server: those must never open, close or
// forget the user's windows (one with a home of its own, SKYBUILD_HOME, has its own).
func restores() bool {
	return os.Getenv("SKY_HEADLESS") == "" && (os.Getenv("SKY_TMUX_SOCKET") == "" || os.Getenv("SKYBUILD_HOME") != "")
}

func newID() string {
	b := make([]byte, 3)
	rand.Read(b)
	return "w" + hex.EncodeToString(b)
}

// adoptID is the window a plain launch of the app opens as: the first window to bring back
// that isn't open yet; the first window ever when there are none.
func adoptID() string {
	running := runningIDs()
	for _, id := range readSaved().IDs {
		if !running[id] {
			return id
		}
	}
	if !running[mainWindow] {
		return mainWindow
	}
	return newID()
}

func windowsDir() string { return filepath.Join(paths.Root(), "windows") }

// start registers this window and keeps trying for the primary role.
func (w *window) start() {
	_ = os.MkdirAll(windowsDir(), 0o700)
	b, _ := json.Marshal(map[string]any{"id": w.id, "pid": os.Getpid(), "started": time.Now()})
	_ = os.WriteFile(w.file(os.Getpid()), b, 0o600)
	if restores() {
		updateSaved(func(s *savedWindows) {
			if !slices.Contains(s.IDs, w.id) {
				s.IDs = append(s.IDs, w.id)
			}
			s.Forgotten = slices.DeleteFunc(s.Forgotten, func(x string) bool { return x == w.id })
		})
	}
	w.tryPrimary()
	if !w.primary.Load() && os.Getenv("SKY_HEADLESS") == "" {
		// Wait on the lock itself: the moment the primary quits, this window takes over.
		go func() {
			if w.lock.Lock() == nil {
				w.primary.Store(true)
			}
		}()
	}
}

func (w *window) tryPrimary() {
	// A hidden dev instance (SKY_HEADLESS) never takes the role from the user's real window.
	if w.primary.Load() || os.Getenv("SKY_HEADLESS") != "" {
		return
	}
	if ok, _ := w.lock.TryLock(); ok {
		w.primary.Store(true)
	}
}

func (w *window) close() {
	close(w.stop)
	os.Remove(w.file(os.Getpid()))
	if w.primary.Load() {
		_ = w.lock.Unlock()
	}
}

func (w *window) file(pid int) string {
	return filepath.Join(windowsDir(), strconv.Itoa(pid)+".json")
}

// pids lists the running Lungo windows (oldest first), dropping records of dead ones.
func (w *window) pids() []int {
	entries, _ := os.ReadDir(windowsDir())
	var out []int
	for _, e := range entries {
		pid, err := strconv.Atoi(strings.TrimSuffix(e.Name(), ".json"))
		if err != nil {
			continue
		}
		if !alive(pid) {
			os.Remove(w.file(pid))
			continue
		}
		out = append(out, pid)
	}
	sort.Ints(out)
	return out
}

// running maps the running windows' pids to their ids.
func (w *window) running() map[int]string {
	out := map[int]string{}
	for _, pid := range w.pids() {
		var rec struct {
			ID string `json:"id"`
		}
		if b, err := os.ReadFile(w.file(pid)); err == nil && json.Unmarshal(b, &rec) == nil {
			out[pid] = rec.ID
		}
	}
	return out
}

// ids are the window ids of the running windows.
func (w *window) ids() map[string]bool {
	out := map[string]bool{}
	for _, id := range w.running() {
		out[id] = true
	}
	return out
}

func runningIDs() map[string]bool { return (&window{}).ids() }

// others are the other running windows (pid → id), each checked to be this app: a record
// left by a window that crashed can name a pid some other program has now.
func (w *window) others() map[int]string {
	out := map[int]string{}
	me := os.Getpid()
	for pid, id := range w.running() {
		if pid != me && isLungo(pid) {
			out[pid] = id
		}
	}
	return out
}

func alive(pid int) bool {
	if runtime.GOOS == "windows" {
		return true // no cheap check; stale records there are harmless
	}
	p, err := os.FindProcess(pid)
	return err == nil && p.Signal(syscall.Signal(0)) == nil
}

// isLungo reports whether pid runs the same program as this process.
var isLungo = func(pid int) bool {
	exe, err := os.Executable()
	if err != nil || runtime.GOOS == "windows" {
		return false
	}
	out, err := exec.Command("ps", "-o", "comm=", "-p", strconv.Itoa(pid)).Output()
	return err == nil && filepath.Base(strings.TrimSpace(string(out))) == filepath.Base(exe)
}

// open starts another window, a new one when id is "".
func (w *window) open(id string) error {
	if id == "" {
		id = newID()
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if runtime.GOOS == "darwin" {
		// …/Lungo.app/Contents/MacOS/Lungo → the bundle, opened as a new instance.
		if bundle := filepath.Dir(filepath.Dir(filepath.Dir(exe))); strings.HasSuffix(bundle, ".app") {
			args := append([]string{"-n"}, sameWorld()...)
			return reaped(exec.Command("open", append(args, bundle, "--args", "--window="+id)...))
		}
	}
	cmd := exec.Command(exe, "--window="+id)
	cmd.Env = append(os.Environ(), "SKY_WINDOW="+id)
	return reaped(cmd)
}

// sameWorld is what `open` needs to start Lungo in the same world as this process: a build
// with a home, a tmux server or an update feed of its own keeps them (open passes no
// environment on by itself).
func sameWorld() []string {
	pass := []string{"SKYBUILD_HOME", "SKY_TMUX_SOCKET", "SKY_UPDATE_FEED"}
	if os.Getenv("SKYBUILD_HOME") != "" {
		pass = append(pass, "HOME") // a world of its own: ~/.ssh/config included
	}
	var args []string
	for _, k := range pass {
		if v := os.Getenv(k); v != "" {
			args = append(args, "--env", k+"="+v)
		}
	}
	return args
}

// reaped starts a command and collects it when it ends, so it doesn't linger as a zombie.
func reaped(cmd *exec.Cmd) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}

// reopen brings back the windows that were open when the app last quit, besides this one.
// Only the window a plain launch opened does it.
func (w *window) reopen() {
	if !w.launched || !restores() {
		return
	}
	running := w.ids()
	for _, id := range readSaved().IDs {
		if id != w.id && !running[id] {
			_ = w.open(id)
			time.Sleep(150 * time.Millisecond) // one at a time, so they stack in order
		}
	}
}

// next brings the next Lungo window to the front.
func (w *window) next() error {
	pids := w.pids()
	if len(pids) < 2 {
		return errors.New("this is the only window")
	}
	me := os.Getpid()
	for i, pid := range pids {
		if pid == me {
			return activate(pids[(i+1)%len(pids)])
		}
	}
	return activate(pids[0])
}

// leaving runs as the window goes. Closed with its close button while other windows stay
// open, it is forgotten. Otherwise the app is quitting: it stays to come back, and ⌘Q (or
// the Dock, or logging out) takes the other windows along, which stay to come back too.
// It reports whether this window is where the app quit.
func (w *window) leaving() (appQuit bool) {
	if !restores() {
		return false
	}
	// Elsewhere closing the window is the only way a window goes.
	closing := w.closing.Load() || runtime.GOOS != "darwin"
	others := w.others()
	switch {
	case closing && len(others) > 0:
		noteErr("window %s closed: forgotten (%d other windows open)", w.id, len(others))
		w.forget()
	case !closing && !w.quitByPeer.Load():
		noteErr("window %s: quitting the app, %d other windows along", w.id, len(others))
		for pid := range others {
			askToQuit(pid)
		}
		return true
	default:
		noteErr("window %s: quitting (closing=%v, asked by another=%v)", w.id, closing, w.quitByPeer.Load())
	}
	return closing // the last window closed: the app quits with it
}

// forget drops this window for good: it won't come back, and its tabs and frame go (the
// tabs are in the page's storage, which the next window to start clears; see Forgotten).
func (w *window) forget() {
	updateSaved(func(s *savedWindows) {
		s.IDs = slices.DeleteFunc(s.IDs, func(x string) bool { return x == w.id })
		if !slices.Contains(s.Forgotten, w.id) {
			s.Forgotten = append(s.Forgotten, w.id)
		}
	})
	os.Remove(stateFile(w.id))
	os.Remove(layoutFile(w.id))
	if w.id != mainWindow {
		updateFrames(func(m map[string]frame) { delete(m, w.id) })
	}
}

// ---------- the windows to bring back ----------

// savedWindows is ~/.skybuild/windows/saved.json: the windows to bring back, in the order
// they were opened, and the ones forgotten whose tabs are still to be cleared.
type savedWindows struct {
	IDs       []string `json:"ids"`
	Forgotten []string `json:"forgotten,omitempty"`
}

func savedFile() string { return filepath.Join(windowsDir(), "saved.json") }

func readSaved() savedWindows {
	var s savedWindows
	if b, err := os.ReadFile(savedFile()); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	return s
}

// updateSaved changes saved.json; every window (each its own process) writes there.
func updateSaved(change func(*savedWindows)) savedWindows {
	_ = os.MkdirAll(windowsDir(), 0o700)
	lock := flock.New(savedFile() + ".lock")
	if err := lock.Lock(); err == nil {
		defer lock.Unlock()
	}
	s := readSaved()
	change(&s)
	if b, err := json.MarshalIndent(s, "", "  "); err == nil {
		tmp := savedFile() + ".tmp"
		if os.WriteFile(tmp, append(b, '\n'), 0o600) == nil {
			_ = os.Rename(tmp, savedFile())
		}
	}
	return s
}

// ---------- what each window has open ----------

// stateFile holds the sessions a window has open ("machine/session"), kept after it quits
// so that a window still to come back keeps its shells from being tidied away.
func stateFile(id string) string { return filepath.Join(windowsDir(), id+".state.json") }

type windowState struct {
	Sessions []string `json:"sessions"`
}

func (w *window) setSessions(keys []string) {
	b, _ := json.Marshal(windowState{Sessions: keys})
	tmp := stateFile(w.id) + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, stateFile(w.id))
	}
}

func readState(id string) windowState {
	var s windowState
	if b, err := os.ReadFile(stateFile(id)); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	return s
}

// keepAll adds the shells every other window points at (open now or to come back) to this
// window's own, per machine: shells to leave alone when tidying.
func (w *window) keepAll(own map[string][]string) map[string][]string {
	out := map[string][]string{}
	for m, list := range own {
		out[m] = append(out[m], list...)
	}
	ids := map[string]bool{}
	for _, id := range readSaved().IDs {
		ids[id] = true
	}
	for _, id := range w.running() {
		ids[id] = true
	}
	delete(ids, w.id)
	for id := range ids {
		for _, k := range readState(id).Sessions {
			if m, s, ok := strings.Cut(k, "/"); ok {
				out[m] = append(out[m], s)
			}
		}
	}
	return out
}

// holder finds the other running window that has a session open: its pid, 0 for none.
func (w *window) holder(key string) int {
	for pid, id := range w.others() {
		if slices.Contains(readState(id).Sessions, key) {
			return pid
		}
	}
	return 0
}

// ---------- asking another window ----------

// A window asks another to do something with a session it shows by leaving a note for it
// (<pid>.ask: "show machine/session" to bring it forward, "release machine/session" to let
// it go to the asking window), which the other picks up within a moment (watchAsks).
func askFile(pid int) string { return filepath.Join(windowsDir(), strconv.Itoa(pid)+".ask") }

func (w *window) ask(pid int, verb, key string) error {
	f, err := os.OpenFile(askFile(pid), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(verb + " " + key + "\n")
	return err
}

// watchAsks hands what other windows ask of this one to do.
func (w *window) watchAsks(do func(verb, machine, session string)) {
	t := time.NewTicker(200 * time.Millisecond)
	defer t.Stop()
	path := askFile(os.Getpid())
	for {
		select {
		case <-w.stop:
			return
		case <-t.C:
		}
		taken := path + ".taken"
		if os.Rename(path, taken) != nil {
			continue
		}
		b, _ := os.ReadFile(taken)
		os.Remove(taken)
		for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			verb, key, _ := strings.Cut(line, " ")
			if m, s, ok := strings.Cut(key, "/"); ok {
				do(verb, m, s)
			}
		}
	}
}

// A window made to take panes from another starts with them: the layout comes in a file
// (<id>.handoff.json) that the new window reads once.
func handoffFile(id string) string { return filepath.Join(windowsDir(), id+".handoff.json") }

func (w *window) openWith(layout string) error {
	id := newID()
	if err := os.WriteFile(handoffFile(id), []byte(layout), 0o600); err != nil {
		return err
	}
	return w.open(id)
}

func (w *window) takeHandoff() string {
	b, err := os.ReadFile(handoffFile(w.id))
	if err != nil {
		return ""
	}
	os.Remove(handoffFile(w.id))
	return string(b)
}

// ---------- each window's tabs ----------

// A window's tabs and splits are kept here (<id>.layout.json), not in the page's storage:
// windows are separate processes, and only one of them got its page storage written to disk,
// so every other window came back empty after a restart.
func layoutFile(id string) string { return filepath.Join(windowsDir(), id+".layout.json") }

func (w *window) saveLayout(layout string) error {
	_ = os.MkdirAll(windowsDir(), 0o700)
	tmp := layoutFile(w.id) + ".tmp"
	if err := os.WriteFile(tmp, []byte(layout), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, layoutFile(w.id))
}

// layout is what the window opens with: panes handed over by another window, else its own
// tabs from last time ("" when there are none: a new window, or one from before this file).
func (w *window) layout() string {
	if h := w.takeHandoff(); h != "" {
		return h
	}
	b, _ := os.ReadFile(layoutFile(w.id))
	return string(b)
}
