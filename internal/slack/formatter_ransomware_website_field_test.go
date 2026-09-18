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

	// Accepted residual, pinned deliberately: the generic order positions
	// website explicitly, but the operator's own slack.field_order overrides
	// it for Slack and omits website entirely. Slack's guarantee still fires
	// -- it must, or an operator whose slack.field_order omits website would
	// see it nowhere on Slack -- and it hoists the field to the front.
	// Interpolating the position instead (splice the field into the resolved
	// Slack order after the last field that precedes website in the generic
	// order and is also present in the resolved order, front when there is
	// none) is well-defined for every input and does close the difference;
	// it is not done here because where a field appears is the operator's
	// decision, while the guarantee that it appears at all is what this
	// fallback owes. Changing the placement starts with that decision, not
	// with this test.
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
		if idx := firstBlockContaining(msg.Blocks, "Website:"); idx != 1 {
			t.Fatalf("Website block index = %d, want 1 (guarantee hoists to the front when the operator's own slack.field_order omits it, even though the generic field_order carries it)", idx)
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
