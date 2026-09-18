package status

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadJSONStatusStatFailure(t *testing.T) {
	var target loadJSONStatusSample
	_, err := loadJSONStatus(filepath.Join(t.TempDir(), "bad\x00name.json"), "sample", &target, nil)
	if err == nil || !strings.Contains(err.Error(), "failed to stat") {
		t.Fatalf("loadJSONStatus() error = %v, want stat failure", err)
	}
}

func TestLoadJSONStatusReadFailureOnDirectory(t *testing.T) {
	statusDir := filepath.Join(t.TempDir(), "status-as-dir")
	if err := os.Mkdir(statusDir, 0700); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}

	var target loadJSONStatusSample
	_, err := loadJSONStatus(statusDir, "sample", &target, nil)
	if err == nil || !strings.Contains(err.Error(), "failed to read") {
		t.Fatalf("loadJSONStatus() error = %v, want read failure", err)
	}
}

// makeUndeletableTempFile creates a non-empty directory in place of a status
// temp file so os.Remove reliably fails on both Windows and Unix.
func makeUndeletableTempFile(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, name, "content"), 0700); err != nil {
		t.Fatalf("MkdirAll(%s blocker) error = %v", name, err)
	}
}

func TestCleanupStatusTempFilesReportsUndeletableTempFile(t *testing.T) {
	dir := t.TempDir()
	makeUndeletableTempFile(t, dir, statusAPIFileName+statusTempFileSuffix)

	err := cleanupStatusTempFiles(dir)
	if err == nil || !strings.Contains(err.Error(), "failed to remove stale temp file") {
		t.Fatalf("cleanupStatusTempFiles() error = %v, want removal failure", err)
	}
}

func TestNewTrackerFallsBackToMemoryWhenDataDirIsFile(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "data-dir-is-a-file")
	if err := os.WriteFile(filePath, []byte("not a directory"), 0600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	tracker, err := NewTracker(filePath)
	if err != nil {
		t.Fatalf("NewTracker() error = %v, want memory fallback", err)
	}
	if !tracker.inMemory {
		t.Fatal("NewTracker() did not fall back to in-memory status")
	}
}

func TestNewTrackerFallsBackToMemoryWhenTempCleanupFails(t *testing.T) {
	dir := t.TempDir()
	makeUndeletableTempFile(t, dir, statusRSSFileName+statusTempFileSuffix)

	tracker, err := NewTracker(dir)
	if err != nil {
		t.Fatalf("NewTracker() error = %v, want memory fallback", err)
	}
	if !tracker.inMemory {
		t.Fatal("NewTracker() did not fall back to in-memory status")
	}
}

func TestNewTrackerWithoutMemoryFallbackReturnsConstructionErrors(t *testing.T) {
	t.Run("data dir is a file", func(t *testing.T) {
		filePath := filepath.Join(t.TempDir(), "data-dir-is-a-file")
		if err := os.WriteFile(filePath, []byte("not a directory"), 0600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}

		_, err := newTracker(filePath, true, true, DefaultRetentionPolicy(), true, false)
		if err == nil || !strings.Contains(err.Error(), "failed to create data directory") {
			t.Fatalf("newTracker() error = %v, want data directory failure", err)
		}
	})

	t.Run("stale temp file not removable", func(t *testing.T) {
		dir := t.TempDir()
		makeUndeletableTempFile(t, dir, statusRetryFileName+statusTempFileSuffix)

		_, err := newTracker(dir, true, true, DefaultRetentionPolicy(), true, false)
		if err == nil || !strings.Contains(err.Error(), "failed to cleanup stale status temp files") {
			t.Fatalf("newTracker() error = %v, want temp cleanup failure", err)
		}
	})

	t.Run("write probe failure", func(t *testing.T) {
		originalProbe := probeWritableStatusStoreFunc
		probeWritableStatusStoreFunc = func(string) error {
			return os.ErrPermission
		}
		t.Cleanup(func() {
			probeWritableStatusStoreFunc = originalProbe
		})

		_, err := newTracker(t.TempDir(), true, true, DefaultRetentionPolicy(), true, false)
		if err == nil || !strings.Contains(err.Error(), "status store writeability probe failed") {
			t.Fatalf("newTracker() error = %v, want probe failure", err)
		}
	})
}

func TestEnsurePrivateDataDirRejectsPathUnderFile(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "plain-file")
	if err := os.WriteFile(filePath, []byte("x"), 0600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	err := ensurePrivateDataDir(filepath.Join(filePath, "sub"))
	if err == nil || !strings.Contains(err.Error(), "sub") {
		t.Fatalf("ensurePrivateDataDir() error = %v, want failure for path under file", err)
	}
}

func TestEnsurePrivateDataDirReportsStatFailure(t *testing.T) {
	err := ensurePrivateDataDir(filepath.Join(t.TempDir(), "bad\x00name"))
	if err == nil || !strings.Contains(err.Error(), "stat") {
		t.Fatalf("ensurePrivateDataDir() error = %v, want stat failure", err)
	}
}

func TestProbeWritableStatusStoreFailureModes(t *testing.T) {
	t.Run("temp write blocked", func(t *testing.T) {
		dir := t.TempDir()
		makeUndeletableTempFile(t, dir, ".ransomware-bot-status-write-test.tmp")

		err := probeWritableStatusStore(dir)
		if err == nil || !strings.Contains(err.Error(), "write temp file") {
			t.Fatalf("probeWritableStatusStore() error = %v, want temp write failure", err)
		}
	})

	t.Run("rename blocked", func(t *testing.T) {
		dir := t.TempDir()
		makeUndeletableTempFile(t, dir, ".ransomware-bot-status-write-test")

		err := probeWritableStatusStore(dir)
		if err == nil || !strings.Contains(err.Error(), "rename temp file") {
			t.Fatalf("probeWritableStatusStore() error = %v, want rename failure", err)
		}
		tempFile := filepath.Join(dir, ".ransomware-bot-status-write-test.tmp")
		if _, statErr := os.Stat(tempFile); !os.IsNotExist(statErr) {
			t.Fatalf("probe temp file was not cleaned up, stat err = %v", statErr)
		}
	})
}

func TestWriteAtomicJSONFileMarshalFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "value.json")

	err := writeAtomicJSONFile(path, "sample", make(chan int))
	if err == nil || !strings.Contains(err.Error(), "failed to marshal") {
		t.Fatalf("writeAtomicJSONFile() error = %v, want marshal failure", err)
	}
}

func TestWriteAtomicJSONFileRenameFailureCleansUpTempFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "status.json")
	// Occupy the target path with a non-empty directory so every rename attempt fails.
	if err := os.MkdirAll(filepath.Join(target, "content"), 0700); err != nil {
		t.Fatalf("MkdirAll(target blocker) error = %v", err)
	}

	err := writeAtomicJSONFile(target, "sample", map[string]string{"key": "value"})
	if err == nil || !strings.Contains(err.Error(), "failed to rename") {
		t.Fatalf("writeAtomicJSONFile() error = %v, want rename failure", err)
	}
	if !strings.Contains(err.Error(), "after 3 attempts") {
		t.Fatalf("writeAtomicJSONFile() error = %v, want exhausted rename attempts", err)
	}
	if _, statErr := os.Stat(target + statusTempFileSuffix); !os.IsNotExist(statErr) {
		t.Fatalf("temp file was not cleaned up after rename failure, stat err = %v", statErr)
	}
}

func writeStatusFileForTest(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", name, err)
	}
}

func TestLoadRetryStatusInitializesNullCollections(t *testing.T) {
	dir := t.TempDir()
	writeStatusFileForTest(t, dir, statusRetryFileName,
		`{"last_updated":"2026-01-01T00:00:00Z","retry_queue":null,"dead_letter_items":null}`)

	tracker, err := NewTracker(dir)
	if err != nil {
		t.Fatalf("NewTracker() error = %v", err)
	}

	snapshot := tracker.RetryStatusSnapshot()
	if snapshot.RetryQueueItems != 0 || snapshot.DeadLetterItems != 0 {
		t.Fatalf("retry snapshot = %+v, want empty collections", snapshot)
	}
	if !tracker.enqueueRetryForTest("item-1", "discord.ransomware", "discord", "api", "Example", "boom", 5, time.Hour) {
		t.Fatal("EnqueueRetry() failed after loading null collections")
	}
}

func TestLoadRetryStatusBackfillsAPIPayloadVersion(t *testing.T) {
	dir := t.TempDir()
	writeStatusFileForTest(t, dir, statusRetryFileName, `{
		"last_updated": "2026-01-01T00:00:00Z",
		"retry_queue": {
			"queue-key-1": {
				"item_key": "item-1",
				"destination_id": "discord.ransomware",
				"messenger": "discord",
				"item_type": "api",
				"title": "Example",
				"retry_count": 1,
				"last_error": "boom",
				"first_failed": "2026-01-01T00:00:00Z",
				"last_retried": "2026-01-01T00:00:00Z",
				"payload": {"post_title": "legacy"}
			}
		},
		"dead_letter_items": []
	}`)

	tracker, err := NewTracker(dir)
	if err != nil {
		t.Fatalf("NewTracker() error = %v", err)
	}

	items := tracker.GetRetryItemsByType("api")
	if len(items) != 1 {
		t.Fatalf("retry queue length = %d, want 1", len(items))
	}
	if items[0].PayloadVersion != RetryPayloadVersionRansomwareEntryV1 {
		t.Fatalf("PayloadVersion = %q, want backfilled %q", items[0].PayloadVersion, RetryPayloadVersionRansomwareEntryV1)
	}
	if !tracker.DirtyState().Retry {
		t.Fatal("sanitized retry status was not marked dirty")
	}
}

func TestLoadRetryStatusDeduplicatesDeadLetterItems(t *testing.T) {
	dir := t.TempDir()
	recent := formatStatusTimestamp(statusNow())
	deadLetter := `{
		"item_key": "item-1",
		"destination_id": "discord.ransomware",
		"item_type": "api",
		"messenger": "discord",
		"title": "Example",
		"retry_count": 2,
		"last_error": "boom",
		"terminal_reason": "max_attempts_exhausted",
		"first_failed": "` + recent + `",
		"dead_at": "` + recent + `"
	}`
	writeStatusFileForTest(t, dir, statusRetryFileName, `{
		"last_updated": "2026-01-01T00:00:00Z",
		"retry_queue": {},
		"dead_letter_items": [`+deadLetter+`,`+deadLetter+`]
	}`)

	tracker, err := NewTracker(dir)
	if err != nil {
		t.Fatalf("NewTracker() error = %v", err)
	}

	if deadLetters := tracker.GetDeadLetterItems(); len(deadLetters) != 1 {
		t.Fatalf("dead letter count = %d, want deduplicated 1", len(deadLetters))
	}
	if !tracker.DirtyState().Retry {
		t.Fatal("deduplicated retry status was not marked dirty")
	}
}

func TestLoadRetryStatusDropsQueuedItemsAlreadyDeadLettered(t *testing.T) {
	dir := t.TempDir()
	recent := formatStatusTimestamp(statusNow())
	writeStatusFileForTest(t, dir, statusRetryFileName, `{
		"last_updated": "2026-01-01T00:00:00Z",
		"retry_queue": {
			"queue-key-1": {
				"item_key": "item-1",
				"destination_id": "discord.ransomware",
				"messenger": "discord",
				"item_type": "rss",
				"title": "Example",
				"retry_count": 1,
				"last_error": "boom",
				"first_failed": "`+recent+`",
				"last_retried": "`+recent+`"
			}
		},
		"dead_letter_items": [{
			"item_key": "item-1",
			"destination_id": "discord.ransomware",
			"item_type": "rss",
			"messenger": "discord",
			"title": "Example",
			"retry_count": 3,
			"last_error": "boom",
			"terminal_reason": "max_attempts_exhausted",
			"first_failed": "`+recent+`",
			"dead_at": "`+recent+`"
		}]
	}`)

	tracker, err := NewTracker(dir)
	if err != nil {
		t.Fatalf("NewTracker() error = %v", err)
	}

	snapshot := tracker.RetryStatusSnapshot()
	if snapshot.RetryQueueItems != 0 {
		t.Fatalf("retry queue items = %d, want 0 after dead-letter reconciliation", snapshot.RetryQueueItems)
	}
	if snapshot.DeadLetterItems != 1 {
		t.Fatalf("dead letter items = %d, want 1", snapshot.DeadLetterItems)
	}
}

func TestReadRSSStatusFileInitializesNullCollections(t *testing.T) {
	dir := t.TempDir()
	writeStatusFileForTest(t, dir, statusRSSFileName,
		`{"last_updated":"2026-01-01T00:00:00Z","feeds":null,"parsed_items":null,"sent_items":null}`)

	tracker, err := NewTracker(dir)
	if err != nil {
		t.Fatalf("NewTracker() error = %v", err)
	}

	summary := tracker.StatusSummary()
	if summary.RSSFeeds != 0 || summary.RSSParsedItems != 0 || summary.RSSSentItems != 0 {
		t.Fatalf("status summary = %+v, want empty RSS state", summary)
	}
	tracker.MarkRSSItemParsed("https://f.test/feed.xml", "k", StoredRSSEntry{Title: "Example"})
	if !tracker.IsRSSItemParsed("https://f.test/feed.xml", "k") {
		t.Fatal("parsed marker missing after loading null collections")
	}
}

func TestNormalizeRSSStatusNilReturnsZero(t *testing.T) {
	if got := normalizeRSSStatus(nil); got != 0 {
		t.Fatalf("normalizeRSSStatus(nil) = %d, want 0", got)
	}
}

func TestBackfillRSSContentSignatureSentItemsSkipsUnusableRecords(t *testing.T) {
	t.Run("parsed items without keys", func(t *testing.T) {
		status := &rssStatus{
			ParsedItems: []StoredRSSEntry{{Key: "   ", FeedURL: "https://f.test/feed.xml", Title: "Example"}},
			SentItems: map[string]rssWebhookSentInfo{
				"sent-1": {ItemKey: "k1", DestinationID: "slack.rss"},
			},
		}
		if got := backfillRSSContentSignatureSentItems(status); got != 0 {
			t.Fatalf("backfill = %d, want 0 without keyed parsed items", got)
		}
	})

	t.Run("unusable sent records", func(t *testing.T) {
		status := &rssStatus{
			ParsedItems: []StoredRSSEntry{{
				Key:       "k1",
				FeedURL:   "https://f.test/feed.xml",
				Title:     "Example",
				Published: "2025-01-15T10:30:00Z",
			}},
			SentItems: map[string]rssWebhookSentInfo{
				"blank-key":     {ItemKey: "", DestinationID: "slack.rss"},
				"signature-key": {ItemKey: "content-sig:v3:abc", DestinationID: "slack.rss"},
				"unknown-key":   {ItemKey: "unknown", DestinationID: "slack.rss"},
				"blank-dest":    {ItemKey: "k1", DestinationID: "   "},
			},
		}
		if got := backfillRSSContentSignatureSentItems(status); got != 0 {
			t.Fatalf("backfill = %d, want 0 for unusable sent records", got)
		}
		if len(status.SentItems) != 4 {
			t.Fatalf("sent items = %d, want unchanged 4", len(status.SentItems))
		}
	})
}

func TestBackfillRSSContentSignatureSentItemsCreatesTitledAliases(t *testing.T) {
	published := time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC)
	item := StoredRSSEntry{
		Key:       "k1",
		FeedURL:   "https://example.test/feed.xml",
		Title:     "Example Report",
		FeedTitle: "Example Feed",
		Published: published.Format(time.RFC3339Nano),
	}
	expectedAliases := len(storedRSSContentSignatures(item))
	if expectedAliases == 0 {
		t.Fatal("test setup produced no content signatures")
	}

	status := &rssStatus{
		ParsedItems: []StoredRSSEntry{item},
		SentItems: map[string]rssWebhookSentInfo{
			makeCompositeKey(item.Key, "slack.rss"): {
				ItemKey:       item.Key,
				DestinationID: "slack.rss",
				SentAt:        published,
			},
		},
	}

	backfilled := backfillRSSContentSignatureSentItems(status)
	if backfilled != expectedAliases {
		t.Fatalf("backfill = %d, want %d aliases", backfilled, expectedAliases)
	}
	if len(status.SentItems) != 1+expectedAliases {
		t.Fatalf("sent items = %d, want original plus %d aliases", len(status.SentItems), expectedAliases)
	}
	for compositeKey, sentInfo := range status.SentItems {
		if sentInfo.ItemKey == item.Key {
			continue
		}
		if sentInfo.Title != item.Title || sentInfo.FeedTitle != item.FeedTitle {
			t.Fatalf("alias %q = %+v, want backfilled title and feed title", compositeKey, sentInfo)
		}
	}

	if again := backfillRSSContentSignatureSentItems(status); again != 0 {
		t.Fatalf("second backfill = %d, want 0 for existing aliases", again)
	}
}

func TestMigrateLegacyRSSProcessedItemsSkipsBlankKeysAndNilParsedItems(t *testing.T) {
	lastCheck := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	status := &rssStatus{
		LastUpdated: time.Date(2025, 6, 2, 0, 0, 0, 0, time.UTC),
		Feeds: map[string]FeedInfo{
			"https://f.test/feed.xml": {
				LastCheck: lastCheck,
				ProcessedItems: map[string]string{
					"":   "ignored blank",
					"  ": "ignored whitespace",
					"k1": "Example Title",
				},
			},
		},
		ParsedItems: nil,
	}

	migrated := migrateLegacyRSSProcessedItems(status)

	if migrated != 2 { // one migrated item plus one cleared legacy feed
		t.Fatalf("migrateLegacyRSSProcessedItems() = %d, want 2", migrated)
	}
	if len(status.ParsedItems) != 1 || status.ParsedItems[0].Key != "k1" {
		t.Fatalf("parsed items = %+v, want single migrated k1", status.ParsedItems)
	}
	if got := status.ParsedItems[0].ParsedAt; got != formatStatusTimestamp(lastCheck) {
		t.Fatalf("ParsedAt = %q, want feed last check %q", got, formatStatusTimestamp(lastCheck))
	}
	if status.Feeds["https://f.test/feed.xml"].ProcessedItems != nil {
		t.Fatal("legacy processed items were not cleared")
	}
}

func TestMigrateLegacyRSSProcessedItemsSkipsAlreadyParsedKeys(t *testing.T) {
	status := &rssStatus{
		Feeds: map[string]FeedInfo{
			"https://f.test/feed.xml": {
				ProcessedItems: map[string]string{"existing": "Already Parsed"},
			},
		},
		ParsedItems: []StoredRSSEntry{{Key: "existing", FeedURL: "https://f.test/feed.xml", Title: "Kept"}},
	}

	migrated := migrateLegacyRSSProcessedItems(status)

	if migrated != 1 { // only the cleared legacy feed counts
		t.Fatalf("migrateLegacyRSSProcessedItems() = %d, want 1", migrated)
	}
	if len(status.ParsedItems) != 1 || status.ParsedItems[0].Title != "Kept" {
		t.Fatalf("parsed items = %+v, want unchanged existing entry", status.ParsedItems)
	}
}

func TestLegacyRSSProcessedParsedAtFallbacks(t *testing.T) {
	lastCheck := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	lastUpdated := time.Date(2025, 6, 2, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name        string
		lastUpdated time.Time
		lastCheck   time.Time
		want        string
	}{
		{name: "feed last check wins", lastUpdated: lastUpdated, lastCheck: lastCheck, want: formatStatusTimestamp(lastCheck)},
		{name: "status last updated fallback", lastUpdated: lastUpdated, want: formatStatusTimestamp(lastUpdated)},
		{name: "no usable timestamp", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := legacyRSSProcessedParsedAt(tt.lastUpdated, tt.lastCheck); got != tt.want {
				t.Fatalf("legacyRSSProcessedParsedAt() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBackfillRSSContentSignatureSentItemsSkipsSkipMarkers(t *testing.T) {
	published := time.Date(2025, 3, 4, 8, 15, 0, 0, time.UTC)
	item := StoredRSSEntry{
		Key:       "skip-marker-item",
		FeedURL:   "https://example.test/feed.xml",
		Title:     "Example Report",
		FeedTitle: "Example Feed",
		Published: published.Format(time.RFC3339Nano),
	}
	signaturesPerItem := len(storedRSSContentSignatures(item))
	if signaturesPerItem == 0 {
		t.Fatal("test setup produced no content signatures")
	}

	newTrackerWithParsedItem := func(t *testing.T) *Tracker {
		t.Helper()
		tracker, err := NewTracker(t.TempDir())
		if err != nil {
			t.Fatalf("NewTracker() error = %v", err)
		}
		tracker.MarkRSSItemsParsed(item.FeedURL, map[string]StoredRSSEntry{item.Key: item})
		return tracker
	}

	tests := []struct {
		name          string
		mark          func(tracker *Tracker)
		wantBackfills int
	}{
		{
			name: "skip marker is not expanded into content signatures",
			mark: func(tracker *Tracker) {
				tracker.MarkRSSItemSkippedForDestination(item.Key, "slack.rss.general")
			},
			wantBackfills: 0,
		},
		{
			name: "sent marker is expanded into content signatures",
			mark: func(tracker *Tracker) {
				tracker.MarkRSSItemSentToDestination(item.Key, item.Title, item.FeedTitle, "slack.rss.general")
			},
			wantBackfills: signaturesPerItem,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tracker := newTrackerWithParsedItem(t)
			tt.mark(tracker)

			tracker.mutex.Lock()
			rssState := tracker.rssStatus
			tracker.mutex.Unlock()

			if got := backfillRSSContentSignatureSentItems(rssState); got != tt.wantBackfills {
				t.Fatalf("backfill = %d, want %d", got, tt.wantBackfills)
			}
		})
	}

	t.Run("legacy record without skipped field is still backfilled", func(t *testing.T) {
		// Decode the record from JSON that predates the field, so this pins the
		// wire compatibility ("keep reading the old one"), not just a zero value.
		legacyJSON := `{"sent_at":"` + published.Format(time.RFC3339Nano) +
			`","destination_id":"slack.rss.general","item_key":"` + item.Key + `"}`
		var legacy rssWebhookSentInfo
		if err := json.Unmarshal([]byte(legacyJSON), &legacy); err != nil {
			t.Fatalf("Unmarshal(legacy sent record) error = %v", err)
		}
		if legacy.Skipped {
			t.Fatal("a record without a skipped field must decode as Skipped=false")
		}

		rssState := &rssStatus{
			ParsedItems: []StoredRSSEntry{item},
			SentItems: map[string]rssWebhookSentInfo{
				makeCompositeKey(item.Key, "slack.rss.general"): legacy,
			},
		}
		if got := backfillRSSContentSignatureSentItems(rssState); got != signaturesPerItem {
			t.Fatalf("legacy backfill = %d, want %d", got, signaturesPerItem)
		}
	})

	t.Run("skipped is omitted from send markers and written for skip markers", func(t *testing.T) {
		// omitempty keeps every real-send record byte-identical to files written
		// by builds that predate the field.
		sent, err := json.Marshal(rssWebhookSentInfo{ItemKey: item.Key, SentAt: published})
		if err != nil {
			t.Fatalf("Marshal(sent record) error = %v", err)
		}
		if strings.Contains(string(sent), "skipped") {
			t.Fatalf("real send record must not serialise a skipped field: %s", sent)
		}
		skipMarker, err := json.Marshal(rssWebhookSentInfo{ItemKey: item.Key, SentAt: published, Skipped: true})
		if err != nil {
			t.Fatalf("Marshal(skip record) error = %v", err)
		}
		if !strings.Contains(string(skipMarker), `"skipped":true`) {
			t.Fatalf("skip marker must serialise skipped=true: %s", skipMarker)
		}
		var roundTripped rssWebhookSentInfo
		if err := json.Unmarshal(skipMarker, &roundTripped); err != nil {
			t.Fatalf("Unmarshal(skip record) error = %v", err)
		}
		if !roundTripped.Skipped {
			t.Fatal("skipped=true did not survive a JSON round-trip")
		}
	})
}

// TestBackfillRSSContentSignatureSentItemsIgnoresDerivedMarkers pins the fix
// itself: the marker a content-signature dedup hit writes for the SUPPRESSED
// entry must never be expanded into content-signature markers at load time,
// because that would mint a second, self-renewing anchor for a story whose own
// anchor already exists. It must still suppress the entry and still keep
// recovery from re-delivering it, and files written before the flag existed
// must keep behaving exactly as before.
//
//nolint:gocyclo // long sequential scenario asserting backfill outcomes across multiple marker states; not a table split candidate
func TestBackfillRSSContentSignatureSentItemsIgnoresDerivedMarkers(t *testing.T) {
	const destination = "slack.rss.general"
	const feedType = "general"
	published := time.Date(2026, 3, 4, 8, 15, 0, 0, time.UTC)
	item := StoredRSSEntry{
		Key:         "derived-backfill-item",
		FeedURL:     "https://example.test/feed.xml",
		FeedType:    feedType,
		Title:       "Example Report",
		Link:        "https://example.test/articles/example-report",
		Description: "example body",
		Published:   published.Format(time.RFC3339Nano),
		FeedTitle:   "Example Feed",
		ParsedAt:    published.Format(time.RFC3339Nano),
	}
	signatures := storedRSSContentSignatures(item)
	if len(signatures) == 0 {
		t.Fatal("test setup produced no content signatures")
	}

	newTrackerWithParsedItem := func(t *testing.T) (*Tracker, string) {
		t.Helper()
		dir := t.TempDir()
		tracker, err := NewTracker(dir)
		if err != nil {
			t.Fatalf("NewTracker() error = %v", err)
		}
		tracker.MarkRSSItemsParsed(item.FeedURL, map[string]StoredRSSEntry{item.Key: item})
		return tracker, dir
	}

	reload := func(t *testing.T, tracker *Tracker, dir string) *Tracker {
		t.Helper()
		if err := tracker.SavePendingChanges(); err != nil {
			t.Fatalf("SavePendingChanges() error = %v", err)
		}
		reloaded, err := NewTracker(dir)
		if err != nil {
			t.Fatalf("NewTracker(reload) error = %v", err)
		}
		return reloaded
	}

	assertNoAliases := func(t *testing.T, tracker *Tracker, label string) {
		t.Helper()
		for _, signature := range signatures {
			if signature == item.Key {
				continue
			}
			if tracker.IsRSSItemSentToDestination(signature, destination) {
				t.Fatalf("%s was expanded into content-signature marker %q", label, signature)
			}
		}
	}

	t.Run("derived marker is not expanded into content signatures", func(t *testing.T) {
		tracker, dir := newTrackerWithParsedItem(t)
		tracker.MarkRSSItemDedupedForDestination(item.Key, item.Title, item.FeedTitle, destination)
		reloaded := reload(t, tracker, dir)

		assertNoAliases(t, reloaded, "the derived marker")

		// A derived marker still suppresses and still keeps recovery from
		// re-delivering the item: only the load-time re-derivation is withheld.
		if !reloaded.IsRSSItemSentToDestination(item.Key, destination) {
			t.Fatal("the derived marker stopped suppressing its own item key")
		}
		for _, unsent := range reloaded.GetUnsentRSSItemsForDestinationFeedType(destination, feedType, nil) {
			if unsent.Key == item.Key {
				t.Fatal("recovery returned an item whose derived marker says it was already delivered")
			}
		}
	})

	t.Run("skip marker is still not expanded into content signatures", func(t *testing.T) {
		tracker, dir := newTrackerWithParsedItem(t)
		tracker.MarkRSSItemSkippedForDestination(item.Key, destination)
		reloaded := reload(t, tracker, dir)

		assertNoAliases(t, reloaded, "the filter-mismatch skip marker")
	})

	t.Run("record written before either flag existed is still backfilled", func(t *testing.T) {
		dir := t.TempDir()
		parsedItem, err := json.Marshal(item)
		if err != nil {
			t.Fatalf("Marshal(parsed item) error = %v", err)
		}
		// Hand-written status file whose sent_items row omits both flags, so
		// this pins the wire compatibility, not just a zero value.
		legacyStatus := `{"last_updated":"` + published.Format(time.RFC3339Nano) +
			`","feeds":{},"parsed_items":[` + string(parsedItem) +
			`],"sent_items":{"` + makeCompositeKey(item.Key, destination) +
			`":{"title":"` + item.Title + `","feed_title":"` + item.FeedTitle +
			`","sent_at":"` + published.Format(time.RFC3339Nano) +
			`","destination_id":"` + destination + `","item_key":"` + item.Key + `"}}}`
		if err := os.WriteFile(filepath.Join(dir, statusRSSFileName), []byte(legacyStatus), 0600); err != nil {
			t.Fatalf("WriteFile(rss_status.json) error = %v", err)
		}

		tracker, err := NewTracker(dir)
		if err != nil {
			t.Fatalf("NewTracker(legacy file) error = %v", err)
		}
		snapshot, ok := tracker.RSSSentItemSnapshot(item.Key, destination)
		if !ok {
			t.Fatal("the hand-written sent marker was not loaded")
		}
		if snapshot.Skipped || snapshot.Derived {
			t.Fatalf("a record without either key decoded as skipped %t / derived %t, want both false",
				snapshot.Skipped, snapshot.Derived)
		}

		aliases := 0
		for _, signature := range signatures {
			if signature == item.Key {
				continue
			}
			if tracker.IsRSSItemSentToDestination(signature, destination) {
				aliases++
			}
		}
		if aliases == 0 {
			t.Fatal("an unflagged legacy marker was no longer expanded into content signatures")
		}

		if err := tracker.SavePendingChanges(); err != nil {
			t.Fatalf("SavePendingChanges() error = %v", err)
		}
		saved, err := os.ReadFile(filepath.Join(dir, statusRSSFileName))
		if err != nil {
			t.Fatalf("ReadFile(rss_status.json) error = %v", err)
		}
		if strings.Contains(string(saved), `"derived":`) || strings.Contains(string(saved), `"skipped":`) {
			t.Fatalf("re-saving a file written before the flags existed invented a flag key: %s", saved)
		}
	})
}

// TestRSSRetentionTreatsDerivedMarkersLikeDeliveries pins invariant 3 of the
// derived-marker change: retention is IDENTICAL for a derived marker and a
// genuine delivery marker. Nothing else in the suite drives a derived marker
// through the two sent_items pruning paths, so without this test a change that
// exempts derived markers from retention -- which would make them immortal
// suppressions, exactly the failure mode the flag was introduced to end --
// passes every other test in the package.
func TestRSSRetentionTreatsDerivedMarkersLikeDeliveries(t *testing.T) {
	const destination = "slack.rss.general"

	t.Run("age based pruning removes a derived marker like a genuine one", func(t *testing.T) {
		// Hand-written file so sent_at is genuinely old and "derived": true has
		// to survive the JSON round-trip to reach the retention pass.
		dir := t.TempDir()
		old := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC).Format(time.RFC3339Nano)
		row := func(itemKey string, derived bool) string {
			flag := ""
			if derived {
				flag = `"derived":true,`
			}
			return `"` + makeCompositeKey(itemKey, destination) + `":{` + flag +
				`"title":"Example Report","feed_title":"Example Feed","sent_at":"` + old +
				`","destination_id":"` + destination + `","item_key":"` + itemKey + `"}`
		}
		legacyStatus := `{"last_updated":"` + old + `","feeds":{},"parsed_items":[],"sent_items":{` +
			row("genuine-orphan", false) + `,` + row("derived-orphan", true) + `}}`
		if err := os.WriteFile(filepath.Join(dir, statusRSSFileName), []byte(legacyStatus), 0600); err != nil {
			t.Fatalf("WriteFile(rss_status.json) error = %v", err)
		}

		tracker, err := NewTracker(dir)
		if err != nil {
			t.Fatalf("NewTracker() error = %v", err)
		}
		snapshot, ok := tracker.RSSSentItemSnapshot("derived-orphan", destination)
		if !ok || !snapshot.Derived {
			t.Fatalf("derived marker did not survive the load: found %t, derived %t", ok, snapshot.Derived)
		}

		policy := DefaultRetentionPolicy()
		policy.RSSParsedMaxAge = time.Hour
		tracker.UpdateRetention(policy)
		tracker.CleanupOldEntriesForRSSDestinations([]string{destination})

		for _, itemKey := range []string{"genuine-orphan", "derived-orphan"} {
			if _, ok := tracker.RSSSentItemSnapshot(itemKey, destination); ok {
				t.Fatalf("sent marker %q outlived rss_parsed_max_age; a derived marker must age out on exactly the same schedule as a delivery marker", itemKey)
			}
		}
	})

	t.Run("count cap prunes derived markers like genuine ones", func(t *testing.T) {
		policy := DefaultRetentionPolicy()
		policy.MaxRSSSentItems = 1
		tracker := NewMemoryTrackerWithRetention(policy)

		now := statusNow().Format(time.RFC3339Nano)
		tracker.MarkRSSItemParsed("https://f.test/feed.xml", "protected", StoredRSSEntry{Title: "P", Published: now})
		tracker.MarkRSSItemSentToDestination("protected", "P", "Feed", destination)
		// Both orphans are derived: under a retention pass that exempts derived
		// markers, neither is a removal candidate and both survive the cap.
		tracker.MarkRSSItemDedupedForDestination("derived-orphan-1", "D1", "Feed", destination)
		tracker.MarkRSSItemDedupedForDestination("derived-orphan-2", "D2", "Feed", destination)

		tracker.CleanupOldEntriesForRSSDestinations([]string{destination})

		if _, ok := tracker.RSSSentItemSnapshot("protected", destination); !ok {
			t.Fatal("the marker of a live parsed row was pruned by the count cap")
		}
		for _, itemKey := range []string{"derived-orphan-1", "derived-orphan-2"} {
			if _, ok := tracker.RSSSentItemSnapshot(itemKey, destination); ok {
				t.Fatalf("derived marker %q survived count-based pruning; MaxRSSSentItems must count and evict it exactly like a delivery marker", itemKey)
			}
		}
	})

	t.Run("evicting a parsed row removes its derived marker with it", func(t *testing.T) {
		policy := DefaultRetentionPolicy()
		policy.MaxRSSParsedItems = 1
		tracker := NewMemoryTrackerWithRetention(policy)

		older := StoredRSSEntry{
			Key: "older", FeedURL: "https://f.test/feed.xml", Title: "Older",
			Published: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339Nano),
		}
		newer := StoredRSSEntry{
			Key: "newer", FeedURL: "https://f.test/feed.xml", Title: "Newer",
			Published: statusNow().Format(time.RFC3339Nano),
		}
		tracker.MarkRSSItemParsed(older.FeedURL, older.Key, older)
		tracker.MarkRSSItemParsed(newer.FeedURL, newer.Key, newer)
		// A derived marker counts as delivered, so the older row is eligible for
		// eviction -- and the row it owns must go with it.
		tracker.MarkRSSItemDedupedForDestination(older.Key, older.Title, "Feed", destination)
		tracker.MarkRSSItemSentToDestination(newer.Key, newer.Title, "Feed", destination)

		tracker.CleanupOldEntriesForRSSDestinations([]string{destination})

		keys := rssItemKeys(tracker.RSSParsedItemsSnapshot())
		if len(keys) != 1 || keys[0] != newer.Key {
			t.Fatalf("parsed items after the cap = %v, want [%s]; a derived marker must make its row fully delivered", keys, newer.Key)
		}
		if _, ok := tracker.RSSSentItemSnapshot(older.Key, destination); ok {
			t.Fatal("the derived marker outlived the parsed row it belongs to, leaving an orphaned suppression behind")
		}
		if _, ok := tracker.RSSSentItemSnapshot(newer.Key, destination); !ok {
			t.Fatal("the surviving row's delivery marker was removed")
		}
	})
}

// TestSavePendingChangesPersistsAttemptBudgetDeadLetter pins that a dead letter
// created when the retry budget runs out marks the retry store dirty, is written by
// SavePendingChanges, and survives a reload with no stale retry_queue row behind it.
func TestSavePendingChangesPersistsAttemptBudgetDeadLetter(t *testing.T) {
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
	if !tracker.DirtyState().Retry {
		t.Fatal("budget-exhausted dead letter did not mark the retry store dirty, SavePendingChanges would skip it")
	}
	if err := tracker.SavePendingChanges(); err != nil {
		t.Fatalf("SavePendingChanges() error = %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, statusRetryFileName))
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", statusRetryFileName, err)
	}
	var persisted retryStatus
	if err := json.Unmarshal(raw, &persisted); err != nil {
		t.Fatalf("Unmarshal(%s) error = %v: %s", statusRetryFileName, err, raw)
	}
	if len(persisted.RetryQueue) != 0 {
		t.Fatalf("persisted retry_queue = %+v, want no stale row", persisted.RetryQueue)
	}
	if len(persisted.DeadLetterItems) != 1 {
		t.Fatalf("persisted dead_letter_items = %d, want 1: %s", len(persisted.DeadLetterItems), raw)
	}

	reloaded, err := NewTracker(dir)
	if err != nil {
		t.Fatalf("NewTracker(reload) error = %v", err)
	}
	if !reloaded.IsRetryDeadLetteredForDestination("item-1", "discord.ransomware", "discord", "api") {
		t.Fatal("reloaded tracker does not report the item dead-lettered")
	}
	if items := reloaded.GetRetryItemsByType("api"); len(items) != 0 {
		t.Fatalf("reloaded retry queue length = %d, want 0", len(items))
	}
	deadLetters := reloaded.GetDeadLetterItems()
	if len(deadLetters) != 1 {
		t.Fatalf("reloaded dead letter count = %d, want 1", len(deadLetters))
	}
	if deadLetters[0].TerminalReason != TerminalReasonMaxAttempts {
		t.Fatalf("reloaded TerminalReason = %q, want %q", deadLetters[0].TerminalReason, TerminalReasonMaxAttempts)
	}
	if deadLetters[0].RetryCount != 2 {
		t.Fatalf("reloaded RetryCount = %d, want 2", deadLetters[0].RetryCount)
	}
	if deadLetters[0].DestinationID != "discord.ransomware" {
		t.Fatalf("reloaded DestinationID = %q, want %q", deadLetters[0].DestinationID, "discord.ransomware")
	}
}
