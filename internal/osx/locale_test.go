package osx

import "testing"

func TestIsUTF8Locale(t *testing.T) {
	for _, c := range []struct {
		all, ctype, lang string
		want             bool
	}{
		{"", "", "", false}, // an app opened from the Dock
		{"", "", "en_US.UTF-8", true},
		{"", "", "C.utf8", true},
		{"", "", "C", false},
		{"", "UTF-8", "", true},         // macOS Terminal's LC_CTYPE
		{"C", "", "en_US.UTF-8", false}, // LC_ALL wins, as tmux reads it
	} {
		t.Setenv("LC_ALL", c.all)
		t.Setenv("LC_CTYPE", c.ctype)
		t.Setenv("LANG", c.lang)
		if got := isUTF8Locale(); got != c.want {
			t.Errorf("LC_ALL=%q LC_CTYPE=%q LANG=%q: %v, want %v", c.all, c.ctype, c.lang, got, c.want)
		}
	}
}
