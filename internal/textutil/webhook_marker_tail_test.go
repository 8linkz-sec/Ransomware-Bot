package textutil

import (
	"errors"
	"net/url"
	"strings"
	"testing"
)

func TestRedactWebhookSecretsForURLConsumesContiguousAnchorTail(t *testing.T) {
	const tail = "SYNTHETICSECRET35"
	tests := []struct {
		name   string
		anchor string
	}{
		{"parseable custom host", "https://hooks.example.test/services/T0"},
		{"parseable known host", "https://hooks.slack.com/services/T0"},
		{"unparseable known host", "https://hooks.slack.com:bad/services/T0"},
		{"unparseable custom host", "https://hooks.example.test:bad/services/T0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := "delivery to " + tt.anchor + "/B0/" + tail + " failed"
			got := RedactWebhookSecretsForURL(input, tt.anchor)
			if strings.Contains(got, tail) || strings.Contains(got, "T0") {
				t.Fatalf("anchored URL tail survived: %q", got)
			}
			if !strings.HasPrefix(got, "delivery to ") || !strings.HasSuffix(got, " failed") {
				t.Fatalf("surrounding message was lost: %q", got)
			}
		})
	}
}

func TestRedactWebhookSecretsForURLTailStopsAtWhitespace(t *testing.T) {
	const anchor = "https://hooks.example.test/services/T0"
	const tail = "SYNTHETICSECRET35"
	for _, boundary := range []string{" ", "\t", "\n", "\f", "\r"} {
		t.Run(boundary, func(t *testing.T) {
			input := anchor + "/B0/" + tail + boundary + "context"
			got := RedactWebhookSecretsForURL(input, anchor)
			if strings.Contains(got, tail) {
				t.Fatalf("anchored URL tail survived: %q", got)
			}
			if !strings.HasSuffix(got, boundary+"context") {
				t.Fatalf("boundary or following context was lost: %q", got)
			}
		})
	}
}

func TestRedactWebhookSecretsForURLConsumesPunctuationInsideTail(t *testing.T) {
	const anchor = "https://hooks.example.test/services/T0"
	for _, punctuation := range []string{")", "'", `"`, "<"} {
		t.Run(punctuation, func(t *testing.T) {
			input := "delivery to " + anchor + "/B0/SYNTHETICSEC" + punctuation + "RET failed"
			got := RedactWebhookSecretsForURL(input, anchor)
			if strings.Contains(got, "SYNTHETICSEC") || strings.Contains(got, "RET") {
				t.Fatalf("punctuation split a secret URL tail: %q", got)
			}
			if !strings.HasSuffix(got, " failed") {
				t.Fatalf("following message was lost: %q", got)
			}
		})
	}
}

func TestRedactWebhookSecretsForURLConsumesQueryAndRepeatedTails(t *testing.T) {
	const anchor = "https://hooks.example.test/services/T0"
	const tail = "SYNTHETICSECRET35"
	input := anchor + "?token=" + tail + " and " + anchor + "&token=" + tail + " next https://other.example.test/public"
	got := RedactWebhookSecretsForURL(input, anchor)
	if strings.Contains(got, tail) || strings.Contains(got, "T0") {
		t.Fatalf("an anchored query tail survived: %q", got)
	}
	if !strings.Contains(got, " and ") || !strings.Contains(got, " next https://other.example.test/public") {
		t.Fatalf("unrelated context was lost: %q", got)
	}
}

func TestRedactWebhookSecretsForURLPreservesQuotedURLErrorSyntax(t *testing.T) {
	tests := []struct {
		name        string
		anchor      string
		urlInError  string
		wantRawPath bool
	}{
		{
			name:       "exact configured URL",
			anchor:     "https://hooks.example.test/services/T0",
			urlInError: "https://hooks.example.test/services/T0",
		},
		{
			name:        "RawPath variant without query",
			anchor:      "https://hooks.example.test/services/a%2Fb%5Cc?token=SECRET",
			urlInError:  "https://hooks.example.test/services/a%2Fb%5Cc",
			wantRawPath: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsed, err := url.Parse(tt.anchor)
			if err != nil || tt.wantRawPath && parsed.RawPath == "" {
				t.Fatalf("url.Parse(%q) = %#v, %v; RawPath branch not exercised", tt.anchor, parsed, err)
			}
			input := (&url.Error{Op: "Post", URL: tt.urlInError, Err: errors.New("network down")}).Error()
			got := RedactWebhookSecretsForURL(input, tt.anchor)
			want := `Post "https://hooks.example.test/services/[redacted]": network down`
			if got != want {
				t.Fatalf("RedactWebhookSecretsForURL(%q) = %q, want %q", input, got, want)
			}
		})
	}
}

func TestRedactWebhookSecretsForURLPreservesBareErrorColon(t *testing.T) {
	const anchor = "https://chat.example.test/hooks/secretvalue"
	for _, tc := range []struct {
		name, text, want string
	}{
		{"full URL", "post " + anchor + ": timeout", "post https://chat.example.test/[redacted]: timeout"},
		{"standalone path", "post /hooks/secretvalue: timeout", "post /[redacted]: timeout"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := RedactWebhookSecretsForURL(tc.text, anchor); got != tc.want {
				t.Fatalf("RedactWebhookSecretsForURL() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRedactWebhookSecretsForURLConsumesColonSecretTail(t *testing.T) {
	const anchor = "https://chat.example.test/hooks/secretvalue"
	for _, tc := range []struct {
		name, text, want string
	}{
		{"full URL", "post " + anchor + ":EXTRASECRET timeout", "post https://chat.example.test/[redacted] timeout"},
		{"standalone path", "post /hooks/secretvalue:EXTRASECRET timeout", "post /[redacted] timeout"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := RedactWebhookSecretsForURL(tc.text, anchor); got != tc.want {
				t.Fatalf("RedactWebhookSecretsForURL() = %q, want %q", got, tc.want)
			}
		})
	}
}
