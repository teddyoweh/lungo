// Package folders copies folders between this computer and a machine: rsync when both ends
// have it (fast, incremental), a tar stream over ssh when they don't (Windows).
package folders

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"skybuild/internal/events"
	"skybuild/internal/osx"
	"skybuild/internal/sshx"
)

// Options for one copy.
type Options struct {
	Delete   bool     `json:"delete"`   // remove files on the destination that the source doesn't have
	Excludes []string `json:"excludes"` // patterns like node_modules or *.log
	Lean     bool     `json:"lean"`     // skip dependency and build folders
	DryRun   bool     `json:"dryRun"`
}

// LeanExcludes are skipped with Options.Lean: things that are rebuilt on the other side.
var LeanExcludes = []string{"node_modules", ".venv", "venv", "__pycache__", ".next", ".turbo", "dist", "build", "target", ".cache", ".DS_Store"}

func (o Options) excludes() []string {
	ex := append([]string{".DS_Store"}, o.Excludes...)
	if o.Lean {
		ex = append(ex, LeanExcludes...)
	}
	return ex
}

// RemoteRel turns ~/x or x into a path relative to the remote home (what rsync and the
// remote shell expect); absolute paths are left alone.
func RemoteRel(p string) string {
	p = strings.TrimSpace(p)
	switch {
	case p == "~" || p == "":
		return "."
	case strings.HasPrefix(p, "~/"):
		return p[2:]
	}
	return p
}

func hasRemoteRsync(ctx context.Context, t sshx.Target) bool {
	out, _ := sshx.Run(ctx, t, "command -v rsync")
	return strings.TrimSpace(out) != ""
}

// Push copies a local file or folder to remote on the machine. A folder's contents land in
// remote (remote is created if needed).
func Push(ctx context.Context, t sshx.Target, local, remote string, o Options, r events.Reporter) error {
	local = expand(local)
	st, err := os.Stat(local)
	if err != nil {
		return err
	}
	remote = RemoteRel(remote)
	events.Stepf(r, "Copying %s → %s:%s", tilde(local), t.Name, display(remote))
	if osx.Has("rsync") && hasRemoteRsync(ctx, t) {
		parent := remote
		if !st.IsDir() {
			parent = path.Dir(remote)
		}
		if _, err := sshx.Run(ctx, t, "mkdir -p "+sshx.Quote(parent)); err != nil {
			return err
		}
		src := local
		if st.IsDir() {
			src = strings.TrimRight(local, `/\`) + "/"
			remote = strings.TrimRight(remote, "/") + "/"
		}
		return rsync(ctx, t, o, r, src, t.Dest()+":"+remote)
	}
	if o.Delete {
		events.Warnf(r, "rsync isn't available, so --delete is ignored")
	}
	return tarPush(ctx, t, local, remote, st.IsDir(), o, r)
}

// Pull copies remote on the machine to local.
func Pull(ctx context.Context, t sshx.Target, remote, local string, o Options, r events.Reporter) error {
	local = expand(local)
	remote = RemoteRel(remote)
	kind, err := sshx.Run(ctx, t, "if [ -d "+sshx.Quote(remote)+" ]; then echo dir; elif [ -e "+sshx.Quote(remote)+" ]; then echo file; fi")
	if err != nil {
		return err
	}
	kind = strings.TrimSpace(kind)
	if kind == "" {
		return fmt.Errorf("%s:%s doesn't exist", t.Name, display(remote))
	}
	events.Stepf(r, "Copying %s:%s → %s", t.Name, display(remote), tilde(local))
	if kind == "dir" {
		if err := os.MkdirAll(local, 0o755); err != nil {
			return err
		}
	} else if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
		return err
	}
	if osx.Has("rsync") && hasRemoteRsync(ctx, t) {
		src, dst := t.Dest()+":"+remote, local
		if kind == "dir" {
			src = strings.TrimRight(src, "/") + "/"
			dst = strings.TrimRight(local, `/\`) + "/"
		}
		return rsync(ctx, t, o, r, src, dst)
	}
	return tarPull(ctx, t, remote, local, kind == "dir", r)
}

func rsync(ctx context.Context, t sshx.Target, o Options, r events.Reporter, src, dst string) error {
	args := []string{"-az", "-v", "-e", t.SSHCommand()}
	for _, ex := range o.excludes() {
		args = append(args, "--exclude", ex)
	}
	if o.Delete {
		args = append(args, "--delete")
	}
	if o.DryRun {
		args = append(args, "-n")
	}
	args = append(args, src, dst)
	cmd := exec.CommandContext(ctx, osx.Which("rsync"), args...)
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	if err := cmd.Start(); err != nil {
		return err
	}
	n := 0
	var tailLines []string
	done := make(chan struct{})
	go func() {
		buf := make([]byte, 32*1024)
		var acc string
		for {
			k, err := pr.Read(buf)
			acc += string(buf[:k])
			for {
				i := strings.IndexByte(acc, '\n')
				if i < 0 {
					break
				}
				line := strings.TrimSpace(acc[:i])
				acc = acc[i+1:]
				if line == "" || strings.HasPrefix(line, "sending incremental") || strings.HasPrefix(line, "receiving") || strings.HasPrefix(line, "Transfer starting") ||
					strings.HasPrefix(line, "sent ") || strings.HasPrefix(line, "total size") || strings.HasSuffix(line, "/") {
					continue
				}
				n++
				tailLines = append(tailLines, line)
				if len(tailLines) > 20 {
					tailLines = tailLines[1:]
				}
				events.Logf(r, "%s", line)
			}
			if err != nil {
				break
			}
		}
		close(done)
	}()
	err := cmd.Wait()
	pw.Close()
	<-done
	if err != nil {
		return fmt.Errorf("rsync failed: %s", strings.Join(tailLines, "; "))
	}
	verb := "Copied"
	if o.DryRun {
		verb = "Would copy"
	}
	events.Donef(r, "%s %d file(s)", verb, n)
	return nil
}

func excluded(name string, ex []string) bool {
	for _, p := range ex {
		if ok, _ := path.Match(p, name); ok || p == name {
			return true
		}
	}
	return false
}

func tarPush(ctx context.Context, t sshx.Target, local, remote string, isDir bool, o Options, r events.Reporter) error {
	pr, pw := io.Pipe()
	ex := o.excludes()
	n := 0
	go func() {
		tw := tar.NewWriter(pw)
		var err error
		if isDir {
			err = filepath.Walk(local, func(p string, info os.FileInfo, err error) error {
				if err != nil {
					return nil
				}
				rel, _ := filepath.Rel(local, p)
				if rel == "." {
					return nil
				}
				if excluded(info.Name(), ex) {
					if info.IsDir() {
						return filepath.SkipDir
					}
					return nil
				}
				return addFile(tw, p, filepath.ToSlash(rel), info, &n)
			})
		} else {
			info, _ := os.Stat(local)
			err = addFile(tw, local, path.Base(remote), info, &n)
		}
		if err == nil {
			err = tw.Close()
		}
		pw.CloseWithError(err)
	}()
	dest := remote
	if !isDir {
		dest = path.Dir(remote)
	}
	_, err := sshx.RunInput(ctx, t, "mkdir -p "+sshx.Quote(dest)+" && tar -xf - -C "+sshx.Quote(dest), pr)
	if err != nil {
		return err
	}
	events.Donef(r, "Copied %d file(s)", n)
	return nil
}

func addFile(tw *tar.Writer, p, name string, info os.FileInfo, n *int) error {
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(p)
		if err != nil {
			return nil
		}
		return tw.WriteHeader(&tar.Header{Name: name, Linkname: target, Typeflag: tar.TypeSymlink, Mode: 0o777, ModTime: info.ModTime()})
	}
	h, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return err
	}
	h.Name = name
	if info.IsDir() {
		h.Name += "/"
		return tw.WriteHeader(h)
	}
	if !info.Mode().IsRegular() {
		return nil
	}
	if err := tw.WriteHeader(h); err != nil {
		return err
	}
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	*n++
	_, err = io.Copy(tw, f)
	return err
}

func tarPull(ctx context.Context, t sshx.Target, remote, local string, isDir bool, r events.Reporter) error {
	src, name := remote, "."
	if !isDir {
		src, name = path.Dir(remote), path.Base(remote)
	}
	args := append(t.Options(true), t.Dest(), "--", sshx.RemotePath+"tar -cf - -C "+sshx.Quote(src)+" "+sshx.Quote(name))
	cmd := exec.CommandContext(ctx, osx.Which("ssh"), args...)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	tr := tar.NewReader(out)
	n := 0
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		rel := filepath.FromSlash(strings.TrimPrefix(h.Name, "./"))
		if strings.Contains(rel, "..") {
			continue
		}
		dst := filepath.Join(local, rel)
		if !isDir {
			dst = local
		}
		switch h.Typeflag {
		case tar.TypeDir:
			os.MkdirAll(dst, 0o755)
		case tar.TypeReg:
			os.MkdirAll(filepath.Dir(dst), 0o755)
			f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(h.Mode).Perm()|0o200)
			if err != nil {
				return err
			}
			_, err = io.Copy(f, tr)
			f.Close()
			if err != nil {
				return err
			}
			os.Chtimes(dst, time.Now(), h.ModTime)
			n++
		}
	}
	if err := cmd.Wait(); err != nil {
		return err
	}
	events.Donef(r, "Copied %d file(s)", n)
	return nil
}

func expand(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		h, _ := os.UserHomeDir()
		p = filepath.Join(h, strings.TrimPrefix(p, "~"))
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return abs
}

func tilde(p string) string {
	h, _ := os.UserHomeDir()
	if strings.HasPrefix(p, h) {
		return "~" + p[len(h):]
	}
	return p
}

func display(remote string) string {
	if remote == "." {
		return "~"
	}
	if strings.HasPrefix(remote, "/") {
		return remote
	}
	return "~/" + remote
}

// Expand resolves a local path the same way Push and Pull do.
func Expand(p string) string { return expand(p) }
