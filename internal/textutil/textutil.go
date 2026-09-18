package textutil

import (
	"errors"
	"html"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/feedurl"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/timeutil"
)

// Pre-compiled regex patterns for HTML stripping
var (
	htmlTagRegex                    = regexp.MustCompile(`<[^>]*>`)
	whitespaceRegex                 = regexp.MustCompile(`\s+`)
	malformedHexHTMLEntityRegex     = regexp.MustCompile(`&#;x([0-9A-Fa-f]+);`)
	malformedDecimalHTMLEntityRegex = regexp.MustCompile(`&#;([0-9]+);`)
	// (?i): a scheme and a hardcoded host are both ASCII text an operator (or,
	// for the webhook two below, a provider's response body) can type in any
	// case -- url.Parse itself lowercases parsed.Scheme, but the RAW string
	// these regexes match against keeps whatever case it was written in, so
	// without (?i) an uppercase-scheme URL such as "HTTPS://user:SECRET@host/"
	// was never matched at all and every redactor built on these three regexes
	// was a complete no-op on it. feedurl.Validate accepts an uppercase scheme
	// (it only checks parsed.Scheme, already lowercased by url.Parse), so this
	// was reachable from a live config value, not merely theoretical. Widening
	// only ever ADDS matches; every string these regexes matched before still
	// matches, byte-for-byte, under (?i).
	genericHTTPURLRegex = regexp.MustCompile(`(?i)https?://[^\s"'<)]+`)
	// discordWebhookURLRegex and slackWebhookURLRegex are a hardcoded, four-host
	// allowlist (discord.com, discordapp.com, hooks.slack.com,
	// hooks.slack-gov.com) -- the only protection RedactWebhookSecrets /
	// RedactURLCredentials give a caller with no known-good webhook URL to
	// anchor a fallback on (internal/api/client.go, internal/discord/webhook.go,
	// internal/status/retry_store.go and tracker.go all call one of those two
	// without a webhookURL argument). Any slack_compatible_webhook_hosts custom
	// host stays completely uncovered on those no-anchor paths -- this is a
	// stopgap that rots the moment a provider adds a host, not full closure.
	// RedactURLCredentials's own url.Parse-failure branch (redactUnparsableURLMatch,
	// below) is deliberately NOT part of that stopgap: it was tried once and
	// reverted the same day (see that function's doc comment) because deriving
	// a "host" from a match these two regexes did not already catch, out of
	// text a parser itself could not make sense of, introduced new leaks
	// rather than closing the old one.
	// The optional "(?::[^@/\s"'<)]*)?" segment after the host tolerates a typo'd
	// or otherwise malformed port between the host and the required literal
	// path prefix (e.g. "hooks.slack.com:bad/services/..."), so a diagnostic
	// regression found 2026-09-04 -- a typo'd port made this regex not match at
	// all, losing the host label on a parse failure -- does not recur. Safe
	// unconditionally, unlike the redactUnparsableURLMatch attempt discussed
	// above: the redaction callback for both regexes already discards
	// everything in the match and returns a fixed literal string, so widening
	// the match window never changes what gets echoed, only which input shapes
	// are recognised as "this is one of the four known hosts". The excluded
	// character class is the SAME one genericHTTPURLRegex/the rest of this file
	// already use ([^/\s"'<)]) -- a literal '/' inside the port position still
	// ends the optional group, so an injected fake path segment
	// ("hooks.slack.com:foo/bar/services/...") cannot walk the match past a real
	// slash into a decoy "/services/..."; the regex simply fails to match that
	// shape at that anchor and the text falls through to the generic,
	// still-safe RedactURLCredentials path -- verified live, not assumed.
	// '@' is excluded from the port class for the same reason a '/' is: an '@'
	// there ends the authority in every real parser, so what follows it -- not
	// the text before it -- is the actual destination. Admitting '@' would let
	// "https://hooks.slack.com:x@other.example.test/services/TOKEN" match and be
	// replaced by the fixed literal naming hooks.slack.com, asserting a
	// destination the string never had. A genuine typo'd port never contains an
	// '@', so excluding it costs the fix nothing.
	// Both regexes splice in the discord*/slack* host constants (declared
	// below) via regexp.QuoteMeta rather than hand-spelling the hosts a
	// second time as regex text. discord(?:app)?\.com and
	// hooks\.slack(?:-gov)?\.com would have been the terser way to write
	// "either host", but they duplicate discordLegacyWebhookHost/
	// slackGovWebhookHost as regex fragments no test or tooling can compare
	// against the constants -- alternating the quoted, fully-spelled
	// constants instead matches the exact same two strings (verified by
	// this package's existing tests, unmodified) and keeps the host spelling
	// in one place.
	discordWebhookURLRegex = regexp.MustCompile(
		`(?i)https://(` + regexp.QuoteMeta(discordWebhookHost) + `|` + regexp.QuoteMeta(discordLegacyWebhookHost) + `)` +
			`(?::[^@/\s"'<)]*)?` +
			`/api(?:/v[0-9]+)?/webhooks/` +
			`[^/\s"'<)]+/[^\s"'<)]+`,
	)
	slackWebhookURLRegex = regexp.MustCompile(
		`(?i)https://(?:` + regexp.QuoteMeta(slackWebhookHost) + `|` + regexp.QuoteMeta(slackGovWebhookHost) + `)` +
			`(?::[^@/\s"'<)]*)?/services/[^\s"'<)]+`,
	)
)

const (
	httpScheme    = "http://"
	httpsScheme   = "https://"
	defangedHTTP  = "hxxp://"
	defangedHTTPS = "hxxps://"

	// discordWebhookHost, discordLegacyWebhookHost, slackWebhookHost and
	// slackGovWebhookHost are the single spelling of the four hardcoded
	// webhook hosts that discordWebhookURLRegex/slackWebhookURLRegex,
	// knownWebhookHosts and the RedactWebhookSecrets replacement callbacks
	// all need to agree on. Hand-maintaining the same host string in several
	// places at once is exactly the shape that let a whitespace character
	// class and Go's \s drift apart elsewhere in this file; a shared
	// constant makes that kind of divergence impossible here rather than
	// merely unlikely. Named discordLegacyWebhookHost, not discordAppWebhookHost,
	// for two reasons: it matches internal/discordurl.LegacyHost, the
	// existing name for this same host elsewhere in the codebase, and
	// "...AppWebhookHost" happens to contain "pW" at the App/Webhook
	// boundary, which gosec's G101 default pattern (".../pw/.../i") matches
	// case-insensitively as a credential-shaped identifier -- a false
	// positive on the operator's audit lint profile, confirmed empirically.
	discordWebhookHost       = "discord.com"
	discordLegacyWebhookHost = "discordapp.com"
	slackWebhookHost         = "hooks.slack.com"
	slackGovWebhookHost      = "hooks.slack-gov.com"

	// redactedURLMarker replaces an entire URL whose scheme+host is not being
	// kept in the output; redactedPathMarker replaces just the path/tail
	// after a scheme+host that IS kept; redactedValueMarker replaces a single
	// non-URL secret value (a query parameter, a credential detail
	// fragment). Several tests in this package assert on these exact
	// strings, so a change to any of them belongs in this one place.
	redactedURLMarker   = "[redacted URL]"
	redactedPathMarker  = "/[redacted]"
	redactedValueMarker = "[redacted]"

	// The space between the dots and "[" is load-bearing: TrimDanglingEscape's
	// orphan-bracket check only ever fires on a "[" or "]" that is the very
	// first character right after textTruncationMarker ("..."), and that space
	// keeps this marker's own "[" out of that position. Close the gap (e.g.
	// "...[truncated]") and TrimDanglingEscape would treat this marker's own
	// bracket as an orphan and strip it. Pinned directly by
	// TestTrimDanglingEscapeDoesNotTouchDescriptionMarkerBracket.
	descriptionTruncationMarker = "... [truncated]"
	textTruncationMarker        = "..."

	// redactedQueryValue is the query-escaped form of the "[redacted]" placeholder
	// that url.Values.Encode produces on the parsed path; the raw-query fallback
	// must match it exactly.
	redactedQueryValue = "%5Bredacted%5D"
)

// DefangURL converts URLs to a safe format by replacing http/https with hxxp/hxxps.
// Returns the original string if the URL has no http/https prefix.
func DefangURL(url string) string {
	if url == "" {
		return url
	}
	lower := strings.ToLower(url)
	if strings.HasPrefix(lower, httpsScheme) {
		return defangedHTTPS + url[len(httpsScheme):]
	}
	if strings.HasPrefix(lower, httpScheme) {
		return defangedHTTP + url[len(httpScheme):]
	}
	return url
}

// StripHTML removes HTML tags and decodes common HTML entities from text.
// Multiple whitespace characters are collapsed into a single space.
func StripHTML(input string) string {
	if input == "" {
		return ""
	}

	// Remove HTML tags using pre-compiled regex
	text := htmlTagRegex.ReplaceAllString(input, "")

	text = decodeHTMLEntities(text)
	text = strings.ReplaceAll(text, "\u00a0", " ")
	text = stripInvisibleFormatting(text)

	// Clean up multiple spaces and trim
	text = whitespaceRegex.ReplaceAllString(text, " ")
	return strings.TrimSpace(text)
}

// stripInvisibleFormatting removes ZERO WIDTH SPACE (U+200B) and the BOM /
// ZERO WIDTH NO-BREAK SPACE (U+FEFF) outright, and normalizes the Unicode
// LINE SEPARATOR (U+2028) and PARAGRAPH SEPARATOR (U+2029) to a plain space.
// It is called before the whitespace-collapse pass in StripHTML on purpose:
// that pass cleans up both the space U+2028/U+2029 turn into and the gap a
// deleted U+200B/U+FEFF leaves behind, so a run of these next to ordinary
// spaces collapses to one space instead of surviving as extra whitespace.
// (Entity decoding is a separate, earlier step in StripHTML, so an
// entity-encoded form such as "&#x200b;" is already a raw character by the
// time it reaches here regardless of where this call sits -- that is not
// why the ordering matters.) The line and paragraph separators are line
// breaks, not invisible marks, but they get the same space treatment an
// ordinary ASCII newline in the same text already receives from that pass,
// rather than being deleted and fusing the words on either side together.
//
// ZERO WIDTH NON-JOINER (U+200C) and ZERO WIDTH JOINER (U+200D) are
// deliberately NOT touched here, even though they also render as nothing
// themselves: unlike U+200B/U+FEFF, their entire purpose is to change how
// their *neighbouring* characters render (Persian \u0628\u0627\u062c\u200c\u0627\u0641\u0632\u0627\u0631 needs the ZWNJ
// to read as "ransomware" rather than fuse into a different word, the same
// goes for Hindi/Tamil conjuncts, and every ZWJ emoji sequence -- \ud83c\udff4\u200d\u2620\ufe0f,
// \ud83d\udc68\u200d\ud83d\udcbb -- falls apart into unrelated glyphs without its ZWJ). Deleting them
// was tried on 2026-09-04 and reverted the same day: it corrupted exactly
// the non-Latin-script and emoji content this bot exists to relay. Their
// removal was originally motivated by filename spoofing in feed/API text
// (e.g. "evil<ZWSP>file.exe"); U+200B and U+FEFF above still close that
// specific case. That motivating set was incomplete anyway -- U+2060 WORD
// JOINER is a direct substitute for U+200B and still passes through
// unstripped, as do U+00AD SOFT HYPHEN, U+180E MONGOLIAN VOWEL SEPARATOR,
// U+034F COMBINING GRAPHEME JOINER and U+200E/U+200F (LTR/RTL MARK); none of
// these are stripped by this function either, so a spoofed filename using
// one of them instead of U+200B/U+FEFF would still pass through unchanged.
//
// This closes a gap BidiIsolate does not: BidiIsolate (and the
// StripBidiControls it calls) only strips bidi-control characters
// (U+2066-U+2069, U+202A-U+202E), not these, so text routed through it after
// StripHTML was still carrying them.
func stripInvisibleFormatting(text string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '\u200b', '\ufeff':
			return -1
		case '\u2028', '\u2029':
			return ' '
		default:
			return r
		}
	}, text)
}

func decodeHTMLEntities(text string) string {
	for i := 0; i < 5; i++ {
		decoded := html.UnescapeString(repairMalformedHTMLEntities(text))
		if decoded == text {
			return decoded
		}
		text = decoded
	}
	return text
}

func repairMalformedHTMLEntities(text string) string {
	text = malformedHexHTMLEntityRegex.ReplaceAllString(text, "&#x${1};")
	return malformedDecimalHTMLEntityRegex.ReplaceAllString(text, "&#${1};")
}

// TruncateText truncates text to a maximum number of runes (Unicode-safe).
// If the text exceeds maxLength runes, it is truncated with "..." appended.
func TruncateText(text string, maxLength int) string {
	if maxLength <= 0 {
		return ""
	}
	if !ExceedsRuneLimit(text, maxLength) {
		return text
	}
	if maxLength <= 3 {
		return PrefixRunes(text, maxLength)
	}
	return PrefixRunes(text, maxLength-3) + textTruncationMarker
}

// TruncateDescription truncates long natural-language descriptions at a
// readable sentence boundary when one is available and marks shortened text.
func TruncateDescription(text string, maxLength int) string {
	if maxLength <= 0 {
		return ""
	}
	if !ExceedsRuneLimit(text, maxLength) {
		return text
	}

	markerLength := len([]rune(descriptionTruncationMarker))
	if maxLength <= markerLength {
		return PrefixRunes(text, maxLength)
	}

	bodyBudget := maxLength - markerLength
	body := strings.TrimSpace(PrefixRunes(text, bodyBudget))
	if sentence := truncateDescriptionSentence(body, bodyBudget); sentence != "" {
		body = sentence
	}
	return body + descriptionTruncationMarker
}

func truncateDescriptionSentence(candidate string, bodyBudget int) string {
	lastSentenceEndByte := -1
	lastSentenceEndRunes := -1
	runeIndex := 0
	for byteIndex, r := range candidate {
		if r == '.' || r == '!' || r == '?' {
			lastSentenceEndByte = byteIndex + len(string(r))
			lastSentenceEndRunes = runeIndex + 1
		}
		runeIndex++
	}

	if lastSentenceEndRunes <= bodyBudget/2 {
		return ""
	}

	sentence := strings.TrimSpace(candidate[:lastSentenceEndByte])
	sentence = strings.TrimRight(sentence, ".!?")
	return strings.TrimSpace(sentence)
}

// TruncateMiddle truncates text to maxLength runes while preserving both ends.
func TruncateMiddle(text string, maxLength int) string {
	if maxLength <= 0 {
		return ""
	}
	if !ExceedsRuneLimit(text, maxLength) {
		return text
	}
	if maxLength <= 3 {
		return PrefixRunes(text, maxLength)
	}
	remaining := maxLength - 3
	head := remaining / 2
	tail := remaining - head
	return PrefixRunes(text, head) + textTruncationMarker + SuffixRunes(text, tail)
}

// TrimDanglingEscape removes a backslash that a truncation left without the
// character it escapes, and a bracket that a truncation left without the
// backslash that escaped it. Escape sequences are only ever emitted in pairs,
// so an odd number of trailing backslashes on the truncated body means the cut
// split a pair, and the surviving backslash would be displayed by the
// renderer; symmetrically, a bare "[" or "]" as the very first character after
// a marker means the cut fell on the OTHER side of the same pair, dropping the
// escaping backslash and leaving the bracket to be displayed unescaped.
//
// The truncation markers are appended after the escaping and are never escaped
// themselves, so they are taken out of the way first: TruncateDescription and
// TruncateText put their marker at the end, TruncateMiddle in the middle. Which
// truncator ran is not knowable here and the text itself may contain "...", so
// every marker boundary AND the end of the string are trimmed. That is safe:
// in escaped text a run of backslashes is odd, or a bracket immediately
// follows a "..." with no escaping backslash of its own, only where a
// truncation cut actually split an escape pair -- callers only ever hand this
// function fully-escaped text (every "[" and "]" already has its own
// backslash before it reaches here), so at any position that is not a
// truncation cut both trims are no-ops.
//
// TruncateMiddle is the only truncator that can produce the bracket case (its
// marker sits in the middle of the text, with real content on both sides);
// TruncateText and TruncateDescription put their marker at the very end, so
// nothing ever follows it for the new check to fire on -- TruncateDescription
// doubly so, since its own marker text ("... [truncated]") has its own literal
// "[" shielded by the space right after the three dots, not by this trim (see
// descriptionTruncationMarker's own comment).
func TrimDanglingEscape(text string) string {
	var trimmed strings.Builder
	rest := text
	for {
		index := strings.Index(rest, textTruncationMarker)
		if index < 0 {
			break
		}
		trimmed.WriteString(trimOddTrailingBackslashes(rest[:index]))
		trimmed.WriteString(textTruncationMarker)
		rest = trimLeadingOrphanBracket(rest[index+len(textTruncationMarker):])
	}
	trimmed.WriteString(trimOddTrailingBackslashes(rest))
	return trimmed.String()
}

// trimLeadingOrphanBracket drops a "[" or "]" that is the very first
// character of text, and leaves text alone otherwise. Used immediately after
// a truncation marker, where a bare leading bracket can only mean a
// TruncateMiddle cut fell between an escaping backslash (kept in the head)
// and the bracket it protected (the first character of the tail) -- every
// other "[" or "]" in escaped text still has its own backslash immediately
// before it, so this can never remove real content, only the one orphaned
// bracket a cut can produce. Unconditionally trimming the first byte instead
// of checking for "[" / "]" first would eat ordinary content whenever the
// tail happens to start with a letter or digit, which is most of the time;
// the HasPrefix guard is what keeps this a true no-op everywhere except the
// exact orphan case.
func trimLeadingOrphanBracket(text string) string {
	if strings.HasPrefix(text, "[") || strings.HasPrefix(text, "]") {
		return text[1:]
	}
	return text
}

func trimOddTrailingBackslashes(text string) string {
	count := 0
	for i := len(text) - 1; i >= 0 && text[i] == '\\'; i-- {
		count++
	}
	if count%2 == 1 {
		return text[:len(text)-1]
	}
	return text
}

func ExceedsRuneLimit(text string, limit int) bool {
	if limit < 0 {
		return text != ""
	}
	count := 0
	for range text {
		count++
		if count > limit {
			return true
		}
	}
	return false
}

func PrefixRunes(text string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	count := 0
	for i := range text {
		if count == maxRunes {
			return text[:i]
		}
		count++
	}
	return text
}

func SuffixRunes(text string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	start := len(text)
	for i := 0; i < maxRunes && start > 0; i++ {
		_, size := utf8.DecodeLastRuneInString(text[:start])
		if size == 0 {
			break
		}
		start -= size
	}
	return text[start:]
}

// FormatCategorySummary renders a compact category list with an omitted count.
func FormatCategorySummary(categories []string, maxShown, maxLength int) string {
	if maxShown <= 0 || maxLength <= 0 {
		return ""
	}

	normalized := make([]string, 0, len(categories))
	for _, category := range categories {
		category = strings.TrimSpace(category)
		if category == "" {
			continue
		}
		normalized = append(normalized, category)
	}
	if len(normalized) == 0 {
		return ""
	}

	shown := len(normalized)
	if shown > maxShown {
		shown = maxShown
	}
	suffix := ""
	if remaining := len(normalized) - shown; remaining > 0 {
		suffix = " (+" + strconv.Itoa(remaining) + " more)"
	}

	bodyBudget := maxLength - len([]rune(suffix))
	if bodyBudget < 0 {
		bodyBudget = 0
	}
	body := TruncateText(strings.Join(normalized[:shown], ", "), bodyBudget)
	return TruncateText(body+suffix, maxLength)
}

// FormatTimestamp normalizes supported timestamp strings to an explicit UTC timestamp.
func FormatTimestamp(timestamp string) string {
	if timestamp == "" {
		return ""
	}

	if parsed, err := timeutil.ParseFlexibleTimestamp(timestamp); err == nil {
		return timeutil.FormatDateTimeUTC(parsed)
	}

	return timestamp
}

// StripBidiControls removes Unicode bidirectional formatting controls from
// untrusted text: the isolate initiators and terminator U+2066-U+2069 and the
// embedding/override controls U+202A-U+202E. Directional marks (U+200E, U+200F,
// U+061C) open no scope and are kept. Text without such a control is returned
// unchanged.
func StripBidiControls(text string) string {
	if strings.IndexFunc(text, isBidiControl) < 0 {
		return text
	}
	return strings.Map(func(r rune) rune {
		if isBidiControl(r) {
			return -1
		}
		return r
	}, text)
}

func isBidiControl(r rune) bool {
	return (r >= '\u2066' && r <= '\u2069') || (r >= '\u202A' && r <= '\u202E')
}

const percentHexDigits = "0123456789ABCDEF"

// PercentEncodeBidiControls percent-encodes the UTF-8 bytes of every Unicode
// bidirectional-formatting control character in s (the same set
// StripBidiControls removes: U+2066-U+2069, U+202A-U+202E) instead of
// deleting them. It is for a string used as a URL, not as display text:
// deleting a control changes which resource the URL actually names, while
// percent-encoding keeps the request target byte-identical to what an HTTP
// client sends today regardless -- HTTP requires an ASCII request line, so a
// client already percent-encodes any non-ASCII byte, this control's UTF-8
// bytes included, before it goes on the wire. Every other byte, including any
// other non-ASCII character already in s, is left untouched. s without a
// bidi control is returned unchanged.
func PercentEncodeBidiControls(s string) string {
	if strings.IndexFunc(s, isBidiControl) < 0 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if !isBidiControl(r) {
			b.WriteRune(r)
			continue
		}
		var buf [utf8.UTFMax]byte
		n := utf8.EncodeRune(buf[:], r)
		for _, c := range buf[:n] {
			b.WriteByte('%')
			b.WriteByte(percentHexDigits[c>>4])
			b.WriteByte(percentHexDigits[c&0x0F])
		}
	}
	return b.String()
}

// BidiIsolate wraps external natural-language text in Unicode isolate controls.
// Bidi controls already present in the text are removed first, so the emitted
// isolate is always balanced and cannot re-order neighbouring fields.
func BidiIsolate(text string) string {
	if text == "" {
		return ""
	}
	return "\u2068" + StripBidiControls(text) + "\u2069"
}

// BidiIsolateLTR wraps URL-like text in a left-to-right Unicode isolate.
// Bidi controls already present in the text are removed first, so the emitted
// isolate is always balanced and cannot re-order neighbouring fields.
func BidiIsolateLTR(text string) string {
	if text == "" {
		return ""
	}
	return "\u2066" + StripBidiControls(text) + "\u2069"
}

// RedactWebhookSecrets removes webhook secrets from error messages, logs, and
// persisted status fields while keeping enough context to diagnose the failure.
func RedactWebhookSecrets(text string) string {
	if text == "" {
		return ""
	}

	redacted := discordWebhookURLRegex.ReplaceAllStringFunc(text, func(match string) string {
		domain := discordWebhookHost
		if strings.Contains(strings.ToLower(match), discordLegacyWebhookHost) {
			domain = discordLegacyWebhookHost
		}
		return httpsScheme + domain + "/api/webhooks/[redacted]"
	})
	redacted = slackWebhookURLRegex.ReplaceAllStringFunc(redacted, func(match string) string {
		host := slackWebhookHost
		if strings.Contains(strings.ToLower(match), slackGovWebhookHost) {
			host = slackGovWebhookHost
		}
		return httpsScheme + host + "/services/[redacted]"
	})

	return RedactURLCredentials(redacted)
}

// RedactWebhookSecretsForURL redacts standard webhook secrets and additionally
// redacts every occurrence of the exact configured webhook URL, even when
// url.Parse(webhookURL) itself failed -- the case this function exists for:
// text is normally the *url.Error produced by that very failure. When
// parsing succeeds, the known host is used to build a readable,
// host-agnostic marker, and both the raw normalized string AND its
// strconv.Quote-escaped form are replaced with it (the same %q-escaped-form
// pass the failure branch's tier two runs, below, and for the identical
// reason -- see the F3 paragraph further down). When parsing does not
// succeed, a two-tier fallback runs, tier two BEFORE tier one: tier two
// always replaces the raw webhookURL string AND its strconv.Quote-escaped
// form -- fmt's %q verb IS strconv.Quote, so this reproduces, byte for byte,
// whatever (*url.Error).Error() wrote, including a control byte or backslash
// %q rendered as two printable characters -- and is what actually closes the
// leak. Tier one then masks any remaining URL-shaped occurrence in text whose
// own scheme+host (extracted without url.Parse, so it works on text a parser
// has already rejected) match the webhook's; it only adds a readable marker
// and catches an occurrence that is not byte-identical to webhookURL (e.g.
// the same webhook logged twice, or embedded in a longer message).
//
// The order is load-bearing, not cosmetic -- DO NOT swap it back, and do not
// call RedactWebhookSecrets on text before either tier runs: RedactWebhookSecrets
// itself masks known-host (Discord/Slack/Slack-Gov) URLs with the same
// unescaped-by-%q character class ([^\s"'<)]+) genericHTTPURLRegex uses, so
// when webhookURL's host is one of those, calling RedactWebhookSecrets first
// mutates and truncates the SAME occurrence tier two needs to match
// byte-for-byte, exactly like a tier-one-before-tier-two ordering bug would --
// the secret-bearing tail would then survive both tiers because neither the
// raw nor the %q form of the full webhookURL exists in the already-mutated
// text any more. Both tiers below therefore run against the untouched `text`
// first, and RedactWebhookSecrets only runs on their combined result -- late
// enough to still catch an unrelated secret elsewhere in the same message,
// too late to ever see (and truncate) the anchored occurrence first. The same
// reasoning applies to a webhookURL containing an unescaped ", ', < or )
// before the secret even on a non-built-in host: a tier-one-first pass would
// match only the prefix up to that character and leave the tail for a tier
// two that can then no longer find it.
//
// When webhookURL itself is a degenerate, non-URL-shaped anchor, tier two's
// unconditional ReplaceAll rewrites every occurrence of that exact substring
// anywhere in text, including inside unrelated prose. Not reachable from any
// of the three call sites in this codebase today via webhookURL ITSELF being
// degenerate -- each always passes a real, if malformed, webhook or feed URL
// -- but worth knowing before reusing this helper with an arbitrary anchor
// string.
//
// CORRECTED, F7, P3 (found 2026-09-04, sixth round on this defect class):
// the paragraph above is true of webhookURL as a whole, but was wrongly read
// as covering every anchor tier two derives from it, including tier 2c's
// preFragment (below). A real, non-degenerate webhookURL/rawURL can still
// yield a DEGENERATE preFragment: "h#ttps://feeds.example.test/rss" is a
// genuine, reachable config value (an operator typo, not a contrived input),
// and its preFragment is the one-character string "h" -- tier 2c's
// unconditional ReplaceAll(text, "h", marker) then rewrote every "h" in the
// entire message, corrupting "https", "scheme" and every other word
// containing one. Reproduced end to end via validateFeedURL: feed
// "h#ttps://feeds.example.test/rss" produced "feed URL must use [redacted
// URL]ttp or [redacted URL]ttps sc[redacted URL]eme" instead of a readable
// error. Not a leak (the runtime tests below still confirm no secret ever
// escapes this pass), but the message becomes useless for diagnosis. Fixed
// by gating tier 2c on preFragment still looking URL-shaped (see its own
// comment below) rather than merely non-empty.
//
// F3, P2 (found 2026-09-04, third instance of this same defect class): the
// SUCCESS branch used to do only the two byte-exact ReplaceAll passes above
// with no strconv.Quote counterpart, even though text is exactly as likely
// to be %q-rendered here as in the failure branch -- both are
// (*url.Error).Error() output, the only difference is whether
// url.Parse(webhookURL) itself succeeded. A webhookURL containing a
// backslash or double quote (the two bytes %q escapes inside a URL) that
// reaches this branch therefore used to survive unredacted. Fixed by giving
// the success branch the identical strconv.Quote pass (factored into
// replaceQuotedForm, below, since it is now needed three times: here for
// normalized, here again for rawURL.String() when RawPath != "", and inline
// in the failure branch's tier two, left untouched to avoid an unrelated
// refactor of already-reviewed code).
//
// Proven unreachable via a *url.Error genuinely produced by
// (*http.Client).Do (the codebase's only two ...ForURL call sites that can
// ever reach this branch, internal/slack/webhook.go doSlackWebhookRequest):
// net/http builds that error's URL field from the already-parsed
// *url.URL's own String()/Redacted() re-serialization, which always
// percent-escapes a backslash or double quote in the path (to %5C / %22)
// before any %q formatting ever sees it -- verified experimentally
// 2026-09-04. So a real transport failure can never carry an unescaped
// backslash or quote into text at all; the existing rawURL.String() pass
// (itself now protected the same way, for defense in depth) already matches
// the percent-escaped form with a plain, unquoted ReplaceAll. The bug is
// real and pinned below regardless, reached directly by constructing a
// *url.Error{Op, URL, Err} the way this function's own contract allows any
// caller to (and the way the fallback branch's tests already do for the
// failure case) -- a future caller that formats an error differently, or
// this function reused with a different anchor source, would hit it live.
// It is also reached today, indirectly, through this function's own
// strings.TrimSpace(webhookURL) below: a webhookURL whose only parse-failing
// byte is a trailing whitespace-class control byte (e.g. a tab) fails
// url.Parse on the untrimmed text.URL but SUCCEEDS once trimmed, crossing
// from the (already-protected) failure branch into the (until this fix,
// unprotected) success branch -- reproduced for the double-quote delimiter
// specifically (the only one of ")'<" that %q escapes) by
// TestRedactWebhookSecretsForURLDelimiterBeforeControlByteQuoteCrossesIntoSuccessBranch
// in webhook_redact_success_test.go.
//
// Three other places this function normalizes an anchor before comparing it
// against text, audited for the same class of defect:
//   - strings.TrimSpace(webhookURL), immediately below, has no matching trim
//     applied to text. Today every reachable caller (the three
//     config.go webhook validators, and internal/slack/webhook.go, which
//     only ever receives a webhookURL already trimmed by
//     config.webhookEndpointConfigs at load time) passes an already-trimmed
//     webhookURL, so the trim below is a no-op against every live caller and
//     cannot, by itself, desync the anchor from text in production. It is
//     NOT provably safe in general, though: the TrimSpace-crosses-branches
//     case in the paragraph above is reached by calling this function
//     directly with an untrimmed webhookURL/text pair, which is exactly what
//     a future caller that does not pre-trim would do. Left as-is rather
//     than fixed here -- removing the trim would also remove the "an
//     all-whitespace webhookURL means no anchor" check that reuses the same
//     variable, widening this fix beyond the success-branch gap it targets.
//   - rawURL.String() (success branch, when parsed.RawPath != "") is a
//     second, independently-computed anchor, byte-different from normalized
//     whenever RawPath needed re-escaping. It is exactly as exposed to the
//     %q mismatch as normalized is, for the same reason, and now gets the
//     same replaceQuotedForm pass.
//   - RedactURLCredentials's parsed.String() (a different function, defined
//     below) is NOT an anchor and is provably safe from this defect class:
//     ReplaceAllStringFunc calls its callback once per genericHTTPURLRegex
//     match and substitutes the callback's return value directly at that
//     match's location, so parsed.String() is the REPLACEMENT text, never a
//     second needle searched for byte-exact equality against some other
//     text. There is no anchor/text pair here that a %q mismatch (or any
//     other re-encoding) could desync.
//
// KNOWN RESIDUAL, and the condition that would make it matter. This function
// substitutes its marker into text with ReplaceAll. The generic URL regex that
// runs afterwards matches the marker PLUS any URL bytes that happen to follow
// it in text, and once that combined span parses, the unparsable-match collapse
// does not fire -- so those trailing bytes survive verbatim. That is true on
// both the success and the failure branch and for every host, not only the
// hardcoded ones, so it is a property of the substitution rather than of any
// one branch; fixing a single branch would reduce exposure by nothing while
// looking like a fix.
//
// It is currently unreachable from every caller for one specific reason: each
// one passes a (*url.Error).Error(), whose URL is %q-quoted, so the regex match
// stops at the closing quote before it can reach anything else.
//
// If you add a caller whose text is NOT quoted that way -- a raw response body,
// a hand-built message, a wrapped error that drops the quoting -- that reason
// disappears and a secret following the marker will be logged verbatim. Check
// that before adding one.
func RedactWebhookSecretsForURL(text, webhookURL string) string {
	normalized := strings.TrimSpace(webhookURL)
	if text == "" || normalized == "" {
		return RedactWebhookSecrets(text)
	}
	parsed, err := url.Parse(normalized)
	if err == nil && parsed.Scheme != "" && parsed.Host != "" {
		marker := parsed.Scheme + "://" + parsed.Host + redactedWebhookPath(parsed.Path)
		// F5, P2 (found 2026-09-04, sixth round on this defect class): this
		// SUCCESS branch trusted parsed.Host/parsed.Path unconditionally --
		// the FAILURE branch's rawWebhookSchemeHost (F1, above) exists
		// precisely because net/url's authority-truncation-at-first-'/' bug
		// can hand back a userinfo credential AS the host, but that bug does
		// not require url.Parse to fail: a userinfo shaped like
		// "TOKEN/pathlike@realhost" with no ':' in it parses as a perfectly
		// valid bare hostname ("TOKEN") with parsed.User == nil, so
		// url.Parse SUCCEEDS -- there is no *url.Error here for tier two's
		// raw/%q ReplaceAll passes to even run against, and the credential
		// reaches the marker directly, unredacted, on every call (an
		// ordinary, error-free success, not just a validation failure).
		// realAuthorityCandidate reruns the identical last-'@'-wins raw scan
		// on normalized and disagrees with parsed.Host exactly when this
		// shape is present; trust its answer over parsed.Host/parsed.Path in
		// that case. See realAuthorityCandidate's own doc comment for why
		// the query/fragment are excluded from that scan (so an unrelated
		// '@' in a query value, e.g. an email address, cannot false-positive
		// this and downgrade an already-correct parsed.Host).
		if rawScheme, rawHost, mismatched := realAuthorityCandidate(normalized, parsed); mismatched {
			marker = redactedURLMarker
			if isLogSafeHost(rawHost) {
				marker = rawScheme + "://" + rawHost + redactedPathMarker
			}
		}
		redacted := strings.ReplaceAll(text, normalized, marker)
		// Same %q-escaped-form pass the fallback's tier two runs below, and
		// for the identical reason: text is normally (*url.Error).Error(),
		// which renders with %q, so a normalized containing a backslash or
		// double quote survives into text as two printable characters (\\ or
		// \") that the raw ReplaceAll above cannot match. See the doc
		// comment above for why this branch needed its own pass (F3, found
		// 2026-09-04, third instance of this defect class).
		redacted = replaceQuotedForm(redacted, normalized, marker)
		if parsed.RawPath != "" {
			rawURL := *parsed
			rawURL.RawQuery = ""
			rawURL.Fragment = ""
			rawURLString := rawURL.String()
			redacted = strings.ReplaceAll(redacted, rawURLString, marker)
			redacted = replaceQuotedForm(redacted, rawURLString, marker)
		}
		return RedactWebhookSecrets(redacted)
	}
	// url.Parse could not make sense of this exact string -- callers only
	// reach this branch redacting the *url.Error text produced when
	// url.Parse(webhookURL) itself failed, so a second url.Parse call on the
	// same input can never succeed either, and a byte-for-byte search for
	// normalized inside text would usually miss: (*url.Error).Error() renders
	// its URL field with %q, which escapes exactly the control byte or
	// backslash that made parsing fail, so the raw and %q-escaped forms of
	// the tail differ byte-for-byte even though they print almost
	// identically.
	// F4, P3 (found 2026-09-04): this used to read "[redacted webhook URL]"
	// unconditionally, including on the FEED path (config.go's
	// validateFeedURL/canonicalFeedURL both anchor on a feed URL, not a
	// webhook one) -- not a leak, but an operator reading `feed
	// 'general_feeds[0]' URL is not valid: ... parse "[redacted webhook
	// URL]": ...` in bot.log sees a webhook label on a feed failure, wrong on
	// its face. Two fixes were weighed: adding a parameter so each of this
	// function's 8 call sites (internal/api, internal/config x5,
	// internal/slack x2) supplies its own wording, or renaming the exported
	// function family to something not webhook-specific. Both touch every
	// call site (a new parameter needs a value at each one; a rename needs
	// the identifier updated at each one) plus, for the rename, ~20 existing
	// test names in this package that spell out the current name -- sizeable
	// surface for a P3 cosmetic fix, and CLAUDE.md's working rules ask for no
	// unrelated renames inside a security-fix pass. Made the marker text
	// itself context-free instead: dropping "webhook" needs no caller to
	// change anything and reads correctly on every existing and future call
	// site, feed or webhook alike.
	marker := redactedURLMarker
	scheme, host, ok := rawWebhookSchemeHost(normalized)
	if ok && isLogSafeHost(host) {
		marker = fallbackWebhookMarker(scheme, host)
	}
	// Tier 2 runs FIRST, against the untouched text -- see the "order is
	// load-bearing" paragraph in the doc comment above for why. It replaces
	// the raw form AND the %q-escaped form that (*url.Error).Error() actually
	// writes. fmt's %q verb is strconv.Quote, so quoting normalized and
	// dropping the outer quotes reproduces those bytes exactly, whatever made
	// the parse fail -- this is what covers a scheme genericHTTPURLRegex does
	// not know (e.g. ftp://, or a mistyped htps://), an authority %q rewrote
	// (a backslash or control byte inside the host), a case difference
	// (HTTPS:// vs the regex's http(s)://), and a string with no scheme at
	// all.
	redacted := strings.ReplaceAll(text, normalized, marker)
	if quoted := strconv.Quote(normalized); len(quoted) >= 2 {
		redacted = strings.ReplaceAll(redacted, quoted[1:len(quoted)-1], marker)
	}
	// Tier 2b, same pass, second half of the F1 fix: neither the raw nor the
	// %q-escaped ReplaceAll above can ever reach a credential fragment that
	// net/url embedded in its own INNER error detail rather than in the URL
	// field -- e.g. parseHost's `invalid port %q after host` (net/url/url.go),
	// whose %q'd "port" value is itself a raw slice of normalized whenever the
	// authority-truncation bug above put a slash-containing userinfo password
	// where a port was expected. That detail is rendered with a plain %s by
	// (*url.Error).Error() ("%s %q: %s", url.go:40), so it reaches text
	// byte-for-byte; redactURLErrorSubstringFragments finds it via err (the
	// SAME *url.Error this function's own url.Parse(normalized) call above
	// just produced) rather than by pattern-matching net/url's wording, and
	// only touches a quoted fragment that is provably a substring of
	// normalized, so an unrelated quoted detail elsewhere is left readable.
	redacted = redactURLErrorSubstringFragments(redacted, err, normalized)
	// Tier 2c, same pass, third channel found while attacking the F1 fix
	// above: url.Parse itself (net/url/url.go's exported Parse, the function
	// that produced err from this function's own url.Parse(normalized) call)
	// cuts rawURL at the FIRST "#" before doing anything else -- unconditionally,
	// regardless of whether that "#" sits in a fragment, a query value or an
	// unescaped userinfo password -- and on failure sets (*url.Error).URL to
	// that pre-"#" prefix, NOT to the original rawURL. So when normalized
	// contains a "#" and the pre-"#" prefix itself fails to parse (e.g. the
	// same authority-truncation bug hits it), text's quoted URL field is only
	// that PREFIX, a proper substring of normalized rather than normalized
	// itself, and the raw/%q-escaped whole-string passes above -- which only
	// ever search for the complete normalized -- can never match a substring.
	// The prefix is still credential-bearing on its own (e.g.
	// "https://svc:TOK111" out of
	// "https://svc:TOK111#HASH@host.example.test/rss"), so it needs the same
	// raw+%q replacement, anchored on the correct marker already computed
	// above from the FULL normalized (rawWebhookSchemeHost's own scan is pure
	// string slicing on normalized, untouched by net/url's cut, so it already
	// finds the real host on the far side of the "#").
	//
	// F7, P3 (found 2026-09-04, sixth round on this defect class): the
	// original guard here was only "hasFragment && preFragment != ''" -- any
	// non-empty prefix, no shape check at all. When the "#" sits VERY early
	// (before the scheme's own "://", e.g. "h#ttps://feeds.example.test/rss"),
	// preFragment is a one- or two-character fragment ("h") that is in no
	// sense credential-bearing, and strings.ReplaceAll(redacted, "h", marker)
	// then rewrote every "h" anywhere in the message -- "https" and "scheme"
	// included -- producing an unreadable error instead of a redacted one.
	// Not a leak (nothing here ever un-redacts a secret; it corrupts
	// unrelated prose instead), but it defeats the whole point of keeping
	// enough context to diagnose the failure. Fixed by additionally
	// requiring preFragment to still look URL-shaped -- contain "://" -- the
	// same minimal bar rawWebhookSchemeHost/isValidURLScheme already apply
	// before trusting a raw-scanned scheme elsewhere in this file. A
	// preFragment built from a genuinely credential-bearing anchor (e.g.
	// "https://svc:TOK111" out of
	// "https://svc:TOK111#HASH@host.example.test/rss", the case this tier
	// exists for) always contains "://" and is unaffected by the added
	// check.
	if preFragment, _, hasFragment := strings.Cut(normalized, "#"); hasFragment && strings.Contains(preFragment, "://") {
		redacted = strings.ReplaceAll(redacted, preFragment, marker)
		if quoted := strconv.Quote(preFragment); len(quoted) >= 2 {
			redacted = strings.ReplaceAll(redacted, quoted[1:len(quoted)-1], marker)
		}
	}
	// Only now does the general-purpose pass run -- late enough that it
	// cannot have already truncated the anchored occurrence tier 2 above just
	// replaced, early enough to still catch any other webhook secret (a
	// different malformed URL, a query-string token) elsewhere in the same
	// message.
	redacted = RedactWebhookSecrets(redacted)
	if ok && isLogSafeHost(host) {
		// Tier 1 runs SECOND, after tier 2 and RedactWebhookSecrets, on
		// whatever text they left behind: mask every remaining URL-shaped
		// occurrence in text whose own scheme+host (extracted the same raw
		// way) match the webhook's -- catches an occurrence that is not
		// byte-identical to webhookURL or its %q form (e.g. the same webhook
		// logged twice, or embedded in a longer message), which tier 2 above
		// cannot match.
		redacted = genericHTTPURLRegex.ReplaceAllStringFunc(redacted, func(match string) string {
			matchScheme, matchHost, matchOK := rawWebhookSchemeHost(match)
			if !matchOK || !strings.EqualFold(matchScheme, scheme) || !strings.EqualFold(matchHost, host) {
				return match
			}
			return marker
		})
	}
	return redacted
}

// replaceQuotedForm additionally replaces anchor's strconv.Quote-escaped form
// (outer quotes stripped) with marker inside text, on top of whatever a
// plain byte-exact ReplaceAll(text, anchor, marker) already caught. fmt's %q
// verb IS strconv.Quote, so this reproduces byte-for-byte whatever
// (*url.Error).Error() -- or any other %q-formatted text -- wrote for
// anchor, catching an occurrence %q rendered as two printable characters
// (backslash doubled to \\, a double quote escaped to \") that a raw,
// unescaped ReplaceAll cannot match. strconv.Quote's output is always at
// least two bytes (a matched pair of quote characters, even for an empty
// anchor), so slicing off the outer quotes never underflows and there is no
// length guard to keep dead. Used by RedactWebhookSecretsForURL's
// url.Parse-SUCCESS branch; the FAILURE branch below inlines the identical
// strconv.Quote pass directly (with its own, pre-existing length guard)
// rather than calling this helper, so as not to touch that already-reviewed,
// already-tested code for an unrelated refactor.
func replaceQuotedForm(text, anchor, marker string) string {
	quoted := strconv.Quote(anchor)
	return strings.ReplaceAll(text, quoted[1:len(quoted)-1], marker)
}

// redactURLErrorSubstringFragments redacts any %q-quoted fragment inside
// err's wrapped detail message that net/url built from a raw slice of
// secret.
//
// F1, P2 (found 2026-09-04), second half: net/url's parseHost/parseAuthority
// (net/url/url.go) return detail errors like `invalid port %q after host` or
// `invalid character %q in host name`, built with fmt.Errorf("...%q...",
// fragment) where fragment is a raw slice of the very string that failed to
// parse. (*url.Error).Error() renders as "%s %q: %s" (url.go:40) -- only the
// URL field gets %q; e.Err (this detail) is written with a plain %s, so its
// own internal %q-quoting reaches text byte-for-byte, unescaped a second
// time. That detail sits OUTSIDE the URL field the rest of
// RedactWebhookSecretsForURL redacts, and the leaking fragment is normally
// shorter than the whole secret (e.g. just the "port" slice of a userinfo
// password containing an unescaped '/'), so neither the raw nor the
// %q-escaped whole-string ReplaceAll passes above can ever match it: both
// search for normalized in full, this fragment is only a piece of it.
//
// err is expected to be the *url.Error RedactWebhookSecretsForURL's own
// url.Parse(secret) call just produced (nil in the success branch, where
// there is nothing to extract), so its detail is guaranteed to correspond to
// secret -- no independent re-parsing or wording match against net/url's
// text is needed.
//
// F6, P2 (found 2026-09-04, sixth round on this defect class, live and
// confirmed on both the feed and webhook path): the original implementation
// scanned detail with a regexp, `"[^"]*"`, for anything quote-delimited, then
// checked whether the INNER bytes were a substring of secret. That primitive
// cannot span a %q-escaped quote: fmt's %q escapes a literal '"' inside the
// fragment to two bytes (\"), so a userinfo password containing one produces
// a detail like `invalid port ":PWDKKK\"1" after host` -- the regex's
// `[^"]*` class stops at the FIRST bare '"', which is the escaped quote's own
// second byte, not the fragment's real closing delimiter, so it matches only
// `":PWDKKK\"` (missing the trailing `1" `). strconv.Unquote then fails on
// that truncated, syntactically-broken candidate (an unterminated escape),
// the raw fallback (the regex's own inner bytes) is not a substring of
// secret either -- it too is missing the closing quote and everything after
// it -- and the whole fragment, port value and all, survives untouched.
// Reproduced end to end on both paths with a literal '"' in the userinfo
// password:
//
//	feed:    parse "https://feeds.example.test/[redacted]": invalid port ":PWDKKK\"1" after host
//	webhook: parse "https://hooks.slack.com/[redacted]": invalid port ":WHSECRET\"9" after host
//
// Fixed by dropping the regex entirely and working the other direction, per
// review: every fragment net/url can embed here (parseHost's colonPort,
// InvalidHostError's single invalid byte, EscapeError's malformed
// percent-escape) is, by construction, a raw contiguous slice of the string
// net/url was given -- see net/url/url.go: colonPort is `host[i:]`, itself
// carved out of the authority slice of secret; InvalidHostError is
// `s[i:i+1]`; EscapeError is `s[i:i+3]` or the whole remaining `s` -- so
// instead of parsing detail for something quote-shaped and checking it
// against secret backwards, every contiguous substring of secret is quoted
// FORWARD with strconv.Quote (the exact transform net/url's own %q verb
// applied) and checked for literal presence in detail. This needs no regex
// and no dependency on net/url's wording: it works for any future message
// shape emitting a %q-rendered raw slice of secret, not just the three known
// today, and it is exact regardless of what bytes the fragment itself
// contains -- there is no unescaping step that can fail or truncate, because
// nothing is ever parsed back out of detail.
//
// Requiring the search string to include BOTH of strconv.Quote's own
// wrapping quote characters (not just the inner bytes) is what keeps this
// precise rather than prone to false positives: a net/url detail message
// never contains more than the one %q-rendered dynamic span, so a shorter or
// longer candidate's quoted form can only ever match by being byte-identical
// to that one real span. A partial candidate cannot "start matching" inside
// it and stop early, because the very next byte after the candidate's own
// content would have to be a bare closing quote, and %q escapes any literal
// quote inside the fragment itself so it can never masquerade as that
// delimiter (this is the same property that lets replaceQuotedForm and the
// other %q passes in this file search unambiguously). Case-sensitive by
// construction (strings.Contains), which is correct, not incidental: every
// fragment net/url embeds is an exact-case raw slice of secret, so a
// same-case candidate always matches a genuine fragment and a
// differently-cased text never falsely matches a candidate that was never
// actually there -- pinned directly in
// TestRedactURLErrorSubstringFragmentsGuardBranches's "case-sensitive"
// subtest.
//
// The inner loop is bounded by len(detail), not len(secret): quoting a byte
// never shrinks the output, so length is non-decreasing as the candidate
// grows, and once a candidate's own quoted length already exceeds detail's
// length it -- and every longer candidate from the same starting point --
// cannot possibly be found inside it, so the loop breaks out of that
// starting position rather than continuing to quote longer, already-hopeless
// candidates.
//
// P2 (found 2026-09-04, performance review): the comment above previously
// claimed total work of O(len(secret) * len(detail)), counting only loop
// *iterations* -- it missed that each iteration's own strconv.Quote and
// strings.Contains calls are themselves O(len(quoted)), not O(1), and quoted
// grows with (end-start). Summed correctly, for a fixed start the inner loop
// does O(len(detail)) iterations (the break above), each costing up to
// O(len(detail)) for the Quote+Contains pair, so one start costs
// O(len(detail)^2) and the total is O(len(secret) * len(detail)^2). That is
// merely linear in secret's length for a short, FIXED detail (harmless --
// see TestRedactURLErrorSubstringFragmentsHandlesLongSecretShortDetail below,
// a long secret against a short, unrelated detail, which this fix must leave
// exactly as fast and exactly as unredacted as before). It is cubic in the
// combined input size whenever detail scales with secret, which it does for
// net/url's colonPort detail (see F1 above, built from a raw slice of
// secret's own authority): measured, an adversarial secret of
// 264/514/1014/2014 bytes (http://0:S@0 followed by a run of colons, one
// byte over each doubling) took roughly 14ms/100ms/790ms/7.5s on 2026-09-04
// hardware.
//
// validateFeedURL (internal/config) and the webhook URL validators all
// funnel here through RedactWebhookSecretsForURL/RedactWebhookErrorForURL on
// every config load and hot-reload-triggered reparse, so an operator typo or
// a corrupted config value that happens to produce a long, colon-heavy
// authority turns a config load into a multi-second stall -- self-inflicted
// (feed/webhook URLs are operator-controlled, not attacker-reachable), but
// unbounded: nothing upstream caps a feed or webhook URL's length, and this
// function's own contract ("short, operator-controlled strings ... not a hot
// loop") does not enforce what it assumes.
//
// Fixed with a work budget on the O(len(secret) * len(detail)^2) cost
// derived above, rather than a rewrite of the matching logic itself or a
// cap on secret's length alone: a length-only cap conflates "long secret,
// long detail" (genuinely cubic, must be bounded) with "long secret, short
// detail" (linear and cheap regardless of secret's length -- exactly
// TestRedactURLErrorSubstringFragmentsHandlesLongSecretShortDetail's shape,
// a 4000-byte secret against a 33-byte detail; a length-only cap flagged
// that case too and broke it in early testing). Below the budget, this
// loop's decision procedure (which candidate substrings get redacted, and
// the exact sequence of ReplaceAll calls that produces text) is completely
// unchanged, so behavior for every realistic input -- and every case this
// defect class's six earlier rounds pinned -- is byte-for-byte identical to
// before this fix.
//
// Past the budget, this does NOT fall back to "leave text alone": detail can
// still carry a raw, unredacted slice of secret (that is the entire reason
// this tier exists -- see F1 above), and simply skipping the loop would
// silently un-fix that leak for exactly the pathological inputs most likely
// to be adversarially or accidentally malformed. Read the wrong lesson from
// "preserve behaviour exactly" here and this fix trades a slowness bug for a
// confidentiality one -- caught in manual testing while developing this fix
// (an early length-only-cap version left the password verbatim in the output
// once secret exceeded the cap). Instead, since detail is guaranteed to
// appear verbatim in text (text is (*url.Error).Error(), which writes detail
// with a plain %s -- see url.go:40, quoted in the F1 comment above), the
// whole detail substring is redacted in one linear ReplaceAll: safe
// regardless of where inside detail a secret-derived fragment sits (no
// per-candidate search needed), and cheap regardless of secret's length
// (O(len(text)+len(detail)), no nested loop). The cost is precision, not
// safety: past-budget input loses whatever non-secret diagnostic text detail
// also carried (e.g. "invalid port ... after host" alongside the leaking
// slice) rather than only the leaking fragment -- an acceptable, already
// self-inflicted-and-pathological-input-only tradeoff, the same one F7 above
// already accepted (corrupts readability, never leaks). Tiers 1, 2 and 2c in
// RedactWebhookSecretsForURL still separately redact the whole URL-shaped
// occurrence of secret, its %q form, and the pre-fragment-truncated URL
// field, so past-budget input is in practice redacted by multiple
// overlapping tiers, not just this fallback.
//
// maxFragmentWorkUnits is chosen from the measurements above: the
// self-referential adversarial shape (detail scaling with secret) costs
// roughly len(secret)^3 units and took ~100ms at len(secret)=511
// (511^3 ~= 1.33e8), so a 1.5e8 budget keeps the slowest input this function
// still runs the real loop for at that same order of magnitude (~100ms,
// once, at config load), while a 4000-byte secret against a 33-byte detail
// (4000*33^2 ~= 4.4e6) stays far under budget and keeps running the
// unmodified, byte-exact loop.
const maxFragmentWorkUnits = 150_000_000

// maxFragmentDetailLenForBudgetCheck bounds len(detail) before it is squared
// to compute the work estimate below, so that product can never overflow
// int64 regardless of how large a detail net/url (or a future caller) hands
// this function: 60,000^2 = 3.6e9, already far past maxFragmentWorkUnits on
// its own, so any detail at or beyond this length goes straight to the
// budget-exceeded fallback without the multiplication ever running.
const maxFragmentDetailLenForBudgetCheck = 60_000

func redactURLErrorSubstringFragments(text string, err error, secret string) string {
	if err == nil || secret == "" {
		return text
	}
	urlErr, ok := err.(*url.Error)
	if !ok || urlErr.Err == nil {
		return text
	}
	detail := urlErr.Err.Error()
	if detail == "" {
		return text
	}
	if len(detail) > maxFragmentDetailLenForBudgetCheck ||
		int64(len(secret))*int64(len(detail))*int64(len(detail)) > maxFragmentWorkUnits {
		return strings.ReplaceAll(text, detail, redactedValueMarker)
	}
	for start := 0; start < len(secret); start++ {
		maxEnd := len(secret)
		if start+len(detail) < maxEnd {
			maxEnd = start + len(detail)
		}
		for end := start + 1; end <= maxEnd; end++ {
			quoted := strconv.Quote(secret[start:end])
			if len(quoted) > len(detail) {
				break
			}
			if strings.Contains(detail, quoted) {
				text = strings.ReplaceAll(text, quoted, `"[redacted]"`)
			}
		}
	}
	return text
}

func redactedWebhookPath(path string) string {
	if strings.HasPrefix(path, "/services/") {
		return "/services/[redacted]"
	}
	if strings.HasPrefix(path, "/api/webhooks/") {
		return "/api/webhooks/[redacted]"
	}
	return redactedPathMarker
}

// knownWebhookHosts restates, as plain strings, the same four-host allowlist
// discordWebhookURLRegex/slackWebhookURLRegex already hardcode (see their own
// doc comment above) -- a caller here already has a raw host substring in
// hand (from rawWebhookSchemeHost) and needs an exact, case-insensitive
// comparison against it, not a full regex match.
var knownWebhookHosts = [...]string{
	discordWebhookHost,
	discordLegacyWebhookHost,
	slackWebhookHost,
	slackGovWebhookHost,
}

// knownWebhookHostPrefix reports whether host -- a raw authority string
// rawWebhookSchemeHost extracted, which may carry a trailing ":<anything>"
// suffix that made url.Parse fail (an invalid port, or any other content) --
// begins with exactly one of the four hardcoded webhook hosts followed by a
// ':'. When it does, hostname is that known host alone (in its canonical
// lowercase spelling) and ok is true; the caller uses this to DROP the
// suffix from what it echoes, never to keep any part of it.
//
// Found 2026-09-04 while fixing a diagnostic regression: RedactWebhookSecretsForURL's
// url.Parse-failure marker (immediately above) echoes host in full, port
// suffix included, whenever isLogSafeHost accepts it -- correct and useful
// when the suffix genuinely is an operator's typo'd port (e.g.
// "hooks.slack.com:bad"), but that marker is unconditionally re-scanned by
// this function's own trailing RedactWebhookSecrets(redacted) call
// (RedactURLCredentials's url.Parse-failure fallback specifically), which
// fails to parse the SAME invalid port a second time and collapses the
// entire marker -- host included -- to the generic "[redacted URL]"
// placeholder, discarding the one thing the operator needs: which
// destination failed.
//
// The fix is not "protect the marker from that second pass" in general --
// tried first, in a scratch copy, and rejected: nothing about host's raw,
// unbounded suffix distinguishes "genuine port typo" from "the tail of a
// credential with no closing delimiter to stop the scan". Constructed live:
// webhookURL "https://TOKEN:SECRETVALUE/path" makes url.Parse treat TOKEN as
// host and SECRETVALUE as an invalid port; rawWebhookSchemeHost returns
// host = "TOKEN:SECRETVALUE" (it does not stop at ':' either), isLogSafeHost
// accepts it (no control bytes), and an unconditionally-protected marker
// would read literally "https://TOKEN:SECRETVALUE/[redacted]" -- the secret
// in the clear. TODAY'S demolition bug is what currently prevents that
// leak; a blanket "never demolish" fix would remove that protection.
//
// What makes dropping the suffix safe here, and ONLY here, is that the part
// BEFORE the colon is checked against a fixed, four-entry, compile-time
// literal list -- never against anything the operator supplies. This is
// deliberately NOT the rejected "echo the host only when the authority
// looks plausible (reject a ':' whose remainder is not all digits)" gate: that
// gate inspects the unbounded SUFFIX's shape and would reject the reported
// defect's own repro (":bad" is not digits, which is why url.Parse failed in
// the first place); this function never inspects the suffix's content at
// all -- it is always discarded once the prefix matches, and the safety
// comes entirely from the prefix being one of four values fixed in source,
// not from any property of what follows it. A length- or entropy-based cut
// on the suffix was considered and also rejected: no threshold is
// justifiable (a short credential and a long port typo are both reachable),
// and this design needs no threshold, because it never looks at the suffix
// to begin with.
func knownWebhookHostPrefix(host string) (hostname string, ok bool) {
	prefix, _, hasColon := strings.Cut(host, ":")
	if !hasColon {
		return "", false
	}
	for _, known := range knownWebhookHosts {
		if strings.EqualFold(prefix, known) {
			return known, true
		}
	}
	return "", false
}

// fallbackWebhookMarker builds RedactWebhookSecretsForURL's url.Parse-failure
// marker from an already-extracted, already-log-safe scheme and host,
// dropping any raw port/suffix on host when its prefix names one of the four
// hardcoded webhook hosts (knownWebhookHostPrefix, above). Split out of
// RedactWebhookSecretsForURL itself purely to keep that function's
// cyclomatic complexity within the project's lint budget; it has no
// independent branch of its own that the caller's isLogSafeHost/ok guard
// does not already cover.
func fallbackWebhookMarker(scheme, host string) string {
	marker := scheme + "://" + host + redactedPathMarker
	if hostname, known := knownWebhookHostPrefix(host); known {
		marker = scheme + "://" + hostname + redactedPathMarker
	}
	return marker
}

// isLogSafeHost reports whether host can be echoed into a log line verbatim.
// rawWebhookSchemeHost's authority scan (below) terminates on any r <= ' '
// (which covers every control byte, including the ones a wrapped copy-paste
// embeds) or 0x7f, so those can never reach the returned host; a backslash or
// a double quote terminates neither the scan nor %q's own escaping, so those
// are the only two bytes that can reach here, and without this guard tier
// one's marker would print one of them straight into bot.log instead of
// withholding it.
func isLogSafeHost(host string) bool {
	if host == "" {
		return false
	}
	for _, r := range host {
		if r < 0x20 || r == 0x7f || r == 0x22 || r == 0x5c {
			return false
		}
	}
	return true
}

// rawWebhookSchemeHost extracts the scheme and host from a URL-shaped string
// using string operations only, never url.Parse. Its original (and still
// primary) caller is RedactWebhookSecretsForURL's fallback, which exists
// precisely because url.Parse already failed once on this class of input;
// realAuthorityCandidate (below), added for F5, also calls it on text that
// url.Parse successfully parsed, specifically to check whether net/url's own
// answer can be trusted at all. Userinfo (a "user:pass@" or
// bare "token@" prefix on the authority) is recognised and dropped -- only
// what follows the "@" is treated as host-candidate territory, so a
// credential embedded there never reaches the returned host string. Reports
// false when no "scheme://" prefix, or no host after it, can be found.
//
// F1, P2 (found 2026-09-04, on both the feed and the webhook path): this used
// to bound its authority scan to the first of '/', '?', '#' etc. BEFORE
// looking for "@", the same simplification net/url's own parseAuthority
// makes (net/url/url.go, Parse -> parseAuthority: authority is the substring
// up to the first of those bytes, full stop). A userinfo password containing
// an unescaped '/', '?' or '#' -- routine for a base64 token, whose alphabet
// includes '/' -- then has its own '@' delimiter sitting AFTER that
// naively-computed authority end, so no '@' is ever found inside it and the
// credential prefix ("svc:B64TOKEN" out of
// "svc:B64TOKEN/SLASHSECRET777@feeds.example.com") was returned as "host"
// and echoed straight into the marker. Reproduced end to end against a real
// config directory, the real rotating logger and config.LoadConfig: bot.log
// carried `parse "https://svc:B64TOKEN/[redacted]": invalid port
// ":B64TOKEN" after host`, the leak surviving both tiers because it sits
// inside the newly-invented "host", not in the byte-exact webhookURL/feed-URL
// string either tier searches for.
//
// Fixed by reversing the order: search the WHOLE remainder for "@" FIRST
// (strings.LastIndex, so the LAST "@" wins if userinfo itself contains one),
// and only bound the authority-stop scan on what follows it. However many
// stop bytes the userinfo before that "@" contains no longer matters -- they
// are never looked at. When rest has no "@" at all, this is exactly the old
// single-pass behaviour (no userinfo to strip). The one traded-off case is a
// rest with no real userinfo at all but an unrelated "@" further out (e.g.
// inside a query value): LastIndex then finds that "@" instead and returns a
// shorter, wrong-but-still-safe "host" fragment of the query -- host is only
// ever used to build a cosmetic, log-safety-gated marker (isLogSafeHost,
// below), never as a byte-exact search anchor, so this costs marker accuracy
// on a rare shape, never a leak.
func rawWebhookSchemeHost(raw string) (scheme, host string, ok bool) {
	schemeEnd := strings.Index(raw, "://")
	if schemeEnd <= 0 {
		return "", "", false
	}
	scheme = raw[:schemeEnd]
	if !isValidURLScheme(scheme) {
		return "", "", false
	}
	rest := raw[schemeEnd+len("://"):]
	hostCandidate := rest
	if at := strings.LastIndex(rest, "@"); at >= 0 {
		hostCandidate = rest[at+1:]
	}
	authorityEnd := strings.IndexFunc(hostCandidate, func(r rune) bool {
		return r == '/' || r == '?' || r == '#' || r <= ' ' || r == 0x7f
	})
	host = hostCandidate
	if authorityEnd >= 0 {
		host = hostCandidate[:authorityEnd]
	}
	if host == "" {
		return "", "", false
	}
	return scheme, host, true
}

// isValidURLScheme reports whether s has the shape of an RFC 3986 scheme
// (letter, then letters/digits/+/-/.). A raw string that fails url.Parse can
// still contain "://" purely by coincidence (e.g. inside an already-broken
// path) or carry a scheme-shaped-but-invalid prefix (a leading digit); this
// keeps rawWebhookSchemeHost from treating that as a real scheme and echoing
// it into the tier-1 marker.
func isValidURLScheme(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		case i > 0 && (r >= '0' && r <= '9' || r == '+' || r == '-' || r == '.'):
		default:
			return false
		}
	}
	return true
}

// realAuthorityCandidate reruns rawWebhookSchemeHost's raw, last-'@'-wins
// authority scan against raw's own text and reports whether it disagrees
// with parsedHost, the Host field a PRIOR, SUCCESSFUL url.Parse(raw) already
// produced.
//
// F5, P2 (found 2026-09-04, sixth round on this defect class): net/url's
// parseAuthority bounds the authority to the first of '/', '?', '#' etc.
// BEFORE ever looking for '@' -- the exact simplification rawWebhookSchemeHost
// itself was fixed for on the url.Parse-FAILURE side (F1, above). That same
// simplification also affects url.Parse's SUCCESS path: a userinfo shaped
// like "TOKEN/pathlike@realhost" (no ':' in it, so nothing in the token
// itself is invalid) makes net/url stop the authority at the first '/',
// find no '@' inside "TOKEN", and treat "TOKEN" as a perfectly ordinary bare
// hostname -- parsed.User stays nil and parsed.Host becomes the credential.
// url.Parse never errors on this shape, so it produces no *url.Error for any
// tier of RedactWebhookSecretsForURL's fallback to run against, and
// RedactURLCredentials (which has no fallback of any kind) trusts
// parsed.Host/parsed.User exactly as net/url reports them. Reproduced end to
// end: general_feeds[0] = "https://FEEDTOKEN777/x@feeds.example.test/rss"
// loads successfully (feedurl.Validate never rejects it -- there is no
// userinfo for it to reject) and bot.log's "feed_url" field, redacted only
// through RedactURLCredentials on every RSS/status log line naming that
// feed, carried the token verbatim.
//
// raw's QUERY component specifically -- between the first '?' and the next
// '#' or end of string -- is cut out (not merely truncated-at) before the
// scan runs WHENEVER parsed.Path is non-empty, so a query value on an
// ordinary "/path?query" URL is never searched: an unrelated '@' in a query
// VALUE (an email address is the common case) must not be mistaken for a
// smuggled userinfo delimiter and downgrade an already-correct parsed.Host
// -- rawWebhookSchemeHost's own doc comment documents accepting exactly
// that trade-off for its original fallback caller, where no such boundary
// is knowable because url.Parse already failed; here, because url.Parse
// SUCCEEDED, the real query start is known precisely and this ambiguity is
// avoidable outright rather than merely accepted. The exclusion is skipped
// -- the query stays in the scan -- when parsed.Path is EMPTY: see the
// implementation's own comment for why a bare "TOKEN?..." with no path at
// all is a different, smuggling-shaped case this same query exclusion would
// otherwise blind the scan to.
//
// The FRAGMENT is deliberately left in the scan, unlike the query -- this is
// not symmetric, and the asymmetry is load-bearing. net/url's exported Parse
// cuts rawURL at the FIRST '#' before doing ANYTHING else (see tier 2c's own
// doc comment on RedactWebhookSecretsForURL, above, for the identical cut on
// the failure side), so a credential shaped like
// "https://TOKEN#realpath@realhost/rss" makes net/url treat "TOKEN" as the
// entire pre-fragment URL, parse it as a bare, valid hostname with no
// userinfo at all, and hand back everything past the "#" as Fragment --
// success, Host == the token, and the real host is sitting unexamined
// inside a field nothing else in this file reads. Found by the exhaustive
// per-byte sweep this round's own verification ran (see TESTING.md):
// excluding the fragment the same way the query is excluded left this shape
// unmatched (rawHost then agreed with the wrong parsed.Host, since neither
// saw the real "@" past the cut). A benign fragment containing an unrelated
// "@" (a copy-pasted anchor of the form "#section-a@b") is a real but far
// rarer shape than a query value containing one, and costs only marker
// accuracy, never a leak, when it fires -- the same accepted trade-off
// rawWebhookSchemeHost's own doc comment already documents for its "@"
// elsewhere in a string it cannot subdivide further.
//
// mismatched is false, and scheme/host are not meaningful, whenever the raw
// scan cannot find a "scheme://host" shape at all, or finds one that AGREES
// with parsedHost (case-insensitively) -- which covers every ordinary URL,
// including a normal, non-smuggled userinfo ("user:pass@host"): the raw scan
// finds the SAME real host past the "@" that url.Parse itself found, so
// there is nothing to override. Only a genuine disagreement -- net/url's
// answer and the raw scan's answer naming two different hosts -- is treated
// as evidence that net/url's authority-truncation bug fired, so this never
// second-guesses a parsed.Host that was already correct.
func realAuthorityCandidate(raw string, parsed *url.URL) (scheme, host string, mismatched bool) {
	authorityAndPath := raw
	// A query is excluded from the scan UNLESS parsed.Path is empty: net/url
	// bounds the authority (and so, absent a real path, Host itself) at the
	// first of '/' OR '?', so "TOKEN?realpath@realhost" -- no '/' anywhere
	// before the '?' -- makes net/url treat "TOKEN" as the WHOLE bare
	// hostname and hand back everything past the '?' as RawQuery, hiding the
	// real host there instead of in a path segment. An ordinary URL that
	// legitimately has a query string almost always has a '/' path before
	// it ("/path?redirect=a@b"), which is exactly the shape parsed.Path !=
	// "" identifies and continues to exclude, preserving the query's usual
	// protection against an unrelated '@' in a query VALUE (e.g. an email
	// address). Found by this round's own exhaustive per-byte sweep (see
	// TESTING.md): a smuggled-via-query shape with no path segment leaked
	// through both RedactURLCredentials and RedactWebhookSecretsForURL's
	// marker until this condition was added.
	if q := strings.Index(raw, "?"); q >= 0 && parsed.Path != "" {
		rest := raw[q:]
		if h := strings.Index(rest, "#"); h >= 0 {
			authorityAndPath = raw[:q] + rest[h:]
		} else {
			authorityAndPath = raw[:q]
		}
	}
	rawScheme, rawHost, ok := rawWebhookSchemeHost(authorityAndPath)
	if !ok || strings.EqualFold(rawHost, parsed.Host) {
		return "", "", false
	}
	return rawScheme, rawHost, true
}

// RedactURLCredentials removes userinfo and sensitive query values from URL
// substrings while preserving enough host/path context for diagnostics.
func RedactURLCredentials(text string) string {
	if text == "" {
		return ""
	}
	return redactURLMatches(text, genericHTTPURLRegex, func(match string) string {
		parsed, err := url.Parse(match)
		if err != nil || parsed.Host == "" {
			return redactUnparsableURLMatch(match)
		}
		// F5, P2 (found 2026-09-04, sixth round on this defect class): this
		// function's own instance of the identical blind spot
		// RedactWebhookSecretsForURL's success branch had -- see
		// realAuthorityCandidate's doc comment. Unlike that function,
		// RedactURLCredentials has no known-good anchor URL and no fallback
		// of any kind on top of what url.Parse reports, so when net/url's
		// authority-truncation bug hands back the credential as parsed.Host
		// with parsed.User == nil, there is nothing else in this function
		// that would ever redact it: parsed.User is nil (nothing to mask)
		// and the credential is not a recognised sensitive query key either.
		// This is the call site every RSS/status/API log line that names a
		// feed or webhook URL goes through with no other anchor available
		// (internal/rss, internal/scheduler, internal/status,
		// internal/discord, internal/slack), so it reaches bot.log on
		// ordinary, successful operation -- not only on a validation
		// failure.
		if rawScheme, rawHost, mismatched := realAuthorityCandidate(match, parsed); mismatched {
			if isLogSafeHost(rawHost) {
				return rawScheme + "://" + rawHost + redactedPathMarker
			}
			return redactedURLMarker
		}
		changed := false
		if parsed.User != nil {
			parsed.User = url.User(redactedValueMarker)
			changed = true
		}
		query, queryErr := url.ParseQuery(parsed.RawQuery)
		if queryErr != nil {
			// ParseQuery dropped segments it could not read (";" separators, bad
			// escapes); re-encoding its partial result would delete them, so
			// rewrite the raw query instead.
			if redacted, ok := redactRawQuery(parsed.RawQuery); ok {
				parsed.RawQuery = redacted
				changed = true
			}
			if !changed {
				return match
			}
			return parsed.String()
		}
		for key, values := range query {
			if !feedurl.IsSensitiveQueryKey(key) {
				continue
			}
			redactedValues := make([]string, len(values))
			for i := range redactedValues {
				redactedValues[i] = redactedValueMarker
			}
			query[key] = redactedValues
			changed = true
		}
		if !changed {
			return match
		}
		parsed.RawQuery = query.Encode()
		return parsed.String()
	})
}

// redactURLMatches applies redact to every non-overlapping match of re in
// text, exactly like re.ReplaceAllStringFunc(text, redact), with one
// deliberate difference: ReplaceAllStringFunc only ever hands redact the
// bytes re actually matched. When a match ends not because text ran out or
// the next byte is whitespace, but because the very next byte is one of
// genericHTTPURLRegex's own excluded delimiters (`"`, `'`, `<`, `)`),
// everything past that byte is ordinary text no call in this file ever
// inspects -- including, potentially, the rest of a credential the delimiter
// split in two.
//
// A literal delimiter byte inside a userinfo credential is exactly the shape
// (*url.Error).Error() produces via %q whenever url.Parse itself rejects a
// URL because of it (net/url's "invalid userinfo"/"invalid port" family of
// errors) -- the delimiter, and everything the credential still had left to
// give, land OUTSIDE the match:
//
//	in:  Get "https://user:SECRET"TAIL@real-host.example/path": dial tcp: no such host
//	old: Get "[redacted URL]"TAIL@real-host.example/path": dial tcp: no such host   (leaks TAIL, real-host.example)
//	new: Get "[redacted URL] dial tcp: no such host
//
// The signal used to decide a delimiter genuinely split a credential, rather
// than merely closing a complete, self-contained URL (a trailing quote in
// `Get "https://host/path": ...`, a trailing paren in prose, `(see
// https://host/path)`), is narrow and specific: does the contiguous run of
// non-whitespace bytes starting AT the delimiter contain an `@`? That `@` is
// what a smuggled userinfo section needs to reach its real host -- a
// complete match's trailing punctuation never has one immediately attached,
// so this fires only on the shape it exists for. Verified this is not
// over-broad: the F5 "smuggled userinfo" success-branch tests
// (authority_truncation_success_test.go, whose matches end at a delimiter
// with nothing but JSON/error punctuation after them, no `@`) and the
// query-value `@`-inside-match test
// (TestRedactURLCredentialsDoesNotLeakSecretWhenAtSignInQueryValueLooksLikeUserinfo,
// whose `@` sits INSIDE the match, not the tail) both continue to pass
// unmodified with this fix applied -- confirmed by running the existing
// suite, not assumed. A broader "swallow the tail whenever redact masked
// anything in the match" version of this fix was tried first and reverted
// the same day for regressing exactly those tests: it could not tell "redact
// found and masked something real" apart from "the match is simply complete,
// and the delimiter is unrelated trailing punctuation".
//
// When the tail run has no `@`, this is a complete no-op: redact's own
// result is used exactly as ReplaceAllStringFunc would have used it, byte
// for byte. When it does, redact's result is discarded and the ENTIRE
// combined span (the original match, the delimiter, and the tail run) is
// replaced with the same unconditional "[redacted URL]" marker every other
// fallback in this file already uses -- never deriving a host or any other
// structure from a match already known to be incomplete, the same lesson
// redactUnparsableURLMatch already settled for the match itself, extended to
// text the match's own boundary could never see in the first place.
//
// Deliberately NOT applied to discordWebhookURLRegex/slackWebhookURLRegex
// (RedactWebhookSecrets, below) or RedactWebhookSecretsForURL's tier 1 pass:
// no live producer of a delimiter-truncated match through either was found
// (RedactWebhookSecrets ends by calling RedactURLCredentials regardless, so
// it already inherits this fix for the reported shape once genericHTTPURLRegex
// is the pass doing the work), and tier 1's own known, separately tracked gap
// is a PATH-segment token shape with no `@` in its tail at all -- this fix's
// `@` signal would not close it, and applying it there anyway would read as a
// fix for something it does not actually close.
func redactURLMatches(text string, re *regexp.Regexp, redact func(match string) string) string {
	locs := re.FindAllStringIndex(text, -1)
	if locs == nil {
		return text
	}
	var b strings.Builder
	b.Grow(len(text))
	last := 0
	for _, loc := range locs {
		start, end := loc[0], loc[1]
		if start < last {
			// Already folded into a previous match's swallowed tail.
			continue
		}
		b.WriteString(text[last:start])
		if end < len(text) && isURLTruncationDelimiter(text[end]) {
			tailEnd := end
			for tailEnd < len(text) && !isASCIISpaceByte(text[tailEnd]) {
				tailEnd++
			}
			if strings.IndexByte(text[end:tailEnd], '@') >= 0 {
				b.WriteString(redactedURLMarker)
				last = tailEnd
				continue
			}
		}
		b.WriteString(redact(text[start:end]))
		last = end
	}
	b.WriteString(text[last:])
	return b.String()
}

// isURLTruncationDelimiter reports whether b is one of the four bytes
// genericHTTPURLRegex excludes from a match without being whitespace.
//
// This list is a second, redundant statement of genericHTTPURLRegex's own
// excluded-byte class: because a match can only end at `\s` or at one of
// these four, and the tail scan below stops immediately on `\s`, checking
// this is equivalent to checking that the tail run is non-empty. It is kept
// for readability -- but that makes it a place that MUST be edited in step
// with genericHTTPURLRegex if that character class ever changes.
func isURLTruncationDelimiter(b byte) bool {
	switch b {
	case '"', '\'', '<', ')':
		return true
	}
	return false
}

// isASCIISpaceByte reports whether b is one of the bytes Go's regexp `\s`
// class matches, which is exactly [`\t` `\n` `\f` `\r` ` `] -- the same class
// genericHTTPURLRegex's own exclusion already treats as the one delimiter a
// raw URL genuinely cannot contain, so this is precisely where a match's own
// byte run ends.
//
// `\v` (0x0b) is deliberately ABSENT and must stay absent: Go's regexp `\s`
// does NOT include it, so genericHTTPURLRegex matches straight THROUGH a
// 0x0b byte. Listing it here would end the tail scan early at a byte the
// regex itself ran past, and a single 0x0b placed before the `@` would carry
// the rest of the credential out of the gate's reach -- reintroducing the
// exact leak this function exists to close. Pinned by
// TestRedactURLCredentialsTailScanMatchesRegexpWhitespaceClass, which sweeps
// every byte 0x00-0x7f and asserts the swallow fires if and only if the byte
// is not matched by `\s`; a coverage number cannot catch this, because the
// whole case list is a single statement and reads 100% either way.
func isASCIISpaceByte(b byte) bool {
	switch b {
	case ' ', '\t', '\n', '\f', '\r':
		return true
	}
	return false
}

// redactUnparsableURLMatch is RedactURLCredentials's fallback for a
// genericHTTPURLRegex match that url.Parse could not make sense of (or that
// "succeeded" with an empty Host). It always returns the same generic,
// context-free marker RedactWebhookSecretsForURL's own fallback already uses
// -- deliberately WITHOUT attempting to salvage a scheme or host from match
// the way that other fallback does for its four hardcoded webhook hosts.
//
// That host-echoing approach was tried here first and reverted the same day:
// match is whatever genericHTTPURLRegex already extracted, and on this branch
// url.Parse has just failed to make sense of it, so there is no guarantee
// left that match still has the shape "scheme://host/...". Two ways that
// assumption breaks, both verified directly against this package's own
// rawWebhookSchemeHost/isLogSafeHost pair, the only string-only primitive
// available for a salvage attempt:
//
//  1. genericHTTPURLRegex stops at the first double quote, single quote,
//     '<' or ')', so a credential followed by one of those bytes truncates
//     match before this function ever sees it. Whatever text is left between "://" and the cut
//     becomes rawWebhookSchemeHost's "host", and isLogSafeHost -- built to
//     reject only a control byte, 0x7f, '"' or '\\' -- waves an ordinary
//     credential straight through: "https://svc:SECRET"z@host/rss" (regex
//     match ends at the '"') came back as
//     "https://svc:SECRET/[redacted]"z@host/rss" -- the secret echoed AS the
//     host, immediately followed by a marker that discards the rest of the
//     URL.
//  2. Even without truncation, rawWebhookSchemeHost's own last-'@'-wins scan
//     (correct for its original caller, which always has a known-good webhook
//     URL to compare against) has no such anchor here: an unrelated '@'
//     inside a query VALUE (an ordinary shape on a freeform error path, e.g.
//     "...?user=admin@corp.example&apikey=SECRET": invalid port) makes the
//     query tail look like the "host", and the marker then discards
//     everything after it -- the log line ends up containing the secret and
//     NOTHING else, which looks redacted while it is not.
//
// Both are the same mechanism: rawWebhookSchemeHost/isLogSafeHost were built
// and proven correct for RedactWebhookSecretsForURL's fallback, where host is
// always one of four hardcoded, known-good webhook hosts and echoing it is
// genuinely useful context. Reusing that pair here means guessing at
// structure inside a string that, by definition -- url.Parse already failed
// on it -- has none. A log line that looks redacted while still carrying the
// credential is worse than one that is obviously raw, so this function never
// makes that guess: the surrounding error text still names what broke
// (invalid port, malformed host, ...), and the operator configured the URL,
// so they can read it back from their own config. Before this fallback
// existed at all, this branch returned match completely unchanged -- so the
// unconditional generic marker below is strictly an improvement over that,
// not a regression from it, even though it is less informative than the
// (unsafe) host-echoing alternative would have been.
func redactUnparsableURLMatch(string) string {
	return redactedURLMarker
}

// redactRawQuery redacts sensitive parameters in a query string that
// url.ParseQuery refuses (most commonly a legacy ";" separator). It rewrites the
// raw string in place instead of re-encoding it, so every byte url.Values.Encode
// would have normalised or dropped is preserved. Reports whether anything was
// redacted.
func redactRawQuery(raw string) (string, bool) {
	changed := false
	ampParts := strings.Split(raw, "&")
	for i, ampPart := range ampParts {
		semiParts := strings.Split(ampPart, ";")
		kept := semiParts[:0]
		redactingValue := false
		for _, semiPart := range semiParts {
			rawKey, _, hasEquals := strings.Cut(semiPart, "=")
			if !hasEquals {
				// A ";"-separated run with no "=" continues the previous value;
				// when that value is being redacted the continuation is part of
				// the secret and is dropped.
				if redactingValue {
					changed = true
					continue
				}
				kept = append(kept, semiPart)
				continue
			}
			decodedKey, err := url.QueryUnescape(rawKey)
			if err != nil {
				decodedKey = rawKey
			}
			if feedurl.IsSensitiveQueryKey(decodedKey) {
				kept = append(kept, rawKey+"="+redactedQueryValue)
				redactingValue = true
				changed = true
				continue
			}
			redactingValue = false
			kept = append(kept, semiPart)
		}
		ampParts[i] = strings.Join(kept, ";")
	}
	if !changed {
		return raw, false
	}
	return strings.Join(ampParts, "&"), true
}

// RedactWebhookError returns an error with webhook secrets removed from its text.
func RedactWebhookError(err error) error {
	if err == nil {
		return nil
	}
	return errors.New(RedactWebhookSecrets(err.Error()))
}

// RedactWebhookErrorForURL returns an error with webhook secrets removed using
// the exact configured webhook URL as additional context.
func RedactWebhookErrorForURL(err error, webhookURL string) error {
	if err == nil {
		return nil
	}
	return errors.New(RedactWebhookSecretsForURL(err.Error(), webhookURL))
}
