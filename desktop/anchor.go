package main

import (
	"os"
	"path/filepath"
	"strings"
)

// Sessions on this computer keep their access to Documents, Desktop and Downloads when the
// app is renamed or moved: see engine.AnchorLocalSessions.

// ownProgram is this app's program inside its bundle, or "" when it isn't running from one
// (or is a development build with sessions of its own, which must leave the real ones alone).
func ownProgram() string {
	if os.Getenv("SKY_TMUX_SOCKET") != "" {
		return ""
	}
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	if !strings.Contains(exe, ".app/Contents/MacOS/") {
		return ""
	}
	return exe
}
