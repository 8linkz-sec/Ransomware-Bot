package discord

import (
	"strings"
	"testing"
	"time"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/api"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/config"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/notifyfmt"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/rss"
)

var emptyRansomwareFieldNames = []string{
	"country",
	"victim",
	"group",
	"activity",
	"attack_date",
	"discovered",
	"post_url",
	"website",
	"description",
	"screenshot",
}

func ransomwareEmptyFieldNames() []string {
	return append([]string(nil), emptyRansomwareFieldNames...)
}

func ransomwareEmptyFieldNamesExcept(excluded ...string) []string {
	exclude := make(map[string]struct{}, len(excluded))
	for _, name := range excluded {
		exclude[name] = struct{}{}
	}

	fields := make([]string, 0, len(emptyRansomwareFieldNames)-len(exclude))
	for _, name := range emptyRansomwareFieldNames {
		if _, ok := exclude[name]; !ok {
			fields = append(fields, name)
		}
	}
	return fields
}

func emptyRansomwareFieldFormat(showEmpty bool, placeholder string) *notifyfmt.FormatOptions {
	return &notifyfmt.FormatOptions{
		ShowEmptyFields: showEmpty,
		EmptyFieldText:  placeholder,
	}
}

func TestDefaultFieldOrderProducesFields(t *testing.T) {
	formatCfg := notifyfmt.DefaultFormatOptions()
	fieldOrder := formatCfg.FieldOrder

	entry := api.RansomwareEntry{
		Group:       "TestGroup",
		Victim:      "TestVictim",
		Country:     "US",
		Activity:    "data leak",
		AttackDate:  "2025-01-15 10:00:00",
		Discovered:  time.Now(),
		ClaimURL:    "https://example.onion/post",
		WebsiteURL:  "https://victim.example.com",
		Description: "Test description",
		Screenshot:  "https://screenshot.example.com/img.png",
	}

	for _, fieldName := range fieldOrder {
		field := createRansomwareField(fieldName, entry, &formatCfg)
		if field == nil {
			t.Errorf("field %q from default FieldOrder returned nil", fieldName)
		}
	}
}

func TestFieldNameAliases(t *testing.T) {
	entry := api.RansomwareEntry{
		ClaimURL:   "https://example.onion/post",
		WebsiteURL: "https://victim.example.com",
	}

	formatCfg := &notifyfmt.FormatOptions{
		ShowUnicodeFlags: false,
		ShowEmptyFields:  false,
	}

	// Both old and new names for post_url should work
	for _, name := range []string{"post_url", "claim_url"} {
		field := createRansomwareField(name, entry, formatCfg)
		if field == nil {
			t.Errorf("field name %q should produce a non-nil field", name)
		}
	}

	// Both old and new names for website should work
	for _, name := range []string{"website", "url"} {
		field := createRansomwareField(name, entry, formatCfg)
		if field == nil {
			t.Errorf("field name %q should produce a non-nil field", name)
		}
	}
}

func TestFormatRansomwareEmbedNormalizesFieldOrderNames(t *testing.T) {
	entry := api.RansomwareEntry{
		Group:    "LockBit",
		Victim:   "Example Corp",
		ClaimURL: "https://example.onion/post",
	}
	formatCfg := &notifyfmt.FormatOptions{
		FieldOrder:      []string{" Group ", " Victim ", "POST_URL"},
		ShowEmptyFields: false,
		EmptyFieldText:  "N/A",
	}

	embed := formatRansomwareEmbed(entry, formatCfg)

	for _, want := range []string{"LockBit", "Example Corp", "hxxps://example.onion/post"} {
		if !containsDiscordFieldValue(embed.Fields, want) {
			t.Fatalf("normalized Discord field_order output missing %q: %#v", want, embed.Fields)
		}
	}
}

func TestFormatRansomwareEmbedHappyPathRendersConfiguredFields(t *testing.T) {
	entry := api.RansomwareEntry{
		Group:       "LockBit",
		Victim:      "TestCorp",
		Country:     "US",
		Activity:    "data leak",
		ClaimURL:    "https://example.onion/post",
		WebsiteURL:  "https://victim.example.com",
		Description: "Test description",
		Discovered:  time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	}
	formatCfg := &notifyfmt.FormatOptions{
		ShowUnicodeFlags: false,
		FieldOrder:       []string{"group", "victim", "country", "activity", "post_url", "website", "description"},
		Discord:          notifyfmt.DiscordFormatOptions{ShowIcons: false},
	}

	embed := formatRansomwareEmbed(entry, formatCfg)

	if !strings.Contains(embed.Title, "Ransomware Alert:") || !strings.Contains(embed.Title, "LockBit") {
		t.Fatalf("Discord ransomware title = %q, want alert group", embed.Title)
	}
	if strings.Contains(embed.Title, "TestCorp") {
		t.Fatalf("Discord ransomware title = %q, want group-only title without victim", embed.Title)
	}
	if embed.Footer == nil || embed.Footer.Text != "Ransomware.live API" {
		t.Fatalf("Discord ransomware footer = %#v, want source label", embed.Footer)
	}
	for _, want := range []struct {
		label string
		value string
	}{
		{label: "Group", value: "LockBit"},
		{label: "Victim", value: "TestCorp"},
		{label: "Country", value: "United States"},
		{label: "Activity", value: "data leak"},
		{label: "Ransom URL", value: "hxxps://example.onion/post"},
		{label: "Website", value: "https://victim.example.com"},
		{label: "Description", value: "Test description"},
	} {
		if !containsDiscordField(embed.Fields, want.label) || !containsDiscordFieldValue(embed.Fields, want.value) {
			t.Fatalf("Discord ransomware embed missing %s=%q: %#v", want.label, want.value, embed.Fields)
		}
	}
	groupIndex := discordFieldIndex(embed.Fields, "Group")
	victimIndex := discordFieldIndex(embed.Fields, "Victim")
	countryIndex := discordFieldIndex(embed.Fields, "Country")
	if groupIndex == -1 || victimIndex == -1 || countryIndex == -1 || groupIndex >= victimIndex || victimIndex >= countryIndex {
		t.Fatalf("Discord ransomware fields not in configured order: group=%d victim=%d country=%d fields=%#v", groupIndex, victimIndex, countryIndex, embed.Fields)
	}
}

func TestFormatRansomwareEmbedUsesConfiguredLabels(t *testing.T) {
	entry := api.RansomwareEntry{
		Group:      "LockBit",
		Victim:     "Example Corp",
		WebsiteURL: "https://victim.example.com",
	}
	formatCfg := &notifyfmt.FormatOptions{
		FieldOrder: []string{"group", "victim", "website"},
		Discord: notifyfmt.DiscordFormatOptions{
			ShowIcons: false,
			FieldLabels: map[string]string{
				"group":             "Gruppe",
				"victim":            "Ziel",
				"website":           "Webseite",
				"ransomware_alert":  "Ransomware Warnung",
				"ransomware_source": "Ransomware Live",
			},
		},
	}

	embed := formatRansomwareEmbed(entry, formatCfg)

	if !strings.Contains(embed.Title, "Ransomware Warnung:") {
		t.Fatalf("Discord ransomware title = %q, want configured alert label", embed.Title)
	}
	if embed.Footer == nil || embed.Footer.Text != "Ransomware Live" {
		t.Fatalf("Discord ransomware footer = %#v, want configured source label", embed.Footer)
	}
	for _, label := range []string{"Gruppe", "Ziel", "Webseite"} {
		if !containsDiscordField(embed.Fields, label) {
			t.Fatalf("Discord ransomware fields missing configured label %q: %#v", label, embed.Fields)
		}
	}
}

func TestFormatRansomwareEmbedUsesDisplayLocaleForCountry(t *testing.T) {
	entry := api.RansomwareEntry{
		Country: "DE",
	}
	formatCfg := notifyfmt.DefaultFormatOptions()
	formatCfg.DisplayLocale = "de"
	formatCfg.ShowUnicodeFlags = false
	formatCfg.FieldOrder = []string{"country"}

	embed := formatRansomwareEmbed(entry, &formatCfg)

	if !containsDiscordFieldValue(embed.Fields, "Deutschland") {
		t.Fatalf("Discord country field not localized: %#v", embed.Fields)
	}
	if containsDiscordFieldValue(embed.Fields, "Germany") {
		t.Fatalf("Discord country field used English fallback: %#v", embed.Fields)
	}
}

func TestFormatRansomwareEmbedUsesDisplayTimestampFormat(t *testing.T) {
	entry := api.RansomwareEntry{
		Discovered: time.Date(2026, 1, 2, 2, 4, 5, 0, time.UTC),
	}
	formatCfg := notifyfmt.DefaultFormatOptions()
	formatCfg.TimestampFormat = "02.01.2006 15:04 MST"
	formatCfg.DisplayTimezone = "Europe/Berlin"
	formatCfg.FieldOrder = []string{"discovered"}

	embed := formatRansomwareEmbed(entry, &formatCfg)

	if !containsDiscordFieldValue(embed.Fields, "02.01.2026 03:04 CET") {
		t.Fatalf("Discord timestamp field not localized: %#v", embed.Fields)
	}
}

func TestDocumentedIDAndPublishedFields(t *testing.T) {
	entry := api.RansomwareEntry{
		ID:        "victim-123",
		Published: time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC),
	}

	formatCfg := &notifyfmt.FormatOptions{
		ShowEmptyFields: false,
		FieldOrder:      []string{"id", "published"},
	}

	embed := formatRansomwareEmbed(entry, formatCfg)

	if !containsDiscordFieldValue(embed.Fields, "victim-123") {
		t.Fatal("Discord embed does not include documented id field")
	}
	if !containsDiscordFieldValue(embed.Fields, "2026-02-03 04:05:06 UTC") {
		t.Fatal("Discord embed does not include documented published field")
	}
}

func TestRansomwareTimestampFieldsIncludeTimezone(t *testing.T) {
	entry := api.RansomwareEntry{
		AttackDate: "2026-02-03 04:05:06.123456",
		Discovered: time.Date(
			2026, 2, 3, 5, 5, 6, 0,
			time.FixedZone("CET", 60*60),
		),
		Published: time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC),
	}

	formatCfg := &notifyfmt.FormatOptions{
		ShowEmptyFields: false,
		FieldOrder: []string{
			"attack_date",
			"discovered",
			"published",
		},
	}

	embed := formatRansomwareEmbed(entry, formatCfg)

	for _, want := range []string{
		"2026-02-03 04:05:06 UTC",
		"2026-02-03 04:05:06 UTC",
		"2026-02-03 04:05:06 UTC",
	} {
		if !containsDiscordFieldValue(embed.Fields, want) {
			t.Fatalf("Discord timestamp field missing %q: %#v", want, embed.Fields)
		}
	}
}

func TestFormatRansomwareEmbedTitleUsesUnknownIdentity(t *testing.T) {
	formatCfg := &notifyfmt.FormatOptions{FieldOrder: []string{"group", "victim"}}

	embed := formatRansomwareEmbed(api.RansomwareEntry{Victim: "TestCorp"}, formatCfg)

	if !strings.Contains(embed.Title, "Unknown group") {
		t.Fatalf("embed title = %q, want Unknown group placeholder", embed.Title)
	}
	if embed.Timestamp != "" {
		t.Fatalf("embed timestamp = %q, want empty for zero discovered", embed.Timestamp)
	}
}

func TestFormatRansomwareEmbedAllowsNilFormatConfig(t *testing.T) {
	entry := api.RansomwareEntry{
		Group:  "LockBit",
		Victim: "Example Corp",
	}

	embed := formatRansomwareEmbed(entry, nil)

	if embed == nil {
		t.Fatal("formatRansomwareEmbed() returned nil")
	}
	if !strings.Contains(embed.Title, "LockBit") {
		t.Fatalf("embed title = %q, want group name", embed.Title)
	}
	if !containsDiscordFieldValue(embed.Fields, "LockBit") {
		t.Fatalf("nil format config did not use default field order: %#v", embed.Fields)
	}
}

func TestCreateRansomwareFieldAllowsNilFormatConfig(t *testing.T) {
	field := createRansomwareField("group", api.RansomwareEntry{Group: "LockBit"}, nil)
	if field == nil {
		t.Fatal("createRansomwareField() returned nil")
	}
	if !strings.Contains(field.Value, "LockBit") {
		t.Fatalf("field value = %q, want group", field.Value)
	}
}

func TestFormatRansomwareEmbedNeverLinksTitleWebsiteStaysAField(t *testing.T) {
	entry := api.RansomwareEntry{
		Group:      "LockBit",
		Victim:     "Example Corp",
		WebsiteURL: "https://victim.example.com",
		Discovered: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}

	embed := formatRansomwareEmbed(entry, &notifyfmt.FormatOptions{FieldOrder: []string{"website"}})

	if embed.URL != "" {
		t.Fatalf("embed.URL = %q, want empty (titles are never links)", embed.URL)
	}
	if !containsDiscordField(embed.Fields, "Website") {
		t.Fatal("website field should remain present for copyability")
	}
}

func TestFormatTimestamp(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"2025-01-15 10:30:45.123456", "2025-01-15 10:30:45 UTC"},
		{"2025-01-15 10:30:45", "2025-01-15 10:30:45 UTC"},
		{"2025-01-15T10:30:45Z", "2025-01-15 10:30:45 UTC"},
		{"2025-01-15T10:30:45+02:00", "2025-01-15 08:30:45 UTC"},
		{"invalid-date", "invalid-date"},
		{"", ""},
	}

	for _, tt := range tests {
		result := formatTimestamp(tt.input)
		if result != tt.expected {
			t.Errorf("formatTimestamp(%q) = %q, want %q", tt.input, result, tt.expected)
		}
	}
}

func TestTruncateDescription(t *testing.T) {

	// No truncation needed
	short := "Hello world"
	if result := discordDescriptionText(short, 50); !strings.Contains(result, short) {
		t.Errorf("expected no truncation, got %q", result)
	}

	// Exact length
	exact := "12345"
	if result := discordDescriptionText(exact, 7); !strings.Contains(result, exact) {
		t.Errorf("expected no truncation at exact length, got %q", result)
	}

	// Truncation with sentence boundary
	text := "First sentence. Second sentence. Third sentence is longer."
	result := discordDescriptionText(text, 55)
	if !strings.Contains(result, "... [truncated]") {
		t.Errorf("expected explicit truncation marker at sentence boundary, got %q", result)
	}
	if len([]rune(result)) > 55 {
		t.Errorf("result length %d exceeds max 55", len([]rune(result)))
	}

	// Truncation with ellipsis (no good sentence boundary)
	noSentence := strings.Repeat("a", 100)
	result = discordDescriptionText(noSentence, 35)
	if !strings.Contains(result, "... [truncated]") {
		t.Errorf("expected explicit marker when no sentence boundary, got %q", result)
	}
	if len([]rune(result)) > 35 {
		t.Errorf("result length %d exceeds max 35", len([]rune(result)))
	}

	// Empty string
	if result := discordDescriptionText("", 100); result != "" {
		t.Errorf("expected empty string, got %q", result)
	}
}

// TestCreateRansomwareField_ShowEmptyFields verifies that when ShowEmptyFields is true,
// empty entry fields are rendered with the configured placeholder text instead of returning nil.
func TestCreateRansomwareField_ShowEmptyFields(t *testing.T) {
	formatCfg := emptyRansomwareFieldFormat(true, "N/A")
	entry := api.RansomwareEntry{} // all fields empty except Discovered (zero time)

	for _, fieldName := range ransomwareEmptyFieldNames() {
		field := createRansomwareField(fieldName, entry, formatCfg)
		if field == nil {
			t.Errorf("ShowEmptyFields=true: field %q should not be nil for empty entry", fieldName)
			continue
		}
		if field.Value != "N/A" {
			t.Errorf("field %q value = %q, want %q", fieldName, field.Value, "N/A")
		}
	}
}

// TestCreateRansomwareField_HideEmptyFields verifies that when ShowEmptyFields is false,
// empty entry fields return nil (are omitted from the embed).
func TestCreateRansomwareField_HideEmptyFields(t *testing.T) {
	formatCfg := emptyRansomwareFieldFormat(false, "N/A")
	entry := api.RansomwareEntry{} // all fields empty

	for _, fieldName := range ransomwareEmptyFieldNamesExcept("discovered") {
		field := createRansomwareField(fieldName, entry, formatCfg)
		if field != nil {
			t.Errorf("ShowEmptyFields=false: field %q should be nil for empty entry, got value %q", fieldName, field.Value)
		}
	}

	discoveredField := createRansomwareField("discovered", entry, formatCfg)
	if discoveredField != nil {
		t.Fatalf("ShowEmptyFields=false: zero 'discovered' should be nil, got %q", discoveredField.Value)
	}
}

// TestCreateRansomwareField_CustomPlaceholder verifies that a custom EmptyFieldText
// value (e.g. "---") is used instead of the default "N/A".
func TestCreateRansomwareField_CustomPlaceholder(t *testing.T) {
	formatCfg := emptyRansomwareFieldFormat(true, "---")
	entry := api.RansomwareEntry{} // all fields empty

	for _, fieldName := range ransomwareEmptyFieldNamesExcept("discovered") {
		field := createRansomwareField(fieldName, entry, formatCfg)
		if field == nil {
			t.Errorf("custom placeholder: field %q should not be nil", fieldName)
			continue
		}
		if field.Value != "---" {
			t.Errorf("field %q value = %q, want %q", fieldName, field.Value, "---")
		}
	}
}

// TestFormatRansomwareEmbed_EmptyFieldsLayout verifies that formatRansomwareEmbed
// with ShowEmptyFields=true includes all configured fields in the embed output.
func TestFormatRansomwareEmbed_EmptyFieldsLayout(t *testing.T) {
	formatCfg := emptyRansomwareFieldFormat(true, "N/A")
	formatCfg.ShowUnicodeFlags = false
	formatCfg.FieldOrder = ransomwareEmptyFieldNames()
	entry := api.RansomwareEntry{
		Discovered: time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC),
	}

	embed := formatRansomwareEmbed(entry, formatCfg)

	if embed == nil {
		t.Fatal("embed should not be nil")
	}

	// The embed should have a title
	if embed.Title == "" {
		t.Error("embed title should not be empty")
	}

	for _, f := range embed.Fields {
		if f.Name == "\u200B" || f.Value == "\u200B" {
			t.Fatalf("embed contains invisible spacer field: %#v", f)
		}
	}

	// We expect one field per entry in FieldOrder (10 fields)
	expectedFields := len(formatCfg.FieldOrder)
	if len(embed.Fields) != expectedFields {
		t.Errorf("expected %d fields, got %d", expectedFields, len(embed.Fields))
	}

	// Verify placeholder values appear for empty fields (not discovered)
	placeholderCount := 0
	for _, f := range embed.Fields {
		if f.Value == "N/A" {
			placeholderCount++
		}
	}

	// All fields except "discovered" should have "N/A" (9 fields)
	expectedPlaceholders := expectedFields - 1 // minus discovered
	if placeholderCount != expectedPlaceholders {
		t.Errorf("expected %d placeholder fields, got %d", expectedPlaceholders, placeholderCount)
	}

	// Verify discovered has an actual formatted date, not a placeholder
	discoveredFound := false
	for _, f := range embed.Fields {
		if strings.Contains(f.Name, "Discovered") {
			discoveredFound = true
			if f.Value == "N/A" || f.Value == "" {
				t.Error("discovered field should have a formatted date value, not a placeholder")
			}
			if !strings.Contains(f.Value, "2025-06-15") {
				t.Errorf("discovered field value %q should contain '2025-06-15'", f.Value)
			}
		}
	}
	if !discoveredFound {
		t.Error("discovered field not found in embed")
	}
}

func TestFormatRansomwareEmbedIsolatesExternalText(t *testing.T) {
	formatCfg := &notifyfmt.FormatOptions{
		ShowEmptyFields:  false,
		ShowUnicodeFlags: false,
		FieldOrder:       []string{"group", "victim", "description", "post_url"},
	}
	entry := api.RansomwareEntry{
		Group:       "שלום Group",
		Victim:      "ضحية Example",
		Description: "عنوان mixed text",
		ClaimURL:    "https://example.onion/שלום",
		Discovered:  time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}

	embed := formatRansomwareEmbed(entry, formatCfg)

	if !strings.Contains(embed.Title, "\u2068שלום Group\u2069") {
		t.Fatalf("embed title lacks bidi isolation: %q", embed.Title)
	}
	for _, want := range []string{"\u2068שלום Group\u2069", "\u2068ضحية Example\u2069", "\u2068عنوان mixed text\u2069", "\u2066hxxps://example.onion/שלום\u2069"} {
		found := false
		for _, field := range embed.Fields {
			if strings.Contains(field.Value, want) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected isolated value %q in embed fields", want)
		}
	}
}

func TestFormatRansomwareEmbedBoundsFieldValuesAndFieldCount(t *testing.T) {
	longText := strings.Repeat("A", 1500)
	formatCfg := &notifyfmt.FormatOptions{
		ShowEmptyFields:  false,
		ShowUnicodeFlags: false,
		FieldOrder: []string{
			"victim", "group", "activity", "attack_date", "post_url", "website",
			"description", "screenshot", "victim", "group", "activity", "attack_date",
			"post_url", "website", "description", "screenshot", "victim", "group",
			"activity", "attack_date", "post_url", "website", "description", "screenshot",
			"victim", "group", "activity", "attack_date",
		},
	}
	entry := api.RansomwareEntry{
		Group:       longText,
		Victim:      longText,
		Activity:    longText,
		AttackDate:  longText,
		ClaimURL:    "https://" + strings.Repeat("a", 1500) + ".onion/post",
		WebsiteURL:  "https://" + strings.Repeat("b", 1500) + ".example.test",
		Description: longText,
		Screenshot:  "https://" + strings.Repeat("c", 1500) + ".example.test/screenshot.png",
		Discovered:  time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}

	embed := formatRansomwareEmbed(entry, formatCfg)

	if len(embed.Fields) > 25 {
		t.Fatalf("embed field count = %d, want <= 25", len(embed.Fields))
	}
	for _, field := range embed.Fields {
		if len([]rune(field.Value)) > 1024 {
			t.Fatalf("field %q value length = %d, want <= 1024", field.Name, len([]rune(field.Value)))
		}
	}
	if length := discordEmbedTextLengthForTest(embed); length > 6000 {
		t.Fatalf("embed total text length = %d, want <= 6000", length)
	}
}

func discordEmbedTextLengthForTest(embed *MessageEmbed) int {
	if embed == nil {
		return 0
	}
	total := len([]rune(embed.Title)) + len([]rune(embed.Description))
	if embed.Footer != nil {
		total += len([]rune(embed.Footer.Text))
	}
	if embed.Author != nil {
		total += len([]rune(embed.Author.Name))
	}
	for _, field := range embed.Fields {
		total += len([]rune(field.Name)) + len([]rune(field.Value))
	}
	return total
}

func TestFormatRSSEmbedBoundsAndIsolatesMetadata(t *testing.T) {
	long := strings.Repeat("א", 3000)
	entry := rss.Entry{
		Title:       "שלום RSS Title",
		Link:        "notaurl://" + strings.Repeat("x", 3000),
		Description: "תיאור mixed description",
		Published:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Author:      long,
		Categories:  []string{long, "security"},
		FeedTitle:   long,
	}

	formatCfg := &notifyfmt.FormatOptions{RSS: notifyfmt.RSSFormatOptions{ShowAuthor: true}}
	embed := formatRSSEmbed(entry, config.FeedTypeGeneral, formatCfg)

	if !strings.Contains(embed.Title, "\u2068שלום RSS Title\u2069") {
		t.Fatalf("RSS title lacks bidi isolation: %q", embed.Title)
	}
	if embed.URL != "" {
		t.Fatalf("invalid RSS link should not be assigned to embed.URL: %q", embed.URL)
	}
	if embed.Footer == nil || len([]rune(embed.Footer.Text)) > 2048 {
		t.Fatalf("footer text length = %d, want <= 2048", len([]rune(embed.Footer.Text)))
	}
	if embed.Author == nil || len([]rune(embed.Author.Name)) > 256 {
		t.Fatalf("author name length = %d, want <= 256", len([]rune(embed.Author.Name)))
	}
	for _, field := range embed.Fields {
		if len([]rune(field.Value)) > 1024 {
			t.Fatalf("field %q value length = %d, want <= 1024", field.Name, len([]rune(field.Value)))
		}
	}
	if !containsDiscordField(embed.Fields, "Link") {
		t.Fatal("invalid long RSS link should be represented as a bounded Link field")
	}
}

func TestFormatRSSEmbedUsesFeedTypeColor(t *testing.T) {
	entry := rss.Entry{
		Title:     "RSS item",
		Published: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}

	tests := []struct {
		name     string
		feedType string
		want     int
	}{
		{name: "general", feedType: config.FeedTypeGeneral, want: defaultRSSColor},
		{name: "government", feedType: config.FeedTypeGovernment, want: defaultGovernmentColor},
		{name: "ransomware", feedType: config.FeedTypeRansomware, want: defaultRansomwareColor},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			embed := formatRSSEmbed(entry, tt.feedType, nil)
			if embed.Color == nil || *embed.Color != tt.want {
				t.Fatalf("RSS embed color = %#v, want %#x", embed.Color, tt.want)
			}
		})
	}
}

func TestFormatRSSEmbedHappyPathRendersArticleMetadata(t *testing.T) {
	published := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	entry := rss.Entry{
		Title:       "Test Article",
		Link:        "https://example.com/article",
		Description: "Test description",
		Author:      "Test Author",
		Published:   published,
		FeedTitle:   "Test Feed",
		FeedURL:     "https://example.com/feed.xml",
		Categories:  []string{"security", "threat"},
	}
	formatCfg := &notifyfmt.FormatOptions{
		RSS: notifyfmt.RSSFormatOptions{
			ShowAuthor: true,
			FieldOrder: []string{"title", "description", "link", "author", "categories", "published", "feed_title", "feed_url"},
		},
		Discord: notifyfmt.DiscordFormatOptions{ShowIcons: false},
	}

	embed := formatRSSEmbed(entry, config.FeedTypeGeneral, formatCfg)

	if !strings.Contains(embed.Title, "Test Article") {
		t.Fatalf("Discord RSS title = %q, want article title", embed.Title)
	}
	if embed.URL != "" {
		t.Fatalf("Discord RSS URL = %q, want empty (titles are never links)", embed.URL)
	}
	if !containsDiscordField(embed.Fields, "Link") || !containsDiscordFieldValue(embed.Fields, entry.Link) {
		t.Fatalf("Discord RSS embed missing Link=%q field: %#v", entry.Link, embed.Fields)
	}
	if !strings.Contains(embed.Description, "Test description") {
		t.Fatalf("Discord RSS description = %q, want article description", embed.Description)
	}
	if embed.Author == nil || !strings.Contains(embed.Author.Name, "Test Author") {
		t.Fatalf("Discord RSS author = %#v, want Test Author", embed.Author)
	}
	if embed.Footer == nil || !strings.Contains(embed.Footer.Text, "Test Feed") {
		t.Fatalf("Discord RSS footer = %#v, want feed title", embed.Footer)
	}
	if embed.Timestamp != published.Format(time.RFC3339) {
		t.Fatalf("Discord RSS timestamp = %q, want %q", embed.Timestamp, published.Format(time.RFC3339))
	}
	for _, want := range []struct {
		label string
		value string
	}{
		{label: "Categories", value: "security, threat"},
		{label: "Published", value: "2026-01-02 03:04:05"},
		{label: "Feed URL", value: "https://example.com/feed.xml"},
	} {
		if !containsDiscordField(embed.Fields, want.label) || !containsDiscordFieldValue(embed.Fields, want.value) {
			t.Fatalf("Discord RSS embed missing %s=%q: %#v", want.label, want.value, embed.Fields)
		}
	}
}

func TestFormatRSSEmbedUsesConfiguredLabels(t *testing.T) {
	entry := rss.Entry{
		Title:      "Vendor advisory",
		Published:  time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC),
		FeedURL:    "notaurl://feed",
		Categories: []string{"security", "ransomware"},
	}
	formatCfg := &notifyfmt.FormatOptions{
		RSS: notifyfmt.RSSFormatOptions{
			FieldOrder: []string{"title", "categories", "published", "feed_url"},
		},
		Discord: notifyfmt.DiscordFormatOptions{
			ShowIcons: false,
			FieldLabels: map[string]string{
				"categories": "Tags",
				"published":  "Veroeffentlicht",
				"feed_url":   "Feed Adresse",
			},
		},
	}

	embed := formatRSSEmbed(entry, config.FeedTypeGeneral, formatCfg)

	for _, want := range []struct {
		label string
		value string
	}{
		{label: "Tags", value: "security, ransomware"},
		{label: "Veroeffentlicht", value: "2026-02-03 04:05:06"},
		{label: "Feed Adresse", value: "notaurl://feed"},
	} {
		if !containsDiscordField(embed.Fields, want.label) || !containsDiscordFieldValue(embed.Fields, want.value) {
			t.Fatalf("Discord RSS embed missing %s=%q: %#v", want.label, want.value, embed.Fields)
		}
	}
}

func TestFormatRSSEmbedUsesFeedURLAsFallbackAction(t *testing.T) {
	entry := rss.Entry{
		Title:     "Feed-only advisory",
		FeedTitle: "Vendor Feed",
		FeedURL:   "https://example.com/feed.xml",
	}
	formatCfg := &notifyfmt.FormatOptions{
		RSS:     notifyfmt.RSSFormatOptions{FieldOrder: []string{"title", "link", "feed_url"}},
		Discord: notifyfmt.DiscordFormatOptions{ShowIcons: false},
	}

	embed := formatRSSEmbed(entry, config.FeedTypeGeneral, formatCfg)

	if embed.URL != "" {
		t.Fatalf("Discord RSS fallback URL = %q, want empty (titles are never links)", embed.URL)
	}
	if !containsDiscordField(embed.Fields, "Link") || !containsDiscordFieldValue(embed.Fields, entry.FeedURL) {
		t.Fatalf("Discord RSS embed missing Link=%q fallback field: %#v", entry.FeedURL, embed.Fields)
	}
}

func TestFormatRSSEmbedOmitsRSSAuthorByDefault(t *testing.T) {
	entry := rss.Entry{
		Title:  "Privacy preserving RSS item",
		Author: "Security Reporter",
	}
	formatCfg := &notifyfmt.FormatOptions{
		RSS: notifyfmt.RSSFormatOptions{
			FieldOrder: []string{"title", "author"},
		},
	}

	embed := formatRSSEmbed(entry, config.FeedTypeGeneral, formatCfg)

	if embed.Author != nil {
		t.Fatalf("RSS author = %#v, want omitted unless rss.show_author is true", embed.Author)
	}
}

func TestFormatRSSEmbedUsesConfiguredFeedTypeColors(t *testing.T) {
	formatCfg := notifyfmt.DefaultFormatOptions()
	formatCfg.Discord.RSSColor = "#123456"
	formatCfg.Discord.GovernmentColor = "#abcdef"
	formatCfg.Discord.RansomwareColor = "#654321"
	entry := rss.Entry{Title: "RSS item"}

	tests := []struct {
		feedType string
		want     int
	}{
		{feedType: config.FeedTypeGeneral, want: 0x123456},
		{feedType: config.FeedTypeGovernment, want: 0xabcdef},
		{feedType: config.FeedTypeRansomware, want: 0x654321},
	}
	for _, tt := range tests {
		embed := formatRSSEmbed(entry, tt.feedType, &formatCfg)
		if embed.Color == nil || *embed.Color != tt.want {
			t.Fatalf("RSS embed color for %s = %#v, want %#x", tt.feedType, embed.Color, tt.want)
		}
	}
}

func TestFormatRansomwareEmbedCanHideDiscordIcons(t *testing.T) {
	formatCfg := notifyfmt.DefaultFormatOptions()
	formatCfg.Discord.ShowIcons = false
	formatCfg.FieldOrder = []string{"group", "victim", "post_url"}
	entry := api.RansomwareEntry{
		Group:    "LockBit",
		Victim:   "Example Corp",
		ClaimURL: "https://example.onion/post",
	}

	embed := formatRansomwareEmbed(entry, &formatCfg)

	if strings.Contains(embed.Title, "🚨") {
		t.Fatalf("embed title still contains icon: %q", embed.Title)
	}
	for _, field := range embed.Fields {
		if strings.ContainsAny(field.Name, "🚨🌍🎯💀📊⚔️🔍📣🔗🌐📝📸") {
			t.Fatalf("field name still contains icon: %q", field.Name)
		}
	}
}

func TestFormatRansomwareEmbedUsesConfiguredDescriptionLimit(t *testing.T) {
	formatCfg := notifyfmt.DefaultFormatOptions()
	formatCfg.Discord.DescriptionMaxChars = 120
	formatCfg.FieldOrder = []string{"description", "post_url"}
	entry := api.RansomwareEntry{
		Description: strings.Repeat("Detailed ransomware incident context. ", 20),
		ClaimURL:    "https://example.onion/post",
	}

	embed := formatRansomwareEmbed(entry, &formatCfg)

	if !containsDiscordField(embed.Fields, "Description") {
		t.Fatalf("expected description field in %#v", embed.Fields)
	}
	for _, field := range embed.Fields {
		if strings.Contains(field.Name, "Description") && len([]rune(field.Value)) > 120 {
			t.Fatalf("description field length = %d, want <= 120", len([]rune(field.Value)))
		}
	}
}

func TestFormatRansomwareEmbedDescriptionUsesSentenceSummary(t *testing.T) {
	entry := api.RansomwareEntry{
		Description: "First sentence. Second sentence. Third sentence should not be cut in the middle of a word.",
	}
	formatCfg := notifyfmt.DefaultFormatOptions()
	formatCfg.Discord.DescriptionMaxChars = 72
	formatCfg.FieldOrder = []string{"description"}

	embed := formatRansomwareEmbed(entry, &formatCfg)

	if len(embed.Fields) != 1 {
		t.Fatalf("expected one description field, got %#v", embed.Fields)
	}
	value := embed.Fields[0].Value
	if !strings.Contains(value, "First sentence. Second sentence... [truncated]") {
		t.Fatalf("description field = %q, want sentence summary marker", value)
	}
	if strings.Contains(value, "Third sentence") {
		t.Fatalf("description field kept next sentence after truncation: %q", value)
	}
	if len([]rune(value)) > 72 {
		t.Fatalf("description field length = %d, want <= 72", len([]rune(value)))
	}
}

func TestFormatRSSEmbedSummarizesLongCategoryLists(t *testing.T) {
	entry := rss.Entry{
		Title:      "Category-heavy feed item",
		Published:  time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Categories: []string{"malware", "ransomware", "advisory", "patch", "ioc", "intel", "weekly"},
	}

	embed := formatRSSEmbed(entry, config.FeedTypeGeneral, nil)

	if !containsDiscordFieldValue(embed.Fields, "malware, ransomware, advisory, patch, ioc (+2 more)") {
		t.Fatalf("expected summarized category list, got fields: %#v", embed.Fields)
	}
}

func TestFormatRSSEmbedSummarizesLongDescription(t *testing.T) {
	entry := rss.Entry{
		Title:       "Long RSS Description",
		Link:        "https://example.com/full-article",
		Description: strings.Repeat("This feed item contains detailed vulnerability context and remediation notes. ", 20),
	}

	embed := formatRSSEmbed(entry, config.FeedTypeGeneral, nil)

	if len([]rune(embed.Description)) > discordRSSSummaryLimit {
		t.Fatalf("RSS description length = %d, want <= %d", len([]rune(embed.Description)), discordRSSSummaryLimit)
	}
	if !strings.Contains(embed.Description, "...") {
		t.Fatalf("RSS summary lacks truncation cue: %q", embed.Description)
	}
	if embed.URL != "" {
		t.Fatalf("embed URL = %q, want empty (titles are never links)", embed.URL)
	}
	if !containsDiscordFieldValue(embed.Fields, entry.Link) {
		t.Fatalf("embed fields = %#v, want full article link %q", embed.Fields, entry.Link)
	}
}

func TestFormatRSSEmbedDescriptionUsesSentenceSummary(t *testing.T) {
	entry := rss.Entry{
		Title:       "Vendor advisory",
		Description: "First sentence. Second sentence. Third sentence should not be cut in the middle of a word.",
	}
	formatCfg := notifyfmt.DefaultFormatOptions()
	formatCfg.RSS.DescriptionMaxChars = 72

	embed := formatRSSEmbed(entry, config.FeedTypeGeneral, &formatCfg)

	if !strings.Contains(embed.Description, "First sentence. Second sentence... [truncated]") {
		t.Fatalf("RSS description = %q, want sentence summary marker", embed.Description)
	}
	if strings.Contains(embed.Description, "Third sentence") {
		t.Fatalf("RSS description kept next sentence after truncation: %q", embed.Description)
	}
	if len([]rune(embed.Description)) > 72 {
		t.Fatalf("RSS description length = %d, want <= 72", len([]rune(embed.Description)))
	}
}

func TestFormatRSSEmbedShowsEmptyMetadataPlaceholders(t *testing.T) {
	formatCfg := &notifyfmt.FormatOptions{
		ShowEmptyFields: true,
		EmptyFieldText:  "No data",
		RSS:             notifyfmt.RSSFormatOptions{ShowAuthor: true},
	}
	entry := rss.Entry{
		Title:     "Sparse RSS item",
		Published: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}

	embed := formatRSSEmbed(entry, config.FeedTypeGeneral, formatCfg)

	if embed.Description != "No data" {
		t.Fatalf("RSS description placeholder = %q", embed.Description)
	}
	for _, name := range []string{"Link", "Categories"} {
		if !containsDiscordField(embed.Fields, name) {
			t.Fatalf("expected RSS placeholder field %q in fields: %#v", name, embed.Fields)
		}
	}
	if got := countDiscordFieldValue(embed.Fields, "No data"); got < 2 {
		t.Fatalf("RSS placeholder field count = %d, want at least 2: %#v", got, embed.Fields)
	}
	if embed.Author == nil || embed.Author.Name != "No data" {
		t.Fatalf("RSS author placeholder = %#v", embed.Author)
	}
}

func TestFormatRSSEmbedOmitsZeroPublishedWhenEmptyFieldsHidden(t *testing.T) {
	entry := rss.Entry{Title: "Undated RSS item"}

	embed := formatRSSEmbed(entry, config.FeedTypeGeneral, &notifyfmt.FormatOptions{ShowEmptyFields: false})

	if embed.Timestamp != "" {
		t.Fatalf("RSS embed timestamp = %q, want empty for zero published", embed.Timestamp)
	}
	if containsDiscordField(embed.Fields, "Published") {
		t.Fatalf("zero published field should be omitted, got fields: %#v", embed.Fields)
	}
}

func TestFormatRSSEmbedUsesDisplayTitleForUntitledItems(t *testing.T) {
	entry := rss.Entry{
		FeedTitle: "Vendor Feed",
		Link:      "https://example.com/advisory",
	}

	embed := formatRSSEmbed(entry, config.FeedTypeGeneral, &notifyfmt.FormatOptions{})

	if !strings.Contains(embed.Title, "Vendor Feed") {
		t.Fatalf("RSS embed title = %q, want feed title fallback", embed.Title)
	}
}

func TestFormatRSSEmbedRespectsRSSFieldOrder(t *testing.T) {
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
			FieldOrder: []string{"title", "published", "categories", "feed_url"},
		},
	}

	embed := formatRSSEmbed(entry, config.FeedTypeGeneral, formatCfg)

	if embed.Description != "" {
		t.Fatalf("RSS description = %q, want omitted", embed.Description)
	}
	if embed.Author != nil {
		t.Fatalf("RSS author = %#v, want omitted", embed.Author)
	}
	if embed.Footer != nil && embed.Footer.Text != "" {
		t.Fatalf("RSS footer = %#v, want feed title omitted", embed.Footer)
	}
	if embed.URL != "" || containsDiscordField(embed.Fields, "Link") {
		t.Fatalf("RSS link should be omitted when link field is absent: url=%q fields=%#v", embed.URL, embed.Fields)
	}

	publishedIndex := discordFieldIndex(embed.Fields, "Published")
	categoriesIndex := discordFieldIndex(embed.Fields, "Categories")
	feedURLIndex := discordFieldIndex(embed.Fields, "Feed URL")
	if publishedIndex == -1 || categoriesIndex == -1 || feedURLIndex == -1 {
		t.Fatalf("expected published, categories, and feed URL fields: %#v", embed.Fields)
	}
	if publishedIndex >= categoriesIndex || categoriesIndex >= feedURLIndex {
		t.Fatalf("RSS fields out of order: published=%d categories=%d feedURL=%d fields=%#v", publishedIndex, categoriesIndex, feedURLIndex, embed.Fields)
	}
}

func containsDiscordField(fields []*MessageEmbedField, namePart string) bool {
	for _, field := range fields {
		if strings.Contains(field.Name, namePart) {
			return true
		}
	}
	return false
}

func containsDiscordFieldValue(fields []*MessageEmbedField, valuePart string) bool {
	for _, field := range fields {
		if strings.Contains(field.Value, valuePart) {
			return true
		}
	}
	return false
}

func discordFieldIndex(fields []*MessageEmbedField, namePart string) int {
	for i, field := range fields {
		if strings.Contains(field.Name, namePart) {
			return i
		}
	}
	return -1
}

func countDiscordFieldValue(fields []*MessageEmbedField, valuePart string) int {
	count := 0
	for _, field := range fields {
		if strings.Contains(field.Value, valuePart) {
			count++
		}
	}
	return count
}

// isBidiControlRune matches the Unicode bidi scope controls that
// textutil.StripBidiControls removes: the isolate initiators/terminator
// U+2066-U+2069 and the embedding/override controls U+202A-U+202E.
func isBidiControlRune(r rune) bool {
	return (r >= '\u2066' && r <= '\u2069') || (r >= '\u202A' && r <= '\u202E')
}

// assertBalancedIsolate pins the contract of the Discord text helpers: a
// rendered string carries either no bidi control at all or exactly the isolate
// pair this package adds -- an initiator as the first rune and U+2069 as the
// last -- and never an embedding/override control.
// Defined per package on purpose; internal/slack has its own copy.
func assertBalancedIsolate(t *testing.T, name, value string) {
	t.Helper()
	runes := []rune(value)
	positions := make([]int, 0, 4)
	for i, r := range runes {
		if r >= '\u202A' && r <= '\u202E' {
			t.Fatalf("%s: %q carries embedding/override control %U at rune index %d", name, value, r, i)
		}
		if isBidiControlRune(r) {
			positions = append(positions, i)
		}
	}
	switch len(positions) {
	case 0:
		return
	case 2:
	default:
		t.Fatalf("%s: %q carries %d bidi controls, want 0 or 2", name, value, len(positions))
	}
	if positions[0] != 0 {
		t.Fatalf("%s: %q opens its isolate at rune index %d, want 0", name, value, positions[0])
	}
	if positions[1] != len(runes)-1 {
		t.Fatalf("%s: %q closes its isolate at rune index %d, want %d", name, value, positions[1], len(runes)-1)
	}
	if opener := runes[0]; opener != '\u2066' && opener != '\u2068' {
		t.Fatalf("%s: %q opens with %U, want U+2066 or U+2068", name, value, opener)
	}
	if closer := runes[len(runes)-1]; closer != '\u2069' {
		t.Fatalf("%s: %q closes with %U, want U+2069", name, value, closer)
	}
}

func TestFormatRSSEmbedStripsBidiControlsFromUntrustedText(t *testing.T) {
	entry := rss.Entry{
		Title:       "Acme \u2067Corp breach",
		Description: "leak of \u202Efdp.exe and more",
		Link:        "https://e.test/\u2066a",
		Author:      strings.Repeat("A", 300) + "\u2067" + strings.Repeat("B", 300),
		Published:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		FeedTitle:   "Feed \u2068X",
		Categories:  []string{"sec\u202Durity"},
	}

	embed := formatRSSEmbed(entry, config.FeedTypeGeneral, &notifyfmt.FormatOptions{
		RSS: notifyfmt.RSSFormatOptions{ShowAuthor: true},
	})

	assertBalancedIsolate(t, "embed.Title", embed.Title)
	assertBalancedIsolate(t, "embed.Description", embed.Description)
	if embed.Author == nil {
		t.Fatal("expected an author block")
	}
	assertBalancedIsolate(t, "embed.Author.Name", embed.Author.Name)
	if embed.Footer == nil {
		t.Fatal("expected a footer block")
	}
	assertBalancedIsolate(t, "embed.Footer.Text", embed.Footer.Text)
	for _, field := range embed.Fields {
		assertBalancedIsolate(t, "field "+field.Name, field.Value)
	}
	// Titles are never links: the bidi-bearing Link is
	// now a real field, which the isolate-balance loop above walks
	// automatically. Pin its presence explicitly so a mutation dropping the
	// field cannot pass this test simply because there is nothing left to
	// walk.
	if !containsDiscordField(embed.Fields, "Link") {
		t.Fatal("expected a Link field carrying the bidi-bearing article link")
	}

	if !strings.Contains(embed.Title, "Acme Corp breach") {
		t.Fatalf("embed title lost its visible characters: %q", embed.Title)
	}
	if !strings.Contains(embed.Description, "leak of fdp.exe and more") {
		t.Fatalf("embed description lost its visible characters: %q", embed.Description)
	}

	if got := len([]rune(embed.Title)); got > 256 {
		t.Fatalf("title length = %d, want <= 256", got)
	}
	if got := len([]rune(embed.Description)); got > 2048 {
		t.Fatalf("description length = %d, want <= 2048", got)
	}
	if got := len([]rune(embed.Author.Name)); got > 256 {
		t.Fatalf("author name length = %d, want <= 256", got)
	}
	if got := len([]rune(embed.Footer.Text)); got > 2048 {
		t.Fatalf("footer text length = %d, want <= 2048", got)
	}
	for _, field := range embed.Fields {
		if got := len([]rune(field.Value)); got > 1024 {
			t.Fatalf("field %q value length = %d, want <= 1024", field.Name, got)
		}
	}
}
