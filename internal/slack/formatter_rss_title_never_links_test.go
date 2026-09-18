package slack

import (
	"strings"
	"testing"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/config"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/notifyfmt"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/rss"
)

// Titles are never links (operator decision). The button
// on the title block is the URL's only home now; when it fails Slack's own
// validation (slackURLButton), a standalone "Link" field fallback guarantees
// the URL is still visible somewhere. These tests pin appendRSSSlackTitle's
// new fallback (internal/slack/formatter.go).

func hasLinkFallbackBlock(blocks []slackBlock) (slackBlock, bool) {
	for _, b := range blocks {
		if b.Text != nil && strings.HasPrefix(b.Text.Text, "*Link:*") {
			return b, true
		}
	}
	return slackBlock{}, false
}

func countLinkFallbackBlocks(blocks []slackBlock) int {
	count := 0
	for _, b := range blocks {
		if b.Text != nil && strings.HasPrefix(b.Text.Text, "*Link:*") {
			count++
		}
	}
	return count
}

func TestSlackRSSTitleNeverLinksWhenLinkFailsValidation(t *testing.T) {
	entry := rss.Entry{
		Title: "Malformed link item",
		Link:  "not-a-url",
	}
	formatCfg := &notifyfmt.FormatOptions{
		RSS: notifyfmt.RSSFormatOptions{FieldOrder: []string{"title", "link"}},
	}

	msg := formatRSSMessage(entry, formatCfg, config.FeedTypeGeneral)

	titleBlock := msg.Blocks[1]
	if strings.Contains(titleBlock.Text.Text, "<") {
		t.Fatalf("title block still contains a link construct: %q", titleBlock.Text.Text)
	}
	if titleBlock.Accessory != nil {
		t.Fatalf("title block accessory = %#v, want nil (button correctly omitted for an invalid link)", titleBlock.Accessory)
	}

	fallback, ok := hasLinkFallbackBlock(msg.Blocks)
	if !ok {
		t.Fatalf("no Link fallback block found: %#v", msg.Blocks)
	}
	want := "*Link:*\n" + slackURLText("not-a-url", slackSectionTextLimit-len("*Link:*\n"))
	if fallback.Text.Text != want {
		t.Fatalf("Link fallback text = %q, want %q", fallback.Text.Text, want)
	}
}

// The fallback belongs below the divider, with the other fields, not
// stacked directly under the title where it would read as part of the
// heading. Nothing else in the suite pins the block order, so moving the
// append above the divider otherwise passes everything.
func TestSlackRSSFallbackFieldAppearsAfterTheDivider(t *testing.T) {
	entry := rss.Entry{
		Title: "Malformed link item",
		Link:  "not-a-url",
	}
	formatCfg := &notifyfmt.FormatOptions{
		RSS: notifyfmt.RSSFormatOptions{FieldOrder: []string{"title", "link"}},
	}

	msg := formatRSSMessage(entry, formatCfg, config.FeedTypeGeneral)

	fallbackIndex := -1
	dividerIndex := -1
	for i, b := range msg.Blocks {
		if b.Type == "divider" && dividerIndex < 0 {
			dividerIndex = i
		}
		if b.Text != nil && strings.HasPrefix(b.Text.Text, "*Link:*") {
			fallbackIndex = i
		}
	}
	if dividerIndex < 0 || fallbackIndex < 0 {
		t.Fatalf("divider index = %d, fallback index = %d, want both present: %#v", dividerIndex, fallbackIndex, msg.Blocks)
	}
	if fallbackIndex < dividerIndex {
		t.Fatalf("Link fallback at block %d, divider at block %d: want the fallback after the divider", fallbackIndex, dividerIndex)
	}
}

func TestSlackRSSTitleNeverLinksWithNonHTTPSchemeLink(t *testing.T) {
	entry := rss.Entry{
		Title: "FTP link item",
		Link:  "ftp://files.example.com/report.pdf",
	}
	formatCfg := &notifyfmt.FormatOptions{
		RSS: notifyfmt.RSSFormatOptions{FieldOrder: []string{"title", "link"}},
	}

	msg := formatRSSMessage(entry, formatCfg, config.FeedTypeGeneral)

	titleBlock := msg.Blocks[1]
	if strings.Contains(titleBlock.Text.Text, "<") {
		t.Fatalf("title block still contains a link construct: %q", titleBlock.Text.Text)
	}
	if titleBlock.Accessory != nil {
		t.Fatalf("title block accessory = %#v, want nil (button correctly omitted for a non-http(s) scheme)", titleBlock.Accessory)
	}
	if _, ok := hasLinkFallbackBlock(msg.Blocks); !ok {
		t.Fatalf("no Link fallback block found: %#v", msg.Blocks)
	}
}

func TestSlackRSSFallbackFieldOmittedWhenButtonSucceeds(t *testing.T) {
	entry := rss.Entry{
		Title: "Valid link item",
		Link:  "https://example.com/article",
	}
	formatCfg := &notifyfmt.FormatOptions{
		RSS: notifyfmt.RSSFormatOptions{FieldOrder: []string{"title", "link"}},
	}

	msg := formatRSSMessage(entry, formatCfg, config.FeedTypeGeneral)

	titleBlock := msg.Blocks[1]
	if _, ok := titleBlock.Accessory.(slackButtonAccessory); !ok {
		t.Fatalf("title block accessory = %#v, want slackButtonAccessory (valid link)", titleBlock.Accessory)
	}
	if _, ok := hasLinkFallbackBlock(msg.Blocks); ok {
		t.Fatalf("Link fallback field must not appear when the button already carries the URL: %#v", msg.Blocks)
	}
}

func TestSlackRSSTitleAndButtonPlainWhenLinkNotConfigured(t *testing.T) {
	entry := rss.Entry{
		Title:       "No link item",
		Description: "Some description",
		Link:        "https://example.com/article",
	}
	formatCfg := &notifyfmt.FormatOptions{
		RSS: notifyfmt.RSSFormatOptions{FieldOrder: []string{"title", "description"}},
	}

	msg := formatRSSMessage(entry, formatCfg, config.FeedTypeGeneral)

	titleBlock := msg.Blocks[1]
	if strings.Contains(titleBlock.Text.Text, "<") {
		t.Fatalf("title block still contains a link construct: %q", titleBlock.Text.Text)
	}
	if titleBlock.Accessory != nil {
		t.Fatalf("title block accessory = %#v, want nil (link not configured)", titleBlock.Accessory)
	}
	if _, ok := hasLinkFallbackBlock(msg.Blocks); ok {
		t.Fatalf("no Link field should appear when the operator did not configure link: %#v", msg.Blocks)
	}
}

func TestSlackRSSNoDuplicateLinkBlockWhenLinkEmptyAndFeedURLUnusable(t *testing.T) {
	t.Run("unusable FeedURL", func(t *testing.T) {
		entry := rss.Entry{
			Title:   "No link item",
			Link:    "",
			FeedURL: "not-a-feed-url",
		}
		formatCfg := &notifyfmt.FormatOptions{
			ShowEmptyFields: true,
			RSS:             notifyfmt.RSSFormatOptions{FieldOrder: []string{"title", "link"}},
		}

		msg := formatRSSMessage(entry, formatCfg, config.FeedTypeGeneral)

		if got := countLinkFallbackBlocks(msg.Blocks); got != 1 {
			t.Fatalf("Link: block count = %d, want exactly 1: %#v", got, msg.Blocks)
		}
		fallback, _ := hasLinkFallbackBlock(msg.Blocks)
		if !strings.Contains(fallback.Text.Text, "N/A") {
			t.Fatalf("Link fallback text = %q, want the pre-existing N/A placeholder", fallback.Text.Text)
		}
		titleBlock := msg.Blocks[1]
		if titleBlock.Accessory != nil {
			t.Fatalf("title block accessory = %#v, want nil (FeedURL is unusable)", titleBlock.Accessory)
		}
	})

	t.Run("valid FeedURL", func(t *testing.T) {
		entry := rss.Entry{
			Title:   "No link item",
			Link:    "",
			FeedURL: "https://feed.example/f.xml",
		}
		formatCfg := &notifyfmt.FormatOptions{
			ShowEmptyFields: true,
			RSS:             notifyfmt.RSSFormatOptions{FieldOrder: []string{"title", "link"}},
		}

		msg := formatRSSMessage(entry, formatCfg, config.FeedTypeGeneral)

		if got := countLinkFallbackBlocks(msg.Blocks); got != 1 {
			t.Fatalf("Link: block count = %d, want exactly 1: %#v", got, msg.Blocks)
		}
		titleBlock := msg.Blocks[1]
		button, ok := titleBlock.Accessory.(slackButtonAccessory)
		if !ok {
			t.Fatalf("title block accessory = %#v, want slackButtonAccessory (valid FeedURL)", titleBlock.Accessory)
		}
		if button.Text.Text != "Open Feed" || button.URL != entry.FeedURL {
			t.Fatalf("Open Feed button = %#v", button)
		}
	})
}

func TestSlackRSSFallbackFieldCoexistsWithConfiguredFeedURLField(t *testing.T) {
	entry := rss.Entry{
		Title:   "Malformed link with feed_url configured",
		Link:    "not-a-url",
		FeedURL: "https://feed.example/f.xml",
	}
	formatCfg := &notifyfmt.FormatOptions{
		RSS: notifyfmt.RSSFormatOptions{FieldOrder: []string{"title", "link", "feed_url"}},
	}

	msg := formatRSSMessage(entry, formatCfg, config.FeedTypeGeneral)

	if got := countLinkFallbackBlocks(msg.Blocks); got != 1 {
		t.Fatalf("Link: block count = %d, want exactly 1: %#v", got, msg.Blocks)
	}
	fallback, _ := hasLinkFallbackBlock(msg.Blocks)
	if !strings.Contains(fallback.Text.Text, "not-a-url") {
		t.Fatalf("Link fallback text = %q, want to carry the malformed link", fallback.Text.Text)
	}

	feedURLCount := 0
	var feedURLBlock slackBlock
	for _, b := range msg.Blocks {
		if b.Text != nil && strings.HasPrefix(b.Text.Text, "*Feed URL:*") {
			feedURLCount++
			feedURLBlock = b
		}
	}
	if feedURLCount != 1 {
		t.Fatalf("Feed URL: block count = %d, want exactly 1: %#v", feedURLCount, msg.Blocks)
	}
	if !strings.Contains(feedURLBlock.Text.Text, "feed.example/f.xml") {
		t.Fatalf("Feed URL fallback text = %q, want to carry the feed URL", feedURLBlock.Text.Text)
	}
}
