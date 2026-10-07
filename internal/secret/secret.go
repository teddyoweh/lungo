// Package secret keeps tokens in the OS credential store: Keychain on macOS, Credential
// Manager on Windows, Secret Service on Linux.
package secret

import (
	"errors"
	"sync"
	"time"

	"github.com/zalando/go-keyring"
)

const service = "skybuild"

// Names of stored secrets.
const (
	TailscaleAuthKey  = "tailscale-authkey"
	ClaudeTokenPrefix = "claude-token:" // + accountUuid
	ScreenLoginPrefix = "screen-login:" // + machine name: "user\npassword" for its Screen Sharing
)

// Reading the Keychain starts /usr/bin/security each time, and the app reads the same few
// secrets every few seconds (every window, every account): answers are kept for a while.
// What this process sets is kept at once; another process's change shows within `fresh`.
const fresh = 30 * time.Second

var cache = struct {
	sync.Mutex
	val map[string]string
	at  map[string]time.Time
}{val: map[string]string{}, at: map[string]time.Time{}}

// Get returns a secret, or "" if it is not set.
func Get(name string) string {
	cache.Lock()
	if at, ok := cache.at[name]; ok && time.Since(at) < fresh {
		v := cache.val[name]
		cache.Unlock()
		return v
	}
	cache.Unlock()
	v, err := keyring.Get(service, name)
	if err != nil {
		v = ""
	}
	remember(name, v)
	return v
}

func remember(name, v string) {
	cache.Lock()
	cache.val[name], cache.at[name] = v, time.Now()
	cache.Unlock()
}

// Set stores a secret; an empty value deletes it.
func Set(name, value string) error {
	if value == "" {
		err := keyring.Delete(service, name)
		if errors.Is(err, keyring.ErrNotFound) {
			err = nil
		}
		if err == nil {
			remember(name, "")
		}
		return err
	}
	err := keyring.Set(service, name, value)
	if err == nil {
		remember(name, value)
	}
	return err
}

// Has reports whether a secret is set.
func Has(name string) bool { return Get(name) != "" }
