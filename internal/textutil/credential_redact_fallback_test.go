package textutil

import (
	"strings"
	"testing"
)

// This file pins RedactURLCredentials's fallback for a genericHTTPURLRegex
// match that url.Parse could not make sense of (redactUnparsableURLMatch).
// Every case below returns the same unconditional "[redacted URL]" marker --
// see that function's own doc comment for why it never attempts to echo a
// scheme or host on this branch, unlike RedactWebhookSecretsForURL's
// analogous fallback.

func TestRedactURLCredentialsMasksSecretInUserinfoOnParseFailure(t *testing.T) {
	const secret = "SECRETPASS"
	text := "see https://user:" + secret + "\x01@host.example.test/rss for details"

	got := RedactURLCredentials(text)

	if strings.Contains(got, secret) {
		t.Fatalf("RedactURLCredentials() leaked userinfo secret: %s", got)
	}
	want := "see [redacted URL] for details"
	if got != want {
		t.Fatalf("RedactURLCredentials(%q) = %q, want %q", text, got, want)
	}
}

func TestRedactURLCredentialsMasksSecretInQueryOnParseFailure(t *testing.T) {
	const secret = "SECRETQUERY"
	text := "see https://host.example.test:abc/rss?token=" + secret + " for details"

	got := RedactURLCredentials(text)

	if strings.Contains(got, secret) {
		t.Fatalf("RedactURLCredentials() leaked query secret: %s", got)
	}
	want := "see [redacted URL] for details"
	if got != want {
		t.Fatalf("RedactURLCredentials(%q) = %q, want %q", text, got, want)
	}
}

func TestRedactURLCredentialsMasksSecretInPathOnParseFailure(t *testing.T) {
	const secret = "SECRETPATH"
	text := "see https://host.example.test/feed%2z" + secret + " for details"

	got := RedactURLCredentials(text)

	if strings.Contains(got, secret) {
		t.Fatalf("RedactURLCredentials() leaked path secret: %s", got)
	}
	want := "see [redacted URL] for details"
	if got != want {
		t.Fatalf("RedactURLCredentials(%q) = %q, want %q", text, got, want)
	}
}

func TestRedactURLCredentialsMasksSecretOnNonBuiltInHostOnParseFailure(t *testing.T) {
	const secret = "SECRETTOKEN"
	text := "see https://chat.example.test/hooks/" + secret + "\x01/x for details"

	got := RedactURLCredentials(text)

	if strings.Contains(got, secret) {
		t.Fatalf("RedactURLCredentials() leaked token on a non-built-in host: %s", got)
	}
	want := "see [redacted URL] for details"
	if got != want {
		t.Fatalf("RedactURLCredentials(%q) = %q, want %q", text, got, want)
	}
}

func TestRedactURLCredentialsMalformedIPv6HostOnParseFailure(t *testing.T) {
	text := "see https://[::1zz/rss for details"

	got := RedactURLCredentials(text)

	want := "see [redacted URL] for details"
	if got != want {
		t.Fatalf("RedactURLCredentials(%q) = %q, want %q", text, got, want)
	}
}

func TestRedactURLCredentialsFallsBackToGenericMarkerWhenHostEmpty(t *testing.T) {
	const secret = "SECRETQ"
	text := "see https://?token=" + secret + " for details"

	got := RedactURLCredentials(text)

	if strings.Contains(got, secret) {
		t.Fatalf("RedactURLCredentials() leaked query secret on an empty host: %s", got)
	}
	want := "see [redacted URL] for details"
	if got != want {
		t.Fatalf("RedactURLCredentials(%q) = %q, want %q", text, got, want)
	}
}

func TestRedactURLCredentialsFallsBackToGenericMarkerWhenRawHostUnsafe(t *testing.T) {
	const secret = "SECRETPATH"
	text := `see https://evil\host.example/` + secret + " for details"

	got := RedactURLCredentials(text)

	if strings.Contains(got, secret) {
		t.Fatalf("RedactURLCredentials() leaked path secret behind an unsafe host: %s", got)
	}
	want := "see [redacted URL] for details"
	if got != want {
		t.Fatalf("RedactURLCredentials(%q) = %q, want %q", text, got, want)
	}
}

// The three shapes below are REVIEW-FIX 2's adversarial table (A, B, C):
// concrete counter-examples to the plan's originally proposed fallback
// design (echo scheme+host straight from rawWebhookSchemeHost, unconditionally
// trusting whatever the truncated regex match left behind), each one a
// distinct mechanism by which that design would have leaked the secret AS the
// echoed host or discarded the rest of the URL around it. The unconditional
// generic marker implemented here closes all three by construction: it never
// looks at what the match contains.

// REVIEW-FIX 2, row A: genericHTTPURLRegex stops at the "'" (single quote),
// so the match never includes the closing "]" of the IPv6 literal; url.Parse
// on the truncated match fails with "missing ']' in host". The rejected
// design would have echoed everything between "://" and the cut -- including
// the secret -- as the "host".
func TestRedactURLCredentialsDoesNotLeakSecretWhenRegexTruncatesBeforeClosingIPv6Bracket(t *testing.T) {
	const secret = "ZONESECRET"
	text := "see https://[2606:4700::1%25en" + secret + "'x]/rss for details"

	got := RedactURLCredentials(text)

	if strings.Contains(got, secret) {
		t.Fatalf("RedactURLCredentials() leaked secret via truncated-match host echo: %s", got)
	}
}

// REVIEW-FIX 2, row B: genericHTTPURLRegex stops at the double quote, so the
// match is "https://svc:<secret>" -- a bare host with an invalid ":<secret>"
// port. The rejected design would have echoed the secret itself as the
// marker's host.
func TestRedactURLCredentialsDoesNotLeakSecretWhenRegexTruncatesAtQuoteInUserinfo(t *testing.T) {
	const secret = "QUOTESECRET"
	text := `see https://svc:` + secret + `"z@feeds.example.test:abc/rss for details`

	got := RedactURLCredentials(text)

	if strings.Contains(got, secret) {
		t.Fatalf("RedactURLCredentials() leaked secret echoed as the host: %s", got)
	}
}

// REVIEW-FIX 2, row C: an unrelated "@" inside a query VALUE (an email
// address) makes the match's tail look like a smuggled userinfo delimiter to
// rawWebhookSchemeHost's last-"@"-wins scan. The rejected design would have
// echoed the query tail as the "host" and discarded everything else in the
// URL -- the log line would have carried the secret and nothing else, an
// outcome the reviewer judged worse than an obviously raw line.
func TestRedactURLCredentialsDoesNotLeakSecretWhenAtSignInQueryValueLooksLikeUserinfo(t *testing.T) {
	const secret = "REALSECRET123"
	text := `Get "https://feeds.example.test:abc/rss?user=admin@corp.example&apikey=` + secret + `": invalid port`

	got := RedactURLCredentials(text)

	if strings.Contains(got, secret) {
		t.Fatalf("RedactURLCredentials() leaked secret via query-value '@' host confusion: %s", got)
	}
	want := `Get "[redacted URL]": invalid port`
	if got != want {
		t.Fatalf("RedactURLCredentials(%q) = %q, want %q", text, got, want)
	}
}

// REVIEW-FIX 5: genericHTTPURLRegex was case-sensitive (`https?://`), so an
// uppercase-scheme URL such as "HTTPS://..." never matched at all and this
// function was a complete no-op on it. feedurl.Validate accepts an
// uppercase-scheme feed URL (it only checks parsed.Scheme, already lowercased
// by url.Parse itself), so this was reachable from a live config value.
func TestRedactURLCredentialsMatchesUppercaseScheme(t *testing.T) {
	const secret = "UPSECRET"
	text := "fetch HTTPS://user:" + secret + "@feeds.example.test/rss for details"

	got := RedactURLCredentials(text)

	if strings.Contains(got, secret) {
		t.Fatalf("uppercase-scheme URL leaked its userinfo credential (case-sensitivity regression): %s", got)
	}
	if !strings.Contains(got, "redacted") {
		t.Fatalf("uppercase-scheme URL was not redacted at all (case-sensitivity regression): %s", got)
	}
	want := "fetch https://%5Bredacted%5D@feeds.example.test/rss for details"
	if got != want {
		t.Fatalf("RedactURLCredentials(%q) = %q, want %q", text, got, want)
	}
}

// The webhook regexes share the same case-sensitivity defect as
// genericHTTPURLRegex (REVIEW-FIX 5 names all three) and got the identical
// (?i) fix: a hardcoded host allowlist that matches "discord.com" but misses
// "DISCORD.COM" is the same bug in a different function.
func TestRedactWebhookSecretsMatchesUppercaseDiscordHost(t *testing.T) {
	const token = "SECRETTOKEN-uppercase-discord"
	text := "post to https://DISCORD.COM/api/webhooks/123456789012345678/" + token + " failed"

	got := RedactWebhookSecrets(text)

	if strings.Contains(got, token) {
		t.Fatalf("uppercase discord.com host token leaked (case-sensitivity regression): %s", got)
	}
	want := "post to https://discord.com/api/webhooks/[redacted] failed"
	if got != want {
		t.Fatalf("RedactWebhookSecrets() = %q, want %q", got, want)
	}
}

// (?i) is only free because both webhook callbacks lower-case the match
// before deciding WHICH built-in host they matched: without that, widening
// the pattern makes an uppercase "DISCORDAPP.COM" match and then be labelled
// "discord.com" in the marker -- a log line stating the wrong host, reachable
// only since the widening. The Slack twin is pinned by the test below; this
// pins the Discord half, which nothing else does.
func TestRedactWebhookSecretsMatchesUppercaseDiscordAppHost(t *testing.T) {
	const token = "SECRETTOKEN-uppercase-discordapp"
	text := "post to https://DISCORDAPP.COM/api/v10/webhooks/123456789012345678/" + token + " failed"

	got := RedactWebhookSecrets(text)

	if strings.Contains(got, token) {
		t.Fatalf("uppercase discordapp.com host token leaked (case-sensitivity regression): %s", got)
	}
	want := "post to https://discordapp.com/api/webhooks/[redacted] failed"
	if got != want {
		t.Fatalf("RedactWebhookSecrets() = %q, want %q", got, want)
	}
}

func TestRedactWebhookSecretsMatchesUppercaseSlackGovHost(t *testing.T) {
	const token = "SECRETTOKEN-uppercase-slackgov"
	text := "post to https://HOOKS.SLACK-GOV.COM/services/T1/B1/" + token + " failed"

	got := RedactWebhookSecrets(text)

	if strings.Contains(got, token) {
		t.Fatalf("uppercase hooks.slack-gov.com host token leaked (case-sensitivity regression): %s", got)
	}
	want := "post to https://hooks.slack-gov.com/services/[redacted] failed"
	if got != want {
		t.Fatalf("RedactWebhookSecrets() = %q, want %q", got, want)
	}
}

// TestRedactWebhookSecretsKeepsWebhookMarkerOnAnUnparsableWebhookURL pins
// RedactWebhookSecrets's INTERNAL tier order, which the unconditional generic
// fallback above made load-bearing where it was not before. The two hardcoded
// webhook regexes must run BEFORE the generic RedactURLCredentials pass: on a
// webhook URL that url.Parse rejects, running the generic pass first collapses
// the whole URL to "[redacted URL]" (redactUnparsableURLMatch never inspects
// its match), and the webhook regexes then find nothing left to recognise, so
// the platform-specific marker -- the only thing telling an operator which
// destination failed -- is gone. Both orders are equally safe; only this one
// keeps the diagnostic. internal/status/tracker.go's auditDetails deliberately
// composes them the other way round and therefore gets the generic marker on
// this shape; that call site is the reason this property is worth pinning
// here rather than left implicit.
func TestRedactWebhookSecretsKeepsWebhookMarkerOnAnUnparsableWebhookURL(t *testing.T) {
	const token = "SECRETTOKEN-unparsable-webhook"
	// The control byte is what makes url.Parse fail; it is inside the token,
	// so genericHTTPURLRegex still matches the whole URL.
	text := "post failed: https://discord.com/api/webhooks/123456789012345678/" + token + "\x01x oops"

	got := RedactWebhookSecrets(text)

	if strings.Contains(got, token) {
		t.Fatalf("RedactWebhookSecrets() leaked the token: %s", got)
	}
	want := "post failed: https://discord.com/api/webhooks/[redacted] oops"
	if got != want {
		t.Fatalf("RedactWebhookSecrets() = %q, want %q -- the webhook tier must run before the generic one", got, want)
	}
}
