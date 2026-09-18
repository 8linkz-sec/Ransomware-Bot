package slack

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/api"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/config"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/retrypolicy"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/textutil"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/webhookhttp"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

const testSlackWebhookURL = "https://hooks.slack.com/services/T12345678/B12345678/abcdefghijklmnopqrstuvwxyz012345" //nolint:gosec // G101: test fixture, not a real credential

func newSlackTestSender(transport http.RoundTripper) *WebhookSender {
	return newWebhookSenderWithClient(&http.Client{Transport: transport})
}

func sendTestSlackRansomware(ctx context.Context, sender *WebhookSender, webhookURL string) error {
	cfg := config.DefaultConfig()
	return sender.SendRansomwareEntry(ctx, webhookURL, api.RansomwareEntry{
		Group:   "LockBit",
		Victim:  "Example Corp",
		Country: "DE",
	}, config.NotificationFormatOptions(&cfg.Format))
}

func TestRetryAfterDelayCapsSlackHeader(t *testing.T) {
	resp := &http.Response{Header: make(http.Header)}
	resp.Header.Set("Retry-After", "120")

	delay, ok := retryAfterDelay(resp, webhookhttp.DefaultPolicy())
	if !ok {
		t.Fatal("retryAfterDelay() ok = false, want true")
	}
	if delay != 60*time.Second {
		t.Fatalf("retryAfterDelay() = %v, want 60s cap", delay)
	}
}

func TestRetryAfterDelayRejectsMissingOrInvalidSlackHeader(t *testing.T) {
	tests := []string{"", "-1", "not-a-number"}
	for _, value := range tests {
		t.Run(value, func(t *testing.T) {
			resp := &http.Response{Header: make(http.Header)}
			if value != "" {
				resp.Header.Set("Retry-After", value)
			}
			if delay, ok := retryAfterDelay(resp, webhookhttp.Policy{}); ok {
				t.Fatalf("retryAfterDelay() = %v, true; want false", delay)
			}
		})
	}
}

func TestRetryAfterDelayAllowsImmediateRetry(t *testing.T) {
	resp := &http.Response{Header: make(http.Header)}
	resp.Header.Set("Retry-After", "0")

	delay, ok := retryAfterDelay(resp, webhookhttp.DefaultPolicy())
	if !ok {
		t.Fatal("retryAfterDelay() ok = false, want true")
	}
	if delay != 0 {
		t.Fatalf("retryAfterDelay() = %v, want immediate retry", delay)
	}
}

func TestNewWebhookSenderUsesConfiguredPolicy(t *testing.T) {
	sender := NewWebhookSender(webhookhttp.Policy{
		RequestTimeout: 17 * time.Second,
		MaxAttempts:    2,
		RetryBaseDelay: 250 * time.Millisecond,
	})
	if sender.client.Timeout != 17*time.Second {
		t.Fatalf("client timeout = %v, want 17s", sender.client.Timeout)
	}
	if sender.policy.MaxAttempts != 2 {
		t.Fatalf("MaxAttempts = %d, want 2", sender.policy.MaxAttempts)
	}
}

func TestSendPayloadSendsPreformattedSlackPayload(t *testing.T) {
	var gotMethod, gotContentType, gotBody string
	sender := newSlackTestSender(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		gotMethod = req.Method
		gotContentType = req.Header.Get(contentTypeHeader)
		gotBody = string(body)
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Body:       io.NopCloser(strings.NewReader("ok")),
		}, nil
	}))

	err := sender.SendPayload(context.Background(), testSlackWebhookURL, map[string]any{
		"text": "preformatted payload",
	})
	if err != nil {
		t.Fatalf("SendPayload() error = %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Fatalf("method = %q, want POST", gotMethod)
	}
	if gotContentType != jsonContentType {
		t.Fatalf("content type = %q, want %q", gotContentType, jsonContentType)
	}
	if !strings.Contains(gotBody, `"text":"preformatted payload"`) {
		t.Fatalf("request body = %q, want preformatted payload JSON", gotBody)
	}
}

func TestExecuteWebhookRedactsWebhookURLFromTransportError(t *testing.T) {
	webhookURL := testSlackWebhookURL
	sender := newSlackTestSender(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return nil, errors.New("permanent failure posting to " + req.URL.String())
	}))

	err := sendTestSlackRansomware(context.Background(), sender, webhookURL)
	if err == nil {
		t.Fatal("expected transport error")
	}

	got := err.Error()
	if strings.Contains(got, webhookURL) || strings.Contains(got, "T12345678/B12345678") {
		t.Fatalf("error leaked Slack webhook URL: %s", got)
	}
	if !strings.Contains(got, "https://hooks.slack.com/services/[redacted]") {
		t.Fatalf("error did not include redacted Slack marker: %s", got)
	}
}

func TestExecuteWebhookRedactsSlackCompatibleURLFromTransportError(t *testing.T) {
	webhookURL := "https://hooks.eu.example/services/T12345678/B12345678/custom-secret-token"
	sender := newSlackTestSender(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return nil, errors.New("permanent failure posting to " + req.URL.String())
	}))

	err := sendTestSlackRansomware(context.Background(), sender, webhookURL)
	if err == nil {
		t.Fatal("expected transport error")
	}
	got := err.Error()
	for _, leaked := range []string{webhookURL, "custom-secret-token", "T12345678/B12345678"} {
		if strings.Contains(got, leaked) {
			t.Fatalf("error leaked Slack-compatible webhook secret %q: %s", leaked, got)
		}
	}
	if !strings.Contains(got, "https://hooks.eu.example/services/[redacted]") {
		t.Fatalf("error did not include Slack-compatible redaction marker: %s", got)
	}
}

func TestExecuteWebhookRedactsSlackCompatibleURLFromResponseBody(t *testing.T) {
	webhookURL := "https://hooks.eu.example/services/T12345678/B12345678/custom-secret-token"
	sender := newSlackTestSender(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusInternalServerError,
			Status:     "500 Internal Server Error",
			Body:       io.NopCloser(strings.NewReader("failed for " + req.URL.String())),
			Header:     make(http.Header),
		}, nil
	}))

	err := sendTestSlackRansomware(context.Background(), sender, webhookURL)
	if err == nil {
		t.Fatal("expected server error")
	}
	got := err.Error()
	for _, leaked := range []string{webhookURL, "custom-secret-token", "T12345678/B12345678"} {
		if strings.Contains(got, leaked) {
			t.Fatalf("error leaked Slack-compatible response secret %q: %s", leaked, got)
		}
	}
}

func TestExecuteWebhookDoesNotRetryAmbiguousServerError(t *testing.T) {
	requests := 0
	sender := newSlackTestSender(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		return &http.Response{
			StatusCode: http.StatusInternalServerError,
			Status:     "500 Internal Server Error",
			Body:       io.NopCloser(strings.NewReader("server error")),
			Header:     make(http.Header),
		}, nil
	}))

	err := sendTestSlackRansomware(context.Background(), sender, testSlackWebhookURL)
	if err == nil {
		t.Fatal("expected server error")
	}
	var statusErr *webhookhttp.HTTPStatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("SendRansomwareEntry() error type = %T, want HTTPStatusError", err)
	}
	if !statusErr.Retryable() {
		t.Fatal("500 Slack webhook error Retryable() = false, want true")
	}
	if requests != 1 {
		t.Fatalf("request count = %d, want 1", requests)
	}
}

func TestExecuteWebhookRetriesRateLimitWithoutWallClockSleep(t *testing.T) {
	disableSlackRetrySleep(t)

	requests := 0
	sender := newSlackTestSender(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		if requests == 1 {
			header := make(http.Header)
			header.Set("Retry-After", "1")
			return &http.Response{
				StatusCode: http.StatusTooManyRequests,
				Status:     "429 Too Many Requests",
				Body:       io.NopCloser(strings.NewReader("rate limited")),
				Header:     header,
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Body:       io.NopCloser(strings.NewReader("ok")),
			Header:     make(http.Header),
		}, nil
	}))

	err := sendTestSlackRansomware(context.Background(), sender, testSlackWebhookURL)
	if err != nil {
		t.Fatalf("SendRansomwareEntry() error = %v, want retry success", err)
	}
	if requests != 2 {
		t.Fatalf("request count = %d, want 2", requests)
	}
}

func TestExecuteWebhookRetriesRateLimitWithConfiguredFallbackDelay(t *testing.T) {
	disableSlackRetrySleep(t)

	requests := 0
	sender := newWebhookSenderWithClient(
		&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			requests++
			if requests == 1 {
				return &http.Response{
					StatusCode: http.StatusTooManyRequests,
					Status:     "429 Too Many Requests",
					Body:       io.NopCloser(strings.NewReader("rate limited")),
					Header:     make(http.Header),
				}, nil
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Body:       io.NopCloser(strings.NewReader("ok")),
				Header:     make(http.Header),
			}, nil
		})},
		webhookhttp.Policy{MaxAttempts: 2, RetryBaseDelay: 250 * time.Millisecond},
	)

	err := sendTestSlackRansomware(context.Background(), sender, testSlackWebhookURL)
	if err != nil {
		t.Fatalf("SendRansomwareEntry() error = %v, want retry success", err)
	}
	if requests != 2 {
		t.Fatalf("request count = %d, want 2", requests)
	}
}

func TestExecuteWebhookMarksClientStatusNonRetryable(t *testing.T) {
	sender := newSlackTestSender(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusNotFound,
			Status:     "404 Not Found",
			Body:       io.NopCloser(strings.NewReader("no_service")),
			Header:     make(http.Header),
		}, nil
	}))

	err := sendTestSlackRansomware(context.Background(), sender, testSlackWebhookURL)
	if err == nil {
		t.Fatal("expected client status error")
	}
	var statusErr *webhookhttp.HTTPStatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("SendRansomwareEntry() error type = %T, want HTTPStatusError", err)
	}
	if statusErr.Retryable() {
		t.Fatal("404 Slack webhook error Retryable() = true, want false")
	}
}

func TestExecuteWebhookCapsLargeErrorResponseBody(t *testing.T) {
	largeBody := strings.Repeat("x", maxSlackWebhookResponseBytes*2)
	sender := newSlackTestSender(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusInternalServerError,
			Status:     "500 Internal Server Error",
			Body:       io.NopCloser(strings.NewReader(largeBody)),
			Header:     make(http.Header),
		}, nil
	}))

	err := sendTestSlackRansomware(context.Background(), sender, testSlackWebhookURL)
	if err == nil {
		t.Fatal("expected server error")
	}
	got := err.Error()
	if !strings.Contains(got, "[truncated]") {
		t.Fatalf("error missing truncation marker: %s", got)
	}
	if len(got) > maxSlackWebhookResponseBytes+256 {
		t.Fatalf("error length = %d, want bounded response body", len(got))
	}
}

func TestExecuteWebhookDoesNotRetryAmbiguousTransportError(t *testing.T) {
	requests := 0
	sender := newSlackTestSender(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		return nil, errors.New("timeout after request submission")
	}))

	err := sendTestSlackRansomware(context.Background(), sender, testSlackWebhookURL)
	if err == nil {
		t.Fatal("expected transport error")
	}
	if requests != 1 {
		t.Fatalf("request count = %d, want 1", requests)
	}
}

// TestSlackRetryAfterOverflowIsCapped drives the real 429 retry loop with an
// overflowing Retry-After header. The recorder also receives the jittered
// waitBeforeSlackRetry backoff, so only the negative-free property and the
// number of capped entries are asserted, never the order.
func TestSlackRetryAfterOverflowIsCapped(t *testing.T) {
	var mu sync.Mutex
	var recorded []time.Duration

	previous := slackRetrySleep
	slackRetrySleep = func(ctx context.Context, delay time.Duration) error {
		mu.Lock()
		recorded = append(recorded, delay)
		mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}
	t.Cleanup(func() { slackRetrySleep = previous })

	sender := newSlackTestSender(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		header := make(http.Header)
		header.Set("Retry-After", "9223372037")
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Header:     header,
			Body:       io.NopCloser(strings.NewReader("rate limited")),
		}, nil
	}))

	if err := sendTestSlackRansomware(context.Background(), sender, testSlackWebhookURL); err == nil {
		t.Fatal("SendRansomwareEntry() error = nil, want rate-limit failure")
	}

	mu.Lock()
	delays := append([]time.Duration(nil), recorded...)
	mu.Unlock()

	capped := 0
	for _, delay := range delays {
		if delay < 0 {
			t.Fatalf("slackRetrySleep argument %v is negative (all args: %v)", delay, delays)
		}
		if delay == retrypolicy.MaxRetryAfterDelay {
			capped++
		}
	}
	if capped != retrypolicy.MaxAttempts-1 {
		t.Fatalf("capped Retry-After sleeps = %d (all args: %v), want %d", capped, delays, retrypolicy.MaxAttempts-1)
	}
}

func disableSlackRetrySleep(t *testing.T) {
	t.Helper()
	previous := slackRetrySleep
	slackRetrySleep = func(ctx context.Context, _ time.Duration) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}
	t.Cleanup(func() { slackRetrySleep = previous })
}

func TestExecuteWebhookTransportErrorPreservesCause(t *testing.T) {
	tests := []struct {
		name          string
		cause         error
		wantRetryable bool
		wantCanceled  bool
		wantDeadline  bool
		wantOpError   bool
	}{
		{name: "context canceled", cause: context.Canceled, wantRetryable: false, wantCanceled: true},
		{name: "context deadline exceeded", cause: context.DeadlineExceeded, wantRetryable: true, wantDeadline: true},
		{
			name:          "connection refused",
			cause:         &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED},
			wantRetryable: true,
			wantOpError:   true,
		},
		{
			name:          "connection reset",
			cause:         &net.OpError{Op: "read", Err: syscall.ECONNRESET},
			wantRetryable: true,
			wantOpError:   true,
		},
		{
			name:          "dns no such host",
			cause:         &net.DNSError{Err: "no such host", IsNotFound: true},
			wantRetryable: true,
		},
		{
			name:          "tls unknown authority",
			cause:         &x509.UnknownAuthorityError{},
			wantRetryable: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sender := newSlackTestSender(roundTripFunc(func(*http.Request) (*http.Response, error) {
				return nil, tc.cause
			}))

			err := sendTestSlackRansomware(context.Background(), sender, testSlackWebhookURL)
			if err == nil {
				t.Fatal("expected transport error")
			}

			var transportErr *webhookhttp.TransportError
			if !errors.As(err, &transportErr) {
				t.Fatalf("errors.As(*webhookhttp.TransportError) = false for %v", err)
			}
			if transportErr.Provider != "slack" {
				t.Fatalf("TransportError.Provider = %q, want %q", transportErr.Provider, "slack")
			}
			if got := transportErr.Retryable(); got != tc.wantRetryable {
				t.Fatalf("TransportError.Retryable() = %t, want %t", got, tc.wantRetryable)
			}
			if got := errors.Is(err, context.Canceled); got != tc.wantCanceled {
				t.Fatalf("errors.Is(err, context.Canceled) = %t, want %t", got, tc.wantCanceled)
			}
			if got := errors.Is(err, context.DeadlineExceeded); got != tc.wantDeadline {
				t.Fatalf("errors.Is(err, context.DeadlineExceeded) = %t, want %t", got, tc.wantDeadline)
			}
			var opErr *net.OpError
			if got := errors.As(err, &opErr); got != tc.wantOpError {
				t.Fatalf("errors.As(err, *net.OpError) = %t, want %t", got, tc.wantOpError)
			}
		})
	}
}

func TestExecuteWebhookTransportErrorKeepsRedactedMessageVerbatim(t *testing.T) {
	webhookURL := "https://hooks.eu.example/services/T12345678/B12345678/custom-secret-token"
	sender := newSlackTestSender(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return nil, errors.New("permanent failure posting to " + req.URL.String())
	}))

	err := sendTestSlackRansomware(context.Background(), sender, webhookURL)
	if err == nil {
		t.Fatal("expected transport error")
	}

	var urlErr *url.Error
	if !errors.As(err, &urlErr) {
		t.Fatalf("errors.As(err, *url.Error) = false, cause is not reachable: %v", err)
	}
	want := textutil.RedactWebhookSecretsForURL(urlErr.Error(), webhookURL)

	const marker = "slack webhook transport error: "
	got := err.Error()
	index := strings.Index(got, marker)
	if index < 0 {
		t.Fatalf("error %q does not contain marker %q", got, marker)
	}
	if tail := got[index+len(marker):]; tail != want {
		t.Fatalf("redacted transport message = %q, want %q", tail, want)
	}
}

func TestExecuteWebhookTransportErrorNeverPrintsTheToken(t *testing.T) {
	webhookURL := "https://hooks.eu.example/services/T12345678/B12345678/custom-secret-token"
	sender := newSlackTestSender(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return nil, errors.New("permanent failure posting to " + req.URL.String())
	}))

	err := sendTestSlackRansomware(context.Background(), sender, webhookURL)
	if err == nil {
		t.Fatal("expected transport error")
	}

	rendered := map[string]string{
		"Error()": err.Error(),
		"%v":      fmt.Sprintf("%v", err),
		"%+v":     fmt.Sprintf("%+v", err),
	}
	for form, text := range rendered {
		for _, leaked := range []string{webhookURL, "custom-secret-token", "T12345678/B12345678"} {
			if strings.Contains(text, leaked) {
				t.Fatalf("%s leaked Slack webhook secret %q: %s", form, leaked, text)
			}
		}
		if !strings.Contains(text, "https://hooks.eu.example/services/[redacted]") {
			t.Fatalf("%s did not include the redaction marker: %s", form, text)
		}
	}

	// The raw cause stays reachable through the chain and DOES carry the token.
	// That is acceptable only because no production consumer unwraps and prints
	// a delivery error: every log site uses webhookFailureFields, which renders
	// deliveryStatusMessage, and every persisted field is redacted again on write.
	// Deleting these assertions is the tripwire for that guarantee.
	var urlErr *url.Error
	if !errors.As(err, &urlErr) {
		t.Fatalf("errors.As(err, *url.Error) = false, raw cause is not reachable: %v", err)
	}
	if !strings.Contains(urlErr.Error(), "custom-secret-token") {
		t.Fatalf("unwrapped cause = %q, want the raw webhook token", urlErr.Error())
	}
	unwrappedLeak := false
	for cause := errors.Unwrap(err); cause != nil; cause = errors.Unwrap(cause) {
		if strings.Contains(cause.Error(), "custom-secret-token") {
			unwrappedLeak = true
			break
		}
	}
	if !unwrappedLeak {
		t.Fatal("no link of the unwrapped chain carries the raw token; the cause was flattened")
	}
}
