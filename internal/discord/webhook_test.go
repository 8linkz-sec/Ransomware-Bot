package discord

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/api"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/config"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/retrypolicy"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/rss"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/webhookhttp"
)

type discordRoundTripFunc func(*http.Request) (*http.Response, error)

func (f discordRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func newDiscordTestSender(transport http.RoundTripper) *WebhookSender {
	return newDiscordTestSenderWithClient(&http.Client{Transport: transport})
}

func newDiscordTestSenderWithClient(client *http.Client, policies ...webhookhttp.Policy) *WebhookSender {
	return newWebhookSenderWithClient(client, policies...)
}

func TestSendRansomwareEntryWrapperExecutesWebhook(t *testing.T) {
	webhookID := "123456789012345678"
	webhookToken := "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_"
	webhookURL := "https://discord.com/api/webhooks/" + webhookID + "/" + webhookToken

	var requests []string
	var bodies []string
	sender := newDiscordTestSender(
		discordRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			body, err := io.ReadAll(req.Body)
			if err != nil {
				t.Errorf("ReadAll(body) error = %v", err)
			}
			requests = append(requests, req.URL.Path)
			bodies = append(bodies, string(body))
			return discordNoContentResponse(req), nil
		}),
	)
	cfg := config.DefaultConfig()

	err := sender.SendRansomwareEntry(context.Background(), webhookURL, api.RansomwareEntry{
		Group:   "LockBit",
		Victim:  "Example Corp",
		Country: "DE",
	}, config.NotificationFormatOptions(&cfg.Format))
	if err != nil {
		t.Fatalf("SendRansomwareEntry() error = %v", err)
	}

	if len(requests) != 1 {
		t.Fatalf("request count = %d, want 1", len(requests))
	}
	if !strings.Contains(requests[0], "/webhooks/"+webhookID+"/"+webhookToken) {
		t.Fatalf("request path = %q, want webhook ID/token path", requests[0])
	}
	if !strings.Contains(bodies[0], "Example Corp") {
		t.Fatalf("ransomware webhook body missing victim: %s", bodies[0])
	}
	if err := (&WebhookSender{}).Close(); err != nil {
		t.Fatalf("Close(nil client) error = %v", err)
	}
}

func TestNewWebhookSenderUsesConfiguredPolicy(t *testing.T) {
	sender, err := NewWebhookSender(webhookhttp.Policy{
		RequestTimeout: 17 * time.Second,
		MaxAttempts:    2,
		RetryBaseDelay: 250 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewWebhookSender() error = %v", err)
	}
	if sender.client.Timeout != 17*time.Second {
		t.Fatalf("client timeout = %v, want 17s", sender.client.Timeout)
	}
	if sender.policy.MaxAttempts != 2 {
		t.Fatalf("MaxAttempts = %d, want 2", sender.policy.MaxAttempts)
	}
}

func TestSendRSSEntryWrapperExecutesWebhook(t *testing.T) {
	webhookID := "123456789012345678"
	webhookToken := "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_"
	webhookURL := "https://discord.com/api/webhooks/" + webhookID + "/" + webhookToken

	var body string
	requests := 0
	sender := newDiscordTestSender(
		discordRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			requests++
			rawBody, err := io.ReadAll(req.Body)
			if err != nil {
				t.Errorf("ReadAll(body) error = %v", err)
			}
			body = string(rawBody)
			if !strings.Contains(req.URL.Path, "/webhooks/"+webhookID+"/"+webhookToken) {
				t.Errorf("request path = %q, want webhook ID/token path", req.URL.Path)
			}
			return discordNoContentResponse(req), nil
		}),
	)
	cfg := config.DefaultConfig()

	err := sender.SendRSSEntry(context.Background(), webhookURL, rss.Entry{
		Title:     "RSS Wrapper Alert",
		Link:      "https://example.test/rss-wrapper-alert",
		FeedTitle: "Example Feed",
		FeedURL:   "https://example.test/feed.xml",
	}, config.FeedTypeGeneral, config.NotificationFormatOptions(&cfg.Format))
	if err != nil {
		t.Fatalf("SendRSSEntry() error = %v", err)
	}
	if requests != 1 {
		t.Fatalf("request count = %d, want 1", requests)
	}
	if !strings.Contains(body, "RSS Wrapper Alert") {
		t.Fatalf("RSS webhook body missing title: %s", body)
	}
}

func TestSendRansomwareEntryWrapperRejectsCancelledContext(t *testing.T) {
	sender, err := NewWebhookSender()
	if err != nil {
		t.Fatalf("NewWebhookSender() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cfg := config.DefaultConfig()

	err = sender.SendRansomwareEntry(ctx, discordValidWebhookURL(), api.RansomwareEntry{
		Victim: "Cancelled Victim",
	}, config.NotificationFormatOptions(&cfg.Format))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("SendRansomwareEntry() error = %v, want context.Canceled", err)
	}
}

func TestSendRSSEntryWrapperRejectsCancelledContext(t *testing.T) {
	sender, err := NewWebhookSender()
	if err != nil {
		t.Fatalf("NewWebhookSender() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cfg := config.DefaultConfig()

	err = sender.SendRSSEntry(ctx, discordValidWebhookURL(), rss.Entry{
		Title: "Cancelled RSS",
	}, config.FeedTypeGeneral, config.NotificationFormatOptions(&cfg.Format))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("SendRSSEntry() error = %v, want context.Canceled", err)
	}
}

func TestSendRansomwareEntryWrapperRejectsInvalidWebhookURL(t *testing.T) {
	sender, err := NewWebhookSender()
	if err != nil {
		t.Fatalf("NewWebhookSender() error = %v", err)
	}
	cfg := config.DefaultConfig()

	err = sender.SendRansomwareEntry(context.Background(), "https://discord.com/not-a-webhook", api.RansomwareEntry{
		Victim: "Invalid URL Victim",
	}, config.NotificationFormatOptions(&cfg.Format))
	if err == nil || !strings.Contains(err.Error(), "invalid webhook URL") {
		t.Fatalf("SendRansomwareEntry() error = %v, want invalid webhook URL", err)
	}
}

func TestSendRSSEntryWrapperRejectsInvalidWebhookURL(t *testing.T) {
	sender, err := NewWebhookSender()
	if err != nil {
		t.Fatalf("NewWebhookSender() error = %v", err)
	}
	cfg := config.DefaultConfig()

	err = sender.SendRSSEntry(context.Background(), "https://discord.com/not-a-webhook", rss.Entry{
		Title: "Invalid URL RSS",
	}, config.FeedTypeGeneral, config.NotificationFormatOptions(&cfg.Format))
	if err == nil || !strings.Contains(err.Error(), "invalid webhook URL") {
		t.Fatalf("SendRSSEntry() error = %v, want invalid webhook URL", err)
	}
}

func TestSendRansomwareEntryWrapperWrapsExecutionFailure(t *testing.T) {
	cfg := config.DefaultConfig()

	err := (&WebhookSender{}).SendRansomwareEntry(context.Background(), discordValidWebhookURL(), api.RansomwareEntry{
		Victim: "Execution Failure Victim",
	}, config.NotificationFormatOptions(&cfg.Format))
	if err == nil || !strings.Contains(err.Error(), "failed to send ransomware entry") {
		t.Fatalf("SendRansomwareEntry() error = %v, want wrapped execution failure", err)
	}
	if !strings.Contains(err.Error(), "discord HTTP client not available") {
		t.Fatalf("SendRansomwareEntry() error = %v, want downstream client error", err)
	}
}

func TestSendRSSEntryWrapperWrapsExecutionFailure(t *testing.T) {
	cfg := config.DefaultConfig()

	err := (&WebhookSender{}).SendRSSEntry(context.Background(), discordValidWebhookURL(), rss.Entry{
		Title: "Execution Failure RSS",
	}, config.FeedTypeGeneral, config.NotificationFormatOptions(&cfg.Format))
	if err == nil || !strings.Contains(err.Error(), "failed to send RSS entry") {
		t.Fatalf("SendRSSEntry() error = %v, want wrapped execution failure", err)
	}
	if !strings.Contains(err.Error(), "discord HTTP client not available") {
		t.Fatalf("SendRSSEntry() error = %v, want downstream client error", err)
	}
}

func discordValidWebhookURL() string {
	webhookID := "123456789012345678"
	webhookToken := "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_"
	return "https://discord.com/api/webhooks/" + webhookID + "/" + webhookToken
}

func discordNoContentResponse(req *http.Request) *http.Response {
	return &http.Response{
		StatusCode: http.StatusNoContent,
		Status:     "204 No Content",
		Body:       io.NopCloser(strings.NewReader("")),
		Header:     make(http.Header),
		Request:    req,
	}
}

func TestSendRansomwareEntryRedactsWebhookURLFromTransportError(t *testing.T) {
	webhookID := "123456789012345678"
	webhookToken := "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_"
	webhookURL := "https://discord.com/api/webhooks/" + webhookID + "/" + webhookToken

	sender := newDiscordTestSender(
		discordRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			return nil, errors.New("permanent failure posting to " + req.URL.String())
		}),
	)
	cfg := config.DefaultConfig()

	err := sender.SendRansomwareEntry(context.Background(), webhookURL, api.RansomwareEntry{
		Victim: "Redacted Transport Victim",
	}, config.NotificationFormatOptions(&cfg.Format))
	if err == nil {
		t.Fatal("expected transport error")
	}

	got := err.Error()
	if strings.Contains(got, webhookURL) || strings.Contains(got, webhookToken) {
		t.Fatalf("error leaked Discord webhook URL/token: %s", got)
	}
	if !strings.Contains(got, "https://discord.com/api/webhooks/[redacted]") {
		t.Fatalf("error did not include redacted Discord marker: %s", got)
	}
}

func TestSendRansomwareEntryWrapsTransportErrorForRetryClassification(t *testing.T) {
	webhookURL := discordValidWebhookURL()

	sender := newDiscordTestSender(
		discordRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			return nil, errors.New("provider response body mentioned timeout and 500")
		}),
	)
	cfg := config.DefaultConfig()

	err := sender.SendRansomwareEntry(context.Background(), webhookURL, api.RansomwareEntry{
		Victim: "Transport Classification Victim",
	}, config.NotificationFormatOptions(&cfg.Format))
	if err == nil {
		t.Fatal("expected transport error")
	}
	var transportErr *webhookhttp.TransportError
	if !errors.As(err, &transportErr) {
		t.Fatalf("SendRansomwareEntry() error type = %T, want TransportError", err)
	}
	if webhookhttp.IsRetryableError(err) {
		t.Fatal("plain Discord transport error with retry-looking text classified retryable, want false")
	}
}

func TestSendRansomwareEntryDoesNotRetryAmbiguousServerError(t *testing.T) {
	webhookID := "123456789012345678"
	webhookToken := "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_"
	webhookURL := "https://discord.com/api/webhooks/" + webhookID + "/" + webhookToken
	requests := 0

	sender := newDiscordTestSender(
		discordRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			requests++
			return &http.Response{
				StatusCode: http.StatusInternalServerError,
				Status:     "500 Internal Server Error",
				Body:       io.NopCloser(strings.NewReader(`{"message":"server error","code":0}`)),
				Header:     make(http.Header),
				Request:    req,
			}, nil
		}),
	)
	cfg := config.DefaultConfig()

	err := sender.SendRansomwareEntry(context.Background(), webhookURL, api.RansomwareEntry{
		Victim: "Server Error Victim",
	}, config.NotificationFormatOptions(&cfg.Format))
	if err == nil {
		t.Fatal("expected server error")
	}
	var statusErr *WebhookHTTPError
	if !errors.As(err, &statusErr) {
		t.Fatalf("SendRansomwareEntry() error type = %T, want WebhookHTTPError", err)
	}
	if statusErr.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status code = %d, want 500", statusErr.StatusCode)
	}
	if !statusErr.Retryable() {
		t.Fatal("500 Discord webhook error Retryable() = false, want true")
	}
	if strings.Contains(err.Error(), "server error") {
		t.Fatalf("HTTP error leaked response body: %v", err)
	}
	if requests != 1 {
		t.Fatalf("request count = %d, want 1", requests)
	}
}

func TestSendRansomwareEntryRetriesRateLimitWithConfiguredPolicy(t *testing.T) {
	webhookID := "123456789012345678"
	webhookToken := "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_"
	webhookURL := "https://discord.com/api/webhooks/" + webhookID + "/" + webhookToken
	requests := 0

	sender := newDiscordTestSenderWithClient(&http.Client{
		Transport: discordRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			requests++
			if requests == 1 {
				return &http.Response{
					StatusCode: http.StatusTooManyRequests,
					Status:     "429 Too Many Requests",
					Body:       io.NopCloser(strings.NewReader("rate limited")),
					Header:     make(http.Header),
					Request:    req,
				}, nil
			}
			return discordNoContentResponse(req), nil
		}),
	}, webhookhttp.Policy{MaxAttempts: 2})
	cfg := config.DefaultConfig()

	err := sender.SendRansomwareEntry(context.Background(), webhookURL, api.RansomwareEntry{
		Victim: "Rate Limited Victim",
	}, config.NotificationFormatOptions(&cfg.Format))
	if err != nil {
		t.Fatalf("SendRansomwareEntry() error = %v, want retry success", err)
	}
	if requests != 2 {
		t.Fatalf("request count = %d, want 2", requests)
	}
}

func TestDiscordRateLimitRetryDelayUsesRetryAfterHeader(t *testing.T) {
	resp := &http.Response{Header: make(http.Header)}
	resp.Header.Set("Retry-After", "2")

	got := discordRateLimitRetryDelay(resp, webhookhttp.Policy{RetryBaseDelay: 250 * time.Millisecond})

	if got != 2*time.Second {
		t.Fatalf("discordRateLimitRetryDelay() = %v, want Retry-After delay", got)
	}
}

func TestDiscordRateLimitRetryDelayFallsBackToConfiguredDelay(t *testing.T) {
	resp := &http.Response{Header: make(http.Header)}

	got := discordRateLimitRetryDelay(resp, webhookhttp.Policy{RetryBaseDelay: 250 * time.Millisecond})

	if got != 250*time.Millisecond {
		t.Fatalf("discordRateLimitRetryDelay() = %v, want configured fallback delay", got)
	}
}

// TestDiscordRateLimitRetryDelayCapsOverflowRetryAfter pins the value that the
// retry loop passes to sleepWithContext one line after computing it: never
// negative, never above the cap, and never zero — Discord has no other backoff.
func TestDiscordRateLimitRetryDelayCapsOverflowRetryAfter(t *testing.T) {
	policy := webhookhttp.DefaultPolicy()

	tests := []struct {
		name   string
		header string
		want   time.Duration
	}{
		{name: "overflow boundary", header: "9223372037", want: retrypolicy.MaxRetryAfterDelay},
		{name: "max int64", header: "9223372036854775807", want: retrypolicy.MaxRetryAfterDelay},
		{name: "wraps positive", header: "18446744074", want: retrypolicy.MaxRetryAfterDelay},
		{name: "above cap", header: "120", want: retrypolicy.MaxRetryAfterDelay},
		{name: "below cap", header: "5", want: 5 * time.Second},
		{name: "zero falls back to base delay", header: "0", want: policy.RetryBaseDelay},
		{name: "past date falls back to base delay", header: time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat), want: policy.RetryBaseDelay},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &http.Response{Header: make(http.Header)}
			resp.Header.Set("Retry-After", tt.header)

			got := discordRateLimitRetryDelay(resp, policy)

			if got <= 0 {
				t.Fatalf("discordRateLimitRetryDelay(%q) = %v, want a positive delay", tt.header, got)
			}
			if got != tt.want {
				t.Fatalf("discordRateLimitRetryDelay(%q) = %v, want %v", tt.header, got, tt.want)
			}
		})
	}
}

func TestSendRansomwareEntryMarksClientStatusNonRetryable(t *testing.T) {
	webhookID := "123456789012345678"
	webhookToken := "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_"
	webhookURL := "https://discord.com/api/webhooks/" + webhookID + "/" + webhookToken

	sender := newDiscordTestSender(
		discordRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusNotFound,
				Status:     "404 Not Found",
				Body:       io.NopCloser(strings.NewReader(`{"message":"Unknown Webhook","code":10015}`)),
				Header:     make(http.Header),
				Request:    req,
			}, nil
		}),
	)
	cfg := config.DefaultConfig()

	err := sender.SendRansomwareEntry(context.Background(), webhookURL, api.RansomwareEntry{
		Victim: "Client Error Victim",
	}, config.NotificationFormatOptions(&cfg.Format))
	if err == nil {
		t.Fatal("expected client status error")
	}
	var statusErr *WebhookHTTPError
	if !errors.As(err, &statusErr) {
		t.Fatalf("SendRansomwareEntry() error type = %T, want WebhookHTTPError", err)
	}
	if statusErr.Retryable() {
		t.Fatal("404 Discord webhook error Retryable() = true, want false")
	}
}

func TestSendRansomwareEntryPropagatesCallerContextToRequest(t *testing.T) {
	webhookID := "123456789012345678"
	webhookToken := "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_"
	webhookURL := "https://discord.com/api/webhooks/" + webhookID + "/" + webhookToken
	requestStarted := make(chan struct{})

	sender := newDiscordTestSenderWithClient(&http.Client{
		Timeout: 2 * time.Second,
		Transport: discordRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			close(requestStarted)
			<-req.Context().Done()
			return nil, req.Context().Err()
		}),
	})
	cfg := config.DefaultConfig()

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- sender.SendRansomwareEntry(ctx, webhookURL, api.RansomwareEntry{
			Victim: "Context Victim",
		}, config.NotificationFormatOptions(&cfg.Format))
	}()

	select {
	case <-requestStarted:
	case <-time.After(250 * time.Millisecond):
		cancel()
		t.Fatal("webhook request did not start")
	}

	cancel()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("SendRansomwareEntry returned nil after context cancellation")
		}
		if !strings.Contains(err.Error(), context.Canceled.Error()) {
			t.Fatalf("SendRansomwareEntry error = %v, want context cancellation", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("SendRansomwareEntry did not return promptly after caller context cancellation")
	}
}
