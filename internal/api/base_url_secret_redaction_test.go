package api

import (
	"strings"
	"testing"
)

// F2, P2 (found 2026-09-04): NormalizeBaseURL used to wrap url.Parse's raw
// *url.Error with %w unredacted. It has three callers -- config.go's
// validateAPIConfig (which wraps it once more with %w, redacting nothing
// itself) and this package's own NewClientWithBaseURLAndPolicy /
// NewClientWithHTTPClient (both of which return it as-is) -- so fixing it
// here, at the source, protects all three. Reproduced end to end with a real
// binary: an api_base_url with userinfo credentials put the FULL raw URL on
// --check-config's stdout, in --dry-run's JSON log line, and in bot.log on a
// failed hot reload. This leaked MORE than the sibling feed-URL defect: the
// whole secret, not just an authority prefix, because api_base_url has no
// feedurl-style userinfo rejection ahead of the parse failure.

func TestNormalizeBaseURLRedactsUserinfoOnParseFailure(t *testing.T) {
	const password = "APIB64TOKEN"
	const secretTail = "APISLASHSECRET777"
	raw := "https://svc:" + password + "/" + secretTail + "@api.example.com"

	_, err := NormalizeBaseURL(raw)
	if err == nil {
		t.Fatal("expected an error for a malformed api_base_url")
	}
	msg := err.Error()
	t.Logf("error: %s", msg)

	if strings.Contains(msg, password) {
		t.Fatalf("LEAK: userinfo password %q survives: %s", password, msg)
	}
	if strings.Contains(msg, secretTail) {
		t.Fatalf("LEAK: userinfo password tail %q survives: %s", secretTail, msg)
	}
	if strings.Contains(msg, raw) {
		t.Fatalf("LEAK: full raw api_base_url survives: %s", msg)
	}
	for _, want := range []string{"invalid api base URL", "api.example.com"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error = %v, want it to still contain %q for diagnosis", err, want)
		}
	}
}

// TestNormalizeBaseURLLeavesSafeMessageUnredacted proves the fix does not
// degrade a message that was never a leak risk: a URL that parses cleanly
// but fails the later host-required / scheme-allow-list checks, neither of
// which echoes the raw URL, and neither of which goes through
// RedactWebhookErrorForURL at all.
func TestNormalizeBaseURLLeavesSafeMessageUnredacted(t *testing.T) {
	_, err := NormalizeBaseURL("ftp://api.example.com")
	if err == nil {
		t.Fatal("expected an error for an unsupported scheme")
	}
	want := "api base URL scheme must be http or https"
	if err.Error() != want {
		t.Fatalf("NormalizeBaseURL() error = %q, want exactly %q", err.Error(), want)
	}
}
