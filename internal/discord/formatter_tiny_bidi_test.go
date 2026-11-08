package discord

import (
	"testing"
)

func TestDiscordTinyTextBudgetsStripBidiControls(t *testing.T) {
	for _, tt := range []struct {
		name       string
		render     func(string, int) string
		wantNormal string
	}{
		{"natural", discordNaturalText, "\u2068abc\u2069"},
		{"rich", discordRichText, "\u2068abc\u2069"},
		{"description", discordDescriptionText, "\u2068abc\u2069"},
		{"URL", discordURLText, "\u2066abc\u2069"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			assertDiscordTinyBidiCases(t, tt.render, tt.wantNormal)
		})
	}
}

func assertDiscordTinyBidiCases(t *testing.T, render func(string, int) string, wantNormal string) {
	t.Helper()
	for _, tt := range []struct {
		name   string
		input  string
		budget int
		want   string
	}{
		{"one control", "\u202eabc", 2, "ab"},
		{"two controls", "\u202e\u202eabc", 2, "ab"},
		{"controls only", "\u202e\u2067", 2, "-"},
		{"controls only one rune", "\u202e\u2067", 1, "-"},
		{"zero budget", "\u202e\u2067", 0, ""},
		{"control free", "ab", 2, "ab"},
		{"empty input", "", 2, ""},
		{"normal budget", "\u202eabc", 8, wantNormal},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := render(tt.input, tt.budget); got != tt.want {
				t.Fatalf("render(%q, %d) = %q, want %q", tt.input, tt.budget, got, tt.want)
			}
		})
	}
}
