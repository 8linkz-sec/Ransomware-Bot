package discordurl

import (
	"strings"
	"testing"
)

func TestParseAcceptsCurrentAndLegacyDiscordWebhookURLs(t *testing.T) {
	tests := []string{
		"https://discord.com/api/webhooks/123/abc",
		"https://discordapp.com/api/webhooks/123/abc",
		"https://discord.com/api/webhooks/123/abc?wait=true",
	}

	for _, rawURL := range tests {
		t.Run(rawURL, func(t *testing.T) {
			parts, err := Parse(rawURL)
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			if parts.ID != "123" || parts.Token != "abc" {
				t.Fatalf("parts = %#v, want ID 123 and token abc", parts)
			}
		})
	}
}

func TestParseRejectsInvalidDiscordWebhookURLs(t *testing.T) {
	tests := []string{
		"",
		" https://discord.com/api/webhooks/123/abc",
		"http://discord.com/api/webhooks/123/abc",
		"https://example.com/api/webhooks/123/abc",
		"https://discord.com/api/webhooks/",
		"https://discord.com/api/webhooks/123",
		"https://discord.com/api/webhooks/123/",
		"https://discord.com/api/v9/webhooks/123/abc",
		"https://discord.com/api/webhooks/123/abc/extra",
	}

	for _, rawURL := range tests {
		t.Run(rawURL, func(t *testing.T) {
			if _, err := Parse(rawURL); err == nil {
				t.Fatal("Parse() succeeded, want rejection")
			}
		})
	}
}

func TestParseAcceptsMixedCaseHost(t *testing.T) {
	parts, err := Parse("https://DISCORD.com/api/webhooks/123/abc")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if parts.ID != "123" || parts.Token != "abc" {
		t.Fatalf("parts = %#v, want ID 123 and token abc", parts)
	}
}

func TestParseRejectsMalformedURL(t *testing.T) {
	_, err := Parse("https://discord.com/api\n/webhooks/123/abc")
	if err == nil {
		t.Fatal("Parse() succeeded, want malformed-URL rejection")
	}
	if !strings.Contains(err.Error(), "malformed") {
		t.Fatalf("Parse() error = %v, want malformed-URL context", err)
	}
}

func TestParseRejectsEmptyID(t *testing.T) {
	// An empty token segment cannot survive strings.Trim (a trailing "/" is
	// trimmed away), so only the empty-ID form reaches the emptiness check.
	_, err := Parse("https://discord.com/api/webhooks//abc")
	if err == nil {
		t.Fatal("Parse() succeeded, want empty-segment rejection")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Fatalf("Parse() error = %v, want empty-segment context", err)
	}
}

func TestValidate(t *testing.T) {
	if err := Validate("https://discord.com/api/webhooks/123/abc"); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if err := Validate("https://example.com/api/webhooks/123/abc"); err == nil {
		t.Fatal("Validate() succeeded, want rejection")
	}
}
