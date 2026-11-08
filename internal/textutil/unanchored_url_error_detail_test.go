package textutil

import (
	"net/url"
	"strings"
	"testing"
)

func TestRedactWebhookSecretsMasksUnanchoredParseErrorDetail(t *testing.T) {
	raw := "https://svc:PWDKKK\"1/tail@feeds.example.test/rss"
	err := malformedURLParseErrorForTest(t, raw)
	if !strings.Contains(err.Error(), `invalid port ":PWDKKK\"1" after host`) {
		t.Fatalf("unexpected parse error shape: %q", err)
	}

	got := RedactWebhookSecrets("feed failed: " + err.Error())
	if strings.Contains(got, "PWDKKK") || strings.Contains(got, "svc") {
		t.Fatalf("unanchored parse error exposed credential or untrusted host: %q", got)
	}
	if want := `feed failed: parse "[redacted URL]": invalid port "[redacted]" after host`; got != want {
		t.Fatalf("RedactWebhookSecrets() = %q, want %q", got, want)
	}
}

func TestRedactWebhookSecretsKeepsUnrelatedQuotedParseDetail(t *testing.T) {
	text := `parse "https://svc:bad/path": unrelated diagnostic "operator note"`
	if got := redactUnanchoredURLParseErrorDetails(text); got != text {
		t.Fatalf("nonmatching parse prose was rewritten: %q", got)
	}
	got := RedactWebhookSecrets(text)
	if !strings.Contains(got, `unrelated diagnostic "operator note"`) {
		t.Fatalf("unrelated quoted detail was changed: %q", got)
	}
}

func TestRedactWebhookSecretsMasksParseErrorAfterForgedProse(t *testing.T) {
	raw := "https://svc:PWDKKK\"1/tail@feeds.example.test/rss"
	err := malformedURLParseErrorForTest(t, raw)
	got := RedactWebhookSecrets(`forged parse "unterminated ` + err.Error())
	if strings.Contains(got, "PWDKKK") || !strings.Contains(got, `parse "[redacted URL]": invalid port "[redacted]" after host`) {
		t.Fatalf("valid error after forged prose was not safely redacted: %q", got)
	}
}

func TestRedactWebhookSecretsMasksBackslashAndQuoteInParseError(t *testing.T) {
	raw := "https://svc:PWD\\KKK\"1/tail@feeds.example.test/rss"
	err := malformedURLParseErrorForTest(t, raw)
	got := RedactWebhookSecrets(err.Error())
	if strings.Contains(got, "PWD") || strings.Contains(got, "svc") {
		t.Fatalf("escaped URL field or detail exposed credential: %q", got)
	}
	if want := `parse "[redacted URL]": invalid port "[redacted]" after host`; got != want {
		t.Fatalf("RedactWebhookSecrets() = %q, want %q", got, want)
	}
}

func TestRedactWebhookSecretsMasksUppercaseSchemeParseErrorDetail(t *testing.T) {
	raw := "HTTPS://svc:PWDKKK\"1/tail@feeds.example.test/rss"
	err := malformedURLParseErrorForTest(t, raw)
	got := RedactWebhookSecrets(err.Error())
	if strings.Contains(got, "PWDKKK") || strings.Contains(got, "svc") {
		t.Fatalf("uppercase scheme parse error exposed credential or untrusted host: %q", got)
	}
}

func TestRedactWebhookSecretsMasksSeparateUnanchoredParseErrors(t *testing.T) {
	firstURL := "https://svc:FIRSTSECRET\"1/tail@feeds.example.test/rss"
	secondURL := "https://svc:SECONDSECRET\"2/tail@feeds.example.test/rss"
	firstErr := malformedURLParseErrorForTest(t, firstURL)
	secondErr := malformedURLParseErrorForTest(t, secondURL)
	got := RedactWebhookSecrets(firstErr.Error() + " | " + secondErr.Error())
	if strings.Contains(got, "FIRSTSECRET") || strings.Contains(got, "SECONDSECRET") {
		t.Fatalf("one of the parse errors exposed its credential: %q", got)
	}
	if strings.Count(got, `parse "[redacted URL]": invalid port "[redacted]" after host`) != 2 {
		t.Fatalf("expected both parse errors to retain safe diagnostics: %q", got)
	}
}

func malformedURLParseErrorForTest(t *testing.T, raw string) error {
	t.Helper()
	_, err := url.Parse(raw)
	if err == nil {
		t.Fatal("expected malformed port to fail parsing")
	}
	return err
}
