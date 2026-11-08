package textutil

import (
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

func TestRedactWebhookSecretsForURLTailStopsAtURLBoundary(t *testing.T) {
	const anchor = "https://hooks.example.test/services/T0"
	const tail = "SYNTHETICSECRET35"
	for _, boundary := range []string{" ", "\t", "\"", "'", "<", ")"} {
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
