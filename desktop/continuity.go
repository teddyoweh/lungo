package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"skybuild/internal/engine"
	"skybuild/internal/events"
	"skybuild/internal/osx"
	"skybuild/internal/paths"
	"skybuild/internal/sshx"
)

// Continuity: what makes the app come back exactly as it was left. Sessions on this computer
// live in tmux like the ones on machines, connections that died while the laptop slept are
// replaced when it wakes, and the app can open by itself at login.

// ---------- a pane's repository ----------

// PaneGit reports the repository state of a folder on a machine (engine.LocalMachine for
// this computer): the branch and how much has changed. Not a repository: Repo is false.
func (a *App) PaneGit(machine, dir string) engine.GitInfo {
	g, _ := a.eng.GitInfo(a.ctx, machine, dir)
	return g
}

// ---------- waking up ----------

// watchWake notices that the computer slept: the wall clock jumps ahead of a ticker that
// stood still. Every ssh connection from before is dead by then, whatever it still claims.
func (p *poller) watchWake(ctx context.Context) {
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	last := time.Now().Round(0) // wall clock only: it keeps counting through sleep
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			now := time.Now().Round(0)
			if gap := now.Sub(last); gap > 20*time.Second {
				go p.woke(ctx, gap)
			}
			last = now
		}
	}
}

func (p *poller) woke(ctx context.Context, slept time.Duration) {
	p.mu.Lock()
	p.quietSince = time.Now()
	p.mu.Unlock()
	// Shared connections first, so nothing new waits on a dead one; panes reconnect on the
	// event (they attach to the same tmux sessions, which never noticed).
	sshx.CloseMaster("")
	ms, _ := p.a.eng.Machines()
	for _, m := range ms {
		p.a.eng.TerminalConnCloser(m.Name)()
	}
	wruntime.EventsEmit(p.a.ctx, "wake", map[string]any{"slept": int(slept.Seconds())})
	p.refresh(ctx)
}

// ---------- tmux on this computer ----------

// LocalTmuxInfo says whether sessions on this computer can outlive the app.
type LocalTmuxInfo struct {
	Installed  bool   `json:"installed"`
	Path       string `json:"path"`
	CanInstall bool   `json:"canInstall"` // the app can install it (Homebrew is there)
	How        string `json:"how"`        // otherwise: the command that installs it
}

func (a *App) LocalTmux() LocalTmuxInfo {
	info := LocalTmuxInfo{Path: engine.LocalTmux()}
	info.Installed = info.Path != ""
	switch runtime.GOOS {
	case "darwin":
		info.CanInstall = osx.Has("brew")
		info.How = "brew install tmux"
	case "linux":
		info.How = "sudo apt install tmux"
	}
	return info
}

// InstallTmux installs tmux with Homebrew (macOS).
func (a *App) InstallTmux() string {
	return a.ops.start("tmux", "Install tmux", "", func(ctx context.Context, r events.Reporter) (any, error) {
		if engine.LocalTmux() != "" {
			return nil, nil
		}
		if runtime.GOOS != "darwin" || !osx.Has("brew") {
			return nil, errors.New("install tmux with your package manager, then reopen Lungo")
		}
		events.Stepf(r, "brew install tmux")
		ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
		defer cancel()
		res, err := osx.Exec(ctx, osx.Cmd{Name: "brew", Args: []string{"install", "tmux"}, Env: []string{"HOMEBREW_NO_AUTO_UPDATE=1"}})
		for _, line := range strings.Split(strings.TrimSpace(res.Stdout), "\n") {
			if line = strings.TrimSpace(line); line != "" {
				events.Logf(r, "%s", line)
			}
		}
		if err != nil {
			return nil, err
		}
		if engine.LocalTmux() == "" {
			return nil, errors.New("brew finished but tmux still isn't on this computer")
		}
		return nil, nil
	})
}

// ---------- open at login ----------

// loginLabel names the login item. It kept the app's first name, like the bundle ID.
const loginLabel = "dev.skybuild.app"

// winRun is where Windows keeps programs to start at login; the value used to be "Skybuild".
const (
	winRun       = `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`
	winRunName   = "Lungo"
	winRunLegacy = "Skybuild"
)

// loginItem is the file that makes the system open the app at login, and its content.
func loginItem() (path, content string, err error) {
	exe, err := os.Executable()
	if err != nil {
		return "", "", err
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	switch runtime.GOOS {
	case "darwin":
		bundle, ok := appBundle()
		if !ok {
			return "", "", errors.New("only the installed app can open at login")
		}
		plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + loginLabel + `</string>
	<key>ProgramArguments</key>
	<array>
		<string>/usr/bin/open</string>
		<string>-a</string>
		<string>` + xmlEscape(bundle) + `</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
</dict>
</plist>
`
		return filepath.Join(paths.Home(), "Library", "LaunchAgents", loginLabel+".plist"), plist, nil
	case "linux":
		entry := "[Desktop Entry]\nType=Application\nName=Lungo\nExec=" + exe + "\nX-GNOME-Autostart-enabled=true\n"
		return filepath.Join(paths.Home(), ".config", "autostart", "skybuild.desktop"), entry, nil
	}
	return "", "", fmt.Errorf("opening at login isn't set up for %s yet", runtime.GOOS)
}

func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

// refreshLoginItem points an existing login item at this copy of the app, so opening at
// login follows the app when it is renamed or moved (Skybuild.app became Lungo.app). Only
// an installed copy does this: a dev build must not take the login item over.
func refreshLoginItem() {
	if os.Getenv("SKY_HEADLESS") != "" {
		return
	}
	switch runtime.GOOS {
	case "windows":
		if exec.Command("reg", "query", winRun, "/v", winRunLegacy).Run() == nil {
			if exe, err := os.Executable(); err == nil &&
				exec.Command("reg", "add", winRun, "/v", winRunName, "/t", "REG_SZ", "/d", `"`+exe+`"`, "/f").Run() == nil {
				_ = exec.Command("reg", "delete", winRun, "/v", winRunLegacy, "/f").Run()
			}
		}
		return
	case "darwin":
		b, ok := appBundle()
		if !ok || !(strings.HasPrefix(b, "/Applications/") || strings.HasPrefix(b, filepath.Join(paths.Home(), "Applications")+"/")) {
			return
		}
	}
	path, content, err := loginItem()
	if err != nil {
		return
	}
	if old, err := os.ReadFile(path); err == nil && string(old) != content {
		_ = os.WriteFile(path, []byte(content), 0o644)
	}
}

// OpenAtLogin reports whether the app opens when you log in.
func (a *App) OpenAtLogin() bool {
	if runtime.GOOS == "windows" {
		return exec.Command("reg", "query", winRun, "/v", winRunName).Run() == nil ||
			exec.Command("reg", "query", winRun, "/v", winRunLegacy).Run() == nil
	}
	path, _, err := loginItem()
	if err != nil {
		return false
	}
	_, err = os.Stat(path)
	return err == nil
}

// SetOpenAtLogin turns opening at login on or off.
func (a *App) SetOpenAtLogin(on bool) error {
	if runtime.GOOS == "windows" {
		_ = exec.Command("reg", "delete", winRun, "/v", winRunLegacy, "/f").Run()
		if !on {
			_ = exec.Command("reg", "delete", winRun, "/v", winRunName, "/f").Run()
			return nil
		}
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		return exec.Command("reg", "add", winRun, "/v", winRunName, "/t", "REG_SZ", "/d", `"`+exe+`"`, "/f").Run()
	}
	path, content, err := loginItem()
	if err != nil {
		return err
	}
	if !on {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}
