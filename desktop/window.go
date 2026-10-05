package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
// instance is a window with an id: "main" for the one the user launched (it restores the
// saved pane layout), a random id for the ones opened from it (they start empty).
//
// Work that must happen once however many windows are open (native notifications, the
// Claude account tick, reaping idle shells) belongs to the primary: whichever instance
// holds ~/.skybuild/desktop.lock. When it quits, another window takes over.

const mainWindow = "main"

type window struct {
	id      string
	lock    *flock.Flock
	primary atomic.Bool
	stop    chan struct{}
}

func newWindow() *window {
	id := os.Getenv("SKY_WINDOW")
	for _, a := range os.Args[1:] {
		if v, ok := strings.CutPrefix(a, "--window="); ok {
			id = v
		}
	}
	if id == "" {
		id = mainWindow
	}
	return &window{id: id, lock: flock.New(filepath.Join(paths.Root(), "desktop.lock")), stop: make(chan struct{})}
}

func windowsDir() string { return filepath.Join(paths.Root(), "windows") }

// start registers this window and keeps trying for the primary role.
func (w *window) start() {
	_ = os.MkdirAll(windowsDir(), 0o700)
	b, _ := json.Marshal(map[string]any{"id": w.id, "pid": os.Getpid(), "started": time.Now()})
	_ = os.WriteFile(w.file(os.Getpid()), b, 0o600)
	w.tryPrimary()
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-w.stop:
				return
			case <-t.C:
				w.tryPrimary()
			}
		}
	}()
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

// ids are the window ids of the running windows.
func (w *window) ids() map[string]bool {
	out := map[string]bool{}
	for _, pid := range w.pids() {
		var rec struct {
			ID string `json:"id"`
		}
		if b, err := os.ReadFile(w.file(pid)); err == nil && json.Unmarshal(b, &rec) == nil {
			out[rec.ID] = true
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

// open starts another window.
func (w *window) open() error {
	b := make([]byte, 3)
	rand.Read(b)
	id := "w" + hex.EncodeToString(b)
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if runtime.GOOS == "darwin" {
		// …/Lungo.app/Contents/MacOS/Lungo → the bundle, opened as a new instance.
		if bundle := filepath.Dir(filepath.Dir(filepath.Dir(exe))); strings.HasSuffix(bundle, ".app") {
			return exec.Command("open", "-n", bundle, "--args", "--window="+id).Start()
		}
	}
	cmd := exec.Command(exe, "--window="+id)
	cmd.Env = append(os.Environ(), "SKY_WINDOW="+id)
	return cmd.Start()
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
