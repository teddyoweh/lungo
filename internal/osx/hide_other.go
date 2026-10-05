//go:build !windows

package osx

import "os/exec"

func hideWindow(*exec.Cmd) {}
