package httpstatus

import (
	"net/http"
	"testing"
)

func TestIsRetryable(t *testing.T) {
	retryable := []int{
		http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout,
	}
	for _, statusCode := range retryable {
		if !IsRetryable(statusCode) {
			t.Fatalf("IsRetryable(%d) = false, want true", statusCode)
		}
	}

	nonRetryable := []int{
		http.StatusOK,
		http.StatusBadRequest,
		http.StatusUnauthorized,
		http.StatusForbidden,
		http.StatusNotFound,
	}
	for _, statusCode := range nonRetryable {
		if IsRetryable(statusCode) {
			t.Fatalf("IsRetryable(%d) = true, want false", statusCode)
		}
	}
}
