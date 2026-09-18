package slack

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/api"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/formatfields"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/notifyfmt"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/rss"
)

func TestFormatRansomwareMessageAllowsNilFormatConfig(t *testing.T) {
	entry := api.RansomwareEntry{
		Group:      "LockBit",
		Victim:     "Example Corp",
		Country:    "DE",
		Discovered: time.Date(2025, 3, 4, 5, 6, 7, 0, time.UTC),
	}

	msg := formatRansomwareMessage(entry, nil)

	if !strings.Contains(msg.Text, "Ransomware Alert") || !strings.Contains(msg.Text, "LockBit") {
		t.Fatalf("fallback text = %q, want default alert label and group", msg.Text)
	}
	if len(msg.Blocks) == 0 || msg.Blocks[0].Type != "header" {
		t.Fatalf("blocks = %#v, want leading header block", msg.Blocks)
	}
	if !strings.Contains(msg.Blocks[0].Text.Text, "LockBit") {
		t.Fatalf("header text = %q, want group name", msg.Blocks[0].Text.Text)
	}
	if !strings.Contains(allSlackText(msg.Blocks), ransomwareSourceLabel) {
		t.Fatalf("blocks missing default source label %q: %s", ransomwareSourceLabel, allSlackText(msg.Blocks))
	}
}

func TestFormatRSSMessageAllowsNilFormatConfig(t *testing.T) {
	entry := rss.Entry{
		Title:     "Advisory Published",
		Link:      "https://example.test/advisory",
		FeedTitle: "Example Feed",
		FeedURL:   "https://example.test/feed.xml",
	}

	msg := formatRSSMessage(entry, nil, "general")

	if !strings.Contains(msg.Text, notifyfmt.DefaultSlackRSSText) {
		t.Fatalf("fallback text = %q, want default RSS header %q", msg.Text, notifyfmt.DefaultSlackRSSText)
	}
	if !strings.Contains(msg.Text, "Advisory Published") {
		t.Fatalf("fallback text = %q, want article title", msg.Text)
	}
	if len(msg.Blocks) == 0 {
		t.Fatal("blocks empty, want rendered RSS blocks")
	}
}

func TestFormatRansomwareMessageSkipsUnknownFieldOrderNames(t *testing.T) {
	entry := api.RansomwareEntry{Group: "LockBit", Victim: "Example Corp"}
	formatCfg := &notifyfmt.FormatOptions{
		FieldOrder: []string{"not_a_real_field", "group"},
	}

	msg := formatRansomwareMessage(entry, formatCfg)

	if !containsFieldText(msg.Blocks, "LockBit") {
		t.Fatalf("blocks missing group value: %s", allSlackText(msg.Blocks))
	}
	if strings.Contains(allSlackText(msg.Blocks), "not_a_real_field") {
		t.Fatalf("blocks rendered unknown field name: %s", allSlackText(msg.Blocks))
	}
	if got := countFieldBlocks(msg.Blocks); got != 1 {
		t.Fatalf("section block count = %d, want 1 (only group rendered)", got)
	}
}

func TestSlackDescriptionMaxCharsCapsAtSectionLimit(t *testing.T) {
	formatCfg := &notifyfmt.FormatOptions{}
	formatCfg.Slack.DescriptionMaxChars = slackSectionTextLimit + 500
	if got := slackDescriptionMaxChars(formatCfg); got != slackSectionTextLimit {
		t.Fatalf("slackDescriptionMaxChars(over limit) = %d, want %d", got, slackSectionTextLimit)
	}
}

func TestSlackURLButtonRejectsNonHTTPScheme(t *testing.T) {
	if button, ok := slackURLButton("Open", "ftp://example.com/file"); ok {
		t.Fatalf("slackURLButton(ftp URL) = %#v, true; want rejection", button)
	}
}

func TestSlackButtonActionIDFallsBackToOpenLink(t *testing.T) {
	if got := slackButtonActionID("   "); got != "open_link" {
		t.Fatalf("slackButtonActionID(blank) = %q, want open_link", got)
	}
	if got := slackButtonActionID("Open Website"); got != "open_website" {
		t.Fatalf("slackButtonActionID(Open Website) = %q, want open_website", got)
	}
}

func TestAppendSlackBlockStopsAtBlockLimit(t *testing.T) {
	blocks := make([]slackBlock, slackBlockLimit)
	got := appendSlackBlock(blocks, slackBlock{Type: "divider"})
	if len(got) != slackBlockLimit {
		t.Fatalf("block count = %d, want capped at %d", len(got), slackBlockLimit)
	}
}

func TestTrimSlackBlocksTruncatesOverLimit(t *testing.T) {
	blocks := make([]slackBlock, slackBlockLimit+5)
	got := trimSlackBlocks(blocks)
	if len(got) != slackBlockLimit {
		t.Fatalf("block count = %d, want trimmed to %d", len(got), slackBlockLimit)
	}
}

func TestSlackLabeledFieldTextClampsNegativeValueBudget(t *testing.T) {
	got := slackLabeledFieldText("Very Long Label", "value", "", false, 3)
	want := "*Very Long Label:*\n"
	if got != want {
		t.Fatalf("slackLabeledFieldText(tiny budget) = %q, want label prefix only %q", got, want)
	}
}

func TestSlackLabeledDescriptionTextHandlesPlaceholderAndTinyBudget(t *testing.T) {
	if got := slackLabeledDescriptionText("Description", "N/A", "N/A", 100); got != "*Description:*\nN/A" {
		t.Fatalf("slackLabeledDescriptionText(placeholder) = %q, want raw placeholder", got)
	}
	if got := slackLabeledDescriptionText("Description", "text", "N/A", 3); got != "*Description:*\n" {
		t.Fatalf("slackLabeledDescriptionText(tiny budget) = %q, want label prefix only", got)
	}
}

// Finding 4 (internal/slack/formatter.go): slackLabeledDescriptionText built
// its "*label:*\n" prefix from the raw operator-configured label, unlike its
// sibling slackLabeledFieldText three lines above it, which escapes the same
// class of string with escapeSlackMrkdwnText. An operator-configured
// description label containing "&", "<" or ">" reached Slack mrkdwn
// unescaped, risking "<...>" being parsed as a link/mention tag.
func TestSlackLabeledDescriptionTextEscapesLabel(t *testing.T) {
	got := slackLabeledDescriptionText("Report & <Findings>", "some description text", "N/A", 200)
	want := "*Report &amp; &lt;Findings&gt;:*\n" + testBidiIsolateOpen + "some description text" + testBidiIsolateClose
	if got != want {
		t.Fatalf("slackLabeledDescriptionText(special-char label) = %q, want escaped label %q", got, want)
	}
}

func TestSlackDateHelpersUseDefaultDisplayFormat(t *testing.T) {
	ts := time.Date(2025, 3, 4, 5, 6, 7, 0, time.UTC)

	got := slackDate(ts)
	if !strings.HasPrefix(got, fmt.Sprintf("<!date^%d^", ts.Unix())) {
		t.Fatalf("slackDate() = %q, want Slack date token with unix timestamp", got)
	}
	if !strings.Contains(got, "2025-03-04 05:06:07 UTC") {
		t.Fatalf("slackDate() = %q, want default-format fallback text", got)
	}

	if got := slackDateFallback(ts); got != "2025-03-04 05:06:07 UTC" {
		t.Fatalf("slackDateFallback() = %q, want default display format", got)
	}
}

func TestSlackTextHelperEdgeCases(t *testing.T) {
	if got := slackNaturalText("", 100); got != "" {
		t.Fatalf("slackNaturalText(empty) = %q, want empty", got)
	}
	if got := slackNaturalText("abcdef", 2); got != "ab" {
		t.Fatalf("slackNaturalText(tiny budget) = %q, want plain truncation without isolates", got)
	}
	if got := slackDescriptionText("", 100); got != "" {
		t.Fatalf("slackDescriptionText(empty) = %q, want empty", got)
	}
	if got := slackDescriptionText("abcdef", 2); got != "ab" {
		t.Fatalf("slackDescriptionText(tiny budget) = %q, want plain truncation without isolates", got)
	}
	if got := slackURLText("", 100); got != "" {
		t.Fatalf("slackURLText(empty) = %q, want empty", got)
	}
	if got := slackURLText("abcdef", 2); got != "ab" {
		t.Fatalf("slackURLText(tiny budget) = %q, want plain truncation without isolates", got)
	}
}

func TestFormatRSSMessageShowsLinkPlaceholderWithTitlePresent(t *testing.T) {
	formatCfg := &notifyfmt.FormatOptions{
		ShowEmptyFields: true,
		EmptyFieldText:  "N/A",
	}
	entry := rss.Entry{Title: "Advisory", FeedTitle: "Example Feed"}

	msg := formatRSSMessage(entry, formatCfg, "general")

	text := allSlackText(msg.Blocks)
	if !strings.Contains(text, "*Link:*") || !strings.Contains(text, "N/A") {
		t.Fatalf("blocks missing Link placeholder: %s", text)
	}
}

func TestPreviewRansomwareEntryExposesBlocks(t *testing.T) {
	formatCfg := notifyfmt.DefaultFormatOptions()
	formatCfg.Slack.FieldOrder = []string{"group", "victim", "website"}
	entry := api.RansomwareEntry{
		Group:      "LockBit",
		Victim:     "Example Corp",
		WebsiteURL: "https://victim.example.com",
		Discovered: time.Date(2025, 3, 4, 5, 6, 7, 0, time.UTC),
	}

	preview := PreviewRansomwareEntry(entry, &formatCfg)

	if preview["platform"] != "slack" || preview["kind"] != "ransomware" {
		t.Fatalf("preview identity = %v/%v, want slack/ransomware", preview["platform"], preview["kind"])
	}
	if fallback, ok := preview["fallback_text"].(string); !ok || !strings.Contains(fallback, "LockBit") {
		t.Fatalf("preview fallback_text = %v, want group name", preview["fallback_text"])
	}
	blocks, ok := preview["blocks"].([]map[string]any)
	if !ok || len(blocks) == 0 {
		t.Fatalf("preview blocks = %#v, want non-empty block list", preview["blocks"])
	}

	var sawText, sawFields, sawElements, sawButton bool
	for _, block := range blocks {
		if _, ok := block["text"]; ok {
			sawText = true
		}
		if _, ok := block["fields"]; ok {
			sawFields = true
		}
		if _, ok := block["elements"]; ok {
			sawElements = true
		}
		if accessory, ok := block["accessory"].(map[string]any); ok {
			sawButton = true
			if accessory["url"] != "https://victim.example.com" {
				t.Fatalf("accessory preview url = %v, want website URL", accessory["url"])
			}
			if _, ok := accessory["text"].(map[string]any); !ok {
				t.Fatalf("accessory preview text = %#v, want text object map", accessory["text"])
			}
		}
	}
	if !sawText || !sawFields || !sawElements || !sawButton {
		t.Fatalf("preview blocks missing sections (text=%v fields=%v elements=%v button=%v): %#v",
			sawText, sawFields, sawElements, sawButton, blocks)
	}
}

func TestPreviewRSSEntryExposesBlocks(t *testing.T) {
	formatCfg := notifyfmt.DefaultFormatOptions()
	entry := rss.Entry{
		Title:     "Advisory Published",
		Link:      "https://example.test/advisory",
		FeedTitle: "Example Feed",
		FeedURL:   "https://example.test/feed.xml",
	}

	preview := PreviewRSSEntry(entry, &formatCfg, "general")

	if preview["platform"] != "slack" || preview["kind"] != "rss" {
		t.Fatalf("preview identity = %v/%v, want slack/rss", preview["platform"], preview["kind"])
	}
	if fallback, ok := preview["fallback_text"].(string); !ok || !strings.Contains(fallback, "Advisory Published") {
		t.Fatalf("preview fallback_text = %v, want article title", preview["fallback_text"])
	}
	if blocks, ok := preview["blocks"].([]map[string]any); !ok || len(blocks) == 0 {
		t.Fatalf("preview blocks = %#v, want non-empty block list", preview["blocks"])
	}
}

func TestSlackAccessoryPreviewDescribesUnknownAccessoryType(t *testing.T) {
	preview := slackAccessoryPreview(42)
	if preview["type"] != "int" {
		t.Fatalf("slackAccessoryPreview(42) type = %v, want dynamic type name", preview["type"])
	}
}

func TestSlackRSSFieldOrderNormalizesConfiguredFields(t *testing.T) {
	formatCfg := &notifyfmt.FormatOptions{}
	formatCfg.RSS.FieldOrder = []string{" Title ", "bogus", "title", "link"}

	got := slackRSSFieldOrder(formatCfg)
	if len(got) != 2 || got[0] != formatfields.RSSFieldTitle || got[1] != formatfields.RSSFieldLink {
		t.Fatalf("slackRSSFieldOrder() = %#v, want [title link]", got)
	}
}

func TestSlackRSSFieldOrderFallsBackWhenAllInvalid(t *testing.T) {
	formatCfg := &notifyfmt.FormatOptions{}
	formatCfg.RSS.FieldOrder = []string{"bogus", "nope"}

	got := slackRSSFieldOrder(formatCfg)
	want := formatfields.DefaultRSSFieldOrder()
	if len(got) != len(want) {
		t.Fatalf("slackRSSFieldOrder() = %#v, want default order %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("slackRSSFieldOrder()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestFormatRSSMessageRendersLinkSectionWithoutTitle(t *testing.T) {
	formatCfg := &notifyfmt.FormatOptions{}
	formatCfg.RSS.FieldOrder = []string{"link"}
	entry := rss.Entry{
		Title: "Advisory",
		Link:  "https://example.test/article",
	}

	msg := formatRSSMessage(entry, formatCfg, "general")

	text := allSlackText(msg.Blocks)
	if !strings.Contains(text, "*Link:*") || !strings.Contains(text, "example.test/article") {
		t.Fatalf("blocks missing standalone link section: %s", text)
	}
}

func TestFormatRSSMessageFeedURLFieldRespectsShowEmpty(t *testing.T) {
	entry := rss.Entry{Title: "Advisory"}

	hideCfg := &notifyfmt.FormatOptions{}
	hideCfg.RSS.FieldOrder = []string{"title", "feed_url"}
	msg := formatRSSMessage(entry, hideCfg, "general")
	if strings.Contains(allSlackText(msg.Blocks), "*Feed URL:*") {
		t.Fatalf("Feed URL block rendered for empty URL with hidden empty fields: %s", allSlackText(msg.Blocks))
	}

	showCfg := &notifyfmt.FormatOptions{ShowEmptyFields: true, EmptyFieldText: "N/A"}
	showCfg.RSS.FieldOrder = []string{"title", "feed_url"}
	msg = formatRSSMessage(entry, showCfg, "general")
	if !strings.Contains(allSlackText(msg.Blocks), "*Feed URL:*\nN/A") {
		t.Fatalf("blocks missing Feed URL placeholder: %s", allSlackText(msg.Blocks))
	}
}

func TestAppendRSSSlackContextSkipsBlockWithoutParts(t *testing.T) {
	// Source text always falls back to "RSS Feed" in BuildRSSView, so the
	// empty-parts guard is exercised with an explicitly empty source view.
	view := notifyfmt.RSSView{}
	options := rssSlackOptionsFromConfig(nil)

	blocks := appendRSSSlackContext(nil, view, false, true, options)
	if len(blocks) != 0 {
		t.Fatalf("appendRSSSlackContext(no parts) = %#v, want no context block", blocks)
	}
}

func TestSlackRSSDescriptionMaxCharsCapsAtSectionLimit(t *testing.T) {
	formatCfg := &notifyfmt.FormatOptions{}
	formatCfg.RSS.DescriptionMaxChars = slackSectionTextLimit + 500
	if got := slackRSSDescriptionMaxChars(formatCfg); got != slackSectionTextLimit {
		t.Fatalf("slackRSSDescriptionMaxChars(over limit) = %d, want %d", got, slackSectionTextLimit)
	}
}

func TestSlackRSSHeaderTextPrefersConfiguredRSSTitleText(t *testing.T) {
	formatCfg := &notifyfmt.FormatOptions{}
	formatCfg.RSS.TitleText = "Custom Feed"
	if got := slackRSSHeaderText(formatCfg, "general"); got != "Custom Feed" {
		t.Fatalf("slackRSSHeaderText(custom RSS title) = %q, want Custom Feed", got)
	}
}
