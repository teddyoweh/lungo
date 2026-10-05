// Package config stores machines, folder links and settings in ~/.skybuild/config.json.
// The CLI, the desktop app and the background sync can all run at once, so every write
// goes through Update, which holds a file lock and re-reads the file first.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/user"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/gofrs/flock"

	"skybuild/internal/model"
	"skybuild/internal/paths"
)

// Defaults remembers the last choices per provider so the next machine is one keypress.
type Defaults struct {
	Account string `json:"account,omitempty"`
	Region  string `json:"region,omitempty"`
	Zone    string `json:"zone,omitempty"`
	Size    string `json:"size,omitempty"`
	DiskGB  int    `json:"diskGB,omitempty"`
}

// Settings are user preferences.
type Settings struct {
	DefaultProvider string              `json:"defaultProvider,omitempty"`
	RemoteUser      string              `json:"remoteUser,omitempty"` // user created on new machines
	Defaults        map[string]Defaults `json:"defaults,omitempty"`
	Tailscale       *bool               `json:"tailscale,omitempty"`    // join new machines to the tailnet
	Docker          *bool               `json:"docker,omitempty"`       // install Docker on new machines
	AutoTmux        *bool               `json:"autoTmux,omitempty"`     // SSH logins land in tmux
	Terminal        string              `json:"terminal,omitempty"`     // app used by "Connect"
	SyncInterval    int                 `json:"syncInterval,omitempty"` // seconds between background syncs
	SyncPaths       []string            `json:"syncPaths,omitempty"`    // extra files/folders under ~ to sync
	SkipCredentials []string            `json:"skipCredentials,omitempty"`
	SkipMCP         []string            `json:"skipMCP,omitempty"`
	SkipEnv         []string            `json:"skipEnv,omitempty"`          // API key names never synced
	ClaudeActive    string              `json:"claudeActive,omitempty"`     // account machines use now
	ClaudeAuto      *bool               `json:"claudeAutoSwitch,omitempty"` // switch accounts when one hits a limit
	ClaudeStrategy  string              `json:"claudeStrategy,omitempty"`   // "smart" (default): use what expires first; "order": my list order
	// Leave running Claude sessions on the old login when machines switch accounts (they move
	// when they next start). By default they are restarted onto the new one when idle.
	ClaudeLeaveSessions bool     `json:"claudeLeaveSessions,omitempty"`
	ProjectRoots        []string `json:"projectRoots,omitempty"` // folders scanned for git repos (default: the usual code folders)
}

// Config is the whole file.
type Config struct {
	Version        int                    `json:"version"`
	Machines       []*model.Machine       `json:"machines"`
	Links          []*model.Link          `json:"links"`
	ClaudeAccounts []*model.ClaudeAccount `json:"claudeAccounts,omitempty"` // in priority order
	APIKeys        []*model.APIKey        `json:"apiKeys,omitempty"`
	Settings       Settings               `json:"settings"`
	Projects       []*Project             `json:"projects,omitempty"` // repos and where they live on machines
	Idle           map[string]IdleStop    `json:"idle,omitempty"`     // stop when idle, by machine name

	// Fields this build doesn't know (written by a newer sky or app) are carried through
	// untouched, so an older CLI and a newer app can share the file without losing data.
	unknown         map[string]json.RawMessage
	unknownSettings map[string]json.RawMessage
}

// IdleStop is one machine's stop-when-idle setting. It is kept beside the machines rather than
// on them: a build that doesn't know the setting rewrites machines from the fields it has,
// but carries a top-level member it doesn't know through untouched.
type IdleStop struct {
	Minutes int  `json:"minutes"`          // stop after this long with nothing going on
	DryRun  bool `json:"dryRun,omitempty"` // the machine only logs that it would stop
}

// IdleFor is a machine's stop-when-idle setting; the zero value means off.
func (c *Config) IdleFor(name string) IdleStop { return c.Idle[name] }

// SetIdle stores a machine's stop-when-idle setting; zero minutes turns it off.
func (c *Config) SetIdle(name string, s IdleStop) {
	if s.Minutes <= 0 {
		delete(c.Idle, name)
		return
	}
	if c.Idle == nil {
		c.Idle = map[string]IdleStop{}
	}
	c.Idle[name] = s
}

// Project remembers a repo: where it is on this computer and in which folder on each machine.
type Project struct {
	Name    string            `json:"name"`
	Local   string            `json:"local,omitempty"`   // absolute path here ("" = only on machines)
	Origin  string            `json:"origin,omitempty"`  // normalized remote URL (github.com/owner/repo), "" = none
	Remotes map[string]string `json:"remotes,omitempty"` // machine name → folder there ("~/code/name")
	Base    map[string]string `json:"base,omitempty"`    // machine name → tree both sides had after the last handoff
	Added   time.Time         `json:"added"`
}

func on(b *bool, def bool) bool {
	if b == nil {
		return def
	}
	return *b
}

func (s Settings) TailscaleOn() bool  { return on(s.Tailscale, true) }
func (s Settings) DockerOn() bool     { return on(s.Docker, true) }
func (s Settings) AutoTmuxOn() bool   { return on(s.AutoTmux, true) }
func (s Settings) ClaudeAutoOn() bool { return on(s.ClaudeAuto, true) }

// ClaudeSmart reports whether accounts are ranked automatically (the default) rather than
// used in the order the user arranged them.
func (s Settings) ClaudeSmart() bool { return s.ClaudeStrategy != "order" }

// Interval is the background sync period.
func (s Settings) Interval() time.Duration {
	if s.SyncInterval < 30 {
		return 2 * time.Minute
	}
	return time.Duration(s.SyncInterval) * time.Second
}

// User is the account name created on new machines: the setting, else the local username.
func (s Settings) User() string {
	if s.RemoteUser != "" {
		return s.RemoteUser
	}
	return LocalUser()
}

var unsafeUser = regexp.MustCompile(`[^a-z0-9_-]`)

// LocalUser turns this computer's username into a valid Linux username.
func LocalUser() string {
	name := "dev"
	if u, err := user.Current(); err == nil {
		name = u.Username
	}
	if i := strings.LastIndexAny(name, `\/`); i >= 0 { // DOMAIN\user on Windows
		name = name[i+1:]
	}
	name = unsafeUser.ReplaceAllString(strings.ToLower(name), "")
	reserved := map[string]bool{"root": true, "ubuntu": true, "admin": true, "": true}
	if reserved[name] || name[0] >= '0' && name[0] <= '9' {
		name = "dev"
	}
	if len(name) > 30 {
		name = name[:30]
	}
	return name
}

// Load reads the config, returning an empty one if there is none yet.
func Load() (*Config, error) {
	c := &Config{Version: 1}
	b, err := os.ReadFile(paths.Config())
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, c); err != nil {
		return nil, fmt.Errorf("%s is not valid JSON: %w", paths.Config(), err)
	}
	c.unknown = unknownKeys(b, reflect.TypeOf(Config{}))
	var raw struct {
		Settings json.RawMessage `json:"settings"`
	}
	if json.Unmarshal(b, &raw) == nil && len(raw.Settings) > 0 {
		c.unknownSettings = unknownKeys(raw.Settings, reflect.TypeOf(Settings{}))
	}
	return c, nil
}

// unknownKeys returns the members of a JSON object that t has no field for.
func unknownKeys(b []byte, t reflect.Type) map[string]json.RawMessage {
	var all map[string]json.RawMessage
	if json.Unmarshal(b, &all) != nil {
		return nil
	}
	for i := 0; i < t.NumField(); i++ {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		delete(all, name)
	}
	if len(all) == 0 {
		return nil
	}
	return all
}

// withUnknown adds carried-through members back into a marshalled object.
func withUnknown(v any, unknown map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	out := map[string]json.RawMessage{}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	for k, raw := range unknown {
		if _, mine := out[k]; !mine {
			out[k] = raw
		}
	}
	return out, nil
}

// Update loads the config under a lock, lets fn change it and saves it if fn succeeds.
func Update(fn func(*Config) error) error {
	if err := paths.Ensure(); err != nil {
		return err
	}
	lock := flock.New(paths.Config() + ".lock")
	if err := lock.Lock(); err != nil {
		return err
	}
	defer lock.Unlock()
	c, err := Load()
	if err != nil {
		return err
	}
	if err := fn(c); err != nil {
		return err
	}
	return c.save()
}

func (c *Config) save() error {
	sort.Slice(c.Machines, func(i, j int) bool { return c.Machines[i].Name < c.Machines[j].Name })
	var v any = c
	if len(c.unknown) > 0 || len(c.unknownSettings) > 0 {
		top, err := withUnknown(c, c.unknown)
		if err != nil {
			return err
		}
		settings, err := withUnknown(c.Settings, c.unknownSettings)
		if err != nil {
			return err
		}
		if top["settings"], err = json.Marshal(settings); err != nil {
			return err
		}
		v = top
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := paths.Config() + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, paths.Config())
}

// Machine finds a machine by name.
func (c *Config) Machine(name string) *model.Machine {
	for _, m := range c.Machines {
		if m.Name == name {
			return m
		}
	}
	return nil
}

// Put adds or replaces a machine.
func (c *Config) Put(m *model.Machine) {
	m.UpdatedAt = time.Now()
	for i, x := range c.Machines {
		if x.Name == m.Name {
			c.Machines[i] = m
			return
		}
	}
	if m.CreatedAt.IsZero() {
		m.CreatedAt = m.UpdatedAt
	}
	c.Machines = append(c.Machines, m)
}

// Remove drops a machine and its folder links.
func (c *Config) Remove(name string) {
	out := c.Machines[:0]
	for _, m := range c.Machines {
		if m.Name != name {
			out = append(out, m)
		}
	}
	c.Machines = out
	links := c.Links[:0]
	for _, l := range c.Links {
		if l.Machine != name {
			links = append(links, l)
		}
	}
	c.Links = links
	delete(c.Idle, name)
}

// Defaults for a provider (never nil map access).
func (s *Settings) For(provider string) Defaults {
	if s.Defaults == nil {
		return Defaults{}
	}
	return s.Defaults[provider]
}

// Remember stores the choices just used for a provider.
func (s *Settings) Remember(provider string, d Defaults) {
	if s.Defaults == nil {
		s.Defaults = map[string]Defaults{}
	}
	s.Defaults[provider] = d
	s.DefaultProvider = provider
}

var validName = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,40}[a-z0-9])?$`)

// ValidName checks a machine name: it becomes an SSH alias, a hostname and a cloud resource name.
func ValidName(name string) error {
	if !validName.MatchString(name) {
		return fmt.Errorf("%q: use lowercase letters, digits and dashes, start with a letter, max 42 chars", name)
	}
	return nil
}
