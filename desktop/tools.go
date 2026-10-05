package main

import (
	"context"
	"time"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"skybuild/internal/config"
	"skybuild/internal/engine"
	"skybuild/internal/model"
)

// Small helpers for the terminal views: ports a session listens on, the files of a pane's
// folder (to mention in a prompt), and the clipboard (a recipe can start from what you copied).

// SessionPorts lists, per session on a machine (engine.LocalMachine: this computer), the
// ports programs started in it are listening on.
func (a *App) SessionPorts(machine string) map[string][]model.Port {
	ports, _ := a.eng.SessionPorts(a.ctx, machine)
	if ports == nil {
		ports = map[string][]model.Port{}
	}
	return ports
}

// ListFiles lists the files under a pane's folder, relative to it.
func (a *App) ListFiles(machine, dir string) []string {
	ctx, cancel := context.WithTimeout(a.ctx, 12*time.Second)
	defer cancel()
	files, _ := a.eng.ListFiles(ctx, machine, dir)
	if files == nil {
		files = []string{}
	}
	return files
}

// ClipboardText returns the text on the clipboard ("" when there is none).
func (a *App) ClipboardText() string {
	s, _ := wruntime.ClipboardGetText(a.ctx)
	return s
}

// ClaudePreciseScroll reports whether Claude Code's own wheel speed-up is turned off in this
// computer's Claude settings, so scrolling follows the trackpad line for line.
func (a *App) ClaudePreciseScroll() bool { return engine.ClaudePreciseScroll() }

// SetClaudePreciseScroll changes that setting. Sessions pick it up when Claude restarts;
// machines get it with the next sync.
func (a *App) SetClaudePreciseScroll(on bool) error { return engine.SetClaudePreciseScroll(on) }

// RestartClaude restarts Claude Code in a session, resuming its conversation.
func (a *App) RestartClaude(machine, session, sid, flags string) error {
	err := a.eng.RestartClaude(a.ctx, machine, session, sid, flags)
	go a.refreshSessions(machine)
	return err
}

// ClaudeMoveSessions reports whether running Claude sessions follow the machines to a new
// account (restarted with their conversation when idle). On unless turned off.
func (a *App) ClaudeMoveSessions() bool { return !engine.MoveSessionsOff() }

// SetClaudeMoveSessions turns that on or off.
func (a *App) SetClaudeMoveSessions(on bool) error {
	return config.Update(func(c *config.Config) error {
		c.Settings.ClaudeLeaveSessions = !on
		return nil
	})
}
