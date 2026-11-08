package textutil

import (
	"strconv"
	"strings"
	"testing"
)

func TestRedactWebhookSecretsForURLMasksProviderDecodedPath(t *testing.T) {
	tests := []struct {
		name    string
		escaped string
		decoded string
	}{
		{"slash", "%2F", "/"},
		{"space", "%20", " "},
		{"quote", "%22", "\""},
		{"hash", "%23", "#"},
		{"question mark", "%3F", "?"},
		{"apostrophe", "%27", "'"},
		{"backslash", "%5C", "\\"},
		{"right parenthesis", "%29", ")"},
		{"less than", "%3C", "<"},
		{"percent", "%25", "%"},
	}
	const unrelated = "https://other.example.test/public"
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configured := "https://hooks.eu.example/services/a" + tt.escaped + "b/FAKEPROVIDERTOKEN"
			decoded := "https://hooks.eu.example/services/a" + tt.decoded + "b/FAKEPROVIDERTOKEN"
			input := "failed for " + decoded + " next " + unrelated

			got := RedactWebhookSecretsForURL(input, configured)
			if strings.Contains(got, "FAKEPROVIDERTOKEN") {
				t.Fatalf("decoded webhook path leaked in provider response: %q", got)
			}
			if got != "failed for https://hooks.eu.example/services/[redacted] next "+unrelated {
				t.Fatalf("redacted response = %q", got)
			}

			quoted := "failed for " + strconv.Quote(decoded) + " next " + unrelated
			got = RedactWebhookSecretsForURL(quoted, configured)
			if strings.Contains(got, "FAKEPROVIDERTOKEN") {
				t.Fatalf("quoted decoded webhook path leaked: %q", got)
			}
			if got != "failed for \"https://hooks.eu.example/services/[redacted]\" next "+unrelated {
				t.Fatalf("redacted quoted response = %q", got)
			}
		})
	}
}
