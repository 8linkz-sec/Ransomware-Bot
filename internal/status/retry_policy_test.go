package status

import (
	"strings"
	"testing"
	"time"
)

// TestRetryDeadLetterDecisionHonorsMaxAttempts pins the attempt budget from both
// sides: retry_max_attempts counts the retries after the first send, so
// retry_count == retry_max_attempts is still allowed and only a higher count is
// terminal.
func TestRetryDeadLetterDecisionHonorsMaxAttempts(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	item := retryItem{RetryCount: 4, FirstFailed: formatStatusTimestamp(now)}

	_, reason, shouldMove := retryDeadLetterDecision(item, "failed", 3, time.Hour, now)

	if !shouldMove || reason != TerminalReasonMaxAttempts {
		t.Fatalf("retryDeadLetterDecision() = reason %q move %v, want max attempts", reason, shouldMove)
	}

	atBudget := retryItem{RetryCount: 3, FirstFailed: formatStatusTimestamp(now)}

	_, reason, shouldMove = retryDeadLetterDecision(atBudget, "failed", 3, time.Hour, now)

	if shouldMove || reason != "" {
		t.Fatalf("retryDeadLetterDecision() = reason %q move %v, want the last retry kept queued", reason, shouldMove)
	}
}

func TestRetryDeadLetterDecisionHonorsRetryWindow(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	item := retryItem{
		RetryCount:  1,
		FirstFailed: formatStatusTimestamp(now.Add(-2 * time.Hour)),
	}

	_, reason, shouldMove := retryDeadLetterDecision(item, "failed", 0, time.Hour, now)

	if !shouldMove || reason != TerminalReasonRetryWindow {
		t.Fatalf("retryDeadLetterDecision() = reason %q move %v, want retry window", reason, shouldMove)
	}
}

func TestRetryDeadLetterDecisionReportsInvalidFirstFailed(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	item := retryItem{RetryCount: 1, FirstFailed: "not-a-time"}

	updated, reason, shouldMove := retryDeadLetterDecision(item, "failed", 0, time.Hour, now)

	if !shouldMove || reason != TerminalReasonInvalidFirstFailed {
		t.Fatalf("retryDeadLetterDecision() = reason %q move %v, want invalid timestamp", reason, shouldMove)
	}
	if !strings.Contains(updated.LastError, "invalid first_failed timestamp") {
		t.Fatalf("updated LastError = %q, want invalid timestamp context", updated.LastError)
	}
}

func TestRetryDeadLetterDecisionKeepsRetryableItemQueued(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	item := retryItem{
		RetryCount:  1,
		FirstFailed: formatStatusTimestamp(now.Add(-10 * time.Minute)),
	}

	_, reason, shouldMove := retryDeadLetterDecision(item, "failed", 3, time.Hour, now)

	if shouldMove || reason != "" {
		t.Fatalf("retryDeadLetterDecision() = reason %q move %v, want keep queued", reason, shouldMove)
	}
}

// TestRetryDeadLetterDecisionMaxAttemptsOneAllowsOneRetry pins that with
// retry_max_attempts = 1 the first send may be retried once: the send that made
// retry_count 1 stays queued, and only the retry that makes it 2 is terminal.
func TestRetryDeadLetterDecisionMaxAttemptsOneAllowsOneRetry(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	item := retryItem{RetryCount: 1, FirstFailed: formatStatusTimestamp(now)}

	_, reason, shouldMove := retryDeadLetterDecision(item, "failed", 1, time.Hour, now)

	if shouldMove || reason != "" {
		t.Fatalf("retryDeadLetterDecision() = reason %q move %v, want the first send retried once", reason, shouldMove)
	}

	retried := retryItem{RetryCount: 2, FirstFailed: formatStatusTimestamp(now)}

	_, reason, shouldMove = retryDeadLetterDecision(retried, "failed", 1, time.Hour, now)

	if !shouldMove || reason != TerminalReasonMaxAttempts {
		t.Fatalf("retryDeadLetterDecision() = reason %q move %v, want max attempts after the single retry", reason, shouldMove)
	}
}
