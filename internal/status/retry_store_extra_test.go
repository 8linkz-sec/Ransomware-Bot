package status

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

func TestRetryDeadLetterDecisionNoWindowKeepsQueued(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	item := retryItem{RetryCount: 1, FirstFailed: formatStatusTimestamp(now)}

	_, reason, shouldMove := retryDeadLetterDecision(item, "failed", 0, 0, now)

	if shouldMove || reason != "" {
		t.Fatalf("retryDeadLetterDecision() = reason %q move %v, want keep queued without window", reason, shouldMove)
	}
}

func TestEnqueueDeferredRetryQueuesWithoutFailureCount(t *testing.T) {
	tracker := NewMemoryTracker()
	payload := []byte(`{"post_title":"deferred"}`)

	if !tracker.EnqueueDeferredRetry(
		"item-1", "discord.ransomware", "discord", "api", "Example", "quiet hours", payload,
	) {
		t.Fatal("EnqueueDeferredRetry() did not queue a new item")
	}

	items := tracker.GetRetryItemsByType("api")
	if len(items) != 1 {
		t.Fatalf("retry queue length = %d, want 1", len(items))
	}
	if items[0].RetryCount != 0 {
		t.Fatalf("RetryCount = %d, want 0 for deferred item", items[0].RetryCount)
	}
	if items[0].LastError != "quiet hours" {
		t.Fatalf("LastError = %q, want deferred reason", items[0].LastError)
	}
	if items[0].PayloadVersion != RetryPayloadVersionRansomwareEntryV1 {
		t.Fatalf("PayloadVersion = %q, want %q", items[0].PayloadVersion, RetryPayloadVersionRansomwareEntryV1)
	}

	if tracker.EnqueueDeferredRetry(
		"item-1", "discord.ransomware", "discord", "api", "Example", "quiet hours", payload,
	) {
		t.Fatal("EnqueueDeferredRetry() re-queued an already queued item")
	}
}

func TestEnqueueDeferredRetrySkipsDeadLetteredItem(t *testing.T) {
	tracker := NewMemoryTracker()

	tracker.MarkRetryDeadLetterForDestination("item-1", "discord.ransomware", "discord", "api", "Example", "terminal")

	if tracker.EnqueueDeferredRetryForDestination(
		"item-1", "discord.ransomware", "discord", "api", "Example", "quiet hours", nil,
	) {
		t.Fatal("EnqueueDeferredRetryForDestination() queued a dead-lettered item")
	}
	if items := tracker.GetRetryItemsByType("api"); len(items) != 0 {
		t.Fatalf("retry queue length = %d, want 0", len(items))
	}
}

func TestRemoveFromRetryQueueRemovesQueuedItem(t *testing.T) {
	tracker := NewMemoryTracker()

	if !tracker.enqueueRetryForTest("item-1", "discord.ransomware", "discord", "api", "Example", "boom", 5, time.Hour) {
		t.Fatal("EnqueueRetry() unexpectedly failed")
	}

	tracker.RemoveFromRetryQueue("item-1", "discord.ransomware")

	if items := tracker.GetRetryItemsByType("api"); len(items) != 0 {
		t.Fatalf("retry queue length = %d, want 0 after removal", len(items))
	}
}

func TestRemoveFromRetryQueueByDestinationHandlesLegacyDestinations(t *testing.T) {
	tracker := NewMemoryTracker()
	legacyURL := "https://discord.com/api/webhooks/123456789012345678/test-token"

	if !tracker.enqueueRetryForTest("item-1", legacyURL, "discord", "api", "Example", "boom", 5, time.Hour) {
		t.Fatal("EnqueueRetry() unexpectedly failed")
	}

	tracker.RemoveFromRetryQueueByDestination("item-1", "discord.ransomware", "", "discord.ransomware", legacyURL)

	if items := tracker.GetRetryItemsByType("api"); len(items) != 0 {
		t.Fatalf("retry queue length = %d, want 0 after legacy destination removal", len(items))
	}
}

func TestRemoveRetryQueueEntryRemovesByOpaqueKey(t *testing.T) {
	tracker := NewMemoryTracker()

	if !tracker.enqueueRetryForTest("item-1", "discord.ransomware", "discord", "api", "Example", "boom", 5, time.Hour) {
		t.Fatal("EnqueueRetry() unexpectedly failed")
	}
	queueKey := queuedRetryKeyForTest(t, tracker, "item-1", "api")

	tracker.RemoveRetryQueueEntry(queueKey)

	if items := tracker.GetRetryItemsByType("api"); len(items) != 0 {
		t.Fatalf("retry queue length = %d, want 0 after queue-key removal", len(items))
	}
}

func TestRecordRetryQueueFailureKeepsRetryableItemQueued(t *testing.T) {
	tracker := NewMemoryTracker()

	if !tracker.enqueueRetryForTest("item-1", "discord.ransomware", "discord", "api", "Example", "first failure", 5, time.Hour) {
		t.Fatal("EnqueueRetry() unexpectedly failed")
	}
	queueKey := queuedRetryKeyForTest(t, tracker, "item-1", "api")

	if !tracker.RecordRetryQueueFailureWithErrorInfo(
		queueKey,
		"second failure",
		RetryErrorInfo{ErrorCategory: "http_5xx", StatusCode: 502},
		5,
		time.Hour,
	) {
		t.Fatal("RecordRetryQueueFailureWithErrorInfo() dead-lettered a retryable item")
	}

	items := tracker.GetRetryItemsByType("api")
	if len(items) != 1 {
		t.Fatalf("retry queue length = %d, want 1", len(items))
	}
	if items[0].RetryCount != 2 {
		t.Fatalf("RetryCount = %d, want 2", items[0].RetryCount)
	}
	if items[0].LastError != "second failure" {
		t.Fatalf("LastError = %q, want updated failure", items[0].LastError)
	}
	if items[0].ErrorCategory != "http_5xx" || items[0].StatusCode != 502 {
		t.Fatalf("error info = %q/%d, want http_5xx/502", items[0].ErrorCategory, items[0].StatusCode)
	}
}

// TestRecordRetryQueueFailureRetryBudgetBoundary pins the attempt budget from
// both sides on the replay path. EnqueueRetryForDestination and
// RecordRetryQueueFailureWithErrorInfo are two independent call sites of
// retryDeadLetterDecision; every other budget test drives the first one, so a
// reintroduced `retry_count >= retry_max_attempts` in the replay path alone
// would go unnoticed. With retry_max_attempts=2 the failure that makes
// retry_count 2 is the second of two allowed retries and stays queued; only the
// failure that makes it 3 is terminal.
func TestRecordRetryQueueFailureRetryBudgetBoundary(t *testing.T) {
	tracker := NewMemoryTracker()

	if !tracker.enqueueRetryForTest(
		"item-1", "discord.ransomware", "discord", "api", "Example", "first failure", 2, time.Hour,
	) {
		t.Fatal("EnqueueRetry() unexpectedly failed")
	}
	queueKey := queuedRetryKeyForTest(t, tracker, "item-1", "api")

	if !tracker.RecordRetryQueueFailure(queueKey, "second failure", 2, time.Hour) {
		t.Fatal("RecordRetryQueueFailure() dead-lettered retry_count 2 with retry_max_attempts=2, want it queued")
	}
	queued := tracker.GetRetryItemsByType("api")
	if len(queued) != 1 {
		t.Fatalf("retry queue length = %d, want 1", len(queued))
	}
	if queued[0].RetryCount != 2 {
		t.Fatalf("queued RetryCount = %d, want 2", queued[0].RetryCount)
	}
	if items := tracker.GetDeadLetterItems(); len(items) != 0 {
		t.Fatalf("dead letter count after the second failure = %d, want 0", len(items))
	}

	if tracker.RecordRetryQueueFailure(queueKey, "third failure", 2, time.Hour) {
		t.Fatal("RecordRetryQueueFailure() queued retry_count 3 with retry_max_attempts=2, want a dead letter")
	}
	if items := tracker.GetRetryItemsByType("api"); len(items) != 0 {
		t.Fatalf("retry queue length = %d, want 0", len(items))
	}
	deadLetters := tracker.GetDeadLetterItems()
	if len(deadLetters) != 1 {
		t.Fatalf("dead letter count = %d, want 1", len(deadLetters))
	}
	if deadLetters[0].TerminalReason != TerminalReasonMaxAttempts {
		t.Fatalf("TerminalReason = %q, want %q", deadLetters[0].TerminalReason, TerminalReasonMaxAttempts)
	}
	if deadLetters[0].RetryCount != 3 {
		t.Fatalf("dead letter RetryCount = %d, want 3", deadLetters[0].RetryCount)
	}
	if deadLetters[0].LastError != "third failure" {
		t.Fatalf("dead letter LastError = %q, want %q", deadLetters[0].LastError, "third failure")
	}
}

func TestRecordRetryQueueFailureMissingEntryReturnsFalse(t *testing.T) {
	tracker := NewMemoryTracker()

	if tracker.RecordRetryQueueFailure("missing-queue-key", "boom", 5, time.Hour) {
		t.Fatal("RecordRetryQueueFailure() reported success for a missing queue entry")
	}
}

func clearQueuedRetryPayloadVersionsForTest(t *testing.T, tracker *Tracker) {
	t.Helper()

	tracker.mutex.Lock()
	defer tracker.mutex.Unlock()
	for key, item := range tracker.retryStore.status.RetryQueue {
		item.PayloadVersion = ""
		tracker.retryStore.status.RetryQueue[key] = item
	}
}

func TestRecordRetryQueueFailureBackfillsLegacyAPIPayloadVersion(t *testing.T) {
	tracker := NewMemoryTracker()
	payload := []byte(`{"post_title":"legacy"}`)

	if !tracker.enqueueRetryForTest("item-1", "discord.ransomware", "discord", "api", "Example", "first failure", 5, time.Hour, payload) {
		t.Fatal("EnqueueRetry() unexpectedly failed")
	}
	// Simulate a legacy persisted queue item that predates payload versioning.
	clearQueuedRetryPayloadVersionsForTest(t, tracker)
	queueKey := queuedRetryKeyForTest(t, tracker, "item-1", "api")

	if !tracker.RecordRetryQueueFailure(queueKey, "second failure", 5, time.Hour) {
		t.Fatal("RecordRetryQueueFailure() dead-lettered a retryable item")
	}

	items := tracker.GetRetryItemsByType("api")
	if len(items) != 1 || items[0].PayloadVersion != RetryPayloadVersionRansomwareEntryV1 {
		t.Fatalf("retry items = %+v, want backfilled payload version", items)
	}
}

func TestEnqueueRetryUpdateBackfillsLegacyAPIPayloadVersion(t *testing.T) {
	tracker := NewMemoryTracker()
	payload := []byte(`{"post_title":"legacy"}`)

	if !tracker.enqueueRetryForTest("item-1", "discord.ransomware", "discord", "api", "Example", "first failure", 5, time.Hour, payload) {
		t.Fatal("initial EnqueueRetry() unexpectedly failed")
	}
	// Simulate a legacy persisted queue item that predates payload versioning.
	clearQueuedRetryPayloadVersionsForTest(t, tracker)

	if !tracker.enqueueRetryForTest("item-1", "discord.ransomware", "discord", "api", "Example", "second failure", 5, time.Hour) {
		t.Fatal("update EnqueueRetry() dead-lettered a retryable item")
	}

	items := tracker.GetRetryItemsByType("api")
	if len(items) != 1 {
		t.Fatalf("retry queue length = %d, want 1", len(items))
	}
	if items[0].RetryCount != 2 {
		t.Fatalf("RetryCount = %d, want 2", items[0].RetryCount)
	}
	if items[0].PayloadVersion != RetryPayloadVersionRansomwareEntryV1 {
		t.Fatalf("PayloadVersion = %q, want backfilled %q", items[0].PayloadVersion, RetryPayloadVersionRansomwareEntryV1)
	}
}

func TestDeadLetterRetryQueueEntryMissingReturnsFalse(t *testing.T) {
	tracker := NewMemoryTracker()

	if tracker.DeadLetterRetryQueueEntry("missing-queue-key", "boom") {
		t.Fatal("DeadLetterRetryQueueEntry() reported success for a missing queue entry")
	}
	if deadLetters := tracker.GetDeadLetterItems(); len(deadLetters) != 0 {
		t.Fatalf("dead letter count = %d, want 0", len(deadLetters))
	}
}

func TestMarkRetryDeadLetterSkipsDuplicateTerminalRecords(t *testing.T) {
	tracker := NewMemoryTracker()

	tracker.MarkRetryDeadLetter("item-1", "discord", "api", "Example", "terminal failure")
	tracker.MarkRetryDeadLetter("item-1", "discord", "api", "Example", "terminal failure again")

	tracker.MarkRetryDeadLetterForDestination("item-2", "discord.ransomware", "discord", "api", "Example", "terminal")
	tracker.MarkRetryDeadLetterForDestination("item-2", "discord.ransomware", "discord", "api", "Example", "terminal again")

	deadLetters := tracker.GetDeadLetterItems()
	if len(deadLetters) != 2 {
		t.Fatalf("dead letter count = %d, want 2 (one per item)", len(deadLetters))
	}
}

func TestMarkRetryDeadLetterForDestinationKeepsOtherDestinationQueued(t *testing.T) {
	tracker := NewMemoryTracker()

	if !tracker.enqueueRetryForTest(
		"item-1", "discord.a", "discord", "api", "Example", "boom", 5, time.Hour, []byte(`{"id":"row-a"}`),
	) {
		t.Fatal("EnqueueRetry() for discord.a unexpectedly failed")
	}
	if !tracker.enqueueRetryForTest(
		"item-1", "discord.b", "discord", "api", "Example", "boom", 5, time.Hour, []byte(`{"id":"row-b"}`),
	) {
		t.Fatal("EnqueueRetry() for discord.b unexpectedly failed")
	}
	queueKeyB := queuedRetryKeyForDestinationForTest(t, tracker, "item-1", "api", "discord.b")
	if !tracker.RecordRetryQueueFailure(queueKeyB, "boom again", 5, time.Hour) {
		t.Fatal("RecordRetryQueueFailure() for discord.b unexpectedly failed")
	}

	tracker.MarkRetryDeadLetterForDestination("item-1", "discord.a", "discord", "api", "Example", "terminal")

	items := tracker.GetRetryItemsByType("api")
	if len(items) != 1 {
		t.Fatalf("retry queue length = %d, want 1 surviving destination", len(items))
	}
	if items[0].DestinationID != "discord.b" {
		t.Fatalf("surviving DestinationID = %q, want discord.b", items[0].DestinationID)
	}
	if items[0].RetryCount != 2 {
		t.Fatalf("surviving RetryCount = %d, want 2 (untouched by the discord.a dead letter)", items[0].RetryCount)
	}
	deadLetters := tracker.GetDeadLetterItems()
	if len(deadLetters) != 1 {
		t.Fatalf("dead letter count = %d, want 1", len(deadLetters))
	}
	got := deadLetters[0]
	if got.DestinationID != "discord.a" {
		t.Fatalf("dead letter DestinationID = %q, want discord.a", got.DestinationID)
	}
	if got.RetryCount != 1 {
		t.Fatalf("dead letter RetryCount = %d, want 1 inherited from the discord.a row, not discord.b", got.RetryCount)
	}
	if string(got.Payload) != `{"id":"row-a"}` {
		t.Fatalf("dead letter Payload = %q, want the discord.a payload", string(got.Payload))
	}
}

func TestSetQueuedRetryTimesRequiresMatchingItem(t *testing.T) {
	tracker := NewMemoryTracker()

	if !tracker.enqueueRetryForTest("item-1", "discord.ransomware", "discord", "api", "Example", "boom", 5, time.Hour) {
		t.Fatal("EnqueueRetry() unexpectedly failed")
	}

	stamp := formatStatusTimestamp(statusNow())
	if tracker.setQueuedRetryTimes("other-item", "api", stamp, stamp) {
		t.Fatal("setQueuedRetryTimes() matched a different item key")
	}
}

func TestSetDeadLetterTimesEdgeCases(t *testing.T) {
	tracker := NewMemoryTracker()

	if updated := tracker.setDeadLetterTimes(nil); updated != 0 {
		t.Fatalf("setDeadLetterTimes(nil) = %d, want 0", updated)
	}

	tracker.MarkRetryDeadLetter("dead-1", "discord", "api", "Example", "terminal")
	if updated := tracker.setDeadLetterTimes(map[string]string{
		"unknown": formatStatusTimestamp(statusNow()),
	}); updated != 0 {
		t.Fatalf("setDeadLetterTimes(unknown key) = %d, want 0", updated)
	}
}

func TestCloneRetryStatusNilReturnsNil(t *testing.T) {
	if cloned := cloneRetryStatus(nil); cloned != nil {
		t.Fatalf("cloneRetryStatus(nil) = %+v, want nil", cloned)
	}
}

func TestIsRetryDeadLetteredForDestinationMatchesLegacyDestination(t *testing.T) {
	tracker := NewMemoryTracker()

	tracker.MarkRetryDeadLetterForDestination("item-1", "legacy.route", "discord", "api", "Example", "terminal")

	if tracker.IsRetryDeadLetteredForDestination("item-1", "discord.new", "discord", "api") {
		t.Fatal("unrelated destination reported dead-lettered without legacy hint")
	}
	if !tracker.IsRetryDeadLetteredForDestination("item-1", "discord.new", "discord", "api", "legacy.route") {
		t.Fatal("legacy destination dead letter was not recognized")
	}
}

func TestGetRetryItemsByTypeRebuildsStaleIndex(t *testing.T) {
	tracker := NewMemoryTracker()

	if !tracker.enqueueRetryForTest("item-1", "discord.ransomware", "discord", "api", "Example", "boom", 5, time.Hour) {
		t.Fatal("EnqueueRetry() unexpectedly failed")
	}

	tracker.mutex.Lock()
	tracker.retryStore.byType = nil
	tracker.mutex.Unlock()

	if items := tracker.GetRetryItemsByType("api"); len(items) != 1 {
		t.Fatalf("retry queue length after index rebuild = %d, want 1", len(items))
	}
}

func TestRetryIndexHelpersIgnoreIncompleteItems(t *testing.T) {
	tracker := NewMemoryTracker()

	tracker.indexRetryQueueItemLocked("", retryItem{ItemType: "api"})
	tracker.indexRetryQueueItemLocked("queue-key", retryItem{})
	if got := len(tracker.retryStore.byType); got != 0 {
		t.Fatalf("byType size = %d, want 0 after incomplete index calls", got)
	}

	tracker.retryStore.byType = nil
	tracker.deindexRetryQueueItemLocked("queue-key", retryItem{ItemType: "api"})
	if tracker.retryStore.byType != nil {
		t.Fatal("deindex on nil byType map recreated the index")
	}

	tracker.indexRetryQueueItemLocked("queue-key", retryItem{ItemType: "api"})
	if _, ok := tracker.retryStore.byType["api"]["queue-key"]; !ok {
		t.Fatal("index on nil byType map did not recreate the index entry")
	}

	tracker.deindexRetryQueueItemLocked("queue-key", retryItem{ItemType: "api"})
	if got := len(tracker.retryStore.byType); got != 0 {
		t.Fatalf("byType size = %d, want 0 after deindexing the last entry", got)
	}
}

func TestCleanupOldEntriesPrunesRetryQueueByCount(t *testing.T) {
	policy := DefaultRetentionPolicy()
	policy.MaxRetryQueueItems = 1
	tracker := NewMemoryTrackerWithRetention(policy)

	if !tracker.enqueueRetryForTest("item-old", "discord.ransomware", "discord", "api", "Old", "boom", 5, 24*time.Hour) {
		t.Fatal("EnqueueRetry() for item-old unexpectedly failed")
	}
	if !tracker.enqueueRetryForTest("item-new", "discord.ransomware", "discord", "api", "New", "boom", 5, 24*time.Hour) {
		t.Fatal("EnqueueRetry() for item-new unexpectedly failed")
	}
	oldTime := formatStatusTimestamp(statusNow().Add(-time.Hour))
	if !tracker.setQueuedRetryTimes("item-old", "api", oldTime, oldTime) {
		t.Fatal("queued retry item missing")
	}

	tracker.CleanupOldEntries()

	items := tracker.GetRetryItemsByType("api")
	if len(items) != 1 || items[0].ItemKey != "item-new" {
		t.Fatalf("retry items after count pruning = %+v, want only item-new", items)
	}
	if !tracker.IsRetryDeadLettered("item-old", "discord", "api") {
		t.Fatal("count-pruned retry item was not moved to dead letter")
	}
	deadLetters := tracker.GetDeadLetterItems()
	if len(deadLetters) != 1 || deadLetters[0].TerminalReason != TerminalReasonRetentionPruned {
		t.Fatalf("dead letters = %+v, want one retention-pruned entry", deadLetters)
	}
}

func TestRetryItemRetentionTimeFallsBackToFirstFailed(t *testing.T) {
	firstFailed := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	got := retryItemRetentionTime(retryItem{FirstFailed: formatStatusTimestamp(firstFailed)})
	if !got.Equal(firstFailed) {
		t.Fatalf("retryItemRetentionTime() = %v, want FirstFailed fallback %v", got, firstFailed)
	}

	if got := retryItemRetentionTime(retryItem{}); !got.IsZero() {
		t.Fatalf("retryItemRetentionTime(empty) = %v, want zero", got)
	}
}

func queuedRetryKeyForDestinationForTest(t *testing.T, tracker *Tracker, itemKey, itemType, destinationID string) string {
	t.Helper()

	for _, record := range tracker.GetQueuedRetryItemsByType(itemType) {
		if record.Item.ItemKey == itemKey && record.Item.DestinationID == destinationID {
			return record.QueueKey
		}
	}
	t.Fatalf("no queued retry record for item %q destination %q", itemKey, destinationID)
	return ""
}

func TestMarkRetryDeadLetterForDestinationKeepsQueuedHistory(t *testing.T) {
	tracker := newTestTracker(t)
	payload := []byte(`{"id":"abc-1234"}`)

	if !tracker.enqueueRetryForTest(
		"api-item-queued", "slack.ransomware", "slack", "api", "Example", "boom", 10, time.Hour, payload,
	) {
		t.Fatal("EnqueueRetry() unexpectedly failed")
	}
	queueKey := queuedRetryKeyForDestinationForTest(t, tracker, "api-item-queued", "api", "slack.ransomware")
	for i := 0; i < 2; i++ {
		if !tracker.RecordRetryQueueFailure(queueKey, "boom again", 10, time.Hour) {
			t.Fatalf("RecordRetryQueueFailure() #%d unexpectedly failed", i+1)
		}
	}
	if !tracker.setQueuedRetryTimes("api-item-queued", "api", "2026-09-01T00:00:00Z", "2026-09-02T00:00:00Z") {
		t.Fatal("setQueuedRetryTimes() did not match the queued item")
	}

	tracker.MarkRetryDeadLetterForDestinationWithErrorInfo(
		"api-item-queued", "slack.ransomware", "slack", "api", "Terminal Example", "403 forbidden",
		RetryErrorInfo{ErrorCategory: "http_status", StatusCode: 403},
	)

	deadLetters := tracker.GetDeadLetterItems()
	if len(deadLetters) != 1 {
		t.Fatalf("dead letter count = %d, want 1", len(deadLetters))
	}
	got := deadLetters[0]
	if got.RetryCount != 3 {
		t.Fatalf("RetryCount = %d, want 3 inherited from the queued row", got.RetryCount)
	}
	if got.FirstFailed != "2026-09-01T00:00:00Z" {
		t.Fatalf("FirstFailed = %q, want the queued 2026-09-01T00:00:00Z", got.FirstFailed)
	}
	if string(got.Payload) != `{"id":"abc-1234"}` {
		t.Fatalf("Payload = %q, want the queued replay payload", string(got.Payload))
	}
	if got.PayloadVersion != RetryPayloadVersionRansomwareEntryV1 {
		t.Fatalf("PayloadVersion = %q, want %q", got.PayloadVersion, RetryPayloadVersionRansomwareEntryV1)
	}
	if got.TerminalReason != TerminalReasonTerminalFailure {
		t.Fatalf("TerminalReason = %q, want %q", got.TerminalReason, TerminalReasonTerminalFailure)
	}
	if got.DestinationID != "slack.ransomware" {
		t.Fatalf("DestinationID = %q, want slack.ransomware", got.DestinationID)
	}
	if got.Title != "Terminal Example" {
		t.Fatalf("Title = %q, want the mark call title Terminal Example, not the queued row's Example", got.Title)
	}
	if got.ItemType != "api" || got.Messenger != "slack" {
		t.Fatalf("ItemType/Messenger = %q/%q, want api/slack", got.ItemType, got.Messenger)
	}
	if !strings.Contains(got.LastError, "403 forbidden") {
		t.Fatalf("LastError = %q, want it to contain the terminal error 403 forbidden", got.LastError)
	}
	if got.ErrorCategory != "http_status" {
		t.Fatalf("ErrorCategory = %q, want http_status", got.ErrorCategory)
	}
	if got.StatusCode != 403 {
		t.Fatalf("StatusCode = %d, want 403", got.StatusCode)
	}
	if got.DeadAt == got.FirstFailed {
		t.Fatalf("DeadAt = %q, want a terminal timestamp distinct from FirstFailed", got.DeadAt)
	}
	if items := tracker.GetQueuedRetryItemsByType("api"); len(items) != 0 {
		t.Fatalf("queued retry records after dead-lettering = %d, want 0", len(items))
	}
}

func TestMarkRetryDeadLetterWithoutQueuedItemKeepsFreshRecord(t *testing.T) {
	tracker := newTestTracker(t)

	tracker.MarkRetryDeadLetter("api-item-fresh", "discord", "api", "Example", "terminal")

	deadLetters := tracker.GetDeadLetterItems()
	if len(deadLetters) != 1 {
		t.Fatalf("dead letter count = %d, want 1", len(deadLetters))
	}
	got := deadLetters[0]
	if got.RetryCount != 0 {
		t.Fatalf("RetryCount = %d, want 0 without a queued row", got.RetryCount)
	}
	if len(got.Payload) != 0 {
		t.Fatalf("Payload = %q, want empty without a queued row", string(got.Payload))
	}
	if got.PayloadVersion != "" {
		t.Fatalf("PayloadVersion = %q, want empty without a queued row", got.PayloadVersion)
	}
	if got.FirstFailed != got.DeadAt {
		t.Fatalf("FirstFailed = %q, want it to equal DeadAt %q", got.FirstFailed, got.DeadAt)
	}
	if got.DestinationID != "" {
		t.Fatalf("DestinationID = %q, want empty for the legacy mark call", got.DestinationID)
	}
	if !tracker.IsRetryDeadLetteredForDestination("api-item-fresh", "discord.ransomware", "discord", "api") {
		t.Fatal("IsRetryDeadLetteredForDestination() = false, want a blank-destination record to block every destination")
	}
}

func TestMarkRetryDeadLetterLegacyKeepsQueuedHistoryWithoutDestination(t *testing.T) {
	tracker := newTestTracker(t)

	if !tracker.enqueueRetryForTest("legacy-item", "discord.a", "discord", "api", "Example", "boom", 10, time.Hour) {
		t.Fatal("EnqueueRetry() unexpectedly failed")
	}
	queueKey := queuedRetryKeyForDestinationForTest(t, tracker, "legacy-item", "api", "discord.a")
	if !tracker.RecordRetryQueueFailure(queueKey, "boom again", 10, time.Hour) {
		t.Fatal("RecordRetryQueueFailure() unexpectedly failed")
	}

	tracker.MarkRetryDeadLetter("legacy-item", "discord", "api", "Example", "terminal")

	deadLetters := tracker.GetDeadLetterItems()
	if len(deadLetters) != 1 {
		t.Fatalf("dead letter count = %d, want 1", len(deadLetters))
	}
	if got := deadLetters[0].RetryCount; got != 2 {
		t.Fatalf("RetryCount = %d, want 2 inherited from the queued row", got)
	}
	if got := deadLetters[0].DestinationID; got != "" {
		t.Fatalf("DestinationID = %q, want it to stay blank so the record blocks every destination", got)
	}
	if items := tracker.GetQueuedRetryItemsByType("api"); len(items) != 0 {
		t.Fatalf("queued retry records after dead-lettering = %d, want 0", len(items))
	}
}

func TestMarkRetryDeadLetterForDestinationKeepsRSSRecoveryHistory(t *testing.T) {
	tracker := newTestTracker(t)

	if !tracker.enqueueRetryForTest(
		"rss-item", "discord.rss.general", "discord", "rss", "Feed Post", "boom", 10, time.Hour,
	) {
		t.Fatal("EnqueueRetry() unexpectedly failed")
	}
	queueKey := queuedRetryKeyForDestinationForTest(t, tracker, "rss-item", "rss", "discord.rss.general")
	if !tracker.RecordRetryQueueFailureWithErrorInfo(
		queueKey, "boom again", RetryErrorInfo{ErrorCategory: "http_status", StatusCode: 503}, 10, time.Hour,
	) {
		t.Fatal("RecordRetryQueueFailureWithErrorInfo() unexpectedly failed")
	}
	queuedFirstFailed := ""
	for _, record := range tracker.GetQueuedRetryItemsByType("rss") {
		queuedFirstFailed = record.Item.FirstFailed
		if record.Item.ErrorCategory != "http_status" || record.Item.StatusCode != 503 {
			t.Fatalf(
				"queued RSS row error info = %q/%d, want http_status/503 so the wipe below can bite",
				record.Item.ErrorCategory, record.Item.StatusCode,
			)
		}
	}
	if queuedFirstFailed == "" {
		t.Fatal("queued RSS row has no FirstFailed timestamp")
	}

	tracker.MarkRetryDeadLetterForDestination(
		"rss-item", "discord.rss.general", "discord", "rss", "Feed Post", "invalid recovery item",
	)

	deadLetters := tracker.GetDeadLetterItems()
	if len(deadLetters) != 1 {
		t.Fatalf("dead letter count = %d, want 1", len(deadLetters))
	}
	got := deadLetters[0]
	if got.RetryCount != 2 {
		t.Fatalf("RetryCount = %d, want 2 inherited from the queued row", got.RetryCount)
	}
	if got.FirstFailed != queuedFirstFailed {
		t.Fatalf("FirstFailed = %q, want the queued %q", got.FirstFailed, queuedFirstFailed)
	}
	if len(got.Payload) != 0 {
		t.Fatalf("Payload = %q, want empty for an RSS recovery row", string(got.Payload))
	}
	if got.ErrorCategory != "" {
		t.Fatalf("ErrorCategory = %q, want it cleared by the empty RetryErrorInfo", got.ErrorCategory)
	}
	if got.StatusCode != 0 {
		t.Fatalf("StatusCode = %d, want it cleared by the empty RetryErrorInfo", got.StatusCode)
	}
	if items := tracker.GetQueuedRetryItemsByType("rss"); len(items) != 0 {
		t.Fatalf("queued retry records after dead-lettering = %d, want 0", len(items))
	}
}

func TestMarkRetryDeadLetterLegacyPicksQueuedRowDeterministically(t *testing.T) {
	for i := 0; i < 20; i++ {
		tracker := newTestTracker(t)

		if !tracker.enqueueRetryForTest(
			"tie-item", "discord.a", "discord", "api", "Example", "boom", 10, time.Hour, []byte(`{"id":"row-a"}`),
		) {
			t.Fatalf("iteration %d: EnqueueRetry(discord.a) unexpectedly failed", i)
		}
		if !tracker.enqueueRetryForTest(
			"tie-item", "discord.b", "discord", "api", "Example", "boom", 10, time.Hour, []byte(`{"id":"row-b"}`),
		) {
			t.Fatalf("iteration %d: EnqueueRetry(discord.b) unexpectedly failed", i)
		}
		queueKeyB := queuedRetryKeyForDestinationForTest(t, tracker, "tie-item", "api", "discord.b")
		if !tracker.RecordRetryQueueFailure(queueKeyB, "boom again", 10, time.Hour) {
			t.Fatalf("iteration %d: RecordRetryQueueFailure(discord.b) unexpectedly failed", i)
		}

		tracker.MarkRetryDeadLetter("tie-item", "discord", "api", "Example", "terminal")

		deadLetters := tracker.GetDeadLetterItems()
		if len(deadLetters) != 1 {
			t.Fatalf("iteration %d: dead letter count = %d, want 1", i, len(deadLetters))
		}
		got := deadLetters[0]
		if got.RetryCount != 2 {
			t.Fatalf("iteration %d: RetryCount = %d, want 2 from the furthest-attempted row", i, got.RetryCount)
		}
		if string(got.Payload) != `{"id":"row-b"}` {
			t.Fatalf("iteration %d: Payload = %q, want the discord.b payload", i, string(got.Payload))
		}
		if got.DestinationID != "" {
			t.Fatalf("iteration %d: DestinationID = %q, want it to stay blank", i, got.DestinationID)
		}
		if items := tracker.GetQueuedRetryItemsByType("api"); len(items) != 0 {
			t.Fatalf("iteration %d: queued retry records = %d, want 0", i, len(items))
		}
	}
}

// attemptBudgetStep is one expected failure outcome in the retry attempt budget.
// wantRetryCount and wantDeadRetryCount use -1 for "no such row".
type attemptBudgetStep struct {
	wantEnqueued       bool
	wantQueueLen       int
	wantRetryCount     int
	wantDeadLen        int
	wantTerminalReason string
	wantDeadRetryCount int
}

func TestEnqueueRetryAttemptBudgetAcrossMaxAttempts(t *testing.T) {
	tests := []struct {
		name        string
		maxAttempts int
		steps       []attemptBudgetStep
	}{
		{
			name:        "unlimited attempts never dead letter",
			maxAttempts: 0,
			steps: []attemptBudgetStep{
				{wantEnqueued: true, wantQueueLen: 1, wantRetryCount: 1, wantDeadLen: 0, wantDeadRetryCount: -1},
				{wantEnqueued: true, wantQueueLen: 1, wantRetryCount: 2, wantDeadLen: 0, wantDeadRetryCount: -1},
				{wantEnqueued: true, wantQueueLen: 1, wantRetryCount: 3, wantDeadLen: 0, wantDeadRetryCount: -1},
			},
		},
		{
			name:        "one retry after the first send dead letters the second failure",
			maxAttempts: 1,
			steps: []attemptBudgetStep{
				{wantEnqueued: true, wantQueueLen: 1, wantRetryCount: 1, wantDeadLen: 0, wantDeadRetryCount: -1},
				{
					wantEnqueued: false, wantQueueLen: 0, wantRetryCount: -1,
					wantDeadLen: 1, wantTerminalReason: TerminalReasonMaxAttempts, wantDeadRetryCount: 2,
				},
			},
		},
		{
			name:        "two retries after the first send dead letter the third failure",
			maxAttempts: 2,
			steps: []attemptBudgetStep{
				{wantEnqueued: true, wantQueueLen: 1, wantRetryCount: 1, wantDeadLen: 0, wantDeadRetryCount: -1},
				{wantEnqueued: true, wantQueueLen: 1, wantRetryCount: 2, wantDeadLen: 0, wantDeadRetryCount: -1},
				{
					wantEnqueued: false, wantQueueLen: 0, wantRetryCount: -1,
					wantDeadLen: 1, wantTerminalReason: TerminalReasonMaxAttempts, wantDeadRetryCount: 3,
				},
			},
		},
		{
			name:        "three retries after the first send dead letter the fourth failure",
			maxAttempts: 3,
			steps: []attemptBudgetStep{
				{wantEnqueued: true, wantQueueLen: 1, wantRetryCount: 1, wantDeadLen: 0, wantDeadRetryCount: -1},
				{wantEnqueued: true, wantQueueLen: 1, wantRetryCount: 2, wantDeadLen: 0, wantDeadRetryCount: -1},
				{wantEnqueued: true, wantQueueLen: 1, wantRetryCount: 3, wantDeadLen: 0, wantDeadRetryCount: -1},
				{
					wantEnqueued: false, wantQueueLen: 0, wantRetryCount: -1,
					wantDeadLen: 1, wantTerminalReason: TerminalReasonMaxAttempts, wantDeadRetryCount: 4,
				},
			},
		},
		{
			name:        "the default five retries after the first send allow six sends",
			maxAttempts: 5,
			steps: []attemptBudgetStep{
				{wantEnqueued: true, wantQueueLen: 1, wantRetryCount: 1, wantDeadLen: 0, wantDeadRetryCount: -1},
				{wantEnqueued: true, wantQueueLen: 1, wantRetryCount: 2, wantDeadLen: 0, wantDeadRetryCount: -1},
				{wantEnqueued: true, wantQueueLen: 1, wantRetryCount: 3, wantDeadLen: 0, wantDeadRetryCount: -1},
				{wantEnqueued: true, wantQueueLen: 1, wantRetryCount: 4, wantDeadLen: 0, wantDeadRetryCount: -1},
				{wantEnqueued: true, wantQueueLen: 1, wantRetryCount: 5, wantDeadLen: 0, wantDeadRetryCount: -1},
				{
					wantEnqueued: false, wantQueueLen: 0, wantRetryCount: -1,
					wantDeadLen: 1, wantTerminalReason: TerminalReasonMaxAttempts, wantDeadRetryCount: 6,
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tracker := NewMemoryTracker()

			for i, step := range tc.steps {
				enqueued := tracker.enqueueRetryForTest(
					"item-1", "discord.ransomware", "discord", "api", "Example", "boom",
					tc.maxAttempts, time.Hour,
				)
				if enqueued != step.wantEnqueued {
					t.Fatalf("failure #%d: EnqueueRetry() = %v, want %v", i+1, enqueued, step.wantEnqueued)
				}

				queued := tracker.GetRetryItemsByType("api")
				if len(queued) != step.wantQueueLen {
					t.Fatalf("failure #%d: retry queue length = %d, want %d", i+1, len(queued), step.wantQueueLen)
				}
				gotRetryCount := -1
				if len(queued) == 1 {
					gotRetryCount = queued[0].RetryCount
				}
				if gotRetryCount != step.wantRetryCount {
					t.Fatalf("failure #%d: queued RetryCount = %d, want %d", i+1, gotRetryCount, step.wantRetryCount)
				}

				deadLetters := tracker.GetDeadLetterItems()
				if len(deadLetters) != step.wantDeadLen {
					t.Fatalf("failure #%d: dead letter count = %d, want %d", i+1, len(deadLetters), step.wantDeadLen)
				}
				gotReason := ""
				gotDeadRetryCount := -1
				if len(deadLetters) == 1 {
					gotReason = deadLetters[0].TerminalReason
					gotDeadRetryCount = deadLetters[0].RetryCount
				}
				if gotReason != step.wantTerminalReason {
					t.Fatalf("failure #%d: dead letter TerminalReason = %q, want %q", i+1, gotReason, step.wantTerminalReason)
				}
				if gotDeadRetryCount != step.wantDeadRetryCount {
					t.Fatalf("failure #%d: dead letter RetryCount = %d, want %d", i+1, gotDeadRetryCount, step.wantDeadRetryCount)
				}
			}
		})
	}
}

// TestEnqueueRetryMaxAttemptsOneAuditsQueueThenDeadLetter pins the audit trail of
// the retry budget with retry_max_attempts=1: the first send is queued, the single
// retry is dead-lettered, and both events carry the matching retry_count.
func TestEnqueueRetryMaxAttemptsOneAuditsQueueThenDeadLetter(t *testing.T) {
	tracker, dir := newTestTrackerWithDir(t)

	if !tracker.enqueueRetryForTest(
		"item-1", "discord.ransomware", "discord", "api", "Example", "boom", 1, time.Hour,
	) {
		t.Fatal("EnqueueRetry() did not queue the first failure, want one retry after the first send")
	}
	if tracker.enqueueRetryForTest(
		"item-1", "discord.ransomware", "discord", "api", "Example", "boom", 1, time.Hour,
	) {
		t.Fatal("EnqueueRetry() queued the second failure, want it dead-lettered with retry_max_attempts=1")
	}

	data, err := os.ReadFile(filepath.Join(dir, statusAuditFileName))
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", statusAuditFileName, err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("audit line count = %d, want exactly 2: %s", len(lines), data)
	}

	var queued DeliveryAuditEvent
	if err := json.Unmarshal([]byte(lines[0]), &queued); err != nil {
		t.Fatalf("Unmarshal(first audit line) error = %v: %s", err, lines[0])
	}
	if queued.Outcome != DeliveryAuditOutcomeRetryQueued {
		t.Fatalf("first audit Outcome = %q, want %q", queued.Outcome, DeliveryAuditOutcomeRetryQueued)
	}
	if queued.Reason != DeliveryAuditReasonWebhookFailure {
		t.Fatalf("first audit Reason = %q, want %q", queued.Reason, DeliveryAuditReasonWebhookFailure)
	}
	if got := queued.Details["retry_count"]; got != "1" {
		t.Fatalf("first audit details[retry_count] = %q, want %q", got, "1")
	}

	var dead DeliveryAuditEvent
	if err := json.Unmarshal([]byte(lines[1]), &dead); err != nil {
		t.Fatalf("Unmarshal(second audit line) error = %v: %s", err, lines[1])
	}
	if dead.Outcome != DeliveryAuditOutcomeDeadLettered {
		t.Fatalf("second audit Outcome = %q, want %q", dead.Outcome, DeliveryAuditOutcomeDeadLettered)
	}
	if dead.Reason != TerminalReasonMaxAttempts {
		t.Fatalf("second audit Reason = %q, want %q", dead.Reason, TerminalReasonMaxAttempts)
	}
	if got := dead.Details["retry_count"]; got != "2" {
		t.Fatalf("second audit details[retry_count] = %q, want %q", got, "2")
	}
}

// TestMarkRetryDeadLetterForDestinationAuditsRetryCountOnFirstAttemptFailure
// pins a fix in internal/status/retry_store.go:
// the mark path's dead_lettered audit event omitted retry_count, unlike the
// exhausted path (TestEnqueueRetryMaxAttemptsOneAuditsQueueThenDeadLetter
// above), so a first-attempt permanent failure -- a non-retryable webhook
// response with nothing ever queued -- left no attempt count in the record.
// Driven through the real production entry point,
// MarkRetryDeadLetterForDestinationWithErrorInfo (the only caller is
// scheduler.go's recordWebhookDeliveryFailure non-retryable branch), reading
// the same delivery_audit.jsonl a production instance would write.
func TestMarkRetryDeadLetterForDestinationAuditsRetryCountOnFirstAttemptFailure(t *testing.T) {
	tracker, dir := newTestTrackerWithDir(t)

	tracker.MarkRetryDeadLetterForDestinationWithErrorInfo(
		"item-1", "discord.ransomware", "discord", "api", "Example", "forbidden",
		RetryErrorInfo{ErrorCategory: "http_status", StatusCode: 403},
	)

	data, err := os.ReadFile(filepath.Join(dir, statusAuditFileName))
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", statusAuditFileName, err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 {
		t.Fatalf("audit line count = %d, want exactly 1: %s", len(lines), data)
	}

	var dead DeliveryAuditEvent
	if err := json.Unmarshal([]byte(lines[0]), &dead); err != nil {
		t.Fatalf("Unmarshal(audit line) error = %v: %s", err, lines[0])
	}
	if dead.Outcome != DeliveryAuditOutcomeDeadLettered {
		t.Fatalf("audit Outcome = %q, want %q", dead.Outcome, DeliveryAuditOutcomeDeadLettered)
	}
	if got, want := dead.Details["retry_count"], "0"; got != want {
		t.Fatalf("audit details[retry_count] = %q, want %q (first-attempt failure, nothing was ever queued)", got, want)
	}
}

// TestMarkRetryDeadLetterForDestinationAuditsRetryCountForInheritedQueuedRow
// is the R4 fix from the 2026-09-04 review of the batch above: the sibling
// test only exercises the branch where nothing was ever queued, so
// deadLetter.RetryCount is always exactly 0 there and a mutant that replaces
// fmt.Sprint(deadLetter.RetryCount) with the literal "0" at
// moveToDeadLetterLocked's audit event still passed the whole suite -- the
// "retry_count" key was pinned, its value was not. This drives the other
// branch: a row already queued (and so already at RetryCount 1) is found by
// queuedRetryItemForDeadLetterLocked and its history inherited, so the
// dead-lettered audit event's retry_count must be the queued row's actual
// count, not 0.
func TestMarkRetryDeadLetterForDestinationAuditsRetryCountForInheritedQueuedRow(t *testing.T) {
	tracker, dir := newTestTrackerWithDir(t)

	// A first failure that is retried (large max attempts so it queues
	// instead of dead-lettering) leaves a queued row with RetryCount 1.
	if !tracker.enqueueRetryForTest(
		"item-2", "discord.ransomware", "discord", "api", "Example2", "boom", 5, time.Hour,
	) {
		t.Fatal("EnqueueRetry() did not queue the first failure, want it queued with retry_max_attempts=5")
	}

	// A separate non-retryable failure for the same item/destination now
	// dead-letters it directly, inheriting the queued row's history.
	tracker.MarkRetryDeadLetterForDestinationWithErrorInfo(
		"item-2", "discord.ransomware", "discord", "api", "Example2", "forbidden",
		RetryErrorInfo{ErrorCategory: "http_status", StatusCode: 403},
	)

	data, err := os.ReadFile(filepath.Join(dir, statusAuditFileName))
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", statusAuditFileName, err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("audit line count = %d, want exactly 2 (queued, then dead-lettered): %s", len(lines), data)
	}

	var dead DeliveryAuditEvent
	if err := json.Unmarshal([]byte(lines[1]), &dead); err != nil {
		t.Fatalf("Unmarshal(second audit line) error = %v: %s", err, lines[1])
	}
	if dead.Outcome != DeliveryAuditOutcomeDeadLettered {
		t.Fatalf("second audit Outcome = %q, want %q", dead.Outcome, DeliveryAuditOutcomeDeadLettered)
	}
	if got, want := dead.Details["retry_count"], "1"; got != want {
		t.Fatalf("audit details[retry_count] = %q, want %q (inherited from the queued row, not the first-attempt default)", got, want)
	}
}

// TestDeadLetterWarnReasonPhraseCoversEveryTerminalReason is a direct,
// table-driven unit test of deadLetterWarnReasonPhrase covering every
// TerminalReason constant plus an unrecognized value, complementing
// TestMoveToDeadLetterWarnNamesRetryWindowExhaustedNotExhaustedRetries below,
// which exercises only the retry_window_exhausted branch behaviourally
// through the real dead-letter path.
func TestDeadLetterWarnReasonPhraseCoversEveryTerminalReason(t *testing.T) {
	tests := []struct {
		reason string
		want   string
	}{
		{TerminalReasonMaxAttempts, "after exhausting retries"},
		{TerminalReasonRetryWindow, "after its retry window elapsed"},
		{TerminalReasonInvalidFirstFailed, "due to an invalid first_failed timestamp"},
		{TerminalReasonExplicit, "on an explicit dead-letter request"},
		{TerminalReasonRetentionPruned, "by retention pruning"},
		{TerminalReasonRSSParsedPruned, "because its parsed RSS item was pruned"},
		{TerminalReasonTerminalFailure, "after a non-retryable failure"},
		// A future, currently-unrecognized TerminalReason* must fall back to
		// neutral wording, not silently reintroduce the "after exhausting
		// retries" misreport this fix removed for every reason above.
		{"unrecognized_reason", "for an unrecognized terminal reason"},
	}
	for _, tt := range tests {
		t.Run(tt.reason, func(t *testing.T) {
			if got := deadLetterWarnReasonPhrase(tt.reason); got != tt.want {
				t.Fatalf("deadLetterWarnReasonPhrase(%q) = %q, want %q", tt.reason, got, tt.want)
			}
		})
	}
}

// TestMoveToDeadLetterWarnNamesRetryWindowExhaustedNotExhaustedRetries pins a
// fix in internal/status/retry_store.go: the
// dead-letter WARN always said "after exhausting retries" regardless of the
// real terminal reason. Concrete surviving scenario: a
// quiet-hours-deferred row (EnqueueDeferredRetryForDestination, retry_count 0)
// whose retry_window elapses before its first real send, which dead-letters
// with TerminalReasonRetryWindow at retry_count 1 -- not max-attempts
// exhaustion. FirstFailed is backdated by a direct field write on the queued
// entry (there is no clock seam for statusNow(); retry_policy_test.go uses
// the same technique against retryDeadLetterDecision directly), then the
// second, real failure is driven through the public EnqueueRetry path so
// updateExistingRetryLocked -> retryDeadLetterDecision -> moveToDeadLetterLocked
// runs for real, exactly as scheduler.recordWebhookDeliveryFailure drives it.
// Log/diagnostic text only, per the finding; the operator-facing
// --list-dead-letter "last_error" wording is scheduler-owned and untouched.
func TestMoveToDeadLetterWarnNamesRetryWindowExhaustedNotExhaustedRetries(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()
	previousLevel := log.GetLevel()
	log.SetLevel(log.WarnLevel)
	defer log.SetLevel(previousLevel)

	tracker := NewMemoryTracker()
	if !tracker.EnqueueDeferredRetryForDestination(
		"item-1", "discord.ransomware", "discord", "api", "Example", "quiet hours deferral", nil,
	) {
		t.Fatal("EnqueueDeferredRetryForDestination() did not queue the deferred item")
	}

	// Backdate FirstFailed so the retry window has already elapsed by the time
	// the real send below fails, simulating quiet hours having run out the
	// clock on a row that was only ever deferred, never actually retried.
	compositeKey := makeCompositeKey("item-1", "discord.ransomware")
	tracker.mutex.Lock()
	queued := tracker.retryStore.status.RetryQueue[compositeKey]
	queued.FirstFailed = formatStatusTimestamp(statusNow().Add(-2 * time.Hour))
	tracker.retryStore.status.RetryQueue[compositeKey] = queued
	tracker.mutex.Unlock()

	if tracker.EnqueueRetry(RetryRequest{
		ItemKey:       "item-1",
		DestinationID: "discord.ransomware",
		Messenger:     Messenger("discord"),
		ItemType:      RetryItemTypeAPI,
		Title:         "Example",
		LastError:     "discord webhook provider unavailable",
		MaxAttempts:   5,
		RetryWindow:   time.Hour,
	}) {
		t.Fatal("EnqueueRetry() kept the item queued, want it dead-lettered once its retry window elapsed")
	}

	deadLetters := tracker.GetDeadLetterItems()
	if len(deadLetters) != 1 || deadLetters[0].TerminalReason != TerminalReasonRetryWindow {
		t.Fatalf("dead letters = %+v, want exactly one with TerminalReason %q", deadLetters, TerminalReasonRetryWindow)
	}
	if got := deadLetters[0].RetryCount; got != 1 {
		t.Fatalf("dead letter RetryCount = %d, want 1 (one real send after the deferral)", got)
	}

	var warnEntry *log.Entry
	for _, entry := range hook.AllEntries() {
		if entry.Message == "" {
			continue
		}
		if strings.Contains(entry.Message, "Item moved to dead letter") {
			warnEntry = entry
			break
		}
	}
	if warnEntry == nil {
		t.Fatal("missing 'Item moved to dead letter' WARN")
	}
	if strings.Contains(warnEntry.Message, "exhausting retries") {
		t.Fatalf("WARN message = %q, want it to name retry_window_exhausted, not claim exhausted retries", warnEntry.Message)
	}
	if !strings.Contains(warnEntry.Message, "retry window elapsed") {
		t.Fatalf("WARN message = %q, want it to say the retry window elapsed", warnEntry.Message)
	}
	if got, _ := warnEntry.Data["terminal_reason"].(string); got != TerminalReasonRetryWindow {
		t.Fatalf("WARN terminal_reason field = %q, want %q", got, TerminalReasonRetryWindow)
	}
}

//nolint:gocyclo // long sequential scenario asserting retry/dead-letter state transitions; not a table split candidate
func TestEnqueueRetryMaxAttemptsOneDeadLetterKeepsPayloadAndFirstFailed(t *testing.T) {
	tracker := NewMemoryTracker()
	payload := []byte(`{"post_title":"x"}`)
	retryable := true
	before := time.Now().UTC()

	req := RetryRequest{
		ItemKey:       "item-1",
		DestinationID: "discord.ransomware",
		Messenger:     Messenger("discord"),
		ItemType:      RetryItemTypeAPI,
		Title:         "Example",
		LastError:     "boom",
		ErrorInfo:     RetryErrorInfo{ErrorCategory: "server_error", StatusCode: 502, Retryable: &retryable},
		MaxAttempts:   1,
		RetryWindow:   time.Hour,
		Payload:       payload,
	}
	if !tracker.EnqueueRetry(req) {
		t.Fatal("EnqueueRetry() did not queue the first failure, want one retry after the first send")
	}
	if tracker.EnqueueRetry(req) {
		t.Fatal("EnqueueRetry() queued the second failure, want it dead-lettered with retry_max_attempts=1")
	}

	deadLetters := tracker.GetDeadLetterItems()
	if len(deadLetters) != 1 {
		t.Fatalf("dead letter count = %d, want 1", len(deadLetters))
	}
	got := deadLetters[0]

	if got.TerminalReason != TerminalReasonMaxAttempts {
		t.Fatalf("TerminalReason = %q, want %q", got.TerminalReason, TerminalReasonMaxAttempts)
	}
	if got.RetryCount != 2 {
		t.Fatalf("RetryCount = %d, want 2", got.RetryCount)
	}
	if got.DestinationID != "discord.ransomware" {
		t.Fatalf("DestinationID = %q, want %q", got.DestinationID, "discord.ransomware")
	}
	if got.Title != "Example" {
		t.Fatalf("Title = %q, want %q", got.Title, "Example")
	}
	if got.LastError != "boom" {
		t.Fatalf("LastError = %q, want %q", got.LastError, "boom")
	}
	if got.PayloadVersion != RetryPayloadVersionRansomwareEntryV1 {
		t.Fatalf("PayloadVersion = %q, want %q", got.PayloadVersion, RetryPayloadVersionRansomwareEntryV1)
	}
	if !bytes.Equal(got.Payload, payload) {
		t.Fatalf("Payload = %q, want %q", got.Payload, payload)
	}
	if got.ErrorCategory != "server_error" {
		t.Fatalf("ErrorCategory = %q, want %q", got.ErrorCategory, "server_error")
	}
	if got.StatusCode != 502 {
		t.Fatalf("StatusCode = %d, want 502", got.StatusCode)
	}
	if got.Retryable == nil || !*got.Retryable {
		t.Fatalf("Retryable = %v, want a pointer to true", got.Retryable)
	}

	after := time.Now().UTC()
	firstFailed, err := parseRequiredStatusTimestamp(got.FirstFailed)
	if err != nil {
		t.Fatalf("parseRequiredStatusTimestamp(FirstFailed %q) error = %v", got.FirstFailed, err)
	}
	if firstFailed.Before(before.Add(-5*time.Second)) || firstFailed.After(after.Add(5*time.Second)) {
		t.Fatalf("FirstFailed = %v, want it within 5s of the enqueue window [%v, %v]", firstFailed, before, after)
	}
	deadAt, err := parseRequiredStatusTimestamp(got.DeadAt)
	if err != nil {
		t.Fatalf("parseRequiredStatusTimestamp(DeadAt %q) error = %v", got.DeadAt, err)
	}
	if deadAt.Before(before.Add(-5*time.Second)) || deadAt.After(after.Add(5*time.Second)) {
		t.Fatalf("DeadAt = %v, want it within 5s of the enqueue window [%v, %v]", deadAt, before, after)
	}
}

func TestEnqueueRetryMaxAttemptsOneRSSSecondFailureDeadLetters(t *testing.T) {
	tracker := NewMemoryTracker()

	if !tracker.enqueueRetryForTest(
		"rss-item-1", "discord.rss.general", "discord", "rss", "Example", "boom", 1, time.Hour,
	) {
		t.Fatal("EnqueueRetry() did not queue the first RSS failure, want one retry after the first send")
	}
	queued := tracker.GetRetryItemsByType("rss")
	if len(queued) != 1 {
		t.Fatalf("retry queue length = %d, want 1", len(queued))
	}
	if queued[0].RetryCount != 1 {
		t.Fatalf("queued RetryCount = %d, want 1", queued[0].RetryCount)
	}
	if items := tracker.GetDeadLetterItems(); len(items) != 0 {
		t.Fatalf("dead letter count after the first failure = %d, want 0", len(items))
	}

	if tracker.enqueueRetryForTest(
		"rss-item-1", "discord.rss.general", "discord", "rss", "Example", "boom", 1, time.Hour,
	) {
		t.Fatal("EnqueueRetry() queued the second RSS failure, want it dead-lettered with retry_max_attempts=1")
	}
	if items := tracker.GetRetryItemsByType("rss"); len(items) != 0 {
		t.Fatalf("retry queue length = %d, want 0", len(items))
	}
	deadLetters := tracker.GetDeadLetterItems()
	if len(deadLetters) != 1 {
		t.Fatalf("dead letter count = %d, want 1", len(deadLetters))
	}
	if deadLetters[0].TerminalReason != TerminalReasonMaxAttempts {
		t.Fatalf("TerminalReason = %q, want %q", deadLetters[0].TerminalReason, TerminalReasonMaxAttempts)
	}
	if deadLetters[0].RetryCount != 2 {
		t.Fatalf("RetryCount = %d, want 2", deadLetters[0].RetryCount)
	}

	// RSS recovery re-attempts the item until it is pruned; every re-attempt is a no-op.
	if tracker.enqueueRetryForTest(
		"rss-item-1", "discord.rss.general", "discord", "rss", "Example", "boom", 1, time.Hour,
	) {
		t.Fatal("EnqueueRetry() re-queued an already dead-lettered RSS item")
	}
	if items := tracker.GetRetryItemsByType("rss"); len(items) != 0 {
		t.Fatalf("retry queue length after re-attempt = %d, want 0", len(items))
	}
	if deadLetters := tracker.GetDeadLetterItems(); len(deadLetters) != 1 {
		t.Fatalf("dead letter count after re-attempt = %d, want 1", len(deadLetters))
	}
}

// TestEnqueueRetryFirstFailureNeverExhaustsRetryWindow pins that a first failure is
// queued with first_failed set to the send time. The enqueue path no longer evaluates
// the retry window for a new key, so the pairing is asserted directly instead of
// through a dead-letter decision.
func TestEnqueueRetryFirstFailureNeverExhaustsRetryWindow(t *testing.T) {
	tracker := NewMemoryTracker()

	if !tracker.enqueueRetryForTest(
		"item-1", "discord.ransomware", "discord", "api", "Example", "boom", 0, time.Nanosecond,
	) {
		t.Fatal("EnqueueRetry() did not queue the first failure, want first_failed == now to keep it inside the window")
	}
	queued := tracker.GetRetryItemsByType("api")
	if len(queued) != 1 {
		t.Fatalf("retry queue length = %d, want 1", len(queued))
	}
	if queued[0].RetryCount != 1 {
		t.Fatalf("RetryCount = %d, want 1", queued[0].RetryCount)
	}
	firstFailed, err := parseRequiredStatusTimestamp(queued[0].FirstFailed)
	if err != nil {
		t.Fatalf("first_failed %q unparsable: %v", queued[0].FirstFailed, err)
	}
	if delta := statusNow().Sub(firstFailed); delta < 0 || delta > 5*time.Second {
		t.Fatalf("first_failed = %q, want the send time (delta %v)", queued[0].FirstFailed, delta)
	}
	deadLetters := tracker.GetDeadLetterItems()
	for _, deadLetter := range deadLetters {
		if deadLetter.TerminalReason == TerminalReasonRetryWindow {
			t.Fatalf("first failure dead-lettered with %q, want it queued", TerminalReasonRetryWindow)
		}
	}
	if len(deadLetters) != 0 {
		t.Fatalf("dead letter count = %d, want 0", len(deadLetters))
	}
}

func TestEnqueueRetryMaxAttemptsOneAfterQuietHoursDeferral(t *testing.T) {
	tracker := NewMemoryTracker()
	payload := []byte(`{"post_title":"deferred"}`)

	if !tracker.EnqueueDeferredRetryForDestination(
		"item-1", "discord.ransomware", "discord", "api", "Example", "quiet hours", payload,
	) {
		t.Fatal("EnqueueDeferredRetryForDestination() did not queue the deferred item")
	}
	queued := tracker.GetRetryItemsByType("api")
	if len(queued) != 1 {
		t.Fatalf("retry queue length = %d, want 1", len(queued))
	}
	if queued[0].RetryCount != 0 {
		t.Fatalf("deferred RetryCount = %d, want 0 (a quiet-hours deferral counts no attempt)", queued[0].RetryCount)
	}
	if deadLetters := tracker.GetDeadLetterItems(); len(deadLetters) != 0 {
		t.Fatalf("dead letter count after deferral = %d, want 0", len(deadLetters))
	}

	// A quiet-hours deferral counts no send, so the deferred row gets exactly the
	// same budget as a fresh one: one send plus one retry at retry_max_attempts=1.
	if !tracker.enqueueRetryForTest(
		"item-1", "discord.ransomware", "discord", "api", "Example", "boom", 1, time.Hour,
	) {
		t.Fatal("EnqueueRetry() did not queue the first real failure of a deferred row")
	}
	requeued := tracker.GetRetryItemsByType("api")
	if len(requeued) != 1 {
		t.Fatalf("retry queue length after the first real failure = %d, want 1", len(requeued))
	}
	if requeued[0].RetryCount != 1 {
		t.Fatalf("RetryCount after the first real failure = %d, want 1", requeued[0].RetryCount)
	}
	if items := tracker.GetDeadLetterItems(); len(items) != 0 {
		t.Fatalf("dead letter count after the first real failure = %d, want 0", len(items))
	}

	if tracker.enqueueRetryForTest(
		"item-1", "discord.ransomware", "discord", "api", "Example", "boom", 1, time.Hour,
	) {
		t.Fatal("EnqueueRetry() queued the second real failure of a deferred row, want it dead-lettered")
	}
	if items := tracker.GetRetryItemsByType("api"); len(items) != 0 {
		t.Fatalf("retry queue length = %d, want 0", len(items))
	}
	deadLetters := tracker.GetDeadLetterItems()
	if len(deadLetters) != 1 {
		t.Fatalf("dead letter count = %d, want 1", len(deadLetters))
	}
	if deadLetters[0].TerminalReason != TerminalReasonMaxAttempts {
		t.Fatalf("TerminalReason = %q, want %q", deadLetters[0].TerminalReason, TerminalReasonMaxAttempts)
	}
	if deadLetters[0].RetryCount != 2 {
		t.Fatalf("RetryCount = %d, want 2", deadLetters[0].RetryCount)
	}
}
