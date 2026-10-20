package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"skybuild/internal/claudeacct"
	"skybuild/internal/config"
	"skybuild/internal/events"
	"skybuild/internal/model"
	"skybuild/internal/paths"
	"skybuild/internal/syncer"
)

// Noticing /login.
//
// /login in any Claude on this computer changes this computer's own Claude Code login
// (~/.claude.json names the account). sky watches it:
//   - following that login (ClaudeFollowLogin), the machines and the pool's "in use" follow
//     it to the account /login chose, when that account is in the pool;
//   - in rotation, a /login to a pool account that can be used says "use this one": it
//     becomes the active account, pinned, and every session moves to it with its long-lived
//     token, so nothing has to sign in again;
//   - an account that isn't in the pool yet is offered to be added (ClaudeLocalLogin.InPool).

// LocalLogin is this computer's own Claude Code login, as the account pool sees it.
type LocalLogin struct {
	Email     string    `json:"email"`
	AccountID string    `json:"accountId,omitempty"` // the pool account it is; "" when not in the pool
	ChangedAt time.Time `json:"changedAt,omitempty"` // when sky saw it change (a /login)
}

type seenLogin struct {
	UUID      string    `json:"uuid"`
	Email     string    `json:"email"`
	ChangedAt time.Time `json:"changedAt"`
}

func seenLoginPath() string { return filepath.Join(paths.State(), "claude-login-seen.json") }

func loadSeenLogin() seenLogin {
	var s seenLogin
	if b, err := os.ReadFile(seenLoginPath()); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	return s
}

// poolAccountFor is the pool account the login is, matched by account UUID or email.
func poolAccountFor(c *config.Config, acct syncer.Account) *model.ClaudeAccount {
	if acct == nil {
		return nil
	}
	for _, a := range c.ClaudeAccounts {
		if a.ID == acct.UUID() || (acct.Email() != "" && strings.EqualFold(a.Email, acct.Email())) {
			return a
		}
	}
	return nil
}

// ClaudeLocalLogin is this computer's Claude Code login and whether the pool has it.
func (e *Engine) ClaudeLocalLogin() *LocalLogin {
	acct := syncer.LocalAccount(paths.Home())
	if acct == nil {
		return nil
	}
	l := &LocalLogin{Email: acct.Email()}
	if s := loadSeenLogin(); s.UUID == acct.UUID() {
		l.ChangedAt = s.ChangedAt
	}
	if c, err := config.Load(); err == nil {
		if a := poolAccountFor(c, acct); a != nil {
			l.AccountID = a.ID
		}
	}
	return l
}

var loginStamp struct {
	sync.Mutex
	stamp string
	uuid  string
}

// LocalLoginChanged reports whether this computer's Claude Code login is another account than
// the last one sky saw. ~/.claude.json is only read again when it changed, so this is cheap
// enough to call every few seconds.
func LocalLoginChanged() bool {
	st, err := os.Stat(filepath.Join(paths.Home(), ".claude.json"))
	if err != nil {
		return false
	}
	stamp := fmt.Sprintf("%d:%d", st.ModTime().UnixNano(), st.Size())
	loginStamp.Lock()
	if loginStamp.stamp != stamp {
		loginStamp.stamp, loginStamp.uuid = stamp, ""
		if acct := syncer.LocalAccount(paths.Home()); acct != nil {
			loginStamp.uuid = acct.UUID()
		}
	}
	uuid := loginStamp.uuid
	loginStamp.Unlock()
	return uuid != "" && uuid != loadSeenLogin().UUID
}

var followMu sync.Mutex

// FollowLocalLogin acts on this computer's Claude Code login (see the top of this file). It
// returns the account sessions were moved to, "" when nothing changed.
func (e *Engine) FollowLocalLogin(ctx context.Context, r events.Reporter) (string, error) {
	followMu.Lock()
	defer followMu.Unlock()
	acct := syncer.LocalAccount(paths.Home())
	if acct == nil {
		return "", nil
	}
	seen := loadSeenLogin()
	changed := seen.UUID != acct.UUID()
	if changed {
		first := seen.UUID == ""
		seen = seenLogin{UUID: acct.UUID(), Email: acct.Email()}
		if !first { // the first look only learns who it is
			seen.ChangedAt = time.Now()
		}
		if err := paths.Ensure(); err != nil {
			return "", err
		}
		b, _ := json.MarshalIndent(seen, "", "  ")
		if _, err := paths.WriteFile(seenLoginPath(), b, 0o600); err != nil {
			return "", err
		}
		if first {
			changed = false
		} else {
			events.Infof(r, "Claude Code on this computer is now signed in as %s", acct.Email())
			_ = e.IdentifyClaudeAccounts(ctx, r) // a nameless pool account may be this one
		}
	}
	c, err := config.Load()
	if err != nil {
		return "", err
	}
	a := poolAccountFor(c, acct)
	if a == nil {
		return "", nil // offered to be added; machines stay where they are
	}
	if a.Email == "" || a.OAuthAccount == nil { // keep the pool's name and plan for it current
		id := a.ID
		_ = config.Update(func(c *config.Config) error {
			for _, x := range c.ClaudeAccounts {
				if x.ID == id {
					if x.Email == "" {
						x.Email = acct.Email()
					}
					if defaultLabel.MatchString(x.Label) {
						x.Label = acct.Email()
					}
					x.OAuthAccount = acct
				}
			}
			return nil
		})
	}
	follow := c.Settings.ClaudeFollowLogin()
	if a.ID == activeID(c) || a.Disabled || claudeacct.Token(a.ID) == "" {
		return "", nil
	}
	switch {
	case follow:
		// Machines follow this computer, whichever account it is on.
	case changed && claudeacct.Load()[a.ID].Usable():
		// A /login to a pool account in rotation: you picked it.
	default:
		return "", nil
	}
	id, name := a.ID, a.Name()
	if err := config.Update(func(c *config.Config) error {
		c.Settings.ClaudeActive = id
		c.Settings.ClaudePinned = !follow
		return nil
	}); err != nil {
		return "", err
	}
	events.Stepf(r, "Moving sessions to %s, the account Claude Code here signed in to", name)
	e.signInEverywhere(ctx, name, r)
	return name, nil
}

// LoginToAdd is the account a /login on this computer just switched to when the pool doesn't
// have it yet, for the app to add (one browser approval). Each account is offered once every
// 12 hours: a closed or refused approval isn't reopened at every look.
func (e *Engine) LoginToAdd() syncer.Account {
	acct := syncer.LocalAccount(paths.Home())
	if acct == nil {
		return nil
	}
	s := loadSeenLogin()
	if s.UUID != acct.UUID() || s.ChangedAt.IsZero() || time.Since(s.ChangedAt) > 10*time.Minute {
		return nil // not a /login sky saw just now
	}
	c, err := config.Load()
	if err != nil || poolAccountFor(c, acct) != nil {
		return nil
	}
	if !claudeacct.Attempt("add-login:"+acct.UUID(), 12*time.Hour) {
		return nil
	}
	return acct
}
