package textutil

import (
	"errors"
	"net/url"
	"strings"
	"testing"
)

func TestRedactWebhookSecrets(t *testing.T) {
	slackURL := "https://hooks.slack.com/services/T12345678/B12345678/abcdefghijklmnopqrstuvwxyz012345" //nolint:gosec // G101: test fixture, not a real credential
	discordURL := "https://discord.com/api/v9/webhooks/123456789012345678/abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_"
	legacyDiscordURL := "https://discordapp.com/api/webhooks/123456789012345678/abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_"

	input := "slack=" + slackURL + " discord=" + discordURL + " legacy=" + legacyDiscordURL

	got := RedactWebhookSecrets(input)

	for _, secret := range []string{slackURL, discordURL, legacyDiscordURL} {
		if strings.Contains(got, secret) {
			t.Fatalf("redacted text still contains webhook secret %q: %s", secret, got)
		}
	}

	for _, marker := range []string{
		"https://hooks.slack.com/services/[redacted]",
		"https://discord.com/api/webhooks/[redacted]",
		"https://discordapp.com/api/webhooks/[redacted]",
	} {
		if !strings.Contains(got, marker) {
			t.Fatalf("redacted text missing marker %q: %s", marker, got)
		}
	}
}

func TestDefangURL(t *testing.T) {
	tests := map[string]string{
		"https://example.test/path": "hxxps://example.test/path",
		"http://example.test/path":  "hxxp://example.test/path",
		"HTTPS://example.test/path": "hxxps://example.test/path",
		"HTTP://example.test/path":  "hxxp://example.test/path",
		"ftp://example.test/path":   "ftp://example.test/path",
		"":                          "",
	}

	for input, want := range tests {
		if got := DefangURL(input); got != want {
			t.Fatalf("DefangURL(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestRedactURLCredentials(t *testing.T) {
	input := "fetch https://user:pass@example.test/feed.xml?token=secret&category=news and https://example.test/path?api_key=abc" //nolint:gosec // G101: test fixture, not a real credential

	got := RedactURLCredentials(input)

	for _, leaked := range []string{"user:pass", "token=secret", "api_key=abc"} {
		if strings.Contains(got, leaked) {
			t.Fatalf("RedactURLCredentials() leaked %q: %s", leaked, got)
		}
	}
	for _, marker := range []string{"https://%5Bredacted%5D@example.test/feed.xml?category=news&token=%5Bredacted%5D", "api_key=%5Bredacted%5D"} {
		if !strings.Contains(got, marker) {
			t.Fatalf("RedactURLCredentials() missing marker %q: %s", marker, got)
		}
	}
}

func TestBidiIsolate(t *testing.T) {
	got := BidiIsolate("שלום Example")
	want := "\u2068שלום Example\u2069"
	if got != want {
		t.Fatalf("BidiIsolate() = %q, want %q", got, want)
	}

	if got := BidiIsolate(""); got != "" {
		t.Fatalf("BidiIsolate(empty) = %q, want empty", got)
	}
}

func TestBidiIsolateLTR(t *testing.T) {
	got := BidiIsolateLTR("https://example.test/שלום")
	want := "\u2066https://example.test/שלום\u2069"
	if got != want {
		t.Fatalf("BidiIsolateLTR() = %q, want %q", got, want)
	}
}

func TestTruncateMiddle(t *testing.T) {
	got := TruncateMiddle("abcdefghijklmnopqrstuvwxyz", 12)
	if got != "abcd...vwxyz" {
		t.Fatalf("TruncateMiddle() = %q, want %q", got, "abcd...vwxyz")
	}
	if got := TruncateMiddle("short", 12); got != "short" {
		t.Fatalf("TruncateMiddle(short) = %q, want short", got)
	}
}

func TestTruncateUnicodeWithoutSplittingRunes(t *testing.T) {
	if got := TruncateText("åß∂ƒ©˙∆˚¬", 6); got != "åß∂..." {
		t.Fatalf("TruncateText(unicode) = %q, want %q", got, "åß∂...")
	}
	if got := TruncateMiddle("åß∂ƒ©˙∆˚¬", 7); got != "åß...˚¬" {
		t.Fatalf("TruncateMiddle(unicode) = %q, want %q", got, "åß...˚¬")
	}
	if got := PrefixRunes("åß∂", 2); got != "åß" {
		t.Fatalf("PrefixRunes() = %q, want åß", got)
	}
	if got := SuffixRunes("åß∂", 2); got != "ß∂" {
		t.Fatalf("SuffixRunes() = %q, want ß∂", got)
	}
}

func TestTruncateDescriptionUsesSentenceBoundaryAndExplicitMarker(t *testing.T) {
	text := "First sentence. Second sentence. Third sentence continues with details that exceed the limit."

	got := TruncateDescription(text, 58)

	want := "First sentence. Second sentence... [truncated]"
	if got != want {
		t.Fatalf("TruncateDescription() = %q, want %q", got, want)
	}
	if len([]rune(got)) > 58 {
		t.Fatalf("TruncateDescription() length = %d, want <= 58", len([]rune(got)))
	}
}

func TestTruncateDescriptionFallsBackWithoutSentenceBoundary(t *testing.T) {
	text := strings.Repeat("a", 100)

	got := TruncateDescription(text, 32)

	if !strings.HasSuffix(got, "... [truncated]") {
		t.Fatalf("TruncateDescription() = %q, want explicit marker", got)
	}
	if len([]rune(got)) > 32 {
		t.Fatalf("TruncateDescription() length = %d, want <= 32", len([]rune(got)))
	}
}

func TestTruncateDescriptionKeepsShortText(t *testing.T) {
	if got := TruncateDescription("Short description.", 80); got != "Short description." {
		t.Fatalf("TruncateDescription(short) = %q", got)
	}
}

func TestStripHTMLUsesStandardEntityDecoding(t *testing.T) {
	got := StripHTML("<p>Tom&apos;s &#x26; Jerry&nbsp; &ndash; alert</p>")
	want := "Tom's & Jerry – alert"
	if got != want {
		t.Fatalf("StripHTML() = %q, want %q", got, want)
	}
}

func TestStripHTMLDecodesNestedMalformedFeedEntities(t *testing.T) {
	input := "YARA-X&&#x23&#x3b;x26&#x3b;&#x23&#x3b;39&#x3b;s 1.18.0 release brings 3 improvements and 2 bugfixes.&#xd;"

	got := StripHTML(input)

	want := "YARA-X's 1.18.0 release brings 3 improvements and 2 bugfixes."
	if got != want {
		t.Fatalf("StripHTML() = %q, want %q", got, want)
	}
}

func TestFormatCategorySummary(t *testing.T) {
	got := FormatCategorySummary([]string{"malware", "ransomware", "advisory", "patch", "ioc", "intel"}, 3, 80)
	if got != "malware, ransomware, advisory (+3 more)" {
		t.Fatalf("FormatCategorySummary() = %q", got)
	}

	got = FormatCategorySummary([]string{"", "  malware  ", "ransomware"}, 5, 80)
	if got != "malware, ransomware" {
		t.Fatalf("FormatCategorySummary() should trim and drop blanks, got %q", got)
	}

	got = FormatCategorySummary([]string{"verylongcategory", "second", "third", "fourth"}, 3, 24)
	if len([]rune(got)) > 24 {
		t.Fatalf("FormatCategorySummary() length = %d, want <= 24: %q", len([]rune(got)), got)
	}
	if !strings.Contains(got, "+") {
		t.Fatalf("FormatCategorySummary() should keep omitted count when truncated, got %q", got)
	}
}

func TestFormatTimestampIncludesUTCForParsedTimestamps(t *testing.T) {
	tests := map[string]string{
		"2026-02-03 04:05:06.123456": "2026-02-03 04:05:06 UTC",
		"2026-02-03 04:05:06":        "2026-02-03 04:05:06 UTC",
		"2026-02-03T05:05:06+01:00":  "2026-02-03 04:05:06 UTC",
	}

	for input, want := range tests {
		if got := FormatTimestamp(input); got != want {
			t.Fatalf("FormatTimestamp(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestFormatTimestampPassesThroughUnparsedInput(t *testing.T) {
	if got := FormatTimestamp(""); got != "" {
		t.Fatalf("FormatTimestamp(empty) = %q, want empty", got)
	}
	if got := FormatTimestamp("not a timestamp"); got != "not a timestamp" {
		t.Fatalf("FormatTimestamp(invalid) = %q, want input echoed back", got)
	}
}

func TestStripHTMLEmptyInput(t *testing.T) {
	if got := StripHTML(""); got != "" {
		t.Fatalf("StripHTML(empty) = %q, want empty", got)
	}
}

// TestStripHTMLRemovesInvisibleFormattingCharacters covers the invisible
// characters StripHTML actually removes or converts: ZERO WIDTH SPACE
// (U+200B) and the BOM / ZERO WIDTH NO-BREAK SPACE (U+FEFF) are deleted
// outright, and the Unicode line and paragraph separators (U+2028/U+2029)
// are converted to a plain space. Routing the result through BidiIsolate
// does not help here -- BidiIsolate only strips bidi-control characters
// (U+2066-U+2069, U+202A-U+202E), not these.
func TestStripHTMLRemovesInvisibleFormattingCharacters(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"zero width space", "evil\u200bfile.exe", "evilfile.exe"},
		{"BOM / zero width no-break space", "evil\ufefffile.exe", "evilfile.exe"},
		{"line separator becomes a space, like an ordinary newline", "first\u2028second", "first second"},
		{"paragraph separator becomes a space, like an ordinary newline", "first\u2029second", "first second"},
		{"entity-encoded zero width space is also removed", "evil&#x200B;file.exe", "evilfile.exe"},
		{"unaffected plain text is untouched", "ordinary text stays exactly as-is", "ordinary text stays exactly as-is"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := StripHTML(tt.in); got != tt.want {
				t.Errorf("StripHTML(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestStripHTMLKeepsZeroWidthJoinersThatShapeContent guards the 2026-09-04
// fix: ZERO WIDTH NON-JOINER (U+200C) and ZERO WIDTH JOINER (U+200D) render
// as nothing themselves, but they change how their *neighbouring* characters
// render -- that is their entire purpose -- so deleting them corrupts
// legitimate content instead of merely removing an invisible mark.
// \u0628\u0627\u062c\u200c\u0627\u0641\u0632\u0627\u0631 (with a ZWNJ between \u0628\u0627\u062c and \u0627\u0641\u0632\u0627\u0631) is the Persian word for
// "ransomware", the subject noun of exactly the headlines this bot relays;
// deleting the ZWNJ fuses it into the wrong word "\u0628\u0627\u062c\u0627\u0641\u0632\u0627\u0631". The same class
// of damage hits Hindi/Tamil conjuncts and every ZWJ emoji sequence -- a
// pirate flag or a technologist emoji falls apart into two unrelated glyphs
// without its ZWJ.
func TestStripHTMLKeepsZeroWidthJoinersThatShapeContent(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"zero width non-joiner survives", "evil\u200cfile.exe", "evil\u200cfile.exe"},
		{"zero width joiner survives", "evil\u200dfile.exe", "evil\u200dfile.exe"},
		{
			"Persian ransomware word keeps its non-joiner intact",
			"\u06af\u0631\u0648\u0647 \u0628\u0627\u062c\u200c\u0627\u0641\u0632\u0627\u0631",
			"\u06af\u0631\u0648\u0647 \u0628\u0627\u062c\u200c\u0627\u0641\u0632\u0627\u0631",
		},
		{
			"pirate flag ZWJ emoji sequence stays one glyph",
			"<p>\U0001F3F4\u200d\u2620\ufe0f alert</p>",
			"\U0001F3F4\u200d\u2620\ufe0f alert",
		},
		{
			"technologist ZWJ emoji sequence stays one glyph",
			"<p>\U0001F468\u200d\U0001F4BB alert</p>",
			"\U0001F468\u200d\U0001F4BB alert",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := StripHTML(tt.in); got != tt.want {
				t.Errorf("StripHTML(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestStripHTMLCollapsesGapsFromInvisibleFormattingRemoval pins the F2
// placement decision: stripInvisibleFormatting runs BEFORE the
// whitespace-collapse pass so the space U+2028/U+2029 convert into, and the
// gap a deleted U+200B/U+FEFF leaves behind, are cleaned up by that same
// pass rather than surviving as extra whitespace. Moving the call after the
// whitespace-collapse pass would leave "first \u2028 second" with three
// spaces and "first \u200b second" with two.
func TestStripHTMLCollapsesGapsFromInvisibleFormattingRemoval(t *testing.T) {
	if got := StripHTML("first \u2028 second"); got != "first second" {
		t.Fatalf("StripHTML(line separator with surrounding spaces) = %q, want %q", got, "first second")
	}
	if got := StripHTML("first \u200b second"); got != "first second" {
		t.Fatalf("StripHTML(zero width space with surrounding spaces) = %q, want %q", got, "first second")
	}
}

// TestStripHTMLLineSeparatorMatchesOrdinaryNewlineHandling documents why
// U+2028/U+2029 are converted to a space rather than deleted outright: an
// ordinary ASCII newline in the same position is already collapsed to a
// single space by StripHTML's existing whitespace pass, and the Unicode line
// and paragraph separators are line breaks, not invisible marks, so they get
// the same treatment for consistency.
func TestStripHTMLLineSeparatorMatchesOrdinaryNewlineHandling(t *testing.T) {
	newline := StripHTML("first\nsecond")
	lineSeparator := StripHTML("first\u2028second")
	paragraphSeparator := StripHTML("first\u2029second")

	if newline != lineSeparator || newline != paragraphSeparator {
		t.Fatalf("StripHTML newline=%q lineSeparator=%q paragraphSeparator=%q, want all equal",
			newline, lineSeparator, paragraphSeparator)
	}
}

func TestDecodeHTMLEntitiesStopsAfterFiveRounds(t *testing.T) {
	// Six encoding levels would need six decode rounds; the loop stops after
	// five and returns the partially decoded text.
	if got := decodeHTMLEntities("&amp;amp;amp;amp;amp;lt;"); got != "&lt;" {
		t.Fatalf("decodeHTMLEntities() = %q, want %q", got, "&lt;")
	}
}

func TestTruncateTextEdgeCases(t *testing.T) {
	tests := []struct {
		name      string
		text      string
		maxLength int
		want      string
	}{
		{name: "zero limit", text: "abc", maxLength: 0, want: ""},
		{name: "negative limit", text: "abc", maxLength: -1, want: ""},
		{name: "fits within limit", text: "ab", maxLength: 10, want: "ab"},
		{name: "limit of three keeps prefix without ellipsis", text: "abcdef", maxLength: 3, want: "abc"},
		{name: "limit below three keeps prefix", text: "abcdef", maxLength: 2, want: "ab"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := TruncateText(tt.text, tt.maxLength); got != tt.want {
				t.Fatalf("TruncateText(%q, %d) = %q, want %q", tt.text, tt.maxLength, got, tt.want)
			}
		})
	}
}

func TestTruncateDescriptionEdgeCases(t *testing.T) {
	if got := TruncateDescription("anything", 0); got != "" {
		t.Fatalf("TruncateDescription(zero limit) = %q, want empty", got)
	}
	// Limits at or below the marker length keep a plain prefix without marker.
	if got := TruncateDescription(strings.Repeat("a", 30), 10); got != strings.Repeat("a", 10) {
		t.Fatalf("TruncateDescription(tiny limit) = %q, want plain prefix", got)
	}
}

func TestTruncateMiddleEdgeCases(t *testing.T) {
	if got := TruncateMiddle("abcdef", 0); got != "" {
		t.Fatalf("TruncateMiddle(zero limit) = %q, want empty", got)
	}
	if got := TruncateMiddle("abcdef", 3); got != "abc" {
		t.Fatalf("TruncateMiddle(limit 3) = %q, want %q", got, "abc")
	}
}

func TestExceedsRuneLimitNegativeLimit(t *testing.T) {
	if !ExceedsRuneLimit("a", -1) {
		t.Fatal("ExceedsRuneLimit(non-empty, negative) = false, want true")
	}
	if ExceedsRuneLimit("", -1) {
		t.Fatal("ExceedsRuneLimit(empty, negative) = true, want false")
	}
}

func TestPrefixAndSuffixRunesEdgeCases(t *testing.T) {
	if got := PrefixRunes("ab", 0); got != "" {
		t.Fatalf("PrefixRunes(zero limit) = %q, want empty", got)
	}
	if got := PrefixRunes("ab", 5); got != "ab" {
		t.Fatalf("PrefixRunes(limit above length) = %q, want full text", got)
	}
	if got := SuffixRunes("ab", 0); got != "" {
		t.Fatalf("SuffixRunes(zero limit) = %q, want empty", got)
	}
	if got := SuffixRunes("ab", 5); got != "ab" {
		t.Fatalf("SuffixRunes(limit above length) = %q, want full text", got)
	}
}

func TestFormatCategorySummaryEdgeCases(t *testing.T) {
	if got := FormatCategorySummary([]string{"a"}, 0, 10); got != "" {
		t.Fatalf("FormatCategorySummary(zero maxShown) = %q, want empty", got)
	}
	if got := FormatCategorySummary([]string{"a"}, 3, 0); got != "" {
		t.Fatalf("FormatCategorySummary(zero maxLength) = %q, want empty", got)
	}
	if got := FormatCategorySummary(nil, 3, 10); got != "" {
		t.Fatalf("FormatCategorySummary(nil) = %q, want empty", got)
	}
	if got := FormatCategorySummary([]string{"", "   "}, 3, 10); got != "" {
		t.Fatalf("FormatCategorySummary(blank entries) = %q, want empty", got)
	}
}

func TestFormatCategorySummaryCollapsesBodyWhenSuffixExceedsLimit(t *testing.T) {
	categories := make([]string, 100)
	for i := range categories {
		categories[i] = "category"
	}

	got := FormatCategorySummary(categories, 1, 5)

	// The omitted-count suffix alone exceeds maxLength, so the body budget
	// collapses to zero and only a truncated suffix remains.
	if got != " (..." {
		t.Fatalf("FormatCategorySummary() = %q, want %q", got, " (...")
	}
	if len([]rune(got)) > 5 {
		t.Fatalf("FormatCategorySummary() length = %d, want <= 5", len([]rune(got)))
	}
}

func TestBidiIsolateLTREmptyInput(t *testing.T) {
	if got := BidiIsolateLTR(""); got != "" {
		t.Fatalf("BidiIsolateLTR(empty) = %q, want empty", got)
	}
}

func TestRedactWebhookSecretsEmptyInput(t *testing.T) {
	if got := RedactWebhookSecrets(""); got != "" {
		t.Fatalf("RedactWebhookSecrets(empty) = %q, want empty", got)
	}
}

func TestRedactURLCredentialsEdgeCases(t *testing.T) {
	if got := RedactURLCredentials(""); got != "" {
		t.Fatalf("RedactURLCredentials(empty) = %q, want empty", got)
	}

	unparsable := "see https://%zz for details"
	wantUnparsable := "see [redacted URL] for details"
	if got := RedactURLCredentials(unparsable); got != wantUnparsable {
		t.Fatalf("RedactURLCredentials(unparsable URL) = %q, want %q", got, wantUnparsable)
	}

	plain := "see https://example.test/plain?category=news for details"
	if got := RedactURLCredentials(plain); got != plain {
		t.Fatalf("RedactURLCredentials(no secrets) = %q, want unchanged input", got)
	}
}

func TestRedactWebhookSecretsForURL(t *testing.T) {
	tests := []struct {
		name       string
		text       string
		webhookURL string
		want       string
	}{
		{
			name:       "services path on slack-compatible host",
			text:       "post to https://chat.example.test/services/T000/B000/secretvalue failed",
			webhookURL: "https://chat.example.test/services/T000/B000/secretvalue",
			want:       "post to https://chat.example.test/services/[redacted] failed",
		},
		{
			name:       "api webhooks path on custom host",
			text:       "post to https://chat.example.test/api/webhooks/123/secretvalue failed",
			webhookURL: "https://chat.example.test/api/webhooks/123/secretvalue",
			want:       "post to https://chat.example.test/api/webhooks/[redacted] failed",
		},
		{
			name:       "other path collapses to generic marker",
			text:       "post to https://chat.example.test/hooks/secretvalue failed",
			webhookURL: "https://chat.example.test/hooks/secretvalue",
			want:       "post to https://chat.example.test/[redacted] failed",
		},
		{
			name:       "empty text stays empty",
			text:       "",
			webhookURL: "https://chat.example.test/hooks/secretvalue",
			want:       "",
		},
		{
			name:       "blank webhook URL keeps text",
			text:       "no webhook here",
			webhookURL: "   ",
			want:       "no webhook here",
		},
		{
			name:       "webhook URL without scheme keeps text",
			text:       "no webhook here",
			webhookURL: "chat.example.test/hooks/secretvalue",
			want:       "no webhook here",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RedactWebhookSecretsForURL(tt.text, tt.webhookURL); got != tt.want {
				t.Fatalf("RedactWebhookSecretsForURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRedactWebhookSecretsForURLRedactsEscapedPathVariant(t *testing.T) {
	webhookURL := "https://chat.example.test/hooks/a%2Fb?x=1"
	text := "full https://chat.example.test/hooks/a%2Fb?x=1 raw https://chat.example.test/hooks/a%2Fb end"

	got := RedactWebhookSecretsForURL(text, webhookURL)

	want := "full https://chat.example.test/[redacted] raw https://chat.example.test/[redacted] end"
	if got != want {
		t.Fatalf("RedactWebhookSecretsForURL() = %q, want %q", got, want)
	}
}

func TestRedactWebhookError(t *testing.T) {
	if got := RedactWebhookError(nil); got != nil {
		t.Fatalf("RedactWebhookError(nil) = %v, want nil", got)
	}

	err := errors.New("post https://hooks.slack.com/services/T000/B000/secretvalue: timeout")
	got := RedactWebhookError(err)
	if got == nil {
		t.Fatal("RedactWebhookError() = nil, want redacted error")
	}
	if strings.Contains(got.Error(), "secretvalue") {
		t.Fatalf("RedactWebhookError() leaked secret: %s", got)
	}
	if !strings.Contains(got.Error(), "https://hooks.slack.com/services/[redacted]") {
		t.Fatalf("RedactWebhookError() missing redaction marker: %s", got)
	}
}

func TestRedactWebhookErrorForURL(t *testing.T) {
	if got := RedactWebhookErrorForURL(nil, "https://chat.example.test/hooks/secretvalue"); got != nil {
		t.Fatalf("RedactWebhookErrorForURL(nil) = %v, want nil", got)
	}

	err := errors.New("post https://chat.example.test/hooks/secretvalue: timeout")
	got := RedactWebhookErrorForURL(err, "https://chat.example.test/hooks/secretvalue")
	if got == nil {
		t.Fatal("RedactWebhookErrorForURL() = nil, want redacted error")
	}
	want := "post https://chat.example.test/[redacted]: timeout"
	if got.Error() != want {
		t.Fatalf("RedactWebhookErrorForURL() = %q, want %q", got.Error(), want)
	}
}

// isBidiControlTestRune mirrors the production predicate for assertions in this
// file without depending on it, so a mutation of the production set stays visible.
func isBidiControlTestRune(r rune) bool {
	return (r >= '\u2066' && r <= '\u2069') || (r >= '\u202A' && r <= '\u202E')
}

// assertBalancedIsolate checks the contract of BidiIsolate/BidiIsolateLTR: the
// result is either empty or carries exactly two bidi controls, an isolate
// initiator as the first rune and the pop-directional-isolate as the last.
func assertBalancedIsolate(t *testing.T, name, value string) {
	t.Helper()
	runes := []rune(value)
	positions := make([]int, 0, 4)
	for i, r := range runes {
		if isBidiControlTestRune(r) {
			positions = append(positions, i)
		}
	}
	if value == "" {
		if len(positions) != 0 {
			t.Fatalf("%s: empty string must carry no control", name)
		}
		return
	}
	if len(positions) != 2 {
		t.Fatalf("%s: %q carries %d bidi controls, want exactly 2", name, value, len(positions))
	}
	if positions[0] != 0 {
		t.Fatalf("%s: %q first control at rune index %d, want 0", name, value, positions[0])
	}
	if positions[1] != len(runes)-1 {
		t.Fatalf("%s: %q closing control at rune index %d, want %d", name, value, positions[1], len(runes)-1)
	}
	if opener := runes[0]; opener != '\u2066' && opener != '\u2068' {
		t.Fatalf("%s: %q opens with %U, want U+2066 or U+2068", name, value, opener)
	}
	if closer := runes[len(runes)-1]; closer != '\u2069' {
		t.Fatalf("%s: %q closes with %U, want U+2069", name, value, closer)
	}
}

func TestRedactURLCredentialsRedactsSemicolonSeparatedQueries(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "semicolon after sensitive key",
			in:   "https://f.example/rss?token=secret;x=1",
			want: "https://f.example/rss?token=%5Bredacted%5D;x=1",
		},
		{
			name: "sensitive key after semicolon",
			in:   "https://f.example/rss?a=1;token=secret",
			want: "https://f.example/rss?a=1;token=%5Bredacted%5D",
		},
		{
			name: "trailing semicolon",
			in:   "https://f.example/rss?token=secret;",
			want: "https://f.example/rss?token=%5Bredacted%5D",
		},
		{
			name: "url embedded in an error string",
			in:   "Get https://f.example/rss?token=secret;x=1: dial tcp: refused",
			want: "Get https://f.example/rss?token=%5Bredacted%5D;x=1: dial tcp: refused",
		},
		{
			name: "semicolon inside the sensitive value",
			in:   "https://f.example/rss?token=a;b&x=1",
			want: "https://f.example/rss?token=%5Bredacted%5D&x=1",
		},
		{
			name: "percent-escaped sensitive key",
			in:   "https://f.example/rss?%74oken=secret;x=1",
			want: "https://f.example/rss?%74oken=%5Bredacted%5D;x=1",
		},
		{ //nolint:gosec // G101: test fixture, not a real credential
			name: "userinfo and semicolon query together",
			in:   "https://u:p@f.example/rss?token=secret;x=1",
			want: "https://%5Bredacted%5D@f.example/rss?token=%5Bredacted%5D;x=1",
		},
		{
			name: "semicolon in a non-sensitive parameter is preserved",
			in:   "https://f.example/rss?token=t1&other=x;y",
			want: "https://f.example/rss?token=%5Bredacted%5D&other=x;y",
		},
		{
			name: "value containing both semicolon and equals keeps the tail",
			in:   "https://f.example/rss?token=YWJj;ZGV=",
			want: "https://f.example/rss?token=%5Bredacted%5D;ZGV=",
		},
		{
			name: "bad escape and semicolon in the same query",
			in:   "https://f.example/rss?x=%zz;token=secret",
			want: "https://f.example/rss?x=%zz;token=%5Bredacted%5D",
		},
		{
			name: "empty continuation run",
			in:   "https://f.example/rss?token=secret;;x=1",
			want: "https://f.example/rss?token=%5Bredacted%5D;x=1",
		},
		{
			name: "leading empty run",
			in:   "https://f.example/rss?;token=secret",
			want: "https://f.example/rss?;token=%5Bredacted%5D",
		},
		{
			name: "fragment survives",
			in:   "https://f.example/rss?token=secret;x=1#frag",
			want: "https://f.example/rss?token=%5Bredacted%5D;x=1#frag",
		},
		{
			name: "plus decodes to a space and is not sensitive",
			in:   "https://f.example/rss?to+ken=secret;x=1",
			want: "https://f.example/rss?to+ken=secret;x=1",
		},
		{
			name: "trailing plus decodes to a trimmed space",
			in:   "https://f.example/rss?token+=secret;x=1",
			want: "https://f.example/rss?token+=%5Bredacted%5D;x=1",
		},
		{
			name: "unreadable key escape fails closed to no match",
			in:   "https://f.example/rss?%zzkey=secret;x=1",
			want: "https://f.example/rss?%zzkey=secret;x=1",
		},
		{
			// The fallback must match keys case-insensitively, like the
			// ParseQuery path does through feedurl.IsSensitiveQueryKey.
			name: "uppercase sensitive key is matched and its case preserved",
			in:   "https://f.example/rss?TOKEN=secret;x=1",
			want: "https://f.example/rss?TOKEN=%5Bredacted%5D;x=1",
		},
		{
			// The continuation drop must end at the next "key=value" run:
			// "plain" belongs to x=1, not to the redacted token.
			name: "continuation drop stops at the next parameter",
			in:   "https://f.example/rss?token=a;x=1;plain",
			want: "https://f.example/rss?token=%5Bredacted%5D;x=1;plain",
		},
		{
			// A bad escape makes ParseQuery fail without any ";" involved. The
			// fallback must run for that error too: re-encoding ParseQuery's
			// partial result would silently delete "x=%zz".
			name: "bad escape without a semicolon keeps the unreadable parameter",
			in:   "https://f.example/rss?token=secret&x=%zz",
			want: "https://f.example/rss?token=%5Bredacted%5D&x=%zz",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RedactURLCredentials(tt.in)
			if got != tt.want {
				t.Fatalf("RedactURLCredentials(%q) = %q, want %q", tt.in, got, tt.want)
			}
			if strings.Contains(tt.want, "%5Bredacted%5D") && strings.Contains(got, "secret") {
				t.Fatalf("RedactURLCredentials(%q) still contains the secret: %q", tt.in, got)
			}
		})
	}
}

func TestRedactURLCredentialsUnchangedForParseableQueries(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "sensitive key with ampersand separator",
			in:   "https://f.example/rss?token=secret&x=1",
			want: "https://f.example/rss?token=%5Bredacted%5D&x=1",
		},
		{
			name: "encode sorts the keys",
			in:   "https://f.example/rss?b=2&token=secret&a=1",
			want: "https://f.example/rss?a=1&b=2&token=%5Bredacted%5D",
		},
		{
			name: "repeated sensitive key keeps both values",
			in:   "https://f.example/rss?a=1&token=s1&token=s2",
			want: "https://f.example/rss?a=1&token=%5Bredacted%5D&token=%5Bredacted%5D",
		},
		{
			name: "key case is preserved",
			in:   "https://f.example/rss?TOKEN=secret&x=1",
			want: "https://f.example/rss?TOKEN=%5Bredacted%5D&x=1",
		},
		{
			name: "escaped value round-trips",
			in:   "https://f.example/rss?key=%20sp+ace&z=a%2Bb",
			want: "https://f.example/rss?key=%5Bredacted%5D&z=a%2Bb",
		},
		{
			name: "nothing sensitive stays verbatim",
			in:   "https://f.example/rss?x=1&y=2",
			want: "https://f.example/rss?x=1&y=2",
		},
		{
			name: "valueless parameter stays verbatim",
			in:   "https://f.example/rss?empty",
			want: "https://f.example/rss?empty",
		},
		{
			name: "unparsable url gets a generic marker, no host echoed",
			in:   "see https://%zz for details",
			want: "see [redacted URL] for details",
		},
		{
			name: "empty input",
			in:   "",
			want: "",
		},
		{ //nolint:gosec // G101: test fixture, not a real credential
			name: "userinfo with an ampersand query",
			in:   "https://u:p@f.example/rss?token=secret&x=1",
			want: "https://%5Bredacted%5D@f.example/rss?token=%5Bredacted%5D&x=1",
		},
		{
			name: "escaped key is normalised by encode",
			in:   "https://f.example/rss?%74oken=secret",
			want: "https://f.example/rss?token=%5Bredacted%5D",
		},
		{
			name: "escaped space in a value is normalised by encode",
			in:   "https://f.example/rss?token=secret&x=%20y",
			want: "https://f.example/rss?token=%5Bredacted%5D&x=+y",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RedactURLCredentials(tt.in); got != tt.want {
				t.Fatalf("RedactURLCredentials(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestRedactURLCredentialsKeepsNonSensitiveSemicolonQuery(t *testing.T) {
	plain := "https://f.example/rss?a=1;b=2"
	if got := RedactURLCredentials(plain); got != plain {
		t.Fatalf("RedactURLCredentials(%q) = %q, want unchanged input", plain, got)
	}

	withUserinfo := "https://u:p@f.example/rss?a=1;b=2" //nolint:gosec // G101: test fixture, not a real credential
	want := "https://%5Bredacted%5D@f.example/rss?a=1;b=2"
	if got := RedactURLCredentials(withUserinfo); got != want {
		t.Fatalf("RedactURLCredentials(%q) = %q, want %q", withUserinfo, got, want)
	}
}

func TestRedactWebhookSecretsRedactsSemicolonFeedToken(t *testing.T) {
	in := "post failed for https://f.example/rss?api_key=SECRET;v=2"
	want := "post failed for https://f.example/rss?api_key=%5Bredacted%5D;v=2"
	got := RedactWebhookSecrets(in)
	if strings.Contains(got, "SECRET") {
		t.Fatalf("RedactWebhookSecrets(%q) still contains the token: %q", in, got)
	}
	if got != want {
		t.Fatalf("RedactWebhookSecrets(%q) = %q, want %q", in, got, want)
	}
}

func TestBidiIsolateStripsExistingControls(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "unpaired isolate initiator is removed",
			in:   "Acme \u2067Corp",
			want: "\u2068Acme Corp\u2069",
		},
		{
			name: "right-to-left override is removed",
			in:   "evil\u202Etxt.exe",
			want: "\u2068eviltxt.exe\u2069",
		},
		{
			name: "truncated isolate pair is removed",
			in:   "Acme \u2067C...",
			want: "\u2068Acme C...\u2069",
		},
		{
			name: "control-only input still yields the bare isolate pair",
			in:   "\u2066",
			want: "\u2068\u2069",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BidiIsolate(tt.in)
			if got != tt.want {
				t.Fatalf("BidiIsolate(%q) = %q, want %q", tt.in, got, tt.want)
			}
			if tt.in != "" && got == "" {
				t.Fatalf("BidiIsolate(%q) = empty, want a non-empty result", tt.in)
			}
			assertBalancedIsolate(t, "BidiIsolate/"+tt.name, got)
		})
	}
}

func TestBidiIsolateLTRStripsExistingControls(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "url with an embedded isolate initiator",
			in:   "https://e.test/\u2068a",
			want: "\u2066https://e.test/a\u2069",
		},
		{
			name: "left-to-right embedding is removed",
			in:   "https://e.test/\u202Ab",
			want: "\u2066https://e.test/b\u2069",
		},
		{
			name: "control-only input still yields the bare isolate pair",
			in:   "\u2067",
			want: "\u2066\u2069",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BidiIsolateLTR(tt.in)
			if got != tt.want {
				t.Fatalf("BidiIsolateLTR(%q) = %q, want %q", tt.in, got, tt.want)
			}
			if tt.in != "" && got == "" {
				t.Fatalf("BidiIsolateLTR(%q) = empty, want a non-empty result", tt.in)
			}
			assertBalancedIsolate(t, "BidiIsolateLTR/"+tt.name, got)
		})
	}
}

func TestBidiIsolateUnchangedForControlFreeText(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "hebrew", in: "שלום Example", want: "\u2068שלום Example\u2069"},
		{name: "plain ascii", in: "plain ascii", want: "\u2068plain ascii\u2069"},
		{name: "left-to-right mark is kept", in: "a\u200Eb", want: "\u2068a\u200Eb\u2069"},
		{name: "japanese", in: "日本語 テスト", want: "\u2068日本語 テスト\u2069"},
		{name: "empty", in: "", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := BidiIsolate(tt.in); got != tt.want {
				t.Fatalf("BidiIsolate(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}

	ltrTests := []struct {
		name string
		in   string
		want string
	}{
		{name: "url", in: "https://example.test/שלום", want: "\u2066https://example.test/שלום\u2069"},
		{name: "right-to-left mark is kept", in: "a\u200Fb", want: "\u2066a\u200Fb\u2069"},
		{name: "arabic letter mark is kept", in: "a\u061Cb", want: "\u2066a\u061Cb\u2069"},
		{name: "empty", in: "", want: ""},
	}

	for _, tt := range ltrTests {
		t.Run("ltr/"+tt.name, func(t *testing.T) {
			if got := BidiIsolateLTR(tt.in); got != tt.want {
				t.Fatalf("BidiIsolateLTR(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestStripBidiControls(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "control free text is returned unchanged", in: "plain", want: "plain"},
		{name: "unpaired isolate initiator", in: "Acme \u2067Corp", want: "Acme Corp"},
		{name: "override and pop", in: "a\u202Eb\u202C", want: "ab"},
		{name: "control only", in: "\u2066", want: ""},
		{name: "left-to-right mark is kept", in: "a\u200Eb", want: "a\u200Eb"},
		{name: "right-to-left mark is kept", in: "a\u200Fb", want: "a\u200Fb"},
		{name: "arabic letter mark is kept", in: "a\u061Cb", want: "a\u061Cb"},
		{name: "zero width space is out of scope", in: "a\u200Bb", want: "a\u200Bb"},
		{name: "empty", in: "", want: ""},
		{name: "U+2066 LRI", in: "a\u2066b", want: "ab"},
		{name: "U+2067 RLI", in: "a\u2067b", want: "ab"},
		{name: "U+2068 FSI", in: "a\u2068b", want: "ab"},
		{name: "U+2069 PDI", in: "a\u2069b", want: "ab"},
		{name: "U+202A LRE", in: "a\u202Ab", want: "ab"},
		{name: "U+202B RLE", in: "a\u202Bb", want: "ab"},
		{name: "U+202C PDF", in: "a\u202Cb", want: "ab"},
		{name: "U+202D LRO", in: "a\u202Db", want: "ab"},
		{name: "U+202E RLO", in: "a\u202Eb", want: "ab"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := StripBidiControls(tt.in)
			if got != tt.want {
				t.Fatalf("StripBidiControls(%q) = %q, want %q", tt.in, got, tt.want)
			}
			if tt.in == tt.want && got != tt.in {
				t.Fatalf("StripBidiControls(%q) must return the input byte-for-byte, got %q", tt.in, got)
			}
		})
	}
}

// TestPercentEncodeBidiControlsEncodesEveryControlInRange pins the exact
// %XX%XX%XX output for a control from each of the two ranges
// PercentEncodeBidiControls covers, plus the four-control mixed case, in
// upper-case hex -- both %E2%80%AE and %e2%80%ae are valid percent-encodings,
// but only upper-case matches Go's own (*url.URL).EscapedPath() output (see
// textutil.go's doc comment and the plan's §3 evidence table).
func TestPercentEncodeBidiControlsEncodesEveryControlInRange(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "isolate initiator U+2066 range",
			in:   "https://e.test/\u2066a",
			want: "https://e.test/%E2%81%A6a",
		},
		{
			name: "embedding/override U+202E range",
			in:   "https://e.test/\u202Egpj.exe",
			want: "https://e.test/%E2%80%AEgpj.exe",
		},
		{
			name: "four mixed controls",
			in:   "https://e.test/\u202E\u2066mixed\u2069\u202C",
			want: "https://e.test/%E2%80%AE%E2%81%A6mixed%E2%81%A9%E2%80%AC",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PercentEncodeBidiControls(tt.in); got != tt.want {
				t.Fatalf("PercentEncodeBidiControls(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestPercentEncodeBidiControlsLeavesControlFreeStringsUnchanged is the I1
// tripwire at the function level: a string with no bidi scope control comes
// back byte-identical, including one with other non-ASCII bytes (an RTL
// script character, not a control) and the exact fixture
// TestDiscordEmbedURLIsNeverEscaped depends on. It also pins that the three
// directional marks StripBidiControls's doc comment calls out as kept
// (U+200E, U+200F, U+061C) are left unencoded here too -- a widened encoder
// set is caught by this test and by the sweep below.
func TestPercentEncodeBidiControlsLeavesControlFreeStringsUnchanged(t *testing.T) {
	tests := []string{
		"https://contoso.example",
		"https://e.test/a[1]",
		"https://example.test/שלום", // שלום -- RTL script, not a control
		"https://e.test/\u200Ea",    // U+200E LRM, kept by StripBidiControls
		"https://e.test/\u200Fa",    // U+200F RLM, kept by StripBidiControls
		"https://e.test/\u061Ca",    // U+061C ALM, kept by StripBidiControls
	}
	for _, in := range tests {
		if got := PercentEncodeBidiControls(in); got != in {
			t.Fatalf("PercentEncodeBidiControls(%q) = %q, want unchanged", in, got)
		}
	}
}

// TestPercentEncodeBidiControlsOutputParsesWithHostIntact asserts url.Parse
// succeeds on the encoded form and Host is unchanged, for the fixtures in
// TestPercentEncodeBidiControlsEncodesEveryControlInRange.
func TestPercentEncodeBidiControlsOutputParsesWithHostIntact(t *testing.T) {
	tests := []string{
		"https://e.test/\u2066a",
		"https://e.test/\u202Egpj.exe",
		"https://e.test/\u202E\u2066mixed\u2069\u202C",
	}
	for _, in := range tests {
		encoded := PercentEncodeBidiControls(in)
		parsed, err := url.Parse(encoded)
		if err != nil {
			t.Fatalf("url.Parse(%q) failed: %v", encoded, err)
		}
		if parsed.Host != "e.test" {
			t.Fatalf("url.Parse(%q).Host = %q, want %q", encoded, parsed.Host, "e.test")
		}
	}
}

// TestPercentEncodeBidiControlsCoversExactlyStripBidiControlsSet is the
// drift guard between the two notions of "bidi control" in this package: for
// every rune in the swept range, StripBidiControls deletes it if and only if
// PercentEncodeBidiControls encodes it. This kills a mutation that widens or
// narrows either function's set independently of the other -- in particular
// widening PercentEncodeBidiControls to include the directional marks
// U+200E/U+200F/U+061C, which StripBidiControls's doc comment says must stay
// unencoded.
func TestPercentEncodeBidiControlsCoversExactlyStripBidiControlsSet(t *testing.T) {
	ranges := [][2]rune{
		{0x2000, 0x20FF},
		{0x0600, 0x06FF},
	}
	for _, rg := range ranges {
		for r := rg[0]; r <= rg[1]; r++ {
			s := string(r)
			stripped := StripBidiControls(s) == ""
			encoded := PercentEncodeBidiControls(s) != s
			if stripped != encoded {
				t.Fatalf("rune %U: StripBidiControls deletes=%v, PercentEncodeBidiControls encodes=%v, want equal",
					r, stripped, encoded)
			}
		}
	}
}
