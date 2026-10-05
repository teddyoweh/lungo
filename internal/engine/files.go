package engine

import (
	"context"
	"strings"
	"time"

	"skybuild/internal/sshx"
)

// filesScript lists the files worth mentioning in a prompt: what git tracks plus what is new
// and not ignored, or (outside a repository) a shallow look that skips hidden folders and
// the usual dependency folders. Capped, so a huge tree answers quickly.
const filesScript = `if git rev-parse --is-inside-work-tree >/dev/null 2>&1; then git ls-files --cached --others --exclude-standard 2>/dev/null | head -n 8000; else find . -maxdepth 5 \( -path '*/.*' -o -name node_modules -o -name vendor -o -name target -o -name dist -o -name build \) -prune -o -type f -print 2>/dev/null | sed 's|^\./||' | head -n 8000; fi; true`

// ListFiles lists files under a folder on a machine (LocalMachine: this computer), relative
// to it.
func (e *Engine) ListFiles(ctx context.Context, machine, dir string) ([]string, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cd := sshx.Quote(dir)
	if strings.HasPrefix(dir, "~/") {
		cd = `"$HOME"/` + sshx.Quote(dir[2:])
	} else if dir == "~" {
		cd = `"$HOME"`
	}
	out, err := e.runOn(ctx, machine, "cd "+cd+" 2>/dev/null || exit 0\n"+filesScript)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			files = append(files, line)
		}
	}
	return files, nil
}
