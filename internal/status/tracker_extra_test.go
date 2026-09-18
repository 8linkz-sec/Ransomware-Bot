package status

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/rss"
)

func TestNormalizeRetentionPolicyDefaultsZeroFields(t *testing.T) {
	if got := normalizeRetentionPolicy(RetentionPolicy{}); !reflect.DeepEqual(got, DefaultRetentionPolicy()) {
		t.Fatalf("normalizeRetentionPolicy(zero) = %+v, want defaults %+v", got, DefaultRetentionPolicy())
	}

	custom := RetentionPolicy{
		MaxAPISentItems:    1,
		MaxRSSParsedItems:  2,
		RSSParsedMaxAge:    3 * time.Hour,
		MaxRSSSentItems:    4,
		MaxRetryQueueItems: 5,
		RetryQueueMaxAge:   6 * time.Hour,
		MaxDeadLetterItems: 7,
		DeadLetterMaxAge:   8 * time.Hour,
		AuditLogMaxSizeMB:  9,
		AuditLogMaxBackups: 10,
		AuditLogMaxAgeDays: 11,
		AuditLogCompress:   boolPtr(false),
	}
	if got := normalizeRetentionPolicy(custom); !reflect.DeepEqual(got, custom) {
		t.Fatalf("normalizeRetentionPolicy(custom) = %+v, want unchanged %+v", got, custom)
	}
}

// TestDefaultRetentionPolicyReturnsFreshCompressPointer pins the fresh-pointer
// invariant documented on DefaultRetentionPolicy: AuditLogCompress must be a
// newly allocated *bool on every call, never a shared package-level pointer.
// A shared pointer would let any single caller that flips *p.AuditLogCompress
// silently change gzip behaviour for every tracker built without an explicit
// compress setting, process-wide.
func TestDefaultRetentionPolicyReturnsFreshCompressPointer(t *testing.T) {
	a := DefaultRetentionPolicy()
	b := DefaultRetentionPolicy()
	if a.AuditLogCompress == nil || b.AuditLogCompress == nil {
		t.Fatalf("AuditLogCompress must not be nil: a=%v b=%v", a.AuditLogCompress, b.AuditLogCompress)
	}
	if a.AuditLogCompress == b.AuditLogCompress {
		t.Fatalf("DefaultRetentionPolicy() returned the same *bool for AuditLogCompress across two calls (%p == %p); every call must allocate a fresh pointer", a.AuditLogCompress, b.AuditLogCompress)
	}
	if *a.AuditLogCompress != true || *b.AuditLogCompress != true {
		t.Fatalf("AuditLogCompress default value = %v/%v, want true/true", *a.AuditLogCompress, *b.AuditLogCompress)
	}
}

func TestUpdateRetentionNormalizesPolicy(t *testing.T) {
	tracker := NewMemoryTracker()

	tracker.UpdateRetention(RetentionPolicy{MaxAPISentItems: 7})

	tracker.mutex.RLock()
	got := tracker.retention
	tracker.mutex.RUnlock()
	if got.MaxAPISentItems != 7 {
		t.Fatalf("MaxAPISentItems = %d, want 7", got.MaxAPISentItems)
	}
	if got.MaxRSSParsedItems != defaultMaxRSSParsedItems {
		t.Fatalf("MaxRSSParsedItems = %d, want default %d", got.MaxRSSParsedItems, defaultMaxRSSParsedItems)
	}
}

func TestRSSEntryFromStoredRejectsInvalidPublished(t *testing.T) {
	_, err := RSSEntryFromStored(StoredRSSEntry{Key: "k", Published: "not-a-time"})
	if err == nil || !strings.Contains(err.Error(), "invalid stored RSS published timestamp") {
		t.Fatalf("RSSEntryFromStored() error = %v, want invalid published timestamp", err)
	}
}

func TestUnsentRSSItemRSSEntryRoundTrip(t *testing.T) {
	valid := UnsentRSSItemFromStored(StoredRSSEntry{
		Key:       "k",
		FeedURL:   "https://example.test/feed.xml",
		Title:     "Example",
		Published: "2025-01-15T10:30:00Z",
	})
	if valid.InvalidReason != "" {
		t.Fatalf("InvalidReason = %q, want empty for valid stored entry", valid.InvalidReason)
	}
	entry, err := valid.RSSEntry()
	if err != nil {
		t.Fatalf("RSSEntry() error = %v", err)
	}
	if entry.Title != "Example" || entry.FeedURL != "https://example.test/feed.xml" {
		t.Fatalf("RSSEntry() = %+v, want stored fields", entry)
	}

	invalid := UnsentRSSItemFromStored(StoredRSSEntry{Key: "k", Published: "garbage"})
	if invalid.InvalidReason == "" {
		t.Fatal("InvalidReason empty for invalid published timestamp")
	}
	if _, err := invalid.RSSEntry(); err == nil || err.Error() != invalid.InvalidReason {
		t.Fatalf("RSSEntry() error = %v, want %q", err, invalid.InvalidReason)
	}
}

func TestParseStoredRSSTimestampHandlesEmptyAndInvalid(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		wantErr bool
	}{
		{name: "empty", value: "", wantErr: false},
		{name: "whitespace", value: "   ", wantErr: false},
		{name: "valid", value: "2025-01-15T10:30:00Z", wantErr: false},
		{name: "invalid", value: "garbage", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsed, err := ParseStoredRSSTimestamp(tt.value)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseStoredRSSTimestamp(%q) error = %v, wantErr %v", tt.value, err, tt.wantErr)
			}
			if strings.TrimSpace(tt.value) == "" && !parsed.IsZero() {
				t.Fatalf("ParseStoredRSSTimestamp(%q) = %v, want zero time", tt.value, parsed)
			}
		})
	}
}

func TestBoundedRSSCategoriesSkipsEmptyEntries(t *testing.T) {
	got := boundedRSSCategories([]string{"  ", "", "news"})
	if len(got) != 1 || got[0] != "news" {
		t.Fatalf("boundedRSSCategories() = %v, want [news]", got)
	}
}

func TestStatusSummaryCountsOnlyFailingFeeds(t *testing.T) {
	tracker := NewMemoryTracker()
	tracker.UpdateFeedStatus("https://ok.test/feed.xml", true, 3, "")
	tracker.UpdateFeedStatus("https://bad.test/feed.xml", false, 0, "fetch failed")

	summary := tracker.StatusSummary()
	if summary.RSSFeeds != 2 {
		t.Fatalf("RSSFeeds = %d, want 2", summary.RSSFeeds)
	}
	if summary.RSSFeedErrors != 1 {
		t.Fatalf("RSSFeedErrors = %d, want 1", summary.RSSFeedErrors)
	}
	if summary.LastRSSError != "fetch failed" {
		t.Fatalf("LastRSSError = %q, want fetch failed", summary.LastRSSError)
	}
}

func TestCanAcquireStateLockReportsHeldLock(t *testing.T) {
	tracker := NewMemoryTracker()

	if !tracker.CanAcquireStateLockForStatusWrite() {
		t.Fatal("CanAcquireStateLockForStatusWrite() = false on idle tracker")
	}

	tracker.mutex.Lock()
	held := tracker.CanAcquireStateLockForStatusWrite()
	tracker.mutex.Unlock()
	if held {
		t.Fatal("CanAcquireStateLockForStatusWrite() = true while lock was held")
	}
}

func TestAPISentItemSnapshotFallsBackToLegacyDestinations(t *testing.T) {
	tracker := NewMemoryTracker()

	if _, ok := tracker.APISentItemSnapshot("item-1", "discord.ransomware"); ok {
		t.Fatal("APISentItemSnapshot() found a marker before any send")
	}

	legacyURL := "https://discord.com/api/webhooks/123456789012345678/test-token"
	tracker.MarkAPIItemSentToWebhook("item-1", "Example", legacyURL)

	snapshot, ok := tracker.APISentItemSnapshot("item-1", "discord.ransomware", "", legacyURL)
	if !ok {
		t.Fatal("APISentItemSnapshot() did not fall back to the legacy destination")
	}
	if snapshot.SentAt.IsZero() {
		t.Fatal("APISentItemSnapshot() returned zero SentAt")
	}
}

func TestRSSSentItemSnapshotFallsBackToLegacyDestinations(t *testing.T) {
	tracker := NewMemoryTracker()

	if _, ok := tracker.RSSSentItemSnapshot("item-1", "slack.rss"); ok {
		t.Fatal("RSSSentItemSnapshot() found a marker before any send")
	}

	legacyURL := "https://hooks.slack.com/services/T12345678/B12345678/testtoken1234567890"
	tracker.MarkRSSItemSentToWebhook("item-1", "Example", "Example Feed", legacyURL)

	snapshot, ok := tracker.RSSSentItemSnapshot("item-1", "slack.rss", "", legacyURL)
	if !ok {
		t.Fatal("RSSSentItemSnapshot() did not fall back to the legacy destination")
	}
	if snapshot.Title != "Example" || snapshot.ItemKey != "item-1" {
		t.Fatalf("RSSSentItemSnapshot() = %+v, want stored title and item key", snapshot)
	}
}

// TestRSSSentItemSnapshotReportsDerivedMarkers is the contract test for the
// additive "derived" flag: RSSSentItemSnapshot is a bare struct conversion of
// rssWebhookSentInfo, so the two structs must keep identical field names, types
// and order, and every marker kind must come back out of the snapshot with the
// flags and titles it was written with -- including through the
// legacy-destination fallback and through a backfill alias.
func TestRSSSentItemSnapshotReportsDerivedMarkers(t *testing.T) {
	const destination = "slack.rss.general"
	legacyURL := "https://hooks.slack.com/services/T12345678/B12345678/testtoken1234567890"
	published := time.Date(2026, 3, 4, 8, 15, 0, 0, time.UTC)
	item := StoredRSSEntry{
		Key:         "derived-snapshot-item",
		FeedURL:     "https://example.test/feed.xml",
		FeedType:    "general",
		Title:       "Example Report",
		Link:        "https://example.test/articles/example-report",
		Description: "example body",
		Published:   published.Format(time.RFC3339Nano),
		FeedTitle:   "Example Feed",
		ParsedAt:    published.Format(time.RFC3339Nano),
	}

	tests := []struct {
		name          string
		mark          func(tracker *Tracker)
		legacy        []string
		wantSkipped   bool
		wantDerived   bool
		wantTitle     string
		wantFeedTitle string
	}{
		{
			name: "genuine delivery marker",
			mark: func(tracker *Tracker) {
				tracker.MarkRSSItemSentToDestination(item.Key, item.Title, item.FeedTitle, destination)
			},
			wantTitle:     item.Title,
			wantFeedTitle: item.FeedTitle,
		},
		{
			name: "filter mismatch skip marker",
			mark: func(tracker *Tracker) {
				tracker.MarkRSSItemSkippedForDestination(item.Key, destination)
			},
			wantSkipped: true,
		},
		{
			name: "content signature suppression marker",
			mark: func(tracker *Tracker) {
				tracker.MarkRSSItemDedupedForDestination(item.Key, item.Title, item.FeedTitle, destination)
			},
			wantDerived:   true,
			wantTitle:     item.Title,
			wantFeedTitle: item.FeedTitle,
		},
		{
			name: "content signature suppression marker through the legacy destination fallback",
			mark: func(tracker *Tracker) {
				tracker.MarkRSSItemDedupedForDestination(item.Key, item.Title, item.FeedTitle, legacyURL)
			},
			legacy:        []string{"", legacyURL},
			wantDerived:   true,
			wantTitle:     item.Title,
			wantFeedTitle: item.FeedTitle,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tracker := NewMemoryTracker()
			tt.mark(tracker)

			snapshot, ok := tracker.RSSSentItemSnapshot(item.Key, destination, tt.legacy...)
			if !ok {
				t.Fatal("RSSSentItemSnapshot() found no marker after it was written")
			}
			if snapshot.Skipped != tt.wantSkipped || snapshot.Derived != tt.wantDerived {
				t.Fatalf("snapshot flags = skipped %t / derived %t, want skipped %t / derived %t",
					snapshot.Skipped, snapshot.Derived, tt.wantSkipped, tt.wantDerived)
			}
			if snapshot.Title != tt.wantTitle || snapshot.FeedTitle != tt.wantFeedTitle {
				t.Fatalf("snapshot titles = %q / %q, want %q / %q",
					snapshot.Title, snapshot.FeedTitle, tt.wantTitle, tt.wantFeedTitle)
			}
			if snapshot.ItemKey != item.Key {
				t.Fatalf("snapshot item key = %q, want %q", snapshot.ItemKey, item.Key)
			}
		})
	}

	t.Run("backfill alias of a genuine delivery stays unflagged", func(t *testing.T) {
		dir := t.TempDir()
		tracker, err := NewTracker(dir)
		if err != nil {
			t.Fatalf("NewTracker() error = %v", err)
		}
		tracker.MarkRSSItemsParsed(item.FeedURL, map[string]StoredRSSEntry{item.Key: item})
		tracker.MarkRSSItemSentToDestination(item.Key, item.Title, item.FeedTitle, destination)
		if err := tracker.SavePendingChanges(); err != nil {
			t.Fatalf("SavePendingChanges() error = %v", err)
		}

		reloaded, err := NewTracker(dir)
		if err != nil {
			t.Fatalf("NewTracker(reload) error = %v", err)
		}
		entry, err := RSSEntryFromStored(item)
		if err != nil {
			t.Fatalf("RSSEntryFromStored() error = %v", err)
		}
		alias := rss.GenerateEntryContentSignature(entry)

		snapshot, ok := reloaded.RSSSentItemSnapshot(alias, destination)
		if !ok {
			t.Fatal("the load-time backfill created no content-signature alias for a genuine delivery")
		}
		if snapshot.Skipped || snapshot.Derived {
			t.Fatalf("backfill alias flags = skipped %t / derived %t, want both false", snapshot.Skipped, snapshot.Derived)
		}
		if snapshot.Title != item.Title || snapshot.FeedTitle != item.FeedTitle {
			t.Fatalf("backfill alias titles = %q / %q, want %q / %q",
				snapshot.Title, snapshot.FeedTitle, item.Title, item.FeedTitle)
		}
	})
}

func TestRecordDeliveryAuditEventNilTrackerIsSafe(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("RecordDeliveryAuditEvent on nil tracker panicked: %v", r)
		}
	}()

	var tracker *Tracker
	tracker.RecordDeliveryAuditEvent(DeliveryAuditEvent{EventType: DeliveryAuditEventCleanup})
}

func TestRecordDeliveryAuditEventSkipsBlankEventType(t *testing.T) {
	tracker, dir := newTestTrackerWithDir(t)

	tracker.RecordDeliveryAuditEvent(DeliveryAuditEvent{EventType: "   "})

	if _, err := os.Stat(filepath.Join(dir, statusAuditFileName)); !os.IsNotExist(err) {
		t.Fatalf("audit file exists after blank event type, stat err = %v", err)
	}
}

func TestRecordDeliveryAuditEventNormalizesTimestampCountAndDetails(t *testing.T) {
	tracker, dir := newTestTrackerWithDir(t)

	occurred := time.Date(2026, 2, 3, 4, 5, 6, 0, time.FixedZone("CET", 3600))
	details := make(map[string]string, maxAuditDetails+5)
	for i := 0; i < maxAuditDetails+5; i++ {
		details[fmt.Sprintf("key-%02d", i)] = "value"
	}

	tracker.RecordDeliveryAuditEvent(DeliveryAuditEvent{
		EventType:  DeliveryAuditEventDeliveryState,
		OccurredAt: occurred,
		Count:      -3,
		Details:    details,
	})

	data, err := os.ReadFile(filepath.Join(dir, statusAuditFileName))
	if err != nil {
		t.Fatalf("ReadFile(audit) error = %v", err)
	}
	var event DeliveryAuditEvent
	if err := json.Unmarshal(data, &event); err != nil {
		t.Fatalf("Unmarshal(audit line) error = %v: %s", err, data)
	}
	if !event.OccurredAt.Equal(occurred) {
		t.Fatalf("OccurredAt = %v, want %v", event.OccurredAt, occurred)
	}
	if !strings.Contains(string(data), `"occurred_at":"2026-02-03T03:05:06Z"`) {
		t.Fatalf("audit line does not contain UTC occurred_at: %s", data)
	}
	if event.Count != 0 {
		t.Fatalf("Count = %d, want negative count clamped to 0", event.Count)
	}
	if len(event.Details) != maxAuditDetails {
		t.Fatalf("details size = %d, want capped at %d", len(event.Details), maxAuditDetails)
	}
	if _, ok := event.Details["key-00"]; !ok {
		t.Fatal("details lost the first sorted key")
	}
	if _, ok := event.Details[fmt.Sprintf("key-%02d", maxAuditDetails+4)]; ok {
		t.Fatal("details kept keys beyond the cap")
	}
}

func TestRecordDeliveryAuditEventWarnsWhenAppendFails(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()

	tracker, dir := newTestTrackerWithDir(t)
	if err := os.Mkdir(filepath.Join(dir, statusAuditFileName), 0700); err != nil {
		t.Fatalf("Mkdir(audit blocker) error = %v", err)
	}

	tracker.RecordDeliveryAuditEvent(DeliveryAuditEvent{
		EventType: DeliveryAuditEventDeliveryState,
		ItemKey:   "item-1",
	})

	for _, entry := range hook.AllEntries() {
		if entry.Message == "Failed to append delivery audit event" {
			return
		}
	}
	t.Fatal("missing warn log for failed audit append")
}

func TestEnsureAPIStatusLoadedIsNoopWhenAlreadyLoaded(t *testing.T) {
	tracker := newTestTracker(t)

	if err := tracker.EnsureAPIStatusLoaded(); err != nil {
		t.Fatalf("EnsureAPIStatusLoaded() error = %v, want nil for already loaded tracker", err)
	}
}

func TestLegacyAPIFetchedItemsLifecycle(t *testing.T) {
	if payloads := NewMemoryTracker().LegacyAPIFetchedItemPayloads(); payloads != nil {
		t.Fatalf("LegacyAPIFetchedItemPayloads() = %v, want nil without legacy items", payloads)
	}

	dir := t.TempDir()
	apiJSON := `{"last_updated":"2026-01-01T00:00:00Z","sent_items":null,` +
		`"fetched_items":[{"post_title":"a"},{"post_title":"b"}]}`
	if err := os.WriteFile(filepath.Join(dir, statusAPIFileName), []byte(apiJSON), 0600); err != nil {
		t.Fatalf("WriteFile(api status) error = %v", err)
	}

	tracker, err := NewTracker(dir)
	if err != nil {
		t.Fatalf("NewTracker() error = %v", err)
	}

	payloads := tracker.LegacyAPIFetchedItemPayloads()
	if len(payloads) != 2 {
		t.Fatalf("legacy payload count = %d, want 2", len(payloads))
	}
	payloads[0][0] = 'X' // mutate the returned clone
	if again := tracker.LegacyAPIFetchedItemPayloads(); string(again[0]) != `{"post_title":"a"}` {
		t.Fatalf("legacy payloads were not cloned: %s", again[0])
	}

	tracker.UpdateAPIStatus(true, 2, "")
	if err := tracker.SavePendingChanges(); err != nil {
		t.Fatalf("SavePendingChanges() error = %v", err)
	}
	expectFileContains(t, filepath.Join(dir, statusAPIFileName), "fetched_items")

	tracker.ClearLegacyAPIFetchedItems()
	if payloads := tracker.LegacyAPIFetchedItemPayloads(); payloads != nil {
		t.Fatalf("legacy payloads after clear = %v, want nil", payloads)
	}
	if err := tracker.SavePendingChanges(); err != nil {
		t.Fatalf("SavePendingChanges() after clear error = %v", err)
	}
	expectFileNotContains(t, filepath.Join(dir, statusAPIFileName), "fetched_items")

	tracker.ClearLegacyAPIFetchedItems() // idempotent when nothing is left
	if tracker.DirtyState().API {
		t.Fatal("second ClearLegacyAPIFetchedItems() marked API status dirty")
	}
}

func TestShouldPollRSSFeedZeroNowUsesCurrentClock(t *testing.T) {
	tracker := NewMemoryTracker()
	feedURL := "https://example.test/feed.xml"

	for i := 0; i < rssFailureCooldownThreshold; i++ {
		tracker.UpdateFeedStatus(feedURL, false, 0, "fetch failed")
	}

	if tracker.ShouldPollRSSFeed(feedURL, time.Time{}) {
		t.Fatal("ShouldPollRSSFeed(zero now) ignored the active failure cooldown")
	}
}

func TestNextRSSFeedAttemptAfterBackoff(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		failures  int
		wantDelay time.Duration
		wantNil   bool
	}{
		{name: "below threshold", failures: rssFailureCooldownThreshold - 1, wantNil: true},
		{name: "at threshold", failures: rssFailureCooldownThreshold, wantDelay: rssFailureCooldownBase},
		{name: "doubles", failures: rssFailureCooldownThreshold + 1, wantDelay: 2 * rssFailureCooldownBase},
		{name: "caps at max", failures: rssFailureCooldownThreshold + 20, wantDelay: rssFailureCooldownMax},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := nextRSSFeedAttemptAfter(now, tt.failures)
			if tt.wantNil {
				if got != nil {
					t.Fatalf("nextRSSFeedAttemptAfter(%d) = %v, want nil", tt.failures, got)
				}
				return
			}
			if got == nil {
				t.Fatalf("nextRSSFeedAttemptAfter(%d) = nil, want delay %v", tt.failures, tt.wantDelay)
			}
			if delay := got.Sub(now); delay != tt.wantDelay {
				t.Fatalf("nextRSSFeedAttemptAfter(%d) delay = %v, want %v", tt.failures, delay, tt.wantDelay)
			}
		})
	}
}

func TestPruneRSSFeedStatusEdgeCases(t *testing.T) {
	tracker := NewMemoryTracker()

	if removed := tracker.PruneRSSFeedStatus([]string{"https://a.test/feed.xml"}); removed != 0 {
		t.Fatalf("PruneRSSFeedStatus() on empty tracker = %d, want 0", removed)
	}

	tracker.UpdateFeedStatus("https://keep.test/feed.xml", true, 1, "")
	tracker.UpdateFeedStatus("https://drop.test/feed.xml", true, 1, "")

	if removed := tracker.PruneRSSFeedStatus([]string{"", "https://keep.test/feed.xml"}); removed != 1 {
		t.Fatalf("PruneRSSFeedStatus() = %d, want 1", removed)
	}
	if _, ok := tracker.GetRSSFeedInfo("https://keep.test/feed.xml"); !ok {
		t.Fatal("active feed status was pruned")
	}
	if _, ok := tracker.GetRSSFeedInfo("https://drop.test/feed.xml"); ok {
		t.Fatal("inactive feed status was retained")
	}
}

func TestIsAPIItemSentToDestinationIgnoresBlankAndUnknownLegacy(t *testing.T) {
	tracker := NewMemoryTracker()
	tracker.MarkAPIItemSentToDestination("item-1", "Example", "discord.a")

	if tracker.IsAPIItemSentToDestination("item-1", "discord.b", "", "discord.b", "discord.unknown") {
		t.Fatal("IsAPIItemSentToDestination() matched via blank or unknown legacy destinations")
	}
}

func TestAPILegacyAliasIsNotAdoptedBySiblingDestination(t *testing.T) {
	const (
		itemKey    = "api-item-shared"
		webhookURL = "https://hooks.slack.com/services/T12345678/B12345678/sharedtoken1234567"
		owner      = "slack.ransomware"
		sibling    = "slack.ransomware.2"
	)

	tests := []struct {
		name           string
		writeAlias     func(tracker *Tracker)
		wantAliasOwner string
		migrateAs      string
		ask            string
		want           bool
	}{
		{
			name: "owner stamped alias is not adopted by sibling destination",
			writeAlias: func(tracker *Tracker) {
				tracker.MarkAPIItemSentToDestination(itemKey, "Example", owner)
				tracker.MarkAPIItemSentToLegacyDestination(itemKey, "Example", webhookURL, owner)
			},
			wantAliasOwner: owner,
			ask:            sibling,
			want:           false,
		},
		{
			name: "owner stamped alias still applies to its own destination",
			writeAlias: func(tracker *Tracker) {
				tracker.MarkAPIItemSentToDestination(itemKey, "Example", owner)
				tracker.MarkAPIItemSentToLegacyDestination(itemKey, "Example", webhookURL, owner)
			},
			wantAliasOwner: owner,
			ask:            owner,
			want:           true,
		},
		{
			name: "genuine legacy alias migrates for the asking destination",
			writeAlias: func(tracker *Tracker) {
				tracker.MarkAPIItemSentToWebhook(itemKey, "Example", webhookURL)
			},
			wantAliasOwner: "",
			ask:            owner,
			want:           true,
		},
		{
			name: "genuine legacy alias stays owner-less so every destination on that URL still adopts",
			writeAlias: func(tracker *Tracker) {
				tracker.MarkAPIItemSentToWebhook(itemKey, "Example", webhookURL)
			},
			wantAliasOwner: "",
			migrateAs:      owner,
			ask:            sibling,
			want:           true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tracker := NewMemoryTracker()
			tt.writeAlias(tracker)
			// The alias marker itself carries the owner stamp the read side
			// discriminates on; assert it before any migration rewrites state.
			aliasSnapshot, ok := tracker.APISentItemSnapshot(itemKey, webhookURL)
			if !ok {
				t.Fatal("no sent marker stored under the legacy webhook-URL key")
			}
			if aliasSnapshot.DestinationID != tt.wantAliasOwner {
				t.Fatalf("legacy alias DestinationID = %q, want %q", aliasSnapshot.DestinationID, tt.wantAliasOwner)
			}
			if tt.migrateAs != "" && !tracker.IsAPIItemSentToDestination(itemKey, tt.migrateAs, webhookURL) {
				t.Fatalf("IsAPIItemSentToDestination(%q) = false, want the legacy alias to migrate first", tt.migrateAs)
			}

			if got := tracker.IsAPIItemSentToDestination(itemKey, tt.ask, webhookURL); got != tt.want {
				t.Fatalf("IsAPIItemSentToDestination(%q) = %v, want %v", tt.ask, got, tt.want)
			}
			snapshot, ok := tracker.APISentItemSnapshot(itemKey, tt.ask)
			if ok != tt.want {
				t.Fatalf("APISentItemSnapshot(%q) ok = %v (%+v), want %v", tt.ask, ok, snapshot, tt.want)
			}
			// Migration never rewrites the alias itself, so a true legacy
			// marker stays adoptable by every destination on that URL.
			aliasAfter, ok := tracker.APISentItemSnapshot(itemKey, webhookURL)
			if !ok {
				t.Fatal("legacy webhook-URL marker disappeared")
			}
			if aliasAfter.DestinationID != tt.wantAliasOwner {
				t.Fatalf("legacy alias DestinationID after the read = %q, want it unchanged at %q",
					aliasAfter.DestinationID, tt.wantAliasOwner)
			}
		})
	}
}

// TestAPIRenamedDestinationDoesNotAdoptOwnerStampedAlias pins the accepted
// trade-off of stamping the owning destination onto the legacy webhook-URL
// alias: destination suffixes are positional, so removing or reordering
// endpoints changes a destination ID and the alias no longer suppresses the
// item for it. The result is one duplicate alert per still-retained item on the
// moved endpoint, which is deliberate — a duplicate is recoverable, a silently
// dropped victim disclosure is not. See CHANGELOG "1.2.0" for the
// accepted trade-off (internal/config/config.go L1030, positional suffixes).
func TestAPIRenamedDestinationDoesNotAdoptOwnerStampedAlias(t *testing.T) {
	const (
		itemKey    = "api-item-reordered"
		webhookURL = "https://hooks.slack.com/services/T12345678/B12345678/reorderedtoken123"
		oldID      = "slack.ransomware"
		newID      = "slack.ransomware.2"
	)

	tracker := NewMemoryTracker()
	tracker.MarkAPIItemSentToDestination(itemKey, "Example", oldID)
	tracker.MarkAPIItemSentToLegacyDestination(itemKey, "Example", webhookURL, oldID)

	// Same physical endpoint, new positional suffix after a config reorder.
	if tracker.IsAPIItemSentToDestination(itemKey, newID, webhookURL) {
		t.Fatal("reordered destination adopted the owner-stamped alias; the documented trade-off is one duplicate, not suppression")
	}
	if snapshot, ok := tracker.APISentItemSnapshot(itemKey, newID); ok {
		t.Fatalf("reordered destination recorded a delivery it never made: %+v", snapshot)
	}
	if !tracker.IsAPIItemSentToDestination(itemKey, oldID, webhookURL) {
		t.Fatal("the owning destination lost its own suppression")
	}
}

// TestAPILegacyAliasWithBlankOwnerFromDiskIsTreatedAsLegacy loads a state file
// whose legacy alias records a whitespace-only destination_id. Such a value can
// only reach the tracker from a hand-edited or foreign api_status.json, and it
// must read as "no recorded destination" (adoptable) rather than as an owner
// nothing can match, which would replay the item to every destination.
func TestAPILegacyAliasWithBlankOwnerFromDiskIsTreatedAsLegacy(t *testing.T) {
	const (
		itemKey    = "api-item-from-disk"
		webhookURL = "https://hooks.slack.com/services/T12345678/B12345678/fromdisktoken1234"
		asking     = "slack.ransomware"
	)

	dir := t.TempDir()
	payload := fmt.Sprintf(
		`{"last_updated":"2026-09-03T00:00:00Z","last_check":"2026-09-03T00:00:00Z","entries_found":0,`+
			`"sent_items":{%q:{"sent_at":"2026-09-03T00:00:00Z","destination_id":"   "}}}`,
		makeCompositeKey(itemKey, webhookURL),
	)
	if err := os.WriteFile(filepath.Join(dir, statusAPIFileName), []byte(payload), 0600); err != nil {
		t.Fatalf("WriteFile(api_status.json) error = %v", err)
	}

	tracker, err := NewTracker(dir)
	if err != nil {
		t.Fatalf("NewTracker() error = %v", err)
	}
	if !tracker.IsAPIItemSentToDestination(itemKey, asking, webhookURL) {
		t.Fatal("whitespace-only recorded destination was not treated as a legacy marker")
	}
}

func TestMarkAPIItemSentRebuildsNilSentItems(t *testing.T) {
	tracker := NewMemoryTracker()
	tracker.mutex.Lock()
	tracker.apiStatus.SentItems = nil
	tracker.mutex.Unlock()

	tracker.MarkAPIItemSentToDestination("item-1", "Example", "discord.a")

	if !tracker.IsAPIItemSentToDestination("item-1", "discord.a") {
		t.Fatal("sent marker missing after nil SentItems map rebuild")
	}
}

func TestSeedAPISentItemRebuildsNilSentItems(t *testing.T) {
	tracker := NewMemoryTracker()
	tracker.mutex.Lock()
	tracker.apiStatus.SentItems = nil
	tracker.mutex.Unlock()

	sentAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	tracker.seedAPISentItemForRetention("item-1", "discord.a", sentAt)

	snapshot, ok := tracker.APISentItemSnapshot("item-1", "discord.a")
	if !ok || !snapshot.SentAt.Equal(sentAt) {
		t.Fatalf("APISentItemSnapshot() = %+v ok=%v, want seeded SentAt %v", snapshot, ok, sentAt)
	}
}

func TestMarkRSSItemsParsedEmptyBatchIsNoop(t *testing.T) {
	tracker := NewMemoryTracker()

	tracker.MarkRSSItemsParsed("https://example.test/feed.xml", nil)

	if tracker.DirtyState().RSS {
		t.Fatal("empty parsed batch marked RSS status dirty")
	}
}

func TestSeedRSSParsedItemsForRetentionEdgeCases(t *testing.T) {
	tracker := NewMemoryTracker()

	tracker.seedRSSParsedItemsForRetention(nil)
	if tracker.DirtyState().RSS {
		t.Fatal("empty seed marked RSS status dirty")
	}

	tracker.mutex.Lock()
	tracker.rssStatus.ParsedItems = nil
	tracker.mutex.Unlock()

	tracker.seedRSSParsedItemsForRetention([]StoredRSSEntry{{Key: "k", FeedURL: "https://f.test/feed.xml"}})
	if items := tracker.RSSParsedItemsSnapshot(); len(items) != 1 || items[0].Key != "k" {
		t.Fatalf("parsed items after seed = %+v, want single seeded entry", items)
	}
}

func TestMarkRSSItemParsedRebuildsNilParsedItems(t *testing.T) {
	tracker := NewMemoryTracker()
	tracker.mutex.Lock()
	tracker.rssStatus.ParsedItems = nil
	tracker.mutex.Unlock()

	tracker.MarkRSSItemParsed("https://f.test/feed.xml", "k", StoredRSSEntry{Title: "Example"})

	if !tracker.IsRSSItemParsed("https://f.test/feed.xml", "k") {
		t.Fatal("parsed marker missing after nil ParsedItems rebuild")
	}
}

func TestSetRSSSentItemTimeMissingMarkerReturnsFalse(t *testing.T) {
	tracker := NewMemoryTracker()

	if tracker.setRSSSentItemTime("missing", "discord.a", statusNow()) {
		t.Fatal("setRSSSentItemTime() reported success for a missing marker")
	}
}

func TestMinimizeRSSParsedItemMissingReturnsFalse(t *testing.T) {
	tracker := NewMemoryTracker()

	if tracker.MinimizeRSSParsedItem("https://f.test/feed.xml", "missing") {
		t.Fatal("MinimizeRSSParsedItem() reported success for a missing item")
	}
}

func TestMinimizeRSSParsedItemBackfillsEmptyParsedAt(t *testing.T) {
	tracker := NewMemoryTracker()
	tracker.seedRSSParsedItemsForRetention([]StoredRSSEntry{{
		Key:     "k",
		FeedURL: "https://f.test/feed.xml",
		Title:   "Example",
	}})

	if !tracker.MinimizeRSSParsedItem("https://f.test/feed.xml", "k") {
		t.Fatal("MinimizeRSSParsedItem() failed for seeded item")
	}

	items := tracker.RSSParsedItemsSnapshot()
	if len(items) != 1 {
		t.Fatalf("parsed items = %d, want 1", len(items))
	}
	if items[0].Title != "" {
		t.Fatalf("minimized Title = %q, want cleared", items[0].Title)
	}
	if items[0].ParsedAt == "" {
		t.Fatal("minimized entry without ParsedAt was not backfilled")
	}
}

func TestSetRSSParsedItemFeedTypeEarlyReturns(t *testing.T) {
	tracker := NewMemoryTracker()
	tracker.MarkRSSItemParsed("https://f.test/feed.xml", "k", StoredRSSEntry{Title: "Example"})

	if tracker.SetRSSParsedItemFeedType("https://f.test/feed.xml", "k", "") {
		t.Fatal("SetRSSParsedItemFeedType() accepted an empty feed type")
	}
	if tracker.SetRSSParsedItemFeedType("https://f.test/feed.xml", "missing", "news") {
		t.Fatal("SetRSSParsedItemFeedType() reported success for a missing item")
	}
	if !tracker.SetRSSParsedItemFeedType("https://f.test/feed.xml", "k", "news") {
		t.Fatal("SetRSSParsedItemFeedType() failed for a known item")
	}
	if tracker.SetRSSParsedItemFeedType("https://f.test/feed.xml", "k", "news") {
		t.Fatal("SetRSSParsedItemFeedType() reported a change for an unchanged feed type")
	}
}

func TestMarkRSSItemSentRebuildsNilSentItems(t *testing.T) {
	tracker := NewMemoryTracker()
	tracker.mutex.Lock()
	tracker.rssStatus.SentItems = nil
	tracker.mutex.Unlock()

	tracker.MarkRSSItemSentToDestination("item-1", "Example", "Example Feed", "slack.rss")

	if !tracker.IsRSSItemSentToDestination("item-1", "slack.rss") {
		t.Fatal("sent marker missing after nil SentItems map rebuild")
	}
}

func TestIsRSSItemSentToDestinationMigratesLegacyDestination(t *testing.T) {
	tracker := NewMemoryTracker()
	legacyURL := "https://hooks.slack.com/services/T12345678/B12345678/testtoken1234567890"
	tracker.MarkRSSItemSentToWebhook("item-1", "Example", "Example Feed", legacyURL)

	if tracker.IsRSSItemSentToDestination("item-1", "slack.rss") {
		t.Fatal("stable destination reported sent before legacy migration")
	}
	if !tracker.IsRSSItemSentToDestination("item-1", "slack.rss", "", "slack.rss", "slack.unknown", legacyURL) {
		t.Fatal("legacy webhook-keyed RSS sent marker was not recognized")
	}
	if !tracker.IsRSSItemSentToDestination("item-1", "slack.rss") {
		t.Fatal("legacy RSS sent marker was not migrated to the stable destination")
	}
}

func TestGetUnsentRSSItemsSkipsBlankLegacyDestinationsInReadPath(t *testing.T) {
	tracker := NewMemoryTracker()
	tracker.MarkRSSItemParsed("https://f.test/feed.xml", "k", StoredRSSEntry{
		Title:     "Example",
		Published: "2025-01-15T10:30:00Z",
	})

	unsent := tracker.GetUnsentRSSItemsForDestination("slack.rss", "", "slack.rss", "slack.other")
	if len(unsent) != 1 || unsent[0].Key != "k" {
		t.Fatalf("unsent items = %+v, want the single parsed item", unsent)
	}
}

func TestMigrateRSSSentLegacyDestinationsSkipRules(t *testing.T) {
	tracker := NewMemoryTracker()
	legacyURL := "https://hooks.slack.com/services/T12345678/B12345678/testtoken1234567890"
	tracker.MarkRSSItemSentToWebhook("item-1", "Example", "Example Feed", legacyURL)
	tracker.MarkRSSItemSentToDestination("done", "Example", "Example Feed", "slack.rss")
	if err := tracker.SavePendingChanges(); err != nil {
		t.Fatalf("SavePendingChanges() error = %v", err)
	}

	tracker.migrateRSSSentLegacyDestinations([]rssSentLegacyMigration{
		{itemKey: "", destinationID: "slack.rss", legacyDestination: legacyURL},
		{itemKey: "item-1", destinationID: "slack.rss", legacyDestination: "slack.rss"},
		{itemKey: "done", destinationID: "slack.rss", legacyDestination: legacyURL},
		{itemKey: "item-1", destinationID: "slack.rss", legacyDestination: "slack.missing"},
	})

	if tracker.DirtyState().RSS {
		t.Fatal("skip-only migrations marked RSS status dirty")
	}
	if tracker.IsRSSItemSentToDestination("item-1", "slack.rss") {
		t.Fatal("skip-only migrations created a stable destination marker")
	}
}

func TestGetUnsentRSSRecoveryItemsReturnsNilWhenEmpty(t *testing.T) {
	tracker := NewMemoryTracker()

	if items := tracker.GetUnsentRSSRecoveryItemsForDestinationFeedType("slack.rss", "", nil); items != nil {
		t.Fatalf("recovery items = %+v, want nil without parsed items", items)
	}
}

func TestMarkRSSItemParsedAfterSeedKeepsFullSortPending(t *testing.T) {
	tracker := NewMemoryTracker()
	tracker.seedRSSParsedItemsForRetention([]StoredRSSEntry{
		{Key: "k2", FeedURL: "https://f.test/feed.xml", Published: "2025-01-02T00:00:00Z"},
		{Key: "k1", FeedURL: "https://f.test/feed.xml", Published: "2025-01-01T00:00:00Z"},
	})
	if state := tracker.RSSParsedSortState(); !state.FullSortDirty {
		t.Fatal("seeding parsed items did not mark a pending full sort")
	}

	tracker.MarkRSSItemParsed("https://f.test/feed.xml", "k3", StoredRSSEntry{Published: "2025-01-03T00:00:00Z"})

	state := tracker.RSSParsedSortState()
	if !state.SortDirty || !state.FullSortDirty {
		t.Fatalf("sort state after append = %+v, want pending full sort", state)
	}
	if got := rssItemKeys(tracker.RSSParsedItemsSnapshot()); len(got) != 3 ||
		got[0] != "k1" || got[1] != "k2" || got[2] != "k3" {
		t.Fatalf("sorted parsed keys = %v, want [k1 k2 k3]", got)
	}
}

func TestGetRSSParsedItemKeysRebuildsIndexAfterFeedTypeUpdate(t *testing.T) {
	tracker := NewMemoryTracker()
	tracker.MarkRSSItemParsed("https://f.test/feed.xml", "k", StoredRSSEntry{Title: "Example"})
	_ = tracker.RSSParsedItemsSnapshot() // settle sorting so only the index is dirty

	if !tracker.SetRSSParsedItemFeedType("https://f.test/feed.xml", "k", "news") {
		t.Fatal("SetRSSParsedItemFeedType() failed")
	}

	keys := tracker.GetRSSParsedItemKeys("https://f.test/feed.xml")
	if _, ok := keys["k"]; !ok {
		t.Fatalf("parsed item keys = %v, want to contain k after index rebuild", keys)
	}
}

func TestSavePendingChangesInMemorySortsParsedItems(t *testing.T) {
	tracker := NewMemoryTracker()
	tracker.MarkRSSItemParsed("https://f.test/feed.xml", "k2", StoredRSSEntry{Published: "2025-01-02T00:00:00Z"})
	tracker.MarkRSSItemParsed("https://f.test/feed.xml", "k1", StoredRSSEntry{Published: "2025-01-01T00:00:00Z"})

	if err := tracker.SavePendingChanges(); err != nil {
		t.Fatalf("SavePendingChanges() error = %v", err)
	}

	if state := tracker.RSSParsedSortState(); state.SortDirty {
		t.Fatalf("sort state after in-memory save = %+v, want settled", state)
	}
	if got := rssItemKeys(tracker.RSSParsedItemsSnapshot()); len(got) != 2 || got[0] != "k1" || got[1] != "k2" {
		t.Fatalf("sorted parsed keys = %v, want [k1 k2]", got)
	}
}

func TestSavePendingChangesInMemoryRebuildsIndexOnly(t *testing.T) {
	tracker := NewMemoryTracker()
	tracker.MarkRSSItemParsed("https://f.test/feed.xml", "k", StoredRSSEntry{Title: "Example"})
	_ = tracker.RSSParsedItemsSnapshot() // settle sorting so only the index is dirty

	if !tracker.SetRSSParsedItemFeedType("https://f.test/feed.xml", "k", "news") {
		t.Fatal("SetRSSParsedItemFeedType() failed")
	}
	if err := tracker.SavePendingChanges(); err != nil {
		t.Fatalf("SavePendingChanges() error = %v", err)
	}

	unsent := tracker.GetUnsentRSSItemsForDestinationFeedType("slack.rss", "news", nil)
	if len(unsent) != 1 || unsent[0].Key != "k" {
		t.Fatalf("feed-typed unsent items = %+v, want reindexed item", unsent)
	}
}

func TestSortRSSParsedDirtyTailSkipsMergeWhenAppendInOrder(t *testing.T) {
	tracker := NewMemoryTracker()
	tracker.MarkRSSItemParsed("https://f.test/feed.xml", "k1", StoredRSSEntry{Published: "2025-01-01T00:00:00Z"})
	if err := tracker.SavePendingChanges(); err != nil {
		t.Fatalf("SavePendingChanges() error = %v", err)
	}

	tracker.MarkRSSItemParsed("https://f.test/feed.xml", "k2", StoredRSSEntry{Published: "2025-01-02T00:00:00Z"})
	if err := tracker.SavePendingChanges(); err != nil {
		t.Fatalf("second SavePendingChanges() error = %v", err)
	}

	if got := rssItemKeys(tracker.RSSParsedItemsSnapshot()); len(got) != 2 || got[0] != "k1" || got[1] != "k2" {
		t.Fatalf("sorted parsed keys = %v, want [k1 k2]", got)
	}
}

func TestMergeRSSParsedSortedTailInterleavesEntries(t *testing.T) {
	tracker := NewMemoryTracker()
	tracker.MarkRSSItemParsed("https://f.test/feed.xml", "k1", StoredRSSEntry{Published: "2025-01-01T00:00:00Z"})
	tracker.MarkRSSItemParsed("https://f.test/feed.xml", "k3", StoredRSSEntry{Published: "2025-01-03T00:00:00Z"})
	if err := tracker.SavePendingChanges(); err != nil {
		t.Fatalf("SavePendingChanges() error = %v", err)
	}

	tracker.MarkRSSItemParsed("https://f.test/feed.xml", "k2", StoredRSSEntry{Published: "2025-01-02T00:00:00Z"})
	if err := tracker.SavePendingChanges(); err != nil {
		t.Fatalf("second SavePendingChanges() error = %v", err)
	}

	if got := rssItemKeys(tracker.RSSParsedItemsSnapshot()); len(got) != 3 ||
		got[0] != "k1" || got[1] != "k2" || got[2] != "k3" {
		t.Fatalf("merged parsed keys = %v, want [k1 k2 k3]", got)
	}
}

func TestStoredRSSSortAndLoadTimeFallbacks(t *testing.T) {
	parsedAt := time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC)
	published := time.Date(2025, 1, 10, 8, 0, 0, 0, time.UTC)

	sortEntry := StoredRSSEntry{Published: "", ParsedAt: formatStatusTimestamp(parsedAt)}
	if got := storedRSSSortTime(sortEntry); !got.Equal(parsedAt) {
		t.Fatalf("storedRSSSortTime() = %v, want ParsedAt fallback %v", got, parsedAt)
	}

	loadEntry := StoredRSSEntry{ParsedAt: "garbage", Published: published.Format(time.RFC3339Nano)}
	if got := storedRSSLoadTime(loadEntry); !got.Equal(published) {
		t.Fatalf("storedRSSLoadTime() = %v, want Published fallback %v", got, published)
	}
}

func TestRecordCleanupAuditSkipsNonPositiveCount(t *testing.T) {
	tracker, dir := newTestTrackerWithDir(t)

	var events []DeliveryAuditEvent
	tracker.recordCleanupAudit(&events, "rss", "rss", DeliveryAuditReasonRSSRetentionPruned, 0, nil)
	if len(events) != 0 {
		t.Fatalf("events = %+v, want no event recorded for a zero count", events)
	}

	if _, err := os.Stat(filepath.Join(dir, statusAuditFileName)); !os.IsNotExist(err) {
		t.Fatalf("audit file exists after zero-count cleanup audit, stat err = %v", err)
	}
}

func TestCleanupRSSSkipsWhenAgeDisabledAndUnderCap(t *testing.T) {
	tracker := NewMemoryTracker()
	// Exercise the internal disabled-age state that public constructors normalize away.
	tracker.mutex.Lock()
	tracker.retention.RSSParsedMaxAge = 0
	tracker.mutex.Unlock()

	tracker.MarkRSSItemParsed("https://f.test/feed.xml", "k", StoredRSSEntry{Title: "Example"})
	tracker.CleanupOldEntriesForRSSDestinations([]string{"discord.rss"})

	if items := tracker.RSSParsedItemsSnapshot(); len(items) != 1 {
		t.Fatalf("parsed items after disabled cleanup = %d, want 1", len(items))
	}
}

func TestCleanupWarnsWhenNoDeliveredItemsRemovable(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()

	policy := DefaultRetentionPolicy()
	policy.MaxRSSParsedItems = 1
	tracker := NewMemoryTrackerWithRetention(policy)
	tracker.MarkRSSItemParsed("https://f.test/feed.xml", "k1", StoredRSSEntry{Published: "2025-01-01T00:00:00Z"})
	tracker.MarkRSSItemParsed("https://f.test/feed.xml", "k2", StoredRSSEntry{Published: "2025-01-02T00:00:00Z"})

	tracker.CleanupOldEntriesForRSSDestinations([]string{"discord.rss"})

	if items := tracker.RSSParsedItemsSnapshot(); len(items) != 2 {
		t.Fatalf("parsed items = %d, want undelivered backlog preserved", len(items))
	}
	found := false
	for _, entry := range hook.AllEntries() {
		if entry.Level == log.WarnLevel && strings.Contains(entry.Message, "no fully delivered parsed items are safe to remove") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("missing warn log for unremovable parsed backlog")
	}
}

func TestCleanupParsedPruneSkipsNonRSSRetryRows(t *testing.T) {
	policy := DefaultRetentionPolicy()
	policy.MaxRSSParsedItems = 1
	tracker := NewMemoryTrackerWithRetention(policy)

	tracker.MarkRSSItemParsed("https://f.test/feed.xml", "old", StoredRSSEntry{Published: "2025-01-01T00:00:00Z"})
	tracker.MarkRSSItemParsed("https://f.test/feed.xml", "new", StoredRSSEntry{Published: statusNow().Format(time.RFC3339Nano)})
	tracker.MarkRSSItemSentToDestination("old", "Old", "Feed", "discord.rss")
	tracker.MarkRSSItemSentToDestination("new", "New", "Feed", "discord.rss")
	if !tracker.enqueueRetryForTest("old", "discord.ransomware", "discord", "api", "Old", "boom", 5, time.Hour) {
		t.Fatal("EnqueueRetry() unexpectedly failed")
	}

	tracker.CleanupOldEntriesForRSSDestinations([]string{"discord.rss"})

	if got := rssItemKeys(tracker.RSSParsedItemsSnapshot()); len(got) != 1 || got[0] != "new" {
		t.Fatalf("parsed items after prune = %v, want [new]", got)
	}
	if items := tracker.GetRetryItemsByType("api"); len(items) != 1 {
		t.Fatalf("API retry rows = %d, want unaffected API retry item", len(items))
	}
}

func TestCleanupPrunesRSSSentItemsByCountRespectingProtectedKeys(t *testing.T) {
	policy := DefaultRetentionPolicy()
	policy.MaxRSSSentItems = 1
	tracker := NewMemoryTrackerWithRetention(policy)

	now := statusNow().Format(time.RFC3339Nano)
	tracker.MarkRSSItemParsed("https://f.test/feed.xml", "p1", StoredRSSEntry{Title: "P1", Published: now})
	tracker.MarkRSSItemParsed("https://f.test/feed.xml", "p2", StoredRSSEntry{Title: "P2", Published: now})
	tracker.MarkRSSItemSentToDestination("p1", "P1", "Feed", "discord.rss")
	tracker.MarkRSSItemSentToDestination("p2", "P2", "Feed", "discord.rss")
	tracker.MarkRSSItemSentToDestination("u1", "U1", "Feed", "discord.rss")
	tracker.MarkRSSItemSentToDestination("u2", "U2", "Feed", "discord.rss")

	tracker.CleanupOldEntriesForRSSDestinations([]string{"discord.rss"})

	for _, protected := range []string{"p1", "p2"} {
		if _, ok := tracker.RSSSentItemSnapshot(protected, "discord.rss"); !ok {
			t.Fatalf("protected sent marker %q was pruned", protected)
		}
	}
	for _, orphan := range []string{"u1", "u2"} {
		if _, ok := tracker.RSSSentItemSnapshot(orphan, "discord.rss"); ok {
			t.Fatalf("orphan sent marker %q survived count-based pruning", orphan)
		}
	}
}

func TestStoredRSSEntryOlderThanFallbacks(t *testing.T) {
	now := statusNow()
	oldStamp := formatStatusTimestamp(now.Add(-2 * time.Hour))
	tests := []struct {
		name   string
		entry  StoredRSSEntry
		maxAge time.Duration
		want   bool
	}{
		{name: "disabled max age", entry: StoredRSSEntry{Published: oldStamp}, maxAge: 0, want: false},
		{name: "parsed at fallback", entry: StoredRSSEntry{Published: "garbage", ParsedAt: oldStamp}, maxAge: time.Hour, want: true},
		{name: "no usable timestamp", entry: StoredRSSEntry{}, maxAge: time.Hour, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := storedRSSEntryOlderThan(tt.entry, now, tt.maxAge); got != tt.want {
				t.Fatalf("storedRSSEntryOlderThan() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestStatusTimeBeforeZeroValues(t *testing.T) {
	earlier := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	later := earlier.Add(time.Hour)
	tests := []struct {
		name        string
		left, right time.Time
		want        bool
	}{
		{name: "zero left sorts first", left: time.Time{}, right: later, want: true},
		{name: "zero right sorts last", left: earlier, right: time.Time{}, want: false},
		{name: "earlier before later", left: earlier, right: later, want: true},
		{name: "later not before earlier", left: later, right: earlier, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := statusTimeBefore(tt.left, tt.right); got != tt.want {
				t.Fatalf("statusTimeBefore() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestUniqueNonEmptyStringsFiltersEmptiesAndDuplicates(t *testing.T) {
	got := uniqueNonEmptyStrings([]string{"a", "", "a", "b"})
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("uniqueNonEmptyStrings() = %v, want [a b]", got)
	}
}

func TestStatusSignatureHelpersZeroTime(t *testing.T) {
	if got := statusSignatureTimestamp(time.Time{}); got != "" {
		t.Fatalf("statusSignatureTimestamp(zero) = %q, want empty", got)
	}
	if got := statusSignatureDate(time.Time{}); got != "" {
		t.Fatalf("statusSignatureDate(zero) = %q, want empty", got)
	}
	published := time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC)
	if got := statusSignatureTimestamp(published); got == "" {
		t.Fatal("statusSignatureTimestamp(non-zero) returned empty")
	}
	if got := statusSignatureDate(published); got == "" {
		t.Fatal("statusSignatureDate(non-zero) returned empty")
	}
}

func TestCleanupOldAPISentItemsRebuildsHeapFromSentItems(t *testing.T) {
	policy := DefaultRetentionPolicy()
	policy.MaxAPISentItems = 1
	tracker := NewMemoryTrackerWithRetention(policy)

	// Simulate sent markers that exist without heap entries (as after a partial load).
	tracker.mutex.Lock()
	tracker.apiStatus.SentItems[makeCompositeKey("old", "discord.a")] = webhookSentInfo{SentAt: statusNow().Add(-2 * time.Hour)}
	tracker.apiStatus.SentItems[makeCompositeKey("new", "discord.a")] = webhookSentInfo{SentAt: statusNow()}
	tracker.mutex.Unlock()

	tracker.CleanupOldEntries()

	if tracker.IsAPIItemSentToDestination("old", "discord.a") {
		t.Fatal("oldest sent marker survived count pruning after heap rebuild")
	}
	if !tracker.IsAPIItemSentToDestination("new", "discord.a") {
		t.Fatal("newest sent marker was pruned")
	}
}

func TestCloneHelpersHandleNilAndCopies(t *testing.T) {
	if got := cloneAPIStatus(nil); got != nil {
		t.Fatalf("cloneAPIStatus(nil) = %+v, want nil", got)
	}
	if got := cloneRSSStatus(nil); got != nil {
		t.Fatalf("cloneRSSStatus(nil) = %+v, want nil", got)
	}
	if got := cloneStringMap(nil); got != nil {
		t.Fatalf("cloneStringMap(nil) = %v, want nil", got)
	}

	original := map[string]string{"a": "1"}
	cloned := cloneStringMap(original)
	cloned["a"] = "2"
	if original["a"] != "1" {
		t.Fatalf("cloneStringMap() did not copy: original = %v", original)
	}
}

// TestUpdateFeedStatusValidatorsKeepStoredValueWhenResponseOmitsIt pins the
// contract behind conditional RSS polling: a stored ETag/Last-Modified may only
// be replaced by a non-empty value. RFC 7232 merely recommends that a 304
// repeats the validators, and a Last-Modified-only origin typically sends
// neither, so an empty value means "unchanged", never "drop it". Wiping one
// removes the feed from GetRSSFeedHTTPValidators and stops conditional polling
// for good, making every later poll re-download the full body.
func TestUpdateFeedStatusValidatorsKeepStoredValueWhenResponseOmitsIt(t *testing.T) {
	feedURL := "https://example.test/keep-validators.xml"
	seeded := rss.FeedHTTPValidators{
		ETag:         `"v1"`,
		LastModified: "Wed, 21 Oct 2015 07:28:00 GMT",
	}
	replacement := rss.FeedHTTPValidators{
		ETag:         `"v2"`,
		LastModified: "Thu, 22 Oct 2015 07:28:00 GMT",
	}

	tests := []struct {
		name     string
		incoming rss.FeedHTTPValidators
		want     rss.FeedHTTPValidators
	}{
		{
			name:     "304 repeats only the etag",
			incoming: rss.FeedHTTPValidators{ETag: `"v1"`},
			want:     seeded,
		},
		{
			name:     "304 repeats neither validator",
			incoming: rss.FeedHTTPValidators{},
			want:     seeded,
		},
		{
			name:     "200 carries a new pair",
			incoming: replacement,
			want:     replacement,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tracker := NewMemoryTracker()
			tracker.UpdateFeedStatusWithErrorInfoAndValidators(feedURL, true, 1, "", SourceErrorInfo{}, seeded)

			tracker.UpdateFeedStatusWithErrorInfoAndValidators(feedURL, true, 1, "", SourceErrorInfo{}, tt.incoming)

			got := tracker.GetRSSFeedHTTPValidators([]string{feedURL})
			if len(got) != 1 {
				t.Fatalf("GetRSSFeedHTTPValidators() = %#v, want one feed", got)
			}
			if got[feedURL] != tt.want {
				t.Fatalf("validators = %+v, want %+v", got[feedURL], tt.want)
			}
		})
	}

	// The guard must not resurrect state for a feed that never offered a
	// validator: such a feed still has to be absent from the map, so the parser
	// sends no If-None-Match / If-Modified-Since for it.
	t.Run("origin never sent a validator", func(t *testing.T) {
		tracker := NewMemoryTracker()
		tracker.UpdateFeedStatusWithErrorInfoAndValidators(feedURL, true, 1, "", SourceErrorInfo{}, rss.FeedHTTPValidators{})
		if got := tracker.GetRSSFeedHTTPValidators([]string{feedURL}); len(got) != 0 {
			t.Fatalf("GetRSSFeedHTTPValidators() = %#v, want no feed", got)
		}
	})

	// A failed fetch must not disable conditional polling either: the guard sits
	// in the success branch, and the failure branch touches no validator, so an
	// origin outage cannot cost the cached pair.
	t.Run("failed fetch keeps the stored pair", func(t *testing.T) {
		tracker := NewMemoryTracker()
		tracker.UpdateFeedStatusWithErrorInfoAndValidators(feedURL, true, 1, "", SourceErrorInfo{}, seeded)
		tracker.UpdateFeedStatusWithErrorInfoAndValidators(feedURL, false, 0, "connection refused", SourceErrorInfo{}, rss.FeedHTTPValidators{})
		got := tracker.GetRSSFeedHTTPValidators([]string{feedURL})
		if got[feedURL] != seeded {
			t.Fatalf("validators after a failed fetch = %+v, want %+v", got[feedURL], seeded)
		}
	})
}

// TestUpdateFeedStatusForcesRefetchAfterNotModifiedDurationElapses reproduces
// and then pins the fix for a residual gap in the 304-validator fix in
// internal/status/tracker.go: the non-empty-validator guard means a
// stored ETag/Last-Modified can only be replaced by a fresh non-empty value,
// so an origin that answers 200 once with a validator it later matches
// unconditionally -- i.e. that 304s forever regardless of what was sent --
// never loses that validator by itself, and the feed silently stops
// delivering while GetRSSFeedHTTPValidators keeps sending conditional
// requests nobody upstream is honestly answering. The trip condition is
// wall-clock time (maxConsecutiveNotModifiedDuration), not a poll count (see
// R2 in the 2026-09-04 review of this fix): rss_poll_interval has a floor but
// no ceiling, so a count-based threshold has unbounded recovery time. Since
// statusNow() is real wall-clock time, a genuine 24h wait is not something a
// unit test can drive; this seeds FeedInfo.ConsecutiveNotModifiedSince
// directly, the same way neighbouring tests seed other time-derived fields
// (see alias_ownership_retention_test.go), while every other call below still
// drives the real success/entriesFound/validators shape a bare 304 produces
// through UpdateFeedStatusWithErrorInfoAndValidators, exactly as
// sendParsedRSSToWebhooksWithConfig does per poll.
func TestUpdateFeedStatusForcesRefetchAfterNotModifiedDurationElapses(t *testing.T) {
	feedURL := "https://example.test/stuck-validator.xml"
	seeded := rss.FeedHTTPValidators{
		ETag:         `"v1"`,
		LastModified: "Wed, 21 Oct 2035 07:28:00 GMT",
	}
	bareNotModified := rss.FeedHTTPValidators{}

	tracker := NewMemoryTracker()
	// The one real 200 that plants the validator the origin will later match
	// unconditionally forever.
	tracker.UpdateFeedStatusWithErrorInfoAndValidators(feedURL, true, 1, "", SourceErrorInfo{}, seeded)

	// A bare 304 still must not touch the validator by itself, however many
	// of them arrive, as long as the streak's elapsed wall-clock time stays
	// under the duration -- wiping it eagerly would break every well-behaved
	// Last-Modified-only feed, which is the guard's whole point.
	for i := 0; i < 5; i++ {
		tracker.UpdateFeedStatusWithErrorInfoAndValidators(feedURL, true, 0, "", SourceErrorInfo{}, bareNotModified)
	}
	got := tracker.GetRSSFeedHTTPValidators([]string{feedURL})
	if got[feedURL] != seeded {
		t.Fatalf("validators after a few bare 304s = %+v, want %+v (still trusted)", got[feedURL], seeded)
	}
	info, ok := tracker.GetRSSFeedInfo(feedURL)
	if !ok {
		t.Fatalf("GetRSSFeedInfo() ok = false, want true")
	}
	if info.ConsecutiveNotModified != 5 {
		t.Fatalf("ConsecutiveNotModified = %d, want 5", info.ConsecutiveNotModified)
	}
	if info.ConsecutiveNotModifiedSince == nil {
		t.Fatalf("ConsecutiveNotModifiedSince = nil, want set once the streak started")
	}

	// Push the recorded streak start to just under the trip duration: one more
	// bare 304 must still leave the validator untouched.
	almostDue := statusNow().Add(-(maxConsecutiveNotModifiedDuration - time.Minute))
	feedInfo := tracker.rssStatus.Feeds[feedURL]
	feedInfo.ConsecutiveNotModifiedSince = &almostDue
	tracker.rssStatus.Feeds[feedURL] = feedInfo

	tracker.UpdateFeedStatusWithErrorInfoAndValidators(feedURL, true, 0, "", SourceErrorInfo{}, bareNotModified)
	got = tracker.GetRSSFeedHTTPValidators([]string{feedURL})
	if got[feedURL] != seeded {
		t.Fatalf("validators just under the duration = %+v, want %+v (still trusted)", got[feedURL], seeded)
	}

	// Push it just past the trip duration: the next bare 304 must force one
	// unconditional refetch by dropping the stored validator.
	overdue := statusNow().Add(-(maxConsecutiveNotModifiedDuration + time.Minute))
	feedInfo = tracker.rssStatus.Feeds[feedURL]
	feedInfo.ConsecutiveNotModifiedSince = &overdue
	tracker.rssStatus.Feeds[feedURL] = feedInfo

	tracker.UpdateFeedStatusWithErrorInfoAndValidators(feedURL, true, 0, "", SourceErrorInfo{}, bareNotModified)

	got = tracker.GetRSSFeedHTTPValidators([]string{feedURL})
	if _, exists := got[feedURL]; exists {
		t.Fatalf("validators after the duration elapsed = %+v, want the feed absent (forced unconditional refetch)", got[feedURL])
	}
	info, ok = tracker.GetRSSFeedInfo(feedURL)
	if !ok {
		t.Fatalf("GetRSSFeedInfo() ok = false, want true")
	}
	if info.ConsecutiveNotModified != 0 {
		t.Fatalf("ConsecutiveNotModified after forced refetch = %d, want 0 (streak reset)", info.ConsecutiveNotModified)
	}
	if info.ConsecutiveNotModifiedSince != nil {
		t.Fatalf("ConsecutiveNotModifiedSince after forced refetch = %v, want nil", info.ConsecutiveNotModifiedSince)
	}
	if info.ETag != "" || info.LastModified != "" {
		t.Fatalf("stored validator after forced refetch = %+v, want both empty", info)
	}
}

// TestUpdateFeedStatusEchoedValidatorStillAdvancesTheStreak is the R1 fix from
// the 2026-09-04 review: RFC 7232 says a 304 SHOULD repeat the ETag, and
// nginx/Apache/Cloudflare all do, so treating "no validator header at all" as
// the only not-fresh shape meant the else-branch reset the streak to 0 on
// every poll for exactly the common echoing-origin case this guard targets,
// and the feed stayed stuck forever. Against the code as it shipped before
// this fix, each of these subtests fails: with the empty-only guard,
// validators.ETag/LastModified is non-empty on every echoed poll, so the
// streak never advances past 0 and the forced refetch never fires even after
// the duration has elapsed. The fix compares the returned validator against
// the one already stored: identical (echoed) is not fresh evidence, a
// genuine change is.
func TestUpdateFeedStatusEchoedValidatorStillAdvancesTheStreak(t *testing.T) {
	pastDue := func() *time.Time {
		t := statusNow().Add(-(maxConsecutiveNotModifiedDuration + time.Minute))
		return &t
	}

	cases := []struct {
		name   string
		seeded rss.FeedHTTPValidators
		echoed rss.FeedHTTPValidators
	}{
		{
			name:   "echoed ETag",
			seeded: rss.FeedHTTPValidators{ETag: `"stable-etag"`},
			echoed: rss.FeedHTTPValidators{ETag: `"stable-etag"`},
		},
		{
			name:   "echoed Last-Modified",
			seeded: rss.FeedHTTPValidators{LastModified: "Wed, 21 Oct 2035 07:28:00 GMT"},
			echoed: rss.FeedHTTPValidators{LastModified: "Wed, 21 Oct 2035 07:28:00 GMT"},
		},
		{
			name:   "echoed pair",
			seeded: rss.FeedHTTPValidators{ETag: `"stable-etag"`, LastModified: "Wed, 21 Oct 2035 07:28:00 GMT"},
			echoed: rss.FeedHTTPValidators{ETag: `"stable-etag"`, LastModified: "Wed, 21 Oct 2035 07:28:00 GMT"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			feedURL := "https://example.test/echoed-validator.xml"
			tracker := NewMemoryTracker()
			tracker.UpdateFeedStatusWithErrorInfoAndValidators(feedURL, true, 1, "", SourceErrorInfo{}, tc.seeded)

			// Three polls, each echoing back exactly what was sent -- not the
			// bare, header-less 304 the other tests in this file drive.
			for i := 0; i < 3; i++ {
				tracker.UpdateFeedStatusWithErrorInfoAndValidators(feedURL, true, 0, "", SourceErrorInfo{}, tc.echoed)
			}
			info, ok := tracker.GetRSSFeedInfo(feedURL)
			if !ok {
				t.Fatalf("GetRSSFeedInfo() ok = false, want true")
			}
			if info.ConsecutiveNotModified != 3 {
				t.Fatalf("ConsecutiveNotModified after 3 echoed polls = %d, want 3 (echoing must still advance the streak)", info.ConsecutiveNotModified)
			}
			if info.ConsecutiveNotModifiedSince == nil {
				t.Fatalf("ConsecutiveNotModifiedSince after echoed polls = nil, want set")
			}

			// Push the recorded streak start past the trip duration and poll
			// once more with the same echoed validator: the forced refetch
			// must still fire, proving the streak was genuinely advancing, not
			// stuck at 0 the way the pre-fix empty-only guard left it.
			feedInfo := tracker.rssStatus.Feeds[feedURL]
			feedInfo.ConsecutiveNotModifiedSince = pastDue()
			tracker.rssStatus.Feeds[feedURL] = feedInfo

			tracker.UpdateFeedStatusWithErrorInfoAndValidators(feedURL, true, 0, "", SourceErrorInfo{}, tc.echoed)
			got := tracker.GetRSSFeedHTTPValidators([]string{feedURL})
			if _, exists := got[feedURL]; exists {
				t.Fatalf("validators after the duration elapsed with an echoed validator = %+v, want the feed absent (forced refetch)", got[feedURL])
			}
		})
	}
}

// TestUpdateFeedStatusGenuinelyChangedValidatorResetsStreak is the other half
// of R1: a validator that actually differs from what is stored -- not an
// echo -- is real evidence the conditional exchange is progressing and must
// reset the streak, even though entriesFound is still 0 for that poll (a
// changed ETag/Last-Modified with no new items is a legitimate shape, e.g. a
// re-published existing entry).
func TestUpdateFeedStatusGenuinelyChangedValidatorResetsStreak(t *testing.T) {
	feedURL := "https://example.test/changed-validator.xml"
	tracker := NewMemoryTracker()
	tracker.UpdateFeedStatusWithErrorInfoAndValidators(feedURL, true, 1, "", SourceErrorInfo{}, rss.FeedHTTPValidators{ETag: `"gen-0"`})

	for i := 0; i < 5; i++ {
		tracker.UpdateFeedStatusWithErrorInfoAndValidators(feedURL, true, 0, "", SourceErrorInfo{}, rss.FeedHTTPValidators{ETag: `"gen-0"`})
	}
	info, ok := tracker.GetRSSFeedInfo(feedURL)
	if !ok {
		t.Fatalf("GetRSSFeedInfo() ok = false, want true")
	}
	if info.ConsecutiveNotModified != 5 {
		t.Fatalf("ConsecutiveNotModified before the change = %d, want 5", info.ConsecutiveNotModified)
	}

	tracker.UpdateFeedStatusWithErrorInfoAndValidators(feedURL, true, 0, "", SourceErrorInfo{}, rss.FeedHTTPValidators{ETag: `"gen-1"`})

	info, ok = tracker.GetRSSFeedInfo(feedURL)
	if !ok {
		t.Fatalf("GetRSSFeedInfo() ok = false, want true")
	}
	if info.ConsecutiveNotModified != 0 {
		t.Fatalf("ConsecutiveNotModified after a genuinely fresh validator = %d, want 0 (reset)", info.ConsecutiveNotModified)
	}
	if info.ConsecutiveNotModifiedSince != nil {
		t.Fatalf("ConsecutiveNotModifiedSince after a genuinely fresh validator = %v, want nil", info.ConsecutiveNotModifiedSince)
	}
	got := tracker.GetRSSFeedHTTPValidators([]string{feedURL})
	if got[feedURL].ETag != `"gen-1"` {
		t.Fatalf("stored ETag after a genuinely fresh validator = %q, want %q", got[feedURL].ETag, `"gen-1"`)
	}
}

// TestUpdateFeedStatusNoStoredValidatorNeverAccumulatesStreak is the R3
// mutation-proof for the `hadStoredValidator &&` clause: a feed that has
// never had a validator at all (e.g. an origin that never sends ETag or
// Last-Modified) still reports success with zero entries on a quiet news
// day, and that must never accumulate a streak or ever warn about forcing a
// refetch -- there is nothing to force, and ETag/LastModified are already
// empty. Dropping the clause makes this loop reach a non-zero streak.
func TestUpdateFeedStatusNoStoredValidatorNeverAccumulatesStreak(t *testing.T) {
	feedURL := "https://example.test/no-validator-ever.xml"
	tracker := NewMemoryTracker()

	for i := 0; i < 20; i++ {
		tracker.UpdateFeedStatusWithErrorInfoAndValidators(feedURL, true, 0, "", SourceErrorInfo{}, rss.FeedHTTPValidators{})
	}

	info, ok := tracker.GetRSSFeedInfo(feedURL)
	if !ok {
		t.Fatalf("GetRSSFeedInfo() ok = false, want true")
	}
	if info.ConsecutiveNotModified != 0 {
		t.Fatalf("ConsecutiveNotModified for a feed that never had a validator = %d, want 0", info.ConsecutiveNotModified)
	}
	if info.ConsecutiveNotModifiedSince != nil {
		t.Fatalf("ConsecutiveNotModifiedSince for a feed that never had a validator = %v, want nil", info.ConsecutiveNotModifiedSince)
	}
}

// TestUpdateFeedStatusEntriesWithoutFreshValidatorResetsStreak is the R3
// mutation-proof for the `entriesFound == 0 &&` clause: a poll that delivers
// real new entries but whose response carries no validator header at all
// (e.g. a plain 200 without ETag/Last-Modified) must reset the streak, not
// advance it -- content genuinely changed, which is exactly the evidence the
// guard exists to look for. Dropping the clause makes this poll advance the
// streak instead of resetting it.
func TestUpdateFeedStatusEntriesWithoutFreshValidatorResetsStreak(t *testing.T) {
	feedURL := "https://example.test/entries-no-validator.xml"
	tracker := NewMemoryTracker()
	tracker.UpdateFeedStatusWithErrorInfoAndValidators(feedURL, true, 1, "", SourceErrorInfo{}, rss.FeedHTTPValidators{ETag: `"v1"`})

	for i := 0; i < 4; i++ {
		tracker.UpdateFeedStatusWithErrorInfoAndValidators(feedURL, true, 0, "", SourceErrorInfo{}, rss.FeedHTTPValidators{})
	}
	info, ok := tracker.GetRSSFeedInfo(feedURL)
	if !ok {
		t.Fatalf("GetRSSFeedInfo() ok = false, want true")
	}
	if info.ConsecutiveNotModified != 4 {
		t.Fatalf("ConsecutiveNotModified before the entries poll = %d, want 4", info.ConsecutiveNotModified)
	}

	tracker.UpdateFeedStatusWithErrorInfoAndValidators(feedURL, true, 2, "", SourceErrorInfo{}, rss.FeedHTTPValidators{})

	info, ok = tracker.GetRSSFeedInfo(feedURL)
	if !ok {
		t.Fatalf("GetRSSFeedInfo() ok = false, want true")
	}
	if info.ConsecutiveNotModified != 0 {
		t.Fatalf("ConsecutiveNotModified after a poll with entries but no fresh validator = %d, want 0 (reset)", info.ConsecutiveNotModified)
	}
	if info.ConsecutiveNotModifiedSince != nil {
		t.Fatalf("ConsecutiveNotModifiedSince after a poll with entries but no fresh validator = %v, want nil", info.ConsecutiveNotModifiedSince)
	}
}

// TestUpdateFeedStatusNotModifiedStreakNeverTripsForWellBehavedFeed shows the
// forced-refetch guard changes nothing for a feed that keeps proving its
// conditional exchange still works: any poll that returns new entries, or a
// validator that genuinely differs from what is stored, resets the streak, so
// a feed that legitimately alternates between real 304s and occasional real
// content -- however many polls arrive in a burst -- is never forced through
// an unconditional refetch and never loses conditional polling. Because the
// trip condition is wall-clock time (maxConsecutiveNotModifiedDuration), a
// tight loop like this one -- however many iterations -- consumes negligible
// real time and so never trips on its own; the assertions below confirm the
// streak still resets correctly on real content, not that the loop count
// avoided a threshold.
func TestUpdateFeedStatusNotModifiedStreakNeverTripsForWellBehavedFeed(t *testing.T) {
	feedURL := "https://example.test/well-behaved.xml"
	current := rss.FeedHTTPValidators{ETag: `"gen-0"`}

	tracker := NewMemoryTracker()
	tracker.UpdateFeedStatusWithErrorInfoAndValidators(feedURL, true, 1, "", SourceErrorInfo{}, current)

	cycles := 3
	const bareNotModifiedPollsPerCycle = 50
	for c := 0; c < cycles; c++ {
		for i := 0; i < bareNotModifiedPollsPerCycle; i++ {
			tracker.UpdateFeedStatusWithErrorInfoAndValidators(feedURL, true, 0, "", SourceErrorInfo{}, rss.FeedHTTPValidators{})
		}
		current = rss.FeedHTTPValidators{ETag: `"gen-` + strconv.Itoa(c+1) + `"`}
		tracker.UpdateFeedStatusWithErrorInfoAndValidators(feedURL, true, 1, "", SourceErrorInfo{}, current)

		got := tracker.GetRSSFeedHTTPValidators([]string{feedURL})
		if got[feedURL] != current {
			t.Fatalf("cycle %d: validators = %+v, want %+v (never forced)", c, got[feedURL], current)
		}
		info, ok := tracker.GetRSSFeedInfo(feedURL)
		if !ok {
			t.Fatalf("cycle %d: GetRSSFeedInfo() ok = false, want true", c)
		}
		if info.ConsecutiveNotModified != 0 {
			t.Fatalf("cycle %d: ConsecutiveNotModified = %d, want 0 (reset by real entries)", c, info.ConsecutiveNotModified)
		}
		if info.ConsecutiveNotModifiedSince != nil {
			t.Fatalf("cycle %d: ConsecutiveNotModifiedSince = %v, want nil (reset by real entries)", c, info.ConsecutiveNotModifiedSince)
		}
	}
}

// TestRSSLegacyAliasIsNotAdoptedBySiblingDestination is the RSS mirror of
// TestAPILegacyAliasIsNotAdoptedBySiblingDestination: two RSS endpoints
// sharing one webhook URL -- in the same webhook block (slack.rss.general /
// slack.rss.general.2) or across feed types (slack.rss.government) -- must
// not adopt each other's sent, skip or derived legacy alias.
//
//nolint:gocyclo // long sequential scenario asserting alias/destination state across multiple steps; not a table split candidate
func TestRSSLegacyAliasIsNotAdoptedBySiblingDestination(t *testing.T) {
	const (
		itemKey = "rss-item-shared"
		url     = "https://hooks.slack.test/services/rss-shared-alias"
		d1      = "slack.rss.general"
		d2      = "slack.rss.general.2"
		gov     = "slack.rss.government"
	)

	t.Run("sent alias", func(t *testing.T) {
		tracker := NewMemoryTracker()
		tracker.MarkRSSItemSentToDestination(itemKey, "Title", "Feed", d1)
		tracker.MarkRSSItemSentToLegacyDestination(itemKey, "Title", "Feed", url, d1)

		if !tracker.IsRSSItemSentToDestination(itemKey, d1, url) {
			t.Fatal("owner asking about its own delivery = false, want true")
		}
		for _, sibling := range []string{d2, gov} {
			if tracker.IsRSSItemSentToDestination(itemKey, sibling, url) {
				t.Fatalf("sibling %q adopted the owner-stamped sent alias", sibling)
			}
			if snapshot, ok := tracker.RSSSentItemSnapshot(itemKey, sibling); ok {
				t.Fatalf("sibling %q recorded a phantom delivery it never made: %+v", sibling, snapshot)
			}
		}
	})

	t.Run("skip alias", func(t *testing.T) {
		tracker := NewMemoryTracker()
		tracker.MarkRSSItemSkippedForDestination(itemKey, d1)
		tracker.MarkRSSItemSkippedForLegacyDestination(itemKey, url, d1)

		if !tracker.IsRSSItemSentToDestination(itemKey, d1, url) {
			t.Fatal("owner asking about its own skip = false, want true")
		}
		snapshot, ok := tracker.RSSSentItemSnapshot(itemKey, d1)
		if !ok || !snapshot.Skipped {
			t.Fatalf("owner skip snapshot = %+v ok=%v, want Skipped=true", snapshot, ok)
		}
		// The alias row itself (raw, queried by its own key: the URL) must match
		// MarkRSSItemSkippedForDestination's primary row byte for byte except
		// DestinationID: no title, no feed title. MarkRSSItemSkippedForLegacyDestination
		// takes no title arguments for exactly this reason -- a caller that started
		// passing itemTitle/feedTitle through would persist RSS titles on the alias
		// row, which the primary skip marker deliberately never does.
		aliasRow, ok := tracker.RSSSentItemSnapshot(itemKey, url)
		if !ok {
			t.Fatal("alias row missing after MarkRSSItemSkippedForLegacyDestination")
		}
		if aliasRow.Title != "" || aliasRow.FeedTitle != "" {
			t.Fatalf("skip alias row = %+v, want Title and FeedTitle empty (skip markers never persist RSS titles)", aliasRow)
		}
		if aliasRow.DestinationID != d1 {
			t.Fatalf("skip alias row DestinationID = %q, want owner %q", aliasRow.DestinationID, d1)
		}
		for _, sibling := range []string{d2, gov} {
			if tracker.IsRSSItemSentToDestination(itemKey, sibling, url) {
				t.Fatalf("sibling %q adopted the owner-stamped skip alias", sibling)
			}
			if snapshot, ok := tracker.RSSSentItemSnapshot(itemKey, sibling); ok {
				t.Fatalf("sibling %q recorded a phantom skip it never made: %+v", sibling, snapshot)
			}
		}
	})

	t.Run("derived alias", func(t *testing.T) {
		tracker := NewMemoryTracker()
		tracker.MarkRSSItemDedupedForDestination(itemKey, "Title", "Feed", d1)
		tracker.MarkRSSItemDedupedForLegacyDestination(itemKey, "Title", "Feed", url, d1)

		if !tracker.IsRSSItemSentToDestination(itemKey, d1, url) {
			t.Fatal("owner asking about its own derived marker = false, want true")
		}
		snapshot, ok := tracker.RSSSentItemSnapshot(itemKey, d1)
		if !ok || !snapshot.Derived {
			t.Fatalf("owner derived snapshot = %+v ok=%v, want Derived=true", snapshot, ok)
		}
		for _, sibling := range []string{d2, gov} {
			if tracker.IsRSSItemSentToDestination(itemKey, sibling, url) {
				t.Fatalf("sibling %q adopted the owner-stamped derived alias", sibling)
			}
			if snapshot, ok := tracker.RSSSentItemSnapshot(itemKey, sibling); ok {
				t.Fatalf("sibling %q recorded a phantom derived marker it never made: %+v", sibling, snapshot)
			}
		}
	})

	// Row note (plan §3b, the row that matters most): a genuine pre-fix legacy
	// marker (unstamped) stays adoptable by every destination on that URL for as
	// long as the alias is retained. Deliberately pinned: migration writes the
	// owner-stamped copy under key|<asking destination> and never touches the
	// key|url alias, so this is the conservative outcome (pre-fix files never
	// produce duplicate alerts), not a bug to "fix" by stamping the alias during
	// migration -- doing so would turn every pre-fix file with a shared URL into
	// a duplicate-alert source.
	t.Run("pre-fix unstamped legacy marker stays adoptable by every destination", func(t *testing.T) {
		tracker := NewMemoryTracker()
		tracker.MarkRSSItemSentToWebhook(itemKey, "Title", "Feed", url)

		for _, asking := range []string{d1, d2, gov} {
			if !tracker.IsRSSItemSentToDestination(itemKey, asking, url) {
				t.Fatalf("destination %q did not adopt the genuine pre-fix legacy marker", asking)
			}
		}
		if alias, ok := tracker.RSSSentItemSnapshot(itemKey, url); !ok || alias.DestinationID != "" {
			t.Fatalf("legacy alias after migration = %+v ok=%v, want DestinationID empty (never stamped)", alias, ok)
		}
	})
}

// TestRSSRenamedDestinationDoesNotAdoptOwnerStampedAlias is the RSS mirror of
// TestAPIRenamedDestinationDoesNotAdoptOwnerStampedAlias: it pins the accepted
// trade-off of stamping the owning destination onto the legacy webhook-URL
// alias. Destination suffixes are positional (internal/config/config.go
// webhookDestinationSuffix), so removing or reordering RSS endpoints changes
// a destination ID and the alias no longer suppresses the item for it. The
// result is one duplicate alert per still-retained item on the moved
// endpoint, which is deliberate -- a duplicate is recoverable, a silently
// dropped alert is not -- and, since the destination-remap guard was added,
// is now reachable only when the operator passes --accept-destination-remap:
// the destinations manifest (data_dir/destinations.json) refuses to start on
// an unacknowledged remap. See CHANGELOG.md "1.2.0".
func TestRSSRenamedDestinationDoesNotAdoptOwnerStampedAlias(t *testing.T) {
	const (
		itemKey = "rss-item-reordered"
		url     = "https://hooks.slack.com/services/T12345678/B12345678/reorderedrsstoken1"
		oldID   = "slack.rss.general"
		newID   = "slack.rss.general.2"
	)

	tracker := NewMemoryTracker()
	tracker.MarkRSSItemSentToDestination(itemKey, "Example", "Example Feed", oldID)
	tracker.MarkRSSItemSentToLegacyDestination(itemKey, "Example", "Example Feed", url, oldID)

	// Same physical endpoint, new positional suffix after a config reorder.
	if tracker.IsRSSItemSentToDestination(itemKey, newID, url) {
		t.Fatal("reordered destination adopted the owner-stamped alias; the documented trade-off is one duplicate, not suppression")
	}
	if snapshot, ok := tracker.RSSSentItemSnapshot(itemKey, newID); ok {
		t.Fatalf("reordered destination recorded a delivery it never made: %+v", snapshot)
	}
	if !tracker.IsRSSItemSentToDestination(itemKey, oldID, url) {
		t.Fatal("the owning destination lost its own suppression")
	}
}

// TestRSSLegacyAliasWithBlankOwnerFromDiskIsTreatedAsLegacy is the RSS mirror
// of TestAPILegacyAliasWithBlankOwnerFromDiskIsTreatedAsLegacy: a whitespace-
// only destination_id on disk (hand-edited or foreign rss_status.json) must
// read as "no recorded destination" (adoptable), not as an owner nothing
// matches -- legacySentMarkerAppliesTo already trims; this pins it for RSS.
func TestRSSLegacyAliasWithBlankOwnerFromDiskIsTreatedAsLegacy(t *testing.T) {
	const (
		itemKey = "rss-item-from-disk"
		url     = "https://hooks.slack.com/services/T12345678/B12345678/fromdiskrsstoken1"
		asking  = "slack.rss.general"
	)

	dir := t.TempDir()
	payload := fmt.Sprintf(
		`{"last_updated":"2026-09-03T00:00:00Z","feeds":{},"parsed_items":[],`+
			`"sent_items":{%q:{"sent_at":"2026-09-03T00:00:00Z","destination_id":"   ","item_key":%q}}}`,
		makeCompositeKey(itemKey, url), itemKey,
	)
	if err := os.WriteFile(filepath.Join(dir, statusRSSFileName), []byte(payload), 0600); err != nil {
		t.Fatalf("WriteFile(rss_status.json) error = %v", err)
	}

	tracker, err := NewTracker(dir)
	if err != nil {
		t.Fatalf("NewTracker() error = %v", err)
	}
	if !tracker.IsRSSItemSentToDestination(itemKey, asking, url) {
		t.Fatal("whitespace-only recorded destination was not treated as a legacy marker")
	}
}

// TestRSSRecoveryDriverHonoursLegacyAliasOwnership pins the recovery driver,
// not just the boolean readers: GetUnsentRSSItemsForDestinationFeedType must
// report the item still unsent for a sibling destination sharing the owner-
// stamped alias's webhook URL, and must not leave a phantom sent marker for
// that sibling behind. See plan §3b-7.
func TestRSSRecoveryDriverHonoursLegacyAliasOwnership(t *testing.T) {
	const (
		feedURL = "https://example.test/feed.xml"
		url     = "https://hooks.slack.test/services/rss-recovery-shared"
		d1      = "slack.rss.general"
		d2      = "slack.rss.general.2"
	)

	tracker := NewMemoryTracker()
	tracker.MarkRSSItemParsed(feedURL, "recovery-item", StoredRSSEntry{
		Key:       "recovery-item",
		FeedURL:   feedURL,
		Title:     "Recovery Item",
		Published: statusNow().Format(time.RFC3339Nano),
	})
	tracker.MarkRSSItemSentToDestination("recovery-item", "Recovery Item", "Feed", d1)
	tracker.MarkRSSItemSentToLegacyDestination("recovery-item", "Recovery Item", "Feed", url, d1)

	if unsent := tracker.GetUnsentRSSItemsForDestinationFeedType(d1, "", nil, url); len(unsent) != 0 {
		t.Fatalf("unsent(%s) = %d, want 0 (already delivered)", d1, len(unsent))
	}
	if unsent := tracker.GetUnsentRSSItemsForDestinationFeedType(d2, "", nil, url); len(unsent) != 1 {
		t.Fatalf("unsent(%s) = %d, want 1 (the sibling never received it)", d2, len(unsent))
	}
	if snapshot, ok := tracker.RSSSentItemSnapshot("recovery-item", d2); ok {
		t.Fatalf("sibling %q got a phantom marker from the recovery driver: %+v", d2, snapshot)
	}
}

// TestMigrateRSSSentLegacyDestinationsSkipsForeignOwnedAlias calls
// migrateRSSSentLegacyDestinations directly, because the read pass
// (rssItemSentToDestinationReadLocked) already applies the same guard first:
// for a sibling asking about an owner-stamped alias, the read returns
// (false, ""), so migrations never contains an entry for it and the guard
// inside migrateRSSSentLegacyDestinations is unreached through every public
// path -- it only fires in the race window between the RUnlock and the Lock
// re-acquisition in GetUnsentRSSItemsForDestinationFeedType. Without this test
// the guard is untested, unreached and unkillable. See plan §3b-8'.
func TestMigrateRSSSentLegacyDestinationsSkipsForeignOwnedAlias(t *testing.T) {
	const (
		itemKey = "rss-item-migration-guard"
		url     = "https://hooks.slack.test/services/rss-migration-guard"
		d1      = "slack.rss.general"
		d2      = "slack.rss.general.2"
	)

	t.Run("owner-stamped alias is not migrated for a foreign destination", func(t *testing.T) {
		tracker := NewMemoryTracker()
		tracker.MarkRSSItemSentToDestination(itemKey, "Example", "Feed", d1)
		tracker.MarkRSSItemSentToLegacyDestination(itemKey, "Example", "Feed", url, d1)
		if err := tracker.SavePendingChanges(); err != nil {
			t.Fatalf("SavePendingChanges() error = %v", err)
		}

		tracker.migrateRSSSentLegacyDestinations([]rssSentLegacyMigration{
			{itemKey: itemKey, destinationID: d2, legacyDestination: url},
		})

		if tracker.DirtyState().RSS {
			t.Fatal("migration of a foreign-owned alias marked RSS status dirty")
		}
		if _, ok := tracker.RSSSentItemSnapshot(itemKey, d2); ok {
			t.Fatal("migration created a key|d2 row for an alias owned by a different destination")
		}
	})

	t.Run("unstamped alias still migrates for the asking destination", func(t *testing.T) {
		tracker := NewMemoryTracker()
		tracker.MarkRSSItemSentToWebhook(itemKey, "Example", "Feed", url)

		tracker.migrateRSSSentLegacyDestinations([]rssSentLegacyMigration{
			{itemKey: itemKey, destinationID: d2, legacyDestination: url},
		})

		if !tracker.DirtyState().RSS {
			t.Fatal("migration of a genuine legacy alias did not mark RSS status dirty")
		}
		if _, ok := tracker.RSSSentItemSnapshot(itemKey, d2); !ok {
			t.Fatal("migration did not create the key|d2 row for a genuine legacy alias")
		}
	})
}

// TestRSSBackfillIgnoresOwnerStampedAliasRows pins plan §4.5's backfill claim:
// backfillRSSContentSignatureSentItems keys derived rows by the *stamped
// owner* (tracker.go makeCompositeKey(contentSignature, sentInfo.
// DestinationID)), never by the URL, so a stamped alias derives exactly the
// composite keys the primary row already derives and the existence guard makes
// the second pass a no-op. The number of content-sig: rows the backfill
// produces must be identical whether the alias is unstamped (pre-fix) or
// stamped with the same owner as the primary row -- no doubling either way.
// See plan §3c-10.
func TestRSSBackfillIgnoresOwnerStampedAliasRows(t *testing.T) {
	const (
		itemKey     = "backfill-shared-item"
		feedURL     = "https://example.test/feed.xml"
		aliasURL    = "https://hooks.slack.test/services/backfill-shared"
		destination = "slack.rss.general"
		published   = "2026-03-04T08:15:00Z"
	)

	buildStatus := func(aliasDestinationID string) string {
		primaryKey := makeCompositeKey(itemKey, destination)
		aliasKey := makeCompositeKey(itemKey, aliasURL)
		aliasDestField := ""
		if aliasDestinationID != "" {
			aliasDestField = fmt.Sprintf(`,"destination_id":%q`, aliasDestinationID)
		}
		return fmt.Sprintf(`{
  "last_updated": %q,
  "feeds": {},
  "parsed_items": [
    {
      "key": %q,
      "feed_url": %q,
      "title": "Backfill Shared Item",
      "description": "backfill body",
      "published": %q,
      "parsed_at": %q
    }
  ],
  "sent_items": {
    %q: {"title":"Backfill Shared Item","feed_title":"Example Feed","sent_at":%q,"destination_id":%q,"item_key":%q},
    %q: {"title":"Backfill Shared Item","feed_title":"Example Feed","sent_at":%q,"item_key":%q%s}
  }
}`, published,
			itemKey, feedURL, published, published,
			primaryKey, published, destination, itemKey,
			aliasKey, published, itemKey, aliasDestField)
	}

	var counts []int
	for _, aliasDestinationID := range []string{"", destination} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, statusRSSFileName), []byte(buildStatus(aliasDestinationID)), 0600); err != nil {
			t.Fatalf("WriteFile(rss_status.json) error = %v", err)
		}
		tracker, err := NewTracker(dir)
		if err != nil {
			t.Fatalf("NewTracker() error = %v", err)
		}

		contentSigRows := 0
		total := 0
		for _, sentInfo := range tracker.rssStatus.SentItems {
			total++
			if strings.HasPrefix(sentInfo.ItemKey, "content-sig:") {
				contentSigRows++
				if sentInfo.DestinationID != destination {
					t.Fatalf("content-sig row owned by %q, want %q (aliasDestinationID=%q)", sentInfo.DestinationID, destination, aliasDestinationID)
				}
			}
		}
		if total != 2+contentSigRows {
			t.Fatalf("total sent rows = %d, want %d (2 originals + %d derived); aliasDestinationID=%q",
				total, 2+contentSigRows, contentSigRows, aliasDestinationID)
		}
		counts = append(counts, contentSigRows)
	}
	if counts[0] == 0 {
		t.Fatal("backfill derived no content-signature rows; test setup produced nothing to compare")
	}
	if counts[0] != counts[1] {
		t.Fatalf("content-sig row counts differ between unstamped and stamped alias: %d vs %d; a stamped alias must not double the backfill",
			counts[0], counts[1])
	}
}

// TestRSSRetentionIgnoresLegacyAliasOwnership pins plan §4.5/§7 risk-adjacent
// claim: cleanupRSSSentItemsRetentionLocked and currentRSSParsedSentItemKeysLocked
// key exclusively on sentInfo.ItemKey and sentInfo.SentAt and never read
// DestinationID, so an owner-stamped alias ages with its primary exactly like
// an unstamped one -- both the age-based prune and the count cap. See plan
// §3c-11.
func TestRSSRetentionIgnoresLegacyAliasOwnership(t *testing.T) {
	const (
		itemKeyA    = "retention-shared-a"
		itemKeyB    = "retention-shared-b"
		destination = "slack.rss.general"
		aliasURL    = "https://hooks.slack.test/services/retention-shared"
	)

	buildRow := func(itemKey, marker string, sentAt time.Time) string {
		return fmt.Sprintf(`%q:{"title":"Example","feed_title":"Example Feed","sent_at":%q,"destination_id":%q,"item_key":%q}`,
			makeCompositeKey(itemKey, marker), sentAt.Format(time.RFC3339Nano), destination, itemKey)
	}

	t.Run("age-based pruning removes an owner-stamped alias with its primary", func(t *testing.T) {
		dir := t.TempDir()
		old := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
		legacyStatus := fmt.Sprintf(`{"last_updated":%q,"feeds":{},"parsed_items":[],"sent_items":{%s,%s}}`,
			old.Format(time.RFC3339Nano), buildRow(itemKeyA, destination, old), buildRow(itemKeyA, aliasURL, old))
		if err := os.WriteFile(filepath.Join(dir, statusRSSFileName), []byte(legacyStatus), 0600); err != nil {
			t.Fatalf("WriteFile(rss_status.json) error = %v", err)
		}
		tracker, err := NewTracker(dir)
		if err != nil {
			t.Fatalf("NewTracker() error = %v", err)
		}
		if got := len(tracker.rssStatus.SentItems); got != 2 {
			t.Fatalf("sent rows before cleanup = %d, want 2", got)
		}

		policy := DefaultRetentionPolicy()
		policy.RSSParsedMaxAge = time.Hour
		tracker.UpdateRetention(policy)
		tracker.CleanupOldEntriesForRSSDestinations([]string{destination})

		if got := len(tracker.rssStatus.SentItems); got != 0 {
			t.Fatalf("sent rows after age-based cleanup = %d, want 0 (the stamped alias ages with its primary)", got)
		}
	})

	t.Run("count cap pops the oldest row regardless of owner", func(t *testing.T) {
		dir := t.TempDir()
		// Recent timestamps on purpose: the default retention policy also
		// enables age-based pruning (RSSParsedMaxAge), and this subtest
		// disables it explicitly below to isolate the count-cap branch, but
		// keeping both rows well within any age window makes the isolation
		// unambiguous even if that default ever changes.
		older := statusNow().Add(-time.Minute)
		newer := statusNow()
		legacyStatus := fmt.Sprintf(`{"last_updated":%q,"feeds":{},"parsed_items":[],"sent_items":{%s,%s}}`,
			newer.Format(time.RFC3339Nano), buildRow(itemKeyA, destination, older), buildRow(itemKeyB, aliasURL, newer))
		if err := os.WriteFile(filepath.Join(dir, statusRSSFileName), []byte(legacyStatus), 0600); err != nil {
			t.Fatalf("WriteFile(rss_status.json) error = %v", err)
		}
		tracker, err := NewTracker(dir)
		if err != nil {
			t.Fatalf("NewTracker() error = %v", err)
		}

		policy := DefaultRetentionPolicy()
		policy.RSSParsedMaxAge = 0
		policy.MaxRSSSentItems = 1
		tracker.UpdateRetention(policy)
		tracker.CleanupOldEntriesForRSSDestinations([]string{destination})

		if _, ok := tracker.RSSSentItemSnapshot(itemKeyA, destination); ok {
			t.Fatal("older row survived the count cap")
		}
		if _, ok := tracker.RSSSentItemSnapshot(itemKeyB, aliasURL); !ok {
			t.Fatal("newer stamped alias row was pruned instead of the older one")
		}
	})
}
