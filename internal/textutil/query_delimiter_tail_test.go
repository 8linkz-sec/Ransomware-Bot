package textutil

import (
	"errors"
	"net/url"
	"strings"
	"testing"
)

func TestRedactURLCredentialsSwallowsRightParenInsideSensitiveQueryValue(t *testing.T) {
	const rawURL = "https://feeds.example.test/rss?token=SEC)RET"
	text := (&url.Error{Op: "Get", URL: rawURL, Err: errors.New("lookup failed")}).Error()

	got := RedactURLCredentials(text)
	const want = `Get "[redacted URL] lookup failed`
	if got != want {
		t.Fatalf("RedactURLCredentials() = %q, want %q", got, want)
	}
	for _, leaked := range []string{"SEC", "RET"} {
		if strings.Contains(got, leaked) {
			t.Fatalf("RedactURLCredentials() leaked %q: %s", leaked, got)
		}
	}
}

func TestRedactURLCredentialsKeepsSeparateURLAfterSensitiveQueryTail(t *testing.T) {
	text := `failed https://feeds.example.test/rss?apikey=SEC)RET then see https://docs.example.test/help`

	got := RedactURLCredentials(text)
	const want = `failed [redacted URL] then see https://docs.example.test/help`
	if got != want {
		t.Fatalf("RedactURLCredentials() = %q, want %q", got, want)
	}
}

func TestRedactURLCredentialsRedactsRepeatedSensitiveQueryTails(t *testing.T) {
	text := `first https://one.example.test/rss?token=FIRST)TAIL then https://two.example.test/rss?api_key=SECOND)TAIL`

	got := RedactURLCredentials(text)
	const want = `first [redacted URL] then [redacted URL]`
	if got != want {
		t.Fatalf("RedactURLCredentials() = %q, want %q", got, want)
	}
}

func TestRedactURLCredentialsKeepsClosingParenWhenSensitiveURLIsComplete(t *testing.T) {
	text := `(see https://feeds.example.test/rss?token=SEC)`

	got := RedactURLCredentials(text)
	const want = `(see https://feeds.example.test/rss?token=%5Bredacted%5D)`
	if got != want {
		t.Fatalf("RedactURLCredentials() = %q, want %q", got, want)
	}
}
