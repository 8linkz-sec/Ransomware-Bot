package textutil

import (
	"strings"
	"testing"
)

func TestRedactWebhookSecretsForURLMasksProviderDecodedPath(t *testing.T) {
	const configured = "https://hooks.eu.example/services/a%2Fb/FAKEPROVIDERTOKEN"
	const decoded = "https://hooks.eu.example/services/a/b/FAKEPROVIDERTOKEN"
	const unrelated = "https://other.example.test/public"
	input := "failed for " + decoded + " next " + unrelated

	got := RedactWebhookSecretsForURL(input, configured)
	if strings.Contains(got, "FAKEPROVIDERTOKEN") {
		t.Fatalf("decoded webhook path leaked in provider response: %q", got)
	}
	if got != "failed for https://hooks.eu.example/services/[redacted] next "+unrelated {
		t.Fatalf("redacted response = %q", got)
	}
}
