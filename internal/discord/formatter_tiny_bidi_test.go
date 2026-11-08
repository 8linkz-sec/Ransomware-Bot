package discord

import (
	"testing"
)

func TestDiscordTinyTextBudgetsStripBidiControls(t *testing.T) {
	for _, tt := range []struct {
		name   string
		render func(string, int) string
	}{
		{"natural", discordNaturalText},
		{"rich", discordRichText},
		{"description", discordDescriptionText},
		{"URL", discordURLText},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.render("\u202eabc", 2); got != "a" {
				t.Fatalf("tiny text = %q, want bidi-free a", got)
			}
			if got := tt.render("ab", 2); got != "ab" {
				t.Fatalf("control-free tiny text = %q, want ab", got)
			}
		})
	}
}
