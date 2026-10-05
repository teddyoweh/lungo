package engine

import (
	"context"
	"fmt"

	"skybuild/internal/config"
	"skybuild/internal/events"
	"skybuild/internal/model"
	"skybuild/internal/sshx"
	"skybuild/internal/syncer"
)

// KeyAdder is implemented by providers that can authorize another SSH key on a running
// machine through the cloud API (so a second computer can import machines it didn't create).
type KeyAdder interface {
	AddKey(ctx context.Context, m *model.Machine, publicKey string) error
}

// Import adds sky machines found in a cloud account that this computer doesn't know yet.
func (e *Engine) Import(ctx context.Context, providerID, account string, r events.Reporter) ([]*model.Machine, error) {
	p, err := e.Provider(providerID)
	if err != nil {
		return nil, err
	}
	if account == "" {
		s, _ := e.Settings()
		account = s.For(providerID).Account
	}
	if account == "" {
		for _, a := range p.Status(ctx).Accounts {
			if a.Default {
				account = a.ID
			}
		}
	}
	if account == "" {
		return nil, fmt.Errorf("pass --account")
	}
	found, err := p.Discover(ctx, account)
	if err != nil {
		return nil, err
	}
	_, pub, err := sshx.EnsureKey()
	if err != nil {
		return nil, err
	}
	c, err := config.Load()
	if err != nil {
		return nil, err
	}
	var added []*model.Machine
	for _, m := range found {
		if c.Machine(m.Name) != nil {
			continue
		}
		if m.User == "" {
			m.User = c.Settings.User()
		}
		m.Sync = syncer.DefaultItems()
		if !sshx.Reachable(ctx, sshx.For(m, m.Address(false))) {
			if ka, ok := p.(KeyAdder); ok && m.Status == model.StatusRunning {
				events.Infof(r, "Authorizing this computer's key on %s", m.Name)
				if err := ka.AddKey(ctx, m, pub); err != nil {
					events.Warnf(r, "%s: %v", m.Name, err)
				}
			} else {
				events.Warnf(r, "%s: add this computer's key (%s.pub) to it to connect", m.Name, "~/.skybuild/keys/sky_ed25519")
			}
		}
		if err := e.save(m); err != nil {
			return added, err
		}
		events.Infof(r, "Imported %s", m.Name)
		added = append(added, m)
	}
	return added, nil
}
