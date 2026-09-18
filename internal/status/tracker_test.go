package status

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/rss"

	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

type loadJSONStatusSample struct {
	Items map[string]string `json:"items"`
}

func newTestTracker(t *testing.T) *Tracker {
	t.Helper()
	tracker, err := NewTracker(t.TempDir())
	if err != nil {
		t.Fatalf("NewTracker() error = %v", err)
	}
	return tracker
}

func newTestTrackerWithDir(t *testing.T) (*Tracker, string) {
	t.Helper()
	dir := t.TempDir()
	tracker, err := NewTracker(dir)
	if err != nil {
		t.Fatalf("NewTracker() error = %v", err)
	}
	return tracker, dir
}

func TestStoredRSSEntryFromRSS(t *testing.T) {
	entry := rss.Entry{
		Title:       "Test Article",
		Link:        "https://example.com/article",
		Description: "A test article",
		Published:   time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC),
		Author:      "Author",
		Categories:  []string{"security"},
		GUID:        "guid-123",
		FeedTitle:   "Test Feed",
		FeedURL:     "https://example.com/feed",
	}

	stored := StoredRSSEntryFromRSS(entry, "test-key")

	if stored.Key != "test-key" {
		t.Errorf("expected key test-key, got %q", stored.Key)
	}
	if stored.Title != entry.Title {
		t.Errorf("expected title %q, got %q", entry.Title, stored.Title)
	}
	if stored.FeedURL != entry.FeedURL {
		t.Errorf("expected FeedURL %q, got %q", entry.FeedURL, stored.FeedURL)
	}
	if stored.Published == "" {
		t.Error("expected non-empty published timestamp")
	}
	if _, err := time.Parse(time.RFC3339Nano, stored.Published); err != nil {
		t.Fatalf("Published = %q, want RFC3339Nano timestamp: %v", stored.Published, err)
	}
	if stored.ParsedAt == "" {
		t.Error("expected non-empty ParsedAt timestamp")
	}
	if _, err := time.Parse(time.RFC3339Nano, stored.ParsedAt); err != nil {
		t.Fatalf("ParsedAt = %q, want status timestamp: %v", stored.ParsedAt, err)
	}
}

func TestStoredRSSEntryFromRSSBoundsPersistentFields(t *testing.T) {
	categories := make([]string, maxStoredRSSCategories+5)
	for i := range categories {
		categories[i] = strings.Repeat("c", maxStoredRSSCategoryRunes+20)
	}
	entry := rss.Entry{
		Title:       strings.Repeat("t", maxStoredRSSTitleRunes+20),
		Link:        "https://example.test/" + strings.Repeat("a", maxStoredRSSURLRunes),
		Description: strings.Repeat("d", maxStoredRSSDescriptionRunes+20),
		Author:      strings.Repeat("a", maxStoredRSSAuthorRunes+20),
		Categories:  categories,
		GUID:        strings.Repeat("g", maxStoredRSSGUIDRunes+20),
		FeedTitle:   strings.Repeat("f", maxStoredRSSFeedTitleRunes+20),
		FeedURL:     "https://example.test/feed.xml",
	}

	stored := StoredRSSEntryFromRSS(entry, "rss-key")

	if len([]rune(stored.Title)) > maxStoredRSSTitleRunes {
		t.Fatalf("Title length = %d, want <= %d", len([]rune(stored.Title)), maxStoredRSSTitleRunes)
	}
	if stored.Link != "" {
		t.Fatalf("overlong Link = %q, want cleared", stored.Link)
	}
	if len([]rune(stored.Description)) > maxStoredRSSDescriptionRunes {
		t.Fatalf("Description length = %d, want <= %d", len([]rune(stored.Description)), maxStoredRSSDescriptionRunes)
	}
	if len([]rune(stored.Author)) > maxStoredRSSAuthorRunes {
		t.Fatalf("Author length = %d, want <= %d", len([]rune(stored.Author)), maxStoredRSSAuthorRunes)
	}
	if len(stored.Categories) > maxStoredRSSCategories {
		t.Fatalf("Categories count = %d, want <= %d", len(stored.Categories), maxStoredRSSCategories)
	}
	for _, category := range stored.Categories {
		if len([]rune(category)) > maxStoredRSSCategoryRunes {
			t.Fatalf("Category length = %d, want <= %d", len([]rune(category)), maxStoredRSSCategoryRunes)
		}
	}
	if len([]rune(stored.GUID)) > maxStoredRSSGUIDRunes {
		t.Fatalf("GUID length = %d, want <= %d", len([]rune(stored.GUID)), maxStoredRSSGUIDRunes)
	}
	if len([]rune(stored.FeedTitle)) > maxStoredRSSFeedTitleRunes {
		t.Fatalf("FeedTitle length = %d, want <= %d", len([]rune(stored.FeedTitle)), maxStoredRSSFeedTitleRunes)
	}
}

func TestLoadJSONStatusInitializesCollections(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "sample_status.json")
	if err := os.WriteFile(filePath, []byte(`{"items":null}`), 0600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	var loaded loadJSONStatusSample
	ok, err := loadJSONStatus(filePath, "sample status", &loaded, func(status *loadJSONStatusSample) {
		if status.Items == nil {
			status.Items = make(map[string]string)
		}
	})
	if err != nil {
		t.Fatalf("loadJSONStatus() error = %v", err)
	}
	if !ok {
		t.Fatal("loadJSONStatus() loaded = false, want true")
	}
	if loaded.Items == nil {
		t.Fatal("init callback did not initialize Items")
	}
}

func TestLoadJSONStatusMissingFile(t *testing.T) {
	var loaded loadJSONStatusSample
	ok, err := loadJSONStatus(filepath.Join(t.TempDir(), "missing.json"), "sample status", &loaded, nil)
	if err != nil {
		t.Fatalf("loadJSONStatus() missing file error = %v", err)
	}
	if ok {
		t.Fatal("loadJSONStatus() loaded = true for missing file")
	}
}

func TestNewTrackerCreatesPrivateDataDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix mode bits are not authoritative on Windows")
	}

	dataDir := filepath.Join(t.TempDir(), "data")
	if _, err := NewTracker(dataDir); err != nil {
		t.Fatalf("NewTracker() error = %v", err)
	}

	info, err := os.Stat(dataDir)
	if err != nil {
		t.Fatalf("Stat(dataDir) error = %v", err)
	}
	if got := info.Mode().Perm(); got != privateDataDirMode {
		t.Fatalf("data dir mode = %v, want %v", got, privateDataDirMode)
	}
}

func TestNewTrackerTightensExistingDataDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix mode bits are not authoritative on Windows")
	}

	dataDir := filepath.Join(t.TempDir(), "data")
	if err := os.Mkdir(dataDir, 0755); err != nil { //nolint:gosec // G301: deliberately loose starting mode, this test asserts NewTracker tightens it
		t.Fatalf("Mkdir(dataDir) error = %v", err)
	}

	if _, err := NewTracker(dataDir); err != nil {
		t.Fatalf("NewTracker() error = %v", err)
	}

	info, err := os.Stat(dataDir)
	if err != nil {
		t.Fatalf("Stat(dataDir) error = %v", err)
	}
	if got := info.Mode().Perm(); got != privateDataDirMode {
		t.Fatalf("data dir mode = %v, want %v", got, privateDataDirMode)
	}
}

func TestNewTrackerFallsBackToMemoryWhenStatusStoreWriteProbeFails(t *testing.T) {
	dataDir := t.TempDir()
	probeErr := errors.New("probe denied")
	originalProbe := probeWritableStatusStoreFunc
	probeWritableStatusStoreFunc = func(string) error {
		return probeErr
	}
	t.Cleanup(func() {
		probeWritableStatusStoreFunc = originalProbe
	})

	tracker, err := NewTracker(dataDir)
	if err != nil {
		t.Fatalf("NewTracker() error = %v, want memory fallback", err)
	}
	if !tracker.inMemory {
		t.Fatal("NewTracker() did not fall back to in-memory status")
	}
	tracker.UpdateAPIStatus(true, 1, "")
	if err := tracker.SavePendingChanges(); err != nil {
		t.Fatalf("SavePendingChanges() in-memory fallback error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, statusAPIFileName)); !os.IsNotExist(err) {
		t.Fatalf("api status file exists after in-memory save, stat err = %v", err)
	}
}

func TestNewReadOnlyTrackerSkipsStatusStoreWriteProbe(t *testing.T) {
	dataDir := t.TempDir()
	originalProbe := probeWritableStatusStoreFunc
	probeWritableStatusStoreFunc = func(string) error {
		t.Fatal("NewReadOnlyTracker called writer status-store probe")
		return nil
	}
	t.Cleanup(func() {
		probeWritableStatusStoreFunc = originalProbe
	})

	if _, err := NewReadOnlyTracker(dataDir); err != nil {
		t.Fatalf("NewReadOnlyTracker() error = %v", err)
	}
}

func TestEnqueueRetryRedactsWebhookSecretsFromPersistedErrors(t *testing.T) {
	tracker, dir := newTestTrackerWithDir(t)

	slackURL := "https://hooks.slack.com/services/T12345678/B12345678/abcdefghijklmnopqrstuvwxyz012345" //nolint:gosec // G101: test fixture, not a real credential
	discordURL := "https://discord.com/api/v9/webhooks/123456789012345678/abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_"
	lastError := "slack failed: " + slackURL + " discord failed: " + discordURL

	if !tracker.enqueueRetryForTest("item-1", discordURL, "discord", "api", "Example", lastError, 3, time.Hour) {
		t.Fatal("EnqueueRetry() unexpectedly moved item to dead letter")
	}
	if err := tracker.SavePendingChanges(); err != nil {
		t.Fatalf("SavePendingChanges() error = %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "retry_status.json"))
	if err != nil {
		t.Fatalf("ReadFile(retry_status.json) error = %v", err)
	}
	got := string(data)

	for _, secret := range []string{slackURL, discordURL} {
		if strings.Contains(got, secret) {
			t.Fatalf("persisted retry status leaked webhook secret %q: %s", secret, got)
		}
	}
	for _, marker := range []string{
		"https://hooks.slack.com/services/[redacted]",
		"https://discord.com/api/webhooks/[redacted]",
	} {
		if !strings.Contains(got, marker) {
			t.Fatalf("persisted retry status missing redacted marker %q: %s", marker, got)
		}
	}
}

func TestSavePendingChangesWritesStatusJSONWithFinalNewline(t *testing.T) {
	tracker, dir := newTestTrackerWithDir(t)

	tracker.UpdateAPIStatus(true, 1, "")
	tracker.MarkRSSItemParsed("https://example.test/feed.xml", "rss-item", StoredRSSEntry{
		Title:     "RSS item",
		FeedTitle: "Feed",
		Published: time.Now().UTC().Format(time.RFC3339),
	})
	if !tracker.enqueueRetryForTest("api-item", "discord.ransomware", "discord", "api", "Example", "temporary failure", 3, time.Hour) {
		t.Fatal("EnqueueRetry() unexpectedly failed")
	}

	if err := tracker.SavePendingChanges(); err != nil {
		t.Fatalf("SavePendingChanges() error = %v", err)
	}

	for _, name := range []string{"api_status.json", "rss_status.json", "retry_status.json"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("ReadFile(%s) error = %v", name, err)
		}
		if len(data) == 0 || data[len(data)-1] != '\n' {
			t.Fatalf("%s does not end with final newline: %q", name, data)
		}
	}
}

func TestRecordDeliveryAuditEventAppendsJSONLAndRedactsSecrets(t *testing.T) {
	tracker, dir := newTestTrackerWithDir(t)

	discordURL := "https://discord.com/api/webhooks/123456789012345678/abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_"
	tracker.RecordDeliveryAuditEvent(DeliveryAuditEvent{ //nolint:gosec // G101: test fixture, not a real credential
		EventType:     DeliveryAuditEventAlertCandidate,
		Source:        RetryItemTypeAPI.String(),
		ItemType:      RetryItemTypeAPI.String(),
		ItemKey:       "api-item-1",
		Title:         strings.Repeat("t", maxAuditStringRunes+20),
		Messenger:     MessengerDiscord.String(),
		DestinationID: discordURL,
		FeedURL:       "https://user:pass@example.test/feed.xml",
		Outcome:       DeliveryAuditOutcomeFiltered,
		Reason:        DeliveryAuditReasonFilterMismatch,
		Details: map[string]string{
			"error": "failed posting to " + discordURL,
		},
	})
	tracker.RecordDeliveryAuditEvent(DeliveryAuditEvent{
		EventType:     DeliveryAuditEventDeliveryState,
		Source:        RetryItemTypeAPI.String(),
		ItemType:      RetryItemTypeAPI.String(),
		ItemKey:       "api-item-1",
		Messenger:     MessengerDiscord.String(),
		DestinationID: "discord.ransomware",
		Outcome:       DeliveryAuditOutcomeDelivered,
	})

	data, err := os.ReadFile(filepath.Join(dir, statusAuditFileName))
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", statusAuditFileName, err)
	}
	got := string(data)
	if strings.Contains(got, discordURL) {
		t.Fatalf("audit file leaked raw Discord webhook URL: %s", got)
	}
	if strings.Contains(got, "user:pass") {
		t.Fatalf("audit file leaked feed URL credentials: %s", got)
	}

	lines := strings.Split(strings.TrimSpace(got), "\n")
	if len(lines) != 2 {
		t.Fatalf("audit line count = %d, want 2: %s", len(lines), got)
	}

	var first DeliveryAuditEvent
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("Unmarshal(first audit line) error = %v", err)
	}
	if first.DestinationID != "" {
		t.Fatalf("raw webhook destination persisted as %q, want stripped", first.DestinationID)
	}
	if first.Details["error"] == "" || strings.Contains(first.Details["error"], discordURL) {
		t.Fatalf("audit details were not redacted: %#v", first.Details)
	}
	if len([]rune(first.Title)) > maxAuditStringRunes {
		t.Fatalf("audit title length = %d, want <= %d", len([]rune(first.Title)), maxAuditStringRunes)
	}

	var second DeliveryAuditEvent
	if err := json.Unmarshal([]byte(lines[1]), &second); err != nil {
		t.Fatalf("Unmarshal(second audit line) error = %v", err)
	}
	if second.DestinationID != "discord.ransomware" {
		t.Fatalf("stable destination = %q, want discord.ransomware", second.DestinationID)
	}
}

func TestEnqueueAPIRetryPersistsPayloadVersion(t *testing.T) {
	tracker, dir := newTestTrackerWithDir(t)

	webhookURL := "https://discord.com/api/webhooks/123456789012345678/test-token"
	payload := []byte(`{"id":"victim-1","group":"lockbit","victim":"Example Corp"}`)
	if !tracker.enqueueRetryForTest("item-1", webhookURL, "discord", "api", "Example Corp", "temporary failure", 3, time.Hour, payload) {
		t.Fatal("EnqueueRetry() unexpectedly failed")
	}

	items := tracker.GetRetryItemsByType("api")
	if len(items) != 1 {
		t.Fatalf("retry queue length = %d, want 1", len(items))
	}
	if items[0].PayloadVersion != RetryPayloadVersionRansomwareEntryV1 {
		t.Fatalf("payload version = %q, want %q", items[0].PayloadVersion, RetryPayloadVersionRansomwareEntryV1)
	}

	if err := tracker.SavePendingChanges(); err != nil {
		t.Fatalf("SavePendingChanges() error = %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "retry_status.json"))
	if err != nil {
		t.Fatalf("ReadFile(retry_status.json) error = %v", err)
	}
	if !strings.Contains(string(data), `"payload_version": "ransomware_entry_v1"`) {
		t.Fatalf("persisted retry status missing payload_version: %s", data)
	}
}

func TestRetryItemsByTypeUsesMaintainedTypeIndex(t *testing.T) {
	tracker := NewMemoryTracker()

	if !tracker.enqueueRetryForTest("api-item", "discord.ransomware", "discord", "api", "API Item", "temporary failure", 3, time.Hour) {
		t.Fatal("EnqueueRetry(api) unexpectedly failed")
	}
	if !tracker.enqueueRetryForTest("rss-item", "discord.rss.general", "discord", "rss", "RSS Item", "temporary failure", 3, time.Hour) {
		t.Fatal("EnqueueRetry(rss) unexpectedly failed")
	}

	if items := tracker.GetRetryItemsByType("api"); len(items) != 1 || items[0].ItemKey != "api-item" {
		t.Fatalf("GetRetryItemsByType(api) = %#v, want api-item only", items)
	}
	if items := tracker.GetRetryItemsByType("rss"); len(items) != 1 || items[0].ItemKey != "rss-item" {
		t.Fatalf("GetRetryItemsByType(rss) = %#v, want rss-item only", items)
	}

	queueItems := tracker.GetQueuedRetryItemsByType("api")
	if len(queueItems) != 1 {
		t.Fatalf("GetQueuedRetryItemsByType(api) length = %d, want 1", len(queueItems))
	}
	tracker.DeadLetterRetryQueueEntry(queueItems[0].QueueKey, "terminal failure")
	if items := tracker.GetRetryItemsByType("api"); len(items) != 0 {
		t.Fatalf("GetRetryItemsByType(api) after dead-letter = %#v, want empty", items)
	}
	if items := tracker.GetRetryItemsByType("rss"); len(items) != 1 || items[0].ItemKey != "rss-item" {
		t.Fatalf("GetRetryItemsByType(rss) = %#v, want rss-item only", items)
	}
}

func TestRetryErrorInfoPersistsToRetryAndDeadLetter(t *testing.T) {
	tracker := NewMemoryTracker()
	retryable := true
	if !tracker.EnqueueRetry(RetryRequest{
		ItemKey:       "item-1",
		DestinationID: "slack.ransomware",
		Messenger:     MessengerSlack,
		ItemType:      RetryItemTypeAPI,
		Title:         "Example Corp",
		LastError:     "provider unavailable",
		ErrorInfo: RetryErrorInfo{
			ErrorCategory: "provider_unavailable",
			StatusCode:    503,
			Retryable:     &retryable,
		},
		MaxAttempts: 1,
		RetryWindow: time.Hour,
		Payload:     []byte(`{"id":"victim-1"}`),
	}) {
		t.Fatal("EnqueueRetry() unexpectedly failed")
	}

	items := tracker.GetRetryItemsByType("api")
	if len(items) != 1 {
		t.Fatalf("retry queue length = %d, want 1", len(items))
	}
	if items[0].ErrorCategory != "provider_unavailable" || items[0].StatusCode != 503 || items[0].Retryable == nil || !*items[0].Retryable {
		t.Fatalf("retry error info = category %q status %d retryable %v", items[0].ErrorCategory, items[0].StatusCode, items[0].Retryable)
	}

	queueItems := tracker.GetQueuedRetryItemsByType("api")
	if len(queueItems) != 1 {
		t.Fatalf("queued retry length = %d, want 1", len(queueItems))
	}
	retryable = false
	if tracker.RecordRetryQueueFailureWithErrorInfo(queueItems[0].QueueKey, "invalid webhook", RetryErrorInfo{
		ErrorCategory: "invalid_webhook",
		StatusCode:    404,
		Retryable:     &retryable,
	}, 1, time.Hour) {
		t.Fatal("RecordRetryQueueFailureWithErrorInfo() kept item queued, want dead letter")
	}

	deadLetters := tracker.GetDeadLetterItems()
	if len(deadLetters) != 1 {
		t.Fatalf("dead letter count = %d, want 1", len(deadLetters))
	}
	if deadLetters[0].ErrorCategory != "invalid_webhook" || deadLetters[0].StatusCode != 404 || deadLetters[0].Retryable == nil || *deadLetters[0].Retryable {
		t.Fatalf("dead-letter error info = category %q status %d retryable %v", deadLetters[0].ErrorCategory, deadLetters[0].StatusCode, deadLetters[0].Retryable)
	}
}

func TestEnqueueRetryClonesPayloadInput(t *testing.T) {
	tracker := NewMemoryTracker()

	webhookURL := "https://discord.com/api/webhooks/123456789012345678/test-token"
	payload := []byte(`{"id":"victim-1","group":"lockbit"}`)
	if !tracker.enqueueRetryForTest("item-1", webhookURL, "discord", "api", "Example Corp", "temporary failure", 3, time.Hour, payload) {
		t.Fatal("EnqueueRetry() unexpectedly failed")
	}

	copy(payload, []byte(`{"id":"mutated"`))

	items := tracker.GetRetryItemsByType("api")
	if len(items) != 1 {
		t.Fatalf("retry queue length = %d, want 1", len(items))
	}
	if strings.Contains(string(items[0].Payload), "mutated") {
		t.Fatalf("stored payload was mutated through caller slice alias: %s", items[0].Payload)
	}
}

//nolint:gocyclo // long sequential scenario asserting dead-letter transition and payload state; not a table split candidate
func TestDeadLetterTransitionPreservesReplayPayload(t *testing.T) {
	tracker, dir := newTestTrackerWithDir(t)

	itemKey := "api-item-with-payload"
	destinationID := "slack.ransomware"
	payload := []byte(`{"id":"victim-1","victim":"Example Corp"}`)
	if !tracker.enqueueRetryForTest(itemKey, destinationID, "slack", "api", "Example Corp", "first failure", 1, time.Hour, payload) {
		t.Fatal("initial EnqueueRetryForDestination() unexpectedly failed")
	}
	if tracker.enqueueRetryForTest(itemKey, destinationID, "slack", "api", "Example Corp", "second failure", 1, time.Hour, payload) {
		t.Fatal("second EnqueueRetryForDestination() accepted item past max attempts")
	}

	if items := tracker.GetRetryItemsByType("api"); len(items) != 0 {
		t.Fatalf("retry queue length = %d, want 0 after dead letter", len(items))
	}
	deadLetters := tracker.GetDeadLetterItems()
	if len(deadLetters) != 1 {
		t.Fatalf("dead letter count = %d, want 1", len(deadLetters))
	}
	dead := deadLetters[0]
	if string(dead.Payload) != string(payload) {
		t.Fatalf("dead letter payload = %s, want %s", dead.Payload, payload)
	}
	if dead.PayloadVersion != RetryPayloadVersionRansomwareEntryV1 {
		t.Fatalf("dead letter payload version = %q, want %q", dead.PayloadVersion, RetryPayloadVersionRansomwareEntryV1)
	}

	if err := tracker.SavePendingChanges(); err != nil {
		t.Fatalf("SavePendingChanges() error = %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "retry_status.json"))
	if err != nil {
		t.Fatalf("ReadFile(retry_status.json) error = %v", err)
	}
	if !strings.Contains(string(data), `"payload":`) || !strings.Contains(string(data), `"payload_version": "ransomware_entry_v1"`) {
		t.Fatalf("persisted dead letter is missing replay payload fields: %s", data)
	}

	reloaded, err := NewTracker(dir)
	if err != nil {
		t.Fatalf("NewTracker(reload) error = %v", err)
	}
	reloadedDeadLetters := reloaded.GetDeadLetterItems()
	if len(reloadedDeadLetters) != 1 {
		t.Fatalf("reloaded dead letter count = %d, want 1", len(reloadedDeadLetters))
	}
	reloadedDead := reloadedDeadLetters[0]
	if reloadedDead.ItemKey != itemKey {
		t.Fatalf("reloaded dead letter item key = %q, want %q", reloadedDead.ItemKey, itemKey)
	}
	if reloadedDead.ItemType != RetryItemTypeAPI.String() {
		t.Fatalf("reloaded dead letter item type = %q, want api", reloadedDead.ItemType)
	}
	if reloadedDead.Messenger != MessengerSlack.String() {
		t.Fatalf("reloaded dead letter messenger = %q, want slack", reloadedDead.Messenger)
	}
	if reloadedDead.Title != "Example Corp" {
		t.Fatalf("reloaded dead letter title = %q, want Example Corp", reloadedDead.Title)
	}
	if reloadedDead.DeadAt == "" {
		t.Fatal("reloaded dead letter DeadAt is empty")
	}
	var reloadedPayload map[string]string
	if err := json.Unmarshal(reloadedDead.Payload, &reloadedPayload); err != nil {
		t.Fatalf("reloaded dead letter payload is not valid JSON: %v", err)
	}
	if reloadedPayload["id"] != "victim-1" || reloadedPayload["victim"] != "Example Corp" {
		t.Fatalf("reloaded dead letter payload = %#v, want victim payload", reloadedPayload)
	}
	if reloadedDead.PayloadVersion != RetryPayloadVersionRansomwareEntryV1 {
		t.Fatalf("reloaded dead letter payload version = %q, want %q", reloadedDead.PayloadVersion, RetryPayloadVersionRansomwareEntryV1)
	}
}

func TestGetRetryItemsByTypeClonesPayloadOutput(t *testing.T) {
	tracker := NewMemoryTracker()

	webhookURL := "https://discord.com/api/webhooks/123456789012345678/test-token"
	if !tracker.enqueueRetryForTest("item-1", webhookURL, "discord", "api", "Example Corp", "temporary failure", 3, time.Hour, []byte(`{"id":"victim-1"}`)) {
		t.Fatal("EnqueueRetry() unexpectedly failed")
	}

	items := tracker.GetRetryItemsByType("api")
	if len(items) != 1 {
		t.Fatalf("retry queue length = %d, want 1", len(items))
	}
	copy(items[0].Payload, []byte(`{"id":"mutated"`))

	items = tracker.GetRetryItemsByType("api")
	if strings.Contains(string(items[0].Payload), "mutated") {
		t.Fatalf("stored payload was mutated through returned slice alias: %s", items[0].Payload)
	}
}

func TestRemoveFromRetryQueueMissingLeavesRetryStateUnchanged(t *testing.T) {
	tracker := NewMemoryTracker()
	if !tracker.enqueueRetryForTest("kept-item", "discord.ransomware", "discord", "api", "Kept Item", "temporary failure", 3, time.Hour) {
		t.Fatal("EnqueueRetry() unexpectedly failed")
	}
	tracker.clearDirtyState()

	tracker.RemoveFromRetryQueueByDestination("missing-item", "discord.ransomware")

	items := tracker.GetRetryItemsByType("api")
	if len(items) != 1 {
		t.Fatalf("retry queue length = %d, want 1", len(items))
	}
	if items[0].ItemKey != "kept-item" {
		t.Fatalf("remaining retry item key = %q, want kept-item", items[0].ItemKey)
	}
	if tracker.DirtyState().Retry {
		t.Fatal("retry dirty state changed for missing retry queue removal")
	}
}

func TestGetDeadLetterItemsClonesPayloadOutput(t *testing.T) {
	tracker := NewMemoryTracker()

	itemKey := "item-1"
	destinationID := "discord.ransomware"
	payload := []byte(`{"id":"victim-1"}`)
	if !tracker.enqueueRetryForTest(itemKey, destinationID, "discord", "api", "Example Corp", "first failure", 1, time.Hour, payload) {
		t.Fatal("initial EnqueueRetryForDestination() unexpectedly failed")
	}
	if tracker.enqueueRetryForTest(itemKey, destinationID, "discord", "api", "Example Corp", "second failure", 1, time.Hour, payload) {
		t.Fatal("second EnqueueRetryForDestination() accepted item past max attempts")
	}

	items := tracker.GetDeadLetterItems()
	if len(items) != 1 {
		t.Fatalf("dead letter count = %d, want 1", len(items))
	}
	copy(items[0].Payload, []byte(`{"id":"mutated"`))

	items = tracker.GetDeadLetterItems()
	if strings.Contains(string(items[0].Payload), "mutated") {
		t.Fatalf("stored dead-letter payload was mutated through returned slice alias: %s", items[0].Payload)
	}
}

func TestAPIItemSentDestinationMigratesLegacyWebhookURLKey(t *testing.T) {
	tracker := NewMemoryTracker()

	itemKey := "api-item-1"
	oldWebhookURL := "https://discord.com/api/webhooks/123456789012345678/old-token"
	newWebhookURL := "https://discord.com/api/webhooks/123456789012345678/new-token"
	destinationID := "discord.ransomware"

	tracker.MarkAPIItemSentToWebhook(itemKey, "Example", oldWebhookURL)
	if !tracker.IsAPIItemSentToDestination(itemKey, destinationID, oldWebhookURL) {
		t.Fatal("legacy webhook-keyed API sent marker was not recognized for stable destination")
	}
	if !tracker.IsAPIItemSentToDestination(itemKey, destinationID, newWebhookURL) {
		t.Fatal("migrated API sent marker did not survive webhook URL rotation")
	}
	if !tracker.IsAPIItemSentToWebhook(itemKey, oldWebhookURL) {
		t.Fatal("legacy API sent marker should remain readable after destination migration")
	}
}

func TestRSSItemSentDestinationMigratesLegacyWebhookURLKey(t *testing.T) {
	tracker := NewMemoryTracker()

	feedURL := "https://example.test/feed.xml"
	itemKey := "rss-item-1"
	oldWebhookURL := "https://hooks.slack.com/services/T12345678/B12345678/oldtoken1234567890"
	newWebhookURL := "https://hooks.slack.com/services/T12345678/B12345678/newtoken1234567890"
	destinationID := "slack.rss.general"

	tracker.MarkRSSItemParsed(feedURL, itemKey, StoredRSSEntry{
		Key:       itemKey,
		FeedURL:   feedURL,
		Title:     "RSS Item",
		Published: time.Now().UTC().Format(time.RFC3339),
	})
	tracker.MarkRSSItemSentToWebhook(itemKey, "RSS Item", "Example Feed", oldWebhookURL)

	if unsent := tracker.GetUnsentRSSItemsForDestination(destinationID, oldWebhookURL); len(unsent) != 0 {
		t.Fatalf("legacy webhook-keyed RSS sent marker was not recognized; unsent count = %d", len(unsent))
	}
	if unsent := tracker.GetUnsentRSSItemsForDestination(destinationID, newWebhookURL); len(unsent) != 0 {
		t.Fatalf("migrated RSS sent marker did not survive webhook URL rotation; unsent count = %d", len(unsent))
	}
	if !tracker.IsRSSItemSentToWebhook(itemKey, oldWebhookURL) {
		t.Fatal("legacy RSS sent marker should remain readable after destination migration")
	}
}

func TestSentItemsPersistDestinationID(t *testing.T) {
	tracker, dir := newTestTrackerWithDir(t)

	tracker.MarkAPIItemSentToDestination("api-item", "API Item", "discord.ransomware")
	tracker.MarkRSSItemSentToDestination("rss-item", "RSS Item", "Example Feed", "slack.rss.general")
	if err := tracker.SavePendingChanges(); err != nil {
		t.Fatalf("SavePendingChanges() error = %v", err)
	}

	expectFileContains(t, filepath.Join(dir, "api_status.json"), `"destination_id": "discord.ransomware"`)
	expectFileContains(t, filepath.Join(dir, "rss_status.json"), `"destination_id": "slack.rss.general"`)
}

func TestRSSFeedHTTPValidatorsPersistKeepAndPrune(t *testing.T) {
	tracker, dir := newTestTrackerWithDir(t)
	feedURL := "https://example.test/feed.xml"
	validators := rss.FeedHTTPValidators{
		ETag:         `"etag-v1"`,
		LastModified: "Wed, 21 Oct 2015 07:28:00 GMT",
	}

	tracker.UpdateFeedStatusWithErrorInfoAndValidators(feedURL, true, 1, "", SourceErrorInfo{}, validators)
	if err := tracker.SavePendingChanges(); err != nil {
		t.Fatalf("SavePendingChanges() error = %v", err)
	}
	expectFileContains(t, filepath.Join(dir, "rss_status.json"), `"etag": "\"etag-v1\""`)
	expectFileContains(t, filepath.Join(dir, "rss_status.json"), `"last_modified": "Wed, 21 Oct 2015 07:28:00 GMT"`)

	reloaded, err := NewTracker(dir)
	if err != nil {
		t.Fatalf("NewTracker(reload) error = %v", err)
	}
	got := reloaded.GetRSSFeedHTTPValidators([]string{feedURL})
	if got[feedURL] != validators {
		t.Fatalf("validators after reload = %+v, want %+v", got[feedURL], validators)
	}
	got[feedURL] = rss.FeedHTTPValidators{ETag: `"mutated"`}
	gotAgain := reloaded.GetRSSFeedHTTPValidators([]string{feedURL})
	if gotAgain[feedURL] != validators {
		t.Fatalf("validators snapshot mutation affected tracker: %+v", gotAgain[feedURL])
	}

	// A successful fetch that carries no validators must keep the stored pair:
	// a 304 need not repeat ETag/Last-Modified (RFC 7232 only recommends it), so
	// an empty value means "unchanged". Clearing it would stop conditional
	// polling for good. Pruning is still the only thing that removes them.
	reloaded.UpdateFeedStatusWithErrorInfoAndValidators(feedURL, true, 1, "", SourceErrorInfo{}, rss.FeedHTTPValidators{})
	if got := reloaded.GetRSSFeedHTTPValidators([]string{feedURL}); got[feedURL] != validators {
		t.Fatalf("validators after successful fetch without validators = %#v, want %+v (kept)", got, validators)
	}

	reloaded.UpdateFeedStatusWithErrorInfoAndValidators(feedURL, true, 1, "", SourceErrorInfo{}, validators)
	if pruned := reloaded.PruneRSSFeedStatus(nil); pruned != 1 {
		t.Fatalf("PruneRSSFeedStatus() = %d, want 1", pruned)
	}
	if got := reloaded.GetRSSFeedHTTPValidators([]string{feedURL}); len(got) != 0 {
		t.Fatalf("validators after prune = %#v, want none", got)
	}
}

func TestRSSFeedFailureCooldownSkipsRepeatedFailuresAndResetsOnSuccess(t *testing.T) {
	tracker := NewMemoryTracker()
	feedURL := "https://example.test/broken.xml"

	tracker.UpdateFeedStatus(feedURL, false, 0, "temporary failure 1")
	tracker.UpdateFeedStatus(feedURL, false, 0, "temporary failure 2")
	if !tracker.ShouldPollRSSFeed(feedURL, statusNow()) {
		t.Fatal("feed entered cooldown before threshold")
	}

	tracker.UpdateFeedStatus(feedURL, false, 0, "temporary failure 3")
	feedInfo, ok := tracker.GetRSSFeedInfo(feedURL)
	if !ok {
		t.Fatalf("GetRSSFeedInfo(%q) missing feed", feedURL)
	}
	if feedInfo.ConsecutiveFailures != 3 {
		t.Fatalf("ConsecutiveFailures = %d, want 3", feedInfo.ConsecutiveFailures)
	}
	if feedInfo.NextAttemptAfter == nil {
		t.Fatal("NextAttemptAfter = nil after cooldown threshold")
	}
	if tracker.ShouldPollRSSFeed(feedURL, statusNow()) {
		t.Fatal("feed should be skipped while failure cooldown is active")
	}
	if !tracker.ShouldPollRSSFeed(feedURL, feedInfo.NextAttemptAfter.Add(time.Second)) {
		t.Fatal("feed should be pollable after failure cooldown expires")
	}

	tracker.UpdateFeedStatus(feedURL, true, 1, "")
	feedInfo, ok = tracker.GetRSSFeedInfo(feedURL)
	if !ok {
		t.Fatalf("GetRSSFeedInfo(%q) missing feed after success", feedURL)
	}
	if feedInfo.ConsecutiveFailures != 0 {
		t.Fatalf("ConsecutiveFailures after success = %d, want 0", feedInfo.ConsecutiveFailures)
	}
	if feedInfo.NextAttemptAfter != nil {
		t.Fatalf("NextAttemptAfter after success = %v, want nil", feedInfo.NextAttemptAfter)
	}
}

func TestEnqueueRetryForDestinationPersistsDestinationID(t *testing.T) {
	tracker, dir := newTestTrackerWithDir(t)

	destinationID := "discord.ransomware"
	webhookURL := "https://discord.com/api/webhooks/123456789012345678/secret-token"
	if !tracker.enqueueRetryForTest("api-item-1", destinationID, "discord", "api", "Example", "temporary failure", 3, time.Hour) {
		t.Fatal("EnqueueRetryForDestination() unexpectedly failed")
	}

	items := tracker.GetQueuedRetryItemsByType("api")
	if len(items) != 1 {
		t.Fatalf("retry queue length = %d, want 1", len(items))
	}
	if got := items[0].Item.DestinationID; got != destinationID {
		t.Fatalf("destination_id = %q, want %q", got, destinationID)
	}

	if err := tracker.SavePendingChanges(); err != nil {
		t.Fatalf("SavePendingChanges() error = %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "retry_status.json"))
	if err != nil {
		t.Fatalf("ReadFile(retry_status.json) error = %v", err)
	}
	persisted := string(data)
	if !strings.Contains(persisted, `"destination_id": "discord.ransomware"`) {
		t.Fatalf("persisted retry status missing destination_id: %s", persisted)
	}
	if strings.Contains(persisted, webhookURL) {
		t.Fatalf("persisted retry status leaked webhook URL: %s", persisted)
	}
}

func TestDeadLetterStateIsScopedToDestination(t *testing.T) {
	tracker := newTestTracker(t)

	itemKey := "api-item-1"
	deadDestination := "discord.ransomware"
	healthyDestination := "discord.government"
	tracker.MarkRetryDeadLetterForDestination(itemKey, deadDestination, "discord", "api", "Example", "terminal failure")

	if !tracker.IsRetryDeadLetteredForDestination(itemKey, deadDestination, "discord", "api") {
		t.Fatal("dead-lettered destination should be blocked")
	}
	if tracker.IsRetryDeadLetteredForDestination(itemKey, healthyDestination, "discord", "api") {
		t.Fatal("different destination should not be blocked by destination-scoped dead letter")
	}
	if tracker.enqueueRetryForTest(itemKey, deadDestination, "discord", "api", "Example", "temporary failure", 3, time.Hour) {
		t.Fatal("dead-lettered destination accepted a retry")
	}
	if !tracker.enqueueRetryForTest(itemKey, healthyDestination, "discord", "api", "Example", "temporary failure", 3, time.Hour) {
		t.Fatal("different destination should remain retryable")
	}

	deadLetters := tracker.GetDeadLetterItems()
	if len(deadLetters) != 1 {
		t.Fatalf("dead letter count = %d, want 1", len(deadLetters))
	}
	if got := deadLetters[0].DestinationID; got != deadDestination {
		t.Fatalf("dead letter destination_id = %q, want %q", got, deadDestination)
	}
}

func TestMarkRetryDeadLetterForDestinationRemovesExistingQueuedRetry(t *testing.T) {
	tracker := newTestTracker(t)

	itemKey := "api-item-queued-then-dead"
	destinationID := "slack.ransomware"
	if !tracker.enqueueRetryForTest(itemKey, destinationID, "slack", "api", "Example", "temporary failure", 5, time.Hour) {
		t.Fatal("initial retry enqueue unexpectedly failed")
	}

	tracker.MarkRetryDeadLetterForDestination(itemKey, destinationID, "slack", "api", "Example", "terminal failure")

	if items := tracker.GetQueuedRetryItemsByType("api"); len(items) != 0 {
		t.Fatalf("retry queue length = %d, want 0 after terminal dead letter", len(items))
	}
	if !tracker.IsRetryDeadLetteredForDestination(itemKey, destinationID, "slack", "api") {
		t.Fatal("dead-lettered destination should be blocked")
	}
}

func TestLegacyDeadLetterBlocksAllDestinations(t *testing.T) {
	tracker := newTestTracker(t)

	tracker.MarkRetryDeadLetter("api-item-1", "discord", "api", "Example", "terminal failure")

	if !tracker.IsRetryDeadLetteredForDestination("api-item-1", "discord.ransomware", "discord", "api") {
		t.Fatal("legacy dead-letter entry should block destination-specific lookups")
	}
	if tracker.enqueueRetryForTest("api-item-1", "discord.ransomware", "discord", "api", "Example", "temporary failure", 3, time.Hour) {
		t.Fatal("legacy dead-letter entry accepted a destination-specific retry")
	}
}

func TestRetryLifecycleTimestampsAreOffsetAware(t *testing.T) {
	tracker := newTestTracker(t)

	webhookURL := "https://discord.com/api/webhooks/123456789012345678/test-token"
	if !tracker.enqueueRetryForTest("retry-item", webhookURL, "discord", "api", "Retry Item", "temporary failure", 3, time.Hour) {
		t.Fatal("EnqueueRetry() unexpectedly moved item to dead letter")
	}
	items := tracker.GetRetryItemsByType("api")
	if len(items) != 1 {
		t.Fatalf("retry items = %d, want 1", len(items))
	}
	for label, value := range map[string]string{
		"first_failed": items[0].FirstFailed,
		"last_retried": items[0].LastRetried,
	} {
		if _, err := time.Parse(time.RFC3339Nano, value); err != nil {
			t.Fatalf("%s = %q, want RFC3339Nano timestamp: %v", label, value, err)
		}
	}

	queueKey := queuedRetryKeyForTest(t, tracker, "retry-item", "api")
	if tracker.RecordRetryQueueFailure(queueKey, "terminal failure", 1, time.Hour) {
		t.Fatal("RecordRetryQueueFailure() should move item to dead letter at max attempts")
	}
	deadLetters := tracker.GetDeadLetterItems()
	if len(deadLetters) != 1 {
		t.Fatalf("dead letter count = %d, want 1", len(deadLetters))
	}
	dead := deadLetters[0]
	for label, value := range map[string]string{
		"first_failed": dead.FirstFailed,
		"dead_at":      dead.DeadAt,
	} {
		if _, err := time.Parse(time.RFC3339Nano, value); err != nil {
			t.Fatalf("%s = %q, want RFC3339Nano timestamp: %v", label, value, err)
		}
	}
}

func TestLoadRetryStatusRedactsExistingWebhookSecrets(t *testing.T) {
	dir := t.TempDir()
	slackURL := "https://hooks.slack.com/services/T12345678/B12345678/abcdefghijklmnopqrstuvwxyz012345" //nolint:gosec // G101: test fixture, not a real credential
	discordURL := "https://discord.com/api/v9/webhooks/123456789012345678/abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_"
	rawStatus := `{
  "last_updated": "2026-01-01T00:00:00Z",
  "retry_queue": {
    "queue-key": {
      "item_key": "item-1",
      "messenger": "discord",
      "item_type": "api",
      "title": "Example",
      "retry_count": 1,
      "last_error": "queue leaked ` + slackURL + ` and ` + discordURL + `",
      "first_failed": "2026-01-01 00:00:00",
      "last_retried": "2026-01-01 00:00:00"
    }
  },
  "dead_letter_items": [
    {
      "item_key": "item-2",
      "messenger": "slack",
      "item_type": "rss",
      "title": "Example",
      "retry_count": 3,
      "last_error": "dead leaked ` + slackURL + `",
      "first_failed": "2026-01-01 00:00:00",
      "dead_at": "2026-01-01 00:00:00"
    }
  ]
}`
	if err := os.WriteFile(filepath.Join(dir, "retry_status.json"), []byte(rawStatus), 0600); err != nil {
		t.Fatalf("WriteFile(retry_status.json) error = %v", err)
	}

	tracker, err := NewTracker(dir)
	if err != nil {
		t.Fatalf("NewTracker() error = %v", err)
	}
	if err := tracker.SavePendingChanges(); err != nil {
		t.Fatalf("SavePendingChanges() error = %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "retry_status.json"))
	if err != nil {
		t.Fatalf("ReadFile(retry_status.json) error = %v", err)
	}
	got := string(data)

	for _, secret := range []string{slackURL, discordURL} {
		if strings.Contains(got, secret) {
			t.Fatalf("loaded retry status still contains webhook secret %q: %s", secret, got)
		}
	}
}

func TestSavePendingChangesKeepsDirtyStateAfterSaveFailures(t *testing.T) {
	tracker, dir := newTestTrackerWithDir(t)

	webhookURL := "https://discord.com/api/webhooks/123456789012345678/test-token"
	tracker.MarkAPIItemSentToWebhook("api-item", "API Item", webhookURL)
	tracker.MarkRSSItemParsed("https://example.test/feed.xml", "rss-item", StoredRSSEntry{
		Title:     "RSS Item",
		Link:      "https://example.test/rss-item",
		Published: "2026-01-01 00:00:00",
	})
	if !tracker.enqueueRetryForTest("retry-item", webhookURL, "discord", "api", "Retry Item", "temporary failure", 3, time.Hour) {
		t.Fatal("EnqueueRetry() unexpectedly moved item to dead letter")
	}

	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("RemoveAll(data dir) error = %v", err)
	}
	if err := os.WriteFile(dir, []byte("not a directory"), 0600); err != nil {
		t.Fatalf("WriteFile(data dir placeholder) error = %v", err)
	}

	if err := tracker.SavePendingChanges(); err == nil {
		t.Fatal("SavePendingChanges() error = nil, want save failure")
	}

	if err := os.Remove(dir); err != nil {
		t.Fatalf("Remove(data dir placeholder) error = %v", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("MkdirAll(data dir) error = %v", err)
	}

	if err := tracker.SavePendingChanges(); err != nil {
		t.Fatalf("SavePendingChanges() after restoring data dir error = %v", err)
	}

	expectFileContains(t, filepath.Join(dir, "api_status.json"), `"sent_at"`)
	expectFileContains(t, filepath.Join(dir, "rss_status.json"), "rss-item")
	expectFileContains(t, filepath.Join(dir, "retry_status.json"), "retry-item")
}

func TestSavePendingChangesReleasesTrackerLockBeforeDiskWrite(t *testing.T) {
	tracker, _ := newTestTrackerWithDir(t)
	tracker.MarkAPIItemSentToDestination("api-item", "API Item", "discord.ransomware")

	originalWriter := writeAtomicJSONFileFunc
	called := false
	writeAtomicJSONFileFunc = func(filePath, label string, value interface{}) error {
		called = true
		if !tracker.CanAcquireStateLockForStatusWrite() {
			t.Fatal("SavePendingChanges held tracker mutex during status file write")
		}
		return originalWriter(filePath, label, value)
	}
	t.Cleanup(func() {
		writeAtomicJSONFileFunc = originalWriter
	})

	if err := tracker.SavePendingChanges(); err != nil {
		t.Fatalf("SavePendingChanges() error = %v", err)
	}
	if !called {
		t.Fatal("test writer was not called")
	}
}

func TestStatusSaveErrorsIncludeTemporaryFilePath(t *testing.T) {
	webhookURL := "https://discord.com/api/webhooks/123456789012345678/test-token"
	tests := []struct {
		name string
		file string
		mark func(*Tracker)
		save func(*Tracker) error
	}{
		{
			name: "api",
			file: "api_status.json",
			mark: func(tracker *Tracker) {
				tracker.MarkAPIItemSentToWebhook("api-item", "API Item", webhookURL)
			},
			save: func(tracker *Tracker) error {
				return tracker.saveAPIStatus()
			},
		},
		{
			name: "rss",
			file: "rss_status.json",
			mark: func(tracker *Tracker) {
				tracker.MarkRSSItemParsed("https://example.test/feed.xml", "rss-item", StoredRSSEntry{
					Title:     "RSS Item",
					Published: "2026-01-01 00:00:00",
				})
			},
			save: func(tracker *Tracker) error {
				return tracker.saveRSSStatus()
			},
		},
		{
			name: "retry",
			file: "retry_status.json",
			mark: func(tracker *Tracker) {
				tracker.enqueueRetryForTest("retry-item", webhookURL, "discord", "api", "Retry Item", "temporary failure", 3, time.Hour)
			},
			save: func(tracker *Tracker) error {
				return tracker.saveRetryStatus()
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			tracker, err := NewTracker(dir)
			if err != nil {
				t.Fatalf("NewTracker() error = %v", err)
			}
			tt.mark(tracker)

			if err := os.RemoveAll(dir); err != nil {
				t.Fatalf("RemoveAll(data dir) error = %v", err)
			}
			if err := os.WriteFile(dir, []byte("not a directory"), 0600); err != nil {
				t.Fatalf("WriteFile(data dir placeholder) error = %v", err)
			}

			err = tt.save(tracker)
			if err == nil {
				t.Fatal("expected save error")
			}
			want := filepath.Join(dir, tt.file+".tmp")
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("save error = %v, want temp path %q", err, want)
			}
		})
	}
}

func TestSaveRetryStatusLogsQueueAndDeadLetterCounts(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()
	previousLevel := log.GetLevel()
	log.SetLevel(log.DebugLevel)
	defer log.SetLevel(previousLevel)

	tracker := newTestTracker(t)
	tracker.enqueueRetryForTest(
		"retry-item",
		"https://discord.com/api/webhooks/123456789012345678/test-token",
		"discord",
		"api",
		"Retry Item",
		"temporary failure",
		3,
		time.Hour,
	)
	tracker.MarkRetryDeadLetter("dead-item", "discord", "api", "Dead Item", "terminal failure")

	if err := tracker.saveRetryStatus(); err != nil {
		t.Fatalf("saveRetryStatus() error = %v", err)
	}

	var saveEntry *log.Entry
	for _, entry := range hook.AllEntries() {
		if entry.Message == "Retry status saved to file" {
			saveEntry = entry
			break
		}
	}
	if saveEntry == nil {
		t.Fatal("missing retry status save log")
	}
	if got := saveEntry.Data["retry_queue_size"]; got != 1 {
		t.Fatalf("retry_queue_size = %#v, want 1", got)
	}
	if got := saveEntry.Data["dead_letter_count"]; got != 1 {
		t.Fatalf("dead_letter_count = %#v, want 1", got)
	}
}

func TestNewTrackerRemovesStaleStatusTempFiles(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"api_status.json.tmp", "rss_status.json.tmp", "retry_status.json.tmp"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("stale snapshot"), 0600); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", name, err)
		}
	}
	keepPath := filepath.Join(dir, "operator-note.tmp")
	if err := os.WriteFile(keepPath, []byte("keep"), 0600); err != nil {
		t.Fatalf("WriteFile(operator-note.tmp) error = %v", err)
	}

	if _, err := NewTracker(dir); err != nil {
		t.Fatalf("NewTracker() error = %v", err)
	}

	for _, name := range []string{"api_status.json.tmp", "rss_status.json.tmp", "retry_status.json.tmp"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("%s still exists after NewTracker cleanup, stat err = %v", name, err)
		}
	}
	if _, err := os.Stat(keepPath); err != nil {
		t.Fatalf("operator note temp file was removed or inaccessible: %v", err)
	}
}

func TestNewTrackerRejectsCorruptStatusFiles(t *testing.T) {
	tests := []struct {
		name string
		file string
	}{
		{name: "api", file: "api_status.json"},
		{name: "rss", file: "rss_status.json"},
		{name: "retry", file: "retry_status.json"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, tt.file), []byte("{not json"), 0600); err != nil {
				t.Fatalf("WriteFile(%s) error = %v", tt.file, err)
			}

			if _, err := NewTracker(dir); err == nil {
				t.Fatalf("NewTracker() succeeded with corrupt %s", tt.file)
			} else if !strings.Contains(err.Error(), filepath.Join(dir, tt.file)) {
				t.Fatalf("NewTracker() error = %v, want path %q", err, filepath.Join(dir, tt.file))
			}
		})
	}
}

func TestNewTrackerWithRetentionLazyAPIDefersAPIStatusLoad(t *testing.T) {
	dir := t.TempDir()
	apiStatusPath := filepath.Join(dir, statusAPIFileName)
	if err := os.WriteFile(apiStatusPath, []byte("{not json"), 0600); err != nil {
		t.Fatalf("WriteFile(api_status.json) error = %v", err)
	}

	tracker, err := NewTrackerWithRetentionLazyAPI(dir, DefaultRetentionPolicy())
	if err != nil {
		t.Fatalf("NewTrackerWithRetentionLazyAPI() error = %v", err)
	}
	if tracker.apiLoaded {
		t.Fatal("lazy API tracker loaded API status during construction")
	}
	if err := tracker.EnsureAPIStatusLoaded(); err == nil {
		t.Fatal("EnsureAPIStatusLoaded() error = nil, want corrupt api_status error")
	} else if !strings.Contains(err.Error(), apiStatusPath) {
		t.Fatalf("EnsureAPIStatusLoaded() error = %v, want path %q", err, apiStatusPath)
	}
}

func TestSavePendingChangesKeepsPersistedAPISentItemsWhenAPIStatusNotLoaded(t *testing.T) {
	dir := t.TempDir()

	seed, err := NewTrackerWithRetention(dir, DefaultRetentionPolicy())
	if err != nil {
		t.Fatalf("NewTrackerWithRetention() error = %v", err)
	}
	seed.MarkAPIItemSentToDestination("item-a", "Item A", "discord.ransomware")
	seed.MarkAPIItemSentToDestination("item-b", "Item B", "discord.ransomware")
	if err := seed.SavePendingChanges(); err != nil {
		t.Fatalf("SavePendingChanges(seed) error = %v", err)
	}

	lazy, err := NewTrackerWithRetentionLazyAPI(dir, DefaultRetentionPolicy())
	if err != nil {
		t.Fatalf("NewTrackerWithRetentionLazyAPI() error = %v", err)
	}
	if lazy.apiLoaded {
		t.Fatal("apiLoaded = true, want false for a lazy tracker")
	}
	lazy.UpdateAPIStatusWithErrorInfo(false, 0, "API key not configured", SourceErrorInfo{
		ErrorCategory: "configuration",
	})
	if err := lazy.SavePendingChanges(); err != nil {
		t.Fatalf("SavePendingChanges(lazy) error = %v", err)
	}
	if !lazy.apiLoaded {
		t.Fatal("apiLoaded = false, want the API mutation to have forced the api_status.json load")
	}

	data, err := os.ReadFile(filepath.Join(dir, statusAPIFileName))
	if err != nil {
		t.Fatalf("ReadFile(api_status.json) error = %v", err)
	}
	var payload struct {
		SentItems map[string]json.RawMessage `json:"sent_items"`
		LastError *string                    `json:"last_error"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("Unmarshal(api_status.json) error = %v", err)
	}
	if len(payload.SentItems) != 2 {
		t.Fatalf("sent_items = %d, want 2 (persisted API dedup state wiped): %s", len(payload.SentItems), data)
	}
	if payload.LastError == nil || !strings.Contains(*payload.LastError, "API key not configured") {
		t.Fatalf("last_error = %v, want the operator API-key error: %s", payload.LastError, data)
	}
}

func TestSavePendingChangesKeepsUnreadableAPIStatusFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, statusAPIFileName)
	if err := os.WriteFile(path, []byte("{not json"), 0600); err != nil {
		t.Fatalf("WriteFile(api_status.json) error = %v", err)
	}
	lazy, err := NewTrackerWithRetentionLazyAPI(dir, DefaultRetentionPolicy())
	if err != nil {
		t.Fatalf("NewTrackerWithRetentionLazyAPI() error = %v", err)
	}
	lazy.UpdateAPIStatusWithErrorInfo(false, 0, "boom", SourceErrorInfo{})
	if err := lazy.SavePendingChanges(); err != nil {
		t.Fatalf("SavePendingChanges() error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(api_status.json) error = %v", err)
	}
	if string(data) != "{not json" {
		t.Fatalf("unreadable api_status.json was overwritten: %s", data)
	}
	if !lazy.apiDirty {
		t.Fatal("apiDirty = false after a skipped API status save, want it kept so the pending state flushes once api_status.json loads")
	}
	// Second cycle: the warn latch must not resurrect the write.
	lazy.UpdateAPIStatusWithErrorInfo(false, 0, "boom-2", SourceErrorInfo{})
	if err := lazy.SavePendingChanges(); err != nil {
		t.Fatalf("SavePendingChanges(2) error = %v", err)
	}
	if data, err = os.ReadFile(path); err != nil || string(data) != "{not json" {
		t.Fatalf("unreadable api_status.json overwritten on the second save: %s (err %v)", data, err)
	}
}

// TestIsAPIItemSentToDestinationLoadsLazyAPIStatusBeforeReading pins the fix:
// IsAPIItemSentToDestination must force the
// lazy api_status.json load first, the same way markAPIItemSentUnderKey
// already does via ensureAPIStatusLoadedForMutation, so a read on an unloaded
// lazy tracker cannot answer "not sent" for an item that is on disk.
//
// This is a direct, white-box call on a freshly constructed lazy tracker, not
// a reproduction through checkAPIOnce: every production caller of
// IsAPIItemSentToDestination is preceded by scheduler.checkAPIOnce's own
// EnsureAPIStatusLoaded call (scheduler.go), so the gap this guards against is
// unreachable today. The test exists to pin the defensive parity itself, not
// to demonstrate a reachable production failure.
func TestIsAPIItemSentToDestinationLoadsLazyAPIStatusBeforeReading(t *testing.T) {
	dir := t.TempDir()

	seed, err := NewTrackerWithRetention(dir, DefaultRetentionPolicy())
	if err != nil {
		t.Fatalf("NewTrackerWithRetention() error = %v", err)
	}
	seed.MarkAPIItemSentToDestination("item-a", "Item A", "discord.ransomware")
	if err := seed.SavePendingChanges(); err != nil {
		t.Fatalf("SavePendingChanges(seed) error = %v", err)
	}

	lazy, err := NewTrackerWithRetentionLazyAPI(dir, DefaultRetentionPolicy())
	if err != nil {
		t.Fatalf("NewTrackerWithRetentionLazyAPI() error = %v", err)
	}
	if lazy.apiLoaded {
		t.Fatal("apiLoaded = true, want false for a lazy tracker before any read")
	}

	if sent := lazy.IsAPIItemSentToDestination("item-a", "discord.ransomware"); !sent {
		t.Fatal("IsAPIItemSentToDestination() = false, want true (on-disk marker missed because the lazy tracker was never loaded)")
	}
	if !lazy.apiLoaded {
		t.Fatal("apiLoaded = false, want the read to have forced the api_status.json load")
	}
}

// TestLegacyAPIFetchedItemPayloadsLoadsLazyAPIStatusBeforeReading pins the
// matching fix: LegacyAPIFetchedItemPayloads must
// force the lazy api_status.json load the same way its paired mutator
// ClearLegacyAPIFetchedItems already does, so the read and the clear agree
// about what is on disk.
//
// Direct, white-box call for the same reason as above: the only production
// caller (migrateLegacyAPIFetchedItemsForConfig) runs inside checkAPIOnce
// after its own EnsureAPIStatusLoaded call, so this path is unreachable in
// production; the test pins the defensive parity itself.
func TestLegacyAPIFetchedItemPayloadsLoadsLazyAPIStatusBeforeReading(t *testing.T) {
	dir := t.TempDir()
	apiJSON := `{"last_updated":"2026-01-01T00:00:00Z","sent_items":null,` +
		`"fetched_items":[{"id":"legacy-1"},{"id":"legacy-2"}]}`
	if err := os.WriteFile(filepath.Join(dir, statusAPIFileName), []byte(apiJSON), 0600); err != nil {
		t.Fatalf("WriteFile(api status) error = %v", err)
	}

	lazy, err := NewTrackerWithRetentionLazyAPI(dir, DefaultRetentionPolicy())
	if err != nil {
		t.Fatalf("NewTrackerWithRetentionLazyAPI() error = %v", err)
	}
	if lazy.apiLoaded {
		t.Fatal("apiLoaded = true, want false for a lazy tracker before any read")
	}

	payloads := lazy.LegacyAPIFetchedItemPayloads()
	if len(payloads) != 2 {
		t.Fatalf("LegacyAPIFetchedItemPayloads() = %d payloads, want 2 (on-disk legacy items missed because the lazy tracker was never loaded)", len(payloads))
	}
	if !lazy.apiLoaded {
		t.Fatal("apiLoaded = false, want the read to have forced the api_status.json load")
	}
}

func TestLoadRetryStatusPrunesDeadLettersByRetention(t *testing.T) {
	dir := t.TempDir()
	policy := DefaultRetentionPolicy()
	policy.MaxDeadLetterItems = 2
	policy.DeadLetterMaxAge = 24 * time.Hour

	baseTime := statusNow()
	data := []byte(fmt.Sprintf(`{
  "last_updated": %q,
  "retry_queue": {},
  "dead_letter_items": [
    {"item_key": "aged-dead", "item_type": "api", "messenger": "discord", "title": "Aged", "dead_at": %q},
    {"item_key": "recent-1", "item_type": "api", "messenger": "discord", "title": "Recent 1", "dead_at": %q},
    {"item_key": "recent-2", "item_type": "api", "messenger": "discord", "title": "Recent 2", "dead_at": %q},
    {"item_key": "recent-3", "item_type": "api", "messenger": "discord", "title": "Recent 3", "dead_at": %q}
  ]
}`,
		baseTime.Format(time.RFC3339Nano),
		formatStatusTimestamp(baseTime.Add(-48*time.Hour)),
		formatStatusTimestamp(baseTime.Add(-3*time.Hour)),
		formatStatusTimestamp(baseTime.Add(-2*time.Hour)),
		formatStatusTimestamp(baseTime.Add(-1*time.Hour)),
	))
	retryStatusPath := filepath.Join(dir, statusRetryFileName)
	if err := os.WriteFile(retryStatusPath, append(data, '\n'), 0600); err != nil {
		t.Fatalf("WriteFile(retry_status.json) error = %v", err)
	}

	tracker, err := NewTrackerWithRetention(dir, policy)
	if err != nil {
		t.Fatalf("NewTrackerWithRetention() error = %v", err)
	}

	deadLetters := tracker.GetDeadLetterItems()
	if len(deadLetters) != 2 {
		t.Fatalf("dead letter count after load = %d, want 2", len(deadLetters))
	}
	if deadLetters[0].ItemKey != "recent-2" || deadLetters[1].ItemKey != "recent-3" {
		t.Fatalf("dead letters after load = %#v, want recent-2/recent-3", deadLetters)
	}
	if !tracker.DirtyState().Retry {
		t.Fatal("retry load retention prune should mark retry status dirty")
	}

	if err := tracker.SavePendingChanges(); err != nil {
		t.Fatalf("SavePendingChanges() error = %v", err)
	}
	expectFileNotContains(t, retryStatusPath, "aged-dead")
	expectFileNotContains(t, retryStatusPath, "recent-1")
	expectFileContains(t, retryStatusPath, "recent-2")
	expectFileContains(t, retryStatusPath, "recent-3")
}

func TestAPIAndRSSStatusPersistAndReload(t *testing.T) {
	dir := t.TempDir()
	webhookURL := "https://discord.com/api/webhooks/123456789012345678/test-token"
	feedURL := "https://example.test/feed.xml"

	tracker, err := NewTracker(dir)
	if err != nil {
		t.Fatalf("NewTracker() error = %v", err)
	}
	tracker.MarkAPIItemSentToWebhook("api-item", "API Item", webhookURL)
	tracker.MarkRSSItemParsed(feedURL, "rss-item", StoredRSSEntry{
		Title:     "RSS Item",
		Link:      "https://example.test/rss-item",
		Published: "2026-01-01 00:00:00",
	})
	tracker.MarkRSSItemSentToWebhook("rss-item", "RSS Item", "Example Feed", webhookURL)
	if err := tracker.SavePendingChanges(); err != nil {
		t.Fatalf("SavePendingChanges: %v", err)
	}

	reloaded, err := NewTracker(dir)
	if err != nil {
		t.Fatalf("NewTracker(reload) error = %v", err)
	}

	if !reloaded.IsAPIItemSentToWebhook("api-item", webhookURL) {
		t.Fatal("reloaded tracker did not preserve API sent marker")
	}
	if !reloaded.IsRSSItemParsed(feedURL, "rss-item") {
		t.Fatal("reloaded tracker did not preserve RSS parsed marker")
	}
	if !reloaded.IsRSSItemSentToWebhook("rss-item", webhookURL) {
		t.Fatal("reloaded tracker did not preserve RSS sent marker")
	}
	if unsent := reloaded.GetUnsentRSSItemsForWebhook(webhookURL); len(unsent) != 0 {
		t.Fatalf("reloaded tracker returned %d unsent RSS items, want 0", len(unsent))
	}
}

func TestMinimizeRSSParsedItemRemovesContentBeforePersistence(t *testing.T) {
	tracker, dir := newTestTrackerWithDir(t)

	feedURL := "https://example.test/feed.xml"
	itemKey := "rss:v2:title:abc123"
	tracker.MarkRSSItemParsed(feedURL, itemKey, StoredRSSEntry{
		Title:       "Filtered Victim Story",
		Link:        "https://example.test/filtered-victim-story",
		Description: "Sensitive description",
		Author:      "Reporter",
		Categories:  []string{"out-of-scope"},
		GUID:        "guid-filtered",
		FeedTitle:   "Example Feed",
		Published:   "2026-01-01 00:00:00",
	})

	if !tracker.MinimizeRSSParsedItem(feedURL, itemKey) {
		t.Fatal("MinimizeRSSParsedItem() returned false")
	}
	if err := tracker.SavePendingChanges(); err != nil {
		t.Fatalf("SavePendingChanges: %v", err)
	}

	rssStatusPath := filepath.Join(dir, "rss_status.json")
	expectFileContains(t, rssStatusPath, itemKey)
	for _, forbidden := range []string{
		"Filtered Victim Story",
		"filtered-victim-story",
		"Sensitive description",
		"Reporter",
		"out-of-scope",
		"guid-filtered",
		"Example Feed",
	} {
		expectFileNotContains(t, rssStatusPath, forbidden)
	}
}

func TestMarkRSSItemParsedStoresOffsetAwareParsedAt(t *testing.T) {
	tracker := newTestTracker(t)

	tracker.MarkRSSItemParsed("https://example.test/feed.xml", "rss-item", StoredRSSEntry{
		Title:     "RSS Item",
		Published: time.Now().UTC().Format(time.RFC3339Nano),
	})

	parsedItems := tracker.RSSParsedItemsSnapshot()
	if len(parsedItems) != 1 {
		t.Fatalf("parsed item count = %d, want 1", len(parsedItems))
	}
	parsedAt := parsedItems[0].ParsedAt
	if _, err := time.Parse(time.RFC3339Nano, parsedAt); err != nil {
		t.Fatalf("ParsedAt = %q, want RFC3339Nano timestamp: %v", parsedAt, err)
	}
}

func TestMarkRSSItemsParsedBatchesEntriesAndIndexesFeedKeys(t *testing.T) {
	tracker := newTestTracker(t)
	feedURL := "https://example.test/feed.xml"

	tracker.MarkRSSItemsParsed(feedURL, map[string]StoredRSSEntry{
		"rss-item-1": {
			Title:     "First RSS Item",
			Published: "2026-01-02T03:04:05Z",
		},
		"rss-item-2": {
			Title:     "Second RSS Item",
			Published: "2026-01-02T04:04:05Z",
		},
	})

	keys := tracker.GetRSSParsedItemKeys(feedURL)
	if len(keys) != 2 {
		t.Fatalf("parsed item keys = %d, want 2: %#v", len(keys), keys)
	}
	for _, key := range []string{"rss-item-1", "rss-item-2"} {
		if _, ok := keys[key]; !ok {
			t.Fatalf("parsed item keys missing %q: %#v", key, keys)
		}
		if !tracker.IsRSSItemParsed(feedURL, key) {
			t.Fatalf("IsRSSItemParsed(%q) = false, want true", key)
		}
	}

	tracker.MarkRSSItemsParsed(feedURL, map[string]StoredRSSEntry{
		"rss-item-2": {
			Title:     "Updated Second RSS Item",
			Published: "2026-01-02T05:04:05Z",
		},
	})

	keys = tracker.GetRSSParsedItemKeys(feedURL)
	if len(keys) != 2 {
		t.Fatalf("parsed item keys after update = %d, want 2: %#v", len(keys), keys)
	}
	items := tracker.GetUnsentRSSItemsForDestination("discord.rss.general")
	if len(items) != 2 {
		t.Fatalf("parsed item count after update = %d, want 2", len(items))
	}
	var updatedTitle string
	for _, entry := range items {
		if entry.Key == "rss-item-2" {
			updatedTitle = entry.Title
			break
		}
	}
	if updatedTitle != "Updated Second RSS Item" {
		t.Fatalf("updated title = %q, want Updated Second RSS Item", updatedTitle)
	}
}

func TestLoadRSSStatusDeduplicatesParsedItemsByFeedAndKey(t *testing.T) {
	dir := t.TempDir()
	rssStatus := `{
  "last_updated": "2026-01-01T00:00:00Z",
  "feeds": {},
  "parsed_items": [
    {
      "key": "same-key",
      "feed_url": "https://example.test/feed.xml",
      "title": "Old Title",
      "published": "2026-01-01T00:00:00Z",
      "parsed_at": "2026-01-01T00:00:00Z"
    },
    {
      "key": "same-key",
      "feed_url": "https://example.test/feed.xml",
      "title": "New Title",
      "published": "2026-01-01T00:00:00Z",
      "parsed_at": "2026-01-01T01:00:00Z"
    }
  ],
  "sent_items": {}
}`
	if err := os.WriteFile(filepath.Join(dir, "rss_status.json"), []byte(rssStatus), 0600); err != nil {
		t.Fatalf("WriteFile(rss_status.json) error = %v", err)
	}

	tracker, err := NewTracker(dir)
	if err != nil {
		t.Fatalf("NewTracker() error = %v", err)
	}

	unsent := tracker.GetUnsentRSSItemsForDestination("discord.rss.general")
	if len(unsent) != 1 {
		t.Fatalf("unsent RSS items = %d, want deduped 1", len(unsent))
	}
	if got := unsent[0].Title; got != "New Title" {
		t.Fatalf("deduped title = %q, want newest entry", got)
	}
	if !tracker.DirtyState().RSS {
		t.Fatal("deduped RSS load should mark status dirty for persistence")
	}
}

func TestLoadRSSStatusDefersParsedItemSortUntilOrderedRead(t *testing.T) {
	dir := t.TempDir()
	rssStatus := `{
  "last_updated": "2026-01-01T00:00:00Z",
  "feeds": {},
  "parsed_items": [
    {
      "key": "newer",
      "feed_url": "https://example.test/feed.xml",
      "title": "Newer",
      "published": "2026-01-01T02:00:00Z",
      "parsed_at": "2026-01-01T02:00:00Z"
    },
    {
      "key": "older",
      "feed_url": "https://example.test/feed.xml",
      "title": "Older",
      "published": "2026-01-01T01:00:00Z",
      "parsed_at": "2026-01-01T01:00:00Z"
    }
  ],
  "sent_items": {}
}`
	if err := os.WriteFile(filepath.Join(dir, "rss_status.json"), []byte(rssStatus), 0600); err != nil {
		t.Fatalf("WriteFile(rss_status.json) error = %v", err)
	}

	tracker, err := NewTracker(dir)
	if err != nil {
		t.Fatalf("NewTracker() error = %v", err)
	}

	sortState := tracker.RSSParsedSortState()
	if !sortState.SortDirty || !sortState.FullSortDirty {
		t.Fatal("RSS load should defer parsed-item sorting")
	}
	if !tracker.IsRSSItemParsed("https://example.test/feed.xml", "older") {
		t.Fatal("parsed index was not available before deferred sort")
	}

	unsent := tracker.GetUnsentRSSItemsForDestination("discord.rss.general")
	if len(unsent) != 2 {
		t.Fatalf("unsent count = %d, want 2", len(unsent))
	}
	if unsent[0].Key != "older" || unsent[1].Key != "newer" {
		t.Fatalf("unsent order = %q/%q, want older/newer", unsent[0].Key, unsent[1].Key)
	}
	sortState = tracker.RSSParsedSortState()
	if sortState.SortDirty || sortState.FullSortDirty {
		t.Fatal("ordered RSS read should complete deferred sort")
	}
}

func TestLoadRSSStatusMigratesLegacyProcessedItems(t *testing.T) {
	dir := t.TempDir()
	feedURL := "https://example.test/feed.xml"
	rssStatus := `{
  "last_updated": "2026-01-01T00:00:00Z",
  "feeds": {
    "https://example.test/feed.xml": {
      "last_check": "2026-01-02T03:04:05Z",
      "entries_found": 1,
      "processed_items": {
        "legacy-key": "Legacy RSS Title",
        "": "ignored empty key"
      }
    }
  },
  "sent_items": {}
}`
	rssStatusPath := filepath.Join(dir, "rss_status.json")
	if err := os.WriteFile(rssStatusPath, []byte(rssStatus), 0600); err != nil {
		t.Fatalf("WriteFile(rss_status.json) error = %v", err)
	}

	tracker, err := NewTracker(dir)
	if err != nil {
		t.Fatalf("NewTracker() error = %v", err)
	}

	if !tracker.IsRSSItemParsed(feedURL, "legacy-key") {
		t.Fatal("legacy processed RSS key was not migrated into parsed item lookup")
	}
	keys := tracker.GetRSSParsedItemKeys(feedURL)
	if len(keys) != 1 {
		t.Fatalf("parsed keys = %d, want 1: %#v", len(keys), keys)
	}
	unsent := tracker.GetUnsentRSSItemsForDestination("discord.rss.general")
	if len(unsent) != 1 {
		t.Fatalf("unsent RSS items = %d, want migrated item", len(unsent))
	}
	if unsent[0].Title != "Legacy RSS Title" {
		t.Fatalf("migrated title = %q, want Legacy RSS Title", unsent[0].Title)
	}
	if !tracker.DirtyState().RSS {
		t.Fatal("legacy RSS processed-item migration should mark status dirty")
	}

	if err := tracker.SavePendingChanges(); err != nil {
		t.Fatalf("SavePendingChanges() error = %v", err)
	}
	expectFileContains(t, rssStatusPath, `"parsed_items"`)
	expectFileNotContains(t, rssStatusPath, `"processed_items"`)
}

func TestLoadRSSStatusBackfillsContentSignatureSentItems(t *testing.T) {
	dir := t.TempDir()
	rssStatusPath := filepath.Join(dir, "rss_status.json")
	destinationID := "slack.rss.general"
	feedURL := "https://example.test/feed.xml"
	published := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	original := rss.Entry{
		Title:       "Ransomware Activity Report",
		Link:        "https://example.test/reports/ransomware-activity",
		Description: "The same report body",
		Published:   published,
		GUID:        "guid-original",
		FeedTitle:   "Example Feed",
		FeedURL:     feedURL,
	}
	primaryKey := rss.GenerateEntryKeyForEntry(original)
	if primaryKey == "" {
		t.Fatal("test RSS entry did not produce a primary key")
	}

	legacyStatus := map[string]interface{}{
		"last_updated": published.Format(time.RFC3339Nano),
		"feeds":        map[string]interface{}{},
		"parsed_items": []StoredRSSEntry{
			{
				Key:         primaryKey,
				FeedURL:     feedURL,
				Title:       original.Title,
				Link:        original.Link,
				Description: original.Description,
				Published:   published.Format(time.RFC3339Nano),
				GUID:        original.GUID,
				FeedTitle:   original.FeedTitle,
				ParsedAt:    published.Format(time.RFC3339Nano),
			},
		},
		"sent_items": map[string]map[string]interface{}{
			"primary-sent-marker": {
				"title":          original.Title,
				"feed_title":     original.FeedTitle,
				"sent_at":        published.Add(time.Minute).Format(time.RFC3339Nano),
				"destination_id": destinationID,
				"item_key":       primaryKey,
			},
		},
	}
	data, err := json.MarshalIndent(legacyStatus, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	if err := os.WriteFile(rssStatusPath, append(data, '\n'), 0600); err != nil {
		t.Fatalf("WriteFile(rss_status.json) error = %v", err)
	}

	tracker, err := NewTracker(dir)
	if err != nil {
		t.Fatalf("NewTracker() error = %v", err)
	}

	variant := original
	variant.Link = "https://example.test/reports/ransomware-activity?utm_source=mirror"
	variant.GUID = "guid-republished"
	if variantKey := rss.GenerateEntryKeyForEntry(variant); variantKey == "" || variantKey == primaryKey {
		t.Fatalf("variant primary key = %q, want a different non-empty key from %q", variantKey, primaryKey)
	}
	contentSignature := rss.GenerateEntryContentSignature(variant)
	if contentSignature != rss.GenerateEntryContentSignature(original) {
		t.Fatal("test RSS variant did not share the original content signature")
	}
	if !tracker.IsRSSItemSentToDestination(contentSignature, destinationID) {
		t.Fatal("content-signature sent marker was not backfilled during RSS status load")
	}
	if !tracker.DirtyState().RSS {
		t.Fatal("RSS content-signature sent backfill should mark status dirty")
	}

	if err := tracker.SavePendingChanges(); err != nil {
		t.Fatalf("SavePendingChanges() error = %v", err)
	}
	expectFileContains(t, rssStatusPath, `"item_key": "content-sig:v3:`)
}

func TestLoadRSSStatusLogOmitsGlobalUnsentItems(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()
	previousLevel := log.GetLevel()
	log.SetLevel(log.InfoLevel)
	defer log.SetLevel(previousLevel)

	dir := t.TempDir()
	rssStatus := `{
  "last_updated": "2026-01-01T00:00:00Z",
  "feeds": {},
  "parsed_items": [
    {
      "key": "rss-item",
      "feed_url": "https://example.test/feed.xml",
      "title": "RSS Item",
      "published": "2026-01-01T00:00:00Z",
      "parsed_at": "2026-01-01T00:00:00Z"
    }
  ],
  "sent_items": {
    "discord-sent": {
      "title": "RSS Item",
      "feed_title": "Example Feed",
      "sent_at": "2026-01-01T00:01:00Z",
      "item_key": "rss-item"
    }
  }
}`
	if err := os.WriteFile(filepath.Join(dir, "rss_status.json"), []byte(rssStatus), 0600); err != nil {
		t.Fatalf("WriteFile(rss_status.json) error = %v", err)
	}

	if _, err := NewTracker(dir); err != nil {
		t.Fatalf("NewTracker() error = %v", err)
	}

	var loadEntry *log.Entry
	for _, entry := range hook.AllEntries() {
		if entry.Message == "RSS status loaded from file" {
			loadEntry = entry
			break
		}
	}
	if loadEntry == nil {
		t.Fatal("missing RSS status load log")
	}
	if _, ok := loadEntry.Data["unsent_items"]; ok {
		t.Fatalf("RSS load log still contains global unsent_items: %#v", loadEntry.Data)
	}
	if got := loadEntry.Data["parsed_items"]; got != 1 {
		t.Fatalf("parsed_items = %#v, want 1; fields=%#v", got, loadEntry.Data)
	}
	if got := loadEntry.Data["sent_items"]; got != 1 {
		t.Fatalf("sent_items = %#v, want 1; fields=%#v", got, loadEntry.Data)
	}
}

func TestStatusWritesUseUTCOffsetAwareTimestamps(t *testing.T) {
	tracker := newTestTracker(t)

	tracker.UpdateAPIStatus(true, 1, "")
	tracker.MarkAPIItemSentToDestination("api-item", "API Item", "discord.ransomware")
	tracker.MarkRSSItemSentToDestination("rss-item", "RSS Item", "Feed", "discord.rss")
	if !tracker.enqueueRetryForTest("retry-item", "discord.ransomware", "discord", "api", "Retry Item", "failed", 3, time.Hour) {
		t.Fatal("EnqueueRetryForDestination() returned false")
	}
	tracker.MarkRetryDeadLetter("dead-item", "discord", "api", "Dead Item", "terminal")

	apiStatus := tracker.APIStatusSnapshot()
	retryStatus := tracker.RetryStatusSnapshot()
	apiSent, ok := tracker.APISentItemSnapshot("api-item", "discord.ransomware")
	if !ok {
		t.Fatal("API sent item snapshot missing api-item/discord.ransomware")
	}
	rssSent, ok := tracker.RSSSentItemSnapshot("rss-item", "discord.rss")
	if !ok {
		t.Fatal("RSS sent item snapshot missing rss-item/discord.rss")
	}
	timestamps := []struct {
		name  string
		value time.Time
	}{
		{"api last_updated", apiStatus.LastUpdated},
		{"api last_check", apiStatus.LastCheck},
		{"api last_success", *apiStatus.LastSuccess},
		{"api sent_at", apiSent.SentAt},
		{"rss sent_at", rssSent.SentAt},
		{"retry last_updated", retryStatus.LastUpdated},
	}
	for _, ts := range timestamps {
		if ts.value.Location() != time.UTC {
			t.Fatalf("%s location = %v, want UTC", ts.name, ts.value.Location())
		}
		if _, err := time.Parse(time.RFC3339Nano, ts.value.Format(time.RFC3339Nano)); err != nil {
			t.Fatalf("%s = %q, want RFC3339Nano-compatible timestamp: %v", ts.name, ts.value.Format(time.RFC3339Nano), err)
		}
	}

	retryItems := tracker.GetRetryItemsByType("api")
	if len(retryItems) != 1 {
		t.Fatalf("retry queue length = %d, want 1", len(retryItems))
	}
	retry := retryItems[0]
	deadLetters := tracker.GetDeadLetterItems()
	if len(deadLetters) != 1 {
		t.Fatalf("dead letter count = %d, want 1", len(deadLetters))
	}
	dead := deadLetters[0]
	for name, value := range map[string]string{
		"retry first_failed": retry.FirstFailed,
		"retry last_retried": retry.LastRetried,
		"dead first_failed":  dead.FirstFailed,
		"dead dead_at":       dead.DeadAt,
	} {
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			t.Fatalf("%s = %q, want RFC3339Nano timestamp: %v", name, value, err)
		}
		if parsed.Location() != time.UTC {
			t.Fatalf("%s location = %v, want UTC", name, parsed.Location())
		}
	}
}

func TestSetRSSParsedItemFeedTypePersistsAndSurvivesMinimize(t *testing.T) {
	tracker, dir := newTestTrackerWithDir(t)

	feedURL := "https://example.test/feed.xml"
	itemKey := "rss-item"
	tracker.MarkRSSItemParsed(feedURL, itemKey, StoredRSSEntry{
		Title:     "RSS Item",
		Published: "2026-01-01T00:00:00Z",
	})

	if !tracker.SetRSSParsedItemFeedType(feedURL, itemKey, "government") {
		t.Fatal("SetRSSParsedItemFeedType() returned false")
	}
	if !tracker.MinimizeRSSParsedItem(feedURL, itemKey) {
		t.Fatal("MinimizeRSSParsedItem() returned false")
	}
	if err := tracker.SavePendingChanges(); err != nil {
		t.Fatalf("SavePendingChanges: %v", err)
	}

	reloaded, err := NewTracker(dir)
	if err != nil {
		t.Fatalf("NewTracker(reload) error = %v", err)
	}
	unsent := reloaded.GetUnsentRSSItemsForDestination("slack.rss.government")
	if len(unsent) != 1 {
		t.Fatalf("GetUnsentRSSItemsForDestination() returned %d items, want 1", len(unsent))
	}
	if unsent[0].FeedType != "government" {
		t.Fatalf("FeedType = %q, want government", unsent[0].FeedType)
	}
}

func TestMarkRSSItemSkippedForWebhookOmitsTitles(t *testing.T) {
	tracker, dir := newTestTrackerWithDir(t)

	webhookURL := "https://discord.com/api/webhooks/123456789012345678/test-token"
	itemKey := "rss:v2:title:abc123"
	tracker.MarkRSSItemSkippedForWebhook(itemKey, webhookURL)
	if err := tracker.SavePendingChanges(); err != nil {
		t.Fatalf("SavePendingChanges: %v", err)
	}

	rssStatusPath := filepath.Join(dir, "rss_status.json")
	expectFileContains(t, rssStatusPath, itemKey)
	for _, forbidden := range []string{`"title"`, `"feed_title"`, webhookURL, "test-token"} {
		expectFileNotContains(t, rssStatusPath, forbidden)
	}
}

func TestMarkAPIItemSentMinimizesPersistedStatus(t *testing.T) {
	dir := t.TempDir()
	webhookURL := "https://discord.com/api/webhooks/123456789012345678/test-token"
	rawItemKey := "lockbit|Example Corp|DE|2026-01-01"
	title := "lockbit -> Example Corp"

	tracker, err := NewTracker(dir)
	if err != nil {
		t.Fatalf("NewTracker() error = %v", err)
	}
	tracker.MarkAPIItemSentToWebhook(rawItemKey, title, webhookURL)
	if err := tracker.SavePendingChanges(); err != nil {
		t.Fatalf("SavePendingChanges: %v", err)
	}

	apiStatusPath := filepath.Join(dir, "api_status.json")
	expectFileContains(t, apiStatusPath, `"sent_at"`)
	for _, forbidden := range []string{rawItemKey, title, "Example Corp", webhookURL, "test-token", `"item_key"`, `"title"`} {
		expectFileNotContains(t, apiStatusPath, forbidden)
	}

	reloaded, err := NewTracker(dir)
	if err != nil {
		t.Fatalf("NewTracker(reload) error = %v", err)
	}
	if !reloaded.IsAPIItemSentToWebhook(rawItemKey, webhookURL) {
		t.Fatal("reloaded tracker did not preserve minimized API sent marker")
	}
}

func TestPruneRSSFeedStatusRemovesOnlyInactiveFeedHealthRecords(t *testing.T) {
	dir := t.TempDir()
	activeFeedURL := "https://active.example.test/feed.xml"
	removedFeedURL := "https://removed.example.test/feed.xml"

	tracker, err := NewTracker(dir)
	if err != nil {
		t.Fatalf("NewTracker() error = %v", err)
	}

	tracker.UpdateFeedStatus(activeFeedURL, true, 2, "")
	tracker.UpdateFeedStatus(removedFeedURL, false, 0, "gone")
	tracker.MarkRSSItemParsed(removedFeedURL, "removed-feed-item", StoredRSSEntry{
		Key:       "removed-feed-item",
		Title:     "Removed Feed Item",
		Published: "2026-01-01 00:00:00",
	})

	if removed := tracker.PruneRSSFeedStatus([]string{activeFeedURL}); removed != 1 {
		t.Fatalf("PruneRSSFeedStatus() removed %d feeds, want 1", removed)
	}
	if _, ok := tracker.GetRSSFeedInfo(activeFeedURL); !ok {
		t.Fatal("active feed status was pruned")
	}
	if _, ok := tracker.GetRSSFeedInfo(removedFeedURL); ok {
		t.Fatal("removed feed status still exists")
	}
	if !tracker.IsRSSItemParsed(removedFeedURL, "removed-feed-item") {
		t.Fatal("prune removed parsed RSS item; only feed health records should be pruned")
	}

	if err := tracker.SavePendingChanges(); err != nil {
		t.Fatalf("SavePendingChanges: %v", err)
	}

	reloaded, err := NewTracker(dir)
	if err != nil {
		t.Fatalf("NewTracker(reload) error = %v", err)
	}
	if _, ok := reloaded.GetRSSFeedInfo(removedFeedURL); ok {
		t.Fatal("removed feed status persisted after prune")
	}
	if _, ok := reloaded.GetRSSFeedInfo(activeFeedURL); !ok {
		t.Fatal("active feed status missing after reload")
	}
	if !reloaded.IsRSSItemParsed(removedFeedURL, "removed-feed-item") {
		t.Fatal("parsed RSS item missing after reload")
	}
}

func TestGetUnsentRSSItemsForWebhookReturnsChronologicalOrder(t *testing.T) {
	tracker := newTestTracker(t)

	feedURL := "https://example.test/feed.xml"
	webhookURL := "https://discord.com/api/webhooks/123456789012345678/test-token"
	newer := StoredRSSEntry{
		Key:       "newer",
		FeedURL:   feedURL,
		Title:     "Newer",
		Published: "2026-01-02 00:00:00",
	}
	older := StoredRSSEntry{
		Key:       "older",
		FeedURL:   feedURL,
		Title:     "Older",
		Published: "2026-01-01 00:00:00",
	}

	tracker.MarkRSSItemParsed(feedURL, newer.Key, newer)
	tracker.MarkRSSItemParsed(feedURL, older.Key, older)

	unsent := tracker.GetUnsentRSSItemsForWebhook(webhookURL)
	if len(unsent) != 2 {
		t.Fatalf("GetUnsentRSSItemsForWebhook() returned %d items, want 2", len(unsent))
	}
	if unsent[0].Key != older.Key || unsent[1].Key != newer.Key {
		t.Fatalf("unsent order = [%s, %s], want [%s, %s]", unsent[0].Key, unsent[1].Key, older.Key, newer.Key)
	}
}

func TestGetUnsentRSSItemsForDestinationFeedTypeScopesParsedItemsAndRetainsUnmappedLegacy(t *testing.T) {
	tracker := NewMemoryTracker()

	destinationID := "slack.rss.general"
	generalFeedURL := "https://example.test/general.xml"
	govFeedURL := "https://example.test/government.xml"
	removedGeneralFeedURL := "https://example.test/removed-general.xml"
	unknownFeedURL := "https://example.test/unknown.xml"

	entries := []StoredRSSEntry{
		{
			Key:       "persisted-general",
			FeedURL:   removedGeneralFeedURL,
			FeedType:  "general",
			Title:     "Persisted General",
			Published: "2026-01-01 00:00:00",
		},
		{
			Key:       "legacy-general",
			FeedURL:   generalFeedURL,
			Title:     "Legacy General",
			Published: "2026-01-02 00:00:00",
		},
		{
			Key:       "foreign-type",
			FeedURL:   generalFeedURL,
			FeedType:  "government",
			Title:     "Foreign Type",
			Published: "2026-01-03 00:00:00",
		},
		{
			Key:       "unknown-feed",
			FeedURL:   unknownFeedURL,
			Title:     "Unknown Feed",
			Published: "2026-01-04 00:00:00",
		},
		{
			Key:       "sent-general",
			FeedURL:   govFeedURL,
			FeedType:  "general",
			Title:     "Already Sent",
			Published: "2026-01-05 00:00:00",
		},
	}
	for _, entry := range entries {
		tracker.MarkRSSItemParsed(entry.FeedURL, entry.Key, entry)
	}
	tracker.MarkRSSItemSentToDestination("sent-general", "Already Sent", "General Feed", destinationID)

	unsent := tracker.GetUnsentRSSItemsForDestinationFeedType(
		destinationID,
		"general",
		map[string]struct{}{generalFeedURL: {}},
	)

	got := rssItemKeys(unsent)
	want := []string{"persisted-general", "legacy-general", "unknown-feed"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("feed-scoped unsent keys = %v, want %v", got, want)
	}
}

func TestGetUnsentRSSRecoveryItemsForDestinationFeedTypeReturnsRecoveryView(t *testing.T) {
	tracker := NewMemoryTracker()
	feedURL := "https://example.test/feed.xml"
	destinationID := "discord.rss.general"
	published := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	tracker.MarkRSSItemParsed(feedURL, "rss-item", StoredRSSEntry{
		Key:        "rss-item",
		FeedURL:    feedURL,
		FeedType:   "general",
		Title:      "Recovery Item",
		Published:  published.Format(time.RFC3339Nano),
		Categories: []string{"security"},
	})

	items := tracker.GetUnsentRSSRecoveryItemsForDestinationFeedType(
		destinationID,
		"general",
		map[string]struct{}{feedURL: {}},
	)

	if len(items) != 1 {
		t.Fatalf("recovery item count = %d, want 1", len(items))
	}
	if items[0].Key != "rss-item" || items[0].Title != "Recovery Item" {
		t.Fatalf("recovery item = %+v, want key/title view", items[0])
	}
	if !items[0].Published.Equal(published) {
		t.Fatalf("Published = %v, want %v", items[0].Published, published)
	}
	if items[0].InvalidReason != "" {
		t.Fatalf("InvalidReason = %q, want empty", items[0].InvalidReason)
	}
}

func TestSavePendingChangesMergesRSSParsedDirtyTail(t *testing.T) {
	tracker := NewMemoryTracker()

	feedURL := "https://example.test/feed.xml"
	destinationID := "discord.rss.general"
	tracker.MarkRSSItemParsed(feedURL, "middle", StoredRSSEntry{
		Title:     "Middle",
		Published: "2026-01-02 00:00:00",
	})
	tracker.MarkRSSItemParsed(feedURL, "latest", StoredRSSEntry{
		Title:     "Latest",
		Published: "2026-01-03 00:00:00",
	})
	if err := tracker.SavePendingChanges(); err != nil {
		t.Fatalf("SavePendingChanges() initial sort error = %v", err)
	}

	tracker.MarkRSSItemParsed(feedURL, "oldest", StoredRSSEntry{
		Title:     "Oldest",
		Published: "2026-01-01 00:00:00",
	})
	sortState := tracker.RSSParsedSortState()
	if sortState.FullSortDirty {
		t.Fatal("append-only RSS batch requested full sort")
	}
	if sortState.DirtyStart != 2 {
		t.Fatalf("RSS parsed dirty start = %d, want 2", sortState.DirtyStart)
	}
	if err := tracker.SavePendingChanges(); err != nil {
		t.Fatalf("SavePendingChanges() tail merge error = %v", err)
	}

	unsent := tracker.GetUnsentRSSItemsForDestination(destinationID)
	got := rssItemKeys(unsent)
	want := []string{"oldest", "middle", "latest"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("unsent order = %v, want %v", got, want)
	}
}

func TestMarkRSSItemParsedUpdateSortKeyUsesFullSortFallback(t *testing.T) {
	tracker := NewMemoryTracker()

	feedURL := "https://example.test/feed.xml"
	destinationID := "discord.rss.general"
	tracker.MarkRSSItemParsed(feedURL, "middle", StoredRSSEntry{
		Title:     "Middle",
		Published: "2026-01-02 00:00:00",
	})
	tracker.MarkRSSItemParsed(feedURL, "latest", StoredRSSEntry{
		Title:     "Latest",
		Published: "2026-01-03 00:00:00",
	})
	if err := tracker.SavePendingChanges(); err != nil {
		t.Fatalf("SavePendingChanges() initial sort error = %v", err)
	}

	tracker.MarkRSSItemParsed(feedURL, "latest", StoredRSSEntry{
		Title:     "Latest moved earlier",
		Published: "2026-01-01 00:00:00",
	})
	if !tracker.RSSParsedSortState().FullSortDirty {
		t.Fatal("existing RSS item sort-key update did not request full sort fallback")
	}
	if err := tracker.SavePendingChanges(); err != nil {
		t.Fatalf("SavePendingChanges() full sort fallback error = %v", err)
	}

	unsent := tracker.GetUnsentRSSItemsForDestination(destinationID)
	got := rssItemKeys(unsent)
	want := []string{"latest", "middle"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("unsent order = %v, want %v", got, want)
	}
}

func rssItemKeys(items []StoredRSSEntry) []string {
	keys := make([]string, 0, len(items))
	for _, item := range items {
		keys = append(keys, item.Key)
	}
	return keys
}

func TestRSSCategoriesDoNotShareStorageAcrossTrackerBoundary(t *testing.T) {
	tracker := NewMemoryTracker()
	feedURL := "https://example.test/feed.xml"
	itemKey := "rss-item"
	categories := []string{"security", "threat"}

	tracker.MarkRSSItemParsed(feedURL, itemKey, StoredRSSEntry{
		Key:        itemKey,
		FeedURL:    feedURL,
		Title:      "RSS Item",
		Published:  "2026-01-01 00:00:00",
		Categories: categories,
	})
	categories[0] = "mutated-input"

	unsent := tracker.GetUnsentRSSItemsForDestination("discord.rss.general")
	if got := unsent[0].Categories[0]; got != "security" {
		t.Fatalf("stored category = %q, want security", got)
	}
	unsent[0].Categories[0] = "mutated-output"

	unsent = tracker.GetUnsentRSSItemsForDestination("discord.rss.general")
	if got := unsent[0].Categories[0]; got != "security" {
		t.Fatalf("returned category mutation changed tracker state: %q", got)
	}
}

func TestStatusSummaryReportsOperationalCounts(t *testing.T) {
	tracker := newTestTracker(t)

	apiWebhookURL := "https://hooks.slack.com/services/T000/B000/status"
	rssWebhookURL := "https://discord.com/api/webhooks/123456789012345678/status-token"
	feedURL := "https://example.test/feed.xml"
	apiErr := "API authentication failed; check api_key"
	rssErr := "feed unavailable"

	tracker.MarkAPIItemSentToWebhook("api-item", "API Item", apiWebhookURL)
	tracker.UpdateAPIStatus(false, 0, apiErr)
	tracker.UpdateFeedStatus(feedURL, false, 0, rssErr)
	tracker.MarkRSSItemParsed(feedURL, "rss-item", StoredRSSEntry{
		Key:       "rss-item",
		FeedURL:   feedURL,
		Title:     "RSS Item",
		Published: "2026-01-01 00:00:00.000000",
	})
	tracker.MarkRSSItemSentToWebhook("rss-item", "RSS Item", "Example Feed", rssWebhookURL)
	if !tracker.enqueueRetryForTest("retry-item", apiWebhookURL, "slack", "api", "Retry Item", "retry pending", 5, time.Hour) {
		t.Fatal("EnqueueRetry() unexpectedly moved retry item to dead letter")
	}
	tracker.MarkRetryDeadLetter("dead-item", "discord", "rss", "Dead Item", "terminal failure")
	deadLetters := tracker.GetDeadLetterItems()
	if len(deadLetters) != 1 {
		t.Fatalf("dead letter count = %d, want 1", len(deadLetters))
	}
	if got := deadLetters[0].TerminalReason; got != TerminalReasonTerminalFailure {
		t.Fatalf("direct dead letter terminal reason = %q, want %q", got, TerminalReasonTerminalFailure)
	}

	summary := tracker.StatusSummary()

	if summary.APISentItems != 1 {
		t.Fatalf("APISentItems = %d, want 1", summary.APISentItems)
	}
	if summary.RSSFeeds != 1 || summary.RSSFeedErrors != 1 {
		t.Fatalf("RSSFeeds/RSSFeedErrors = %d/%d, want 1/1", summary.RSSFeeds, summary.RSSFeedErrors)
	}
	if summary.RSSParsedItems != 1 || summary.RSSSentItems != 1 {
		t.Fatalf("RSSParsedItems/RSSSentItems = %d/%d, want 1/1", summary.RSSParsedItems, summary.RSSSentItems)
	}
	if summary.RetryQueueItems != 1 || summary.DeadLetterItems != 1 {
		t.Fatalf("RetryQueueItems/DeadLetterItems = %d/%d, want 1/1", summary.RetryQueueItems, summary.DeadLetterItems)
	}
	if summary.LastAPIError != apiErr {
		t.Fatalf("LastAPIError = %q, want %q", summary.LastAPIError, apiErr)
	}
	if summary.LastRSSError != rssErr {
		t.Fatalf("LastRSSError = %q, want %q", summary.LastRSSError, rssErr)
	}
}

func TestSourceHealthErrorInfoPersistsAndClears(t *testing.T) {
	tracker := NewMemoryTracker()
	retryable := true
	timeout := false
	tracker.UpdateAPIStatusWithErrorInfo(false, 0, "rate limited", SourceErrorInfo{
		ErrorCategory: "rate_limited",
		StatusCode:    429,
		Retryable:     &retryable,
		Timeout:       &timeout,
	})
	apiStatus := tracker.APIStatusSnapshot()
	if apiStatus.LastErrorCategory != "rate_limited" || apiStatus.LastStatusCode != 429 {
		t.Fatalf("API source info = category %q status %d", apiStatus.LastErrorCategory, apiStatus.LastStatusCode)
	}
	if apiStatus.LastRetryable == nil || !*apiStatus.LastRetryable {
		t.Fatalf("API LastRetryable = %v, want true", apiStatus.LastRetryable)
	}
	if apiStatus.LastTimeout == nil || *apiStatus.LastTimeout {
		t.Fatalf("API LastTimeout = %v, want false", apiStatus.LastTimeout)
	}

	retryable = false
	timeout = true
	tracker.UpdateFeedStatusWithErrorInfo("https://feed.example/rss", false, 0, "timeout", SourceErrorInfo{
		ErrorCategory: "timeout",
		Retryable:     &retryable,
		Timeout:       &timeout,
	})
	feedInfo, ok := tracker.GetRSSFeedInfo("https://feed.example/rss")
	if !ok {
		t.Fatal("GetRSSFeedInfo() missing feed source status")
	}
	if feedInfo.LastErrorCategory != "timeout" {
		t.Fatalf("feed LastErrorCategory = %q, want timeout", feedInfo.LastErrorCategory)
	}
	if feedInfo.LastRetryable == nil || *feedInfo.LastRetryable {
		t.Fatalf("feed LastRetryable = %v, want false", feedInfo.LastRetryable)
	}
	if feedInfo.LastTimeout == nil || !*feedInfo.LastTimeout {
		t.Fatalf("feed LastTimeout = %v, want true", feedInfo.LastTimeout)
	}

	tracker.UpdateAPIStatus(true, 1, "")
	apiStatus = tracker.APIStatusSnapshot()
	if apiStatus.LastErrorCategory != "" || apiStatus.LastRetryable != nil || apiStatus.LastTimeout != nil {
		t.Fatalf("API source error info was not cleared after success: %+v", apiStatus)
	}
	tracker.UpdateFeedStatus("https://feed.example/rss", true, 1, "")
	feedInfo, ok = tracker.GetRSSFeedInfo("https://feed.example/rss")
	if !ok {
		t.Fatal("GetRSSFeedInfo() missing feed source status after success")
	}
	if feedInfo.LastErrorCategory != "" || feedInfo.LastRetryable != nil || feedInfo.LastTimeout != nil {
		t.Fatalf("feed source error info was not cleared after success: %+v", feedInfo)
	}
}

func TestCleanupOldEntriesKeepsUnsentRSSItemsAboveRetentionCap(t *testing.T) {
	tracker := newTestTracker(t)

	const retentionCap = 10000
	feedURL := "https://example.test/feed.xml"
	webhookURL := "https://discord.com/api/webhooks/123456789012345678/test-token"
	baseTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	entries := make([]StoredRSSEntry, 0, retentionCap+1)
	for i := 0; i < retentionCap+1; i++ {
		key := fmt.Sprintf("rss-%05d", i)
		published := baseTime.Add(time.Duration(i) * time.Minute).Format(time.RFC3339)
		entries = append(entries, StoredRSSEntry{
			Key:       key,
			FeedURL:   feedURL,
			Title:     fmt.Sprintf("RSS Item %05d", i),
			Link:      fmt.Sprintf("https://example.test/rss-%05d", i),
			Published: published,
			ParsedAt:  published,
		})
	}
	tracker.seedRSSParsedItemsForRetention(entries)

	tracker.CleanupOldEntries()

	if got := tracker.StatusSummary().RSSParsedItems; got != retentionCap+1 {
		t.Fatalf("parsed RSS item count = %d, want %d unsent items preserved", got, retentionCap+1)
	}
	if !tracker.IsRSSItemParsed(feedURL, "rss-00000") {
		t.Fatal("oldest unsent RSS item was removed by cleanup")
	}
	if unsent := tracker.GetUnsentRSSItemsForWebhook(webhookURL); len(unsent) != retentionCap+1 {
		t.Fatalf("unsent RSS item count = %d, want %d", len(unsent), retentionCap+1)
	}
}

func TestCleanupOldEntriesKeepsOldAPISentMarkers(t *testing.T) {
	tracker := newTestTracker(t)

	itemKey := "id:old-api-item"
	destinationID := "slack.ransomware"
	tracker.MarkAPIItemSentToDestination(itemKey, "Old API Item", destinationID)

	oldSentAt := time.Now().Add(-90 * 24 * time.Hour)
	tracker.setAllAPISentItemTimes(oldSentAt)

	tracker.CleanupOldEntries()

	if !tracker.IsAPIItemSentToDestination(itemKey, destinationID) {
		t.Fatal("old API sent marker was removed by cleanup")
	}
}

func TestCleanupOldEntriesPrunesAPISentItemsByCount(t *testing.T) {
	tracker := NewMemoryTracker()
	destinationID := "slack.ransomware"
	baseTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	for i := 0; i < defaultMaxAPISentItems+2; i++ {
		itemKey := fmt.Sprintf("api-%06d", i)
		tracker.seedAPISentItemForRetention(itemKey, destinationID, baseTime.Add(time.Duration(i)*time.Second))
	}

	tracker.CleanupOldEntries()

	if got := tracker.StatusSummary().APISentItems; got != defaultMaxAPISentItems {
		t.Fatalf("API sent item count = %d, want %d", got, defaultMaxAPISentItems)
	}
	if tracker.IsAPIItemSentToDestination("api-000000", destinationID) {
		t.Fatal("oldest API sent item was retained over count cap")
	}
	if !tracker.IsAPIItemSentToDestination(fmt.Sprintf("api-%06d", defaultMaxAPISentItems+1), destinationID) {
		t.Fatal("newest API sent item was pruned")
	}
}

func TestCleanupOldEntriesUsesConfiguredAPISentRetention(t *testing.T) {
	policy := DefaultRetentionPolicy()
	policy.MaxAPISentItems = 2
	tracker := NewMemoryTrackerWithRetention(policy)
	destinationID := "slack.ransomware"
	baseTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	for i := 0; i < 4; i++ {
		itemKey := fmt.Sprintf("api-%d", i)
		tracker.seedAPISentItemForRetention(itemKey, destinationID, baseTime.Add(time.Duration(i)*time.Second))
	}

	tracker.CleanupOldEntries()

	if got := tracker.StatusSummary().APISentItems; got != 2 {
		t.Fatalf("API sent item count = %d, want configured cap 2", got)
	}
	if tracker.IsAPIItemSentToDestination("api-0", destinationID) {
		t.Fatal("oldest API sent item was retained over configured cap")
	}
	if !tracker.IsAPIItemSentToDestination("api-3", destinationID) {
		t.Fatal("newest API sent item was pruned by configured cap")
	}
}

func TestCleanupOldEntriesSkipsStaleAPISentHeapEntries(t *testing.T) {
	policy := DefaultRetentionPolicy()
	policy.MaxAPISentItems = 2
	tracker := NewMemoryTrackerWithRetention(policy)
	destinationID := "slack.ransomware"
	baseTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	tracker.seedStaleAPISentHeapEntryForInvariant("api-updated", destinationID, baseTime)
	tracker.seedAPISentItemForRetention("api-updated", destinationID, baseTime.Add(10*time.Minute))
	tracker.seedAPISentItemForRetention("api-old", destinationID, baseTime.Add(time.Minute))
	tracker.seedAPISentItemForRetention("api-recent", destinationID, baseTime.Add(5*time.Minute))

	tracker.CleanupOldEntries()

	if got := tracker.StatusSummary().APISentItems; got != 2 {
		t.Fatalf("API sent item count = %d, want configured cap 2", got)
	}
	if !tracker.IsAPIItemSentToDestination("api-updated", destinationID) {
		t.Fatal("updated API sent marker was pruned by a stale heap entry")
	}
	if !tracker.IsAPIItemSentToDestination("api-recent", destinationID) {
		t.Fatal("recent API sent marker was pruned")
	}
	if tracker.IsAPIItemSentToDestination("api-old", destinationID) {
		t.Fatal("oldest current API sent marker was retained over configured cap")
	}
}

func TestRSSCleanupDoesNotPruneAPISentHistory(t *testing.T) {
	tracker := NewMemoryTracker()
	for i := 0; i < defaultMaxAPISentItems+1; i++ {
		key := fmt.Sprintf("api-%06d", i)
		tracker.seedAPISentItemForRetention(key, "discord.ransomware", time.Now().UTC())
	}

	tracker.CleanupOldEntriesForRSSDestinations([]string{"discord.rss.general"})

	if got := tracker.StatusSummary().APISentItems; got != defaultMaxAPISentItems+1 {
		t.Fatalf("API sent item count = %d, want %d after RSS-only cleanup", got, defaultMaxAPISentItems+1)
	}
}

func TestCleanupOldEntriesUsesConfiguredRSSParsedRetention(t *testing.T) {
	policy := DefaultRetentionPolicy()
	policy.MaxRSSParsedItems = 2
	tracker := NewMemoryTrackerWithRetention(policy)
	feedURL := "https://example.test/feed.xml"
	destinationID := "discord.rss.general"
	baseTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	entries := make([]StoredRSSEntry, 0, 3)
	for i := 0; i < 3; i++ {
		key := fmt.Sprintf("rss-%d", i)
		published := baseTime.Add(time.Duration(i) * time.Minute).Format(time.RFC3339)
		entries = append(entries, StoredRSSEntry{
			Key:       key,
			FeedURL:   feedURL,
			Title:     fmt.Sprintf("RSS Item %d", i),
			Published: published,
			ParsedAt:  published,
		})
	}
	tracker.seedRSSParsedItemsForRetention(entries)
	tracker.MarkRSSItemSentToDestination("rss-0", "RSS Item 0", "Example Feed", destinationID)

	tracker.CleanupOldEntriesForRSSDestinations([]string{destinationID})

	if tracker.IsRSSItemParsed(feedURL, "rss-0") {
		t.Fatal("fully delivered oldest RSS item was retained over configured cap")
	}
	if got := tracker.StatusSummary().RSSParsedItems; got != 2 {
		t.Fatalf("parsed RSS item count = %d, want configured cap 2", got)
	}
}

func TestCleanupOldEntriesKeepsRSSItemsUntilAllWebhookTargetsSent(t *testing.T) {
	tracker := newTestTracker(t)

	const retentionCap = 10000
	feedURL := "https://example.test/feed.xml"
	discordURL := "https://discord.com/api/webhooks/123456789012345678/test-token"
	slackURL := "https://hooks.slack.com/services/T12345678/B12345678/testtoken1234567890"
	baseTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	entries := make([]StoredRSSEntry, 0, retentionCap+2)
	for i := 0; i < retentionCap+2; i++ {
		key := fmt.Sprintf("rss-%05d", i)
		published := baseTime.Add(time.Duration(i) * time.Minute).Format(time.RFC3339)
		entries = append(entries, StoredRSSEntry{
			Key:       key,
			FeedURL:   feedURL,
			Title:     fmt.Sprintf("RSS Item %05d", i),
			Link:      fmt.Sprintf("https://example.test/rss-%05d", i),
			Published: published,
			ParsedAt:  published,
		})
	}
	tracker.seedRSSParsedItemsForRetention(entries)

	tracker.MarkRSSItemSentToWebhook("rss-00000", "RSS Item 00000", "Example Feed", discordURL)
	tracker.MarkRSSItemSentToWebhook("rss-00001", "RSS Item 00001", "Example Feed", discordURL)
	tracker.MarkRSSItemSentToWebhook("rss-00001", "RSS Item 00001", "Example Feed", slackURL)
	contentSig := rssContentSignatureForTest(feedURL, "RSS Item 00001", baseTime.Add(time.Minute))
	tracker.MarkRSSItemSentToWebhook(contentSig, "RSS Item 00001", "Example Feed", discordURL)
	tracker.MarkRSSItemSentToWebhook(contentSig, "RSS Item 00001", "Example Feed", slackURL)

	tracker.CleanupOldEntriesForRSSWebhooks([]string{discordURL, slackURL})

	if !tracker.IsRSSItemParsed(feedURL, "rss-00000") {
		t.Fatal("partially delivered RSS item was removed by cleanup")
	}
	if tracker.IsRSSItemParsed(feedURL, "rss-00001") {
		t.Fatal("fully delivered RSS item was kept while retention cap was exceeded")
	}
	if tracker.IsRSSItemSentToWebhook(contentSig, discordURL) || tracker.IsRSSItemSentToWebhook(contentSig, slackURL) {
		t.Fatal("content-signature sent records for removed RSS item were kept")
	}
	if got := tracker.StatusSummary().RSSParsedItems; got != retentionCap+1 {
		t.Fatalf("parsed RSS item count = %d, want %d after removing only fully delivered item", got, retentionCap+1)
	}
	if unsent := tracker.GetUnsentRSSItemsForWebhook(slackURL); len(unsent) != retentionCap+1 {
		t.Fatalf("slack unsent RSS item count = %d, want %d", len(unsent), retentionCap+1)
	}
}

func TestCleanupOldEntriesPrunesDeliveredRSSItemsByAge(t *testing.T) {
	tracker := newTestTracker(t)

	feedURL := "https://example.test/feed.xml"
	webhookURL := "https://discord.com/api/webhooks/123456789012345678/test-token"
	oldTime := time.Now().Add(-defaultRSSParsedMaxAge - time.Hour).UTC()
	recentTime := time.Now().UTC()

	tracker.seedRSSParsedItemsForRetention([]StoredRSSEntry{
		{
			Key:       "old-delivered",
			FeedURL:   feedURL,
			Title:     "Old Delivered",
			Published: oldTime.Format(time.RFC3339),
			ParsedAt:  oldTime.Format(statusTimestampLayoutMicros),
		},
		{
			Key:       "old-unsent",
			FeedURL:   feedURL,
			Title:     "Old Unsent",
			Published: oldTime.Format(time.RFC3339),
			ParsedAt:  oldTime.Format(statusTimestampLayoutMicros),
		},
		{
			Key:       "recent-delivered",
			FeedURL:   feedURL,
			Title:     "Recent Delivered",
			Published: recentTime.Format(time.RFC3339),
			ParsedAt:  recentTime.Format(statusTimestampLayoutMicros),
		},
	})
	tracker.MarkRSSItemSentToWebhook("old-delivered", "Old Delivered", "Example Feed", webhookURL)
	tracker.MarkRSSItemSentToWebhook("recent-delivered", "Recent Delivered", "Example Feed", webhookURL)
	if !tracker.enqueueRetryForTest("old-delivered", webhookURL, "discord", "rss", "Old Delivered", "temporary failure", 5, time.Hour) {
		t.Fatal("EnqueueRetry(old-delivered) unexpectedly moved item to dead letter")
	}
	if !tracker.enqueueRetryForTest("old-unsent", webhookURL, "discord", "rss", "Old Unsent", "temporary failure", 5, time.Hour) {
		t.Fatal("EnqueueRetry(old-unsent) unexpectedly moved item to dead letter")
	}

	tracker.CleanupOldEntriesForRSSWebhooks([]string{webhookURL})

	if tracker.IsRSSItemParsed(feedURL, "old-delivered") {
		t.Fatal("old delivered RSS item was not pruned by age")
	}
	if !tracker.IsRSSItemParsed(feedURL, "old-unsent") {
		t.Fatal("old unsent RSS item was pruned before delivery")
	}
	if !tracker.IsRSSItemParsed(feedURL, "recent-delivered") {
		t.Fatal("recent delivered RSS item was pruned")
	}
	if tracker.IsRSSItemSentToWebhook("old-delivered", webhookURL) {
		t.Fatal("sent marker for pruned RSS item was retained")
	}
	if !tracker.IsRSSItemSentToWebhook("recent-delivered", webhookURL) {
		t.Fatal("sent marker for recent RSS item was removed")
	}
	if items := tracker.GetRetryItemsByType("rss"); len(items) != 1 || items[0].ItemKey != "old-unsent" {
		t.Fatalf("remaining RSS retry items = %+v, want only old-unsent", items)
	}
	if !tracker.IsRetryDeadLettered("old-delivered", "discord", "rss") {
		t.Fatal("retry row for pruned RSS item was not moved to dead letter")
	}
	deadLetters := tracker.GetDeadLetterItems()
	if len(deadLetters) != 1 {
		t.Fatalf("dead letter count = %d, want 1", len(deadLetters))
	}
	if got := deadLetters[0].TerminalReason; got != TerminalReasonRSSParsedPruned {
		t.Fatalf("dead letter terminal reason = %q, want %q", got, TerminalReasonRSSParsedPruned)
	}
}

func TestCleanupOldEntriesKeepsSentMarkerUntilParsedItemPruned(t *testing.T) {
	tracker := newTestTracker(t)

	feedURL := "https://example.test/feed.xml"
	webhookURL := "https://discord.com/api/webhooks/123456789012345678/test-token"
	oldTime := time.Now().Add(-defaultRSSParsedMaxAge - time.Hour).UTC()
	tracker.seedRSSParsedItemsForRetention([]StoredRSSEntry{{
		Key:       "old-delivered-with-old-sent-marker",
		FeedURL:   feedURL,
		Title:     "Old Delivered With Old Sent Marker",
		Published: oldTime.Format(time.RFC3339),
		ParsedAt:  oldTime.Format(statusTimestampLayoutMicros),
	}})
	tracker.MarkRSSItemSentToWebhook("old-delivered-with-old-sent-marker", "Old Delivered With Old Sent Marker", "Example Feed", webhookURL)
	if !tracker.setRSSSentItemTime("old-delivered-with-old-sent-marker", webhookURL, oldTime) {
		t.Fatal("seeded RSS sent item missing")
	}

	tracker.CleanupOldEntriesForRSSWebhooks([]string{webhookURL})

	if tracker.IsRSSItemParsed(feedURL, "old-delivered-with-old-sent-marker") {
		t.Fatal("old fully delivered parsed RSS item was retained after its sent marker was pruned first")
	}
	if tracker.IsRSSItemSentToWebhook("old-delivered-with-old-sent-marker", webhookURL) {
		t.Fatal("sent marker for pruned parsed RSS item was retained")
	}
}

func TestCleanupOldEntriesPrunesAgedRSSSentItemsWithoutParsedCleanup(t *testing.T) {
	tracker := newTestTracker(t)

	feedURL := "https://example.test/feed.xml"
	webhookURL := "https://discord.com/api/webhooks/123456789012345678/test-token"
	recentTime := time.Now().UTC()
	oldTime := time.Now().Add(-defaultRSSParsedMaxAge - time.Hour).UTC()

	tracker.seedRSSParsedItemsForRetention([]StoredRSSEntry{{
		Key:       "current-item",
		FeedURL:   feedURL,
		Title:     "Current Item",
		Published: recentTime.Format(time.RFC3339),
		ParsedAt:  recentTime.Format(statusTimestampLayoutMicros),
	}})
	tracker.MarkRSSItemSentToWebhook("current-item", "Current Item", "Example Feed", webhookURL)

	oldContentSignature := rssContentSignatureForTest(feedURL, "Old Duplicate", oldTime)
	tracker.MarkRSSItemSentToDestination(oldContentSignature, "Old Duplicate", "Example Feed", webhookURL)
	if !tracker.setRSSSentItemTime(oldContentSignature, webhookURL, oldTime) {
		t.Fatal("seeded RSS sent item missing")
	}
	tracker.clearDirtyState()

	tracker.CleanupOldEntriesForRSSWebhooks([]string{webhookURL})

	if tracker.IsRSSItemSentToWebhook(oldContentSignature, webhookURL) {
		t.Fatal("aged RSS content-signature sent item was retained without parsed cleanup")
	}
	if !tracker.IsRSSItemSentToWebhook("current-item", webhookURL) {
		t.Fatal("recent RSS sent item was removed")
	}
	if got := tracker.StatusSummary().RSSSentItems; got != 1 {
		t.Fatalf("RSS sent item count = %d, want 1", got)
	}
	if !tracker.DirtyState().RSS {
		t.Fatal("RSS cleanup did not mark status dirty after pruning aged sent item")
	}
}

func TestParseStatusTimestampAcceptsStatusLayout(t *testing.T) {
	value := time.Now().Add(-defaultRetryQueueMaxAge - time.Hour).Format(statusTimestampLayout)

	parsed := parseStatusTimestamp(value)
	if parsed.IsZero() {
		t.Fatalf("parseStatusTimestamp(%q) returned zero", value)
	}
	if parsed.Location() != time.UTC {
		t.Fatalf("parsed location = %v, want UTC", parsed.Location())
	}
}

func TestCleanupOldEntriesMovesAgedRetryQueueItemsToDeadLetter(t *testing.T) {
	tracker := newTestTracker(t)

	itemKey := "retry-item"
	webhookURL := "https://discord.com/api/webhooks/123456789012345678/test-token"
	if !tracker.enqueueRetryForTest(itemKey, webhookURL, "discord", "api", "Retry Item", "temporary failure", 5, time.Hour) {
		t.Fatal("EnqueueRetry() unexpectedly moved item to dead letter")
	}

	oldTime := statusNow().Add(-defaultRetryQueueMaxAge - time.Hour).Format(statusTimestampLayout)
	if !tracker.setQueuedRetryTimes(itemKey, "api", oldTime, oldTime) {
		t.Fatal("queued retry item missing")
	}

	tracker.CleanupOldEntries()

	if items := tracker.GetRetryItemsByType("api"); len(items) != 0 {
		t.Fatalf("retry queue length = %d, want 0 after aged retry cleanup", len(items))
	}
	if !tracker.IsRetryDeadLettered(itemKey, "discord", "api") {
		t.Fatal("aged retry item was not moved to dead letter")
	}
	deadLetters := tracker.GetDeadLetterItems()
	if len(deadLetters) != 1 {
		t.Fatalf("dead letter count = %d, want 1", len(deadLetters))
	}
	if got := deadLetters[0].TerminalReason; got != TerminalReasonRetentionPruned {
		t.Fatalf("dead letter terminal reason = %q, want %q", got, TerminalReasonRetentionPruned)
	}
}

func TestCleanupOldEntriesUsesConfiguredRetryQueueMaxAge(t *testing.T) {
	policy := DefaultRetentionPolicy()
	policy.RetryQueueMaxAge = time.Hour
	tracker := NewMemoryTrackerWithRetention(policy)

	itemKey := "retry-item"
	webhookURL := "https://discord.com/api/webhooks/123456789012345678/test-token"
	if !tracker.enqueueRetryForTest(itemKey, webhookURL, "discord", "api", "Retry Item", "temporary failure", 5, 24*time.Hour) {
		t.Fatal("EnqueueRetry() unexpectedly moved item to dead letter")
	}

	oldTime := statusNow().Add(-2 * time.Hour).Format(statusTimestampLayout)
	if !tracker.setQueuedRetryTimes(itemKey, "api", oldTime, oldTime) {
		t.Fatal("queued retry item missing")
	}

	tracker.CleanupOldEntries()

	if items := tracker.GetRetryItemsByType("api"); len(items) != 0 {
		t.Fatalf("retry queue length = %d, want 0 after configured age cleanup", len(items))
	}
	if !tracker.IsRetryDeadLettered(itemKey, "discord", "api") {
		t.Fatal("retry item older than configured age was not moved to dead letter")
	}
}

func TestCleanupOldEntriesPrunesAgedDeadLetters(t *testing.T) {
	tracker := newTestTracker(t)

	oldTime := statusNow().Add(-defaultDeadLetterMaxAge - time.Hour).Format(statusTimestampLayout)
	recentTime := statusNow().Format(statusTimestampLayout)
	tracker.MarkRetryDeadLetter("old-dead", "discord", "api", "Old", "terminal failure")
	tracker.MarkRetryDeadLetter("recent-dead", "discord", "api", "Recent", "terminal failure")
	if updated := tracker.setDeadLetterTimes(map[string]string{
		"old-dead":    oldTime,
		"recent-dead": recentTime,
	}); updated != 2 {
		t.Fatalf("updated dead letter timestamps = %d, want 2", updated)
	}

	tracker.CleanupOldEntries()

	deadLetters := tracker.GetDeadLetterItems()
	if len(deadLetters) != 1 {
		t.Fatalf("dead letter count = %d, want 1", len(deadLetters))
	}
	if got := deadLetters[0].ItemKey; got != "recent-dead" {
		t.Fatalf("remaining dead letter key = %q, want recent-dead", got)
	}
}

func rssContentSignatureForTest(feedURL, title string, published time.Time) string {
	return storedRSSContentSignatureLegacyV3(feedURL, title, published, "")
}

func TestEnqueueRetryDoesNotRequeueDeadLetteredItem(t *testing.T) {
	tracker := newTestTracker(t)

	webhookURL := "https://discord.com/api/webhooks/123456789012345678/test-token"
	if !tracker.enqueueRetryForTest("item-1", webhookURL, "discord", "api", "Example", "first failure", 1, time.Hour) {
		t.Fatal("first EnqueueRetry() unexpectedly failed")
	}
	if tracker.enqueueRetryForTest("item-1", webhookURL, "discord", "api", "Example", "second failure", 1, time.Hour) {
		t.Fatal("second EnqueueRetry() should move item to dead letter")
	}
	if tracker.enqueueRetryForTest("item-1", webhookURL, "discord", "api", "Example", "third failure", 1, time.Hour) {
		t.Fatal("dead-lettered item was requeued")
	}

	if items := tracker.GetRetryItemsByType("api"); len(items) != 0 {
		t.Fatalf("retry queue length = %d, want 0 after dead letter", len(items))
	}
	if !tracker.IsRetryDeadLettered("item-1", "discord", "api") {
		t.Fatal("item should be reported as dead-lettered")
	}
	deadLetters := tracker.GetDeadLetterItems()
	if len(deadLetters) != 1 {
		t.Fatalf("dead letter count = %d, want 1", len(deadLetters))
	}
	if got := deadLetters[0].TerminalReason; got != TerminalReasonMaxAttempts {
		t.Fatalf("dead letter terminal reason = %q, want %q", got, TerminalReasonMaxAttempts)
	}
}

func TestEnqueueRetryDeadLettersMalformedFirstFailedTimestamp(t *testing.T) {
	tracker := newTestTracker(t)

	itemKey := "item-1"
	webhookURL := "https://discord.com/api/webhooks/123456789012345678/test-token"
	if !tracker.enqueueRetryForTest(itemKey, webhookURL, "discord", "api", "Example", "first failure", 10, time.Hour) {
		t.Fatal("initial EnqueueRetry() unexpectedly failed")
	}

	if !tracker.setQueuedRetryTimes(itemKey, "api", "not-a-time", "2026-01-01 00:00:00") {
		t.Fatal("queued retry item missing")
	}

	if tracker.enqueueRetryForTest(itemKey, webhookURL, "discord", "api", "Example", "second failure", 10, time.Hour) {
		t.Fatal("EnqueueRetry() accepted malformed first_failed timestamp")
	}
	if items := tracker.GetRetryItemsByType("api"); len(items) != 0 {
		t.Fatalf("retry queue length = %d, want 0 after dead letter", len(items))
	}
	if !tracker.IsRetryDeadLettered(itemKey, "discord", "api") {
		t.Fatal("item should be reported as dead-lettered")
	}
	assertDeadLetterContains(t, tracker, "invalid first_failed timestamp", TerminalReasonInvalidFirstFailed)
}

func TestEnqueueRetryRetryWindowMovesToDeadLetter(t *testing.T) {
	tracker := newTestTracker(t)

	itemKey := "item-1"
	webhookURL := "https://discord.com/api/webhooks/123456789012345678/test-token"
	if !tracker.enqueueRetryForTest(itemKey, webhookURL, "discord", "api", "Example", "first failure", 0, time.Hour) {
		t.Fatal("initial EnqueueRetry() unexpectedly failed")
	}

	item := tracker.GetRetryItemsByType("api")[0]
	if !tracker.setQueuedRetryTimes(itemKey, "api", formatStatusTimestamp(time.Now().Add(-2*time.Hour)), item.LastRetried) {
		t.Fatal("queued retry item missing")
	}

	if tracker.enqueueRetryForTest(itemKey, webhookURL, "discord", "api", "Example", "second failure", 0, time.Hour) {
		t.Fatal("EnqueueRetry() accepted item outside retry window")
	}
	if items := tracker.GetRetryItemsByType("api"); len(items) != 0 {
		t.Fatalf("retry queue length = %d, want 0 after retry window dead letter", len(items))
	}
	if !tracker.IsRetryDeadLettered(itemKey, "discord", "api") {
		t.Fatal("item should be reported as dead-lettered")
	}
	assertDeadLetterContains(t, tracker, "second failure", TerminalReasonRetryWindow)
}

func TestRecordRetryQueueFailureDeadLettersMalformedFirstFailedTimestamp(t *testing.T) {
	tracker := newTestTracker(t)

	itemKey := "item-1"
	webhookURL := "https://discord.com/api/webhooks/123456789012345678/test-token"
	if !tracker.enqueueRetryForTest(itemKey, webhookURL, "discord", "api", "Example", "first failure", 10, time.Hour) {
		t.Fatal("initial EnqueueRetry() unexpectedly failed")
	}

	if !tracker.setQueuedRetryTimes(itemKey, "api", "not-a-time", tracker.GetRetryItemsByType("api")[0].LastRetried) {
		t.Fatal("queued retry item missing")
	}

	queueKey := queuedRetryKeyForTest(t, tracker, itemKey, "api")
	if tracker.RecordRetryQueueFailure(queueKey, "second failure", 10, time.Hour) {
		t.Fatal("RecordRetryQueueFailure() accepted malformed first_failed timestamp")
	}
	if items := tracker.GetRetryItemsByType("api"); len(items) != 0 {
		t.Fatalf("retry queue length = %d, want 0 after dead letter", len(items))
	}
	if !tracker.IsRetryDeadLettered(itemKey, "discord", "api") {
		t.Fatal("item should be reported as dead-lettered")
	}
	assertDeadLetterContains(t, tracker, "invalid first_failed timestamp", TerminalReasonInvalidFirstFailed)
}

func TestRecordRetryQueueFailureRetryWindowMovesToDeadLetter(t *testing.T) {
	tracker := newTestTracker(t)

	itemKey := "item-1"
	webhookURL := "https://discord.com/api/webhooks/123456789012345678/test-token"
	if !tracker.enqueueRetryForTest(itemKey, webhookURL, "discord", "api", "Example", "first failure", 0, time.Hour) {
		t.Fatal("initial EnqueueRetry() unexpectedly failed")
	}

	item := tracker.GetRetryItemsByType("api")[0]
	if !tracker.setQueuedRetryTimes(itemKey, "api", formatStatusTimestamp(time.Now().Add(-2*time.Hour)), item.LastRetried) {
		t.Fatal("queued retry item missing")
	}

	queueKey := queuedRetryKeyForTest(t, tracker, itemKey, "api")
	if tracker.RecordRetryQueueFailure(queueKey, "second failure", 0, time.Hour) {
		t.Fatal("RecordRetryQueueFailure() accepted item outside retry window")
	}
	if items := tracker.GetRetryItemsByType("api"); len(items) != 0 {
		t.Fatalf("retry queue length = %d, want 0 after retry window dead letter", len(items))
	}
	if !tracker.IsRetryDeadLettered(itemKey, "discord", "api") {
		t.Fatal("item should be reported as dead-lettered")
	}
	assertDeadLetterContains(t, tracker, "second failure", TerminalReasonRetryWindow)
}

func assertDeadLetterContains(t *testing.T, tracker *Tracker, wantError, wantReason string) {
	t.Helper()

	deadLetters := tracker.GetDeadLetterItems()
	if len(deadLetters) != 1 {
		t.Fatalf("dead letter count = %d, want 1", len(deadLetters))
	}
	if got := deadLetters[0].LastError; !strings.Contains(got, wantError) {
		t.Fatalf("dead letter LastError = %q, want it to contain %q", got, wantError)
	}
	if got := deadLetters[0].TerminalReason; got != wantReason {
		t.Fatalf("dead letter TerminalReason = %q, want %q", got, wantReason)
	}
}

func (tracker *Tracker) enqueueRetryForTest(
	itemKey, destinationID, messenger, itemType, title, lastError string,
	maxAttempts int,
	retryWindow time.Duration,
	payload ...[]byte,
) bool {
	return tracker.EnqueueRetry(retryRequestForTest(
		itemKey,
		destinationID,
		messenger,
		itemType,
		title,
		lastError,
		maxAttempts,
		retryWindow,
		payload...,
	))
}

func retryRequestForTest(
	itemKey, destinationID, messenger, itemType, title, lastError string,
	maxAttempts int,
	retryWindow time.Duration,
	payload ...[]byte,
) RetryRequest {
	req := RetryRequest{
		ItemKey:       itemKey,
		DestinationID: destinationID,
		Messenger:     Messenger(messenger),
		ItemType:      RetryItemType(itemType),
		Title:         title,
		LastError:     lastError,
		MaxAttempts:   maxAttempts,
		RetryWindow:   retryWindow,
	}
	if len(payload) > 0 {
		req.Payload = payload[0]
	}
	return req
}

func queuedRetryKeyForTest(t *testing.T, tracker *Tracker, itemKey, itemType string) string {
	t.Helper()
	for _, record := range tracker.GetQueuedRetryItemsByType(itemType) {
		if record.Item.ItemKey == itemKey {
			return record.QueueKey
		}
	}
	t.Fatalf("queued retry %q/%q not found", itemType, itemKey)
	return ""
}

func expectFileContains(t *testing.T, path, want string) {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", path, err)
	}
	if !strings.Contains(string(data), want) {
		t.Fatalf("%s does not contain %q: %s", path, want, data)
	}
}

func expectFileNotContains(t *testing.T, path, forbidden string) {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", path, err)
	}
	if strings.Contains(string(data), forbidden) {
		t.Fatalf("%s contains forbidden %q: %s", path, forbidden, data)
	}
}
