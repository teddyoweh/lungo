package paths

import (
	"bytes"
	"os"
	"path/filepath"
)

// WriteFile writes a file only when its contents change, and in one step: through a file of
// its own next to it, renamed into place, so a reader never sees half of it and windows
// writing at once don't trip over one temporary name. It reports whether it wrote.
func WriteFile(path string, data []byte, perm os.FileMode) (bool, error) {
	// A link (a dotfiles manager's ~/.ssh/config) stays a link: what it points at is written.
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, data) {
		return false, nil
	}
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return false, err
	}
	tmp := f.Name()
	_, werr := f.Write(data)
	cerr := f.Close()
	if werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(tmp, perm)
	}
	if werr == nil {
		werr = os.Rename(tmp, path)
	}
	if werr != nil {
		os.Remove(tmp)
		return false, werr
	}
	return true, nil
}
