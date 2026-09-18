package status

import "time"

// FeedInfo contains status information for a single RSS feed
type FeedInfo struct {
	LastCheck           time.Time         `json:"last_check"`
	LastSuccess         *time.Time        `json:"last_success"`
	LastError           *string           `json:"last_error"`
	LastErrorCategory   string            `json:"last_error_category,omitempty"`
	LastStatusCode      int               `json:"last_status_code,omitempty"`
	LastRetryable       *bool             `json:"last_retryable,omitempty"`
	LastTimeout         *bool             `json:"last_timeout,omitempty"`
	ETag                string            `json:"etag,omitempty"`
	LastModified        string            `json:"last_modified,omitempty"`
	EntriesFound        int               `json:"entries_found"`
	SuccessRate         float64           `json:"success_rate"`
	ConsecutiveFailures int               `json:"consecutive_failures,omitempty"`
	NextAttemptAfter    *time.Time        `json:"next_attempt_after,omitempty"`
	ProcessedItems      map[string]string `json:"processed_items,omitempty"`
	// ConsecutiveNotModified counts consecutive successful polls that reported
	// zero entries while offering no validator fresher than the one already on
	// file (empty, or byte-identical to what is stored) -- i.e. polls that look
	// like a 304, including one that echoes back the validator it was sent.
	// It resets whenever a poll returns entries or a genuinely fresh (non-empty,
	// changed) validator. Kept for diagnostics; the forced-refetch guard in
	// UpdateFeedStatusWithErrorInfoAndValidators trips on
	// ConsecutiveNotModifiedSince, not on this count (see
	// maxConsecutiveNotModifiedDuration).
	ConsecutiveNotModified int `json:"consecutive_not_modified,omitempty"`
	// ConsecutiveNotModifiedSince is when the current ConsecutiveNotModified
	// streak started (nil while the streak is 0). The forced-refetch guard
	// compares wall-clock time elapsed since this timestamp against
	// maxConsecutiveNotModifiedDuration, so recovery time is bounded
	// regardless of rss_poll_interval, which has a floor but no ceiling.
	ConsecutiveNotModifiedSince *time.Time `json:"consecutive_not_modified_since,omitempty"`
}
