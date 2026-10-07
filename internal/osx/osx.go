// Package osx wraps the operating-system bits that differ between macOS, Linux and Windows:
// running commands, finding binaries, opening a browser or a terminal.
package osx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"skybuild/internal/paths"
)

// Start runs a command without waiting for it, and collects it when it ends: a command
// started and never waited for stays in the process table (a zombie) for as long as the
// app runs.
func Start(cmd *exec.Cmd) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}

// Result of a finished command.
type Result struct {
	Stdout string
	Stderr string
	Code   int
}

// Cmd describes a command to run.
type Cmd struct {
	Name  string
	Args  []string
	Stdin io.Reader
	Env   []string
	Dir   string
}

// Exec runs a command and returns its output. A non-zero exit is an error that carries stderr.
func Exec(ctx context.Context, c Cmd) (Result, error) {
	cmd := exec.CommandContext(ctx, Which(c.Name), c.Args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr, cmd.Stdin, cmd.Dir = &out, &errb, c.Stdin, c.Dir
	if len(c.Env) > 0 {
		cmd.Env = append(os.Environ(), c.Env...)
	}
	hideWindow(cmd)
	// When the command ends (or its context does), stop waiting for its output a moment later
	// even if a process it started still holds the pipe (ssh's ProxyCommand, a shell's helper,
	// git): without this, Run waits for that process, which can be forever.
	cmd.WaitDelay = 3 * time.Second
	err := cmd.Run()
	r := Result{Stdout: out.String(), Stderr: errb.String()}
	if cmd.ProcessState != nil {
		r.Code = cmd.ProcessState.ExitCode()
	}
	if err != nil {
		msg := strings.TrimSpace(r.Stderr)
		if msg == "" {
			msg = strings.TrimSpace(r.Stdout)
		}
		if len(msg) > 1500 {
			msg = "…" + msg[len(msg)-1500:]
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return r, fmt.Errorf("%s timed out", c.Name)
		}
		if msg == "" {
			return r, fmt.Errorf("%s %s: %w", c.Name, firstArgs(c.Args), err)
		}
		return r, fmt.Errorf("%s %s: %s", c.Name, firstArgs(c.Args), msg)
	}
	return r, nil
}

// Run is Exec for the common case: name, args, stdout.
func Run(ctx context.Context, name string, args ...string) (string, error) {
	r, err := Exec(ctx, Cmd{Name: name, Args: args})
	return r.Stdout, err
}

func firstArgs(a []string) string {
	n := len(a)
	if n > 3 {
		n = 3
	}
	return strings.Join(a[:n], " ")
}

var extraDirs = sync.OnceValue(func() []string {
	h := paths.Home()
	dirs := []string{
		filepath.Join(h, ".local", "bin"),
		filepath.Join(h, "go", "bin"),
		filepath.Join(h, ".bun", "bin"),
	}
	switch runtime.GOOS {
	case "darwin":
		dirs = append(dirs, "/opt/homebrew/bin", "/usr/local/bin",
			"/opt/homebrew/share/google-cloud-sdk/bin", "/usr/local/share/google-cloud-sdk/bin",
			filepath.Join(h, "google-cloud-sdk", "bin"),
			"/Applications/Tailscale.app/Contents/MacOS")
	case "linux":
		dirs = append(dirs, "/usr/local/bin", "/snap/bin", filepath.Join(h, "google-cloud-sdk", "bin"))
	case "windows":
		pf := os.Getenv("ProgramFiles")
		dirs = append(dirs, filepath.Join(pf, "Tailscale"), filepath.Join(pf, "GitHub CLI"),
			filepath.Join(os.Getenv("LOCALAPPDATA"), "Google", "Cloud SDK", "google-cloud-sdk", "bin"))
	}
	return dirs
})

// Which finds a binary on PATH or in the usual install locations (a desktop app launched
// from Finder gets a bare PATH). It returns the name unchanged if nothing is found.
func Which(name string) string {
	if filepath.IsAbs(name) {
		return name
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	for _, d := range extraDirs() {
		for _, ext := range exts() {
			p := filepath.Join(d, name+ext)
			if st, err := os.Stat(p); err == nil && !st.IsDir() {
				return p
			}
		}
	}
	return name
}

// Has reports whether a binary can be found.
func Has(name string) bool {
	p := Which(name)
	if p == name {
		_, err := exec.LookPath(name)
		return err == nil
	}
	return true
}

func exts() []string {
	if runtime.GOOS == "windows" {
		return []string{".exe", ".cmd", ".bat", ""}
	}
	return []string{""}
}

// FixPath loads PATH from the user's login shell. A macOS app started from Finder or the
// Dock only gets /usr/bin:/bin, which hides gcloud, gh, claude and tailscale.
func FixPath() {
	if runtime.GOOS == "windows" {
		return
	}
	// The PATH found last time goes in at once, so nothing waits on the login shell (it can
	// take seconds); the shell is asked again in the background to keep it current.
	if b, err := os.ReadFile(pathCache()); err == nil && len(b) > 0 {
		os.Setenv("PATH", strings.TrimSpace(string(b))+string(os.PathListSeparator)+os.Getenv("PATH"))
		go fixPath(20 * time.Second) // the shell's answer goes in front; the remembered one stays behind it
		return
	}
	if fixPath(6 * time.Second) {
		return
	}
	// Right after a computer starts, the login shell can take longer than that (cold caches,
	// a network mount): keep asking in the background until it answers.
	go func() {
		for _, wait := range []time.Duration{20, 40, 60, 120, 240} {
			time.Sleep(wait * time.Second)
			if fixPath(20 * time.Second) {
				return
			}
		}
	}()
}

// FixLocale gives the programs this process starts a UTF-8 locale when it has none. A macOS
// app opened from Finder, the Dock or at login gets no LANG at all, and tmux then prints every
// tab and non-ASCII character as "_": its session lists can't be read (no sessions show) and
// attached panes lose ✳, ❯ and box lines. ssh passes LANG on (SendEnv), so machines get it too.
func FixLocale() {
	if runtime.GOOS == "windows" || isUTF8Locale() {
		return
	}
	os.Setenv("LANG", preferredLocale())
}

// isUTF8Locale applies tmux's test: the first of LC_ALL, LC_CTYPE and LANG that is set names
// UTF-8.
func isUTF8Locale() bool {
	for _, k := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		if v := os.Getenv(k); v != "" {
			v = strings.ToUpper(v)
			return strings.Contains(v, "UTF-8") || strings.Contains(v, "UTF8")
		}
	}
	return false
}

// preferredLocale is the UTF-8 form of the language and region picked in System Settings
// (what Terminal sets LANG to), else en_US.UTF-8; C.UTF-8 on Linux.
func preferredLocale() string {
	if runtime.GOOS != "darwin" {
		return "C.UTF-8"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/usr/bin/defaults", "read", "-g", "AppleLocale").Output()
	if err == nil {
		name, _, _ := strings.Cut(strings.TrimSpace(string(out)), "@") // "fr_FR@rg=usz…"
		if name != "" {
			if _, err := os.Stat(filepath.Join("/usr/share/locale", name+".UTF-8")); err == nil {
				return name + ".UTF-8"
			}
		}
	}
	return "en_US.UTF-8"
}

var pathFixed atomic.Bool

// pathCache is the login shell's PATH as last found.
func pathCache() string { return filepath.Join(paths.Root(), "state", "login-path") }

// fixPath asks the login shell for PATH once; it reports whether that worked.
func fixPath(limit time.Duration) bool {
	if pathFixed.Load() {
		return true
	}
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/zsh"
	}
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	c := exec.CommandContext(ctx, shell, "-ilc", `printf '\n__SKY_PATH__%s' "$PATH"`)
	c.WaitDelay = 2 * time.Second // a helper the shell started may hold its output open
	out, err := c.Output()
	if err != nil {
		return false
	}
	s := string(out)
	if i := strings.LastIndex(s, "__SKY_PATH__"); i >= 0 {
		if p := strings.TrimSpace(s[i+len("__SKY_PATH__"):]); p != "" {
			os.Setenv("PATH", p+string(os.PathListSeparator)+os.Getenv("PATH"))
			pathFixed.Store(true)
			_ = os.MkdirAll(filepath.Dir(pathCache()), 0o700)
			_, _ = paths.WriteFile(pathCache(), []byte(p+"\n"), 0o600)
			return true
		}
	}
	return false
}

// OpenURL opens a link in the default browser.
func OpenURL(url string) error {
	switch runtime.GOOS {
	case "darwin":
		return Start(exec.Command("open", url))
	case "windows":
		return Start(exec.Command("rundll32", "url.dll,FileProtocolHandler", url))
	default:
		return Start(exec.Command("xdg-open", url))
	}
}

// Reveal shows a folder in Finder / Explorer / the file manager.
// RevealFile shows a file in the file manager, selected.
func RevealFile(path string) error {
	switch runtime.GOOS {
	case "darwin":
		return Start(exec.Command("open", "-R", path))
	case "windows":
		return Start(exec.Command("explorer", "/select,", path))
	default:
		return Start(exec.Command("xdg-open", filepath.Dir(path)))
	}
}

func Reveal(path string) error {
	switch runtime.GOOS {
	case "darwin":
		return Start(exec.Command("open", path))
	case "windows":
		return Start(exec.Command("explorer", path))
	default:
		return Start(exec.Command("xdg-open", path))
	}
}

// Terminals lists the terminal apps "Connect" can use on this computer.
func Terminals() []string {
	switch runtime.GOOS {
	case "darwin":
		var out []string
		for _, t := range []string{"Ghostty", "iTerm", "Warp", "Terminal"} {
			if t == "Terminal" || appExists(t) {
				out = append(out, t)
			}
		}
		return out
	case "windows":
		if Has("wt") {
			return []string{"Windows Terminal", "Command Prompt"}
		}
		return []string{"Command Prompt"}
	default:
		var out []string
		for _, t := range []string{"x-terminal-emulator", "gnome-terminal", "konsole", "kitty", "alacritty", "xterm"} {
			if Has(t) {
				out = append(out, t)
			}
		}
		return out
	}
}

func appExists(name string) bool {
	for _, d := range []string{"/Applications", filepath.Join(paths.Home(), "Applications")} {
		app := name
		if name == "iTerm" {
			app = "iTerm"
		}
		if _, err := os.Stat(filepath.Join(d, app+".app")); err == nil {
			return true
		}
	}
	return false
}

// OpenTerminal opens a new terminal window running command (e.g. "ssh api-box").
// pref picks the app; empty means the first one Terminals returns.
func OpenTerminal(pref, command string) error {
	if pref == "" {
		if ts := Terminals(); len(ts) > 0 {
			pref = ts[0]
		}
	}
	switch runtime.GOOS {
	case "darwin":
		esc := strings.ReplaceAll(strings.ReplaceAll(command, `\`, `\\`), `"`, `\"`)
		switch pref {
		case "Ghostty":
			return Start(exec.Command("open", "-na", "Ghostty", "--args", "-e", "/bin/zsh", "-lc", command))
		case "iTerm":
			return Start(exec.Command("osascript",
				"-e", `tell application "iTerm" to create window with default profile command "`+esc+`"`,
				"-e", `tell application "iTerm" to activate`))
		case "Warp":
			// Warp runs commands through launch configurations: write one and open it by URL.
			// The command also goes on the clipboard in case Warp ignores the URL.
			copyToClipboard(command)
			dir := filepath.Join(paths.Home(), ".warp", "launch_configurations")
			name := "skybuild.yaml"
			yaml := fmt.Sprintf("---\nname: skybuild\nwindows:\n  - tabs:\n      - title: %q\n        layout:\n          cwd: %q\n          commands:\n            - exec: %q\n",
				command, paths.Home(), command)
			if os.MkdirAll(dir, 0o755) == nil && os.WriteFile(filepath.Join(dir, name), []byte(yaml), 0o644) == nil {
				return Start(exec.Command("open", "warp://launch/"+name))
			}
			return Start(exec.Command("open", "-a", "Warp"))
		default:
			return Start(exec.Command("osascript",
				"-e", `tell application "Terminal" to do script "`+esc+`"`,
				"-e", `tell application "Terminal" to activate`))
		}
	case "windows":
		args := strings.Fields(command)
		if pref == "Windows Terminal" && Has("wt") {
			return Start(exec.Command(Which("wt"), args...))
		}
		return Start(exec.Command("cmd", append([]string{"/c", "start", ""}, args...)...))
	default:
		switch pref {
		case "gnome-terminal":
			return Start(exec.Command("gnome-terminal", "--", "sh", "-c", command))
		case "":
			return errors.New("no terminal app found")
		default:
			return Start(exec.Command(pref, "-e", "sh", "-c", command))
		}
	}
}

func copyToClipboard(s string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("pbcopy")
	case "windows":
		cmd = exec.Command("clip")
	default:
		cmd = exec.Command("xclip", "-selection", "clipboard")
	}
	cmd.Stdin = strings.NewReader(s)
	_ = cmd.Run()
}
