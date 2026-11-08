package textutil

import (
	"fmt"
	"testing"
)

func TestStripHTMLRemovesBidiControlsAfterEntityDecoding(t *testing.T) {
	for _, control := range []rune{
		'\u202a', '\u202b', '\u202c', '\u202d', '\u202e',
		'\u2066', '\u2067', '\u2068', '\u2069',
	} {
		t.Run(fmt.Sprintf("U+%04X", control), func(t *testing.T) {
			inputs := []string{
				"evil" + string(control) + "file.exe",
				"<p>evil" + string(control) + "file.exe</p>",
				"evil" + fmt.Sprintf("&#%d;", control) + "file.exe",
			}
			for _, input := range inputs {
				if got := StripHTML(input); got != "evilfile.exe" {
					t.Errorf("StripHTML(%q) = %q, want %q", input, got, "evilfile.exe")
				}
			}
		})
	}

	if got := StripHTML("evil&#x202e;file.exe"); got != "evilfile.exe" {
		t.Errorf("hex-encoded RLO: got %q", got)
	}
	if got := StripHTML("first \u202e second"); got != "first second" {
		t.Errorf("whitespace around RLO: got %q", got)
	}
	if got := StripHTML("first\u200c\u200dsecond"); got != "first\u200c\u200dsecond" {
		t.Errorf("joining marks changed: got %q", got)
	}
	if got := StripHTML("ordinary text stays as-is"); got != "ordinary text stays as-is" {
		t.Errorf("ordinary text changed: got %q", got)
	}
	if got := StripHTML("&#x202e;"); got != "" {
		t.Errorf("only RLO: got %q, want empty", got)
	}
}
