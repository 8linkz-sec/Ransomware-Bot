package textutil

import (
	"errors"
	"net/url"
	"strings"
	"testing"
)

// F1, P2 (found 2026-09-04, fifth round on this defect class): the shared
// helper's fallback still leaked a userinfo password containing '/', '?' or
// '#' on BOTH the feed and the webhook path. Reproduced from a real bot.log:
// general_feeds[0] = "https://svc:B64TOKEN/SLASHSECRET777@feeds.example.com/rss"
// produced `parse "https://svc:B64TOKEN/[redacted]": invalid port
// ":B64TOKEN" after host` -- two independent leak channels, both pinned
// below. This test reproduces the exact finding text end to end through
// RedactWebhookErrorForURL, the wrapper used by both validateFeedURL
// (internal/config) and the webhook validators.
func TestRedactWebhookErrorForURLFixesReportedFeedURLLeak(t *testing.T) {
	const password = "B64TOKEN"
	const secretTail = "SLASHSECRET777"
	raw := "https://svc:" + password + "/" + secretTail + "@feeds.example.com/rss"
	text := realParseFailure(t, raw)

	got := RedactWebhookErrorForURL(&urlErrorLike{text}, raw).Error()

	if strings.Contains(got, password) {
		t.Fatalf("LEAK: userinfo password %q survives: %s", password, got)
	}
	if strings.Contains(got, secretTail) {
		t.Fatalf("LEAK: userinfo password tail %q survives: %s", secretTail, got)
	}
	if !strings.Contains(got, "https://feeds.example.com/[redacted]") {
		t.Fatalf("expected the correctly-hosted marker (real host past the '@'), got: %s", got)
	}
	if !strings.Contains(got, `invalid port "[redacted]" after host`) {
		t.Fatalf("expected the inner net/url detail redacted too, got: %s", got)
	}
}

// urlErrorLike lets the test above build an error whose .Error() is exactly
// the pre-computed text (the reported finding's own wording), while still
// exercising RedactWebhookErrorForURL's real entry point.
type urlErrorLike struct{ msg string }

func (e *urlErrorLike) Error() string { return e.msg }

// F1 -- pins the rawWebhookSchemeHost rewrite directly: a userinfo password
// containing an unescaped '/', '?' or '#' (separately, and all three
// together) used to make the naive first-stop-byte authority scan cut off
// before ever reaching the real '@', so the credential prefix became the
// "host" echoed into the marker. Fixed by searching the whole remainder for
// the LAST '@' before bounding the host. Every case here is a genuine
// url.Parse failure (a real *url.Error), never a hand-written literal.
func TestRedactWebhookSecretsForURLMarkerUsesRealHostDespiteUserinfoSlashQuestionHash(t *testing.T) {
	cases := []struct {
		name string
		pass string // goes right after "svc:", before "@host..."
	}{
		{"slash", "TOK1/REST1"},
		{"question mark", "TOK2?REST2"},
		{"hash", "TOK3#REST3"},
		{"all three", "TOK4/A?B#REST4"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			raw := "https://svc:" + c.pass + "@host.example.test/rss"
			text := realParseFailure(t, raw)

			got := RedactWebhookSecretsForURL(text, raw)

			if strings.Contains(got, c.pass) {
				t.Fatalf("LEAK: password %q survives: %s", c.pass, got)
			}
			if !strings.Contains(got, "https://host.example.test/[redacted]") {
				t.Fatalf("expected marker anchored on the real host past the userinfo, got: %s", got)
			}
		})
	}
}

// F1 -- pins redactURLErrorSubstringFragments: net/url's own
// `invalid port %q after host` (net/url/url.go parseHost) embeds a raw slice
// of the secret in the error's INNER detail, not its URL field, whenever the
// authority-truncation bug above lands a slash-containing userinfo password
// where a port was expected. Neither the raw nor the %q-escaped whole-string
// ReplaceAll passes can reach a fragment shorter than the whole secret; this
// pins that the new fragment-level pass does.
func TestRedactWebhookSecretsForURLRedactsInnerNetURLPortDetail(t *testing.T) {
	const password = "PORTLEAK777"
	raw := "https://svc:" + password + "/tail@host.example.test/rss"
	text := realParseFailure(t, raw)
	if !strings.Contains(text, `"`) || !strings.Contains(text, "invalid port") {
		t.Fatalf("precondition: expected a net/url 'invalid port' detail, got %q", text)
	}

	got := RedactWebhookSecretsForURL(text, raw)

	if strings.Contains(got, password) {
		t.Fatalf("LEAK in inner net/url detail: %s", got)
	}
}

// F1 -- pins the %q-escape mismatch inside redactURLErrorSubstringFragments
// itself: net/url's `invalid port %q after host` escapes a literal backslash
// in the port value to two bytes, so a raw (unescaped) containment check
// against secret misses it; strconv.Unquote must be tried first. Also
// combines with a literal '#', which additionally exercises tier 2c (the
// url.Parse pre-fragment-cut channel) in the same case.
func TestRedactWebhookSecretsForURLRedactsBackslashInInnerPortDetail(t *testing.T) {
	const password = `BACKSLASH777`
	raw := `https://svc:` + password + `\tail#frag@host.example.test/rss`
	text := realParseFailure(t, raw)

	got := RedactWebhookSecretsForURL(text, raw)

	if strings.Contains(got, password) {
		t.Fatalf("LEAK: backslash-adjacent password survives: %s", got)
	}
}

// F1 -- pins tier 2c directly: url.Parse cuts rawURL at the FIRST '#' before
// doing anything else (net/url/url.go's exported Parse), so when that '#'
// sits inside an unescaped userinfo password and the pre-'#' prefix itself
// fails to parse, (*url.Error).URL is that TRUNCATED prefix, not the full
// webhookURL/feedURL -- a proper substring, which the whole-string raw/%q
// ReplaceAll passes (anchored on the full, untruncated secret) can never
// match. Without tier 2c the URL field itself (still credential-bearing on
// its own, e.g. "https://svc:HASHLEAK777") survives untouched.
func TestRedactWebhookSecretsForURLRedactsPreFragmentTruncatedURLField(t *testing.T) {
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

// F1 coverage -- redactURLErrorSubstringFragments's own defensive guards,
// tested directly rather than only indirectly through
// RedactWebhookSecretsForURL: its only production call site always passes
// the *url.Error url.Parse itself just returned (which the stdlib contract
// guarantees is either nil or a *url.Error with a non-nil Err whose Error()
// text is, in practice, never empty), so the "wrong type" / "nil inner Err" /
// "empty detail" branches, the empty-quoted-fragment continue, and the
// not-a-substring-of-secret continue are all defensive rather than reachable
// from that one call site. "empty detail" (added with the 2026-09-04
// performance fix's work-budget guard) is checked ahead of that guard for the
// same reason: an empty detail can never contain a leaking fragment, so
// there is nothing for either the exact search or its budget-exceeded
// wholesale-redact fallback to do.
func TestRedactURLErrorSubstringFragmentsGuardBranches(t *testing.T) {
	const text = `parse "https://host.example.test": invalid port "not-a-secret" after host`

	t.Run("nil err", func(t *testing.T) {
		if got := redactURLErrorSubstringFragments(text, nil, "secret"); got != text {
			t.Fatalf("got %q, want text unchanged", got)
		}
	})
	t.Run("empty secret", func(t *testing.T) {
		err := &url.Error{Op: "parse", URL: "x", Err: errors.New(`invalid port "not-a-secret" after host`)}
		if got := redactURLErrorSubstringFragments(text, err, ""); got != text {
			t.Fatalf("got %q, want text unchanged", got)
		}
	})
	t.Run("err not a *url.Error", func(t *testing.T) {
		if got := redactURLErrorSubstringFragments(text, errors.New("plain error"), "secret"); got != text {
			t.Fatalf("got %q, want text unchanged", got)
		}
	})
	t.Run("url.Error with nil inner Err", func(t *testing.T) {
		err := &url.Error{Op: "parse", URL: "x", Err: nil}
		if got := redactURLErrorSubstringFragments(text, err, "secret"); got != text {
			t.Fatalf("got %q, want text unchanged", got)
		}
	})
	t.Run("url.Error with empty inner Err text", func(t *testing.T) {
		err := &url.Error{Op: "parse", URL: "x", Err: errors.New("")}
		if got := redactURLErrorSubstringFragments(text, err, "secret"); got != text {
			t.Fatalf("got %q, want text unchanged", got)
		}
	})
	t.Run("empty quoted fragment in detail", func(t *testing.T) {
		err := &url.Error{Op: "parse", URL: "x", Err: errors.New(`invalid port "" after host`)}
		got := redactURLErrorSubstringFragments(`parse "x": invalid port "" after host`, err, "secret")
		want := `parse "x": invalid port "" after host`
		if got != want {
			t.Fatalf("got %q, want %q (empty fragment left alone)", got, want)
		}
	})
	t.Run("quoted fragment not a substring of secret", func(t *testing.T) {
		err := &url.Error{Op: "parse", URL: "x", Err: errors.New(`invalid port "unrelated" after host`)}
		got := redactURLErrorSubstringFragments(text, err, "secret-does-not-contain-that")
		if got != text {
			t.Fatalf("got %q, want text unchanged (fragment not a secret substring)", got)
		}
	})
	t.Run("quoted fragment is a substring of secret", func(t *testing.T) {
		err := &url.Error{Op: "parse", URL: "x", Err: errors.New(`invalid port ":ABC123" after host`)}
		in := `parse "x": invalid port ":ABC123" after host`
		got := redactURLErrorSubstringFragments(in, err, "https://svc:ABC123@host.example.test/rss")
		want := `parse "x": invalid port "[redacted]" after host`
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
}

// F1 coverage -- rawWebhookSchemeHost's new last-'@' scan must not regress
// the ordinary case (no userinfo ambiguity at all, ordinary '@' in query):
// this pins that an unrelated '@' appearing only in the query of a
// completely different, unrelated URL elsewhere in the same message does not
// get treated as a userinfo delimiter for THAT url's own host extraction
// (tier 1's own scheme+host matching uses the same helper).
func TestRedactWebhookSecretsForURLFallbackToleratesUnrelatedAtInQuery(t *testing.T) {
	const token = "very-secret-at-in-query-token-f1x"
	raw := "https://chat.example.test/hooks/" + token + "\t/tail"
	text := realParseFailure(t, raw) + " (also seen https://docs.example.org/help?redirect=a@b)"

	got := RedactWebhookSecretsForURL(text, raw)

	if strings.Contains(got, token) {
		t.Fatalf("LEAK: %s", got)
	}
	if !strings.Contains(got, "https://docs.example.org/help?redirect=a@b") {
		t.Fatalf("unrelated URL with an '@' in its query was mangled: %s", got)
	}
}
