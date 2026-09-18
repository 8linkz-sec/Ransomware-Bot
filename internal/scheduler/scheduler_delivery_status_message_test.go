package scheduler

import (
	"context"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/webhookhttp"
)

// TestDeliveryStatusMessageTransportErrorOrdering pins the ordering fix in
// internal/scheduler/scheduler.go's deliveryStatusMessage (see also
// scheduler_slack_cancel_test.go's TestSlackTLSFailureDeadLettersOnFirstAttempt):
// *url.Error -- what http.Client.Do returns on every transport failure,
// including a permanent TLS/x509 one -- always satisfies Go's net.Error
// interface regardless of the underlying cause (its Timeout/Temporary
// methods exist unconditionally; what varies is only what they report once
// called), so the generic net.Error arm matched before the more specific
// *webhookhttp.TransportError arm and reported every transport failure,
// retryable or not, as "provider unavailable ... retry scheduled". The
// TransportError arm must now be checked first, and a NON-retryable
// TransportError (an untrusted certificate, for example) must be named as a
// terminal failure instead of a transient one.
//
// The "TLS handshake timeout" case pins the review follow-up to that same
// finding: every client.Do error is wrapped in a TransportError, so the new
// arm must also single out a net.Error whose Timeout() is true and report it
// as a timeout, not fall through to the generic "provider unavailable" --
// webhookhttp.NewTransport hard-codes a 10s TLSHandshakeTimeout while
// webhook_request_timeout (which governs http.Client.Timeout) is operator-
// configurable from 1s to 5m, so any configured value above 10s makes the
// TLS-handshake timeout the one that actually fires for a host that accepts
// TCP but stalls the handshake. Measured, same input:
//
//	NEW (pre-fix):  TransportError(url.Error(net.OpError{timeout})) => "slack webhook provider unavailable"
//	OLD:            TransportError(url.Error(net.OpError{timeout})) => "Webhook delivery timed out; provider or network did not respond before the deadline"
//
// The pre-existing timeout assertions in
// TestDeliveryStatusMessageContextAndNetworkErrors use a bare fakeNetError
// that is never wrapped in a TransportError, so they only ever reached the
// generic net.Error arm below and could not catch this.
func TestDeliveryStatusMessageTransportErrorOrdering(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "retryable transport error (dial failure)",
			err: webhookhttp.NewTransportError("slack", &url.Error{
				Op:  "Post",
				URL: "https://hooks.example/redacted",
				Err: &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")},
			}, "redacted"),
			want: "slack webhook provider unavailable",
		},
		{
			name: "non-retryable transport error (x509 unknown authority)",
			err: webhookhttp.NewTransportError("slack", &url.Error{
				Op:  "Post",
				URL: "https://hooks.example/redacted",
				Err: &x509.UnknownAuthorityError{},
			}, "redacted"),
			want: "slack webhook connection failed permanently; verify the certificate and network path",
		},
		{
			name: "retryable transport error carrying a net.Error timeout (TLS handshake timeout, for example)",
			err: webhookhttp.NewTransportError("slack", &url.Error{
				Op:  "Post",
				URL: "https://hooks.example/redacted",
				Err: &net.OpError{Op: "dial", Net: "tcp", Err: &fakeNetError{msg: "TLS handshake timeout", timeout: true}},
			}, "redacted"),
			want: "Webhook delivery timed out; provider or network did not respond before the deadline",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := deliveryStatusMessage("slack", tc.err); got != tc.want {
				t.Fatalf("deliveryStatusMessage() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestDeliveryStatusMessageNeverPromisesARetry pins the fix:
// deliveryStatusMessage/deliveryStatusMessageForHTTPStatus used to
// append "; retry scheduled" from error classification alone, before the
// tracker's own retry-window/max-attempts decision runs -- an item can still
// dead-letter on THIS attempt even though the attempt's own error looked
// retryable (a transient 503 that lands on an already-exhausted retry
// window), and the persisted last_error kept the stale promise because
// nothing downstream ever corrects it. The classification text must
// therefore never predict a future retry.
//
// Finding F-C (2026-09-04 review round): asserting exact "want" strings for
// only four arms left deliveryStatusMessage's own guarantee at 100%
// statement coverage while two branches -- the 401/403/404 "rejected
// delivery" arm and the 400/payload "rejected the message payload" arm, both
// in the untyped text-fallback switch -- were reachable only by substring,
// never asserted not to carry the suffix. Re-adding "; retry scheduled" to
// either one still passed every test. The corpus below now reaches every
// arm deliveryStatusMessage can return through (context cancellation and
// deadline, every deliveryStatusMessageForHTTPStatus case via a typed
// HTTPStatusError, both TransportError outcomes, both generic net.Error
// outcomes, and every case of the untyped text-classification fallback,
// including the two previously-uncovered ones), and the loop below checks
// every case for the "retry"/"scheduled" wording structurally -- the way
// deadLetterWarnReasonPhrase's neutral default already does for the
// dead-letter WARN side of this same guarantee -- instead of relying on
// exact-match assertions to happen to catch it.
func TestDeliveryStatusMessageNeverPromisesARetry(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "canceled",
			err:  context.Canceled,
			want: "Webhook delivery canceled",
		},
		{
			name: "deadline exceeded",
			err:  context.DeadlineExceeded,
			want: "Webhook delivery timed out; provider or network did not respond before the deadline",
		},
		{
			name: "429 via typed HTTPStatusError",
			err:  webhookhttp.NewHTTPStatusError("discord", http.StatusTooManyRequests, "429 Too Many Requests", "", nil),
			want: "discord webhook rate limited delivery",
		},
		{
			name: "401 via typed HTTPStatusError",
			err:  webhookhttp.NewHTTPStatusError("discord", http.StatusUnauthorized, "401 Unauthorized", "", nil),
			want: "discord webhook rejected delivery; verify or rotate the webhook URL",
		},
		{
			name: "403 via typed HTTPStatusError",
			err:  webhookhttp.NewHTTPStatusError("discord", http.StatusForbidden, "403 Forbidden", "", nil),
			want: "discord webhook rejected delivery; verify or rotate the webhook URL",
		},
		{
			name: "404 via typed HTTPStatusError",
			err:  webhookhttp.NewHTTPStatusError("discord", http.StatusNotFound, "404 Not Found", "", nil),
			want: "discord webhook rejected delivery; verify or rotate the webhook URL",
		},
		{
			name: "400 via typed HTTPStatusError",
			err:  webhookhttp.NewHTTPStatusError("discord", http.StatusBadRequest, "400 Bad Request", "", nil),
			want: "discord webhook rejected the message payload; check formatting configuration",
		},
		{
			name: "other 4xx via typed HTTPStatusError",
			err:  webhookhttp.NewHTTPStatusError("discord", http.StatusPaymentRequired, "402 Payment Required", "", nil),
			want: "discord webhook rejected the message payload; check formatting configuration",
		},
		{
			name: "5xx via typed HTTPStatusError",
			err:  webhookhttp.NewHTTPStatusError("discord", http.StatusServiceUnavailable, "503 Service Unavailable", "", nil),
			want: "discord webhook provider unavailable",
		},
		{
			name: "default status code via typed HTTPStatusError",
			err:  webhookhttp.NewHTTPStatusError("discord", http.StatusFound, "302 Found", "", nil),
			want: "discord webhook delivery failed; check webhook configuration and provider availability",
		},
		{
			name: "non-retryable transport error",
			err: webhookhttp.NewTransportError("discord", &url.Error{
				Op:  "Post",
				URL: "https://hooks.example/redacted",
				Err: &x509.UnknownAuthorityError{},
			}, "redacted"),
			want: "discord webhook connection failed permanently; verify the certificate and network path",
		},
		{
			name: "retryable transport error, non-timeout",
			err: webhookhttp.NewTransportError("discord", &url.Error{
				Op:  "Post",
				URL: "https://hooks.example/redacted",
				Err: &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")},
			}, "redacted"),
			want: "discord webhook provider unavailable",
		},
		{
			name: "retryable transport error, timeout",
			err: webhookhttp.NewTransportError("discord", &url.Error{
				Op:  "Post",
				URL: "https://hooks.example/redacted",
				Err: &net.OpError{Op: "dial", Net: "tcp", Err: &fakeNetError{msg: "TLS handshake timeout", timeout: true}},
			}, "redacted"),
			want: "Webhook delivery timed out; provider or network did not respond before the deadline",
		},
		{
			name: "generic net.Error, timeout",
			err:  &fakeNetError{msg: "i/o timeout", timeout: true},
			want: "Webhook delivery timed out; provider or network did not respond before the deadline",
		},
		{
			name: "generic net.Error, non-timeout",
			err:  &fakeNetError{msg: "connection refused"},
			want: "discord webhook provider unavailable",
		},
		{
			name: "401/403/404 via untyped error text fallback",
			err:  errors.New("webhook http 401: unauthorized"),
			want: "discord webhook rejected delivery; verify or rotate the webhook URL",
		},
		{
			name: "400/payload via untyped error text fallback",
			err:  errors.New("invalid payload: cannot send an empty message"),
			want: "discord webhook rejected the message payload; check formatting configuration",
		},
		{
			name: "rate limit via untyped error text fallback",
			err:  errors.New("rate limit exceeded"),
			want: "discord webhook rate limited delivery",
		},
		{
			name: "5xx via untyped error text fallback",
			err:  errors.New("server returned status 502"),
			want: "discord webhook provider unavailable",
		},
		{
			name: "default via untyped error text fallback",
			err:  errors.New("unexpected response shape"),
			want: "discord webhook delivery failed; check webhook configuration and provider availability",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := deliveryStatusMessage("discord", tc.err)
			if got != tc.want {
				t.Fatalf("deliveryStatusMessage() = %q, want %q", got, tc.want)
			}
			lower := strings.ToLower(got)
			if strings.Contains(lower, "retry") || strings.Contains(lower, "scheduled") {
				t.Fatalf("deliveryStatusMessage() = %q, must never promise a retry ('retry'/'scheduled' found)", got)
			}
		})
	}
}

// TestDeliveryStatusMessageForHTTPStatusNeverPromisesARetry is the direct
// twin for deliveryStatusMessageForHTTPStatus, reached from
// deliveryStatusMessage's *webhookhttp.HTTPStatusError arm and used
// verbatim for the persisted last_error / dead-letter WARN field.
func TestDeliveryStatusMessageForHTTPStatusNeverPromisesARetry(t *testing.T) {
	tests := map[int]string{
		http.StatusTooManyRequests:     "slack webhook rate limited delivery",
		http.StatusInternalServerError: "slack webhook provider unavailable",
	}
	for statusCode, want := range tests {
		if got := deliveryStatusMessageForHTTPStatus("slack", statusCode); got != want {
			t.Fatalf("deliveryStatusMessageForHTTPStatus(%d) = %q, want %q", statusCode, got, want)
		}
	}
}
