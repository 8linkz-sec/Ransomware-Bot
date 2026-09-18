package webhookhttp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/httpstatus"
)

type Retryable interface {
	Retryable() bool
}

type PermanentError struct {
	err error
}

func (e *PermanentError) Error() string {
	if e == nil || e.err == nil {
		return ""
	}
	return e.err.Error()
}

func (e *PermanentError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func (e *PermanentError) Retryable() bool {
	return false
}

func NewPermanentError(err error) error {
	if err == nil {
		return nil
	}
	return &PermanentError{err: err}
}

type TransportError struct {
	Provider string
	err      error
	message  string
}

func (e *TransportError) Error() string {
	if e == nil {
		return ""
	}
	provider := strings.TrimSpace(e.Provider)
	if provider == "" {
		provider = "webhook"
	}
	message := strings.TrimSpace(e.message)
	if message == "" && e.err != nil {
		message = e.err.Error()
	}
	if message == "" {
		return fmt.Sprintf("%s webhook transport error", provider)
	}
	return fmt.Sprintf("%s webhook transport error: %s", provider, message)
}

func (e *TransportError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func (e *TransportError) Retryable() bool {
	if e == nil {
		return false
	}
	return isRetryableTransportError(e.err)
}

func NewTransportError(provider string, err error, message string) error {
	if err == nil {
		return nil
	}
	return &TransportError{
		Provider: provider,
		err:      err,
		message:  message,
	}
}

type HTTPStatusError struct {
	Provider   string
	StatusCode int
	Status     string
	Body       string
	err        error
}

func (e *HTTPStatusError) Error() string {
	if e == nil {
		return ""
	}

	provider := strings.TrimSpace(e.Provider)
	if provider == "" {
		provider = "webhook"
	}
	message := fmt.Sprintf("%s webhook HTTP %d", provider, e.StatusCode)
	if e.Status != "" {
		message = fmt.Sprintf("%s: %s", message, e.Status)
	}
	if e.Body != "" {
		message = fmt.Sprintf("%s: %s", message, e.Body)
	}
	return message
}

func (e *HTTPStatusError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func (e *HTTPStatusError) Retryable() bool {
	if e == nil {
		return false
	}
	return httpstatus.IsRetryable(e.StatusCode)
}

func NewHTTPStatusError(provider string, statusCode int, status, body string, err error) *HTTPStatusError {
	return &HTTPStatusError{
		Provider:   provider,
		StatusCode: statusCode,
		Status:     status,
		Body:       body,
		err:        err,
	}
}

func IsRetryableError(err error) bool {
	if err == nil {
		return false
	}

	var retryable Retryable
	if errors.As(err, &retryable) {
		return retryable.Retryable()
	}

	return true
}

func isRetryableTransportError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}

	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}

	var dnsErr *net.DNSError
	return errors.As(err, &dnsErr)
}
