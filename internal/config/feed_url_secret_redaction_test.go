package config

import (
	"strings"
	"testing"
)

// These tests pin a fix in internal/config/config.go:
// validateFeedURL used to return feedurl.Validate's
// error unredacted whenever the underlying url.Parse failed, and
// feedurl.Parse wraps that raw *url.Error with %w, so any secret embedded in
// a feed URL's query, path or userinfo survived into the error text
// verbatim. That error reached bot.log through the non-strict branch of
// loadFeedsConfigWithMode even though the config went on to load
// successfully (feeds disabled) -- there was no failure for an operator to
// notice, just the secret sitting in the log. Every input below is built
// from a URL that genuinely fails url.Parse (never a hand-written literal
// standing in for one), the same shapes TestValidateFeedURL already proves
// feedurl.Validate rejects at the Parse stage (e.g. "https://host:bad/path").

// UPDATED 2026-09-04 (RedactURLCredentials parse-failure fallback,
// operator decision): this test used to additionally require "example.com"
// -- the host -- to survive into the message. That assertion pinned exactly
// the behaviour this round deliberately removes: RedactURLCredentials's
// url.Parse-failure fallback (redactUnparsableURLMatch) no longer echoes any
// scheme or host on that branch, unconditionally, even when a different,
// more careful pass upstream (RedactWebhookSecretsForURL's own fallback,
// which built the "https://example.com:bad/[redacted]" marker this message
// carried before RedactURLCredentials's own regex re-scanned and rejected
// it as unparsable due to the non-numeric "bad" port) had already produced a
// safe, host-preserving marker. The operator's stated trade-off is that this
// diagnostic loss is acceptable; the query-secret-absence property below is
// unweakened and still the load-bearing assertion.
func TestValidateFeedURLRedactsQuerySecretOnParseFailure(t *testing.T) {
	rawURL := "https://example.com:bad/feed?apikey=QUERYSECRET777" //nolint:gosec // G101: test fixture, not a real credential

	err := validateFeedURL(rawURL, "ransomware_feeds[0]")
	if err == nil {
		t.Fatal("expected error for malformed feed URL")
	}
	msg := err.Error()
	t.Logf("error: %s", msg)
	if strings.Contains(msg, "QUERYSECRET777") {
		t.Fatalf("secret leaked in error: %v", err)
	}
	if strings.Contains(msg, rawURL) {
		t.Fatalf("raw feed URL leaked in error: %v", err)
	}
	if strings.Contains(msg, "example.com") {
		t.Fatalf("error = %v, host is no longer expected to be echoed on a parse failure", err)
	}
	want := `feed 'ransomware_feeds[0]' URL is not valid: feed URL is malformed: parse "[redacted URL]": invalid port "[redacted]" after host`
	if msg != want {
		t.Fatalf("error = %q, want %q", msg, want)
	}
}

func TestValidateFeedURLRedactsPathSecretOnParseFailure(t *testing.T) {
	rawURL := "https://example.com/feed/PATHSECRET777%zz?ok=1" //nolint:gosec // G101: test fixture, not a real credential

	err := validateFeedURL(rawURL, "general_feeds[1]")
	if err == nil {
		t.Fatal("expected error for malformed feed URL")
	}
	msg := err.Error()
	t.Logf("error: %s", msg)
	if strings.Contains(msg, "PATHSECRET777") {
		t.Fatalf("secret leaked in error: %v", err)
	}
	if strings.Contains(msg, rawURL) {
		t.Fatalf("raw feed URL leaked in error: %v", err)
	}
	for _, want := range []string{"general_feeds[1]", "invalid URL escape"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error = %v, want it to still contain %q for diagnosis", err, want)
		}
	}
}

func TestValidateFeedURLRedactsUserinfoSecretOnParseFailure(t *testing.T) {
	rawURL := "https://user:USERINFOSECRET777@example.com:bad/feed" //nolint:gosec // G101: test fixture, not a real credential

	err := validateFeedURL(rawURL, "government_feeds[0]")
	if err == nil {
		t.Fatal("expected error for malformed feed URL")
	}
	msg := err.Error()
	t.Logf("error: %s", msg)
	for _, leaked := range []string{"USERINFOSECRET777", "user:USERINFOSECRET777", rawURL} {
		if strings.Contains(msg, leaked) {
			t.Fatalf("userinfo secret leaked in error (%q found): %v", leaked, err)
		}
	}
	if !strings.Contains(msg, "invalid port") {
		t.Fatalf("error = %v, want it to still contain the parse failure reason", err)
	}
}

// TestValidateFeedURLLeavesSafeMessageUnredacted is the shape that must NOT
// be touched by the fix: a feed URL that parses successfully (no *url.Error,
// nothing to redact) but fails a later, already-safe feedurl.Validate check
// that never echoes the raw URL. Proves the redaction pass added to
// validateFeedURL does not degrade a message that was never a leak risk.
//
// F3, P3 (found 2026-09-04): tightened from a strings.Contains check to
// exact byte equality -- what the comment above always claimed to pin. A
// substring check would still pass if validateFeedURL silently added extra
// text (or partially redacted the safe message), so it was not actually
// proving the message survives untouched.
func TestValidateFeedURLLeavesSafeMessageUnredacted(t *testing.T) {
	rawURL := "http://example.com/feed"

	err := validateFeedURL(rawURL, "general_feeds[3]")
	if err == nil {
		t.Fatal("expected error for disallowed plain-http feed URL")
	}
	msg := err.Error()
	t.Logf("error: %s", msg)
	want := `feed 'general_feeds[3]' URL is not valid: feed URL scheme must be https (got "http")`
	if msg != want {
		t.Fatalf("error = %q, want exactly %q", msg, want)
	}
}

// F3, P3 (found 2026-09-04, code-reviewer): the six original tests in this
// file are each built from a genuine url.Parse failure, but every one of
// their inputs happens to have no %q-escaping byte (backslash, double quote,
// control char) before the secret and never targets a built-in Discord/Slack
// host -- so a scratch-overlay mutation of textutil.RedactWebhookSecretsForURL
// (deleting the strconv.Quote tier-2 pass, or moving RedactWebhookSecrets
// before the anchored tiers) left all six green; only internal/textutil's
// own suite caught it. This test closes that blind spot on the feed side.
//
// The delimiter has to be one genericHTTPURLRegex itself does not match on
// (a double quote, here) so that tier 1's independent whole-URL regex match
// cannot mask the same gap and the test end up passing for the wrong
// reason: the regex's char class stops at the quote, so its match never
// reaches the token either way, and only the whole-string tier-2 raw+%q
// ReplaceAll pair (anchored on the complete feed URL) can still catch it.
// The trailing tab is what actually makes url.Parse fail, mirroring the
// webhook-side TestRedactWebhookSecretsForURLSurvivesDelimiterBeforeSecret
// this test is the feed-path counterpart of. Mutation-proved below.
func TestValidateFeedURLRedactsSecretAfterQuoteDelimiterOnParseFailure(t *testing.T) {
	const token = "QUOTEDELIMSECRET777"
	rawURL := "https://example.com/feed/a\"b/" + token + "\t/tail"

	err := validateFeedURL(rawURL, "general_feeds[4]")
	if err == nil {
		t.Fatal("expected error for malformed feed URL")
	}
	msg := err.Error()
	t.Logf("error: %s", msg)
	if strings.Contains(msg, token) {
		t.Fatalf("secret leaked in error: %v", err)
	}
	if strings.Contains(msg, rawURL) {
		t.Fatalf("raw feed URL leaked in error: %v", err)
	}
	if !strings.Contains(msg, "general_feeds[4]") {
		t.Fatalf("error = %v, want config field context", err)
	}
}

// TestValidateFeedURLRedactsSlashInUserinfoOnParseFailure is the feed-side
// counterpart of internal/config's own
// TestValidateAPIConfigRedactsUserinfoOnParseFailure
// (api_base_url_secret_redaction_test.go): both go through
// textutil.RedactWebhookErrorForURL, but only the API-side test and
// internal/textutil's own suite ever exercised the F1 authority-truncation
// hardening in rawWebhookSchemeHost (its last-'@'-wins scan, needed
// specifically when the userinfo password contains an unescaped '/' before
// its own '@'). Found in review 2026-09-04 (sixth round on this defect
// class): reverting that hardening in a scratch overlay left every existing
// test in THIS file green -- none of the six original cases here, nor the
// F3 quote-delimiter case, has a slash in the userinfo before its '@' -- so
// this file was blind to a regression the API-side file and textutil's own
// suite would have caught. This closes that gap directly on the feed path.
//
// UPDATED 2026-09-04 (RedactURLCredentials parse-failure fallback, operator
// decision): the fixture used to force the url.Parse failure with an
// invalid ":bad" port on the real host ("...@example.com:bad/feed"). That
// literal port survived, verbatim, into RedactWebhookSecretsForURL's own
// fallback marker (rawWebhookSchemeHost has no port validation, only a
// control-byte/quote/backslash safety check), which made the RECONSTRUCTED
// marker "https://example.com:bad/[redacted]" itself fail url.Parse when
// RedactURLCredentials's later re-scan (inside the RedactWebhookSecrets call
// RedactWebhookSecretsForURL makes on its own output) got to it -- and this
// round's fallback fix now returns the fully generic marker unconditionally
// on that branch, erasing the host RedactWebhookSecretsForURL had already
// safely preserved. That interaction would have made this test's OWN
// property -- rawWebhookSchemeHost's smuggled-userinfo hardening produces
// the correct real host, not the credential, in the visible marker --
// unobservable at this integration layer, silently reopening exactly the
// blind spot this test was written to close. Rather than weaken the
// assertion, the fixture now forces the parse failure with a trailing
// control byte in the PATH instead (mirroring
// TestValidateFeedURLRedactsSecretAfterQuoteDelimiterOnParseFailure's own
// tab-delimiter approach) so the extracted host stays a clean
// "example.com" with nothing appended -- the reconstructed marker parses
// successfully, RedactURLCredentials's re-scan takes its ordinary
// success path (no fallback involved at all), and the real host stays
// observably correct end to end, still proving the property this test
// exists for. The control byte sits INSIDE the path rather than at the very
// end: feedurl.Parse rejects any rawURL with leading/trailing whitespace
// before url.Parse ever runs (a trailing tab, tried first, is whitespace and
// was silently trimmed away by that earlier check, producing a different
// error entirely and never reaching url.Parse at all). internal/textutil's
// own suite
// (TestRedactURLCredentialsSmuggledCredentialWithUnsafeRealHostFallsBackToPlaceholder
// and neighbours) separately covers the fallback-marker-itself-unparsable
// shape directly, without this test's config-integration layer in the way.
func TestValidateFeedURLRedactsSlashInUserinfoOnParseFailure(t *testing.T) {
	const password = "FEEDB64TOKEN"
	const secretTail = "FEEDSLASHSECRET777"
	rawURL := "https://svc:" + password + "/" + secretTail + "@example.com/fe\x01ed" //nolint:gosec // G101: test fixture, not a real credential

	err := validateFeedURL(rawURL, "general_feeds[5]")
	if err == nil {
		t.Fatal("expected error for malformed feed URL")
	}
	msg := err.Error()
	t.Logf("error: %s", msg)
	for _, leaked := range []string{password, secretTail, rawURL} {
		if strings.Contains(msg, leaked) {
			t.Fatalf("LEAK: %q survives: %v", leaked, err)
		}
	}
	if !strings.Contains(msg, "example.com") {
		t.Fatalf("error = %v, want the real host (past the smuggled userinfo) preserved for diagnosis", err)
	}
}

// TestCanonicalFeedURLRedactsSecretOnParseFailure pins the same fix applied
// to canonicalFeedURL's own feedurl.Parse call -- a sibling of
// validateFeedURL's leak, unreachable through validateFeedURLs today (it
// only runs after validateFeedURL has already parsed the same rawURL
// successfully) but directly unit-tested and reachable by any future caller
// that does not go through validateFeedURL first.
func TestCanonicalFeedURLRedactsSecretOnParseFailure(t *testing.T) {
	rawURL := "https://example.com:bad/feed?apikey=CANONSECRET777" //nolint:gosec // G101: test fixture, not a real credential

	_, err := canonicalFeedURL(rawURL)
	if err == nil {
		t.Fatal("expected error for malformed feed URL")
	}
	msg := err.Error()
	t.Logf("error: %s", msg)
	if strings.Contains(msg, "CANONSECRET777") {
		t.Fatalf("secret leaked in error: %v", err)
	}
	if !strings.Contains(msg, "invalid port") {
		t.Fatalf("error = %v, want it to still contain the parse failure reason", err)
	}
}
