//go:build darwin

package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// canReplace: the app's folder (/Applications) can take the new app.
func canReplace(bundle string) bool { return unix.Access(filepath.Dir(bundle), unix.W_OK) == nil }

// startInstaller starts the helper that swaps the app once every window has quit: this
// program again, on its own (see installMain), so it outlives the windows.
func (a *App) startInstaller(relaunch bool) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	bundle, ok := appBundle()
	if !ok {
		return errors.New("not running from an app bundle")
	}
	pids := []string{strconv.Itoa(os.Getpid())}
	for pid := range a.win.others() {
		pids = append(pids, strconv.Itoa(pid))
	}
	args := []string{"--install-update", "--from=" + readyApp(), "--to=" + bundle, "--wait=" + strings.Join(pids, ",")}
	if relaunch {
		args = append(args, "--relaunch")
	}
	cmd := exec.Command(exe, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Dir = "/"
	return reaped(cmd)
}

// installMain is that helper: once every window has quit it swaps the new app into the old
// one's place in a single step (never a moment without an app there), removes the old one,
// and opens Lungo again when asked to.
func installMain(args []string) {
	var from, to string
	var pids []int
	relaunch := false
	for _, a := range args {
		switch {
		case strings.HasPrefix(a, "--from="):
			from = strings.TrimPrefix(a, "--from=")
		case strings.HasPrefix(a, "--to="):
			to = strings.TrimPrefix(a, "--to=")
		case strings.HasPrefix(a, "--wait="):
			for _, p := range strings.Split(strings.TrimPrefix(a, "--wait="), ",") {
				if n, err := strconv.Atoi(p); err == nil {
					pids = append(pids, n)
				}
			}
		case a == "--relaunch":
			relaunch = true
		}
	}
	keepErrors()
	if from == "" || !strings.HasSuffix(to, ".app") {
		noteErr("update: nothing to install")
		return
	}
	open := func() {
		if relaunch {
			_ = exec.Command("/usr/bin/open", append(sameWorld(), to)...).Run()
		}
	}
	deadline := time.Now().Add(90 * time.Second)
	for _, pid := range pids {
		for alive(pid) {
			if time.Now().After(deadline) {
				noteErr("update: Lungo is still open; it installs next time it quits")
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	lock := updateLock()
	if err := lock.Lock(); err == nil {
		defer lock.Unlock()
	}
	version := plistValue(filepath.Join(from, "Contents", "Info.plist"), "CFBundleShortVersionString")
	if version == "" {
		open() // another helper got there first
		return
	}
	if err := swapApp(from, to); err != nil {
		noteErr("update: couldn't put %s in place: %v", version, err)
		open()
		return
	}
	_ = os.RemoveAll(filepath.Dir(from)) // ready/, which now holds the old app
	s := readUpdateState()
	s.State, s.Ready, s.Progress, s.Error = "", "", 0, ""
	writeUpdateState(s)
	noteErr("updated to %s", version)
	open()
}

// swapApp exchanges the new app and the old one in one step. Across volumes the new one is
// first copied next to the old.
func swapApp(from, to string) error {
	err := unix.RenamexNp(from, to, unix.RENAME_SWAP)
	if err == nil || !errors.Is(err, unix.EXDEV) {
		return err
	}
	near := to + ".update"
	_ = os.RemoveAll(near)
	if out, err := exec.Command("/usr/bin/ditto", from, near).CombinedOutput(); err != nil {
		return errors.New(strings.TrimSpace(string(out)))
	}
	if err := unix.RenamexNp(near, to, unix.RENAME_SWAP); err != nil {
		_ = os.RemoveAll(near)
		return err
	}
	return os.RemoveAll(near) // the old app
}
