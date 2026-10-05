// Package provider defines what a cloud adapter must do. Each adapter drives the cloud's own
// CLI (gcloud, aws, az), so "linking an account" means signing in to that CLI once and sky
// reuses the same login the user already has.
package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"skybuild/internal/events"
	"skybuild/internal/model"
	"skybuild/internal/osx"
)

// Provider creates and manages machines in one cloud.
type Provider interface {
	ID() string
	Label() string
	// Status checks the CLI is installed and signed in, and lists usable accounts.
	Status(ctx context.Context) model.ProviderStatus
	// Login runs the CLI's own sign-in, which opens a browser.
	Login(ctx context.Context, r events.Reporter) error
	Regions() []model.Region
	DefaultRegion() string
	Sizes() []model.Size
	Create(ctx context.Context, s model.Spec, r events.Reporter) (*model.Machine, error)
	// Refresh updates status and addresses from the cloud.
	Refresh(ctx context.Context, m *model.Machine) error
	Start(ctx context.Context, m *model.Machine) error
	Stop(ctx context.Context, m *model.Machine) error
	// Delete removes the instance, and the data volume unless keepDisk.
	Delete(ctx context.Context, m *model.Machine, keepDisk bool, r events.Reporter) error
	// Resize changes the machine type, stopping and restarting it if needed.
	Resize(ctx context.Context, m *model.Machine, size string, r events.Reporter) error
	// GrowDisk enlarges the data volume (the engine grows the filesystem afterwards).
	GrowDisk(ctx context.Context, m *model.Machine, gb int) error
	// Discover lists sky machines in an account, to re-import them on another computer.
	Discover(ctx context.Context, account string) ([]*model.Machine, error)
}

// Label keys put on every cloud resource sky creates.
const (
	LabelKey   = "skybuild"
	LabelValue = "machine"
	LabelUser  = "sky-user"
)

// CLI runs a cloud CLI and decodes JSON output.
type CLI struct {
	Bin string
	Env []string // extra environment, e.g. AWS_PROFILE
	Pre []string // args added before every call
}

// Run returns stdout.
func (c CLI) Run(ctx context.Context, args ...string) (string, error) {
	r, err := osx.Exec(ctx, osx.Cmd{Name: c.Bin, Args: append(append([]string{}, c.Pre...), args...), Env: c.Env})
	return r.Stdout, err
}

// JSON runs and decodes stdout into v.
func (c CLI) JSON(ctx context.Context, v any, args ...string) error {
	out, err := c.Run(ctx, args...)
	if err != nil {
		return err
	}
	if strings.TrimSpace(out) == "" {
		return nil
	}
	if err := json.Unmarshal([]byte(out), v); err != nil {
		return fmt.Errorf("%s returned unexpected output: %w", c.Bin, err)
	}
	return nil
}

// TempFile writes content to a private temp file and returns its path and a cleanup func.
func TempFile(pattern, content string) (string, func(), error) {
	f, err := os.CreateTemp("", pattern)
	if err != nil {
		return "", func() {}, err
	}
	name := f.Name()
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		os.Remove(name)
		return "", func() {}, err
	}
	f.Close()
	return name, func() { os.Remove(name) }, nil
}

// SizeByID finds a size in a catalogue.
func SizeByID(sizes []model.Size, id string) (model.Size, bool) {
	for _, s := range sizes {
		if s.ID == id {
			return s, true
		}
	}
	return model.Size{}, false
}

// DefaultSize returns the size marked Default, or the first.
func DefaultSize(sizes []model.Size) model.Size {
	for _, s := range sizes {
		if s.Default {
			return s
		}
	}
	if len(sizes) == 0 {
		return model.Size{}
	}
	return sizes[0]
}

// IsNotFound guesses whether a CLI error means the resource does not exist.
func IsNotFound(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	for _, k := range []string{"not found", "notfound", "was not found", "does not exist", "invalidinstanceid.notfound", "resourcenotfound"} {
		if strings.Contains(s, k) {
			return true
		}
	}
	return false
}
