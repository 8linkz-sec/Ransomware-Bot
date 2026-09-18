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
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/webhookhttp"
)

type discordBrokenReader struct{}

func (discordBrokenReader) Read([]byte) (int, error) {
	return 0, errors.New("read failure")
}

func TestCloseReleasesIdleConnections(t *testing.T) {
	sender, err := NewWebhookSender()
	if err != nil {
		t.Fatalf("NewWebhookSender() error = %v", err)
	}
	if err := sender.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestExecuteWebhookRejectsCancelledContext(t *testing.T) {
	sender := newDiscordTestSender(discordRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		t.Error("unexpected request for cancelled context")
		return discordNoContentResponse(req), nil
	}))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := sender.executeWebhook(ctx, "123456789012345678", "token", &WebhookParams{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("executeWebhook(cancelled ctx) error = %v, want context.Canceled", err)
	}
}

func TestExecuteWebhookRejectsInvalidEndpointCharacters(t *testing.T) {
	sender := newDiscordTestSender(discordRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		t.Error("unexpected request for invalid endpoint")
		return discordNoContentResponse(req), nil
	}))

	err := sender.executeWebhook(context.Background(), "bad\nid", "token", &WebhookParams{})
	if err == nil || !strings.Contains(err.Error(), "failed to create request") {
		t.Fatalf("executeWebhook(invalid endpoint) error = %v, want request creation failure", err)
	}
}

func TestExecuteWebhookRateLimitAbortsWhenContextCancelledDuringSleep(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	requests := 0
	sender := newDiscordTestSenderWithClient(
		&http.Client{Transport: discordRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			requests++
			cancel()
			header := make(http.Header)
			header.Set("Retry-After", "5")
			return &http.Response{
				StatusCode: http.StatusTooManyRequests,
				Status:     "429 Too Many Requests",
				Body:       io.NopCloser(strings.NewReader("rate limited")),
				Header:     header,
			}, nil
		})},
		webhookhttp.Policy{MaxAttempts: 3, RetryBaseDelay: time.Millisecond},
	)

	err := sender.SendRansomwareEntry(ctx, discordValidWebhookURL(), api.RansomwareEntry{
		Group:  "LockBit",
		Victim: "Example Corp",
	}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("SendRansomwareEntry() error = %v, want context.Canceled", err)
	}
	if requests != 1 {
		t.Fatalf("request count = %d, want 1 (no retry after cancellation)", requests)
	}
}

func TestExecuteWebhookRateLimitWaitsBeforeRetry(t *testing.T) {
	requests := 0
	sender := newDiscordTestSenderWithClient(
		&http.Client{Transport: discordRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			requests++
			if requests == 1 {
				return &http.Response{
					StatusCode: http.StatusTooManyRequests,
					Status:     "429 Too Many Requests",
					Body:       io.NopCloser(strings.NewReader("rate limited")),
					Header:     make(http.Header),
				}, nil
			}
			return discordNoContentResponse(req), nil
		})},
		webhookhttp.Policy{MaxAttempts: 2, RetryBaseDelay: time.Millisecond},
	)

	err := sender.SendRansomwareEntry(context.Background(), discordValidWebhookURL(), api.RansomwareEntry{
		Group:  "LockBit",
		Victim: "Example Corp",
	}, nil)
	if err != nil {
		t.Fatalf("SendRansomwareEntry() error = %v, want retry success", err)
	}
	if requests != 2 {
		t.Fatalf("request count = %d, want 2", requests)
	}
}

func TestHandleDiscordWebhookResponseWarnsOnBodyReadFailure(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusInternalServerError,
		Status:     "500 Internal Server Error",
		Body:       io.NopCloser(discordBrokenReader{}),
		Header:     make(http.Header),
	}

	retry, err := handleDiscordWebhookResponse(resp, 10, 0, webhookhttp.DefaultPolicy())
	if retry {
		t.Fatal("handleDiscordWebhookResponse() retry = true, want false for server error")
	}
	var statusErr *webhookhttp.HTTPStatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("handleDiscordWebhookResponse() error type = %T, want HTTPStatusError", err)
	}
	if statusErr.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status code = %d, want 500", statusErr.StatusCode)
	}
}

func TestRetrypolicyDelayFromHeaderNilResponse(t *testing.T) {
	if delay, ok := retrypolicyDelayFromHeader(nil); ok || delay != 0 {
		t.Fatalf("retrypolicyDelayFromHeader(nil) = %v, %v; want 0, false", delay, ok)
	}
}

func TestReadDiscordWebhookResponseBodyTruncatesLargeBodies(t *testing.T) {
	body := strings.Repeat("a", maxDiscordWebhookResponseBytes+100)

	got, err := readDiscordWebhookResponseBody(strings.NewReader(body))
	if err != nil {
		t.Fatalf("readDiscordWebhookResponseBody() error = %v", err)
	}
	if !strings.HasSuffix(got, "...[truncated]") {
		t.Fatalf("truncated body missing marker, got tail %q", got[len(got)-32:])
	}
	if len(got) != maxDiscordWebhookResponseBytes+len("...[truncated]") {
		t.Fatalf("truncated body length = %d, want %d", len(got), maxDiscordWebhookResponseBytes+len("...[truncated]"))
	}
}
