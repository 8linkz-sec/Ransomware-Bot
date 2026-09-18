package webhookhttp

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestNewClientUsesDedicatedTransport(t *testing.T) {
	client := NewClient()
	if client.Timeout != RequestTimeout {
		t.Fatalf("Timeout = %v, want %v", client.Timeout, RequestTimeout)
	}
	if client.Transport == nil {
		t.Fatal("Transport is nil")
	}
	if client.Transport == http.DefaultTransport {
		t.Fatal("Transport reuses http.DefaultTransport")
	}
	if client.CheckRedirect == nil {
		t.Fatal("CheckRedirect is nil")
	}
}

func TestNewClientUsesConfiguredPolicy(t *testing.T) {
	client := NewClient(Policy{
		RequestTimeout: 25 * time.Second,
		MaxAttempts:    2,
		RetryBaseDelay: 10 * time.Millisecond,
	})
	if client.Timeout != 25*time.Second {
		t.Fatalf("Timeout = %v, want configured timeout", client.Timeout)
	}
}

func TestNormalizePolicyFillsDefaults(t *testing.T) {
	policy := NormalizePolicy(Policy{})
	defaults := DefaultPolicy()
	if policy.RequestTimeout != defaults.RequestTimeout {
		t.Fatalf("RequestTimeout = %v, want %v", policy.RequestTimeout, defaults.RequestTimeout)
	}
	if policy.MaxAttempts != defaults.MaxAttempts {
		t.Fatalf("MaxAttempts = %d, want %d", policy.MaxAttempts, defaults.MaxAttempts)
	}
	if policy.RetryBaseDelay != 0 {
		t.Fatalf("RetryBaseDelay = %v, want explicit zero preserved", policy.RetryBaseDelay)
	}
}

func TestNewClientDoesNotShareTransports(t *testing.T) {
	first := NewClient()
	second := NewClient()
	if first.Transport == second.Transport {
		t.Fatal("NewClient returned clients sharing one transport")
	}
}

func TestSameHostHTTPSRedirectPolicyAllowsSameHostHTTPS(t *testing.T) {
	original := requestWithURL(t, "https://hooks.slack.com/services/T/B/token")
	redirect := requestWithURL(t, "https://hooks.slack.com/services/T/B/other")

	if err := SameHostHTTPSRedirectPolicy(redirect, []*http.Request{original}); err != nil {
		t.Fatalf("SameHostHTTPSRedirectPolicy() error = %v", err)
	}
}

func TestSameHostHTTPSRedirectPolicyBlocksSchemeDowngrade(t *testing.T) {
	original := requestWithURL(t, "https://discord.com/api/webhooks/123/token")
	redirect := requestWithURL(t, "http://discord.com/api/webhooks/123/token")

	err := SameHostHTTPSRedirectPolicy(redirect, []*http.Request{original})
	if err == nil {
		t.Fatal("SameHostHTTPSRedirectPolicy() succeeded, want downgrade rejection")
	}
	if !strings.Contains(err.Error(), "refusing redirect") {
		t.Fatalf("error = %v, want redirect rejection", err)
	}
}

func TestSameHostHTTPSRedirectPolicyBlocksHostChange(t *testing.T) {
	original := requestWithURL(t, "https://discord.com/api/webhooks/123/token")
	redirect := requestWithURL(t, "https://example.com/api/webhooks/123/token")

	err := SameHostHTTPSRedirectPolicy(redirect, []*http.Request{original})
	if err == nil {
		t.Fatal("SameHostHTTPSRedirectPolicy() succeeded, want host-change rejection")
	}
	if !strings.Contains(err.Error(), "host changed") {
		t.Fatalf("error = %v, want host-change rejection", err)
	}
}

func TestNormalizePolicyReplacesNegativeRetryBaseDelay(t *testing.T) {
	policy := NormalizePolicy(Policy{RetryBaseDelay: -time.Second})
	if policy.RetryBaseDelay != DefaultPolicy().RetryBaseDelay {
		t.Fatalf("RetryBaseDelay = %v, want default", policy.RetryBaseDelay)
	}
}

func TestSameHostHTTPSRedirectPolicyBlocksTooManyHops(t *testing.T) {
	original := requestWithURL(t, "https://discord.com/api/webhooks/123/token")
	via := make([]*http.Request, maxSameHostRedirects)
	for i := range via {
		via[i] = original
	}

	err := SameHostHTTPSRedirectPolicy(requestWithURL(t, "https://discord.com/next"), via)
	if err == nil {
		t.Fatal("SameHostHTTPSRedirectPolicy() succeeded, want hop-limit rejection")
	}
	if !strings.Contains(err.Error(), "redirect blocked after") {
		t.Fatalf("error = %v, want hop-limit rejection", err)
	}
}

func TestSameHostHTTPSRedirectPolicyRejectsMissingURLs(t *testing.T) {
	original := requestWithURL(t, "https://discord.com/api/webhooks/123/token")
	redirect := requestWithURL(t, "https://discord.com/api/webhooks/123/other")

	tests := []struct {
		name     string
		req      *http.Request
		via      []*http.Request
		wantText string
	}{
		{
			name:     "nil redirect request",
			req:      nil,
			via:      []*http.Request{original},
			wantText: "missing redirect URL",
		},
		{
			name:     "redirect request without URL",
			req:      &http.Request{},
			via:      []*http.Request{original},
			wantText: "missing redirect URL",
		},
		{
			name:     "empty via chain",
			req:      redirect,
			via:      nil,
			wantText: "missing original URL",
		},
		{
			name:     "nil original request",
			req:      redirect,
			via:      []*http.Request{nil},
			wantText: "missing original URL",
		},
		{
			name:     "original request without URL",
			req:      redirect,
			via:      []*http.Request{{}},
			wantText: "missing original URL",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := SameHostHTTPSRedirectPolicy(tt.req, tt.via)
			if err == nil {
				t.Fatal("SameHostHTTPSRedirectPolicy() succeeded, want rejection")
			}
			if !strings.Contains(err.Error(), tt.wantText) {
				t.Fatalf("error = %v, want %q", err, tt.wantText)
			}
		})
	}
}

func TestSameHostHTTPSRedirectPolicyBlocksPortChange(t *testing.T) {
	original := requestWithURL(t, "https://discord.com:8443/api/webhooks/123/token")
	redirect := requestWithURL(t, "https://discord.com/api/webhooks/123/token")

	err := SameHostHTTPSRedirectPolicy(redirect, []*http.Request{original})
	if err == nil {
		t.Fatal("SameHostHTTPSRedirectPolicy() succeeded, want port-change rejection")
	}
	if !strings.Contains(err.Error(), "host changed") {
		t.Fatalf("error = %v, want host-change rejection", err)
	}
}

func TestSameWebhookHostRejectsNilURLs(t *testing.T) {
	u := requestWithURL(t, "https://discord.com/api").URL
	if sameWebhookHost(nil, u) {
		t.Fatal("sameWebhookHost(nil, u) = true, want false")
	}
	if sameWebhookHost(u, nil) {
		t.Fatal("sameWebhookHost(u, nil) = true, want false")
	}
	if sameWebhookHost(nil, nil) {
		t.Fatal("sameWebhookHost(nil, nil) = true, want false")
	}
}

func TestNormalizedWebhookPortDefaults(t *testing.T) {
	tests := []struct {
		rawURL string
		want   string
	}{
		{"https://discord.com/api", "443"},
		{"http://discord.com/api", "80"},
		{"https://discord.com:8443/api", "8443"},
		{"ftp://discord.com/api", ""},
	}

	for _, tt := range tests {
		t.Run(tt.rawURL, func(t *testing.T) {
			u := requestWithURL(t, tt.rawURL).URL
			if got := normalizedWebhookPort(u); got != tt.want {
				t.Fatalf("normalizedWebhookPort(%q) = %q, want %q", tt.rawURL, got, tt.want)
			}
		})
	}
}

func requestWithURL(t *testing.T, rawURL string) *http.Request {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("Parse(%q) error = %v", rawURL, err)
	}
	return &http.Request{URL: parsed}
}
