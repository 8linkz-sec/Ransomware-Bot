package scheduler

import (
	"testing"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/api"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/status"
)

// TestPendingAPIEntriesForTargetRecordsDeduplicatedAuditEvent pins Finding
// 8b: pendingAPIEntriesForTarget silently skipped an entry already sent to a
// destination with no delivery_audit.jsonl trace, unlike its own neighbour
// three lines down (the filter-mismatch case) and unlike the RSS delivery
// path's twin (rss_delivery.go). This asserts the entry is still excluded
// from unsent (unchanged behaviour) AND that a deduplicated/already_sent
// audit event is now recorded, reusing the exact outcome/reason constants
// the RSS path already uses.
//
// A second, fresh entry is included in the same call so the test also pins
// the negative case: the audit event must
// fire ONLY for the already-sent entry, not for every entry the function
// evaluates. Without this, a mutation that records the dedup event
// unconditionally for every entry -- including ones that go on to be
// delivered -- passed the whole suite undetected.
func TestPendingAPIEntriesForTargetRecordsDeduplicatedAuditEvent(t *testing.T) {
	s := newTestScheduler(t)
	webhookURL := "https://hooks.slack.test/services/dedup-audit"
	destinationID := "slack.ransomware"
	dupEntry := api.RansomwareEntry{
		ID:     "dedup-entry",
		Group:  "lockbit",
		Victim: "Dedup Audit Corp",
	}
	freshEntry := api.RansomwareEntry{
		ID:     "fresh-entry",
		Group:  "blackcat",
		Victim: "Fresh Delivery Corp",
	}
	dupKey := api.GenerateEntryKey(dupEntry)
	freshKey := api.GenerateEntryKey(freshEntry)
	s.statusTracker.MarkAPIItemSentToDestination(dupKey, api.DisplayRansomwareTitle(dupEntry), destinationID)

	unsent, filteredCount := s.pendingAPIEntriesForWebhook(
		[]api.RansomwareEntry{dupEntry, freshEntry}, webhookURL, "slack", destinationID, nil,
	)

	if filteredCount != 0 {
		t.Fatalf("filteredCount = %d, want 0 for an already-sent skip", filteredCount)
	}
	if len(unsent) != 1 || unsent[0].ID != freshEntry.ID {
		t.Fatalf("unsent = %#v, want exactly the fresh entry (dedup entry excluded)", unsent)
	}

	events := deliveryAuditEventsForTest(t, s.config.DataDir)
	if !hasDeliveryAuditEvent(events, dupKey, status.DeliveryAuditOutcomeDeduplicated, status.DeliveryAuditReasonAlreadySent) {
		t.Fatalf("no deduplicated/already_sent audit event for %q/%q; events = %+v", dupKey, destinationID, events)
	}
	if hasDeliveryAuditEvent(events, freshKey, status.DeliveryAuditOutcomeDeduplicated, status.DeliveryAuditReasonAlreadySent) {
		t.Fatalf("unexpected deduplicated/already_sent audit event for the FRESH (undelivered, unsent) entry %q; events = %+v", freshKey, events)
	}
	for _, event := range events {
		if event.ItemKey == freshKey {
			t.Fatalf("fresh entry %q must have no audit event at all from pendingAPIEntriesForTarget (only recorded on skip); got %+v", freshKey, event)
		}
	}
}

// TestPendingAPIEntriesForTargetRecordsDeadLetteredAuditEvent pins the fix
// to api_delivery.go's apiEntryDeadLettered skip: unlike
// its neighbour three lines up (the already-sent skip, fixed above by
// TestPendingAPIEntriesForTargetRecordsDeduplicatedAuditEvent), skipping an
// entry because it is already dead-lettered for this destination left no
// delivery_audit.jsonl trace at all -- hit on every cycle for any
// still-in-window dead-lettered item, so an operator inspecting the audit
// file saw the item simply vanish with no record of why. This follows that
// neighbouring fix's exact pattern: same outcome/reason shape, same
// negative-case guard (the audit event must fire ONLY for the dead-lettered
// entry, not for the entry that goes on to be delivered).
func TestPendingAPIEntriesForTargetRecordsDeadLetteredAuditEvent(t *testing.T) {
	s := newTestScheduler(t)
	webhookURL := "https://hooks.slack.test/services/dead-letter-audit"
	destinationID := "slack.ransomware"
	deadEntry := api.RansomwareEntry{
		ID:     "dead-entry",
		Group:  "lockbit",
		Victim: "Dead Letter Audit Corp",
	}
	freshEntry := api.RansomwareEntry{
		ID:     "fresh-entry-2",
		Group:  "blackcat",
		Victim: "Fresh Delivery Corp 2",
	}
	deadKey := api.GenerateEntryKey(deadEntry)
	freshKey := api.GenerateEntryKey(freshEntry)
	s.statusTracker.MarkRetryDeadLetterForDestinationWithErrorInfo(
		deadKey, destinationID, "slack", status.RetryItemTypeAPI.String(),
		api.DisplayRansomwareTitle(deadEntry), "slack webhook rejected delivery; verify or rotate the webhook URL",
		status.RetryErrorInfo{},
	)

	unsent, filteredCount := s.pendingAPIEntriesForWebhook(
		[]api.RansomwareEntry{deadEntry, freshEntry}, webhookURL, "slack", destinationID, nil,
	)

	if filteredCount != 0 {
		t.Fatalf("filteredCount = %d, want 0 for a dead-lettered skip", filteredCount)
	}
	if len(unsent) != 1 || unsent[0].ID != freshEntry.ID {
		t.Fatalf("unsent = %#v, want exactly the fresh entry (dead-lettered entry excluded)", unsent)
	}

	events := deliveryAuditEventsForTest(t, s.config.DataDir)
	if !hasDeliveryAuditEvent(events, deadKey, status.DeliveryAuditOutcomeDeadLettered, status.DeliveryAuditReasonAlreadyDeadLettered) {
		t.Fatalf("no dead_lettered/already_dead_lettered audit event for %q/%q; events = %+v", deadKey, destinationID, events)
	}
	if hasDeliveryAuditEvent(events, freshKey, status.DeliveryAuditOutcomeDeadLettered, status.DeliveryAuditReasonAlreadyDeadLettered) {
		t.Fatalf("unexpected dead_lettered/already_dead_lettered audit event for the FRESH (undelivered, unsent) entry %q; events = %+v", freshKey, events)
	}
	for _, event := range events {
		if event.ItemKey == freshKey {
			t.Fatalf("fresh entry %q must have no audit event at all from pendingAPIEntriesForTarget (only recorded on skip); got %+v", freshKey, event)
		}
	}
}
