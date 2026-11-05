package jira

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCleanFilename(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"../../etc/passwd":  "passwd",
		`C:\Users\me\a.png`: "a.png",
		"bad\x00\"name.txt": "badname.txt",
		"\xff\xfe.png":      ".png",
		"":                  "attachment",
	}

	for in, want := range tests {
		if got := cleanFilename(in); got != want {
			t.Errorf("cleanFilename(%q) = %q, want %q", in, got, want)
		}
	}

	// 100 three-byte characters are 300 bytes, so the cut lands inside one.
	long := cleanFilename(strings.Repeat("漢", 100) + ".png")
	if !utf8.ValidString(long) || len(long) > maxFilename || !strings.HasSuffix(long, ".png") {
		t.Errorf("a long name became %d bytes, valid %v: %q", len(long), utf8.ValidString(long), long)
	}
}
