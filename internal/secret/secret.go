// Package secret keeps tokens in the OS credential store: Keychain on macOS, Credential
// Manager on Windows, Secret Service on Linux.
package secret

import (
	"errors"

	"github.com/zalando/go-keyring"
)

const service = "skybuild"

// Names of stored secrets.
const (
	TailscaleAuthKey  = "tailscale-authkey"
	ClaudeTokenPrefix = "claude-token:" // + accountUuid
	ScreenLoginPrefix = "screen-login:" // + machine name: "user\npassword" for its Screen Sharing
)

// Get returns a secret, or "" if it is not set.
func Get(name string) string {
	v, err := keyring.Get(service, name)
	if err != nil {
		return ""
	}
	return v
}

// Set stores a secret; an empty value deletes it.
func Set(name, value string) error {
	if value == "" {
		err := keyring.Delete(service, name)
		if errors.Is(err, keyring.ErrNotFound) {
			return nil
		}
		return err
	}
	return keyring.Set(service, name, value)
}

// Has reports whether a secret is set.
func Has(name string) bool { return Get(name) != "" }
