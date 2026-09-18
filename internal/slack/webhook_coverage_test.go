package slack

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/rss"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/webhookhttp"

	logtest "github.com/sirupsen/logrus/hooks/test"
)

type slackBrokenReader struct{}

func (slackBrokenReader) Read([]byte) (int, error) {
	return 0, errors.New("read failure")
}

func TestCloseHandlesNilAndActiveClient(t *testing.T) {
	if err := (&WebhookSender{}).Close(); err != nil {
		t.Fatalf("Close(nil client) error = %v", err)
	}
	if err := NewWebhookSender().Close(); err != nil {
		t.Fatalf("Close(active client) error = %v", err)
	}
}

func TestSendPayloadRejectsCancelledContext(t *testing.T) {
	sender := newSlackTestSender(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		t.Error("unexpected request for cancelled context")
		return nil, errors.New("unexpected")
	}))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := sender.SendPayload(ctx, testSlackWebhookURL, map[string]any{"text": "hello"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("SendPayload(cancelled ctx) error = %v, want context.Canceled", err)
	}
}

func TestSendRSSEntrySendsFormattedMessage(t *testing.T) {
	var body string
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("ReadAll(body) error = %v", err)
		}
		body = string(raw)
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()

	sender := NewWebhookSender()
	err := sender.SendRSSEntry(context.Background(), server.URL, rss.Entry{
		Title:     "RSS Slack Alert",
		Link:      "https://example.test/rss-slack-alert",
		FeedTitle: "Example Feed",
		FeedURL:   "https://example.test/feed.xml",
	}, "general", nil)
	if err != nil {
		t.Fatalf("SendRSSEntry() error = %v", err)
	}
	if requests != 1 {
		t.Fatalf("request count = %d, want 1", requests)
	}
	if !strings.Contains(body, "RSS Slack Alert") {
		t.Fatalf("RSS webhook body missing title: %s", body)
	}
}

func TestSendRSSEntryWrapsWebhookFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no_service", http.StatusNotFound)
	}))
	defer server.Close()

	sender := NewWebhookSender()
	err := sender.SendRSSEntry(context.Background(), server.URL, rss.Entry{
		Title: "RSS Slack Alert",
	}, "general", nil)
	if err == nil || !strings.Contains(err.Error(), "failed to send RSS entry") {
		t.Fatalf("SendRSSEntry() error = %v, want wrapped send failure", err)
	}
}

func TestSendPayloadRejectsUnmarshalablePayload(t *testing.T) {
	sender := newSlackTestSender(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		t.Error("unexpected request for unmarshalable payload")
		return nil, errors.New("unexpected")
	}))

	err := sender.SendPayload(context.Background(), testSlackWebhookURL, func() {})
	if err == nil || !strings.Contains(err.Error(), "failed to marshal payload") {
		t.Fatalf("SendPayload(func payload) error = %v, want marshal failure", err)
	}
}

func TestSendPayloadRejectsInvalidWebhookURL(t *testing.T) {
	sender := newSlackTestSender(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		t.Error("unexpected request for invalid webhook URL")
		return nil, errors.New("unexpected")
	}))

	err := sender.SendPayload(context.Background(), "http://bad\nurl", map[string]any{"text": "hello"})
	if err == nil || !strings.Contains(err.Error(), "failed to create request") {
		t.Fatalf("SendPayload(invalid URL) error = %v, want request creation failure", err)
	}
}

func TestExecuteWebhookAbortsWhenRetrySleepFails(t *testing.T) {
	sleepErr := errors.New("sleep interrupted")
	previous := slackRetrySleep
	slackRetrySleep = func(ctx context.Context, _ time.Duration) error {
		return sleepErr
	}
	t.Cleanup(func() { slackRetrySleep = previous })

	requests := 0
	sender := newSlackTestSender(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		header := make(http.Header)
		header.Set("Retry-After", "1")
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Status:     "429 Too Many Requests",
			Body:       io.NopCloser(strings.NewReader("rate limited")),
			Header:     header,
		}, nil
	}))

	err := sendTestSlackRansomware(context.Background(), sender, testSlackWebhookURL)
	if !errors.Is(err, sleepErr) {
		t.Fatalf("SendRansomwareEntry() error = %v, want retry sleep failure", err)
	}
	if requests != 1 {
		t.Fatalf("request count = %d, want 1 (no retry after sleep failure)", requests)
	}
}

func TestExecuteWebhookStopsWhenContextCancelledBeforeRetryAttempt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	requests := 0
	sender := newSlackTestSender(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		cancel()
		header := make(http.Header)
		header.Set("Retry-After", "0")
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Status:     "429 Too Many Requests",
			Body:       io.NopCloser(strings.NewReader("rate limited")),
			Header:     header,
		}, nil
	}))

	err := sendTestSlackRansomware(ctx, sender, testSlackWebhookURL)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("SendRansomwareEntry() error = %v, want context.Canceled", err)
	}
	if requests != 1 {
		t.Fatalf("request count = %d, want 1 (no retry after cancellation)", requests)
	}
}

func TestHandleSlackWebhookResponseWarnsOnBodyReadFailure(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Body:       io.NopCloser(slackBrokenReader{}),
		Header:     make(http.Header),
	}

	outcome := handleSlackWebhookResponse(resp, testSlackWebhookURL, 5, 0, webhookhttp.DefaultPolicy())
	if outcome.err != nil {
		t.Fatalf("handleSlackWebhookResponse(200, broken body) err = %v, want success", outcome.err)
	}
	if outcome.retry {
		t.Fatal("handleSlackWebhookResponse(200, broken body) retry = true, want false")
	}
}

// Finding 5 (internal/slack/webhook.go): only exactly HTTP 200 counted as a
// successful Slack-compatible delivery. Real Slack answers 200 on success,
// but slack_compatible_webhook_hosts is an arbitrary,
// operator-populated hostname allow-list, and some self-hosted receivers
// answer 201/204 for an accepted delivery. Operator decision 2026-09-03:
// accept 200, 201 and 204 as success; 202 stays a failure ("accepted,
// processing follows" can still fail afterwards, and treating it as success
// would let a proxy that never forwards be recorded as delivered with no
// retry and no dead letter).
func TestHandleSlackWebhookResponseTreats201And204AsSuccess(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
	}{
		{"201 Created", http.StatusCreated},
		{"204 No Content", http.StatusNoContent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &http.Response{
				StatusCode: tt.statusCode,
				Status:     tt.name,
				Body:       io.NopCloser(strings.NewReader("")),
				Header:     make(http.Header),
			}

			outcome := handleSlackWebhookResponse(resp, testSlackWebhookURL, 5, 0, webhookhttp.DefaultPolicy())
			if outcome.err != nil {
				t.Fatalf("handleSlackWebhookResponse(%d) err = %v, want success", tt.statusCode, outcome.err)
			}
			if outcome.retry {
				t.Fatalf("handleSlackWebhookResponse(%d) retry = true, want false", tt.statusCode)
			}
		})
	}
}

func TestHandleSlackWebhookResponseStillTreats202AsFailure(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusAccepted,
		Status:     "202 Accepted",
		Body:       io.NopCloser(strings.NewReader("")),
		Header:     make(http.Header),
	}

	outcome := handleSlackWebhookResponse(resp, testSlackWebhookURL, 5, 0, webhookhttp.DefaultPolicy())
	if outcome.err == nil {
		t.Fatal("handleSlackWebhookResponse(202) err = nil, want failure -- 202 (accepted, processing follows) must stay a failure")
	}
}

func TestHandleSlackWebhookResponseLogsInfoOnNon200Success(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()

	for _, statusCode := range []int{http.StatusCreated, http.StatusNoContent} {
		resp := &http.Response{
			StatusCode: statusCode,
			Status:     http.StatusText(statusCode),
			Body:       io.NopCloser(strings.NewReader("")),
			Header:     make(http.Header),
		}
		handleSlackWebhookResponse(resp, testSlackWebhookURL, 5, 0, webhookhttp.DefaultPolicy())
	}

	infoLogs := 0
	for _, entry := range hook.AllEntries() {
		if entry.Message != "Slack-compatible webhook returned a non-200 success status" {
			continue
		}
		infoLogs++
		statusCode, _ := entry.Data["status_code"].(int)
		if statusCode != http.StatusCreated && statusCode != http.StatusNoContent {
			t.Fatalf("INFO log status_code = %v, want 201 or 204", entry.Data["status_code"])
		}
		if host, _ := entry.Data["host"].(string); host != "hooks.slack.com" {
			t.Fatalf("INFO log host = %v, want hooks.slack.com", entry.Data["host"])
		}
	}
	if infoLogs != 2 {
		t.Fatalf("INFO log count = %d, want 2 (one per non-200 success)", infoLogs)
	}
}

// 200 stays logged only at Debug (no behaviour change for the documented,
// exact-200 case).
func TestHandleSlackWebhookResponseDoesNotLogInfoOn200(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Body:       io.NopCloser(strings.NewReader("ok")),
		Header:     make(http.Header),
	}
	handleSlackWebhookResponse(resp, testSlackWebhookURL, 5, 0, webhookhttp.DefaultPolicy())

	for _, entry := range hook.AllEntries() {
		if entry.Message == "Slack-compatible webhook returned a non-200 success status" {
			t.Fatalf("unexpected non-200-success INFO log for a plain 200 response: %v", entry.Data)
		}
	}
}

func TestSlackWebhookHostReturnsEmptyForUnparsableURL(t *testing.T) {
	if got := slackWebhookHost("http://exa\nmple.com"); got != "" {
		t.Fatalf("slackWebhookHost(unparsable URL) = %q, want empty", got)
	}
}

func TestRetryAfterDelayNilResponse(t *testing.T) {
	if delay, ok := retryAfterDelay(nil, webhookhttp.DefaultPolicy()); ok || delay != 0 {
		t.Fatalf("retryAfterDelay(nil) = %v, %v; want 0, false", delay, ok)
	}
}

func TestSleepWithContextBehavior(t *testing.T) {
	if err := sleepWithContext(context.Background(), 0); err != nil {
		t.Fatalf("sleepWithContext(zero delay) error = %v, want nil", err)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleepWithContext(cancelled, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("sleepWithContext(cancelled ctx) error = %v, want context.Canceled", err)
	}

	if err := sleepWithContext(context.Background(), time.Millisecond); err != nil {
		t.Fatalf("sleepWithContext(short delay) error = %v, want nil", err)
	}
}
