package textutil

import "testing"

func TestStripHTMLInvisibleFormattingKeepsTextMeaning(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"word joiner in filename", "invoice\u2060.exe", "invoice.exe"},
		{"entity encoded word joiner", "invoice&#x2060;.exe", "invoice.exe"},
		{"soft hyphen in filename", "invoice\u00ad.exe", "invoice.exe"},
		{"entity encoded soft hyphen", "invoice&#xAD;.exe", "invoice.exe"},
		{"Mongolian vowel separator", "\u182c\u1820\u1828\u180e\u1820", "\u182c\u1820\u1828\u180e\u1820"},
		{"combining grapheme joiner", "a\u0301\u034f\u0323", "a\u0301\u034f\u0323"},
		{"left to right mark", "\u05e9\u200eABC", "\u05e9\u200eABC"},
		{"right to left mark", "ABC\u200f\u05e9", "ABC\u200f\u05e9"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := StripHTML(tt.in); got != tt.want {
				t.Fatalf("StripHTML(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
