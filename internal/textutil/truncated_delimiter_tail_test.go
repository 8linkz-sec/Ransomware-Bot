package textutil

import (
	"regexp"
	"strings"
	"testing"
)

// This file pins the eighth round of fixes on this defect class (found
// 2026-09-04): genericHTTPURLRegex stops a match at the first `"`, `'`, `<`
// or `)` byte, but everything past that byte -- including the rest of a
// credential the delimiter split in two -- was never part of any match and
// so passed through RedactURLCredentials (and, via its trailing call,
// RedactWebhookSecrets) completely unexamined. redactURLMatches closes this
// by swallowing the extended span into the generic "[redacted URL]" marker
// whenever a match was truncated by one of those four bytes AND the
// contiguous non-whitespace run starting at the delimiter contains an `@` --
// the signal a smuggled userinfo section needs to reach its real host. See
// redactURLMatches's own doc comment in textutil.go for the full design
// rationale, including why the gate is deliberately narrow and what it does
// not close.

// TestRedactURLCredentialsDoesNotLeakTailAfterQuoteTruncatesUserinfo is the
// exact reported shape: a literal double quote inside a credential ends the
// regex match right there, and everything after it -- the rest of the
// credential, the '@', and the real host -- used to survive untouched.
func TestRedactURLCredentialsDoesNotLeakTailAfterQuoteTruncatesUserinfo(t *testing.T) {
	const secret = "SECRET"
	const tail = "TAIL"
	text := `Get "https://user:` + secret + `"` + tail + `@real-host.example/path": dial tcp: no such host`

	got := RedactURLCredentials(text)

	for _, leaked := range []string{secret, tail, "real-host.example"} {
		if strings.Contains(got, leaked) {
			t.Fatalf("RedactURLCredentials() leaked %q: %s", leaked, got)
		}
	}
	want := `Get "[redacted URL] dial tcp: no such host`
	if got != want {
		t.Fatalf("RedactURLCredentials(%q) = %q, want %q", text, got, want)
	}
}

// TestRedactURLCredentialsDoesNotLeakTailAfterApostropheTruncatesUserinfo is
// the same shape with the delimiter that is a valid RFC 3986 sub-delim (`'`),
// so genericHTTPURLRegex still excludes it from a match.
func TestRedactURLCredentialsDoesNotLeakTailAfterApostropheTruncatesUserinfo(t *testing.T) {
	const secret = "SECRET"
	const tail = "TAIL"
	text := `Get "https://user:` + secret + `'` + tail + `@real-host.example/path": dial tcp: no such host`

	got := RedactURLCredentials(text)

	for _, leaked := range []string{secret, tail, "real-host.example"} {
		if strings.Contains(got, leaked) {
			t.Fatalf("RedactURLCredentials() leaked %q: %s", leaked, got)
		}
	}
	want := `Get "[redacted URL] dial tcp: no such host`
	if got != want {
		t.Fatalf("RedactURLCredentials(%q) = %q, want %q", text, got, want)
	}
}

// TestRedactURLCredentialsDoesNotLeakTailAfterLessThanTruncatesUserinfo is
// the same shape with `<` as the truncating delimiter.
func TestRedactURLCredentialsDoesNotLeakTailAfterLessThanTruncatesUserinfo(t *testing.T) {
	const secret = "SECRET"
	const tail = "TAIL"
	text := `Get "https://user:` + secret + `<` + tail + `@real-host.example/path": dial tcp: no such host`

	got := RedactURLCredentials(text)

	for _, leaked := range []string{secret, tail, "real-host.example"} {
		if strings.Contains(got, leaked) {
			t.Fatalf("RedactURLCredentials() leaked %q: %s", leaked, got)
		}
	}
	want := `Get "[redacted URL] dial tcp: no such host`
	if got != want {
		t.Fatalf("RedactURLCredentials(%q) = %q, want %q", text, got, want)
	}
}

// TestRedactURLCredentialsDoesNotLeakTailAfterRightParenTruncatesUserinfo is
// the same shape with `)` as the truncating delimiter -- the byte a
// parenthesized prose URL reference also ends on (see
// TestRedactURLCredentialsLeavesBenignParenthesizedURLUnchanged below for the
// case where this must NOT fire).
func TestRedactURLCredentialsDoesNotLeakTailAfterRightParenTruncatesUserinfo(t *testing.T) {
	const secret = "SECRET"
	const tail = "TAIL"
	text := `Get "https://user:` + secret + `)` + tail + `@real-host.example/path": dial tcp: no such host`

	got := RedactURLCredentials(text)

	for _, leaked := range []string{secret, tail, "real-host.example"} {
		if strings.Contains(got, leaked) {
			t.Fatalf("RedactURLCredentials() leaked %q: %s", leaked, got)
		}
	}
	want := `Get "[redacted URL] dial tcp: no such host`
	if got != want {
		t.Fatalf("RedactURLCredentials(%q) = %q, want %q", text, got, want)
	}
}

// TestRedactURLCredentialsDoesNotLeakTailWithStrayDelimiterBeforeTheRealAtSign
// puts a second delimiter byte inside the tail, before the real '@' and
// before any whitespace. The tail scan must keep going past it -- it stops
// only on whitespace (isASCIISpaceByte), never on a delimiter
// (isURLTruncationDelimiter) -- or the swallow would end early and re-leak
// everything from the stray delimiter onward, including the real host. None
// of the four single-delimiter tests above can catch a tail scan that stops
// on the wrong byte class, because none of them has a second delimiter byte
// inside the tail.
func TestRedactURLCredentialsDoesNotLeakTailWithStrayDelimiterBeforeTheRealAtSign(t *testing.T) {
	const secret = "SECRET"
	const tailHead = "TAI"
	const tailAfterStray = "L"
	text := `Get "https://user:` + secret + `"` + tailHead + `'` + tailAfterStray + `@real-host.example/path": dial tcp: no such host`

	got := RedactURLCredentials(text)

	for _, leaked := range []string{secret, tailHead, "real-host.example"} {
		if strings.Contains(got, leaked) {
			t.Fatalf("RedactURLCredentials() leaked %q: %s", leaked, got)
		}
	}
	want := `Get "[redacted URL] dial tcp: no such host`
	if got != want {
		t.Fatalf("RedactURLCredentials(%q) = %q, want %q", text, got, want)
	}
}

// TestRedactURLCredentialsDoesNotLeakSecondOccurrenceQuoteTruncated is a
// second, independently sourced adversarial input for the same gate: the
// smuggled '@' and the real host sit past the quote that truncates the
// match, in a bare URL with no surrounding error text at all.
func TestRedactURLCredentialsDoesNotLeakSecondOccurrenceQuoteTruncated(t *testing.T) {
	const secret = "QUOTESECRET"
	text := `https://svc:` + secret + `"z@feeds.example.test:abc/rss`

	got := RedactURLCredentials(text)

	for _, leaked := range []string{secret, "feeds.example.test"} {
		if strings.Contains(got, leaked) {
			t.Fatalf("RedactURLCredentials() leaked %q: %s", leaked, got)
		}
	}
	want := `[redacted URL]`
	if got != want {
		t.Fatalf("RedactURLCredentials(%q) = %q, want %q", text, got, want)
	}
}

// TestRedactURLCredentialsNeverDerivesHostFromATruncatedTail guards the
// design's central guarantee: once a match is known to be incomplete,
// nothing about what follows it -- including a "clever" reassembly of
// pre-delimiter and tail bytes that happens to look like a scheme+host -- is
// ever trusted as structure. The swallowed span always collapses to the same
// unconditional generic marker; it never echoes a host derived from the
// combined span, unlike RedactWebhookSecretsForURL's fallback, which is
// deliberately allowed to echo a host it already independently validated.
func TestRedactURLCredentialsNeverDerivesHostFromATruncatedTail(t *testing.T) {
	const secret = "SECRET"
	const decoyHost = "attacker.example"
	text := `Get "https://user:` + secret + `"` + decoyHost + `@real-host.example/path": dial tcp: no such host`

	got := RedactURLCredentials(text)

	for _, leaked := range []string{secret, decoyHost, "real-host.example"} {
		if strings.Contains(got, leaked) {
			t.Fatalf("RedactURLCredentials() leaked %q: %s", leaked, got)
		}
	}
	want := `Get "[redacted URL] dial tcp: no such host`
	if got != want {
		t.Fatalf("RedactURLCredentials(%q) = %q, want %q", text, got, want)
	}
}

// TestRedactURLCredentialsSwallowsTailWhenAtSignImmediatelyFollowsDelimiter
// puts the `@` in the very first position after the truncating delimiter, so
// the whole tail run is `"@...`. The credential itself ended at the
// delimiter, so nothing secret is left in the tail -- but the real host past
// the `@` is, and once a match is known to be incomplete nothing after it is
// trusted. None of the tests above reaches this offset: every one of them has
// at least one tail byte between the delimiter and the `@`, so a gate that
// required the `@` at offset two or later would pass all of them and still
// leak here.
func TestRedactURLCredentialsSwallowsTailWhenAtSignImmediatelyFollowsDelimiter(t *testing.T) {
	const secret = "SEC" + "RET"
	text := `Get "https://user:` + secret + `"@real-host.example/path": dial tcp: no such host`

	got := RedactURLCredentials(text)

	for _, leaked := range []string{secret, "real-host.example"} {
		if strings.Contains(got, leaked) {
			t.Fatalf("RedactURLCredentials() leaked %q: %s", leaked, got)
		}
	}
	want := `Get "[redacted URL] dial tcp: no such host`
	if got != want {
		t.Fatalf("RedactURLCredentials(%q) = %q, want %q", text, got, want)
	}
}

// TestRedactURLCredentialsSwallowsTailWhenTruncatedMatchNeedsNoRedactionItself
// truncates a match that is, on its own, a perfectly ordinary URL the redact
// callback returns byte-for-byte unchanged -- while the credential and the
// real host sit entirely in the tail past the delimiter. The decision to
// swallow must depend only on the delimiter and the `@` in the tail run,
// never on whether the callback found anything to mask inside the match:
// every other test in this file truncates a match that fails to parse (an
// invalid port) and so is always rewritten by the callback, which cannot
// distinguish the two rules.
func TestRedactURLCredentialsSwallowsTailWhenTruncatedMatchNeedsNoRedactionItself(t *testing.T) {
	const secret = "TOKEN" + "TAIL"
	text := `Get "https://feeds.example.test/rss"` + secret + `@real-host.example/path": dial tcp`

	got := RedactURLCredentials(text)

	for _, leaked := range []string{secret, "real-host.example", "feeds.example.test"} {
		if strings.Contains(got, leaked) {
			t.Fatalf("RedactURLCredentials() leaked %q: %s", leaked, got)
		}
	}
	want := `Get "[redacted URL] dial tcp`
	if got != want {
		t.Fatalf("RedactURLCredentials(%q) = %q, want %q", text, got, want)
	}
}

// TestRedactURLCredentialsLeavesBenignParenthesizedURLUnchanged is the
// negative case the '@'-in-tail gate exists to leave alone: a complete,
// self-contained URL followed by ordinary prose punctuation, with no '@'
// anywhere past the delimiter. Must be byte-identical to the input.
func TestRedactURLCredentialsLeavesBenignParenthesizedURLUnchanged(t *testing.T) {
	text := `(see https://example.com/path)`

	got := RedactURLCredentials(text)

	if got != text {
		t.Fatalf("RedactURLCredentials(%q) = %q, want it unchanged", text, got)
	}
}

// TestRedactURLCredentialsLeavesQuotedCompleteURLErrorTextUnchanged is a
// second negative case, with the '@' sitting INSIDE the match (a query
// value) rather than in the tail -- the gate must not fire on that, and the
// existing url.Parse-failure fallback behaviour for this exact input is
// unchanged from before this fix.
func TestRedactURLCredentialsLeavesQuotedCompleteURLErrorTextUnchanged(t *testing.T) {
	const secret = "REALSECRET123"
	text := `Get "https://feeds.example.test:abc/rss?user=admin@corp.example&apikey=` + secret + `": invalid port`

	got := RedactURLCredentials(text)

	if strings.Contains(got, secret) {
		t.Fatalf("RedactURLCredentials() leaked query secret: %s", got)
	}
	want := `Get "[redacted URL]": invalid port`
	if got != want {
		t.Fatalf("RedactURLCredentials(%q) = %q, want %q", text, got, want)
	}
}

// TestRedactURLCredentialsTailScanMatchesRegexpWhitespaceClass pins the one
// invariant the tail scan cannot be written without: isASCIISpaceByte must
// list exactly the bytes Go's regexp `\s` class matches. `\s` is [\t\n\f\r ]
// and does NOT include '\v' (0x0b), so genericHTTPURLRegex matches straight
// through a 0x0b byte; a tail scan that treated '\v' as whitespace would
// stop at a byte the regex itself ran past, and a single 0x0b placed before
// the real '@' would carry the rest of a credential out of the gate's reach
// and reopen the leak in full.
//
// This sweeps every byte 0x00-0x7f and compares isASCIISpaceByte's
// definition of "whitespace" against the regexp package itself, rather than
// against a second hand-written byte list -- a coverage number cannot catch
// a wrong byte in a single switch statement, because the statement reads
// 100% either way regardless of which bytes are listed.
func TestRedactURLCredentialsTailScanMatchesRegexpWhitespaceClass(t *testing.T) {
	regexpSpace := regexp.MustCompile(`^\s$`)
	const delimiter = '"'
	const secret = "SECRET"
	const tail = "TAIL"

	for b := 0; b <= 0x7f; b++ {
		byteVal := byte(b)
		if byteVal == delimiter || byteVal == '@' {
			continue
		}
		text := "Get \"https://user:" + secret + "\"" + tail + string(byteVal) + "@real-host.example/path\": dial tcp"
		got := RedactURLCredentials(text)

		wantSwallowed := !regexpSpace.MatchString(string(byteVal))
		gotSwallowed := !strings.Contains(got, "real-host.example")

		if gotSwallowed != wantSwallowed {
			t.Fatalf(
				"byte 0x%02x: regexp \\s says whitespace=%v, but the tail was swallowed=%v (want swallowed=%v); got %q",
				b, !wantSwallowed, gotSwallowed, wantSwallowed, got,
			)
		}
	}
}

// TestRedactURLCredentialsHandlesASecondMatchInsideASwallowedTail pins the
// overlap guard (`if start < last { continue }`) in redactURLMatches: a
// second genericHTTPURLRegex match can begin INSIDE the tail span the first
// match's swallow already folded in. Without the guard this panics
// (slice bounds out of range) on a redaction path fed provider-controlled
// text -- internal/api/client.go's sanitizeAPIErrorBody redacts a raw HTTP
// error response body an upstream provider controls.
func TestRedactURLCredentialsHandlesASecondMatchInsideASwallowedTail(t *testing.T) {
	const firstSecret = "FIRSTSECRET"
	const secondSecret = "SECONDSECRET"
	text := `https://a:` + firstSecret + `"https://b:` + secondSecret + `@real-host.example/x rest`

	got := RedactURLCredentials(text)

	for _, leaked := range []string{firstSecret, secondSecret, "real-host.example"} {
		if strings.Contains(got, leaked) {
			t.Fatalf("RedactURLCredentials() leaked %q: %s", leaked, got)
		}
	}
	want := `[redacted URL] rest`
	if got != want {
		t.Fatalf("RedactURLCredentials(%q) = %q, want %q", text, got, want)
	}
}
