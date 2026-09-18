package discord

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/api"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/config"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/formatfields"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/notifyfmt"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/rss"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/textutil"
)

// The bidi isolate controls the Discord text helpers emit.
const (
	isolateLRI = "\u2066" // left-to-right isolate, used for URL-ish values
	isolateFSI = "\u2068" // first-strong isolate, used for natural text
	isolatePDI = "\u2069" // pop directional isolate
)

// liveMaskedLink models Discord's escape handling: a backslash consumes the next
// character, so "\[" is a literal bracket the link parser never sees. A masked
// link needs an UNESCAPED "[", a later UNESCAPED "]" and an immediate "(".
func liveMaskedLink(s string) bool {
	open := false
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' {
			i++ // consume the escaped byte
			continue
		}
		switch s[i] {
		case '[':
			open = true
		case ']':
			if open && i+1 < len(s) && s[i+1] == '(' {
				return true
			}
		}
	}
	return false
}

// stripIsolates removes the bidi isolate pair the Discord helpers add, so a test
// can compare the rendered text against the raw feed value.
func stripIsolates(value string) string {
	return strings.NewReplacer(isolateLRI, "", isolateFSI, "", isolatePDI, "").Replace(value)
}

func assertNoLiveMaskedLink(t *testing.T, name, value string) {
	t.Helper()
	if liveMaskedLink(value) {
		t.Fatalf("%s: %q still renders a masked link", name, value)
	}
}

func assertEscapedBrackets(t *testing.T, name, value string) {
	t.Helper()
	if !strings.Contains(value, `\[`) || !strings.Contains(value, `\]`) {
		t.Fatalf("%s: %q lacks escaped brackets", name, value)
	}
}

func maskedLinkRSSEntry() rss.Entry {
	return rss.Entry{
		Title:       "Click [here](https://evil.example)",
		Description: "Read more at [safe site](https://evil.example/steal) now.",
		Author:      "[Reporter](https://evil.example/who)",
		Categories:  []string{"[cat](https://evil.example/c)"},
		FeedTitle:   "[Feed](https://evil.example/f)",
		Link:        "https://good.example/a",
		Published:   time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC),
	}
}

func rssShowAuthorFormat() *notifyfmt.FormatOptions {
	formatCfg := notifyfmt.DefaultFormatOptions()
	formatCfg.RSS.ShowAuthor = true
	formatCfg.RSS.FieldOrder = []string{
		formatfields.RSSFieldTitle,
		formatfields.RSSFieldDescription,
		formatfields.RSSFieldLink,
		formatfields.RSSFieldAuthor,
		formatfields.RSSFieldCategories,
		formatfields.RSSFieldPublished,
		formatfields.RSSFieldFeedTitle,
	}
	return &formatCfg
}

// TestFormatRSSEmbedNeutralisesMaskedLinksInMarkdownSinks pins the fix and axis C:
// the markdown-rendering sinks (embed description, field values) can no longer
// carry a live masked link, while the plain-text sinks (title, footer, author)
// stay byte-identical because a backslash would be DISPLAYED there.
func TestFormatRSSEmbedNeutralisesMaskedLinksInMarkdownSinks(t *testing.T) {
	entry := maskedLinkRSSEntry()
	embed := formatRSSEmbed(entry, config.FeedTypeGeneral, rssShowAuthorFormat())

	assertNoLiveMaskedLink(t, "embed.Description", embed.Description)
	assertEscapedBrackets(t, "embed.Description", embed.Description)

	categoriesIndex := discordFieldIndex(embed.Fields, "Categories")
	if categoriesIndex < 0 {
		t.Fatal("Categories field missing")
	}
	categories := embed.Fields[categoriesIndex].Value
	assertNoLiveMaskedLink(t, "Categories field", categories)
	assertEscapedBrackets(t, "Categories field", categories)

	// Axis C: plain-text sinks stay unescaped. If a later change escapes them,
	// these three assertions fail.
	for _, sink := range []struct {
		name  string
		value string
	}{
		{"embed.Title", embed.Title},
		{"embed.Footer.Text", embed.Footer.Text},
		{"embed.Author.Name", embed.Author.Name},
	} {
		if strings.Contains(sink.value, `\`) {
			t.Fatalf("%s: %q is a plain-text sink and must not be escaped", sink.name, sink.value)
		}
		if !liveMaskedLink(sink.value) {
			t.Fatalf("%s: %q lost its raw markdown, plain-text sinks stay unchanged", sink.name, sink.value)
		}
	}

	// The RED input for the no-op backslash rule: feed text that already
	// contains backslashes. Without the `\` -> `\\` pair the escaper produces
	// `\\[label\\](url)`, which Discord renders as a literal backslash followed
	// by a LIVE link -- and every other row in this file would still pass.
	t.Run("feed text that already carries backslashes", func(t *testing.T) {
		preEscaped := maskedLinkRSSEntry()
		preEscaped.Description = `Read \[label\](https://evil.example) now.`
		preEscaped.Categories = []string{`\[cat\](https://evil.example/c)`}

		escapedEmbed := formatRSSEmbed(preEscaped, config.FeedTypeGeneral, rssShowAuthorFormat())

		assertNoLiveMaskedLink(t, "embed.Description", escapedEmbed.Description)
		if !strings.Contains(escapedEmbed.Description, `\\`) {
			t.Fatalf("embed.Description = %q, want the feed backslash doubled", escapedEmbed.Description)
		}

		index := discordFieldIndex(escapedEmbed.Fields, "Categories")
		if index < 0 {
			t.Fatal("Categories field missing")
		}
		assertNoLiveMaskedLink(t, "Categories field", escapedEmbed.Fields[index].Value)
	})

	// Invariant 11: the RSS pipeline decodes entities first (rss/parser.go
	// safeFeedText -> textutil.StripHTML) and only then reaches the formatter,
	// so an entity-encoded bracket cannot slip past the escape. The bidi
	// embedding &#8235; (U+202B) is stripped by BidiIsolate in the same pass.
	t.Run("entity-encoded brackets and bidi controls", func(t *testing.T) {
		const raw = "Click &#91;here&#93;(https://evil.example) and &#8235;&lbrack;x&rbrack;(https://e2.example)"
		decoded := textutil.StripHTML(raw)
		if !strings.Contains(decoded, "[here](https://evil.example)") {
			t.Fatalf("StripHTML(%q) = %q, want the entities decoded", raw, decoded)
		}

		// Order pin: the DECODE happens upstream, the escape here, and the
		// formatter must never decode what it has just escaped. An escape-then-
		// decode pipeline turns the escaped "&#91;" back into a live "[" -- the
		// assertions below on `decoded` alone cannot see that, because the test
		// does the decoding itself.
		for _, sink := range []struct {
			name   string
			render func(string, int) string
		}{
			{"discordRichText", discordRichText},
			{"discordDescriptionText", discordDescriptionText},
			{"discordURLText", discordURLText},
		} {
			got := stripIsolates(sink.render(raw, 400))
			if !strings.Contains(got, "&#91;") || !strings.Contains(got, "&#93;") {
				t.Fatalf("%s(%q) = %q decoded the entities; decoding must stay upstream of the escape",
					sink.name, raw, got)
			}
			assertNoLiveMaskedLink(t, sink.name, got)
		}

		entity := maskedLinkRSSEntry()
		entity.Description = decoded
		entity.Categories = []string{decoded}

		entityEmbed := formatRSSEmbed(entity, config.FeedTypeGeneral, rssShowAuthorFormat())

		assertNoLiveMaskedLink(t, "embed.Description", entityEmbed.Description)
		assertEscapedBrackets(t, "embed.Description", entityEmbed.Description)
		assertBalancedIsolate(t, "embed.Description", entityEmbed.Description)

		index := discordFieldIndex(entityEmbed.Fields, "Categories")
		if index < 0 {
			t.Fatal("Categories field missing")
		}
		assertNoLiveMaskedLink(t, "Categories field", entityEmbed.Fields[index].Value)
		assertEscapedBrackets(t, "Categories field", entityEmbed.Fields[index].Value)
	})
}

// TestFormatRansomwareEmbedNeutralisesMaskedLinksInFieldValues covers the
// ransomware.live API route. The field order is set explicitly because
// description/website/screenshot are not part of the default Discord order.
func TestFormatRansomwareEmbedNeutralisesMaskedLinksInFieldValues(t *testing.T) {
	formatCfg := notifyfmt.DefaultFormatOptions()
	formatCfg.FieldOrder = []string{
		formatfields.FieldGroup,
		formatfields.FieldVictim,
		formatfields.FieldDescription,
		formatfields.FieldPostURL,
		formatfields.FieldWebsite,
		formatfields.FieldScreenshot,
	}

	entry := api.RansomwareEntry{
		Group:       "Click [here](https://evil.example)",
		Victim:      "Acme [Corp](https://evil.example/v)",
		Description: "Contact [support](https://evil.example/phish) for details.",
		ClaimURL:    "https://leak.example/p[1](https://evil.example/c)",
		WebsiteURL:  "https://acme.example/w[1](https://evil.example/w)",
		Screenshot:  "https://shot.example/s[1](https://evil.example/s)",
		Discovered:  time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC),
	}

	embed := formatRansomwareEmbed(entry, &formatCfg)
	if len(embed.Fields) != len(formatCfg.FieldOrder) {
		t.Fatalf("embed has %d fields, want %d", len(embed.Fields), len(formatCfg.FieldOrder))
	}
	for _, field := range embed.Fields {
		assertNoLiveMaskedLink(t, "field "+field.Name, field.Value)
		assertEscapedBrackets(t, "field "+field.Name, field.Value)
	}

	if strings.Contains(embed.Title, `\`) {
		t.Fatalf("embed.Title = %q is a plain-text sink and must not be escaped", embed.Title)
	}
	if !liveMaskedLink(embed.Title) {
		t.Fatalf("embed.Title = %q lost its raw markdown", embed.Title)
	}
}

// TestFormatRSSEmbedNeutralisesMaskedLinkInUnparsableLinkField covers the third
// injection route: an unparsable link is always rendered as a field now
// (titles are never links), and the markdown-rendering
// field text neutralises the masked-link construct.
func TestFormatRSSEmbedNeutralisesMaskedLinkInUnparsableLinkField(t *testing.T) {
	tests := []struct {
		name string
		link string
	}{
		{name: "masked link", link: "[Download decryptor](https://evil.example)"},
		{name: "pre-escaped masked link", link: `\[Download decryptor\](https://evil.example)`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry := maskedLinkRSSEntry()
			entry.Link = tt.link

			embed := formatRSSEmbed(entry, config.FeedTypeGeneral, rssShowAuthorFormat())

			linkIndex := discordFieldIndex(embed.Fields, "Link")
			if linkIndex < 0 {
				t.Fatal("Link field missing")
			}
			value := embed.Fields[linkIndex].Value
			assertNoLiveMaskedLink(t, "Link field", value)
			assertEscapedBrackets(t, "Link field", value)
		})
	}
}

// TestDiscordURLFieldsNeverCarryEmbedURLAndBracketsAreEscaped pins the new
// invariant (titles are never links): embed.URL is always
// empty now on every path, the website/link URL is always a field, and a
// field value is display text -- so a URL containing literal brackets gets
// the same masked-link neutralisation escape every other Discord URL field
// already applies (escapeDiscordMarkdownText), the opposite of the
// pre-change invariant for these two sinks.
func TestDiscordURLFieldsNeverCarryEmbedURLAndBracketsAreEscaped(t *testing.T) {
	formatCfg := notifyfmt.DefaultFormatOptions()
	ransomware := api.RansomwareEntry{
		Group:      "Group",
		WebsiteURL: "https://acme.example/a[1]",
		Discovered: time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC),
	}
	ransomwareFormatCfg := formatCfg
	ransomwareFormatCfg.FieldOrder = []string{"website"}
	embed := formatRansomwareEmbed(ransomware, &ransomwareFormatCfg)
	if embed.URL != "" {
		t.Fatalf("ransomware embed.URL = %q, want empty (titles are never links)", embed.URL)
	}
	websiteIndex := discordFieldIndex(embed.Fields, "Website")
	if websiteIndex < 0 {
		t.Fatal("Website field missing")
	}
	if got := embed.Fields[websiteIndex].Value; !strings.Contains(got, `\[1\]`) {
		t.Fatalf("Website field value = %q, want escaped brackets \\[1\\]", got)
	}

	entry := rss.Entry{
		Title:     "Item",
		Link:      "https://good.example/a[1]",
		Published: time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC),
	}
	rssEmbed := formatRSSEmbed(entry, config.FeedTypeGeneral, rssShowAuthorFormat())
	if rssEmbed.URL != "" {
		t.Fatalf("RSS embed.URL = %q, want empty (titles are never links)", rssEmbed.URL)
	}
	linkIndex := discordFieldIndex(rssEmbed.Fields, "Link")
	if linkIndex < 0 {
		t.Fatal("Link field missing")
	}
	if got := rssEmbed.Fields[linkIndex].Value; !strings.Contains(got, `\[1\]`) {
		t.Fatalf("Link field value = %q, want escaped brackets \\[1\\]", got)
	}
}

// TestDiscordMarkdownEscapeLeavesLegitimateTextUnchanged is the appearance
// guarantee for option B' (link form only): removing the backslashes from the
// payload reproduces today's value, and everything but the bracket rows carries
// no backslash at all, which fails if the escape set ever widens to A.
func TestDiscordMarkdownEscapeLeavesLegitimateTextUnchanged(t *testing.T) {
	tests := []struct {
		name          string
		description   string
		wantBackslash bool
	}{
		{name: "plain headline", description: "CISA releases four ICS advisories"},
		{name: "underscores", description: "Threat actor drops invoice_2024_final.pdf"},
		{name: "asterisks", description: "LockBit 3.0 *new* builder leaked"},
		{name: "inline code", description: "Run `whoami` to check the account"},
		{name: "leading quote", description: "> The advisory supersedes AA24-109A"},
		// Discord consumes the backslash, so the operator still sees
		// "[Updated] CISA advisory AA24-109A".
		{name: "bracketed tag", description: "[Updated] CISA advisory AA24-109A", wantBackslash: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry := rss.Entry{
				Title:       "Headline",
				Description: tt.description,
				Link:        "https://good.example/a",
				Published:   time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC),
			}
			embed := formatRSSEmbed(entry, config.FeedTypeGeneral, rssShowAuthorFormat())

			body := stripIsolates(embed.Description)
			if unescaped := strings.ReplaceAll(body, `\`, ""); unescaped != tt.description {
				t.Fatalf("embed.Description unescaped = %q, want %q", unescaped, tt.description)
			}
			if got := strings.Contains(body, `\`); got != tt.wantBackslash {
				t.Fatalf("embed.Description = %q carries backslash %v, want %v", body, got, tt.wantBackslash)
			}
		})
	}
}

// TestDiscordMarkdownEscapeRespectsLimitsAfterGrowth pins invariant 2: the
// escape runs BEFORE truncation, so worst-case doubling cannot push a sink past
// its constant. A "truncate then escape" implementation blows the field-value
// and title limits.
func TestDiscordMarkdownEscapeRespectsLimitsAfterGrowth(t *testing.T) {
	for _, n := range []int{700, 1200, 3000} {
		payload := strings.Repeat("[", n)
		entry := rss.Entry{
			Title:       payload,
			Description: payload,
			Author:      payload,
			Categories:  []string{payload},
			FeedTitle:   payload,
			Link:        "https://good.example/" + payload,
			Published:   time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC),
		}
		embed := formatRSSEmbed(entry, config.FeedTypeGeneral, rssShowAuthorFormat())

		assertRuneLimit(t, n, "embed.Title", embed.Title, discordEmbedTitleLimit)
		assertRuneLimit(t, n, "embed.Description", embed.Description, discordRSSSummaryLimit)
		assertRuneLimit(t, n, "embed.Author.Name", embed.Author.Name, discordEmbedAuthorNameLimit)
		assertRuneLimit(t, n, "embed.Footer.Text", embed.Footer.Text, discordEmbedFooterTextLimit)
		for _, field := range embed.Fields {
			assertRuneLimit(t, n, "field "+field.Name, field.Value, discordEmbedFieldValueLimit)
		}
		if total := discordEmbedTextLengthForTest(embed); total > discordEmbedTotalTextLimit {
			t.Fatalf("n=%d: embed total text length = %d, want <= %d", n, total, discordEmbedTotalTextLimit)
		}

		formatCfg := notifyfmt.DefaultFormatOptions()
		formatCfg.FieldOrder = []string{formatfields.FieldGroup, formatfields.FieldDescription}
		ransomware := api.RansomwareEntry{
			Group:       payload,
			Description: payload,
			Discovered:  time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC),
		}
		ransomwareEmbed := formatRansomwareEmbed(ransomware, &formatCfg)
		assertRuneLimit(t, n, "ransomware embed.Title", ransomwareEmbed.Title, discordEmbedTitleLimit)
		for _, field := range ransomwareEmbed.Fields {
			assertRuneLimit(t, n, "ransomware field "+field.Name, field.Value, discordEmbedFieldValueLimit)
		}
	}
}

// TestDiscordEscapeStaysOffOperatorOwnedText pins the other side of the escape
// boundary: only FEED text is escaped. Field labels and the empty-field
// placeholder come from the operator's config, are rendered as-is today, and
// must not sprout backslashes -- that would be an appearance change nobody
// asked for. Bracketed labels are a realistic operator choice ("[Cat]"), so the
// fixtures use them deliberately.
func TestDiscordEscapeStaysOffOperatorOwnedText(t *testing.T) {
	const placeholder = "[none]"
	labels := map[string]string{
		"categories":  "[Cat]",
		"link":        "[Link]",
		"published":   "[Pub]",
		"feed_url":    "[Feed URL]",
		"group":       "[Grp]",
		"victim":      "[Vic]",
		"description": "[Desc]",
		"post_url":    "[Post]",
		"website":     "[Web]",
		"screenshot":  "[Shot]",
	}

	assertPlain := func(t *testing.T, what, value string) {
		t.Helper()
		if strings.Contains(value, `\`) {
			t.Fatalf("%s = %q is operator-owned text and must not be escaped", what, value)
		}
	}

	t.Run("rss route", func(t *testing.T) {
		formatCfg := notifyfmt.DefaultFormatOptions()
		formatCfg.EmptyFieldText = placeholder
		formatCfg.ShowEmptyFields = true
		formatCfg.FieldLabels = labels
		formatCfg.RSS.ShowAuthor = true
		formatCfg.RSS.FieldOrder = []string{
			formatfields.RSSFieldTitle,
			formatfields.RSSFieldDescription,
			formatfields.RSSFieldLink,
			formatfields.RSSFieldAuthor,
			formatfields.RSSFieldCategories,
			formatfields.RSSFieldPublished,
			formatfields.RSSFieldFeedTitle,
			formatfields.RSSFieldFeedURL,
		}

		// Everything empty, so every sink renders the placeholder.
		embed := formatRSSEmbed(rss.Entry{Title: "Headline"}, config.FeedTypeGeneral, &formatCfg)

		assertPlain(t, "embed.Description", embed.Description)
		if embed.Author != nil {
			assertPlain(t, "embed.Author.Name", embed.Author.Name)
		}
		if embed.Footer != nil {
			assertPlain(t, "embed.Footer.Text", embed.Footer.Text)
		}
		if len(embed.Fields) == 0 {
			t.Fatal("no fields rendered, the placeholder path was not exercised")
		}
		sawCategories := false
		for _, field := range embed.Fields {
			assertPlain(t, "field name "+field.Name, field.Name)
			assertPlain(t, "field value of "+field.Name, field.Value)
			if strings.Contains(field.Name, "[Cat]") {
				sawCategories = true
				if stripIsolates(field.Value) != placeholder {
					t.Fatalf("Categories value = %q, want the placeholder %q verbatim",
						field.Value, placeholder)
				}
			}
		}
		if !sawCategories {
			t.Fatal("Categories field missing, the placeholder path was not exercised")
		}
	})

	t.Run("ransomware route", func(t *testing.T) {
		formatCfg := notifyfmt.DefaultFormatOptions()
		formatCfg.EmptyFieldText = placeholder
		formatCfg.ShowEmptyFields = true
		formatCfg.FieldLabels = labels
		formatCfg.FieldOrder = []string{
			formatfields.FieldGroup,
			formatfields.FieldVictim,
			formatfields.FieldDescription,
			formatfields.FieldPostURL,
			formatfields.FieldWebsite,
			formatfields.FieldScreenshot,
		}

		embed := formatRansomwareEmbed(api.RansomwareEntry{
			Discovered: time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC),
		}, &formatCfg)

		assertPlain(t, "embed.Title", embed.Title)
		if embed.Footer != nil {
			assertPlain(t, "embed.Footer.Text", embed.Footer.Text)
		}
		if len(embed.Fields) == 0 {
			t.Fatal("no fields rendered, the placeholder path was not exercised")
		}
		for _, field := range embed.Fields {
			assertPlain(t, "field name "+field.Name, field.Name)
			assertPlain(t, "field value of "+field.Name, field.Value)
			if field.Value != placeholder {
				t.Fatalf("field %q value = %q, want the placeholder %q verbatim",
					field.Name, field.Value, placeholder)
			}
		}
	})

	// A non-empty Categories list still goes through the escape: the operator
	// owns the placeholder, the feed owns the values.
	t.Run("categories from the feed are still escaped", func(t *testing.T) {
		formatCfg := rssShowAuthorFormat()
		formatCfg.EmptyFieldText = placeholder
		embed := formatRSSEmbed(rss.Entry{
			Title:      "Headline",
			Categories: []string{"[cat](https://evil.example/c)"},
			Link:       "https://good.example/a",
			Published:  time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC),
		}, config.FeedTypeGeneral, formatCfg)

		index := discordFieldIndex(embed.Fields, "Categories")
		if index < 0 {
			t.Fatal("Categories field missing")
		}
		assertNoLiveMaskedLink(t, "Categories field", embed.Fields[index].Value)
		assertEscapedBrackets(t, "Categories field", embed.Fields[index].Value)
	})
}

func assertRuneLimit(t *testing.T, n int, name, value string, limit int) {
	t.Helper()
	if length := len([]rune(value)); length > limit {
		t.Fatalf("n=%d: %s length = %d, want <= %d", n, name, length, limit)
	}
}

// TestDiscordMarkdownEscapeKeepsIsolatesBalanced pins invariant 4 against the
// escape change: escaping runs before BidiIsolate, so every sink still carries
// either no bidi control or exactly the pair this package adds.
func TestDiscordMarkdownEscapeKeepsIsolatesBalanced(t *testing.T) {
	entry := maskedLinkRSSEntry()
	entry.Title = "Click \u202E[here](https://evil.example)"
	entry.Description = "Read \u2067[safe site](https://evil.example/steal) now."
	entry.Author = "\u202A[Reporter](https://evil.example/who)"
	entry.Categories = []string{"\u2066[cat](https://evil.example/c)"}
	entry.FeedTitle = "[Feed](https://evil.example/f)\u2069"
	entry.Link = "[not a url](https://evil.example)"

	embed := formatRSSEmbed(entry, config.FeedTypeGeneral, rssShowAuthorFormat())

	assertBalancedIsolate(t, "embed.Title", embed.Title)
	assertBalancedIsolate(t, "embed.Description", embed.Description)
	assertBalancedIsolate(t, "embed.Author.Name", embed.Author.Name)
	assertBalancedIsolate(t, "embed.Footer.Text", embed.Footer.Text)
	for _, field := range embed.Fields {
		assertBalancedIsolate(t, "field "+field.Name, field.Value)
	}
	assertNoLiveMaskedLink(t, "embed.Description", embed.Description)
}

// TestFormatRSSEmbedGoldenPayloadForPlainTitle is the byte-identity net for
// "no appearance change on normal traffic": the marshalled payload of a fully
// plain entry must not move a single byte. It also pins the JSON key names and
// the omitempty shape of MessageEmbed.
func TestFormatRSSEmbedGoldenPayloadForPlainTitle(t *testing.T) {
	entry := rss.Entry{
		Title:       "CISA releases four ICS advisories",
		Description: "The advisories cover industrial control systems.",
		Categories:  []string{"advisory"},
		FeedTitle:   "CISA",
		Link:        "https://www.cisa.gov/a",
		Published:   time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC),
	}
	formatCfg := notifyfmt.DefaultFormatOptions()

	embed := formatRSSEmbed(entry, config.FeedTypeGovernment, &formatCfg)
	payload, err := json.Marshal(&WebhookParams{Embeds: []*MessageEmbed{embed}})
	if err != nil {
		t.Fatalf("marshal webhook params: %v", err)
	}

	// Titles are never links: embed.URL is always empty
	// now, and the article link is always its own field ahead of Categories,
	// wrapped in isolateLRI (URL-ish values), not isolateFSI (natural text).
	want := `{"embeds":[` +
		`{"title":"` + isolateFSI + `CISA releases four ICS advisories` + isolatePDI + `",` +
		`"description":"` + isolateFSI + `The advisories cover industrial control systems.` +
		isolatePDI + `",` +
		`"timestamp":"2026-09-03T10:00:00Z","color":16753920,` +
		`"footer":{"text":"` + isolateFSI + `CISA` + isolatePDI + `"},` +
		`"fields":[{"name":"🔗 Link","value":"` + isolateLRI + `https://www.cisa.gov/a` + isolatePDI + `"},` +
		`{"name":"📂 Categories","value":"` + isolateFSI + `advisory` + isolatePDI + `"},` +
		`{"name":"📅 Published","value":"2026-09-03 10:00:00 UTC","inline":true}]}]}`

	if string(payload) != want {
		t.Fatalf("golden payload changed:\n got %s\nwant %s", payload, want)
	}
}

// endsWithLiveEscape scans text with Discord's "a backslash consumes the next
// character" rule and reports whether the scan ends on a backslash that has
// nothing left to consume -- the artefact a truncation leaves behind when it
// cuts an escape pair in half. Discord displays such a backslash.
func endsWithLiveEscape(text string) bool {
	for i := 0; i < len(text); i++ {
		if text[i] != '\\' {
			continue
		}
		if i+1 >= len(text) {
			return true
		}
		i++
	}
	return false
}

// hasDanglingEscape applies endsWithLiveEscape to the escaped body, i.e. with
// the truncation markers -- which are appended AFTER escaping and are never
// escaped -- taken out of the way. TruncateMiddle puts its marker in the middle,
// so the head before the first marker is checked there.
func hasDanglingEscape(text string) bool {
	if body, ok := strings.CutSuffix(text, "... [truncated]"); ok {
		return endsWithLiveEscape(body)
	}
	if body, ok := strings.CutSuffix(text, "..."); ok {
		return endsWithLiveEscape(body)
	}
	if head, _, ok := strings.Cut(text, "..."); ok {
		return endsWithLiveEscape(head)
	}
	return endsWithLiveEscape(text)
}

// TestDiscordMarkdownEscapeLeavesNoDanglingBackslash pins the truncation
// artefact escape-before-truncate introduces: the cut can land between a
// backslash and the character it escapes, and Discord would display that
// backslash. It also re-proves invariant 9 over the sweep: truncating an
// escaped string can never re-form a live masked link.
func TestDiscordMarkdownEscapeLeavesNoDanglingBackslash(t *testing.T) {
	helpers := []struct {
		name   string
		render func(string, int) string
	}{
		{name: "discordRichText", render: discordRichText},
		{name: "discordDescriptionText", render: discordDescriptionText},
		{name: "discordURLText", render: discordURLText},
	}

	// The +2 budget accounts for the isolate pair the helpers reserve, so the
	// truncator sees exactly the budget from the measured table.
	rows := []struct {
		raw    string
		budget int
	}{
		{raw: "ab[cdef", budget: 6},
		{raw: "ab[cdef", budget: 7},
		{raw: "a[bcdefgh", budget: 6},
		{raw: "abc]defgh", budget: 7},
	}
	for _, helper := range helpers {
		for _, row := range rows {
			got := stripIsolates(helper.render(row.raw, row.budget+2))
			if hasDanglingEscape(got) {
				t.Fatalf("%s(%q, %d) = %q leaves a dangling backslash", helper.name, row.raw, row.budget+2, got)
			}
		}
	}

	payloads := []string{
		strings.Repeat("x", 40) + "[y]" + strings.Repeat("z", 200),
		strings.Repeat("x", 60) + "[label](https://evil.example)" + strings.Repeat("z", 200),
		strings.Repeat("x", 12) + `\[label\](https://evil.example)` + strings.Repeat("z", 40),
		strings.Repeat("[", 120),
		strings.Repeat("]", 120),
	}
	for _, helper := range helpers {
		for _, payload := range payloads {
			for budget := 4; budget <= 300; budget++ {
				got := stripIsolates(helper.render(payload, budget))
				if hasDanglingEscape(got) {
					t.Fatalf("%s(len %d payload, %d) = %q leaves a dangling backslash",
						helper.name, len(payload), budget, got)
				}
				if liveMaskedLink(got) {
					t.Fatalf("%s(len %d payload, %d) = %q re-formed a masked link",
						helper.name, len(payload), budget, got)
				}
			}
		}
	}
}

// TestDiscordURLTextDropsOrphanBracketAtTruncationBoundary is the finding's
// own literal repro run through the real, unescaped call signature:
// discordURLText escapes every "[" first, so a middle-truncation cut can fall
// exactly between the escaping backslash (kept in the head) and the bracket
// it protected (the first character the tail would otherwise start with).
// Before the internal/textutil TrimDanglingEscape fix, the "..." marker was
// immediately followed by a bare, unescaped "[".
func TestDiscordURLTextDropsOrphanBracketAtTruncationBoundary(t *testing.T) {
	got := stripIsolates(discordURLText(strings.Repeat("[", 60), 6))

	if strings.Contains(got, "...[") {
		t.Fatalf("discordURLText(...) = %q still has an orphan bracket right after the marker", got)
	}
	if hasDanglingEscape(got) {
		t.Fatalf("discordURLText(...) = %q leaves a dangling backslash", got)
	}
}
