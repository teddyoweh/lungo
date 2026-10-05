//go:build !windows

package syncer

import "testing"

func TestParseToken(t *testing.T) {
	tok := "sk-ant-oat01-" + "abcdefghijklmnopqrstuvwxyz0123456789_-ABCDEFGHIJ"
	raw := "\x1b[32m✓ Long-lived token created\x1b[0m\r\n\r\n  " + tok[:30] + "\r\n  " + tok[30:] + "\r\n\r\nStore this token securely."
	got, err := parseToken(raw)
	if err != nil || got != tok {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := parseToken("nothing here"); err == nil {
		t.Error("expected error")
	}
}
