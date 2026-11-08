package slack

import (
	"strings"
	"testing"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/api"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/formatfields"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/notifyfmt"
)

// Discord is the leading system (operator decision): the Slack ransomware
// message no longer omits the website/darknet URL when it is absent from the
// operator's configured field order. These tests pin the new fallback
// ransomwareSlackBlocks inserts, mirroring internal/discord's equivalent
// (formatter_ransomware_website_field_test.go).

func TestFormatRansomwareMessageShowsWebsiteBlockWhenNotConfigured(t *testing.T) {
	tests := []struct {
		name string
		cfg  *notifyfmt.FormatOptions
	}{
		{name: "default format options", cfg: defaultFormatOptionsPtr()},
		{name: "nil config", cfg: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry := api.RansomwareEntry{
				Group:      "LockBit",
				Victim:     "Example Corp",
				WebsiteURL: "http://lockbitxyz123.onion/example-corp",
			}

			msg := formatRansomwareMessage(entry, tt.cfg)

			idx := firstBlockContaining(msg.Blocks, "Website:")
			if idx != 1 {
				t.Fatalf("Website block index = %d, want 1 (inserted before the configured loop)", idx)
			}
			button, ok := msg.Blocks[idx].Accessory.(slackButtonAccessory)
			if !ok {
				t.Fatalf("Website block accessory = %#v, want slackButtonAccessory", msg.Blocks[idx].Accessory)
			}
			if button.URL != entry.WebsiteURL {
				t.Fatalf("Website button URL = %q, want %q", button.URL, entry.WebsiteURL)
			}
		})
	}
}

func defaultFormatOptionsPtr() *notifyfmt.FormatOptions {
	opts := notifyfmt.DefaultFormatOptions()
	return &opts
}

func TestFormatRansomwareMessageWebsiteBlockNotDuplicatedWhenConfigured(t *testing.T) {
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

			msg := formatRansomwareMessage(entry, formatCfg)

			count := 0
			for _, block := range msg.Blocks {
				if containsFieldText([]slackBlock{block}, "Website:") {
					count++
				}
			}
			if count != 1 {
				t.Fatalf("website-carrying block count = %d, want exactly 1: %#v", count, msg.Blocks)
			}
			idx := firstBlockContaining(msg.Blocks, "Website:")
			if idx != 2 {
				t.Fatalf("Website block index = %d, want 2 (its configured position, not hoisted)", idx)
			}
		})
	}
}

func TestFormatRansomwareMessageNoWebsiteBlockWhenURLEmptyAndNotShown(t *testing.T) {
	entry := api.RansomwareEntry{Group: "LockBit", Victim: "Example Corp"}
	formatCfg := &notifyfmt.FormatOptions{FieldOrder: []string{"group", "victim"}}

	msg := formatRansomwareMessage(entry, formatCfg)

	if containsFieldText(msg.Blocks, "Website:") {
		t.Fatalf("blocks = %#v, want no Website block when WebsiteURL is empty", msg.Blocks)
	}
}

func TestFormatRansomwareMessageNoWebsitePlaceholderWhenURLEmptyEvenWithShowEmpty(t *testing.T) {
	for _, websiteURL := range []string{"", "   "} {
		entry := api.RansomwareEntry{Group: "LockBit", Victim: "Example Corp", WebsiteURL: websiteURL}
		formatCfg := &notifyfmt.FormatOptions{
			FieldOrder:      []string{"group", "victim"},
			ShowEmptyFields: true,
		}

		msg := formatRansomwareMessage(entry, formatCfg)

		if containsFieldText(msg.Blocks, "Website:") {
			t.Fatalf("WebsiteURL=%q, ShowEmptyFields=true: blocks = %#v, want no Website block/placeholder synthesised", websiteURL, msg.Blocks)
		}
	}
}

func TestSlackRansomwareFieldOrderHasWebsiteRecognisesURLAlias(t *testing.T) {
	if !slackRansomwareFieldOrderHasWebsite([]string{"url"}) {
		t.Fatal("slackRansomwareFieldOrderHasWebsite([\"url\"]) = false, want true (url is the website alias)")
	}
	if slackRansomwareFieldOrderHasWebsite([]string{"group", "victim"}) {
		t.Fatal("slackRansomwareFieldOrderHasWebsite([\"group\",\"victim\"]) = true, want false")
	}
	if !slackRansomwareFieldOrderHasWebsite([]string{formatfields.FieldWebsite}) {
		t.Fatal("slackRansomwareFieldOrderHasWebsite([\"website\"]) = false, want true")
	}
}

func TestFormatRansomwareMessageWebsiteFallbackRespectsFieldOrderPrecedence(t *testing.T) {
	entry := api.RansomwareEntry{
		Group:      "LockBit",
		Victim:     "Example Corp",
		WebsiteURL: "http://lockbitxyz123.onion/example-corp",
	}

	t.Run("slack-specific order carries website, generic order does not", func(t *testing.T) {
		formatCfg := &notifyfmt.FormatOptions{
			FieldOrder: []string{"group", "victim"},
			Slack: notifyfmt.SlackFormatOptions{
				FieldOrder: []string{"group", "victim", "website"},
			},
		}

		msg := formatRansomwareMessage(entry, formatCfg)

		count := 0
		for _, block := range msg.Blocks {
			if containsFieldText([]slackBlock{block}, "Website:") {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("website-carrying block count = %d, want exactly 1: %#v", count, msg.Blocks)
		}
		if idx := firstBlockContaining(msg.Blocks, "Website:"); idx != 2 {
			t.Fatalf("Website block index = %d, want 2 (slack-specific position)", idx)
		}
	})

	t.Run("generic order carries website via url alias, slack order unset", func(t *testing.T) {
		formatCfg := &notifyfmt.FormatOptions{
			FieldOrder: []string{"group", "victim", "url"},
		}

		msg := formatRansomwareMessage(entry, formatCfg)

		count := 0
		for _, block := range msg.Blocks {
			if containsFieldText([]slackBlock{block}, "Website:") {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("website-carrying block count = %d, want exactly 1: %#v", count, msg.Blocks)
		}
	})

	// A generic Website position is retained relative to fields shared with
	// Slack's effective order, even when Slack omits Website explicitly.
	t.Run("generic order carries website, slack-specific order omits it", func(t *testing.T) {
		formatCfg := &notifyfmt.FormatOptions{
			FieldOrder: []string{"group", "victim", "website"},
			Slack: notifyfmt.SlackFormatOptions{
				FieldOrder: []string{"group", "victim"},
			},
		}

		msg := formatRansomwareMessage(entry, formatCfg)

		count := 0
		for _, block := range msg.Blocks {
			if containsFieldText([]slackBlock{block}, "Website:") {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("website-carrying block count = %d, want exactly 1: %#v", count, msg.Blocks)
		}
		if idx := firstBlockContaining(msg.Blocks, "Website:"); idx != 2 {
			t.Fatalf("Website block index = %d, want 2 (after the shared group and victim fields)", idx)
		}
	})

	t.Run("website follows the last shared predecessor before a full-width field", func(t *testing.T) {
		withPostURL := entry
		withPostURL.ClaimURL = "https://example.test/claim"
		formatCfg := &notifyfmt.FormatOptions{
			FieldOrder: []string{"group", "url", "post_url"},
			Slack: notifyfmt.SlackFormatOptions{
				FieldOrder: []string{"group", "post_url"},
			},
		}
		msg := formatRansomwareMessage(withPostURL, formatCfg)
		if idx := firstBlockContaining(msg.Blocks, "Website:"); idx != 2 {
			t.Fatalf("Website block index = %d, want 2 (after group, before Ransom URL)", idx)
		}
		if idx := firstBlockContaining(msg.Blocks, "Ransom URL:"); idx != 3 {
			t.Fatalf("Ransom URL block index = %d, want 3 (after Website)", idx)
		}
		if got := formatCfg.Slack.FieldOrder; len(got) != 2 || got[0] != "group" || got[1] != "post_url" {
			t.Fatalf("slack.field_order mutated: %#v", got)
		}
		if got := formatCfg.FieldOrder; len(got) != 3 || got[0] != "group" || got[1] != "url" || got[2] != "post_url" {
			t.Fatalf("field_order mutated: %#v", got)
		}
	})

	t.Run("full-width shared predecessor anchors website after its block", func(t *testing.T) {
		withPostURL := entry
		withPostURL.ClaimURL = "https://example.test/claim"
		formatCfg := &notifyfmt.FormatOptions{
			FieldOrder: []string{"group", "post_url", "website"},
			Slack: notifyfmt.SlackFormatOptions{
				FieldOrder: []string{"group", "post_url"},
			},
		}
		msg := formatRansomwareMessage(withPostURL, formatCfg)
		if idx := firstBlockContaining(msg.Blocks, "Ransom URL:"); idx != 2 {
			t.Fatalf("Ransom URL block index = %d, want 2", idx)
		}
		if idx := firstBlockContaining(msg.Blocks, "Website:"); idx != 3 {
			t.Fatalf("Website block index = %d, want 3 (after Ransom URL)", idx)
		}
	})

	t.Run("missing predecessor uses the earlier shared field", func(t *testing.T) {
		withCountry := entry
		withCountry.Country = "DE"
		withCountry.ClaimURL = "https://example.test/claim"
		formatCfg := &notifyfmt.FormatOptions{
			FieldOrder: []string{"group", "country", "victim", "website", "post_url"},
			Slack: notifyfmt.SlackFormatOptions{
				FieldOrder: []string{"group", "country", "post_url"},
			},
		}
		msg := formatRansomwareMessage(withCountry, formatCfg)
		if idx := firstBlockContaining(msg.Blocks, "Website:"); idx != 2 {
			t.Fatalf("Website block index = %d, want 2 (after Country)", idx)
		}
		if idx := firstBlockContaining(msg.Blocks, "Ransom URL:"); idx != 3 {
			t.Fatalf("Ransom URL block index = %d, want 3", idx)
		}
	})

	t.Run("no shared predecessor keeps website at the front", func(t *testing.T) {
		formatCfg := &notifyfmt.FormatOptions{
			FieldOrder: []string{"country", "website", "group"},
			Slack: notifyfmt.SlackFormatOptions{
				FieldOrder: []string{"group"},
			},
		}
		msg := formatRansomwareMessage(entry, formatCfg)
		if idx := firstBlockContaining(msg.Blocks, "Website:"); idx != 1 {
			t.Fatalf("Website block index = %d, want 1 (no shared predecessor)", idx)
		}
	})
}

// TestFormatRansomwareMessageTrimsPaddedWebsiteURLAndKeepsButton pins a
// regression: ransomwareSlackFieldFor used to compare the raw (untrimmed)
// field value against "", so a padded WebsiteURL rendered its
// leading/trailing whitespace verbatim and slackURLButton's url.Parse failed
// on the padded string, silently dropping the accessory entirely -- on the
// one platform where the URL is meant to be clickable. Discord's
// createRansomwareField already trims before this same test; Slack now does
// too.
func TestFormatRansomwareMessageTrimsPaddedWebsiteURLAndKeepsButton(t *testing.T) {
	const paddedURL = "  http://padded.onion/x  "
	const trimmedURL = "http://padded.onion/x"
	entry := api.RansomwareEntry{Group: "LockBit", Victim: "Example Corp", WebsiteURL: paddedURL}

	msg := formatRansomwareMessage(entry, defaultFormatOptionsPtr())

	idx := firstBlockContaining(msg.Blocks, "Website:")
	if idx == -1 {
		t.Fatalf("blocks = %#v, want a Website block", msg.Blocks)
	}
	text := msg.Blocks[idx].Text.Text
	if !strings.Contains(text, trimmedURL) {
		t.Fatalf("Website block text = %q, want the trimmed URL %q", text, trimmedURL)
	}
	if strings.Contains(text, paddedURL) {
		t.Fatalf("Website block text = %q, want no leading/trailing padding", text)
	}
	button, ok := msg.Blocks[idx].Accessory.(slackButtonAccessory)
	if !ok {
		t.Fatalf("Website block accessory = %#v, want slackButtonAccessory (button must survive trimming)", msg.Blocks[idx].Accessory)
	}
	if button.URL != trimmedURL {
		t.Fatalf("Website button URL = %q, want %q", button.URL, trimmedURL)
	}
}
