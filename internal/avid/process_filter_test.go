package avid

import "testing"

func TestProcessFilter(t *testing.T) {
	cases := map[string]string{
		"AvidMediaComposer":     "AvidMediaComposer.exe",
		"AvidMediaComposer.exe": "AvidMediaComposer.exe",
		"avidmediacomposer.EXE": "avidmediacomposer.EXE", // suffix check é case-insensitive
		"":                      ".exe",                  // edge case — caller não devia passar vazio
	}
	for in, want := range cases {
		if got := processFilter(in); got != want {
			t.Errorf("processFilter(%q) = %q, esperava %q", in, got, want)
		}
	}
}
