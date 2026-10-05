package apikeys

import "testing"

func TestMask(t *testing.T) {
	cases := map[string]string{
		"sk-proj-abcdefghijklmnopqrstuvwxyz123456": "sk-proj-…3456",
		"xai-abcdefghijklmnopqrstuvwxyzKmg5":       "xai-…Kmg5",
		"28b6c5c4-1111-2222-3333-444444441282":     "28b6…1282",
		"AIzaSyD-abcdefghijklmnopqrstuv":           "AIza…stuv",
		"short":                                    "••••••",
	}
	for in, want := range cases {
		if got := Mask(in); got != want {
			t.Errorf("Mask(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestForName(t *testing.T) {
	for name, want := range map[string]string{"OPENAI_API_KEY": "openai", "GOOGLE_API_KEY": "gemini", "GH_TOKEN": "github", "OPENAI_ORG_KEY": "openai"} {
		if p, ok := ForName(name); !ok || p.ID != want {
			t.Errorf("ForName(%q) = %q", name, p.ID)
		}
	}
	if _, ok := ForName("MY_THING_KEY"); ok {
		t.Error("unexpected match")
	}
	if ValidName("openai") == nil || ValidName("OPENAI_API_KEY") != nil {
		t.Error("ValidName")
	}
}
