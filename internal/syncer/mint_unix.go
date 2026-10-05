//go:build !windows

package syncer

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/creack/pty"

	"skybuild/internal/osx"
)

var (
	oscRe = regexp.MustCompile(`\x1b\][^\x07\x1b]*(\x07|\x1b\\)`)
	csiRe = regexp.MustCompile(`\x1b\[[0-9;?<>=]*[A-Za-z~]`)
	escRe = regexp.MustCompile(`\x1b[()][A-Za-z0-9]|\x1b[=>]`)
)

// MintToken runs `claude setup-token`, which signs in through the browser (approving by
// itself when the browser is already signed in to claude.ai) and prints a token valid for a
// year. It needs a terminal, so it runs under a pseudo-terminal wide enough not to wrap.
func MintToken(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, osx.Which("claude"), "setup-token")
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 50, Cols: 400})
	if err != nil {
		return "", err
	}
	defer f.Close()
	var buf bytes.Buffer
	done := make(chan struct{})
	go func() {
		io.Copy(&buf, f)
		close(done)
	}()
	_ = cmd.Wait()
	f.Close()
	<-done
	return parseToken(buf.String())
}

func parseToken(raw string) (string, error) {
	raw = oscRe.ReplaceAllString(raw, "")
	raw = csiRe.ReplaceAllString(raw, "")
	raw = escRe.ReplaceAllString(raw, "")
	start := strings.LastIndex(raw, "sk-ant-oat01-")
	if start < 0 {
		return "", errors.New("claude setup-token didn't finish; run `sky login claude` to try again")
	}
	rest := raw[start:]
	if end := strings.Index(rest, "Store"); end > 0 {
		rest = rest[:end]
	} else if end := strings.IndexAny(rest, " \t"); end > 0 {
		rest = rest[:end]
	}
	token := strings.Join(strings.Fields(rest), "")
	if !tokenRe.MatchString(token) {
		return "", errors.New("could not read the token from claude setup-token")
	}
	return token, nil
}
