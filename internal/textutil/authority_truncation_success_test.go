package textutil

import (
	"errors"
	"net/url"
	"strings"
	"testing"
)

// This file pins the sixth round of fixes on this defect class (found
// 2026-09-04): F5 (the url.Parse-SUCCESS branch of both
// RedactWebhookSecretsForURL and RedactURLCredentials trusted
// parsed.Host/parsed.User even when net/url's own authority-truncation bug
// made them wrong), F6 (redactURLErrorSubstringFragments's %q-fragment
// extraction could not span an escaped quote inside the fragment), and F7 (a
// "#" near the start of a malformed anchor made tier 2c's needle a single
// character, corrupting unrelated prose).

// F5 -- the exact reported shape: a userinfo containing '/' and no ':' makes
// net/url treat the credential itself as parsed.Host, with parsed.User nil
// (there is no error at all, let alone a *url.Error for the fallback tiers
// to run against). RedactURLCredentials has no anchor and no fallback of any
// kind on top of what url.Parse reports, so this is the call site every
// RSS/status log line that names a feed goes through on ordinary, successful
// operation.
func TestRedactURLCredentialsRedactsSmuggledCredentialOnParseSuccess(t *testing.T) {
	const token = "FEEDTOKEN777"
	raw := "https://" + token + "/x@feeds.example.test/rss"

	if _, err := url.Parse(raw); err != nil {
		t.Fatalf("precondition: url.Parse(%q) should SUCCEED (this is the parse-success branch), got %v", raw, err)
	}

	text := `"feed_url":"` + raw + `"`
	got := RedactURLCredentials(text)

	if strings.Contains(got, token) {
		t.Fatalf("LEAK: %s", got)
	}
	if !strings.Contains(got, "feeds.example.test") {
		t.Fatalf("expected the real host preserved for diagnosis, got: %s", got)
	}
}

// F5 -- the exact reported end-to-end shape: a feed URL of this form loads
// successfully through feedurl.Validate (there is no userinfo for it to
// reject -- net/url never reports one) and reaches bot.log's "feed_url"
// field, logged four times per the finding, through RedactURLCredentials
// alone with no other anchor available.
func TestRedactURLCredentialsRedactsSmuggledCredentialRepeatedOccurrences(t *testing.T) {
	const token = "FEEDTOKEN777"
	raw := "https://" + token + "/x@feeds.example.test/rss"
	text := strings.Repeat(`"feed_url":"`+raw+`" `, 4)

	got := RedactURLCredentials(text)

	if strings.Contains(got, token) {
		t.Fatalf("LEAK: %s", got)
	}
	if strings.Count(got, "feeds.example.test") != 4 {
		t.Fatalf("expected all four occurrences redacted with the real host preserved, got: %s", got)
	}
}

// F5 -- the same shape on the webhook path, through
// RedactWebhookSecretsForURL's success branch (textutil.go:554 in the
// finding), where the marker is built directly from parsed.Host with no
// error to redact through.
func TestRedactWebhookSecretsForURLMarkerDoesNotLeakSmuggledCredentialOnParseSuccess(t *testing.T) {
	const token = "TOKENIII"
	raw := "https://" + token + "/x@host.example.test/rss"

	if _, err := url.Parse(raw); err != nil {
		t.Fatalf("precondition: url.Parse(%q) should SUCCEED, got %v", raw, err)
	}

	text := realURLError(t, "Post", raw, errF3SimulatedTransportFailure)
	got := RedactWebhookSecretsForURL(text, raw)

	if strings.Contains(got, token) {
		t.Fatalf("LEAK (parse-success marker built from smuggled parsed.Host): %s", got)
	}
	if !strings.Contains(got, "https://host.example.test/[redacted]") {
		t.Fatalf("expected the correctly-hosted marker (real host past the smuggled '@'), got: %s", got)
	}
}

// F5 -- the same shape on a built-in webhook host, proving the override
// correctly identifies hooks.slack.com as the real host rather than leaving
// the credential as the marker's host.
func TestRedactWebhookSecretsForURLMarkerFindsBuiltInHostPastSmuggledCredential(t *testing.T) {
	const token = "WHSECRET777"
	raw := "https://" + token + "/x@hooks.slack.com/services/T00/B00"

	if _, err := url.Parse(raw); err != nil {
		t.Fatalf("precondition: url.Parse(%q) should SUCCEED, got %v", raw, err)
	}

	text := realURLError(t, "Post", raw, errF3SimulatedTransportFailure)
	got := RedactWebhookSecretsForURL(text, raw)

	if strings.Contains(got, token) {
		t.Fatalf("LEAK: %s", got)
	}
	if !strings.Contains(got, "https://hooks.slack.com/[redacted]") {
		t.Fatalf("expected the built-in host recognised past the smuggled credential, got: %s", got)
	}
}

// F5 -- an ordinary URL with a genuine, unrelated '@' in a query VALUE (an
// email address is the common real-world case) must NOT be treated as a
// smuggled userinfo delimiter: realAuthorityCandidate excludes the query
// from its scan specifically so this stays byte-identical to the pre-fix
// behaviour rather than losing the real host to a false positive.
func TestRedactURLCredentialsUnrelatedAtInQueryValueIsNotMistakenForSmuggledCredential(t *testing.T) {
	raw := "https://host.example.test/path?redirect=a@b"
	text := "see " + raw + " for detail"

	got := RedactURLCredentials(text)

	if got != text {
		t.Fatalf("got %q, want unchanged (no userinfo, no sensitive query key, unrelated '@' in query value): %s", got, got)
	}
}

// F5 -- the same non-regression, this time with a sensitive query key
// alongside the unrelated '@', proving the existing query-redaction branch
// still runs normally (is not short-circuited by the new check) when there
// is no smuggled credential.
func TestRedactURLCredentialsOrdinaryQuerySecretStillRedactedAlongsideUnrelatedAt(t *testing.T) {
	raw := "https://host.example.test/path?redirect=a@b&token=PLAINQUERYSECRET1"
	text := "see " + raw + " for detail"

	got := RedactURLCredentials(text)

	if strings.Contains(got, "PLAINQUERYSECRET1") {
		t.Fatalf("LEAK: %s", got)
	}
	if !strings.Contains(got, "host.example.test") {
		t.Fatalf("expected the real host preserved, got: %s", got)
	}
}

// F5 -- an ordinary userinfo credential (no smuggling shape at all) must
// still be redacted exactly as before: realAuthorityCandidate's raw scan
// agrees with parsed.Host here, so mismatched is false and the pre-existing
// parsed.User redaction path runs unchanged.
func TestRedactURLCredentialsOrdinaryUserinfoUnaffectedByF5Change(t *testing.T) {
	raw := "https://user:ORDINARYPASSWORD1@host.example.test/rss" //nolint:gosec // G101: test fixture, not a real credential
	text := "see " + raw + " for detail"

	got := RedactURLCredentials(text)

	if strings.Contains(got, "ORDINARYPASSWORD1") || strings.Contains(got, "user:") {
		t.Fatalf("LEAK: %s", got)
	}
	if !strings.Contains(got, "host.example.test") {
		t.Fatalf("expected the real host preserved, got: %s", got)
	}
}

// F5 -- guards the isLogSafeHost gate on the override path: when the raw
// scan's own answer is itself not log-safe (contains a backslash), the
// function must fall back to the generic placeholder rather than echoing an
// unsafe byte into the marker.
func TestRedactURLCredentialsSmuggledCredentialWithUnsafeRealHostFallsBackToPlaceholder(t *testing.T) {
	const token = "UNSAFEHOSTTOKEN1"
	raw := `https://` + token + `/x@ho` + `\` + `st.example.test/rss`
	text := "see " + raw + " for detail"

	got := RedactURLCredentials(text)

	if strings.Contains(got, token) {
		t.Fatalf("LEAK: %s", got)
	}
	if !strings.Contains(got, "[redacted URL]") {
		t.Fatalf("expected the generic placeholder when the raw-scanned host is not log-safe, got: %s", got)
	}
}

// F5 -- found by this round's own exhaustive sweep, not the original review:
// net/url's exported Parse cuts rawURL at the FIRST '#' before doing
// anything else, so a credential with no path and no query, immediately
// followed by '#', makes net/url treat the credential as the WHOLE bare
// hostname and hand back everything past the '#' as Fragment -- hiding the
// real host there instead of in a path segment. realAuthorityCandidate's
// query exclusion (added to avoid an unrelated '@' in a query VALUE being
// mistaken for smuggling) does not touch the fragment, specifically so this
// shape stays covered.
func TestRedactURLCredentialsSmuggledCredentialViaFragmentTruncation(t *testing.T) {
	const token = "FRAGTOKEN1"
	raw := "https://" + token + "#tail/x@host.example.test/rss"
	text := `"feed_url":"` + raw + `"`

	got := RedactURLCredentials(text)

	if strings.Contains(got, token) {
		t.Fatalf("LEAK: %s", got)
	}
	if !strings.Contains(got, "host.example.test") {
		t.Fatalf("expected the real host (past the fragment cut) preserved, got: %s", got)
	}
}

// F5 -- the webhook-path twin of the fragment-truncation case above.
func TestRedactWebhookSecretsForURLSmuggledCredentialViaFragmentTruncation(t *testing.T) {
	const token = "FRAGTOKEN2"
	raw := "https://" + token + "#tail/x@host.example.test/rss"
	text := realURLError(t, "Post", raw, errF3SimulatedTransportFailure)

	got := RedactWebhookSecretsForURL(text, raw)

	if strings.Contains(got, token) {
		t.Fatalf("LEAK: %s", got)
	}
	if !strings.Contains(got, "https://host.example.test/[redacted]") {
		t.Fatalf("expected the correctly-hosted marker, got: %s", got)
	}
}

// F5 -- found by this round's own exhaustive sweep: net/url bounds the
// authority at the first of '/' OR '?', so a credential with NO path at all
// before a '?' makes net/url treat the credential as the WHOLE bare
// hostname and hand back everything past the '?' as RawQuery -- hiding the
// real host there instead of in a path segment. This is the case
// realAuthorityCandidate's parsed.Path == "" check exists to still catch
// even though the query is normally excluded from the scan.
func TestRedactURLCredentialsSmuggledCredentialViaQueryWithNoPath(t *testing.T) {
	const token = "QTOKEN1"
	raw := "https://" + token + "?tail/x@host.example.test/rss"
	text := `"feed_url":"` + raw + `"`

	got := RedactURLCredentials(text)

	if strings.Contains(got, token) {
		t.Fatalf("LEAK: %s", got)
	}
	if !strings.Contains(got, "host.example.test") {
		t.Fatalf("expected the real host (past the query, no path present) preserved, got: %s", got)
	}
}

// F5 -- the webhook-path twin of the query-with-no-path case above.
func TestRedactWebhookSecretsForURLSmuggledCredentialViaQueryWithNoPath(t *testing.T) {
	const token = "QTOKEN2"
	raw := "https://" + token + "?tail/x@host.example.test/rss"
	text := realURLError(t, "Post", raw, errF3SimulatedTransportFailure)

	got := RedactWebhookSecretsForURL(text, raw)

	if strings.Contains(got, token) {
		t.Fatalf("LEAK: %s", got)
	}
	if !strings.Contains(got, "https://host.example.test/[redacted]") {
		t.Fatalf("expected the correctly-hosted marker, got: %s", got)
	}
}

// F5 -- a WITH-path query case must still be protected against the
// unrelated-'@'-in-a-query-value false positive: this is the non-regression
// twin of the two cases above, proving parsed.Path != "" still excludes the
// query from the scan the normal way.
func TestRedactURLCredentialsOrdinaryPathAndQueryWithAtStillExcludesQuery(t *testing.T) {
	raw := "https://host.example.test/path?redirect=a@b"
	text := "see " + raw + " for detail"

	got := RedactURLCredentials(text)

	if got != text {
		t.Fatalf("got %q, want unchanged (path present, query '@' must stay excluded from the scan): %s", got, got)
	}
}

// F6 -- reproduces the exact reported feed-path finding text: a userinfo
// password containing a literal double quote makes the OLD %q-fragment
// regex ("[^\"]*") stop at the escaped quote's own second byte instead of
// the fragment's real closing delimiter, so neither the regex match nor its
// strconv.Unquote fallback ever equal a substring of secret and the whole
// port fragment -- including the trailing part of the password -- survives.
func TestRedactWebhookSecretsForURLRedactsQuoteEmbeddedPortFragmentFeedPath(t *testing.T) {
	const password = `PWDKKK"1`
	// A trailing "/tail@..." (rather than a bare "@...") is what actually
	// reaches parseHost's "invalid port" branch instead of parseAuthority's
	// earlier "invalid userinfo" check: net/url's authority-truncation-at-
	// first-'/' bug (see rawWebhookSchemeHost's own F1 doc comment) makes it
	// treat "svc:PWDKKK\"1" -- everything up to that first '/' -- as the
	// WHOLE authority with no '@' in it, so parseHost splits it at the first
	// ':' into host="svc" and colonPort=":PWDKKK\"1", which is what fails
	// validOptionalPort and gets %q-rendered into the error.
	raw := "https://svc:" + password + "/tail@feeds.example.test/rss"
	text := realParseFailure(t, raw)
	if !strings.Contains(text, "invalid port") {
		t.Fatalf("precondition: expected a net/url 'invalid port' detail, got %q", text)
	}
	t.Logf("pre-redaction text: %s", text)

	got := RedactWebhookSecretsForURL(text, raw)

	if strings.Contains(got, "PWDKKK") {
		t.Fatalf("LEAK: quote-embedded password survives: %s", got)
	}
	if !strings.Contains(got, "https://feeds.example.test/[redacted]") {
		t.Fatalf("expected the correctly-hosted marker, got: %s", got)
	}
}

// F6 -- the webhook-path twin of the case above, on a built-in host.
func TestRedactWebhookSecretsForURLRedactsQuoteEmbeddedPortFragmentWebhookPath(t *testing.T) {
	const password = `WHSECRET"9`
	raw := "https://svc:" + password + "/tail@hooks.slack.com/services/T00/B00"
	text := realParseFailure(t, raw)
	if !strings.Contains(text, "invalid port") {
		t.Fatalf("precondition: expected a net/url 'invalid port' detail, got %q", text)
	}
	t.Logf("pre-redaction text: %s", text)

	got := RedactWebhookSecretsForURL(text, raw)

	if strings.Contains(got, "WHSECRET") {
		t.Fatalf("LEAK: quote-embedded password survives: %s", got)
	}
	if !strings.Contains(got, "https://hooks.slack.com/[redacted]") {
		t.Fatalf("expected the correctly-hosted marker, got: %s", got)
	}
}

// F6 -- two embedded quotes in the same password, the multi-quote shape
// named in the finding ("two quotes: invalid port \":A\\\"PWDQQ\\\"...\"
// after host").
func TestRedactWebhookSecretsForURLRedactsPasswordWithTwoEmbeddedQuotes(t *testing.T) {
	const password = `A"PWDQQ"9` //nolint:gosec // G101: test fixture, not a real credential
	raw := "https://svc:" + password + "/tail@host.example.test/rss"
	text := realParseFailure(t, raw)

	got := RedactWebhookSecretsForURL(text, raw)

	if strings.Contains(got, "PWDQQ") {
		t.Fatalf("LEAK: multi-quote password survives: %s", got)
	}
}

// F6 -- direct unit coverage of redactURLErrorSubstringFragments itself with
// a hand-built err, isolating the fix from the rest of
// RedactWebhookSecretsForURL's tiers (which could otherwise also close the
// gap and mask a regression in this function specifically).
func TestRedactURLErrorSubstringFragmentsHandlesEmbeddedQuote(t *testing.T) {
	const secret = `https://svc::PWDKKK"1@host.example.test/rss` //nolint:gosec // G101: test fixture, not a real credential
	detail := `invalid port ":PWDKKK\"1" after host`
	err := &url.Error{Op: "parse", URL: "x", Err: errors.New(detail)}
	text := `parse "x": ` + detail

	got := redactURLErrorSubstringFragments(text, err, secret)

	if strings.Contains(got, "PWDKKK") {
		t.Fatalf("LEAK: %s", got)
	}
	want := `parse "x": invalid port "[redacted]" after host`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// F6, case-sensitivity item (review): the containment check must be
// case-sensitive. Every fragment net/url embeds is an exact-case raw slice
// of secret, so this never misses a real fragment; it only, correctly,
// leaves a coincidentally similar but differently-cased quoted span alone.
// Direct unit test on the unexported helper, isolating this from any other
// pass that might otherwise also touch the text.
func TestRedactURLErrorSubstringFragmentsCaseSensitiveDoesNotMatchDifferentCase(t *testing.T) {
	const secret = "https://svc:ABC123@host.example.test/rss" //nolint:gosec // G101: test fixture, not a real credential
	detail := `invalid port ":abc123" after host`             // lowercase; secret's password is uppercase
	err := &url.Error{Op: "parse", URL: "x", Err: errors.New(detail)}
	text := `parse "x": ` + detail

	got := redactURLErrorSubstringFragments(text, err, secret)

	if got != text {
		t.Fatalf("got %q, want unchanged: a differently-cased quoted span must not match an uppercase candidate from secret", got)
	}
}

// F6 -- guard against the forward-derivation scanning INTO the marker/query
// portion of detail and over-redacting: a candidate whose quoted form is
// longer than detail cannot possibly be found, and the loop must not panic
// or waste unbounded time on a long secret with a short detail.
func TestRedactURLErrorSubstringFragmentsHandlesLongSecretShortDetail(t *testing.T) {
	secret := "https://svc:" + strings.Repeat("A", 4000) + "@host.example.test/rss"
	detail := `invalid port "short" after host`
	err := &url.Error{Op: "parse", URL: "x", Err: errors.New(detail)}
	text := `parse "x": ` + detail

	got := redactURLErrorSubstringFragments(text, err, secret)

	if got != text {
		t.Fatalf("got %q, want unchanged (no candidate from this secret should match this detail)", got)
	}
}

// F7 -- reproduces the exact reported finding end to end: a "#" positioned
// before the scheme's own "://" makes preFragment the one-character string
// "h", and the old unconditional ReplaceAll(text, "h", marker) corrupted
// every "h" in the surrounding message, not just the malformed URL. text is
// feedurl.Parse's real, verbatim wording for this shape ("feed URL must use
// http or https scheme", from feedurl.Parse's parsed.Scheme == "" branch),
// wrapped exactly the way validateFeedURL wraps it -- the same call this
// function's %q/tier-two logic is written for.
func TestRedactWebhookSecretsForURLDoesNotCorruptMessageOnDegeneratePreFragmentAnchor(t *testing.T) {
	raw := "h#ttps://feeds.example.test/rss"
	text := `feed 'general_feeds[0]' URL is not valid: feed URL must use http or https scheme`

	got := RedactWebhookSecretsForURL(text, raw)

	if got != text {
		t.Fatalf("message corrupted by a degenerate one-character anchor: got %q, want unchanged %q", got, text)
	}
}

// F7 -- a genuinely credential-bearing preFragment (the case tier 2c exists
// for) must be unaffected by the added shape check: it contains "://" and is
// still fully redacted.
func TestRedactWebhookSecretsForURLTier2cStillRedactsGenuineCredentialBearingPreFragment(t *testing.T) {
	const password = "HASHLEAK777"
	raw := "https://svc:" + password + "#tail@host.example.test/rss"
	text := realParseFailure(t, raw)
	if !strings.Contains(text, password) {
		t.Fatalf("precondition: expected the raw *url.Error text to still carry the truncated URL field with the password, got %q", text)
	}

	got := RedactWebhookSecretsForURL(text, raw)

	if strings.Contains(got, password) {
		t.Fatalf("LEAK: pre-fragment-truncated URL field survives: %s", got)
	}
	if !strings.Contains(got, "https://host.example.test/[redacted]") {
		t.Fatalf("expected the correctly-hosted marker, got: %s", got)
	}
}
