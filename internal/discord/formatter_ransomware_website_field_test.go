package discord

import (
	"strings"
	"testing"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/api"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/formatfields"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/notifyfmt"
)

// Titles are never links (operator decision): the Discord
// ransomware embed no longer sets embed.URL, so the website/darknet URL must
// always be visible as its own field instead. These tests pin the new
// fallback field formatRansomwareEmbed inserts (see internal/discord/formatter.go).

func TestFormatRansomwareEmbedShowsWebsiteFieldWhenNotConfigured(t *testing.T) {
	entry := api.RansomwareEntry{
		Group:      "LockBit",
		Victim:     "Example Corp",
		WebsiteURL: "http://lockbitxyz123.onion/example-corp",
	}
	formatCfg := notifyfmt.DefaultFormatOptions()

	embed := formatRansomwareEmbed(entry, &formatCfg)

	if embed.URL != "" {
		t.Fatalf("embed.URL = %q, want empty (titles are never links)", embed.URL)
	}
	if !containsDiscordFieldValue(embed.Fields, entry.WebsiteURL) {
		t.Fatalf("embed fields = %#v, want the website URL present", embed.Fields)
	}
	// Pins the before-the-configured-loop insertion: without this, "move
	// the fallback after the loop" survives every other test in this file.
	if got := discordFieldIndex(embed.Fields, "Website"); got != 0 {
		t.Fatalf("Website field index = %d, want 0 (inserted before the configured loop)", got)
	}
}

func TestFormatRansomwareEmbedWebsiteFieldNotDuplicatedWhenConfigured(t *testing.T) {
	tests := []struct {
		name    string
		spelled string
	}{
		{name: "website", spelled: "website"},
		{name: "url alias", spelled: "url"},
		{name: "uppercase", spelled: "WEBSITE"},
		{name: "whitespace padded", spelled: "  website  "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry := api.RansomwareEntry{
				Group:      "LockBit",
				Victim:     "Example Corp",
				WebsiteURL: "http://lockbitxyz123.onion/example-corp",
			}
			formatCfg := &notifyfmt.FormatOptions{
				FieldOrder: []string{"group", "victim", tt.spelled},
			}

			embed := formatRansomwareEmbed(entry, formatCfg)

			if embed.URL != "" {
				t.Fatalf("embed.URL = %q, want empty (titles are never links)", embed.URL)
			}
			count := 0
			for _, field := range embed.Fields {
				if containsDiscordFieldValue([]*MessageEmbedField{field}, entry.WebsiteURL) {
					count++
				}
			}
			if count != 1 {
				t.Fatalf("website-carrying field count = %d, want exactly 1: %#v", count, embed.Fields)
			}
			if got := discordFieldIndex(embed.Fields, "Website"); got != 2 {
				t.Fatalf("Website field index = %d, want 2 (its configured position, not hoisted)", got)
			}
		})
	}
}

func TestFormatRansomwareEmbedNoWebsiteFieldWhenURLEmptyAndNotShown(t *testing.T) {
	entry := api.RansomwareEntry{Group: "LockBit", Victim: "Example Corp"}
	formatCfg := &notifyfmt.FormatOptions{FieldOrder: []string{"group", "victim"}}

	embed := formatRansomwareEmbed(entry, formatCfg)

	if containsDiscordField(embed.Fields, "Website") {
		t.Fatalf("embed fields = %#v, want no Website field when WebsiteURL is empty", embed.Fields)
	}
}

func TestFormatRansomwareEmbedNoWebsitePlaceholderWhenURLEmptyEvenWithShowEmpty(t *testing.T) {
	for _, websiteURL := range []string{"", "   "} {
		entry := api.RansomwareEntry{Group: "LockBit", Victim: "Example Corp", WebsiteURL: websiteURL}
		formatCfg := &notifyfmt.FormatOptions{
			FieldOrder:      []string{"group", "victim"},
			ShowEmptyFields: true,
		}

		embed := formatRansomwareEmbed(entry, formatCfg)

		if containsDiscordField(embed.Fields, "Website") {
			t.Fatalf("WebsiteURL=%q, ShowEmptyFields=true: embed fields = %#v, want no Website field/placeholder synthesised", websiteURL, embed.Fields)
		}
	}
}

// The website fallback is inserted before the configured loop, but it is
// still budget-gated: unlike embed.URL, a field counts towards Discord's
// 6000-rune per-embed text limit. The gate is reachable because the footer
// text comes from the operator's own ransomware_source label and is not
// truncated, so a label that alone exceeds the limit must suppress the
// field rather than emit an embed Discord would reject outright. Without
// this test, dropping the gate entirely survives the whole suite.
func TestFormatRansomwareEmbedWebsiteFallbackRespectsTotalTextLimit(t *testing.T) {
	entry := api.RansomwareEntry{Group: "LockBit", WebsiteURL: "http://lockbitxyz123.onion/example-corp"}

	// A footer just under the cap still leaves room for the field.
	fits := notifyfmt.DefaultFormatOptions()
	fits.FieldLabels = map[string]string{"ransomware_source": strings.Repeat("F", 5000)}
	embed := formatRansomwareEmbed(entry, &fits)
	if !containsDiscordField(embed.Fields, "Website") {
		t.Fatalf("footer 5000 runes: embed fields = %#v, want the Website fallback present", embed.Fields)
	}
	if got := discordEmbedTextLength(embed); got > discordEmbedTotalTextLimit {
		t.Fatalf("footer 5000 runes: embed text length = %d, want <= %d", got, discordEmbedTotalTextLimit)
	}

	// A footer that alone exhausts the budget must suppress it, and the
	// embed must stay inside the limit Discord enforces.
	exhausted := notifyfmt.DefaultFormatOptions()
	exhausted.FieldLabels = map[string]string{"ransomware_source": strings.Repeat("F", discordEmbedTotalTextLimit)}
	embed = formatRansomwareEmbed(entry, &exhausted)
	if containsDiscordField(embed.Fields, "Website") {
		t.Fatalf("footer %d runes: embed fields = %#v, want the Website fallback suppressed by the total-text budget", discordEmbedTotalTextLimit, embed.Fields)
	}
}

func TestDiscordRansomwareFieldOrderHasWebsiteRecognisesURLAlias(t *testing.T) {
	if !discordRansomwareFieldOrderHasWebsite([]string{"url"}) {
		t.Fatal("discordRansomwareFieldOrderHasWebsite([\"url\"]) = false, want true (url is the website alias)")
	}
	if discordRansomwareFieldOrderHasWebsite([]string{"group", "victim"}) {
		t.Fatal("discordRansomwareFieldOrderHasWebsite([\"group\",\"victim\"]) = true, want false")
	}
	if !discordRansomwareFieldOrderHasWebsite([]string{formatfields.FieldWebsite}) {
		t.Fatal("discordRansomwareFieldOrderHasWebsite([\"website\"]) = false, want true")
	}
}
