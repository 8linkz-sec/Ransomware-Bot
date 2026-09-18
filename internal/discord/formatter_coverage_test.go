package discord

import (
	"strings"
	"testing"
	"time"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/api"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/formatfields"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/notifyfmt"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/rss"
)

func TestFormatRansomwareEmbedSkipsUnknownFieldOrderNames(t *testing.T) {
	entry := api.RansomwareEntry{Group: "LockBit", Victim: "Example Corp"}
	formatCfg := &notifyfmt.FormatOptions{
		FieldOrder: []string{"group", "not_a_real_field", "victim"},
	}

	embed := formatRansomwareEmbed(entry, formatCfg)

	if len(embed.Fields) != 2 {
		t.Fatalf("field count = %d, want 2 (unknown field skipped): %#v", len(embed.Fields), embed.Fields)
	}
	if !containsDiscordFieldValue(embed.Fields, "LockBit") || !containsDiscordFieldValue(embed.Fields, "Example Corp") {
		t.Fatalf("expected group and victim fields, got %#v", embed.Fields)
	}
}

func TestFormatRansomwareEmbedStopsAtEmbedFieldLimit(t *testing.T) {
	fieldOrder := make([]string, 0, discordEmbedFieldLimit+5)
	for i := 0; i < discordEmbedFieldLimit+5; i++ {
		fieldOrder = append(fieldOrder, "victim")
	}
	formatCfg := &notifyfmt.FormatOptions{FieldOrder: fieldOrder}

	embed := formatRansomwareEmbed(api.RansomwareEntry{Victim: "Example Corp"}, formatCfg)

	if len(embed.Fields) != discordEmbedFieldLimit {
		t.Fatalf("field count = %d, want capped at %d", len(embed.Fields), discordEmbedFieldLimit)
	}
}

func TestDiscordEmbedTextLengthNilEmbed(t *testing.T) {
	if got := discordEmbedTextLength(nil); got != 0 {
		t.Fatalf("discordEmbedTextLength(nil) = %d, want 0", got)
	}
}

func TestDiscordEmbedCanAddFieldAllowsNilField(t *testing.T) {
	if !discordEmbedCanAddField(&MessageEmbed{Title: "t"}, nil) {
		t.Fatal("discordEmbedCanAddField(embed, nil) = false, want true")
	}
}

func TestDiscordTrimEmbedToTotalLimitDropsTrailingFields(t *testing.T) {
	embed := &MessageEmbed{}
	for i := 0; i < 7; i++ {
		embed.Fields = append(embed.Fields, &MessageEmbedField{
			Name:  "F",
			Value: strings.Repeat("x", 1000),
		})
	}

	discordTrimEmbedToTotalLimit(embed)

	if got := discordEmbedTextLength(embed); got > discordEmbedTotalTextLimit {
		t.Fatalf("embed text length = %d, want <= %d", got, discordEmbedTotalTextLimit)
	}
	if len(embed.Fields) != 5 {
		t.Fatalf("field count after trim = %d, want 5", len(embed.Fields))
	}
}

func TestDiscordRansomwareLabelNilConfigUsesFallback(t *testing.T) {
	if got := discordRansomwareLabel(nil, "ransomware_alert", "Ransomware Alert"); got != "Ransomware Alert" {
		t.Fatalf("discordRansomwareLabel(nil) = %q, want fallback", got)
	}
}

func TestDiscordRSSLabelKeysMapsFeedTitleToSourceAlias(t *testing.T) {
	got := discordRSSLabelKeys(formatfields.RSSFieldFeedTitle)
	if len(got) != 2 || got[0] != formatfields.RSSFieldFeedTitle || got[1] != "source" {
		t.Fatalf("discordRSSLabelKeys(feed_title) = %#v, want [feed_title source]", got)
	}

	if got := discordRSSLabelKeys("published"); len(got) != 1 || got[0] != "published" {
		t.Fatalf("discordRSSLabelKeys(published) = %#v, want [published]", got)
	}
}

func TestCreateRansomwareFieldRejectsUnknownFieldName(t *testing.T) {
	entry := api.RansomwareEntry{Group: "LockBit"}
	if field := createRansomwareField("not_a_real_field", entry, nil); field != nil {
		t.Fatalf("createRansomwareField(unknown) = %#v, want nil", field)
	}
}

func TestPreviewRansomwareEntryExposesEmbedMetadata(t *testing.T) {
	formatCfg := notifyfmt.DefaultFormatOptions()
	entry := api.RansomwareEntry{
		Group:      "LockBit",
		Victim:     "Example Corp",
		Discovered: time.Date(2025, 3, 4, 5, 6, 7, 0, time.UTC),
		WebsiteURL: "https://victim.example.com",
	}

	preview := PreviewRansomwareEntry(entry, &formatCfg)

	if preview["platform"] != "discord" || preview["kind"] != "ransomware" {
		t.Fatalf("preview identity = %v/%v, want discord/ransomware", preview["platform"], preview["kind"])
	}
	title, ok := preview["title"].(string)
	if !ok || !strings.Contains(title, "LockBit") {
		t.Fatalf("preview title = %v, want group name", preview["title"])
	}
	if strings.Contains(title, "Example Corp") {
		t.Fatalf("preview title = %q, want group-only title without victim", title)
	}
	// Titles are never links: embed.URL is always empty
	// now, so the "url" preview key never appears -- the website URL must
	// instead be findable in the fields list, which is the operator's
	// second requirement (the URL stays visible) applying to --dry-run
	// output too.
	if _, ok := preview["url"]; ok {
		t.Fatalf("preview url = %v, want key absent (URL moved to fields)", preview["url"])
	}
	if ts, ok := preview["timestamp"].(string); !ok || !strings.HasPrefix(ts, "2025-03-04") {
		t.Fatalf("preview timestamp = %v, want RFC3339 discovered time", preview["timestamp"])
	}
	if footer, ok := preview["footer"].(string); !ok || footer != discordRansomwareSourceLabel {
		t.Fatalf("preview footer = %v, want %q", preview["footer"], discordRansomwareSourceLabel)
	}
	fields, ok := preview["fields"].([]map[string]any)
	if !ok || len(fields) == 0 {
		t.Fatalf("preview fields = %#v, want non-empty field list", preview["fields"])
	}
	for _, key := range []string{"name", "value", "inline"} {
		if _, ok := fields[0][key]; !ok {
			t.Fatalf("preview field missing key %q: %#v", key, fields[0])
		}
	}
	websiteInFields := false
	for _, field := range fields {
		if value, ok := field["value"].(string); ok && strings.Contains(value, "victim.example.com") {
			websiteInFields = true
			break
		}
	}
	if !websiteInFields {
		t.Fatalf("preview fields = %#v, want the website URL to appear in a field value", fields)
	}
}

func TestPreviewRSSEntryExposesDescriptionAndAuthor(t *testing.T) {
	formatCfg := notifyfmt.DefaultFormatOptions()
	formatCfg.RSS.ShowAuthor = true
	entry := rss.Entry{
		Title:       "Advisory Published",
		Link:        "https://example.test/advisory",
		Description: "Summary of the advisory.",
		Author:      "Jane Doe",
		Published:   time.Date(2025, 6, 7, 8, 9, 10, 0, time.UTC),
		FeedTitle:   "Example Feed",
		FeedURL:     "https://example.test/feed.xml",
	}

	preview := PreviewRSSEntry(entry, "general", &formatCfg)

	if preview["platform"] != "discord" || preview["kind"] != "rss" {
		t.Fatalf("preview identity = %v/%v, want discord/rss", preview["platform"], preview["kind"])
	}
	if description, ok := preview["description"].(string); !ok || !strings.Contains(description, "Summary of the advisory") {
		t.Fatalf("preview description = %v, want RSS summary", preview["description"])
	}
	if author, ok := preview["author"].(string); !ok || !strings.Contains(author, "Jane Doe") {
		t.Fatalf("preview author = %v, want RSS author", preview["author"])
	}
	// Titles are never links: embed.URL is always empty
	// now, so the "url" preview key never appears -- the article link must
	// instead be findable in the fields list.
	if _, ok := preview["url"]; ok {
		t.Fatalf("preview url = %v, want key absent (URL moved to fields)", preview["url"])
	}
	if footer, ok := preview["footer"].(string); !ok || !strings.Contains(footer, "Example Feed") {
		t.Fatalf("preview footer = %v, want feed title", preview["footer"])
	}
	fields, ok := preview["fields"].([]map[string]any)
	if !ok {
		t.Fatalf("preview fields = %#v, want field list", preview["fields"])
	}
	linkInFields := false
	for _, field := range fields {
		if value, ok := field["value"].(string); ok && strings.Contains(value, "example.test/advisory") {
			linkInFields = true
			break
		}
	}
	if !linkInFields {
		t.Fatalf("preview fields = %#v, want the article link to appear in a field value", fields)
	}
}

func TestDiscordRSSFieldOrderNormalizesConfiguredFields(t *testing.T) {
	formatCfg := &notifyfmt.FormatOptions{}
	formatCfg.RSS.FieldOrder = []string{" Title ", "bogus", "title", "link"}

	got := discordRSSFieldOrder(formatCfg)
	if len(got) != 2 || got[0] != formatfields.RSSFieldTitle || got[1] != formatfields.RSSFieldLink {
		t.Fatalf("discordRSSFieldOrder() = %#v, want [title link]", got)
	}
}

func TestDiscordRSSFieldOrderFallsBackWhenAllInvalid(t *testing.T) {
	formatCfg := &notifyfmt.FormatOptions{}
	formatCfg.RSS.FieldOrder = []string{"bogus", "nope"}

	got := discordRSSFieldOrder(formatCfg)
	want := formatfields.DefaultRSSFieldOrder()
	if len(got) != len(want) {
		t.Fatalf("discordRSSFieldOrder() = %#v, want default order %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("discordRSSFieldOrder()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestFormatRSSEmbedUsesConfiguredRSSTitleTextForUntitledItems(t *testing.T) {
	formatCfg := &notifyfmt.FormatOptions{}
	formatCfg.RSS.TitleText = "Custom Feed Item"
	entry := rss.Entry{FeedURL: "https://example.test/feed.xml"}

	embed := formatRSSEmbed(entry, "general", formatCfg)

	if !strings.Contains(embed.Title, "Custom Feed Item") {
		t.Fatalf("embed title = %q, want configured RSS title fallback", embed.Title)
	}
}

func TestFormatRSSEmbedRendersPublishedPlaceholderWhenEmpty(t *testing.T) {
	formatCfg := &notifyfmt.FormatOptions{
		ShowEmptyFields: true,
		EmptyFieldText:  "N/A",
	}
	entry := rss.Entry{Title: "Alert", FeedTitle: "Example Feed"}

	embed := formatRSSEmbed(entry, "general", formatCfg)

	idx := discordFieldIndex(embed.Fields, "Published")
	if idx < 0 {
		t.Fatalf("embed missing Published placeholder field: %#v", embed.Fields)
	}
	if embed.Fields[idx].Value != "N/A" {
		t.Fatalf("Published field value = %q, want placeholder", embed.Fields[idx].Value)
	}
	if embed.Timestamp != "" {
		t.Fatalf("embed timestamp = %q, want empty for zero published time", embed.Timestamp)
	}
}

func TestFormatRSSEmbedFeedURLFieldRespectsShowEmpty(t *testing.T) {
	entry := rss.Entry{Title: "Alert"}

	hideCfg := &notifyfmt.FormatOptions{}
	hideCfg.RSS.FieldOrder = []string{"title", "feed_url"}
	embed := formatRSSEmbed(entry, "general", hideCfg)
	if containsDiscordField(embed.Fields, "Feed URL") {
		t.Fatalf("Feed URL field rendered for empty URL with hidden empty fields: %#v", embed.Fields)
	}

	showCfg := &notifyfmt.FormatOptions{ShowEmptyFields: true, EmptyFieldText: "N/A"}
	showCfg.RSS.FieldOrder = []string{"title", "feed_url"}
	embed = formatRSSEmbed(entry, "general", showCfg)
	idx := discordFieldIndex(embed.Fields, "Feed URL")
	if idx < 0 {
		t.Fatalf("embed missing Feed URL placeholder field: %#v", embed.Fields)
	}
	if embed.Fields[idx].Value != "N/A" {
		t.Fatalf("Feed URL field value = %q, want placeholder", embed.Fields[idx].Value)
	}
}

func TestDiscordRSSDescriptionMaxCharsBounds(t *testing.T) {
	if got := discordRSSDescriptionMaxChars(nil); got != discordRSSSummaryLimit {
		t.Fatalf("discordRSSDescriptionMaxChars(nil) = %d, want %d", got, discordRSSSummaryLimit)
	}

	overCfg := &notifyfmt.FormatOptions{}
	overCfg.RSS.DescriptionMaxChars = discordRSSSummaryLimit + 1
	if got := discordRSSDescriptionMaxChars(overCfg); got != discordRSSSummaryLimit {
		t.Fatalf("discordRSSDescriptionMaxChars(over limit) = %d, want cap %d", got, discordRSSSummaryLimit)
	}

	tinyCfg := &notifyfmt.FormatOptions{}
	tinyCfg.RSS.DescriptionMaxChars = 1
	if got := discordRSSDescriptionMaxChars(tinyCfg); got != 3 {
		t.Fatalf("discordRSSDescriptionMaxChars(1) = %d, want minimum 3", got)
	}

	normalCfg := &notifyfmt.FormatOptions{}
	normalCfg.RSS.DescriptionMaxChars = 42
	if got := discordRSSDescriptionMaxChars(normalCfg); got != 42 {
		t.Fatalf("discordRSSDescriptionMaxChars(42) = %d, want 42", got)
	}
}

func TestDiscordDescriptionMaxCharsCapsAtFieldValueLimit(t *testing.T) {
	formatCfg := &notifyfmt.FormatOptions{}
	formatCfg.Discord.DescriptionMaxChars = discordEmbedFieldValueLimit + 500
	if got := discordDescriptionMaxChars(formatCfg); got != discordEmbedFieldValueLimit {
		t.Fatalf("discordDescriptionMaxChars(over limit) = %d, want %d", got, discordEmbedFieldValueLimit)
	}
}

// discordDescriptionMaxChars is the non-RSS twin of
// discordRSSDescriptionMaxChars (which floors at 3) but passed 1 and 2
// straight through, so the maxLength <= 2 branches of discordRichText /
// discordDescriptionText / discordURLText could skip TrimDanglingEscape and
// BidiIsolate. Not reachable through config: validateDescriptionMaxChars
// floors discord.description_max_chars at 50 unconditionally at config load.
// Defensive parity only.
func TestDiscordDescriptionMaxCharsFloorsAtThree(t *testing.T) {
	tests := []struct {
		name  string
		value int
		want  int
	}{
		{"one floors to three", 1, 3},
		{"two floors to three", 2, 3},
		{"three unchanged", 3, 3},
		{"four unchanged", 4, 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			formatCfg := &notifyfmt.FormatOptions{}
			formatCfg.Discord.DescriptionMaxChars = tt.value
			if got := discordDescriptionMaxChars(formatCfg); got != tt.want {
				t.Fatalf("discordDescriptionMaxChars(%d) = %d, want %d", tt.value, got, tt.want)
			}
		})
	}
}

func TestParseDiscordHexColorParsesLetterDigits(t *testing.T) {
	if got, ok := parseDiscordHexColor("#a1b2c3"); !ok || got != 0xa1b2c3 {
		t.Fatalf("parseDiscordHexColor(#a1b2c3) = %#x, %v; want 0xa1b2c3, true", got, ok)
	}
	if got, ok := parseDiscordHexColor("#A1B2C3"); !ok || got != 0xa1b2c3 {
		t.Fatalf("parseDiscordHexColor(#A1B2C3) = %#x, %v; want 0xa1b2c3, true", got, ok)
	}
	if _, ok := parseDiscordHexColor("#12g456"); ok {
		t.Fatal("parseDiscordHexColor(#12g456) ok = true, want false")
	}
}

func TestDiscordTextHelperEdgeCases(t *testing.T) {
	if got := discordNaturalText("", 100); got != "" {
		t.Fatalf("discordNaturalText(empty) = %q, want empty", got)
	}
	if got := discordNaturalText("abcdef", 2); got != "ab" {
		t.Fatalf("discordNaturalText(tiny budget) = %q, want plain truncation without isolates", got)
	}
	if got := discordRichText("", 100); got != "" {
		t.Fatalf("discordRichText(empty) = %q, want empty", got)
	}
	// The escape sits above the tiny-budget branch, so the <= 2 path escapes too.
	if got := discordRichText("[abcdef", 2); got != `\[` {
		t.Fatalf("discordRichText(tiny budget) = %q, want an escaped bracket without isolates", got)
	}
	if got := discordDescriptionText("", 100); got != "" {
		t.Fatalf("discordDescriptionText(empty) = %q, want empty", got)
	}
	if got := discordDescriptionText("abcdef", 2); got != "ab" {
		t.Fatalf("discordDescriptionText(tiny budget) = %q, want plain truncation without isolates", got)
	}
	if got := discordURLText("", 100); got != "" {
		t.Fatalf("discordURLText(empty) = %q, want empty", got)
	}
	if got := discordURLText("abcdef", 2); got != "ab" {
		t.Fatalf("discordURLText(tiny budget) = %q, want plain truncation without isolates", got)
	}
}
