//go:build windows

package syncer

import (
	"context"
	"errors"
)

// MintToken can't drive `claude setup-token` without a Unix pseudo-terminal; on Windows the
// user runs it and pastes the token.
func MintToken(context.Context) (string, error) {
	return "", errors.New("run `claude setup-token` in a terminal, then `sky login claude --token <token>`")
}
