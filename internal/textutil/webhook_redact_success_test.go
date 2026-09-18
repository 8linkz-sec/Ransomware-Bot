package textutil

import (
	"net/url"
	"strings"
	"testing"
)

// F3, P2 -- third instance of the %q-escape-mismatch defect class, in the
// url.Parse-SUCCESS branch of RedactWebhookSecretsForURL: unlike the
// url.Parse-FAILURE fallback (tier two, fixed 2026-09-03), the success branch
// did only two byte-exact ReplaceAll passes (the raw normalized string, and
// rawURL.String() when parsed.RawPath != "") with no strconv.Quote
// counterpart. When the surrounding text is %q-rendered (what
// (*url.Error).Error() always does) and the webhook URL contains a backslash
// or a double quote that survives into that text UNESCAPED-by-Go's-own-URL-
// library (see the case below that does NOT reproduce this way), neither
// ReplaceAll pass matches and the token survives.
//
// realURLError builds the text this branch actually receives -- a genuine
// *url.Error{Op:"Post", ...}.Error() -- never a hand-written string. This
// mirrors http.Client.Do's own error shape (Op "Post"/"Get" for a live
// request failure, as opposed to url.Parse's own "parse" Op used by the
// fallback branch's realParseFailure helper in the sibling test file).
func realURLError(t *testing.T, op, raw string, cause error) string {
	t.Helper()
	if _, err := url.Parse(raw); err != nil {
		t.Fatalf("precondition: url.Parse(%q) should SUCCEED (this is the success-branch case), got %v", raw, err)
	}
	uerr := &url.Error{Op: op, URL: raw, Err: cause}
	return uerr.Error()
}

var errF3SimulatedTransportFailure = &stubTransportError{msg: "dial tcp: simulated failure"}

// stubTransportError stands in for the *net.OpError http.Client.Do wraps a
// real dial failure in -- only its Error() text matters here, so a minimal
// stub avoids depending on actually triggering one.
type stubTransportError struct{ msg string }

func (e *stubTransportError) Error() string { return e.msg }

// Backslash in the path, custom (non-built-in) host: reproduces the exact
// finding text (raw = ".../hooks/a\b/ADVSECRETBACKSLASHPATH1").
func TestRedactWebhookErrorForURLParseSuccessBackslashPathCustomHost(t *testing.T) {
	const token = "very-secret-backslash-custom-fs01"
	raw := `https://chat.example.test/hooks/a\b/` + token
	text := realURLError(t, "Post", raw, errF3SimulatedTransportFailure)

	got := RedactWebhookSecretsForURL(text, raw)

	if strings.Contains(got, token) {
		t.Fatalf("LEAK (parse-success branch, backslash, custom host): %s", got)
	}
}

// Backslash in the path, built-in host (hooks.slack.com): the same shape is
// reachable on a Slack-native URL too, not just a custom
// slack_compatible_webhook_hosts entry.
func TestRedactWebhookErrorForURLParseSuccessBackslashPathBuiltInHost(t *testing.T) {
	const token = "very-secret-backslash-builtin-fs02"
	raw := `https://hooks.slack.com/services/T00/B00/a\b/` + token
	text := realURLError(t, "Post", raw, errF3SimulatedTransportFailure)

	got := RedactWebhookSecretsForURL(text, raw)

	if strings.Contains(got, token) {
		t.Fatalf("LEAK (parse-success branch, backslash, built-in host): %s", got)
	}
}

// Double quote in the path, custom host: %q writes \" for the same reason it
// writes \\ for a backslash -- same defect, second byte.
func TestRedactWebhookErrorForURLParseSuccessQuotePathCustomHost(t *testing.T) {
	const token = "very-secret-dquote-custom-fs03"
	raw := `https://chat.example.test/hooks/a"b/` + token
	text := realURLError(t, "Post", raw, errF3SimulatedTransportFailure)

	got := RedactWebhookSecretsForURL(text, raw)

	if strings.Contains(got, token) {
		t.Fatalf("LEAK (parse-success branch, double quote, custom host): %s", got)
	}
}

// Double quote in the path, built-in host.
func TestRedactWebhookErrorForURLParseSuccessQuotePathBuiltInHost(t *testing.T) {
	const token = "very-secret-dquote-builtin-fs04"
	raw := `https://discord.com/api/webhooks/123456789012345678/a"b/` + token
	text := realURLError(t, "Post", raw, errF3SimulatedTransportFailure)

	got := RedactWebhookSecretsForURL(text, raw)

	if strings.Contains(got, token) {
		t.Fatalf("LEAK (parse-success branch, double quote, built-in host): %s", got)
	}
}

// Same four cases through the actual production entry point,
// RedactWebhookErrorForURL (the wrapper slack/webhook.go:214/221 call), not
// just the lower-level RedactWebhookSecretsForURL.
func TestRedactWebhookErrorForURLParseSuccessQuoteEscapeViaErrorWrapper(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"backslash/custom", `https://chat.example.test/hooks/a\b/` + "very-secret-wrap-fs05"},
		{"dquote/custom", `https://chat.example.test/hooks/a"b/` + "very-secret-wrap-fs06"},
		{"backslash/builtin", `https://hooks.slack-gov.com/services/T00/B00/a\b/` + "very-secret-wrap-fs07"},
		{"dquote/builtin", `https://hooks.slack-gov.com/services/T00/B00/a"b/` + "very-secret-wrap-fs08"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := url.Parse(c.raw); err != nil {
				t.Fatalf("precondition: url.Parse(%q) should SUCCEED, got %v", c.raw, err)
			}
			uerr := &url.Error{Op: "Post", URL: c.raw, Err: errF3SimulatedTransportFailure}

			got := RedactWebhookErrorForURL(uerr, c.raw).Error()

			token := c.raw[strings.LastIndex(c.raw, "/")+1:]
			if strings.Contains(got, token) {
				t.Fatalf("LEAK: %s", got)
			}
		})
	}
}

// The delimiter-immediately-before-the-control-byte shape, for the double
// quote specifically: reuses realParseFailure (the sibling fallback test
// file's helper) because the mechanism that reaches the success branch here
// is NOT a literal backslash/quote surviving cleanly -- it is
// RedactWebhookSecretsForURL's own internal strings.TrimSpace(webhookURL)
// (see the doc comment on RedactWebhookSecretsForURL). A trailing tab makes
// url.Parse(raw) FAIL (the fallback path realParseFailure asserts), but
// url.Parse(strings.TrimSpace(raw)) then SUCCEEDS because a bare trailing
// quote alone does not fail parsing -- so the anchor computed inside the
// function lands in the success branch while `text` (built from the
// UNTRIMMED raw) still carries the %q-escaped quote. Verified experimentally
// (2026-09-04): of the four genericHTTPURLRegex delimiters (")" "'" "<"
// which are NOT escaped by %q survive the raw ReplaceAll pass unmodified
// even without this fix; only '"' is escaped by %q and needs it.
func TestRedactWebhookSecretsForURLDelimiterBeforeControlByteQuoteCrossesIntoSuccessBranch(t *testing.T) {
	const token = "very-secret-delim-quote-fs09"
	raw := "https://chat.example.test/hooks/" + token + `"` + "\t"
	text := realParseFailure(t, raw) // url.Parse(raw) fails: trailing control byte

	if _, err := url.Parse(strings.TrimSpace(raw)); err != nil {
		t.Fatalf("precondition: url.Parse(TrimSpace(raw)) should SUCCEED (this is what crosses into the success branch), got %v", err)
	}

	got := RedactWebhookSecretsForURL(text, raw)

	if strings.Contains(got, token) {
		t.Fatalf("LEAK (TrimSpace crossed into the unprotected success branch): %s", got)
	}
}

// Same shape, the three delimiters that are NOT escaped by %q: kept as a
// contrast case proving they never needed this fix (the raw ReplaceAll pass
// already matches them, since %q leaves them as literal bytes). Documents the
// reviewer's table result directly rather than leaving it implicit.
func TestRedactWebhookSecretsForURLDelimiterBeforeControlByteNonQuoteDelimitersAlreadySafe(t *testing.T) {
	delims := []string{")", "'", "<"}
	for _, ch := range delims {
		t.Run(ch, func(t *testing.T) {
			const token = "very-secret-delim-nonquote-fs10"
			raw := "https://chat.example.test/hooks/" + token + ch + "\t"
			text := realParseFailure(t, raw)

			got := RedactWebhookSecretsForURL(text, raw)

			if strings.Contains(got, token) {
				t.Fatalf("LEAK: %s", got)
			}
		})
	}
}

// RawPath re-encoded anchor: when parsed.RawPath != "", the success branch
// additionally tries rawURL.String() (the percent-escaped wire form) as an
// anchor. This must get the same strconv.Quote treatment for the same
// reason, pinned directly rather than only through the normalized-string
// cases above (which also happen to have a non-empty RawPath, but do not
// isolate this second pass).
func TestRedactWebhookErrorForURLParseSuccessRawPathAnchorQuoted(t *testing.T) {
	const token = "very-secret-rawpath-fs11"
	raw := `https://chat.example.test/hooks/a\b/` + token
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("precondition: url.Parse(%q) should SUCCEED, got %v", raw, err)
	}
	if parsed.RawPath == "" {
		t.Fatalf("precondition: expected a non-empty RawPath for %q", raw)
	}

	text := realURLError(t, "Post", raw, errF3SimulatedTransportFailure)
	got := RedactWebhookSecretsForURL(text, raw)

	if strings.Contains(got, token) {
		t.Fatalf("LEAK (RawPath anchor, parse-success branch): %s", got)
	}
}
