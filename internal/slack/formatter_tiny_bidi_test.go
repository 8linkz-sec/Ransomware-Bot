package slack

import "testing"

func TestSlackTinyTextBudgetsStripBidiControls(t *testing.T) {
	for _, tt := range []struct {
		name   string
		render func(string, int) string
	}{
		{"natural", slackNaturalText},
		{"description", slackDescriptionText},
		{"URL", slackURLText},
	} {
		t.Run(tt.name, func(t *testing.T) {
			assertSlackTinyBidiControlBudgets(t, tt.render)
			assertSlackTinyBidiOtherCases(t, tt.name, tt.render)
		})
	}
}

func assertSlackTinyBidiControlBudgets(t *testing.T, render func(string, int) string) {
	t.Helper()
	if got := render("\u202eabc", 2); got != "ab" {
		t.Fatalf("tiny text = %q, want bidi-free ab", got)
	}
	if got := render("\u202e\u202eabc", 2); got != "ab" {
		t.Fatalf("tiny text after two controls = %q, want visible ab", got)
	}
	if got := render("\u202e\u2067", 2); got != "-" {
		t.Fatalf("controls-only tiny text = %q, want safe nonempty placeholder", got)
	}
	if got := render("\u202e\u2067", 1); got != "-" {
		t.Fatalf("controls-only one-rune text = %q, want safe nonempty placeholder", got)
	}
	if got := render("\u202e\u2067", 0); got != "" {
		t.Fatalf("zero-budget text = %q, want empty", got)
	}
}

func assertSlackTinyBidiOtherCases(t *testing.T, name string, render func(string, int) string) {
	t.Helper()
	if got := render("ab", 2); got != "ab" {
		t.Fatalf("control-free tiny text = %q, want ab", got)
	}
	if got := render("\u202eA&<>", 2); got != "A" {
		t.Fatalf("tiny escaped text = %q, want intact entity boundary after A", got)
	}
	if got := render("", 2); got != "" {
		t.Fatalf("empty input = %q, want empty", got)
	}
	wantNormal := "\u2068abc\u2069"
	if name == "URL" {
		wantNormal = "\u2066abc\u2069"
	}
	if got := render("\u202eabc", 8); got != wantNormal {
		t.Fatalf("normal-budget text = %q, want %q", got, wantNormal)
	}
}
