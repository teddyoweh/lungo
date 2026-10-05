package main

import (
	"os/exec"
	"runtime"
	"strconv"

	"github.com/gen2brain/beeep"
)

// notify shows a native notification. On macOS osascript is used directly: it needs no
// bundle setup and shows under the app's name when the app is the one running it.
func notify(title, body string) {
	if runtime.GOOS == "darwin" {
		script := "display notification " + strconv.Quote(body) + " with title " + strconv.Quote(title) + ` sound name "Glass"`
		_ = exec.Command("osascript", "-e", script).Start()
		return
	}
	_ = beeep.Notify(title, body, "")
}
