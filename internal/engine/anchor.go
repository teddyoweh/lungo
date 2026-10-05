package engine

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"skybuild/internal/paths"
	"skybuild/internal/sshx"
)

// Folder access for sessions on this computer (macOS).
//
// macOS decides whether a program may read Documents, Desktop or Downloads by the app that is
// responsible for it. For everything in a local session that is the app that started the tmux
// server, and once that app instance has quit macOS goes by the path its program was at: it
// checks the app on disk there. Sessions outlive the app, so when the app is renamed or moved
// nothing is at that path any more, and every session is refused without a prompt ("Operation
// not permitted" in a folder that worked a minute ago).
//
// So the server remembers the path it answers to (@sky-app), and an app that now lives
// somewhere else leaves a link there to its own program. The sessions then go by the
// permission the app has now, and nothing has to be restarted.

// anchorNote records the link left for a server, so it is removed once no server needs it.
func anchorNote() string { return filepath.Join(paths.Root(), "session-anchor") }

// AnchorLocalSessions keeps the sessions on this computer attached to the running app's
// folder permissions. exe is the app's own program. It reports whether a server is there and
// settled, so the caller can stop asking.
func AnchorLocalSessions(ctx context.Context, exe string) bool {
	if runtime.GOOS != "darwin" || exe == "" || LocalTmux() == "" {
		return true
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := localSh(ctx, `tmux display -p '#{start_time}	#{@sky-app}' 2>/dev/null`)
	f := strings.SplitN(strings.TrimRight(out, "\r\n"), "\t", 2) // the path is empty on a server that doesn't say
	if err != nil || len(f) < 2 || f[0] == "" {
		dropAnchor("") // no server: nothing answers to an old path
		return false
	}
	owner := f[1]
	if owner == "" {
		owner = exe // a server that doesn't say was started by this app
		if _, err := localSh(ctx, "tmux set -g @sky-app "+sshx.Quote(owner)); err != nil {
			return false
		}
	}
	if owner == exe {
		dropAnchor("")
		return true
	}
	return leaveAnchor(owner, exe) == nil
}

// realFile reports whether something other than a link is at path.
func realFile(path string) bool {
	st, err := os.Lstat(path)
	return err == nil && st.Mode()&os.ModeSymlink == 0
}

// anchorRoot is the app bundle a program path is in ("…/Name.app"), or "" when it isn't in one.
func anchorRoot(program string) string {
	dir := filepath.Dir(program)
	if filepath.Base(dir) == "MacOS" && filepath.Base(filepath.Dir(dir)) == "Contents" {
		if root := filepath.Dir(filepath.Dir(dir)); strings.HasSuffix(root, ".app") {
			return root
		}
	}
	return ""
}

// leaveAnchor puts a link to exe where the server's programs look for their app. An app that
// is really there (an old copy kept installed) is left alone.
func leaveAnchor(owner, exe string) error {
	if realFile(owner) {
		return nil
	}
	if have, err := os.Readlink(owner); err != nil || have != exe {
		if err := os.MkdirAll(filepath.Dir(owner), 0o755); err != nil {
			return err
		}
		_ = os.Remove(owner)
		if err := os.Symlink(exe, owner); err != nil {
			return err
		}
	}
	if root := anchorRoot(owner); root != "" && readNote() != root && !exists(filepath.Join(root, "Contents", "Info.plist")) {
		// Only the link is in it: not an app to show in Finder. Noted, to be removed later
		// (dropAnchor takes away nothing but such a link and the empty folders around it).
		_ = exec.Command("/usr/bin/chflags", "hidden", root).Run()
		dropAnchor(root)
		_ = os.WriteFile(anchorNote(), []byte(root+"\n"), 0o600)
	}
	return nil
}

func readNote() string {
	b, _ := os.ReadFile(anchorNote())
	return strings.TrimSpace(string(b))
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// dropAnchor removes the link left for a server that is gone. keep is an anchor still in use.
func dropAnchor(keep string) {
	root := readNote()
	if root == "" || root == keep {
		return
	}
	_ = os.Remove(anchorNote())
	// Only what leaveAnchor made: a bundle with no Info.plist, holding a link.
	if !strings.HasSuffix(root, ".app") || exists(filepath.Join(root, "Contents", "Info.plist")) {
		return
	}
	entries, err := os.ReadDir(filepath.Join(root, "Contents", "MacOS"))
	if err != nil || len(entries) != 1 || entries[0].Type()&os.ModeSymlink == 0 {
		return
	}
	_ = os.Remove(filepath.Join(root, "Contents", "MacOS", entries[0].Name()))
	_ = os.Remove(filepath.Join(root, "Contents", "MacOS"))
	_ = os.Remove(filepath.Join(root, "Contents"))
	_ = os.Remove(root)
}
