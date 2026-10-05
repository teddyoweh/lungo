package engine

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"skybuild/internal/paths"
	"skybuild/internal/sshx"
	"skybuild/internal/syncer"
)

// Files on a machine, looked at from here.
//
// A session runs where its machine is, and so do the files it makes: a video Claude rendered
// on the Mac mini is on the Mac mini. These find such a file from the name a session printed,
// list what was made lately, and bring a file over (into a cache) so it can be shown on the
// computer you are sitting at.

// RemoteFile is a file or folder on a machine (LocalMachine: this computer).
type RemoteFile struct {
	Path string    `json:"path"` // absolute, on the machine
	Name string    `json:"name"`
	Size int64     `json:"size"`
	Mod  time.Time `json:"mod"`
	Dir  bool      `json:"dir,omitempty"`
}

// statLines prints "mtime<TAB>size<TAB>type<TAB>path" for the paths on stdin (NUL-separated),
// with the stat of GNU (Linux) or BSD (macOS).
const statLines = `if stat -c %Y / >/dev/null 2>&1; then xargs -0 stat --printf '%Y\t%s\t%F\t%n\n' 2>/dev/null; else xargs -0 stat -f '%m%t%z%t%HT%t%N' 2>/dev/null; fi`

// skipDirs are folders never looked into: hidden ones, dependencies and build caches, and the
// Library of a Mac.
const skipDirs = `\( -name '.?*' -o -name node_modules -o -name Library -o -name __pycache__ -o -name venv -o -name site-packages -o -name target -o -name DerivedData \) -prune`

// skipFiles are files nobody means when they look for what was made: compiled leftovers and
// the Finder's notes.
const skipFiles = `! -name '*.pyc' ! -name '*.pyo' ! -name '*.o' ! -name '*.class' ! -name '.DS_Store' ! -name '*.swp'`

func parseRemoteFiles(out string) []RemoteFile {
	var files []RemoteFile
	seen := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.SplitN(line, "\t", 4)
		if len(f) < 4 || f[3] == "" || seen[f[3]] {
			continue
		}
		seen[f[3]] = true
		mod, _ := strconv.ParseInt(f[0], 10, 64)
		size, _ := strconv.ParseInt(f[1], 10, 64)
		files = append(files, RemoteFile{Path: f[3], Name: filepath.Base(f[3]), Size: size, Mod: time.Unix(mod, 0), Dir: strings.Contains(strings.ToLower(f[2]), "directory")})
	}
	return files
}

// sessionDir is shell that sets $d to the folder a session's program is in. dir is used when
// there is no such session (a pane that isn't one); failing both, the home folder.
func sessionDir(session, dir string) string {
	sh := `d=""; `
	if session != "" {
		sh += `d=$(tmux display -p -t ` + sshx.Quote("="+session+":") + ` '#{pane_current_path}' 2>/dev/null); `
	}
	if dir != "" {
		sh += `[ -d "$d" ] || d=` + sshx.Quote(dir) + `; case "$d" in "~") d="$HOME";; "~/"*) d="$HOME/${d#\~/}";; esac; `
	}
	return sh + `[ -d "$d" ] || d="$HOME"; `
}

// FindFile finds the file a session means by name: a path as it was printed ("~/x.png",
// "src/app.ts", "/tmp/out.mp4") or only a file's name ("Creed-promo.mp4"). A relative path is
// tried from the session's folder; a bare name is also looked for on the Desktop, in
// Downloads and Documents, under the session's folder and under the home folder. The
// likeliest comes first: an exact path, then the most recently changed.
func (e *Engine) FindFile(ctx context.Context, machine, session, dir, name string) ([]RemoteFile, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("no file name")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	script := sessionDir(session, dir) + `n=` + sshx.Quote(name) + `
case "$n" in "~") n="$HOME";; "~/"*) n="$HOME/${n#\~/}";; esac
case "$n" in
/*) [ -e "$n" ] && printf '%s\0' "$n";;
*) cd "$d" 2>/dev/null || cd
   if [ -e "$n" ]; then printf '%s\0' "$PWD/$n"; else
     b=$(basename -- "$n")
     for r in "$HOME/Desktop" "$HOME/Downloads" "$HOME/Documents" "$HOME"; do [ -e "$r/$b" ] && printf '%s\0' "$r/$b"; done
     find "$PWD" -maxdepth 6 ` + skipDirs + ` -o -name "$b" -print 2>/dev/null | head -n 12 | tr '\n' '\0'
     [ "$PWD" = "$HOME" ] || find "$HOME" -maxdepth 4 ` + skipDirs + ` -o -name "$b" -print 2>/dev/null | head -n 12 | tr '\n' '\0'
   fi;;
esac | ` + statLines + `; true`
	out, err := e.runOn(ctx, machine, script)
	if err != nil {
		return nil, err
	}
	files := parseRemoteFiles(out)
	if len(files) > 1 && !strings.Contains(name, "/") {
		sort.SliceStable(files, func(i, j int) bool { return files[i].Mod.After(files[j].Mod) })
	}
	return files, nil
}

// RecentFiles lists what was made or changed lately where a session works: under its folder,
// and on the machine's Desktop and in its Downloads (where Claude tends to leave what it
// made for you). Newest first. It returns the session's folder too.
func (e *Engine) RecentFiles(ctx context.Context, machine, session, dir string) (string, []RemoteFile, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	script := sessionDir(session, dir) + `printf '%s\n' "$d"
{ [ "$d" = "$HOME" ] || find "$d" -maxdepth 5 ` + skipDirs + ` -o -type f -mtime -7 ` + skipFiles + ` -print0 2>/dev/null
  for r in "$HOME/Desktop" "$HOME/Downloads"; do find "$r" -maxdepth 1 -type f -mtime -7 ! -name '.*' ` + skipFiles + ` -print0 2>/dev/null; done
  [ "$d" = "$HOME" ] && find "$HOME" -maxdepth 1 -type f -mtime -7 ! -name '.*' ` + skipFiles + ` -print0 2>/dev/null
} | ` + statLines + ` | sort -rn | head -n 120; true`
	out, err := e.runOn(ctx, machine, script)
	if err != nil {
		return "", nil, err
	}
	dir, rest, _ := strings.Cut(out, "\n")
	return strings.TrimSpace(dir), parseRemoteFiles(rest), nil
}

// ListDir lists a folder on a machine: folders first, then files, by name. Hidden entries
// are left out.
func (e *Engine) ListDir(ctx context.Context, machine, dir string) ([]RemoteFile, error) {
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	script := `n=` + sshx.Quote(dir) + `
case "$n" in ""|"~") n="$HOME";; "~/"*) n="$HOME/${n#\~/}";; esac
[ -d "$n" ] || { echo "no such folder: $n" >&2; exit 1; }
find "$n" -mindepth 1 -maxdepth 1 ! -name '.*' -print0 2>/dev/null | ` + statLines + `; true`
	out, err := e.runOn(ctx, machine, script)
	if err != nil {
		return nil, err
	}
	files := parseRemoteFiles(out)
	sort.Slice(files, func(i, j int) bool {
		if files[i].Dir != files[j].Dir {
			return files[i].Dir
		}
		return strings.ToLower(files[i].Name) < strings.ToLower(files[j].Name)
	})
	return files, nil
}

// peekDir is where files brought over from machines are kept.
func peekDir() string { return filepath.Join(paths.Root(), "peek") }

// FetchFile brings a file from a machine to this computer and returns where it is. A file
// on this computer is used where it lies. A copy fetched before is used again while the
// file on the machine hasn't changed. progress hears how many bytes have arrived.
func (e *Engine) FetchFile(ctx context.Context, machine string, f RemoteFile, progress func(done int64)) (string, error) {
	if f.Dir {
		return "", fmt.Errorf("%s is a folder", f.Name)
	}
	if IsLocal(machine) {
		return f.Path, nil
	}
	m, err := e.Machine(machine)
	if err != nil {
		return "", err
	}
	sum := sha1.Sum([]byte(f.Path))
	dir := filepath.Join(peekDir(), machine, hex.EncodeToString(sum[:])[:12])
	local := filepath.Join(dir, f.Name)
	if st, err := os.Stat(local); err == nil && st.Size() == f.Size && st.ModTime().Unix() == f.Mod.Unix() {
		return local, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, ".part-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	w := io.Writer(tmp)
	if progress != nil {
		w = &countingWriter{w: tmp, fn: progress}
	}
	err = sshx.RunTo(ctx, e.Target(m), "cat -- "+sshx.Quote(f.Path), w)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), local); err != nil {
		return "", err
	}
	_ = os.Chtimes(local, time.Now(), f.Mod)
	return local, nil
}

type countingWriter struct {
	w    io.Writer
	n    int64
	fn   func(int64)
	last time.Time
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	if time.Since(c.last) > 120*time.Millisecond {
		c.last = time.Now()
		c.fn(c.n)
	}
	return n, err
}

// TidyPeek removes files brought over more than a few days ago.
func TidyPeek() {
	cutoff := time.Now().Add(-4 * 24 * time.Hour)
	_ = filepath.WalkDir(peekDir(), func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		// The time a copy carries is the original's; when it was fetched is the folder's.
		if st, err := os.Stat(filepath.Dir(path)); err == nil && st.ModTime().Before(cutoff) {
			_ = os.RemoveAll(filepath.Dir(path))
		}
		return nil
	})
}

// InstallLocalPeek puts the peek command on this computer (machines get it from sync), so a
// session here can show you a file the same way.
func InstallLocalPeek() {
	if LocalTmux() == "" {
		return
	}
	path := filepath.Join(paths.Home(), ".local", "bin", "peek")
	if have, err := os.ReadFile(path); err == nil && string(have) == syncer.PeekCommand {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	_ = os.WriteFile(path, []byte(syncer.PeekCommand), 0o755)
}
