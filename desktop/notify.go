package main

import (
	"os/exec"
	"runtime"
	"strconv"

	"github.com/gen2brain/beeep"

	"skybuild/internal/osx"
)

// notify shows a native notification. On macOS osascript is used directly: it needs no
// bundle setup and shows under the app's name when the app is the one running it.
func notify(title, body string) {
	if runtime.GOOS == "darwin" {
		script := "display notification " + strconv.Quote(body) + " with title " + strconv.Quote(title) + ` sound name "Glass"`
		_ = osx.Start(exec.Command("osascript", "-e", script)) // collected when it ends
		return
	}
	_ = beeep.Notify(title, body, "")
}
