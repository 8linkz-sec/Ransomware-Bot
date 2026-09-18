package slack

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/api"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/config"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/notifyfmt"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/rss"
)

// containsFieldText checks if any block field or text object contains the expected text.
func containsFieldText(blocks []slackBlock, text string) bool {
	for _, b := range blocks {
		for _, f := range b.Fields {
			if strings.Contains(f.Text, text) {
				return true
			}
		}
		if b.Text != nil && strings.Contains(b.Text.Text, text) {
			return true
		}
	}
	return false
}

// countFieldBlocks counts the number of section blocks that contain fields or text objects.
func countFieldBlocks(blocks []slackBlock) int {
	count := 0
	for _, b := range blocks {
		if b.Type == "section" && (len(b.Fields) > 0 || b.Text != nil) {
			count++
		}
	}
	return count
}

var emptyRansomwareFieldOrder = []string{
	"group",
	"victim",
	"country",
	"activity",
	"attack_date",
	"discovered",
	"screenshot",
	"post_url",
	"website",
	"description",
}

var emptyRansomwareFieldLabels = []string{
	"Group:",
	"Victim:",
	"Country:",
	"Activity:",
	"Attack Date:",
	"Discovered:",
	"Screenshot:",
	"Ransom URL:",
	"Website:",
	"Description:",
}

func ransomwareEmptyFieldOrder() []string {
	return append([]string(nil), emptyRansomwareFieldOrder...)
}

func ransomwareEmptyFieldLabels() []string {
	return append([]string(nil), emptyRansomwareFieldLabels...)
}

func ransomwareEmptyFieldOrderPrefix(n int) []string {
	return ransomwareEmptyFieldOrder()[:n]
}

func emptyRansomwareMessageFormat(showEmpty bool, placeholder string) *notifyfmt.FormatOptions {
	return &notifyfmt.FormatOptions{
		ShowEmptyFields:  showEmpty,
		EmptyFieldText:   placeholder,
		ShowUnicodeFlags: false,
		FieldOrder:       ransomwareEmptyFieldOrder(),
	}
}

func allSlackText(blocks []slackBlock) string {
	var builder strings.Builder
	for _, b := range blocks {
		if b.Text != nil {
			builder.WriteString(b.Text.Text)
			builder.WriteByte('\n')
		}
		for _, f := range b.Fields {
			builder.WriteString(f.Text)
			builder.WriteByte('\n')
		}
		for _, e := range b.Elements {
			builder.WriteString(e.Text)
			builder.WriteByte('\n')
		}
	}
	return builder.String()
}

func TestFormatRansomwareMessage(t *testing.T) {
	entry := api.RansomwareEntry{
		Group:      "LockBit",
		Victim:     "TestCorp",
		Country:    "US",
		Activity:   "data leak",
		Discovered: time.Now(),
	}

	formatCfg := &notifyfmt.FormatOptions{
		ShowUnicodeFlags: true,
		FieldOrder:       []string{"group", "victim", "country"},
	}

	msg := formatRansomwareMessage(entry, formatCfg)

	if msg.Text == "" {
		t.Error("expected non-empty fallback text")
	}
	if len(msg.Blocks) == 0 {
		t.Error("expected blocks to be generated")
	}

	// First block should be header
	if msg.Blocks[0].Type != "header" {
		t.Errorf("expected first block to be header, got %q", msg.Blocks[0].Type)
	}
	blockText := allSlackText(msg.Blocks)
	for _, want := range []string{
		"Ransomware Alert:",
		"LockBit",
		"TestCorp",
		"*Group:*",
		"*Victim:*",
		"*Country:*",
		"United States",
	} {
		if !strings.Contains(blockText, want) {
			t.Fatalf("Slack ransomware blocks missing %q:\n%s", want, blockText)
		}
	}
	for _, want := range []string{"Ransomware Alert:", "LockBit", "TestCorp", "Country: US"} {
		if !strings.Contains(msg.Text, want) {
			t.Fatalf("Slack ransomware fallback missing %q:\n%s", want, msg.Text)
		}
	}
	groupIndex := strings.Index(blockText, "*Group:*")
	victimIndex := strings.Index(blockText, "*Victim:*")
	countryIndex := strings.Index(blockText, "*Country:*")
	if groupIndex == -1 || victimIndex == -1 || countryIndex == -1 || groupIndex >= victimIndex || victimIndex >= countryIndex {
		t.Fatalf("Slack ransomware fields not in configured order: group=%d victim=%d country=%d\n%s", groupIndex, victimIndex, countryIndex, blockText)
	}
}

func TestFormatRansomwareMessageFallbackUsesUnknownIdentity(t *testing.T) {
	msg := formatRansomwareMessage(api.RansomwareEntry{Victim: "Example Corp"}, &notifyfmt.FormatOptions{})

	if !strings.Contains(msg.Text, "Unknown group") {
		t.Fatalf("fallback text = %q, want Unknown group placeholder", msg.Text)
	}
	if strings.Contains(msg.Text, " ->  ") || strings.Contains(msg.Text, ":  ->") {
		t.Fatalf("fallback text has blank identity: %q", msg.Text)
	}
}

func TestFormatRansomwareMessageFallbackIncludesAlertDetails(t *testing.T) {
	entry := api.RansomwareEntry{
		Group:       "LockBit",
		Victim:      "Example Corp",
		Country:     "DE",
		Activity:    "data theft",
		AttackDate:  "2026-01-02 03:04:05",
		Discovered:  time.Date(2026, 1, 3, 4, 5, 6, 0, time.UTC),
		WebsiteURL:  "https://victim.example.com",
		ClaimURL:    "https://example.onion/post",
		Screenshot:  "https://example.com/screenshot.png",
		Description: "Leaked <b>files</b>",
	}

	msg := formatRansomwareMessage(entry, &notifyfmt.FormatOptions{})

	for _, want := range []string{
		"Ransomware Alert:",
		"Group: LockBit",
		"Victim: Example Corp",
		"Country: DE",
		"Activity: data theft",
		"Attack Date: 2026-01-02",
		"Discovered: 2026-01-03 04:05:06",
		"Website: https://victim.example.com",
		"Ransom URL: hxxps://example.onion/post",
		"Screenshot: https://example.com/screenshot.png",
		"Description: Leaked files",
		"Source: Ransomware.live API",
	} {
		if !strings.Contains(msg.Text, want) {
			t.Fatalf("fallback text missing %q:\n%s", want, msg.Text)
		}
	}
}

func TestFormatRansomwareMessageUsesConfiguredLabels(t *testing.T) {
	entry := api.RansomwareEntry{
		Group:      "LockBit",
		Victim:     "Example Corp",
		WebsiteURL: "https://victim.example.com",
		Discovered: time.Date(2026, 1, 3, 4, 5, 6, 0, time.UTC),
	}
	formatCfg := &notifyfmt.FormatOptions{
		FieldOrder: []string{"group", "victim", "website"},
		Slack: notifyfmt.SlackFormatOptions{
			FieldLabels: map[string]string{
				"group":             "Gruppe",
				"victim":            "Ziel",
				"website":           "Webseite",
				"source":            "Quelle",
				"discovered":        "Gefunden",
				"ransomware_alert":  "Ransomware Warnung",
				"ransomware_source": "Ransomware Live",
				"open_website":      "Webseite oeffnen",
			},
		},
	}

	msg := formatRansomwareMessage(entry, formatCfg)
	text := allSlackText(msg.Blocks)

	for _, want := range []string{
		"Ransomware Warnung:",
		"*Gruppe:*",
		"*Ziel:*",
		"*Webseite:*",
		"Gefunden:",
		"Quelle: Ransomware Live",
	} {
		if !strings.Contains(text, want) && !strings.Contains(msg.Text, want) {
			t.Fatalf("Slack ransomware output missing configured label %q\nfallback=%s\nblocks=%s", want, msg.Text, text)
		}
	}

	foundButton := false
	for _, block := range msg.Blocks {
		button, ok := block.Accessory.(slackButtonAccessory)
		if ok && button.Text.Text == "Webseite oeffnen" && button.AccessibilityLabel == "Webseite oeffnen" {
			foundButton = true
			break
		}
	}
	if !foundButton {
		t.Fatalf("Slack ransomware blocks missing configured website button: %#v", msg.Blocks)
	}
}

func TestFormatRansomwareMessageHeaderIncludesRansomwareIdentity(t *testing.T) {
	entry := api.RansomwareEntry{
		Group:  "LockBit",
		Victim: "Example Corp",
	}
	formatCfg := &notifyfmt.FormatOptions{
		Slack: notifyfmt.SlackFormatOptions{TitleText: "Custom Alert Title"},
	}

	msg := formatRansomwareMessage(entry, formatCfg)

	if len(msg.Blocks) == 0 || msg.Blocks[0].Text == nil {
		t.Fatalf("missing Slack header block: %#v", msg.Blocks)
	}
	header := msg.Blocks[0].Text.Text
	for _, want := range []string{"Ransomware Alert:", "LockBit", "Example Corp"} {
		if !strings.Contains(header, want) {
			t.Fatalf("Slack header = %q, want %q", header, want)
		}
	}
	if strings.Contains(header, "Custom Alert Title") {
		t.Fatalf("Slack header = %q, should prefer alert identity over generic configured title", header)
	}
}

func TestFormatRansomwareMessageHeaderUsesConfiguredTitleWhenIdentityMissing(t *testing.T) {
	formatCfg := &notifyfmt.FormatOptions{
		Slack: notifyfmt.SlackFormatOptions{TitleText: "Custom Alert Title"},
	}

	msg := formatRansomwareMessage(api.RansomwareEntry{}, formatCfg)

	if len(msg.Blocks) == 0 || msg.Blocks[0].Text == nil {
		t.Fatalf("missing Slack header block: %#v", msg.Blocks)
	}
	if got := msg.Blocks[0].Text.Text; got != "Custom Alert Title" {
		t.Fatalf("Slack header = %q, want configured generic title", got)
	}
}

func TestFormatRansomwareMessageEscapesSlackMrkdwnExternalContent(t *testing.T) {
	entry := api.RansomwareEntry{
		Group:       "LockBit <!channel>",
		Victim:      "<@U123> & Sons",
		WebsiteURL:  "https://victim.example/?a=1&b=<tag>",
		Description: "Leaked files & credentials",
	}
	formatCfg := &notifyfmt.FormatOptions{
		FieldOrder: []string{"group", "victim", "website", "description"},
	}

	msg := formatRansomwareMessage(entry, formatCfg)
	text := allSlackText(msg.Blocks)

	for _, raw := range []string{"<!channel>", "<@U123>", "b=<tag>", "files & credentials"} {
		if strings.Contains(text, raw) {
			t.Fatalf("Slack mrkdwn contains unescaped external token %q in:\n%s", raw, text)
		}
	}
	for _, escaped := range []string{"&lt;!channel&gt;", "&lt;@U123&gt; &amp; Sons", "b=&lt;tag&gt;", "files &amp; credentials"} {
		if !strings.Contains(text, escaped) {
			t.Fatalf("Slack mrkdwn missing escaped token %q in:\n%s", escaped, text)
		}
	}
}

func TestFormatRansomwareMessageUsesConfiguredDescriptionLimit(t *testing.T) {
	entry := api.RansomwareEntry{
		Description: strings.Repeat("Detailed ransomware incident context. ", 20),
		ClaimURL:    "https://example.onion/post",
	}
	formatCfg := notifyfmt.DefaultFormatOptions()
	formatCfg.Slack.DescriptionMaxChars = 120
	formatCfg.Slack.FieldOrder = []string{"description", "post_url"}

	msg := formatRansomwareMessage(entry, &formatCfg)
	descriptionIndex := firstBlockContaining(msg.Blocks, "Description:")
	if descriptionIndex == -1 {
		t.Fatalf("expected description block in %#v", msg.Blocks)
	}
	blockText := msg.Blocks[descriptionIndex].Text.Text
	if len([]rune(blockText)) > 120 {
		t.Fatalf("description block length = %d, want <= 120", len([]rune(blockText)))
	}
}

func TestFormatRansomwareMessageDescriptionUsesSentenceSummary(t *testing.T) {
	entry := api.RansomwareEntry{
		Description: "First sentence. Second sentence. Third sentence should not be cut in the middle of a word.",
	}
	formatCfg := notifyfmt.DefaultFormatOptions()
	formatCfg.Slack.DescriptionMaxChars = 72
	formatCfg.Slack.FieldOrder = []string{"description"}

	msg := formatRansomwareMessage(entry, &formatCfg)

	descriptionIndex := firstBlockContaining(msg.Blocks, "Description:")
	if descriptionIndex == -1 {
		t.Fatalf("expected description block in %#v", msg.Blocks)
	}
	blockText := msg.Blocks[descriptionIndex].Text.Text
	if !strings.Contains(blockText, "First sentence. Second sentence... [truncated]") {
		t.Fatalf("Slack description = %q, want sentence summary marker", blockText)
	}
	if strings.Contains(blockText, "Third sentence") {
		t.Fatalf("Slack description kept next sentence after truncation: %q", blockText)
	}
	if len([]rune(blockText)) > 72 {
		t.Fatalf("Slack description length = %d, want <= 72", len([]rune(blockText)))
	}
}

func TestFormatRSSMessage(t *testing.T) {
	entry := rss.Entry{
		Title:       "Test Article",
		Link:        "https://example.com/article",
		Description: "Test description",
		Published:   time.Now(),
		FeedTitle:   "Test Feed",
		Categories:  []string{"security", "threat"},
	}

	formatCfg := &notifyfmt.FormatOptions{}

	msg := formatRSSMessage(entry, formatCfg, config.FeedTypeGeneral)

	if msg.Text == "" {
		t.Error("expected non-empty fallback text")
	}
	if len(msg.Blocks) == 0 {
		t.Error("expected blocks to be generated")
	}
	text := allSlackText(msg.Blocks)
	for _, want := range []string{
		"Test Article",
		"Test description",
		"Test Feed",
		"*Categories:*",
		"security, threat",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("Slack RSS blocks missing %q:\n%s", want, text)
		}
	}
	for _, want := range []string{
		"RSS Feed Update: Test Article",
		"Link: https://example.com/article",
		"Description: Test description",
		"Source: Test Feed",
		"Categories: security, threat",
	} {
		if !strings.Contains(msg.Text, want) {
			t.Fatalf("Slack RSS fallback missing %q:\n%s", want, msg.Text)
		}
	}
	foundArticleButton := false
	for _, block := range msg.Blocks {
		if button, ok := block.Accessory.(slackButtonAccessory); ok && button.URL == entry.Link {
			foundArticleButton = true
			break
		}
	}
	if !foundArticleButton {
		t.Fatalf("Slack RSS blocks missing article button for %q: %#v", entry.Link, msg.Blocks)
	}
}

func TestFormatRSSMessageUsesConfiguredLabels(t *testing.T) {
	entry := rss.Entry{
		Title:      "Vendor advisory",
		Link:       "https://example.com/advisory",
		Published:  time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC),
		FeedTitle:  "Vendor Feed",
		Categories: []string{"security", "ransomware"},
	}
	formatCfg := notifyfmt.DefaultFormatOptions()
	formatCfg.RSS.FieldOrder = []string{"title", "link", "categories", "published", "feed_title"}
	formatCfg.Slack.FieldLabels = map[string]string{
		"rss_update":   "Feed Meldung",
		"categories":   "Tags",
		"published":    "Veroeffentlicht",
		"source":       "Quelle",
		"open_article": "Artikel oeffnen",
	}

	msg := formatRSSMessage(entry, &formatCfg, config.FeedTypeGeneral)
	text := allSlackText(msg.Blocks)

	for _, want := range []string{
		"Feed Meldung: Vendor advisory",
		"Tags: security, ransomware",
		"Veroeffentlicht: 2026-02-03",
		"Quelle: Vendor Feed",
		"*Tags:*",
		"Veroeffentlicht:",
		"Quelle:",
	} {
		if !strings.Contains(text, want) && !strings.Contains(msg.Text, want) {
			t.Fatalf("Slack RSS output missing configured label %q\nfallback=%s\nblocks=%s", want, msg.Text, text)
		}
	}

	titleBlock := msg.Blocks[1]
	button, ok := titleBlock.Accessory.(slackButtonAccessory)
	if !ok || button.Text.Text != "Artikel oeffnen" || button.AccessibilityLabel != "Artikel oeffnen" {
		t.Fatalf("Slack RSS article button = %#v, want configured label", titleBlock.Accessory)
	}
}

func TestFormatRSSMessageUsesArticleHeaderWithFeedTypeFallback(t *testing.T) {
	entry := rss.Entry{Title: "Policy advisory", Published: time.Now()}
	formatCfg := &notifyfmt.FormatOptions{
		Slack: notifyfmt.SlackFormatOptions{RSSText: "General RSS"},
	}

	general := formatRSSMessage(entry, formatCfg, config.FeedTypeGeneral)
	government := formatRSSMessage(rss.Entry{Published: time.Now()}, formatCfg, config.FeedTypeGovernment)
	ransomware := formatRSSMessage(rss.Entry{Published: time.Now()}, formatCfg, config.FeedTypeRansomware)

	if general.Blocks[0].Text.Text != "Policy advisory" {
		t.Fatalf("general RSS header = %q", general.Blocks[0].Text.Text)
	}
	if government.Blocks[0].Text.Text != "Government RSS Update" {
		t.Fatalf("government RSS header = %q", government.Blocks[0].Text.Text)
	}
	if ransomware.Blocks[0].Text.Text != "Ransomware RSS Update" {
		t.Fatalf("ransomware RSS header = %q", ransomware.Blocks[0].Text.Text)
	}
}

func TestFormatRSSMessageUsesDisplayTitleForUntitledItems(t *testing.T) {
	entry := rss.Entry{
		FeedTitle: "Vendor Feed",
		Link:      "https://example.com/advisory",
	}

	msg := formatRSSMessage(entry, &notifyfmt.FormatOptions{
		RSS: notifyfmt.RSSFormatOptions{ShowAuthor: true},
	}, config.FeedTypeGeneral)

	if msg.Blocks[0].Text.Text != "Vendor Feed" {
		t.Fatalf("RSS header = %q, want feed title fallback", msg.Blocks[0].Text.Text)
	}
	if !strings.Contains(msg.Text, "RSS Feed Update: Vendor Feed") {
		t.Fatalf("RSS fallback text = %q, want display title", msg.Text)
	}
	if !strings.Contains(allSlackText(msg.Blocks), "Vendor Feed") {
		t.Fatalf("RSS blocks missing display title: %#v", msg.Blocks)
	}
}

func TestFormatRSSMessageNeverLinksTitleButtonCarriesTheURL(t *testing.T) {
	entry := rss.Entry{
		Title:     "Vendor advisory",
		Link:      "https://example.com/advisory?id=1&source=<feed>",
		Published: time.Now(),
	}

	msg := formatRSSMessage(entry, &notifyfmt.FormatOptions{
		RSS: notifyfmt.RSSFormatOptions{ShowAuthor: true},
	}, config.FeedTypeGeneral)
	text := allSlackText(msg.Blocks)

	// Titles are never links: no link construct at all in
	// the title text, on any target.
	if strings.Contains(text, "<https://example.com/advisory") {
		t.Fatalf("Slack RSS title still contains a link construct:\n%s", text)
	}
	if strings.Contains(text, "Link:") {
		t.Fatalf("Slack RSS message still renders a standalone Link section:\n%s", text)
	}
	titleBlock := msg.Blocks[1]
	button, ok := titleBlock.Accessory.(slackButtonAccessory)
	if !ok {
		t.Fatalf("Slack RSS title block accessory = %#v, want slackButtonAccessory", titleBlock.Accessory)
	}
	if button.Text.Text != "Open Article" || button.URL != entry.Link || button.ActionID != "open_article" {
		t.Fatalf("Slack RSS article button = %#v", button)
	}
	if button.AccessibilityLabel != "Open Article" {
		t.Fatalf("Slack RSS article button accessibility label = %q", button.AccessibilityLabel)
	}
}

func TestFormatRSSMessageUsesFeedURLAsFallbackAction(t *testing.T) {
	entry := rss.Entry{
		Title:   "Feed-only advisory",
		FeedURL: "https://example.com/feed.xml",
	}

	msg := formatRSSMessage(entry, &notifyfmt.FormatOptions{
		RSS: notifyfmt.RSSFormatOptions{ShowAuthor: true},
	}, config.FeedTypeGeneral)

	titleBlock := msg.Blocks[1]
	button, ok := titleBlock.Accessory.(slackButtonAccessory)
	if !ok {
		t.Fatalf("Slack RSS title block accessory = %#v, want slackButtonAccessory", titleBlock.Accessory)
	}
	if button.Text.Text != "Open Feed" || button.URL != entry.FeedURL || button.ActionID != "open_feed" {
		t.Fatalf("Slack RSS fallback button = %#v", button)
	}
	if button.AccessibilityLabel != "Open Feed" {
		t.Fatalf("Slack RSS fallback button accessibility label = %q", button.AccessibilityLabel)
	}
	// Titles are never links: pin that this scenario's
	// title also stays unlinked, explicitly rather than incidentally.
	if strings.Contains(titleBlock.Text.Text, "<https://") {
		t.Fatal("Slack RSS title should never be a link, even in the FeedURL-fallback case")
	}
}

func TestFormatRSSMessageFallbackIncludesArticleDetails(t *testing.T) {
	entry := rss.Entry{
		Title:       "Vendor advisory",
		Link:        "https://example.com/advisory",
		Description: "Patch immediately",
		Author:      "Security Team",
		Categories:  []string{"security", "ransomware"},
		Published:   time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC),
		FeedTitle:   "Vendor Feed",
	}

	msg := formatRSSMessage(entry, &notifyfmt.FormatOptions{
		RSS: notifyfmt.RSSFormatOptions{ShowAuthor: true},
	}, config.FeedTypeGeneral)

	for _, want := range []string{
		"RSS Feed Update: Vendor advisory",
		"Description: Patch immediately",
		"Link: https://example.com/advisory",
		"Author: Security Team",
		"Categories: security, ransomware",
		"Published: 2026-02-03 04:05:06",
		"Source: Vendor Feed",
	} {
		if !strings.Contains(msg.Text, want) {
			t.Fatalf("RSS fallback text missing %q:\n%s", want, msg.Text)
		}
	}
}

func TestFormatRSSMessageEscapesSlackMrkdwnExternalContent(t *testing.T) {
	entry := rss.Entry{
		Title:       "Breaking <!channel> & <https://evil.example|open>",
		Link:        "https://example.com/article?a=1&b=<tag>",
		Description: "Summary & details",
		Author:      "<@U123>",
		Published:   time.Now(),
		FeedTitle:   "Feed <#C123>",
		Categories:  []string{"security & threat", "<!subteam^S123>"},
	}

	msg := formatRSSMessage(entry, &notifyfmt.FormatOptions{
		RSS: notifyfmt.RSSFormatOptions{ShowAuthor: true},
	}, config.FeedTypeGeneral)
	text := allSlackText(msg.Blocks)

	for _, raw := range []string{"<!channel>", "<https://evil.example|open>", "Summary & details", "<@U123>", "<#C123>", "<!subteam^S123>"} {
		if strings.Contains(text, raw) {
			t.Fatalf("Slack mrkdwn contains unescaped external token %q in:\n%s", raw, text)
		}
	}
	for _, escaped := range []string{"&lt;!channel&gt;", "&amp;", "&lt;https://evil.example|open&gt;", "Summary &amp; details", "&lt;@U123&gt;", "&lt;#C123&gt;", "&lt;!subteam^S123&gt;"} {
		if !strings.Contains(text, escaped) {
			t.Fatalf("Slack mrkdwn missing escaped token %q in:\n%s", escaped, text)
		}
	}

	// Titles are never links: the link now lives only in
	// the button's url JSON field, which allSlackText does not walk and
	// which is correctly NOT mrkdwn-escaped -- a URL field is a navigation
	// target, not display text. This is the only remaining coverage of what
	// happens to a link containing mrkdwn metacharacters.
	titleBlock := msg.Blocks[1]
	button, ok := titleBlock.Accessory.(slackButtonAccessory)
	if !ok {
		t.Fatalf("Slack RSS title block accessory = %#v, want slackButtonAccessory", titleBlock.Accessory)
	}
	if button.URL != entry.Link {
		t.Fatalf("Slack RSS button URL = %q, want raw unescaped link %q", button.URL, entry.Link)
	}
}

func TestFormatRSSMessageOmitsRSSAuthorByDefault(t *testing.T) {
	entry := rss.Entry{
		Title:  "Privacy preserving RSS item",
		Author: "Security Reporter",
	}
	formatCfg := &notifyfmt.FormatOptions{
		RSS: notifyfmt.RSSFormatOptions{FieldOrder: []string{"title", "author"}},
	}

	msg := formatRSSMessage(entry, formatCfg, config.FeedTypeGeneral)
	text := msg.Text + "\n" + allSlackText(msg.Blocks)

	if strings.Contains(text, "Security Reporter") || strings.Contains(text, "Author:") {
		t.Fatalf("Slack RSS author rendered without rss.show_author:\n%s", text)
	}
}

func TestFormatRSSMessageDoesNotStripNormalizedDescriptionAgain(t *testing.T) {
	entry := rss.Entry{
		Title:       "Parser-normalized RSS",
		Description: "Already normalized <indicator> & text",
	}

	msg := formatRSSMessage(entry, &notifyfmt.FormatOptions{}, config.FeedTypeGeneral)
	text := allSlackText(msg.Blocks)

	if !strings.Contains(text, "Already normalized &lt;indicator&gt; &amp; text") {
		t.Fatalf("Slack RSS description was stripped or not escaped as normalized text:\n%s", text)
	}
}

func TestFormatRansomwareFieldAliases(t *testing.T) {
	entry := api.RansomwareEntry{
		ClaimURL:   "https://example.onion/post",
		WebsiteURL: "https://victim.example.com",
	}

	formatCfg := &notifyfmt.FormatOptions{
		FieldOrder: []string{"post_url", "claim_url", "website", "url"},
	}

	msg := formatRansomwareMessage(entry, formatCfg)

	// Should have blocks generated for both aliases
	if len(msg.Blocks) < 2 {
		t.Errorf("expected blocks for aliased fields, got %d blocks", len(msg.Blocks))
	}
}

func TestFormatRansomwareMessageDocumentedIDAndPublishedFields(t *testing.T) {
	entry := api.RansomwareEntry{
		ID:        "victim-123",
		Published: time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC),
	}
	formatCfg := &notifyfmt.FormatOptions{
		Slack: notifyfmt.SlackFormatOptions{
			FieldOrder: []string{"id", "published"},
		},
	}

	msg := formatRansomwareMessage(entry, formatCfg)

	if !containsFieldText(msg.Blocks, "victim-123") {
		t.Fatal("Slack message does not include documented id field")
	}
	if !containsFieldText(msg.Blocks, "2026-02-03 04:05:06") {
		t.Fatal("Slack message does not include documented published field")
	}
}

func TestFormatRansomwareMessageMixedCaseFieldOrder(t *testing.T) {
	entry := api.RansomwareEntry{
		Group:       "LockBit",
		Victim:      "Example Corp",
		ClaimURL:    "https://example.onion/post",
		WebsiteURL:  "https://victim.example.com",
		Description: "Incident description",
	}
	formatCfg := &notifyfmt.FormatOptions{
		Slack: notifyfmt.SlackFormatOptions{
			FieldOrder: []string{" Group ", "POST_URL", " Website ", "Description"},
		},
	}

	msg := formatRansomwareMessage(entry, formatCfg)
	text := allSlackText(msg.Blocks)

	for _, want := range []string{"LockBit", "Ransom URL", "Website", "Description"} {
		if !strings.Contains(text, want) {
			t.Fatalf("mixed-case Slack field_order output missing %q:\n%s", want, text)
		}
	}
}

// TestFormatRansomwareMessage_ShowEmptyFields verifies that when ShowEmptyFields is true,
// empty entry fields are rendered with the configured placeholder text in Slack blocks.
func TestFormatRansomwareMessage_ShowEmptyFields(t *testing.T) {
	entry := api.RansomwareEntry{} // all fields empty

	formatCfg := emptyRansomwareMessageFormat(true, "N/A")

	msg := formatRansomwareMessage(entry, formatCfg)

	if msg.Text == "" {
		t.Error("expected non-empty fallback text")
	}

	// Verify that placeholder text appears in the blocks
	if !containsFieldText(msg.Blocks, "N/A") {
		t.Error("expected placeholder text 'N/A' to appear in blocks when ShowEmptyFields=true")
	}

	// All fields from FieldOrder should be represented.
	// Check that key field labels are present in the output.
	for _, label := range ransomwareEmptyFieldLabels() {
		if !containsFieldText(msg.Blocks, label) {
			t.Errorf("expected label %q to be present in blocks with ShowEmptyFields=true", label)
		}
	}
}

// TestFormatRansomwareMessage_HideEmptyFields verifies that when ShowEmptyFields is false,
// empty fields are omitted, resulting in fewer blocks.
func TestFormatRansomwareMessage_HideEmptyFields(t *testing.T) {
	entry := api.RansomwareEntry{} // all fields empty

	formatCfg := emptyRansomwareMessageFormat(false, "N/A")

	msgHidden := formatRansomwareMessage(entry, formatCfg)

	// Now generate with ShowEmptyFields=true for comparison
	formatCfgShow := emptyRansomwareMessageFormat(true, "N/A")
	msgShown := formatRansomwareMessage(entry, formatCfgShow)

	hiddenSections := countFieldBlocks(msgHidden.Blocks)
	shownSections := countFieldBlocks(msgShown.Blocks)

	if hiddenSections >= shownSections {
		t.Errorf("HideEmptyFields should produce fewer section blocks (%d) than ShowEmptyFields (%d)", hiddenSections, shownSections)
	}

	// Placeholder text should not appear when hiding empty fields
	if containsFieldText(msgHidden.Blocks, "N/A") {
		t.Error("placeholder text 'N/A' should not appear when ShowEmptyFields=false")
	}
}

// TestFormatRansomwareMessage_CustomPlaceholder verifies that a custom EmptyFieldText
// is used in Slack messages when ShowEmptyFields is enabled.
func TestFormatRansomwareMessage_CustomPlaceholder(t *testing.T) {
	entry := api.RansomwareEntry{} // all fields empty

	formatCfg := emptyRansomwareMessageFormat(true, "---")
	formatCfg.FieldOrder = ransomwareEmptyFieldOrderPrefix(3)

	msg := formatRansomwareMessage(entry, formatCfg)

	if !containsFieldText(msg.Blocks, "---") {
		t.Error("expected custom placeholder '---' to appear in blocks")
	}

	// Default "N/A" should not appear when custom placeholder is set
	// (check field values, not the context footer which may have "Unknown")
	for _, b := range msg.Blocks {
		for _, f := range b.Fields {
			if strings.Contains(f.Text, "N/A") {
				t.Errorf("found default 'N/A' in field text %q; expected custom placeholder '---'", f.Text)
			}
		}
	}
}

// TestFormatRansomwareMessageTrimsWhitespaceOnlyFieldValues pins that the
// value trim in ransomwareSlackFieldFor is not URL-specific: any ransomware
// field whose value is nothing but whitespace counts as empty, exactly as it
// has always done on Discord (createRansomwareField trims sharedField.Value
// before its own empty test). Without the trim a whitespace-only Group used
// to render as a visible blank row on Slack while Discord suppressed it or
// showed the configured placeholder.
func TestFormatRansomwareMessageTrimsWhitespaceOnlyFieldValues(t *testing.T) {
	entry := api.RansomwareEntry{Group: "   ", Victim: "Example Corp"}
	fieldOrder := []string{"group", "victim"}

	t.Run("suppressed when empty fields are hidden", func(t *testing.T) {
		msg := formatRansomwareMessage(entry, &notifyfmt.FormatOptions{FieldOrder: fieldOrder})

		if containsFieldText(msg.Blocks, "Group:") {
			t.Fatalf("blocks = %#v, want no Group row for a whitespace-only value", msg.Blocks)
		}
	})

	t.Run("placeholder when empty fields are shown", func(t *testing.T) {
		msg := formatRansomwareMessage(entry, &notifyfmt.FormatOptions{
			FieldOrder:      fieldOrder,
			ShowEmptyFields: true,
			EmptyFieldText:  "---",
		})

		idx := firstBlockContaining(msg.Blocks, "Group:")
		if idx == -1 {
			t.Fatalf("blocks = %#v, want a Group row carrying the placeholder", msg.Blocks)
		}
		if !containsFieldText(msg.Blocks, "---") {
			t.Fatalf("blocks = %#v, want the configured placeholder for a whitespace-only value", msg.Blocks)
		}
	})

	t.Run("padded non-URL value renders trimmed", func(t *testing.T) {
		padded := api.RansomwareEntry{Group: "  LockBit  ", Victim: "Example Corp"}

		msg := formatRansomwareMessage(padded, &notifyfmt.FormatOptions{FieldOrder: fieldOrder})

		text := allSlackText(msg.Blocks)
		if !strings.Contains(text, "LockBit") || strings.Contains(text, "  LockBit  ") {
			t.Fatalf("blocks text = %q, want the Group value rendered without its padding", text)
		}
	})

}

func TestFormatRansomwareMessageNormalizesCountryAndAttackDateLikeDiscord(t *testing.T) {
	entry := api.RansomwareEntry{
		Country:    "US",
		AttackDate: "2026-02-03 04:05:06.123456",
		Discovered: time.Date(2026, 2, 4, 5, 6, 7, 0, time.UTC),
	}
	formatCfg := &notifyfmt.FormatOptions{
		ShowUnicodeFlags: false,
		FieldOrder:       []string{"country", "attack_date"},
	}

	msg := formatRansomwareMessage(entry, formatCfg)
	text := allSlackText(msg.Blocks)

	if !strings.Contains(text, "United States") {
		t.Fatalf("Slack country field was not normalized to country display name:\n%s", text)
	}
	if strings.Contains(text, "Country:*\n\u2068US\u2069") {
		t.Fatalf("Slack country field still renders raw ISO code:\n%s", text)
	}
	if !strings.Contains(text, "<!date^") ||
		!strings.Contains(text, "2026-02-03 04:05:06 UTC") ||
		strings.Contains(text, ".123456") {
		t.Fatalf("Slack attack date was not normalized:\n%s", text)
	}
}

func TestFormatRansomwareMessageUsesDisplayLocaleForCountry(t *testing.T) {
	entry := api.RansomwareEntry{
		Country: "DE",
	}
	formatCfg := notifyfmt.DefaultFormatOptions()
	formatCfg.DisplayLocale = "de"
	formatCfg.ShowUnicodeFlags = false
	formatCfg.Slack.FieldOrder = []string{"country"}

	msg := formatRansomwareMessage(entry, &formatCfg)
	text := allSlackText(msg.Blocks)

	if !strings.Contains(text, "Deutschland") {
		t.Fatalf("Slack country field not localized:\n%s", text)
	}
	if strings.Contains(text, "Germany") {
		t.Fatalf("Slack country field used English fallback:\n%s", text)
	}
}

func TestFormatRansomwareMessageUsesDisplayTimestampFormat(t *testing.T) {
	entry := api.RansomwareEntry{
		Discovered: time.Date(2026, 1, 2, 2, 4, 5, 0, time.UTC),
	}
	formatCfg := notifyfmt.DefaultFormatOptions()
	formatCfg.TimestampFormat = "02.01.2006 15:04 MST"
	formatCfg.DisplayTimezone = "Europe/Berlin"
	formatCfg.Slack.FieldOrder = []string{"discovered"}

	msg := formatRansomwareMessage(entry, &formatCfg)
	text := allSlackText(msg.Blocks)

	if !strings.Contains(text, "02.01.2026 03:04 CET") {
		t.Fatalf("Slack timestamp field not localized:\n%s", text)
	}
}

// TestFormatRSSMessage_EmptyDescription verifies that when the RSS entry has no
// description, no description block is added to the Slack message.
func TestFormatRSSMessage_EmptyDescription(t *testing.T) {
	entry := rss.Entry{
		Title:      "Test Article",
		Link:       "https://example.com/article",
		Published:  time.Now(),
		FeedTitle:  "Test Feed",
		Categories: []string{"security"},
	}
	// Description is intentionally left empty

	formatCfg := &notifyfmt.FormatOptions{}

	msg := formatRSSMessage(entry, formatCfg, config.FeedTypeGeneral)

	if msg.Text == "" {
		t.Error("expected non-empty fallback text")
	}

	// Verify no block contains a description section
	// The description block would be a section with a Text field containing the description.
	// With empty description, only the title section, link section, categories, and context should exist.
	for _, b := range msg.Blocks {
		if b.Type == "section" && b.Text != nil {
			// The title block contains the article title in bold
			if strings.Contains(b.Text.Text, "Test Article") {
				continue
			}
			// Any other section text block with non-empty content that is not metadata
			// would be unexpected (likely a description block)
			if !strings.Contains(b.Text.Text, "Published:") {
				t.Errorf("unexpected text section found (possible description block): %q", b.Text.Text)
			}
		}
	}
}

// TestFormatRSSMessage_NoCategories verifies that when the RSS entry has no
// categories, no categories block is added to the Slack message.
func TestFormatRSSMessage_NoCategories(t *testing.T) {
	entry := rss.Entry{
		Title:       "Test Article",
		Link:        "https://example.com/article",
		Description: "Some description",
		Published:   time.Now(),
		FeedTitle:   "Test Feed",
		Categories:  []string{}, // empty categories
	}

	formatCfg := &notifyfmt.FormatOptions{}

	msg := formatRSSMessage(entry, formatCfg, config.FeedTypeGeneral)

	if msg.Text == "" {
		t.Error("expected non-empty fallback text")
	}

	// Verify no block contains "Categories:" text
	if containsFieldText(msg.Blocks, "Categories:") {
		t.Error("expected no categories block when entry has empty categories")
	}
}

func TestFormatRSSMessageShowsEmptyMetadataPlaceholders(t *testing.T) {
	entry := rss.Entry{
		Title:     "Sparse RSS item",
		Published: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	formatCfg := &notifyfmt.FormatOptions{
		ShowEmptyFields: true,
		EmptyFieldText:  "No data",
		RSS:             notifyfmt.RSSFormatOptions{ShowAuthor: true},
	}

	msg := formatRSSMessage(entry, formatCfg, config.FeedTypeGeneral)
	text := allSlackText(msg.Blocks)

	for _, label := range []string{"Description:", "Link:", "Author:", "Categories:"} {
		if !strings.Contains(text, label) {
			t.Fatalf("Slack RSS placeholder output missing %q:\n%s", label, text)
		}
	}
	if count := strings.Count(text, "No data"); count < 4 {
		t.Fatalf("Slack RSS placeholder count = %d, want at least 4:\n%s", count, text)
	}
}

func TestFormatRSSMessageRespectsRSSFieldOrder(t *testing.T) {
	entry := rss.Entry{
		Title:       "Configurable RSS item",
		Link:        "https://example.com/article",
		Description: "Description should be omitted",
		Published:   time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		Author:      "Reporter",
		Categories:  []string{"security"},
		FeedTitle:   "Vendor Feed",
		FeedURL:     "https://example.com/feed.xml",
	}
	formatCfg := &notifyfmt.FormatOptions{
		RSS: notifyfmt.RSSFormatOptions{
			FieldOrder: []string{"title", "categories", "feed_url", "published"},
		},
	}

	msg := formatRSSMessage(entry, formatCfg, config.FeedTypeGeneral)
	text := allSlackText(msg.Blocks)

	for _, omitted := range []string{"Description should be omitted", "Author:", "Source:"} {
		if strings.Contains(text, omitted) {
			t.Fatalf("Slack RSS output contains omitted field %q:\n%s", omitted, text)
		}
	}
	if len(msg.Blocks) < 2 || msg.Blocks[1].Accessory != nil {
		t.Fatalf("Slack RSS title should not include article button when link field is omitted: %#v", msg.Blocks)
	}

	categoriesIndex := firstBlockContaining(msg.Blocks, "Categories:")
	feedURLIndex := firstBlockContaining(msg.Blocks, "Feed URL:")
	publishedIndex := firstBlockContaining(msg.Blocks, "Published:")
	if categoriesIndex == -1 || feedURLIndex == -1 || publishedIndex == -1 {
		t.Fatalf("expected categories, feed URL, and published blocks in:\n%s", text)
	}
	if categoriesIndex >= feedURLIndex || feedURLIndex >= publishedIndex {
		t.Fatalf("RSS blocks out of order: categories=%d feedURL=%d published=%d\n%s", categoriesIndex, feedURLIndex, publishedIndex, text)
	}
}

func TestFormatRSSMessageUsesRSSDescriptionLimit(t *testing.T) {
	entry := rss.Entry{
		Title:       "Long RSS item",
		Description: strings.Repeat("Detailed vulnerability remediation context. ", 20),
	}
	formatCfg := &notifyfmt.FormatOptions{
		RSS: notifyfmt.RSSFormatOptions{
			FieldOrder:          []string{"title", "description"},
			DescriptionMaxChars: 90,
		},
	}

	msg := formatRSSMessage(entry, formatCfg, config.FeedTypeGeneral)
	index := firstBlockContaining(msg.Blocks, "Detailed vulnerability")
	if index == -1 {
		t.Fatalf("expected RSS description block in %#v", msg.Blocks)
	}
	if got := len([]rune(msg.Blocks[index].Text.Text)); got > 90 {
		t.Fatalf("RSS description length = %d, want <= 90: %q", got, msg.Blocks[index].Text.Text)
	}
}

func TestFormatRSSMessageDescriptionUsesSentenceSummary(t *testing.T) {
	entry := rss.Entry{
		Title:       "Vendor advisory",
		Description: "First sentence. Second sentence. Third sentence should not be cut in the middle of a word.",
	}
	formatCfg := notifyfmt.DefaultFormatOptions()
	formatCfg.RSS.FieldOrder = []string{"title", "description"}
	formatCfg.RSS.DescriptionMaxChars = 72

	msg := formatRSSMessage(entry, &formatCfg, config.FeedTypeGeneral)

	descriptionIndex := firstBlockContaining(msg.Blocks, "First sentence")
	if descriptionIndex == -1 {
		t.Fatalf("expected RSS description block in %#v", msg.Blocks)
	}
	blockText := msg.Blocks[descriptionIndex].Text.Text
	if !strings.Contains(blockText, "First sentence. Second sentence... [truncated]") {
		t.Fatalf("Slack RSS description = %q, want sentence summary marker", blockText)
	}
	if strings.Contains(blockText, "Third sentence") {
		t.Fatalf("Slack RSS description kept next sentence after truncation: %q", blockText)
	}
	if len([]rune(blockText)) > 72 {
		t.Fatalf("Slack RSS description length = %d, want <= 72", len([]rune(blockText)))
	}
}

func TestFormatRansomwareMessagePreservesSlackFieldOrderForFullWidthFields(t *testing.T) {
	entry := api.RansomwareEntry{
		Description: "Important triage context",
		ClaimURL:    "https://example.onion/post",
		Discovered:  time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	formatCfg := &notifyfmt.FormatOptions{
		Slack: notifyfmt.SlackFormatOptions{
			FieldOrder: []string{"description", "post_url"},
		},
	}

	msg := formatRansomwareMessage(entry, formatCfg)

	descriptionIndex := firstBlockContaining(msg.Blocks, "Description:")
	urlIndex := firstBlockContaining(msg.Blocks, "Ransom URL:")
	if descriptionIndex == -1 || urlIndex == -1 {
		t.Fatalf("expected description and ransom URL blocks, got indexes %d and %d", descriptionIndex, urlIndex)
	}
	if descriptionIndex > urlIndex {
		t.Fatalf("description block index = %d, want before ransom URL index %d", descriptionIndex, urlIndex)
	}
}

func TestFormatRansomwareMessageAddsButtonsForWebsiteAndScreenshot(t *testing.T) {
	entry := api.RansomwareEntry{
		WebsiteURL: "https://victim.example.com",
		Screenshot: "https://ransom.example.com/screenshot.png",
	}
	formatCfg := &notifyfmt.FormatOptions{
		FieldOrder: []string{"website", "screenshot"},
	}

	msg := formatRansomwareMessage(entry, formatCfg)

	websiteBlock := msg.Blocks[firstBlockContaining(msg.Blocks, "Website:")]
	websiteButton, ok := websiteBlock.Accessory.(slackButtonAccessory)
	if !ok {
		t.Fatalf("website block accessory = %#v, want slackButtonAccessory", websiteBlock.Accessory)
	}
	if websiteButton.URL != entry.WebsiteURL {
		t.Fatalf("website button URL = %q", websiteButton.URL)
	}

	screenshotBlock := msg.Blocks[firstBlockContaining(msg.Blocks, "Screenshot:")]
	screenshotButton, ok := screenshotBlock.Accessory.(slackButtonAccessory)
	if !ok {
		t.Fatalf("screenshot block accessory = %#v, want slackButtonAccessory", screenshotBlock.Accessory)
	}
	if screenshotButton.URL != entry.Screenshot {
		t.Fatalf("screenshot button URL = %q", screenshotButton.URL)
	}
}

func TestFormatRansomwareMessageDoesNotRepeatDiscoveredInDefaultLayout(t *testing.T) {
	entry := api.RansomwareEntry{
		Group:      "LockBit",
		Victim:     "Example Corp",
		Discovered: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	}
	cfg := config.DefaultConfig()

	msg := formatRansomwareMessage(entry, config.NotificationFormatOptions(&cfg.Format))

	if count := strings.Count(allSlackText(msg.Blocks), "Discovered:"); count != 1 {
		t.Fatalf("Discovered occurrence count = %d, want 1", count)
	}
	if !strings.Contains(allSlackText(msg.Blocks), "Source: Ransomware.live API") {
		t.Fatal("Slack context footer should still include source metadata")
	}
}

func TestFormatRansomwareMessageUsesSlackDateTokens(t *testing.T) {
	entry := api.RansomwareEntry{
		Group:      "LockBit",
		Victim:     "Example Corp",
		AttackDate: "2026-01-01T01:02:03Z",
		Discovered: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		Published:  time.Date(2026, 1, 3, 4, 5, 6, 0, time.UTC),
	}
	cfg := config.DefaultConfig()
	cfg.Format.Slack.FieldOrder = []string{"attack_date", "discovered", "published"}

	msg := formatRansomwareMessage(entry, config.NotificationFormatOptions(&cfg.Format))
	text := allSlackText(msg.Blocks)

	if count := strings.Count(text, "<!date^"); count != 3 {
		t.Fatalf("Slack date token count = %d, want 3:\n%s", count, text)
	}
	if strings.Contains(text, "&lt;!date^") {
		t.Fatalf("Slack date token was escaped:\n%s", text)
	}
}

func TestFormatRansomwareMessageParsesSharedTimestampLayoutsForSlackDates(t *testing.T) {
	entry := api.RansomwareEntry{
		Group:      "LockBit",
		Victim:     "Example Corp",
		AttackDate: "Mon, 05 Jan 2026 08:00:00 +0000",
	}
	cfg := config.DefaultConfig()
	cfg.Format.Slack.FieldOrder = []string{"attack_date"}

	msg := formatRansomwareMessage(entry, config.NotificationFormatOptions(&cfg.Format))
	text := allSlackText(msg.Blocks)

	if !strings.Contains(text, "<!date^") {
		t.Fatalf("Slack attack date did not use shared parsed date token:\n%s", text)
	}
}

func TestFormatRSSMessageUsesSlackDateTokenForPublishedFooter(t *testing.T) {
	entry := rss.Entry{
		Title:     "Feed item",
		Published: time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC),
		FeedTitle: "Vendor Feed",
	}

	msg := formatRSSMessage(entry, &notifyfmt.FormatOptions{}, config.FeedTypeGeneral)
	text := allSlackText(msg.Blocks)

	if !strings.Contains(text, "<!date^") || !strings.Contains(text, "date_short_pretty") {
		t.Fatalf("Slack RSS footer missing date token:\n%s", text)
	}
	if strings.Contains(text, "&lt;!date^") {
		t.Fatalf("Slack RSS date token was escaped:\n%s", text)
	}
}

func TestFormatRansomwareMessageUsesPlaceholderForMissingFooterDiscovered(t *testing.T) {
	entry := api.RansomwareEntry{Group: "LockBit", Victim: "Example Corp"}
	cfg := config.DefaultConfig()
	cfg.Format.EmptyFieldText = "No data"
	cfg.Format.Slack.FieldOrder = []string{"group", "victim"}

	msg := formatRansomwareMessage(entry, config.NotificationFormatOptions(&cfg.Format))
	text := allSlackText(msg.Blocks)

	if !strings.Contains(text, "Discovered: No data") {
		t.Fatalf("Slack footer = %q, want configured placeholder for missing discovered", text)
	}
	if strings.Contains(text, "Discovered: Unknown") {
		t.Fatalf("Slack footer still uses hardcoded Unknown: %q", text)
	}
}

func TestFormatRSSMessageUsesPlaceholderForMissingPublishedFooter(t *testing.T) {
	entry := rss.Entry{Title: "Undated feed item", FeedTitle: "Feed"}
	cfg := config.DefaultConfig()
	cfg.Format.EmptyFieldText = "No data"

	msg := formatRSSMessage(entry, config.NotificationFormatOptions(&cfg.Format), config.FeedTypeGeneral)
	text := allSlackText(msg.Blocks)

	if !strings.Contains(text, "Published: No data") {
		t.Fatalf("Slack RSS footer = %q, want configured placeholder for missing published", text)
	}
	if strings.Contains(text, "Published: Unknown") {
		t.Fatalf("Slack RSS footer still uses hardcoded Unknown: %q", text)
	}
}

func TestFormatRansomwareMessageBoundsAndIsolatesExternalText(t *testing.T) {
	longRTL := strings.Repeat("שלום", 700)
	entry := api.RansomwareEntry{
		Group:       longRTL,
		Victim:      longRTL,
		Activity:    longRTL,
		AttackDate:  longRTL,
		ClaimURL:    "https://" + strings.Repeat("a", 3500) + ".onion/post",
		WebsiteURL:  "https://" + strings.Repeat("b", 3500) + ".example.test",
		Screenshot:  "https://" + strings.Repeat("c", 3500) + ".example.test/image.png",
		Description: longRTL,
		Discovered:  time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	formatCfg := &notifyfmt.FormatOptions{
		ShowUnicodeFlags: false,
		FieldOrder:       []string{"group", "victim", "activity", "attack_date", "post_url", "website", "screenshot", "description"},
	}

	msg := formatRansomwareMessage(entry, formatCfg)

	if !strings.Contains(msg.Text, "\u2068") || !strings.Contains(msg.Text, "\u2069") {
		t.Fatalf("fallback text lacks bidi isolation: %q", msg.Text)
	}
	assertSlackTextLimits(t, msg.Blocks)
}

func TestFormatRSSMessageBoundsAndIsolatesExternalText(t *testing.T) {
	longRTL := strings.Repeat("שלום", 800)
	entry := rss.Entry{
		Title:       "שלום RSS Title " + longRTL,
		Link:        "https://" + strings.Repeat("x", 3500) + ".example.test/article",
		Description: "<p>" + longRTL + "</p>",
		Published:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Author:      longRTL,
		Categories:  []string{longRTL, longRTL, longRTL, "security"},
		FeedTitle:   longRTL,
	}

	msg := formatRSSMessage(entry, &notifyfmt.FormatOptions{}, config.FeedTypeGeneral)

	if !strings.Contains(msg.Text, "\u2068") || !strings.Contains(msg.Text, "\u2069") {
		t.Fatalf("fallback text lacks bidi isolation: %q", msg.Text)
	}
	assertSlackTextLimits(t, msg.Blocks)
}

func firstBlockContaining(blocks []slackBlock, text string) int {
	for i, block := range blocks {
		if block.Text != nil && strings.Contains(block.Text.Text, text) {
			return i
		}
		for _, field := range block.Fields {
			if strings.Contains(field.Text, text) {
				return i
			}
		}
		for _, element := range block.Elements {
			if strings.Contains(element.Text, text) {
				return i
			}
		}
	}
	return -1
}

func assertSlackTextLimits(t *testing.T, blocks []slackBlock) {
	t.Helper()
	for _, block := range blocks {
		if block.Text != nil {
			limit := 3000
			if block.Type == "header" {
				limit = 150
			}
			if length := len([]rune(block.Text.Text)); length > limit {
				t.Fatalf("%s block text length = %d, want <= %d", block.Type, length, limit)
			}
		}
		for _, field := range block.Fields {
			if length := len([]rune(field.Text)); length > 2000 {
				t.Fatalf("%s block field length = %d, want <= 2000", block.Type, length)
			}
		}
		for _, element := range block.Elements {
			if length := len([]rune(element.Text)); length > 3000 {
				t.Fatalf("%s block element length = %d, want <= 3000", block.Type, length)
			}
		}
		if button, ok := block.Accessory.(slackButtonAccessory); ok {
			if length := len([]rune(button.URL)); length > 3000 {
				t.Fatalf("%s block button URL length = %d, want <= 3000", block.Type, length)
			}
		}
	}
}

// fieldLabels returns the bold labels of a section block's field text objects, in order.
func fieldLabels(block slackBlock) []string {
	labels := make([]string, 0, len(block.Fields))
	for _, f := range block.Fields {
		label, _, _ := strings.Cut(f.Text, ":*\n")
		labels = append(labels, strings.TrimPrefix(label, "*"))
	}
	return labels
}

//nolint:gocyclo // permutation-driven assertions over field order; not a table split candidate
func TestFormatRansomwareMessageFieldOrderPermutations(t *testing.T) {
	entry := api.RansomwareEntry{
		Group: "LockBit", Victim: "Example Corp", Country: "US", Activity: "Manufacturing",
		WebsiteURL: "https://victim.example.com", Screenshot: "https://ransom.example.com/shot.png",
		ClaimURL: "https://example.onion/post", Description: "Incident description",
		Discovered: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), AttackDate: "2026-01-01",
	}
	tests := []struct {
		name                    string
		fieldOrder              []string
		wantBlocks              int
		wantFields              map[int][]string
		wantText, wantValue     map[int]string
		wantFooter, notInFooter string
	}{
		{name: "shipped top level field order",
			fieldOrder: []string{"victim", "activity", "country", "website", "group", "discovered", "attack_date", "description", "screenshot", "post_url"},
			wantBlocks: 8,
			wantFields: map[int][]string{1: {"Victim", "Activity", "Country"}, 3: {"Group", "Discovered", "Attack Date"}},
			wantText:   map[int]string{2: "Website:", 4: "Description:", 5: "Screenshot:", 6: "Ransom URL:"},
			wantValue:  map[int]string{1: "Example Corp", 3: "LockBit"},
			wantFooter: "Source: Ransomware.live API", notInFooter: "Discovered:"},
		{name: "discovered post_url victim",
			fieldOrder: []string{"discovered", "post_url", "victim"},
			wantBlocks: 6,
			wantFields: map[int][]string{2: {"Discovered"}, 4: {"Victim"}},
			wantText:   map[int]string{1: "Website:", 3: "Ransom URL:"},
			wantValue:  map[int]string{2: "<!date^", 4: "Example Corp"},
			wantFooter: "Source: Ransomware.live API", notInFooter: "Discovered:"},
		{name: "interleaved compact and full width three times",
			fieldOrder: []string{"group", "description", "victim", "website", "country", "screenshot", "activity", "post_url"},
			wantBlocks: 10,
			wantFields: map[int][]string{1: {"Group"}, 3: {"Victim"}, 5: {"Country"}, 7: {"Activity"}},
			wantText:   map[int]string{2: "Description:", 4: "Website:", 6: "Screenshot:", 8: "Ransom URL:"},
			wantValue:  map[int]string{1: "LockBit", 5: "United States"},
			wantFooter: "Discovered:"},
		{name: "package default order stays intact",
			fieldOrder: nil,
			wantBlocks: 5,
			wantFields: map[int][]string{2: {"Group", "Victim", "Country", "Activity", "Discovered"}},
			wantText:   map[int]string{1: "Website:", 3: "Ransom URL:"},
			wantValue:  map[int]string{2: "LockBit"},
			wantFooter: "Source: Ransomware.live API", notInFooter: "Discovered:"},
		// Compact field names are not de-duplicated (only full-width fields are, via
		// renderedFullWidth), so repeating a compact name is the only way to cross
		// slackSectionFieldLimit = 10 and reach a second section chunk.
		{name: "more than ten compact fields split across two sections",
			fieldOrder: []string{"group", "group", "group", "group", "group", "group", "group", "group", "group", "group", "group", "description", "victim"},
			wantBlocks: 7,
			wantFields: map[int][]string{
				2: {"Group", "Group", "Group", "Group", "Group", "Group", "Group", "Group", "Group", "Group"},
				3: {"Group"},
				5: {"Victim"}},
			wantText:   map[int]string{1: "Website:", 4: "Description:"},
			wantValue:  map[int]string{2: "LockBit", 5: "Example Corp"},
			wantFooter: "Discovered:"},
		// Two multi-field flushes: the second chunk of the first flush must survive the
		// eleven compact fields rendered after the full-width block, not only the first chunk.
		{name: "eleven compact fields on both sides of a full width field",
			fieldOrder: []string{
				"group", "group", "group", "group", "group", "group", "group", "group", "group", "group", "group",
				"description",
				"victim", "victim", "victim", "victim", "victim", "victim", "victim", "victim", "victim", "victim", "victim"},
			wantBlocks: 8,
			wantFields: map[int][]string{
				2: {"Group", "Group", "Group", "Group", "Group", "Group", "Group", "Group", "Group", "Group"},
				3: {"Group"},
				5: {"Victim", "Victim", "Victim", "Victim", "Victim", "Victim", "Victim", "Victim", "Victim", "Victim"},
				6: {"Victim"}},
			wantText:   map[int]string{1: "Website:", 4: "Description:"},
			wantValue:  map[int]string{3: "LockBit", 6: "Example Corp"},
			wantFooter: "Discovered:"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var cfg *notifyfmt.FormatOptions
			if tc.fieldOrder != nil {
				cfg = &notifyfmt.FormatOptions{FieldOrder: tc.fieldOrder}
			}

			msg := formatRansomwareMessage(entry, cfg)

			if len(msg.Blocks) != tc.wantBlocks {
				t.Errorf("block count = %d, want %d", len(msg.Blocks), tc.wantBlocks)
			}
			for idx, want := range tc.wantFields {
				if idx >= len(msg.Blocks) {
					t.Fatalf("block %d missing, message has %d blocks", idx, len(msg.Blocks))
				}
				got := fieldLabels(msg.Blocks[idx])
				if strings.Join(got, "|") != strings.Join(want, "|") {
					t.Errorf("block %d labels = %v, want %v", idx, got, want)
				}
			}
			for idx, want := range tc.wantValue {
				if idx >= len(msg.Blocks) {
					t.Fatalf("block %d missing, message has %d blocks", idx, len(msg.Blocks))
				}
				if len(msg.Blocks[idx].Fields) == 0 {
					t.Fatalf("block %d has no fields, want value %q", idx, want)
				}
				if !strings.Contains(msg.Blocks[idx].Fields[0].Text, want) {
					t.Errorf("block %d field 0 = %q, want to contain %q", idx, msg.Blocks[idx].Fields[0].Text, want)
				}
			}
			for idx, want := range tc.wantText {
				if idx >= len(msg.Blocks) {
					t.Fatalf("block %d missing, message has %d blocks", idx, len(msg.Blocks))
				}
				if msg.Blocks[idx].Text == nil {
					t.Fatalf("block %d has no text object, want to contain %q", idx, want)
				}
				if !strings.Contains(msg.Blocks[idx].Text.Text, want) {
					t.Errorf("block %d text = %q, want to contain %q", idx, msg.Blocks[idx].Text.Text, want)
				}
			}

			last := msg.Blocks[len(msg.Blocks)-1]
			if last.Type != "context" || len(last.Elements) == 0 {
				t.Fatalf("last block = %#v, want a context block with elements", last)
			}
			if !strings.Contains(last.Elements[0].Text, tc.wantFooter) {
				t.Errorf("footer = %q, want to contain %q", last.Elements[0].Text, tc.wantFooter)
			}
			if tc.notInFooter != "" && strings.Contains(last.Elements[0].Text, tc.notInFooter) {
				t.Errorf("footer = %q, want not to contain %q", last.Elements[0].Text, tc.notInFooter)
			}

			assertSlackTextLimits(t, msg.Blocks)
			if len(msg.Blocks) > 50 {
				t.Errorf("block count = %d, want <= 50", len(msg.Blocks))
			}
			for i, b := range msg.Blocks {
				if len(b.Fields) > 10 {
					t.Errorf("block %d field count = %d, want <= 10", i, len(b.Fields))
				}
			}
		})
	}
}

// slackLinkTargetRegex matches the target half of a Slack mrkdwn link or
// special mention (`<target|label>`). The target is never rendered as text, so
// it is removed before the bidi assertions below.
var slackLinkTargetRegex = regexp.MustCompile(`<[^|>]*\|`)

// assertBalancedIsolate pins the contract of the Slack text helpers on a
// composed block string: no embedding/override control survives, and every
// isolate initiator is closed inside the same string. A Slack block text is
// assembled from a label, mrkdwn decoration and one or more isolated values, so
// the isolate pair is not necessarily at the string boundaries -- the property
// that matters is that the scope never escapes the block.
// Depth is additionally capped at 1: every isolate a Slack block carries comes
// from one textutil.BidiIsolate/BidiIsolateLTR call on already stripped text, so
// pairs sit side by side and never nest. Without that cap a depth-balanced but
// wrongly shaped output (an extra initiator at the start plus an extra
// terminator at the end) would pass unnoticed.
// Defined per package on purpose; internal/discord has its own copy.
func assertBalancedIsolate(t *testing.T, name, value string) {
	t.Helper()
	rendered := slackLinkTargetRegex.ReplaceAllString(value, "")
	depth := 0
	for i, r := range []rune(rendered) {
		switch {
		case r >= '\u202A' && r <= '\u202E':
			t.Fatalf("%s: %q carries embedding/override control %U at rune index %d", name, value, r, i)
		case r >= '\u2066' && r <= '\u2068':
			depth++
			if depth > 1 {
				t.Fatalf("%s: %q nests bidi isolates (depth %d) at rune index %d", name, value, depth, i)
			}
		case r == '\u2069':
			depth--
			if depth < 0 {
				t.Fatalf("%s: %q closes an isolate that was never opened at rune index %d", name, value, i)
			}
		}
	}
	if depth != 0 {
		t.Fatalf("%s: %q leaves %d unterminated bidi isolate(s)", name, value, depth)
	}
}

// forEachSlackRenderedText yields every rendered text string of a Slack
// message: the fallback line and each block text, field and context element.
//
// Header blocks are included since 2026-09-03: appendRSSSlackTitle and
// appendRansomwareHeaderBlock route their plain_text through slackHeaderText,
// which strips bidi controls without adding an isolate. assertBalancedIsolate's
// three properties (no override control, depth <= 1, depth 0 at the end) all
// hold for a control-free string, so no isolate has to be present for a header
// to pass.
func forEachSlackRenderedText(msg slackMessage, visit func(name, text string)) {
	visit("msg.Text", msg.Text)
	for i, b := range msg.Blocks {
		if b.Text != nil {
			visit(fmt.Sprintf("blocks[%d].text", i), b.Text.Text)
		}
		for j, f := range b.Fields {
			visit(fmt.Sprintf("blocks[%d].fields[%d]", i, j), f.Text)
		}
		for j, e := range b.Elements {
			visit(fmt.Sprintf("blocks[%d].elements[%d]", i, j), e.Text)
		}
	}
}

func TestFormatRSSMessageStripsBidiControlsFromUntrustedText(t *testing.T) {
	entry := rss.Entry{
		Title:       "Acme \u2067Corp breach",
		Description: "leak of \u202Efdp.exe and more",
		Link:        "https://e.test/\u2066a",
		Author:      strings.Repeat("A", 300) + "\u2067" + strings.Repeat("B", 300),
		Published:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		FeedTitle:   "Feed \u2068X",
		Categories:  []string{"sec\u202Durity"},
	}

	formatCfg := &notifyfmt.FormatOptions{RSS: notifyfmt.RSSFormatOptions{ShowAuthor: true}}
	msg := formatRSSMessage(entry, formatCfg, config.FeedTypeGeneral)

	visited := 0
	forEachSlackRenderedText(msg, func(name, text string) {
		visited++
		assertBalancedIsolate(t, name, text)
	})
	if visited < 2 {
		t.Fatalf("walked %d rendered strings, want the fallback plus at least one block text", visited)
	}

	assertBalancedIsolate(t, "msg.Text", msg.Text)
	if got := len([]rune(msg.Text)); got > 3000 {
		t.Fatalf("fallback text length = %d, want <= 3000", got)
	}
	if !strings.Contains(msg.Text, "Acme Corp breach") {
		t.Fatalf("fallback text lost its visible characters: %q", msg.Text)
	}
	if !strings.Contains(msg.Text, "leak of fdp.exe and more") {
		t.Fatalf("fallback description lost its visible characters: %q", msg.Text)
	}
}

// TestFormatRansomwareMessageStripsBidiControlsFromUntrustedText mirrors
// TestFormatRSSMessageStripsBidiControlsFromUntrustedText for the ransomware
// path: the ransomware header (built from Group/Victim through
// ransomwareHeaderText) is the U+202E sink the header-bidi fix closes, and
// until the walker exclusion is removed (forEachSlackRenderedText) this test
// cannot see it.
func TestFormatRansomwareMessageStripsBidiControlsFromUntrustedText(t *testing.T) {
	entry := api.RansomwareEntry{
		Group:       "gr\u202Eoup",
		Victim:      "Vic\u2067tim",
		Country:     "DE",
		Activity:    "act\u2068ivity",
		Description: "leak of \u202Efdp.exe and more",
		Discovered:  time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	formatCfg := &notifyfmt.FormatOptions{
		FieldOrder: []string{"group", "victim", "country", "activity", "discovered", "description"},
	}

	msg := formatRansomwareMessage(entry, formatCfg)

	visited := 0
	forEachSlackRenderedText(msg, func(name, text string) {
		visited++
		assertBalancedIsolate(t, name, text)
	})
	if visited < 2 {
		t.Fatalf("walked %d rendered strings, want the fallback plus at least one block text", visited)
	}

	assertBalancedIsolate(t, "msg.Text", msg.Text)
	if got := len([]rune(msg.Text)); got > 3000 {
		t.Fatalf("fallback text length = %d, want <= 3000", got)
	}
	if !strings.Contains(msg.Text, "group -&gt; Victim") {
		t.Fatalf("fallback text lost its visible characters: %q", msg.Text)
	}
}

// TestSlackRSSHeaderStripsBidiControls is RED on the tree as it stands:
// appendRSSSlackTitle builds its plain_text from
// TruncateText(escapeSlackMrkdwnText(view.Title), slackHeaderTextLimit),
// which never strips bidi controls, so the unterminated U+2067 in the title
// reaches the header verbatim.
func TestSlackRSSHeaderStripsBidiControls(t *testing.T) {
	entry := rss.Entry{
		Title:     "Acme \u2067Corp breach",
		Link:      "https://e.test/a",
		Published: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		FeedTitle: "Feed X",
	}

	msg := formatRSSMessage(entry, &notifyfmt.FormatOptions{}, config.FeedTypeGeneral)

	if msg.Blocks[0].Type != "header" {
		t.Fatalf("blocks[0].Type = %q, want header", msg.Blocks[0].Type)
	}
	if msg.Blocks[0].Text.Type != "plain_text" {
		t.Fatalf("blocks[0].Text.Type = %q, want plain_text", msg.Blocks[0].Text.Type)
	}
	if got, want := msg.Blocks[0].Text.Text, "Acme Corp breach"; got != want {
		t.Fatalf("RSS header = %q, want %q", got, want)
	}
}

// TestSlackRSSHeaderPreservesMrkdwnEscape pins I7 at the RSS header call
// site specifically (appendRSSSlackTitle, formatter.go:938). Without this,
// dropping escapeSlackMrkdwnText from that one call site
// (Text: slackHeaderText(view.Title, slackUntitledRSSHeaderText) instead of
// slackHeaderText(escapeSlackMrkdwnText(view.Title), ...)) is caught only by
// the pre-existing, broader TestFormatRSSMessageEscapesSlackMrkdwnExternalContent
// (which asserts over the whole rendered message, not the header block in
// isolation) and by nothing in this file's new bidi-header tests: every
// title fixture they use is free of '&', '<' and '>'. Verified by mutation
// during review (2026-09-03): this test fails under that mutation while
// TestSlackControlFreeMessagesAreByteIdentical does not, since its RSS
// fixture title carries no mrkdwn-special characters either.
func TestSlackRSSHeaderPreservesMrkdwnEscape(t *testing.T) {
	entry := rss.Entry{
		Title:     "Tom & Jerry <b> attack",
		Link:      "https://e.test/a",
		Published: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		FeedTitle: "Feed X",
	}

	msg := formatRSSMessage(entry, &notifyfmt.FormatOptions{}, config.FeedTypeGeneral)

	if got, want := msg.Blocks[0].Text.Text, "Tom &amp; Jerry &lt;b&gt; attack"; got != want {
		t.Fatalf("RSS header = %q, want %q", got, want)
	}
}

// TestSlackRansomwareHeaderStripsBidiControls is RED on the tree as it
// stands: appendRansomwareHeaderBlock never strips bidi controls from
// ransomwareHeaderText's output. The exact equality pins I6 (no isolate
// added) and I7 (the -&gt; escape preserved) at once.
func TestSlackRansomwareHeaderStripsBidiControls(t *testing.T) {
	entry := api.RansomwareEntry{
		Group:      "gr\u202Eoup",
		Victim:     "Vic\u2067tim",
		Country:    "DE",
		Discovered: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}

	msg := formatRansomwareMessage(entry, &notifyfmt.FormatOptions{})

	if got, want := msg.Blocks[0].Text.Text, "Ransomware Alert: group -&gt; Victim"; got != want {
		t.Fatalf("ransomware header = %q, want %q", got, want)
	}

	rssMsg := formatRSSMessage(rss.Entry{
		Title:     "Acme \u2067Corp breach",
		Published: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		FeedTitle: "Feed X",
	}, &notifyfmt.FormatOptions{}, config.FeedTypeGeneral)

	for _, header := range []string{rssMsg.Blocks[0].Text.Text, msg.Blocks[0].Text.Text} {
		for _, r := range header {
			if r >= '\u2066' && r <= '\u2069' {
				t.Fatalf("header %q carries isolate control %U", header, r)
			}
			if r >= '\u202A' && r <= '\u202E' {
				t.Fatalf("header %q carries embedding/override control %U", header, r)
			}
		}
	}
}

// TestSlackHeaderLimitCountsRenderedRunesAfterStrip is RED on the tree as it
// stands: today the header carries the 40 leading U+2067 controls and only
// 107 "A" survive the 150-rune limit. After the fix the budget is spent on
// rendered characters only.
func TestSlackHeaderLimitCountsRenderedRunesAfterStrip(t *testing.T) {
	entry := rss.Entry{
		Title:     strings.Repeat("\u2067", 40) + strings.Repeat("A", 200),
		Published: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		FeedTitle: "Feed X",
	}

	msg := formatRSSMessage(entry, &notifyfmt.FormatOptions{}, config.FeedTypeGeneral)
	header := msg.Blocks[0].Text.Text

	if got := len([]rune(header)); got != 150 {
		t.Fatalf("header rune length = %d, want 150", got)
	}
	for _, r := range header {
		if (r >= '\u2066' && r <= '\u2069') || (r >= '\u202A' && r <= '\u202E') {
			t.Fatalf("header %q carries bidi control %U", header, r)
		}
	}
	// textTruncationMarker is "..."; a marker change should be caught here too.
	if want := strings.Repeat("A", 147) + "..."; header != want {
		t.Fatalf("header = %q, want %q", header, want)
	}
}

// testBidiIsolateOpen and testBidiIsolateClose are the paired isolate
// controls textutil.BidiIsolate wraps natural text in (U+2068, U+2069),
// spelled out as rune escapes rather than pasted invisible characters.
//
// jsonEscLT/jsonEscGT/jsonEscAmp are the literal six-character sequences
// (backslash, "u", four hex digits) that encoding/json.Marshal emits in place
// of "<", ">" and "&" in every string value by default (SetEscapeHTML is
// never turned off in internal/slack/webhook.go) -- spelled out as named
// constants so the golden strings below read as the actual marshalled bytes
// rather than the bare characters that would silently fail to match them.
const (
	testBidiIsolateOpen  = "\u2068"
	testBidiIsolateClose = "\u2069"
	jsonEscLT            = "\\u003c"
	jsonEscGT            = "\\u003e"
	jsonEscAmp           = "\\u0026"
)

// TestSlackControlFreeMessagesAreByteIdentical is GREEN today and after the
// fix: it is the I1 tripwire that a regression to BidiIsolate-wrapping the
// header, or to a strip-before-truncate reorder, must trip. The golden bytes
// below are the actual encoding/json.Marshal output, re-verified against the
// unmodified tree during plan review.
func TestSlackControlFreeMessagesAreByteIdentical(t *testing.T) {
	rssEntry := rss.Entry{
		Title:       "Acme Corp breach",
		Description: "leak of pdf.exe and more",
		Link:        "https://e.test/a",
		Published:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		FeedTitle:   "Feed X",
	}
	rssCfg := &notifyfmt.FormatOptions{RSS: notifyfmt.RSSFormatOptions{ShowAuthor: true}}
	rssMsg := formatRSSMessage(rssEntry, rssCfg, config.FeedTypeGeneral)

	rssGot, err := json.Marshal(rssMsg)
	if err != nil {
		t.Fatalf("json.Marshal(rssMsg) error = %v", err)
	}
	// Titles are never links: the title section is now
	// plain (no <url|text> link construct); the button accessory carries the
	// URL, byte-identical to before.
	wantRSSJSON := `{"text":"` + testBidiIsolateOpen + `RSS Feed Update: Acme Corp breach | Description: leak of pdf.exe and more | Link: https://e.test/a | Published: 2026-01-01 00:00:00 UTC | Source: Feed X` + testBidiIsolateClose + `","blocks":[{"type":"header","text":{"type":"plain_text","text":"Acme Corp breach"}},{"type":"section","text":{"type":"mrkdwn","text":"*` + testBidiIsolateOpen + `Acme Corp breach` + testBidiIsolateClose + `*"},"accessory":{"type":"button","text":{"type":"plain_text","text":"Open Article"},"url":"https://e.test/a","action_id":"open_article","accessibility_label":"Open Article"}},{"type":"divider"},{"type":"section","text":{"type":"mrkdwn","text":"` + testBidiIsolateOpen + `leak of pdf.exe and more` + testBidiIsolateClose + `"}},{"type":"context","elements":[{"type":"mrkdwn","text":"Published: ` + jsonEscLT + `!date^1767225600^{date_short_pretty} {time_secs}|2026-01-01 00:00:00 UTC` + jsonEscGT + ` | Source: ` + testBidiIsolateOpen + `Feed X` + testBidiIsolateClose + `"}]}]}`
	if string(rssGot) != wantRSSJSON {
		t.Fatalf("RSS control-free payload changed:\n got=%s\nwant=%s", rssGot, wantRSSJSON)
	}

	ransomwareEntry := api.RansomwareEntry{
		Group:      "group",
		Victim:     "Victim",
		Country:    "DE",
		Discovered: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	ransomwareMsg := formatRansomwareMessage(ransomwareEntry, &notifyfmt.FormatOptions{})

	ransomwareGot, err := json.Marshal(ransomwareMsg)
	if err != nil {
		t.Fatalf("json.Marshal(ransomwareMsg) error = %v", err)
	}
	wantRansomwareJSON := `{"text":"` + testBidiIsolateOpen + `Ransomware Alert: group -` + jsonEscAmp + `gt; Victim | Group: group | Victim: Victim | Country: DE | Discovered: 2026-01-01 00:00:00 UTC | Source: Ransomware.live API` + testBidiIsolateClose + `","blocks":[{"type":"header","text":{"type":"plain_text","text":"Ransomware Alert: group -` + jsonEscAmp + `gt; Victim"}},{"type":"section","fields":[{"type":"mrkdwn","text":"*Group:*\n` + testBidiIsolateOpen + `group` + testBidiIsolateClose + `"},{"type":"mrkdwn","text":"*Victim:*\n` + testBidiIsolateOpen + `Victim` + testBidiIsolateClose + `"},{"type":"mrkdwn","text":"*Country:*\n` + testBidiIsolateOpen + `Germany` + testBidiIsolateClose + `"},{"type":"mrkdwn","text":"*Discovered:*\n` + jsonEscLT + `!date^1767225600^{date_short_pretty} {time_secs}|2026-01-01 00:00:00 UTC` + jsonEscGT + `"}]},{"type":"context","elements":[{"type":"mrkdwn","text":"Source: Ransomware.live API"}]}]}`
	if string(ransomwareGot) != wantRansomwareJSON {
		t.Fatalf("ransomware control-free payload changed:\n got=%s\nwant=%s", ransomwareGot, wantRansomwareJSON)
	}
}

// TestSlackHeaderNeverEmptyAfterStrip is the I8 tripwire: a bare strip with
// no fallback would emit "" for a title that is nothing but bidi controls,
// which a Slack header block (1-150 characters required) does not accept.
func TestSlackHeaderNeverEmptyAfterStrip(t *testing.T) {
	t.Run("rss all-controls title falls back to the constant", func(t *testing.T) {
		entry := rss.Entry{
			Title:     "\u2067\u202E\u2066",
			Link:      "https://e.test/a",
			Published: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			FeedTitle: "Feed X",
		}
		msg := formatRSSMessage(entry, &notifyfmt.FormatOptions{}, config.FeedTypeGeneral)
		if got, want := msg.Blocks[0].Text.Text, "Untitled RSS item"; got != want {
			t.Fatalf("RSS header = %q, want %q", got, want)
		}
		if len(msg.Blocks[0].Text.Text) == 0 {
			t.Fatal("RSS header is empty")
		}
	})

	t.Run("ransomware header cannot strip to empty", func(t *testing.T) {
		// model.DisplayRansomwareTitle supplies "Unknown group"/"Unknown victim" for
		// a blank identity, so this path never reaches slackHeaderText's fallback;
		// asserted anyway so both call sites share one contract.
		entry := api.RansomwareEntry{Group: "\u202E", Victim: ""}
		msg := formatRansomwareMessage(entry, &notifyfmt.FormatOptions{})
		if got, want := msg.Blocks[0].Text.Text, "Ransomware Alert:  -&gt; Unknown victim"; got != want {
			t.Fatalf("ransomware header = %q, want %q", got, want)
		}
	})

	t.Run("rss blank title still uses the pre-existing FeedTitle fallback", func(t *testing.T) {
		entry := rss.Entry{
			Title:     "",
			FeedTitle: "F",
			Published: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		}
		msg := formatRSSMessage(entry, &notifyfmt.FormatOptions{}, config.FeedTypeGeneral)
		if got, want := msg.Blocks[0].Text.Text, "F"; got != want {
			t.Fatalf("RSS header = %q, want %q", got, want)
		}
	})
}
