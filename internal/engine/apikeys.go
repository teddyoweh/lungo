package engine

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"skybuild/internal/apikeys"
	"skybuild/internal/config"
	"skybuild/internal/events"
	"skybuild/internal/model"
	"skybuild/internal/syncer"
)

// APIKeyView is one API key for lists: stored in sky, found in a shell file, or both.
type APIKeyView struct {
	Name     string   `json:"name"`
	Provider string   `json:"provider"`
	Label    string   `json:"label"`
	Icon     string   `json:"icon"`
	Docs     string   `json:"docs,omitempty"`
	Masked   string   `json:"masked"`
	Stored   bool     `json:"stored"`          // kept in sky's keychain
	Shell    string   `json:"shell,omitempty"` // exported from this shell file
	Sync     bool     `json:"sync"`            // goes to machines
	Machines []string `json:"machines"`        // empty = every machine (stored keys)
	CanTest  bool     `json:"canTest"`
}

// APIKeys lists every API key sky can put on machines.
func (e *Engine) APIKeys(ctx context.Context) ([]APIKeyView, error) {
	c, err := config.Load()
	if err != nil {
		return nil, err
	}
	skip := map[string]bool{}
	for _, n := range c.Settings.SkipEnv {
		skip[n] = true
	}
	views := map[string]*APIKeyView{}
	get := func(name string) *APIKeyView {
		if v := views[name]; v != nil {
			return v
		}
		v := &APIKeyView{Name: name, Sync: !skip[name], Machines: []string{}}
		if p, ok := apikeys.ForName(name); ok {
			v.Provider, v.Label, v.Icon, v.Docs, v.CanTest = p.ID, p.Label, p.Icon, p.Docs, p.CanTest()
		}
		views[name] = v
		return v
	}
	shellVals, from := syncer.ShellSecrets(ctx)
	for name, val := range shellVals {
		v := get(name)
		v.Shell, v.Masked = from[name], apikeys.Mask(val)
	}
	for _, k := range c.APIKeys {
		v := get(k.Name)
		v.Stored = true
		v.Masked = apikeys.Mask(apikeys.Get(k.Name))
		if k.Machines != nil {
			v.Machines = k.Machines
		}
		if k.Provider != "" {
			if p, ok := apikeys.ByID(k.Provider); ok {
				v.Provider, v.Label, v.Icon, v.Docs, v.CanTest = p.ID, p.Label, p.Icon, p.Docs, p.CanTest()
			}
		}
	}
	out := make([]APIKeyView, 0, len(views))
	for _, v := range views {
		if v.Label == "" {
			v.Label = v.Name
		}
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Stored != out[j].Stored {
			return out[i].Stored
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// SetAPIKey stores (or replaces) a key. machines empty = every machine.
func (e *Engine) SetAPIKey(name, value, provider string, machines []string) error {
	name = strings.TrimSpace(strings.ToUpper(name))
	value = strings.TrimSpace(value)
	if err := apikeys.ValidName(name); err != nil {
		return err
	}
	if value == "" {
		return errors.New("the key is empty")
	}
	if provider == "" {
		if p, ok := apikeys.ForName(name); ok {
			provider = p.ID
		}
	}
	if err := apikeys.Set(name, value); err != nil {
		return err
	}
	return config.Update(func(c *config.Config) error {
		for _, k := range c.APIKeys {
			if k.Name == name {
				k.Provider = provider
				if machines != nil {
					k.Machines = machines
				}
				return nil
			}
		}
		c.APIKeys = append(c.APIKeys, &model.APIKey{Name: name, Provider: provider, Machines: machines, AddedAt: time.Now()})
		c.Settings.SkipEnv = without(c.Settings.SkipEnv, name)
		return nil
	})
}

// RemoveAPIKey deletes a stored key (keys in shell files stay; turn their sync off instead).
func (e *Engine) RemoveAPIKey(name string) error {
	found := false
	err := config.Update(func(c *config.Config) error {
		out := c.APIKeys[:0]
		for _, k := range c.APIKeys {
			if k.Name == name {
				found = true
				continue
			}
			out = append(out, k)
		}
		c.APIKeys = out
		return nil
	})
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("sky doesn't store %s (if it's in a shell file, turn its sync off instead)", name)
	}
	return apikeys.Set(name, "")
}

// SetAPIKeySync turns syncing a key to machines on or off.
func (e *Engine) SetAPIKeySync(name string, on bool) error {
	return e.UpdateSettings(func(s *config.Settings) {
		s.SkipEnv = without(s.SkipEnv, name)
		if !on {
			s.SkipEnv = append(s.SkipEnv, name)
		}
	})
}

// SetAPIKeyMachines picks which machines get a stored key (empty = all).
func (e *Engine) SetAPIKeyMachines(name string, machines []string) error {
	return config.Update(func(c *config.Config) error {
		for _, k := range c.APIKeys {
			if k.Name == name {
				k.Machines = machines
				return nil
			}
		}
		return fmt.Errorf("sky doesn't store %s", name)
	})
}

// TestAPIKey checks a key (stored or from a shell file) against its provider.
func (e *Engine) TestAPIKey(ctx context.Context, name string) (bool, string, error) {
	val := apikeys.Get(name)
	if val == "" {
		vals, _ := syncer.ShellSecrets(ctx)
		val = vals[name]
	}
	if val == "" {
		return false, "", fmt.Errorf("no value for %s", name)
	}
	c, _ := config.Load()
	p, ok := apikeys.ForName(name)
	for _, k := range c.APIKeys {
		if k.Name == name && k.Provider != "" {
			p, ok = apikeys.ByID(k.Provider)
		}
	}
	if !ok {
		return false, "", fmt.Errorf("sky doesn't know which service %s is for", name)
	}
	return apikeys.Test(ctx, p, val)
}

// PushKeys syncs API keys (and nothing else) to every machine.
func (e *Engine) PushKeys(ctx context.Context, r events.Reporter) []syncer.Result {
	return e.Sync(ctx, nil, syncer.Options{Only: []string{syncer.ItemEnv}}, r)
}

func (e *Engine) managedKeys() []syncer.ManagedKey {
	c, err := config.Load()
	if err != nil {
		return nil
	}
	var out []syncer.ManagedKey
	for _, k := range c.APIKeys {
		if v := apikeys.Get(k.Name); v != "" {
			out = append(out, syncer.ManagedKey{Name: k.Name, Value: v, Machines: k.Machines})
		}
	}
	return out
}

func without(list []string, s string) []string {
	out := list[:0:0]
	for _, x := range list {
		if x != s {
			out = append(out, x)
		}
	}
	return out
}
