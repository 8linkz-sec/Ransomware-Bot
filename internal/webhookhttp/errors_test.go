package webhookhttp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"testing"
)

func TestHTTPStatusErrorRetryableUsesHTTPStatusPolicy(t *testing.T) {
	retryable := NewHTTPStatusError("slack", http.StatusServiceUnavailable, "503 Service Unavailable", "", nil)
	if !retryable.Retryable() {
		t.Fatal("503 webhook error Retryable() = false, want true")
	}
	if !IsRetryableError(retryable) {
		t.Fatal("IsRetryableError(503) = false, want true")
	}

	permanent := NewHTTPStatusError("discord", http.StatusForbidden, "403 Forbidden", "", nil)
	if permanent.Retryable() {
		t.Fatal("403 webhook error Retryable() = true, want false")
	}
	if IsRetryableError(permanent) {
		t.Fatal("IsRetryableError(403) = true, want false")
	}
}

func TestPermanentErrorIsNotRetryableThroughWrapping(t *testing.T) {
	err := errors.Join(NewPermanentError(errors.New("invalid webhook URL")))
	if IsRetryableError(err) {
		t.Fatal("IsRetryableError(permanent) = true, want false")
	}
}

func TestUnclassifiedErrorDefaultsRetryable(t *testing.T) {
	if !IsRetryableError(errors.New("temporary transport failure")) {
		t.Fatal("IsRetryableError(unclassified) = false, want true")
	}
}

type webhookHTTPTestNetError struct {
	message   string
	timeout   bool
	temporary bool
}

func (e webhookHTTPTestNetError) Error() string {
	return e.message
}

func (e webhookHTTPTestNetError) Timeout() bool {
	return e.timeout
}

func (e webhookHTTPTestNetError) Temporary() bool {
	return e.temporary
}

func TestTransportErrorRetryableUsesNetErrorInsteadOfMessageText(t *testing.T) {
	retryable := NewTransportError(
		"discord",
		webhookHTTPTestNetError{message: "request timed out", timeout: true},
		"request timed out",
	)
	if !IsRetryableError(retryable) {
		t.Fatal("IsRetryableError(timeout transport error) = false, want true")
	}

	permanent := NewTransportError(
		"discord",
		errors.New("provider response body mentioned timeout and 500"),
		"provider response body mentioned timeout and 500",
	)
	if IsRetryableError(permanent) {
		t.Fatal("IsRetryableError(plain transport error with retry-looking text) = true, want false")
	}
}

func TestPermanentErrorAccessors(t *testing.T) {
	var nilErr *PermanentError
	if got := nilErr.Error(); got != "" {
		t.Fatalf("nil PermanentError Error() = %q, want empty", got)
	}
	if nilErr.Unwrap() != nil {
		t.Fatal("nil PermanentError Unwrap() != nil")
	}

	empty := &PermanentError{}
	if got := empty.Error(); got != "" {
		t.Fatalf("empty PermanentError Error() = %q, want empty", got)
	}

	inner := errors.New("invalid webhook URL")
	wrapped := NewPermanentError(inner)
	if got := wrapped.Error(); got != "invalid webhook URL" {
		t.Fatalf("PermanentError Error() = %q, want wrapped message", got)
	}
	if !errors.Is(wrapped, inner) {
		t.Fatal("PermanentError does not unwrap to inner error")
	}
}

func TestNewPermanentErrorNilReturnsNil(t *testing.T) {
	if err := NewPermanentError(nil); err != nil {
		t.Fatalf("NewPermanentError(nil) = %v, want nil", err)
	}
}

func TestNewTransportErrorNilReturnsNil(t *testing.T) {
	if err := NewTransportError("discord", nil, "ignored"); err != nil {
		t.Fatalf("NewTransportError(nil) = %v, want nil", err)
	}
}

func TestTransportErrorErrorFormatting(t *testing.T) {
	tests := []struct {
		name string
		err  *TransportError
		want string
	}{
		{
			name: "nil receiver",
			err:  nil,
			want: "",
		},
		{
			name: "empty provider falls back to webhook",
			err:  &TransportError{message: "boom"},
			want: "webhook webhook transport error: boom",
		},
		{
			name: "message falls back to wrapped error",
			err:  &TransportError{Provider: "slack", err: errors.New("dial failed")},
			want: "slack webhook transport error: dial failed",
		},
		{
			name: "no message and no wrapped error",
			err:  &TransportError{Provider: "discord"},
			want: "discord webhook transport error",
		},
		{
			name: "provider and message",
			err:  &TransportError{Provider: "discord", message: "request timed out"},
			want: "discord webhook transport error: request timed out",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.Error(); got != tt.want {
				t.Fatalf("Error() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTransportErrorUnwrap(t *testing.T) {
	var nilErr *TransportError
	if nilErr.Unwrap() != nil {
		t.Fatal("nil TransportError Unwrap() != nil")
	}
	if nilErr.Retryable() {
		t.Fatal("nil TransportError Retryable() = true, want false")
	}

	inner := errors.New("connection reset")
	transport := &TransportError{Provider: "slack", err: inner}
	if transport.Unwrap() != inner {
		t.Fatal("TransportError Unwrap() did not return wrapped error")
	}
}

func TestTransportErrorRetryableClassification(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil wrapped error",
			err:  nil,
			want: false,
		},
		{
			name: "context canceled",
			err:  context.Canceled,
			want: false,
		},
		{
			name: "wrapped context canceled",
			err:  fmt.Errorf("send failed: %w", context.Canceled),
			want: false,
		},
		{
			name: "context deadline exceeded",
			err:  context.DeadlineExceeded,
			want: true,
		},
		{
			name: "temporary-only net error is not retryable",
			err:  webhookHTTPTestNetError{message: "temporary failure", temporary: true},
			want: false,
		},
		{
			name: "temporary dns error",
			err:  &net.DNSError{Err: "server misbehaving", Name: "hooks.example", IsTemporary: true},
			want: true,
		},
		{
			name: "net op error",
			err:  &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")},
			want: true,
		},
		{
			name: "dns error",
			err:  &net.DNSError{Err: "no such host", Name: "hooks.invalid"},
			want: true,
		},
		{
			name: "plain error",
			err:  errors.New("unexpected failure"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transport := &TransportError{Provider: "discord", err: tt.err}
			if got := transport.Retryable(); got != tt.want {
				t.Fatalf("Retryable() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHTTPStatusErrorErrorFormatting(t *testing.T) {
	tests := []struct {
		name string
		err  *HTTPStatusError
		want string
	}{
		{
			name: "nil receiver",
			err:  nil,
			want: "",
		},
		{
			name: "empty provider falls back to webhook",
			err:  &HTTPStatusError{StatusCode: http.StatusInternalServerError},
			want: "webhook webhook HTTP 500",
		},
		{
			name: "status and body appended",
			err:  NewHTTPStatusError("discord", http.StatusTooManyRequests, "429 Too Many Requests", "rate limited", nil),
			want: "discord webhook HTTP 429: 429 Too Many Requests: rate limited",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.Error(); got != tt.want {
				t.Fatalf("Error() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHTTPStatusErrorUnwrapAndNilRetryable(t *testing.T) {
	var nilErr *HTTPStatusError
	if nilErr.Unwrap() != nil {
		t.Fatal("nil HTTPStatusError Unwrap() != nil")
	}
	if nilErr.Retryable() {
		t.Fatal("nil HTTPStatusError Retryable() = true, want false")
	}

	inner := errors.New("read body failed")
	statusErr := NewHTTPStatusError("slack", http.StatusBadGateway, "502 Bad Gateway", "", inner)
	if statusErr.Unwrap() != inner {
		t.Fatal("HTTPStatusError Unwrap() did not return wrapped error")
	}
}

func TestIsRetryableErrorNilIsNotRetryable(t *testing.T) {
	if IsRetryableError(nil) {
		t.Fatal("IsRetryableError(nil) = true, want false")
	}
}
