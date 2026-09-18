package textutil

import (
	"strings"
	"testing"
)

// This file pins the fix for a diagnostic regression found 2026-09-04: a
// typo'd port in a webhook URL (e.g. "hooks.slack.com:bad") made url.Parse
// fail, and the resulting error text lost BOTH the destination host and the
// cause, collapsing all the way to the fully generic "[redacted URL]"
// marker. See knownWebhookHostPrefix's own doc comment (textutil.go) for the
// full mechanism and why the fix is narrowly scoped to four hardcoded hosts
// rather than any host-shaped prefix.

// --- Part A: RedactWebhookSecretsForURL (anchored) ---------------------

func TestRedactWebhookSecretsForURLPreservesDiscordHostOnPortTypo(t *testing.T) {
	token := "very-secret-discord-port-typo-token-" + "kh01"
	raw := "https://discord.com:bad/api/webhooks/T0/" + token
	text := realParseFailure(t, raw)

	got := RedactWebhookSecretsForURL(text, raw)

	if strings.Contains(got, token) {
		t.Fatalf("token leaks: %s", got)
	}
	want := `parse "https://discord.com/[redacted]": invalid port "[redacted]" after host`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRedactWebhookSecretsForURLPreservesDiscordAppHostOnPortTypo(t *testing.T) {
	token := "very-secret-discordapp-port-typo-token-" + "kh02"
	raw := "https://discordapp.com:bad/api/webhooks/T0/" + token
	text := realParseFailure(t, raw)

	got := RedactWebhookSecretsForURL(text, raw)

	if strings.Contains(got, token) {
		t.Fatalf("token leaks: %s", got)
	}
	want := `parse "https://discordapp.com/[redacted]": invalid port "[redacted]" after host`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRedactWebhookSecretsForURLPreservesSlackHostOnPortTypo(t *testing.T) {
	// The task's own reported repro, anchored form.
	token := "very-secret-slack-port-typo-token-" + "kh03"
	raw := "https://hooks.slack.com:bad/services/T0/B0/" + token
	text := realParseFailure(t, raw)

	got := RedactWebhookSecretsForURL(text, raw)

	if strings.Contains(got, token) {
		t.Fatalf("token leaks: %s", got)
	}
	want := `parse "https://hooks.slack.com/[redacted]": invalid port "[redacted]" after host`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRedactWebhookSecretsForURLPreservesSlackGovHostOnPortTypo(t *testing.T) {
	token := "very-secret-slackgov-port-typo-token-" + "kh04"
	raw := "https://hooks.slack-gov.com:bad/services/T0/B0/" + token
	text := realParseFailure(t, raw)

	got := RedactWebhookSecretsForURL(text, raw)

	if strings.Contains(got, token) {
		t.Fatalf("token leaks: %s", got)
	}
	want := `parse "https://hooks.slack-gov.com/[redacted]": invalid port "[redacted]" after host`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if strings.Contains(got, "https://hooks.slack.com/") {
		t.Fatalf("mislabelled as hooks.slack.com: %s", got)
	}
}

func TestRedactWebhookSecretsForURLLeavesCustomHostGenericOnPortTypo(t *testing.T) {
	token := "very-secret-custom-host-port-typo-token-" + "kh05"
	raw := "https://custom.example.test:bad/services/T0/B0/" + token
	text := realParseFailure(t, raw)

	got := RedactWebhookSecretsForURL(text, raw)

	if strings.Contains(got, token) {
		t.Fatalf("token leaks: %s", got)
	}
	want := `parse "[redacted URL]": invalid port "[redacted]" after host`
	if got != want {
		t.Fatalf("got %q, want %q (unchanged from today: only the four hardcoded hosts get named)", got, want)
	}
}

// TestRedactWebhookSecretsForURLDoesNotLeakCredentialMisparsedAsHostPort is
// the §2 counter-example, reconstructed as a real url.Parse failure: an
// authority with no '@' at all, where url.Parse treats the text before the
// colon as "host" and everything after it as an invalid "port" -- which is
// exactly what happens to a credential of the shape "TOKEN:SECRET" with no
// delimiter to stop the scan. Guards against reintroducing the naive
// "protect the marker from the second pass unconditionally" fix that was
// tried and rejected: that version leaks SECRETVALUE here because nothing
// about the raw scan distinguishes it from a genuine port typo.
func TestRedactWebhookSecretsForURLDoesNotLeakCredentialMisparsedAsHostPort(t *testing.T) {
	const fakeHost = "faketoken"
	secret := "very-" + "secret-value-that-looks-like-a-port-kh06"
	raw := "https://" + fakeHost + ":" + secret + "/path"
	text := realParseFailure(t, raw)

	got := RedactWebhookSecretsForURL(text, raw)

	if strings.Contains(got, secret) {
		t.Fatalf("secret leaks: %s", got)
	}
	if strings.Contains(got, fakeHost) {
		t.Fatalf("fake host echoed as if it were a real known host: %s", got)
	}
	want := `parse "[redacted URL]": invalid port "[redacted]" after host`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRedactWebhookSecretsForURLRejectsSubdomainSpoofOfKnownHostOnPortTypo(t *testing.T) {
	token := "very-secret-subdomain-spoof-token-" + "kh07"
	raw := "https://hooks.slack.com.evil.test:bad/services/T0/B0/" + token
	text := realParseFailure(t, raw)

	got := RedactWebhookSecretsForURL(text, raw)

	if strings.Contains(got, token) {
		t.Fatalf("token leaks: %s", got)
	}
	want := `parse "[redacted URL]": invalid port "[redacted]" after host`
	if got != want {
		t.Fatalf("got %q, want %q (a subdomain-suffix spoof must not pass the exact-match gate)", got, want)
	}
}

// TestRedactWebhookSecretsForURLRejectsPrefixedLookalikeHostOnPortTypo kills
// the mutation that swaps knownWebhookHostPrefix's exact-match EqualFold for
// a strings.HasSuffix check: "evil-discord.com" is a domain an attacker can
// actually register, unlike the "...evil.test" suffix-spoof shape above,
// which only kills prefix/containment mutants.
func TestRedactWebhookSecretsForURLRejectsPrefixedLookalikeHostOnPortTypo(t *testing.T) {
	token := "very-secret-lookalike-host-token-" + "kh08"
	raw := "https://evil-discord.com:bad/api/webhooks/T0/" + token
	text := realParseFailure(t, raw)

	got := RedactWebhookSecretsForURL(text, raw)

	if strings.Contains(got, token) {
		t.Fatalf("token leaks: %s", got)
	}
	want := `parse "[redacted URL]": invalid port "[redacted]" after host`
	if got != want {
		t.Fatalf("got %q, want %q (a HasSuffix-style match must not treat evil-discord.com as discord.com)", got, want)
	}
}

// TestRedactWebhookSecretsForURLKnownHostPortTypoSplitsOnTheFirstColon pins
// that the host/suffix split happens at the FIRST colon, not the last. A raw
// authority can carry more than one colon (a doubled port typo, a pasted
// "host:port:something"), and only a first-colon split leaves the known host
// alone on the left where the exact match can find it; splitting on the last
// colon instead yields the prefix "hooks.slack.com:8080", which matches no
// literal, and the marker silently degrades to the fully generic one. Measured:
// without this test that swap survives every other assertion in this file.
func TestRedactWebhookSecretsForURLKnownHostPortTypoSplitsOnTheFirstColon(t *testing.T) {
	token := "very-secret-double-colon-port-token-" + "kh23"
	raw := "https://hooks.slack.com:8080:9090/services/T0/B0/" + token
	text := realParseFailure(t, raw)

	got := RedactWebhookSecretsForURL(text, raw)

	if strings.Contains(got, token) {
		t.Fatalf("token leaks: %s", got)
	}
	if strings.Contains(got, "8080") || strings.Contains(got, "9090") {
		t.Fatalf("port suffix echoed instead of dropped: %s", got)
	}
	want := `parse "https://hooks.slack.com/[redacted]": invalid port "[redacted]" after host`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRedactWebhookSecretsForURLKnownHostPortTypoCaseInsensitive(t *testing.T) {
	token := "very-secret-uppercase-host-token-" + "kh09"
	raw := "https://HOOKS.SLACK.COM:BAD/services/T0/B0/" + token
	text := realParseFailure(t, raw)

	got := RedactWebhookSecretsForURL(text, raw)

	if strings.Contains(got, token) {
		t.Fatalf("token leaks: %s", got)
	}
	if !strings.Contains(got, "https://hooks.slack.com/[redacted]") {
		t.Fatalf("expected canonical lowercase host in marker, got %q", got)
	}
}

// TestRedactWebhookSecretsForURLKnownHostPortTypoStillGuardedByIsLogSafeHost
// uses a backslash in the port position, not a control byte: rawWebhookSchemeHost's
// authority scan stops at any byte <= ' ', so a control byte never reaches
// host in the first place and isLogSafeHost never even gets a chance to
// reject it. A backslash (or a double quote) is one of only two bytes that
// can actually reach and fail isLogSafeHost from the port position.
func TestRedactWebhookSecretsForURLKnownHostPortTypoStillGuardedByIsLogSafeHost(t *testing.T) {
	token := "very-secret-backslash-port-token-" + "kh10"
	raw := `https://hooks.slack.com:ba\d/services/T0/B0/` + token
	text := realParseFailure(t, raw)

	got := RedactWebhookSecretsForURL(text, raw)

	if strings.Contains(got, token) {
		t.Fatalf("token leaks: %s", got)
	}
	if !strings.Contains(got, "[redacted URL]") {
		t.Fatalf("expected the generic placeholder when host is not log-safe, got %q", got)
	}
	if strings.Contains(got, "hooks.slack.com") {
		t.Fatalf("known-host marker must not be built from a host isLogSafeHost rejects: %s", got)
	}
}

func TestRedactWebhookSecretsForURLKnownHostPortTypoLeavesUnrelatedSecondURLIntact(t *testing.T) {
	primaryToken := "very-secret-primary-slack-token-" + "kh11"
	secondToken := "very-secret-second-discord-token-" + "kh12"
	primary := "https://hooks.slack.com:bad/services/T0/B0/" + primaryToken
	second := "https://discord.com/api/webhooks/456/" + secondToken
	text := realParseFailure(t, primary) + " and also see " + second

	got := RedactWebhookSecretsForURL(text, primary)

	if strings.Contains(got, primaryToken) {
		t.Fatalf("primary token leaks: %s", got)
	}
	if strings.Contains(got, secondToken) {
		t.Fatalf("second, unrelated token leaks: %s", got)
	}
	if !strings.Contains(got, "https://hooks.slack.com/[redacted]") {
		t.Fatalf("primary marker missing host label: %s", got)
	}
	if !strings.Contains(got, "https://discord.com/api/webhooks/[redacted]") {
		t.Fatalf("second, unrelated URL was not correctly redacted on its own: %s", got)
	}
}

func TestRedactWebhookSecretsForURLKnownHostPortTypoRepeatedOccurrence(t *testing.T) {
	token := "very-secret-repeated-occurrence-token-" + "kh13"
	raw := "https://hooks.slack.com:bad/services/T0/B0/" + token
	failure := realParseFailure(t, raw)
	text := failure + " (retrying) " + failure

	got := RedactWebhookSecretsForURL(text, raw)

	if strings.Contains(got, token) {
		t.Fatalf("token leaks: %s", got)
	}
	if strings.Count(got, "https://hooks.slack.com/[redacted]") != 2 {
		t.Fatalf("expected the host-labelled marker at both occurrences, got %q", got)
	}
}

// --- Part B: RedactWebhookSecrets (unanchored regex widening) ----------

func TestRedactWebhookSecretsDiscordHostPortTypoKeepsMarker(t *testing.T) {
	token := "very-secret-unanchored-discord-token-" + "kh14"
	text := "post to https://discord.com:bad/api/webhooks/T0/" + token + " failed"

	got := RedactWebhookSecrets(text)

	if strings.Contains(got, token) {
		t.Fatalf("token leaks: %s", got)
	}
	want := "post to https://discord.com/api/webhooks/[redacted] failed"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRedactWebhookSecretsDiscordAppHostPortTypoKeepsMarker(t *testing.T) {
	token := "very-secret-unanchored-discordapp-token-" + "kh15"
	text := "post to https://discordapp.com:bad/api/webhooks/T0/" + token + " failed"

	got := RedactWebhookSecrets(text)

	if strings.Contains(got, token) {
		t.Fatalf("token leaks: %s", got)
	}
	want := "post to https://discordapp.com/api/webhooks/[redacted] failed"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRedactWebhookSecretsSlackHostPortTypoKeepsMarker(t *testing.T) {
	// The task's own reported repro, unanchored form.
	token := "very-secret-unanchored-slack-token-" + "kh16"
	text := "post to https://hooks.slack.com:bad/services/T0/B0/" + token + " failed"

	got := RedactWebhookSecrets(text)

	if strings.Contains(got, token) {
		t.Fatalf("token leaks: %s", got)
	}
	want := "post to https://hooks.slack.com/services/[redacted] failed"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRedactWebhookSecretsSlackGovHostPortTypoKeepsMarker(t *testing.T) {
	token := "very-secret-unanchored-slackgov-token-" + "kh17"
	text := "post to https://hooks.slack-gov.com:bad/services/T0/B0/" + token + " failed"

	got := RedactWebhookSecrets(text)

	if strings.Contains(got, token) {
		t.Fatalf("token leaks: %s", got)
	}
	want := "post to https://hooks.slack-gov.com/services/[redacted] failed"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// TestRedactWebhookSecretsRegexPortGroupDoesNotCrossRealSlashIntoDecoyPath
// guards the excluded-'/' char class: a '/' inside the fake port position
// must not let the match window walk past it into a decoy "/services/..."
// segment.
func TestRedactWebhookSecretsRegexPortGroupDoesNotCrossRealSlashIntoDecoyPath(t *testing.T) {
	token := "very-secret-decoy-path-token-" + "kh18"
	text := "error at https://hooks.slack.com:foo/bar/services/" + token + " path"

	got := RedactWebhookSecrets(text)

	if strings.Contains(got, token) {
		t.Fatalf("token leaks: %s", got)
	}
	if strings.Contains(got, "https://hooks.slack.com/services/[redacted]") {
		t.Fatalf("regex matched across a real slash into a decoy path segment: %s", got)
	}
	if !strings.Contains(got, "[redacted URL]") {
		t.Fatalf("expected the generic marker for a shape the widened regex must not match: %s", got)
	}
}

func TestRedactWebhookSecretsRegexPortGroupCaseInsensitiveOnKnownHost(t *testing.T) {
	token := "very-secret-uppercase-unanchored-token-" + "kh19"
	text := "error at https://HOOKS.SLACK.COM:BAD/services/" + token + " path"

	got := RedactWebhookSecrets(text)

	if strings.Contains(got, token) {
		t.Fatalf("token leaks: %s", got)
	}
	if !strings.Contains(got, "https://hooks.slack.com/services/[redacted]") {
		t.Fatalf("expected canonical lowercase host in marker, got %q", got)
	}
}

// TestRedactWebhookSecretsRegexPortGroupDoesNotCrossUserinfoIntoAnotherHost
// pins the review's correction to the port character class: '@' must stay
// excluded, or a userinfo-shaped authority after the colon gets mislabelled
// as the known host that precedes it, when the real authority is what
// follows the '@'. Not a token-leak assertion: this shape falls through to
// RedactURLCredentials, whose generic path-token handling is unrelated to
// this plan and unchanged by it (the token here is not redacted before or
// after this change, matching the plan's own measured baseline, §12.3) --
// what matters is that the fixed literal never claims hooks.slack.com is the
// destination when the real authority is other.example.test.
func TestRedactWebhookSecretsRegexPortGroupDoesNotCrossUserinfoIntoAnotherHost(t *testing.T) {
	token := "very-secret-userinfo-spoof-token-" + "kh20"
	text := "error at https://hooks.slack.com:u@other.example.test/services/" + token + " path"

	got := RedactWebhookSecrets(text)

	if strings.Contains(got, "https://hooks.slack.com/services/[redacted]") {
		t.Fatalf("the real authority is other.example.test; must not be mislabelled hooks.slack.com: %s", got)
	}
	if !strings.Contains(got, "other.example.test") {
		t.Fatalf("the real authority other.example.test must survive when the fixed literal is not asserted: %s", got)
	}
}

// TestRedactWebhookSecretsWidenedRegexCoversValidExplicitPort pins that Part
// B is not diagnostics-only: these two shapes parse successfully today, so
// RedactURLCredentials returns them untouched and the token reaches the log
// verbatim before this fix.
func TestRedactWebhookSecretsWidenedRegexCoversValidExplicitPort(t *testing.T) {
	discordToken := "very-secret-explicit-port-discord-token-" + "kh21"
	slackToken := "very-secret-empty-port-slack-token-" + "kh22"
	discordText := "post to https://discord.com:8443/api/webhooks/123/" + discordToken + " failed"
	slackText := "post to https://hooks.slack.com:/services/T0/B0/" + slackToken + " failed"

	gotDiscord := RedactWebhookSecrets(discordText)
	gotSlack := RedactWebhookSecrets(slackText)

	if strings.Contains(gotDiscord, discordToken) {
		t.Fatalf("discord token leaks on a valid explicit port: %s", gotDiscord)
	}
	if strings.Contains(gotSlack, slackToken) {
		t.Fatalf("slack token leaks on an empty explicit port: %s", gotSlack)
	}
}
