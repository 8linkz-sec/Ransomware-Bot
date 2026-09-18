package status

import (
	"fmt"
	"time"
)

func retryDeadLetterDecision(
	item retryItem,
	lastError string,
	maxAttempts int,
	retryWindow time.Duration,
	now time.Time,
) (retryItem, string, bool) {
	// retry_max_attempts counts retries AFTER the first send, so an item that has
	// been sent RetryCount times has used RetryCount-1 retries. The budget is spent
	// once RetryCount-1 >= maxAttempts, i.e. RetryCount > maxAttempts, which allows
	// maxAttempts+1 sends in total. maxAttempts == 0 means unlimited.
	if maxAttempts > 0 && item.RetryCount > maxAttempts {
		return item, TerminalReasonMaxAttempts, true
	}

	if retryWindow <= 0 {
		return item, "", false
	}

	firstFailed, err := parseRequiredStatusTimestamp(item.FirstFailed)
	if err != nil {
		item.LastError = retryWindowTimestampError(lastError, item.FirstFailed, err)
		return item, TerminalReasonInvalidFirstFailed, true
	}
	if now.Sub(firstFailed) > retryWindow {
		return item, TerminalReasonRetryWindow, true
	}
	return item, "", false
}

func retryWindowTimestampError(lastError, firstFailed string, err error) string {
	return fmt.Sprintf("%s; invalid first_failed timestamp %q: %v", lastError, firstFailed, err)
}
