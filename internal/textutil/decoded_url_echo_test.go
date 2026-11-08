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

func TestRedactWebhookSecretsForURLMasksProviderDecodedQuery(t *testing.T) {
	tests := []struct {
		name       string
		configured string
		echoed     string
		want       string
	}{
		{
			name:       "escaped path and decoded percent query",
			configured: "https://hooks.eu.example/services/a%2Fb/FAKEPROVIDERTOKEN?trace=EXTRA%20SECRET",
			echoed:     "https://hooks.eu.example/services/a%2Fb/FAKEPROVIDERTOKEN?trace=EXTRA SECRET",
			want:       "https://hooks.eu.example/services/[redacted]",
		},
		{
			name:       "decoded path and decoded percent query",
			configured: "https://hooks.eu.example/services/a%2Fb/FAKEPROVIDERTOKEN?trace=EXTRA%20SECRET",
			echoed:     "https://hooks.eu.example/services/a/b/FAKEPROVIDERTOKEN?trace=EXTRA SECRET",
			want:       "https://hooks.eu.example/services/[redacted]",
		},
		{
			name:       "query only percent space",
			configured: "https://hooks.eu.example/public?token=EXTRA%20SECRET",
			echoed:     "https://hooks.eu.example/public?token=EXTRA SECRET",
			want:       "https://hooks.eu.example/[redacted]",
		},
		{
			name:       "decoded path and decoded plus query",
			configured: "https://hooks.eu.example/services/a%2Fb/FAKEPROVIDERTOKEN?trace=EXTRA+SECRET",
			echoed:     "https://hooks.eu.example/services/a/b/FAKEPROVIDERTOKEN?trace=EXTRA SECRET",
			want:       "https://hooks.eu.example/services/[redacted]",
		},
		{
			name:       "query only plus space",
			configured: "https://hooks.eu.example/public?token=EXTRA+SECRET",
			echoed:     "https://hooks.eu.example/public?token=EXTRA SECRET",
			want:       "https://hooks.eu.example/[redacted]",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const unrelated = "https://other.example.test/public"
			got := RedactWebhookSecretsForURL("failed for "+tt.echoed+" next "+unrelated, tt.configured)
			if strings.Contains(got, "FAKEPROVIDERTOKEN") || strings.Contains(got, "EXTRA") || strings.Contains(got, "SECRET") {
				t.Fatalf("decoded webhook URL leaked: %q", got)
			}
			if want := "failed for " + tt.want + " next " + unrelated; got != want {
				t.Fatalf("redacted response = %q, want %q", got, want)
			}
		})
	}
}

func TestRedactWebhookSecretsForURLMasksProviderPathOnlyEcho(t *testing.T) {
	tests := []struct {
		name       string
		configured string
		echoed     string
		want       string
	}{
		{
			name:       "decoded path",
			configured: "https://hooks.eu.example/services/a%2Fb/FAKEPROVIDERTOKEN",
			echoed:     "/services/a/b/FAKEPROVIDERTOKEN",
			want:       "/services/[redacted]",
		},
		{
			name:       "escaped path",
			configured: "https://hooks.eu.example/services/a%2Fb/FAKEPROVIDERTOKEN",
			echoed:     "/services/a%2Fb/FAKEPROVIDERTOKEN",
			want:       "/services/[redacted]",
		},
		{
			name:       "request URI",
			configured: "https://hooks.eu.example/services/a%2Fb/FAKEPROVIDERTOKEN?trace=EXTRA%20SECRET",
			echoed:     "/services/a%2Fb/FAKEPROVIDERTOKEN?trace=EXTRA%20SECRET",
			want:       "/services/[redacted]",
		},
		{
			name:       "decoded path and query without host",
			configured: "https://hooks.eu.example/services/a%2Fb/FAKEPROVIDERTOKEN?trace=EXTRA%20SECRET",
			echoed:     "/services/a/b/FAKEPROVIDERTOKEN?trace=EXTRA SECRET",
			want:       "/services/[redacted]",
		},
		{
			name:       "query only without host",
			configured: "https://hooks.eu.example/public?token=EXTRA%20SECRET",
			echoed:     "/public?token=EXTRA SECRET",
			want:       "/[redacted]",
		},
		{
			name:       "root path with decoded query",
			configured: "https://hooks.eu.example/?token=EXTRA%20SECRET",
			echoed:     "/?token=EXTRA SECRET",
			want:       "/[redacted]",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RedactWebhookSecretsForURL("failed for "+tt.echoed+" next", tt.configured)
			for _, token := range []string{"FAKEPROVIDERTOKEN", "EXTRA", "SECRET"} {
				if strings.Contains(got, token) {
					t.Fatalf("path-only webhook echo leaked %q: %q", token, got)
				}
			}
			if want := "failed for " + tt.want + " next"; got != want {
				t.Fatalf("redacted response = %q, want %q", got, want)
			}
		})
	}
}
