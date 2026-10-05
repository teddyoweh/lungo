//go:build windows

package osx

import (
	"os/exec"
	"syscall"
)

// hideWindow keeps console windows from flashing when the desktop app runs a command.
func hideWindow(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
}
