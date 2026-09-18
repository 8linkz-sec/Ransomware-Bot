package slack

import (
	"net/url"
	"testing"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/textutil"
)

// TestSlackURLButtonPercentEncodesBidiControls -- RED today: button.URL
// carries the raw U+202E.
func TestSlackURLButtonPercentEncodesBidiControls(t *testing.T) {
	button, ok := slackURLButton("Open website", "https://e.test/\u202Egpj.exe")
	if !ok {
		t.Fatal("slackURLButton returned ok=false, want ok=true")
	}
	want := "https://e.test/%E2%80%AEgpj.exe"
	if button.URL != want {
		t.Fatalf("button.URL = %q, want %q", button.URL, want)
	}
}

// TestSlackURLButtonStillRejectsBidiInUserinfo is the B1/B4 tripwire on the
// Slack side: a bidi control in the userinfo component makes url.Parse fail
// on the raw string; slackURLButton must validate the raw string, not the
// encoded one, or a URL that renders with no button at all today would start
// rendering a live button to evil.test. Passes today and must still pass
// after the fix; its mutation is "encode before url.Parse", which it kills.
func TestSlackURLButtonStillRejectsBidiInUserinfo(t *testing.T) {
	raw := "https://discord.com\u202E@evil.test/"

	if _, err := url.Parse(raw); err == nil { //nolint:staticcheck // SA1007: the precondition IS that this fails to parse; that is the trap being documented
		t.Fatalf("precondition failed: url.Parse(%q) succeeded, want an error documenting the trap", raw)
	}
	if _, err := url.Parse(textutil.PercentEncodeBidiControls(raw)); err != nil {
		t.Fatalf("precondition failed: url.Parse(encoded) = %v, want success documenting the trap", err)
	}

	_, ok := slackURLButton("Open website", raw)
	if ok {
		t.Fatal("slackURLButton returned ok=true, want false -- must still reject, not silently start " +
			"accepting a URL that fails to parse today")
	}
}
