package textutil

import (
	"net/url"
	"strings"
	"testing"
)

// realParseFailure builds the text RedactWebhookSecretsForURL/RedactWebhookErrorForURL
// actually receive in production: the *url.Error text from a genuine failed
// url.Parse(raw), never a hand-written literal (the plan's own rule for these
// tests, corrected during plan review after a hand-written-text case was found
// to pass by coincidence against a scenario production never produces). Fails
// the test if raw parses cleanly, so a case that stops reaching this fallback
// (e.g. Go's url.Parse becoming more lenient) is caught instead of silently
// exercising nothing.
func realParseFailure(t *testing.T, raw string) string {
	t.Helper()
	_, err := url.Parse(raw)
	if err == nil {
		t.Fatalf("url.Parse(%q) succeeded, want a failure to build the fallback text from", raw)
	}
	return err.Error()
}

// Plan §6a.1 -- pins the core defect: a Slack-compatible custom host whose
// webhook URL fails url.Parse (embedded control character) must not echo the
// raw token, and the host-anchored marker must still appear.
func TestRedactWebhookSecretsForURLRedactsCustomHostOnParseFailure(t *testing.T) {
	const token = "very-secret-compatible-token-fb01"
	raw := "https://chat.example.test/hooks/" + token + "\t/tail"
	text := realParseFailure(t, raw)

	got := RedactWebhookSecretsForURL(text, raw)

	if strings.Contains(got, token) {
		t.Fatalf("token leaks: %s", got)
	}
	if !strings.Contains(got, "https://chat.example.test/[redacted]") {
		t.Fatalf("missing host-anchored marker: %s", got)
	}
}

// Plan §6a.2 -- same shape on hooks.slack-gov.com, and pins that the widened
// regex/marker (§3a) names the host correctly instead of mislabelling it
// hooks.slack.com.
func TestRedactWebhookSecretsForURLRedactsSlackGovHostOnParseFailure(t *testing.T) {
	const token = "very-secret-slackgov-token-fb02"
	raw := "https://hooks.slack-gov.com/services/T00/B00/" + token + "\t/x"
	text := realParseFailure(t, raw)

	got := RedactWebhookSecretsForURL(text, raw)

	if strings.Contains(got, token) {
		t.Fatalf("token leaks: %s", got)
	}
	if !strings.Contains(got, "https://hooks.slack-gov.com/[redacted]") {
		t.Fatalf("missing correctly-hosted marker: %s", got)
	}
	if strings.Contains(got, "https://hooks.slack.com/") {
		t.Fatalf("mislabelled as hooks.slack.com: %s", got)
	}
}

// Plan §6a.3 -- guards against a fix that masks only the path and leaves
// userinfo credentials exposed. Both assertions are load-bearing: dropping
// only the "@"-split (mutation §6c.2) leaks the token itself, dropping the
// EqualFold match condition for a HasPrefix one (mutation §6c.3) leaves the
// userinfo shape visible without leaking the token.
func TestRedactWebhookSecretsForURLDropsUserinfoOnParseFailure(t *testing.T) {
	const token = "very-secret-userinfo-token-fb03"
	raw := "https://user:" + token + "@chat.example.test/hooks/x\t/tail"
	text := realParseFailure(t, raw)

	got := RedactWebhookSecretsForURL(text, raw)

	if strings.Contains(got, token) {
		t.Fatalf("token leaks: %s", got)
	}
	if strings.Contains(got, "user:") || strings.Contains(got, "@chat.example.test") {
		t.Fatalf("userinfo survives: %s", got)
	}
}

// Plan §6a.4 -- a token in the query string, not the path, on a real parse
// failure: the whole query must be dropped, not just a key RedactURLCredentials
// happens to recognise.
func TestRedactWebhookSecretsForURLRedactsQueryOnlySecretOnParseFailure(t *testing.T) {
	const token = "very-secret-query-token-fb04"
	raw := "https://chat.example.test/hooks?token=" + token + "\t"
	text := realParseFailure(t, raw)

	got := RedactWebhookSecretsForURL(text, raw)

	if strings.Contains(got, token) {
		t.Fatalf("token leaks: %s", got)
	}
}

// An earlier hand-written-text
// version of this test passed by coincidence against a scenario production
// never produces. A schemeless webhookURL WITH an embedded control character
// really does fail url.Parse (verified: it is reachable from
// validateSlackCompatibleWebhookURL today), so this must be built from a real
// failure like every other case here -- and the fix must still withhold the
// whole value rather than guess at a host boundary.
func TestRedactWebhookSecretsForURLWithholdsWholeValueWhenNoSchemeFound(t *testing.T) {
	const token = "very-secret-noscheme-token-fb05"
	raw := "chat.example.test/hooks/" + token + "\t/tail"
	text := realParseFailure(t, raw)

	got := RedactWebhookSecretsForURL(text, raw)

	if strings.Contains(got, token) {
		t.Fatalf("token leaks: %s", got)
	}
	if !strings.Contains(got, "[redacted URL]") {
		t.Fatalf("placeholder missing: %s", got)
	}
}

// Plan §6a.6 -- pins §3a directly: a well-formed hooks.slack-gov.com webhook
// URL embedded in plain text (no parse failure involved, the no-anchor path
// RedactWebhookSecrets alone protects) is redacted and correctly hosted.
func TestSlackWebhookURLRegexMatchesSlackGovHost(t *testing.T) {
	const token = "very-secret-noanchor-token-fb06"
	text := "post to https://hooks.slack-gov.com/services/T1/B1/" + token + " failed"

	got := RedactWebhookSecrets(text)

	if strings.Contains(got, token) {
		t.Fatalf("token leaks: %s", got)
	}
	want := "post to https://hooks.slack-gov.com/services/[redacted] failed"
	if got != want {
		t.Fatalf("RedactWebhookSecrets() = %q, want %q", got, want)
	}
}

// There are eight reachable input classes on which an earlier design
// (compare an unescaped raw host against %q-escaped
// text, search only http(s):// shapes) was a silent no-op. Each of these
// classes is reachable from config_general.json because url.Parse runs before
// any scheme/host/allowlist check. Six are exercised here (the other two --
// "no scheme, control char" and query-only -- are §6a.5/§6a.4 above).
func TestRedactWebhookSecretsForURLFallbackClosesReviewFixLeakClasses(t *testing.T) {
	cases := []struct {
		name    string
		rawTmpl string // "{TOK}" is replaced with a per-case token
	}{
		{"scheme not http(s) (ftp)", "ftp://chat.example.test/hooks/{TOK}\t/tail"},
		{"mistyped scheme (htps)", "htps://hooks.slack-gov.com/services/T/B/{TOK}\t/x"},
		{"uppercase scheme (HTTPS)", "HTTPS://chat.example.test/hooks/{TOK}\t/x"},
		{"control byte inside authority", "https://chat.exa\tmple.test/hooks/{TOK}/tail"},
		{"backslash in path", "https://chat.example.test\\hooks/{TOK}\t/x"},
		{"empty host", "https:///hooks/{TOK}\t/x"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			token := "leaktoken-" + strings.ReplaceAll(tt.name, " ", "-")
			raw := strings.ReplaceAll(tt.rawTmpl, "{TOK}", token)
			text := realParseFailure(t, raw)

			got := RedactWebhookSecretsForURL(text, raw)

			if strings.Contains(got, token) {
				t.Fatalf("token leaks (one of the silent no-op classes above): %s", got)
			}
		})
	}
}

// strings.EqualFold(matchScheme, scheme) &&
// strings.EqualFold(matchHost, host) mutated to exact "==" changed nothing in
// the existing test set, because every case there builds text from the same
// raw string it compares against (case can never differ). This test embeds a
// SECOND, differently-cased occurrence of the same host with a different
// token that is NOT byte-identical to the webhook URL or its %q-escaped form,
// so tier 2 cannot catch it -- only tier 1's case-insensitive host match can.
func TestRedactWebhookSecretsForURLFallbackHostMatchIsCaseInsensitive(t *testing.T) {
	const anchorToken = "anchor-token-fb07" //nolint:gosec // G101: test fixture, not a real credential
	raw := "https://chat.example.test/hooks/" + anchorToken + "\t/tail"
	baseText := realParseFailure(t, raw)

	const secondToken = "second-url-token-fb07b"
	text := baseText + " (also tried https://CHAT.EXAMPLE.TEST/hooks/" + secondToken + ")"

	got := RedactWebhookSecretsForURL(text, raw)

	if strings.Contains(got, anchorToken) {
		t.Fatalf("anchor token leaks: %s", got)
	}
	if strings.Contains(got, secondToken) {
		t.Fatalf("case-differing host occurrence not masked (EqualFold regression): %s", got)
	}
}

// isValidURLScheme mutated to always return
// true also changed nothing in the existing test set. A raw value whose
// pre-"://" bytes are not scheme-shaped (starts with a digit) must be
// rejected as "no scheme found" -- not accepted and echoed into the tier-1
// marker as if it were a real scheme.
func TestRawWebhookSchemeHostRejectsNonSchemeShapedPrefix(t *testing.T) {
	const token = "invalid-scheme-token-fb08"
	raw := "1abc://chat.example.test/hooks/" + token + "\t/x"
	text := realParseFailure(t, raw)

	got := RedactWebhookSecretsForURL(text, raw)

	if strings.Contains(got, token) {
		t.Fatalf("token leaks: %s", got)
	}
	if strings.Contains(got, "1abc://chat.example.test/[redacted]") {
		t.Fatalf("non-scheme-shaped prefix %q accepted as a scheme and echoed into the marker (isValidURLScheme regression): %s", "1abc", got)
	}
	if !strings.Contains(got, "[redacted URL]") {
		t.Fatalf("expected the literal placeholder when no valid scheme prefix exists: %s", got)
	}
}

// F1 -- pins the tier-ordering fix directly: genericHTTPURLRegex's character
// class ([^\s"'<)]+) does not escape ", ', < or ) with %q, so a webhookURL
// containing one of those characters before the secret used to make a
// tier-one-first pass replace only the prefix up to that character, mutate
// the text in place, and leave the (still secret-bearing) tail for tier two
// to search for in vain. Every case here builds its text from a real
// url.Parse failure (a trailing tab supplies the control character that
// makes parsing fail; the delimiter itself does not) so each one reproduces
// exactly the shape production hits, never a hand-written literal.
func TestRedactWebhookSecretsForURLSurvivesDelimiterBeforeSecret(t *testing.T) {
	delims := []struct {
		name string
		ch   string
	}{
		{"double quote", `"`},
		{"single quote", `'`},
		{"less-than", "<"},
		{"close paren", ")"},
	}
	for _, d := range delims {
		t.Run(d.name, func(t *testing.T) {
			const token = "very-secret-delimiter-token-fb10"
			raw := "https://chat.example.test/hooks/a" + d.ch + "b/" + token + "\t/tail"
			text := realParseFailure(t, raw)

			got := RedactWebhookSecretsForURL(text, raw)

			if strings.Contains(got, token) {
				t.Fatalf("token leaks after %s truncation (tier-ordering regression): %s", d.name, got)
			}
		})
	}
}

// F1 -- the markdown-paste shape: an operator copying a webhook URL out of a
// Markdown link (`[text](https://.../token)`) commonly picks up a stray "("
// and ")" around an unrelated query segment. ")" is one of the unescaped
// genericHTTPURLRegex terminators above, so this is the same defect in the
// shape it is actually reached in.
func TestRedactWebhookSecretsForURLSurvivesMarkdownParenBeforeSecret(t *testing.T) {
	const token = "very-secret-markdown-paren-token-fb11"
	raw := "https://chat.example.test/hooks/x?a=(b)&token=" + token + "\t/y"
	text := realParseFailure(t, raw)

	got := RedactWebhookSecretsForURL(text, raw)

	if strings.Contains(got, token) {
		t.Fatalf("token leaks after markdown-paren truncation (tier-ordering regression): %s", got)
	}
}

// F1 coverage -- closes tier one's "no match" branch: a genuine second,
// unrelated URL sitting alongside the malformed webhook URL in the same
// message must survive tier one's ReplaceAllStringFunc untouched, because
// its scheme+host does not match the webhook's. Without this case tier one's
// closure is only ever exercised on its "matches, replace" path.
func TestRedactWebhookSecretsForURLFallbackLeavesUnrelatedURLUntouched(t *testing.T) {
	const token = "very-secret-unrelated-url-token-fb13" //nolint:gosec // G101: test fixture, not a real credential
	raw := "https://chat.example.test/hooks/" + token + "\t/tail"
	text := realParseFailure(t, raw) + " next try https://docs.example.org/help"

	got := RedactWebhookSecretsForURL(text, raw)

	if strings.Contains(got, token) {
		t.Fatalf("token leaks: %s", got)
	}
	if !strings.Contains(got, "https://docs.example.org/help") {
		t.Fatalf("unrelated URL was mangled: %s", got)
	}
}

// F1 -- pins a second instance of the same defect class, found while
// building the end-to-end proof: RedactWebhookSecretsForURL used to call the
// general-purpose RedactWebhookSecrets(text) as its very first line, and
// RedactWebhookSecrets's own discordWebhookURLRegex/slackWebhookURLRegex use
// the identical unescaped-by-%q character class ([^\s"'<)]+) as
// genericHTTPURLRegex. When webhookURL's host is one of the three built-in
// hosts those regexes know (discord.com, hooks.slack.com,
// hooks.slack-gov.com) AND contains a delimiter before the secret, that
// upfront call truncated and mutated the text before either tier of the
// anchored fallback ever got to see it untouched -- reproduced end to end
// through the real config.LoadConfig -> scheduler.go:874 -> bot.log chain for
// hooks.slack-gov.com specifically. Fixed by running RedactWebhookSecrets
// only on the RESULT of the anchored tiers, never before them.
func TestRedactWebhookSecretsForURLSurvivesDelimiterOnBuiltInHost(t *testing.T) {
	hosts := []struct {
		name string
		tmpl string
	}{
		{"discord.com", "https://discord.com/api/webhooks/123/a)b/{TOK}\t/tail"},
		{"hooks.slack.com", "https://hooks.slack.com/services/T00/B00/a)b/{TOK}\t/tail"},
		{"hooks.slack-gov.com", "https://hooks.slack-gov.com/services/T00/B00/a)b/{TOK}\t/tail"},
	}
	for _, h := range hosts {
		t.Run(h.name, func(t *testing.T) {
			token := "very-secret-builtin-host-delim-token-fb14"
			raw := strings.ReplaceAll(h.tmpl, "{TOK}", token)
			text := realParseFailure(t, raw)

			got := RedactWebhookSecretsForURL(text, raw)

			if strings.Contains(got, token) {
				t.Fatalf("token leaks after delimiter truncation on built-in host %s: %s", h.name, got)
			}
		})
	}
}

// F2 -- pins the isLogSafeHost guard directly. Dropping it (changing
// `if ok && isLogSafeHost(host)` to `if ok`) still passes every token-leak
// assertion in this file, because tier two already replaces the token via
// the raw/quoted forms regardless of the guard; what the guard actually
// prevents is a raw backslash from the URL's authority reaching the tier-one
// marker verbatim. rawWebhookSchemeHost's authority scan does not terminate
// on '\\' (only on '/', '?', '#', r <= ' ', or 0x7f), so a backslash embedded
// in the host survives extraction, and without this guard would be echoed
// straight into bot.log as part of the marker.
func TestRedactWebhookSecretsForURLGuardsBackslashAuthorityFromMarker(t *testing.T) {
	const token = "very-secret-backslash-authority-token-fb12"
	const host = `chat.example\authority.test`
	raw := "https://" + host + "/hooks/" + token + "/tail"
	text := realParseFailure(t, raw)

	got := RedactWebhookSecretsForURL(text, raw)

	if strings.Contains(got, token) {
		t.Fatalf("token leaks: %s", got)
	}
	// The stdlib error text itself contains an unrelated, quoted backslash
	// (`invalid character "\\" in host name`); what this guards against is
	// the backslash-bearing HOST reaching the output verbatim as part of a
	// tier-one marker, so check for that specific substring rather than for
	// any backslash in the output.
	if strings.Contains(got, host) {
		t.Fatalf("raw backslash-bearing authority reached the output as a marker host (isLogSafeHost regression): %s", got)
	}
	if !strings.Contains(got, "[redacted URL]") {
		t.Fatalf("expected the literal placeholder when host is not log-safe: %s", got)
	}
}

// TestIsLogSafeHostAndIsValidURLSchemeRejectEmptyInput pins the empty-input
// guard on both unexported helpers directly. rawWebhookSchemeHost's own
// "" checks (host == "" after extraction, schemeEnd <= 0) mean neither
// helper's empty branch is reachable through RedactWebhookSecretsForURL
// today, but it is a real guarantee of each helper's contract, not a
// coverage trick, and a genuine future caller depending on it deserves a
// pinned answer rather than an undefined one.
func TestIsLogSafeHostAndIsValidURLSchemeRejectEmptyInput(t *testing.T) {
	if isLogSafeHost("") {
		t.Fatal("isLogSafeHost(\"\") = true, want false")
	}
	if isValidURLScheme("") {
		t.Fatal("isValidURLScheme(\"\") = true, want false")
	}
}
