package scheduler

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/api"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/config"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/filter"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/rss"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/status"

	logtest "github.com/sirupsen/logrus/hooks/test"
)

func enqueueRetryForTest(
	t *testing.T,
	tracker *status.Tracker,
	itemKey, destinationID, messenger, itemType, title, lastError string,
	maxAttempts int,
	retryWindow time.Duration,
	payload ...[]byte,
) bool {
	t.Helper()
	req := status.RetryRequest{
		ItemKey:       itemKey,
		DestinationID: destinationID,
		Messenger:     status.Messenger(messenger),
		ItemType:      status.RetryItemType(itemType),
		Title:         title,
		LastError:     lastError,
		MaxAttempts:   maxAttempts,
		RetryWindow:   retryWindow,
	}
	if len(payload) > 0 {
		req.Payload = payload[0]
	}
	return tracker.EnqueueRetry(req)
}

func TestDeferAPIEntriesForQuietHoursQueuesPayloadWithoutRetryIncrement(t *testing.T) {
	tracker, err := status.NewTracker(t.TempDir())
	if err != nil {
		t.Fatalf("NewTracker() error = %v", err)
	}

	s := &Scheduler{
		config: &config.Config{
			RetryMaxAttempts: 3,
			RetryWindow:      time.Hour,
		},
		statusTracker: tracker,
	}

	entry := api.RansomwareEntry{
		ID:       "victim-1",
		Group:    "lockbit",
		Victim:   "Example Corp",
		Country:  "DE",
		Activity: "manufacturing",
	}
	webhookURL := "https://discord.com/api/webhooks/123456789012345678/test-token"

	if queued := s.deferAPIEntriesForQuietHours([]api.RansomwareEntry{entry}, webhookURL, "discord", "discord.ransomware"); queued != 1 {
		t.Fatalf("first defer queued %d entries, want 1", queued)
	}
	if queued := s.deferAPIEntriesForQuietHours([]api.RansomwareEntry{entry}, webhookURL, "discord", "discord.ransomware"); queued != 0 {
		t.Fatalf("second defer queued %d entries, want 0 duplicate", queued)
	}

	items := tracker.GetRetryItemsByType("api")
	if len(items) != 1 {
		t.Fatalf("retry queue length = %d, want 1", len(items))
	}

	item := items[0]
	if item.ItemKey != api.GenerateEntryKey(entry) {
		t.Fatalf("retry item key = %q, want %q", item.ItemKey, api.GenerateEntryKey(entry))
	}
	if item.Messenger != "discord" {
		t.Fatalf("retry item messenger = %q, want discord", item.Messenger)
	}
	if item.RetryCount != 0 {
		t.Fatalf("retry count = %d, want 0 for quiet-hours deferral", item.RetryCount)
	}
	if !strings.Contains(item.LastError, "quiet hours") {
		t.Fatalf("last error = %q, want quiet-hours reason", item.LastError)
	}
	if len(item.Payload) == 0 {
		t.Fatal("retry item payload is empty")
	}

	var payload api.RansomwareEntry
	if err := json.Unmarshal(item.Payload, &payload); err != nil {
		t.Fatalf("payload unmarshal error = %v", err)
	}
	if payload.ID != entry.ID || payload.Victim != entry.Victim {
		t.Fatalf("payload = %+v, want original entry %+v", payload, entry)
	}
}

func TestPendingAPIEntriesSkipsDeadLetteredItems(t *testing.T) {
	s := newTestScheduler(t)
	webhookURL := "https://discord.com/api/webhooks/123456789012345678/test-token"
	entry := api.RansomwareEntry{
		ID:      "victim-1",
		Group:   "lockbit",
		Victim:  "Example Corp",
		Country: "DE",
	}
	key := api.GenerateEntryKey(entry)

	if !enqueueRetryForTest(t, s.statusTracker, key, webhookURL, "discord", "api", "Example", "first failure", 1, time.Hour) {
		t.Fatal("first EnqueueRetry() unexpectedly failed")
	}
	if enqueueRetryForTest(t, s.statusTracker, key, webhookURL, "discord", "api", "Example", "second failure", 1, time.Hour) {
		t.Fatal("second EnqueueRetry() should dead-letter item")
	}

	pending, _ := s.pendingAPIEntriesForWebhook([]api.RansomwareEntry{entry}, webhookURL, "discord", "discord.ransomware", nil)
	if len(pending) != 0 {
		t.Fatalf("pending entries length = %d, want 0 for dead-lettered item", len(pending))
	}
}

func TestPendingAPIEntriesUsesLegacyFallbackDedupeKey(t *testing.T) {
	s := newTestScheduler(t)
	webhookURL := "https://discord.com/api/webhooks/123456789012345678/test-token"
	entry := api.RansomwareEntry{
		Group:      "LockBit",
		Victim:     "Corp",
		Country:    "US",
		AttackDate: "2025-01-15",
		ClaimURL:   "https://example.onion/post",
	}
	legacyKey := "LockBit|Corp|US|2025-01-15"
	if api.GenerateEntryKey(entry) == legacyKey {
		t.Fatal("GenerateEntryKey still returns the legacy fallback key")
	}

	s.statusTracker.MarkAPIItemSentToWebhook(legacyKey, "legacy sent marker", webhookURL)

	pending, _ := s.pendingAPIEntriesForWebhook([]api.RansomwareEntry{entry}, webhookURL, "discord", "discord.ransomware", nil)
	if len(pending) != 0 {
		t.Fatalf("pending entries length = %d, want 0 when legacy key is already sent", len(pending))
	}
}

func TestFilterDeadLetteredRSSItems(t *testing.T) {
	s := newTestScheduler(t)
	webhookURL := "https://discord.com/api/webhooks/123456789012345678/test-token"
	item := status.StoredRSSEntry{
		Key:       "rss-item-1",
		FeedURL:   "https://example.test/feed.xml",
		Title:     "Example RSS",
		Published: "2026-01-01 00:00:00",
	}

	if !enqueueRetryForTest(t, s.statusTracker, item.Key, webhookURL, "discord", "rss", item.Title, "first failure", 1, time.Hour) {
		t.Fatal("first EnqueueRetry() unexpectedly failed")
	}
	if enqueueRetryForTest(t, s.statusTracker, item.Key, webhookURL, "discord", "rss", item.Title, "second failure", 1, time.Hour) {
		t.Fatal("second EnqueueRetry() should dead-letter RSS item")
	}

	filtered := s.filterDeadLetteredRSSItems(unsentRSSItemsForTest(t, item), "discord", "discord.rss.general", "")
	if len(filtered) != 0 {
		t.Fatalf("filtered RSS items length = %d, want 0 for dead-lettered item", len(filtered))
	}
}

func TestFilterAttemptedRSSItems(t *testing.T) {
	s := newTestScheduler(t)
	webhookURL := "https://discord.com/api/webhooks/123456789012345678/test-token"
	items := []status.StoredRSSEntry{
		{Key: "rss-item-1", Title: "Attempted"},
		{Key: "rss-item-2", Title: "Not Attempted"},
	}
	attempted := rssDeliveryAttemptSet{
		rssDeliveryAttemptKey("rss-item-1", webhookURL): struct{}{},
	}

	filtered := s.filterAttemptedRSSItems(unsentRSSItemsForTest(t, items...), webhookURL, attempted)

	if len(filtered) != 1 {
		t.Fatalf("filtered RSS items length = %d, want 1", len(filtered))
	}
	if filtered[0].Key != "rss-item-2" {
		t.Fatalf("remaining item key = %q, want rss-item-2", filtered[0].Key)
	}
}

func TestSendParsedRSSToWebhooksMarksPrimaryKeyWhenContentSignatureAlreadySent(t *testing.T) {
	s := newTestScheduler(t)
	feedURL := "https://example.test/feed.xml"
	webhookURL := "https://discord.com/api/webhooks/123456789012345678/test-token"
	s.config.DiscordWebhooks.RSS = config.WebhookConfig{Enabled: true, URL: webhookURL}
	s.config.Feeds.GeneralFeeds = []string{feedURL}
	s.feedTypeMap = buildFeedTypeMap(s.config)

	published := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	entry := rss.Entry{
		Title:     "Duplicate Article",
		Link:      "https://example.test/article-variant",
		GUID:      "guid-variant",
		FeedURL:   feedURL,
		FeedTitle: "Example Feed",
		Published: published,
	}
	primaryKey := rss.GenerateEntryKey(entry.FeedURL, entry.GUID, entry.Link, entry.Title)
	contentSig := rss.GenerateContentSignature(entry.FeedURL, entry.Title, entry.Published)
	s.statusTracker.MarkRSSItemSentToWebhook(contentSig, entry.Title, entry.FeedTitle, webhookURL)

	s.sendParsedRSSToWebhooks(t.Context(), &rss.FeedResults{
		Entries:    map[string][]rss.Entry{feedURL: {entry}},
		FeedErrors: map[string]string{},
	}, "general", s.getWebhookTargets("general"), nil)

	if !s.statusTracker.IsRSSItemSentToDestination(primaryKey, "discord.rss.general", webhookURL) {
		t.Fatal("primary RSS key was not marked sent when content signature was already sent")
	}
}

func TestSendParsedRSSToWebhooksPersistsEmptyFeedStatus(t *testing.T) {
	s := newTestScheduler(t)
	feedURL := "https://example.test/feed.xml"

	s.sendParsedRSSToWebhooks(t.Context(), &rss.FeedResults{
		Entries:    map[string][]rss.Entry{feedURL: {}},
		FeedErrors: map[string]string{},
	}, "general", nil, nil)

	reloaded, err := status.NewTracker(s.config.DataDir)
	if err != nil {
		t.Fatalf("NewTracker() error = %v", err)
	}
	summary := reloaded.StatusSummary()
	if summary.RSSFeeds != 1 {
		t.Fatalf("RSSFeeds = %d, want persisted empty feed status", summary.RSSFeeds)
	}
	if summary.RSSFeedErrors != 0 {
		t.Fatalf("RSSFeedErrors = %d, want successful empty feed status", summary.RSSFeedErrors)
	}
}

func TestMinimizeRSSItemsWithoutMatchingTargetRemovesOutOfScopeContent(t *testing.T) {
	s := newTestScheduler(t)
	feedURL := "https://example.test/feed.xml"
	webhookURL := "https://discord.com/api/webhooks/123456789012345678/test-token"
	entry := rss.Entry{
		Title:       "Filtered Victim Story",
		Link:        "https://example.test/filtered-victim-story",
		Description: "Sensitive description",
		Author:      "Reporter",
		Categories:  []string{"out-of-scope"},
		GUID:        "guid-filtered",
		FeedTitle:   "Example Feed",
		FeedURL:     feedURL,
		Published:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	key := rss.GenerateEntryKey(entry.FeedURL, entry.GUID, entry.Link, entry.Title)
	s.statusTracker.MarkRSSItemParsed(feedURL, key, status.StoredRSSEntry{
		Key:         key,
		FeedURL:     feedURL,
		Title:       entry.Title,
		Link:        entry.Link,
		Description: entry.Description,
		Author:      entry.Author,
		Categories:  entry.Categories,
		GUID:        entry.GUID,
		FeedTitle:   entry.FeedTitle,
		Published:   "2026-01-01 00:00:00",
	})

	targets := []webhookTarget{{
		url:       webhookURL,
		messenger: "discord",
		filters:   &filter.Rules{IncludeKeywords: []string{"must-not-match"}},
	}}
	if minimized := s.minimizeRSSItemsWithoutMatchingTarget(&rss.FeedResults{
		Entries: map[string][]rss.Entry{feedURL: {entry}},
	}, targets); minimized != 1 {
		t.Fatalf("minimized items = %d, want 1", minimized)
	}
	if err := s.statusTracker.SavePendingChanges(); err != nil {
		t.Fatalf("SavePendingChanges: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(s.config.DataDir, "rss_status.json"))
	if err != nil {
		t.Fatalf("ReadFile(rss_status.json) error = %v", err)
	}
	got := string(data)
	if !strings.Contains(got, key) {
		t.Fatalf("rss_status.json missing minimized key %q: %s", key, got)
	}
	for _, forbidden := range []string{entry.Title, entry.Link, entry.Description, entry.Author, entry.Categories[0], entry.GUID, entry.FeedTitle} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("rss_status.json contains out-of-scope RSS content %q: %s", forbidden, got)
		}
	}
}

func TestSendParsedRSSToWebhooksFilteredItemsUseMinimalSkipMarkers(t *testing.T) {
	s := newTestScheduler(t)
	feedURL := "https://example.test/feed.xml"
	webhookURL := "https://discord.com/api/webhooks/123456789012345678/test-token"
	entry := rss.Entry{
		Title:      "Filtered Victim Story",
		Link:       "https://example.test/filtered-victim-story",
		FeedTitle:  "Example Feed",
		FeedURL:    feedURL,
		Published:  time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Categories: []string{"out-of-scope"},
	}
	targets := []webhookTarget{{
		url:       webhookURL,
		messenger: "discord",
		filters:   &filter.Rules{IncludeKeywords: []string{"must-not-match"}},
	}}

	s.sendParsedRSSToWebhooks(t.Context(), &rss.FeedResults{
		Entries:    map[string][]rss.Entry{feedURL: {entry}},
		FeedErrors: map[string]string{},
	}, config.FeedTypeGeneral, targets, nil)

	data, err := os.ReadFile(filepath.Join(s.config.DataDir, "rss_status.json"))
	if err != nil {
		t.Fatalf("ReadFile(rss_status.json) error = %v", err)
	}
	got := string(data)
	if strings.Contains(got, `"title"`) || strings.Contains(got, `"feed_title"`) {
		t.Fatalf("filtered RSS skip marker persisted titles: %s", got)
	}
	for _, forbidden := range []string{entry.Title, entry.Link, entry.FeedTitle, webhookURL, "test-token"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("rss_status.json contains filtered RSS content %q: %s", forbidden, got)
		}
	}
}

func TestSendRSSItemsToWebhookMarksPrimaryKeyWhenContentSignatureAlreadySent(t *testing.T) {
	s := newTestScheduler(t)
	feedURL := "https://example.test/feed.xml"
	webhookURL := "https://hooks.slack.com/services/T12345678/B12345678/testtoken1234567890"
	published := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	stored := status.StoredRSSEntry{
		Key:       rss.GenerateEntryKey(feedURL, "guid-variant", "", "Duplicate Article"),
		FeedURL:   feedURL,
		Title:     "Duplicate Article",
		GUID:      "guid-variant",
		Published: published.Format("2006-01-02 15:04:05.999999"),
		FeedTitle: "Example Feed",
	}
	rssEntry, err := status.RSSEntryFromStored(stored)
	if err != nil {
		t.Fatalf("RSSEntryFromStored() error = %v", err)
	}
	contentSig := rss.GenerateEntryContentSignature(rssEntry)
	s.statusTracker.MarkRSSItemSentToWebhook(contentSig, stored.Title, stored.FeedTitle, webhookURL)

	s.sendRSSItemsToWebhook(t.Context(), unsentRSSItemsForTest(t, stored), webhookURL, "general", "slack", nil)

	if !s.statusTracker.IsRSSItemSentToWebhook(stored.Key, webhookURL) {
		t.Fatal("recovery primary RSS key was not marked sent when content signature was already sent")
	}
}

func TestProcessAPIRetryQueuePersistsFailureOnlyRetryUpdates(t *testing.T) {
	s := newTestScheduler(t)
	var requestCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("temporary failure"))
	}))
	defer server.Close()
	s.config.SlackWebhooks.Ransomware = config.WebhookConfig{Enabled: true, URL: server.URL}

	entry := api.RansomwareEntry{
		ID:      "victim-1",
		Group:   "lockbit",
		Victim:  "Example Corp",
		Country: "DE",
	}
	key := api.GenerateEntryKey(entry)
	payload, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("Marshal(entry) error = %v", err)
	}
	if !enqueueRetryForTest(t, s.statusTracker, key, server.URL, "slack", "api", "Example Corp", "first failure", 5, time.Hour, payload) {
		t.Fatal("initial EnqueueRetry() unexpectedly failed")
	}
	if err := s.statusTracker.SavePendingChanges(); err != nil {
		t.Fatalf("SavePendingChanges: %v", err)
	}

	s.processAPIRetryQueue(t.Context(), nil)

	if requestCount != 1 {
		t.Fatalf("slack request count = %d, want 1", requestCount)
	}
	data, err := os.ReadFile(filepath.Join(s.config.DataDir, "retry_status.json"))
	if err != nil {
		t.Fatalf("ReadFile(retry_status.json) error = %v", err)
	}
	if !strings.Contains(string(data), `"retry_count": 2`) {
		t.Fatalf("retry failure update was not persisted: %s", data)
	}
}

func TestProcessAPIRetryQueueDeadLettersPermanentFailure(t *testing.T) {
	s := newTestScheduler(t)
	var requestCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("no_service"))
	}))
	defer server.Close()
	s.config.SlackWebhooks.Ransomware = config.WebhookConfig{Enabled: true, URL: server.URL}

	entry := api.RansomwareEntry{
		ID:      "victim-permanent-1",
		Group:   "lockbit",
		Victim:  "Example Corp",
		Country: "DE",
	}
	key := api.GenerateEntryKey(entry)
	payload, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("Marshal(entry) error = %v", err)
	}
	if !enqueueRetryForTest(t, s.statusTracker, key, server.URL, "slack", "api", "Example Corp", "first failure", 5, time.Hour, payload) {
		t.Fatal("initial EnqueueRetry() unexpectedly failed")
	}

	s.processAPIRetryQueue(t.Context(), nil)

	if requestCount != 1 {
		t.Fatalf("slack request count = %d, want 1", requestCount)
	}
	if items := s.statusTracker.GetRetryItemsByType("api"); len(items) != 0 {
		t.Fatalf("retry queue length = %d, want 0 after permanent retry failure", len(items))
	}
	if !s.statusTracker.IsRetryDeadLettered(key, "slack", "api") {
		t.Fatal("permanent API retry failure was not dead-lettered")
	}
}

func TestCheckAPIOnceProcessesRetryQueueWhenAPIFetchFails(t *testing.T) {
	s := newTestScheduler(t)
	s.config.APIKey = "test-key"
	s.config.SlackWebhooks.Ransomware = config.WebhookConfig{
		Enabled: true,
	}

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"temporary outage"}`))
	}))
	defer apiServer.Close()

	apiClient, err := api.NewClientWithBaseURL("test-key", apiServer.URL)
	if err != nil {
		t.Fatalf("NewClientWithBaseURL() error = %v", err)
	}
	s.apiClient = apiClient

	var slackRequests int
	slackServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slackRequests++
		w.WriteHeader(http.StatusOK)
	}))
	defer slackServer.Close()
	s.config.SlackWebhooks.Ransomware.URL = slackServer.URL

	entry := api.RansomwareEntry{
		ID:      "victim-1",
		Group:   "lockbit",
		Victim:  "Example Corp",
		Country: "DE",
	}
	key := api.GenerateEntryKey(entry)
	payload, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("Marshal(entry) error = %v", err)
	}
	if !enqueueRetryForTest(t, s.statusTracker, key, slackServer.URL, "slack", "api", "Example Corp", "temporary failure", 5, time.Hour, payload) {
		t.Fatal("EnqueueRetry() unexpectedly failed")
	}
	if err := s.statusTracker.SavePendingChanges(); err != nil {
		t.Fatalf("SavePendingChanges: %v", err)
	}

	s.checkAPIOnce(t.Context())

	if slackRequests != 1 {
		t.Fatalf("slack request count = %d, want 1", slackRequests)
	}
	if !s.statusTracker.IsAPIItemSentToWebhook(key, slackServer.URL) {
		t.Fatal("API retry item was not marked sent after fetch failure")
	}
	if items := s.statusTracker.GetRetryItemsByType("api"); len(items) != 0 {
		t.Fatalf("retry queue length = %d, want 0 after replay", len(items))
	}
}

func TestProcessAPIRetryQueueDeadLettersMissingPayload(t *testing.T) {
	s := newTestScheduler(t)
	webhookURL := "https://hooks.slack.com/services/T12345678/B12345678/testtoken1234567890"
	s.config.SlackWebhooks.Ransomware = config.WebhookConfig{Enabled: true, URL: webhookURL}
	if !enqueueRetryForTest(t, s.statusTracker, "api-item", webhookURL, "slack", "api", "Missing Payload", "temporary failure", 5, time.Hour) {
		t.Fatal("EnqueueRetry() unexpectedly failed")
	}

	s.processAPIRetryQueue(t.Context(), nil)

	if items := s.statusTracker.GetRetryItemsByType("api"); len(items) != 0 {
		t.Fatalf("retry queue length = %d, want 0", len(items))
	}
	if !s.statusTracker.IsRetryDeadLettered("api-item", "slack", "api") {
		t.Fatal("missing-payload API retry item was not dead-lettered")
	}
	reloaded, err := status.NewTracker(s.config.DataDir)
	if err != nil {
		t.Fatalf("NewTracker(reload) error = %v", err)
	}
	if !reloaded.IsRetryDeadLettered("api-item", "slack", "api") {
		t.Fatal("missing-payload API retry dead-letter was not persisted")
	}
}

func TestProcessAPIRetryQueueDeadLettersMalformedPayload(t *testing.T) {
	s := newTestScheduler(t)
	webhookURL := "https://hooks.slack.com/services/T12345678/B12345678/testtoken1234567890"
	s.config.SlackWebhooks.Ransomware = config.WebhookConfig{Enabled: true, URL: webhookURL}
	if !enqueueRetryForTest(t, s.statusTracker, "api-item", webhookURL, "slack", "api", "Malformed Payload", "temporary failure", 5, time.Hour, []byte("{not json")) {
		t.Fatal("EnqueueRetry() unexpectedly failed")
	}

	s.processAPIRetryQueue(t.Context(), nil)

	if items := s.statusTracker.GetRetryItemsByType("api"); len(items) != 0 {
		t.Fatalf("retry queue length = %d, want 0", len(items))
	}
	if !s.statusTracker.IsRetryDeadLettered("api-item", "slack", "api") {
		t.Fatal("malformed-payload API retry item was not dead-lettered")
	}
}

func TestProcessAPIRetryQueueSkipsDisabledWebhook(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()

	s := newTestScheduler(t)
	webhookURL := "https://hooks.slack.com/services/T12345678/B12345678/testtoken1234567890"
	entry := enqueueAPIRetryPayloadForTest(t, s, webhookURL)

	s.processAPIRetryQueue(t.Context(), nil)

	if items := s.statusTracker.GetRetryItemsByType("api"); len(items) != 1 {
		t.Fatalf("retry queue length = %d, want 1 for disabled webhook", len(items))
	}
	if s.statusTracker.IsAPIItemSentToWebhook(api.GenerateEntryKey(entry), webhookURL) {
		t.Fatal("disabled webhook retry item was marked sent")
	}
	assertAPIRetrySkipLog(t, hook, "destination_disabled")
}

func TestProcessAPIRetryQueueSkipsActiveQuietHours(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()

	s := newTestScheduler(t)
	webhookURL := "https://hooks.slack.com/services/T12345678/B12345678/testtoken1234567890"
	s.config.SlackWebhooks.Ransomware = config.WebhookConfig{
		Enabled: true,
		URL:     webhookURL,
		QuietHours: &config.QuietHours{
			Enabled:  true,
			Start:    "00:00",
			End:      "23:59",
			Timezone: "UTC",
		},
	}
	enqueueAPIRetryPayloadForTest(t, s, webhookURL)

	s.processAPIRetryQueue(t.Context(), nil)

	if items := s.statusTracker.GetRetryItemsByType("api"); len(items) != 1 {
		t.Fatalf("retry queue length = %d, want 1 during quiet hours", len(items))
	}
	assertAPIRetrySkipLog(t, hook, "quiet_hours")
}

func TestProcessAPIRetryQueueRemovesAlreadySentItem(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()

	s := newTestScheduler(t)
	webhookURL := "https://hooks.slack.com/services/T12345678/B12345678/testtoken1234567890"
	s.config.SlackWebhooks.Ransomware = config.WebhookConfig{Enabled: true, URL: webhookURL}
	entry := enqueueAPIRetryPayloadForTest(t, s, webhookURL)
	key := api.GenerateEntryKey(entry)
	s.statusTracker.MarkAPIItemSentToWebhook(key, "already sent", webhookURL)

	s.processAPIRetryQueue(t.Context(), nil)

	if items := s.statusTracker.GetRetryItemsByType("api"); len(items) != 0 {
		t.Fatalf("retry queue length = %d, want 0 for already-sent item", len(items))
	}
	assertAPIRetrySkipLog(t, hook, "already_sent")
}

func TestProcessAPIRetryQueueSkipsItemStillInAPIWindow(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()

	s := newTestScheduler(t)
	webhookURL := "https://hooks.slack.com/services/T12345678/B12345678/testtoken1234567890"
	s.config.SlackWebhooks.Ransomware = config.WebhookConfig{Enabled: true, URL: webhookURL}
	entry := enqueueAPIRetryPayloadForTest(t, s, webhookURL)

	s.processAPIRetryQueue(t.Context(), []api.RansomwareEntry{entry})

	if items := s.statusTracker.GetRetryItemsByType("api"); len(items) != 1 {
		t.Fatalf("retry queue length = %d, want 1 for in-window item", len(items))
	}
	assertAPIRetrySkipLog(t, hook, "in_api_window")
}

func TestProcessAPIRetryQueueKeepsInWindowItemFilteredByCurrentConfig(t *testing.T) {
	s := newTestScheduler(t)
	webhookURL := "https://hooks.slack.com/services/T12345678/B12345678/testtoken1234567890"
	s.config.SlackWebhooks.Ransomware = config.WebhookConfig{
		Enabled: true,
		URL:     webhookURL,
		Filters: &config.WebhookFilters{
			IncludeCountries: []string{"FR"},
		},
	}
	entry := enqueueAPIRetryPayloadForTest(t, s, webhookURL)

	s.processAPIRetryQueue(t.Context(), []api.RansomwareEntry{entry})

	if items := s.statusTracker.GetRetryItemsByType("api"); len(items) != 1 {
		t.Fatalf("retry queue length = %d, want 1 for in-window filtered item", len(items))
	}
}

func TestProcessAPIRetryQueueKeepsItemFilteredByCurrentConfig(t *testing.T) {
	s := newTestScheduler(t)
	webhookURL := "https://hooks.slack.com/services/T12345678/B12345678/testtoken1234567890"
	s.config.SlackWebhooks.Ransomware = config.WebhookConfig{
		Enabled: true,
		URL:     webhookURL,
		Filters: &config.WebhookFilters{
			IncludeCountries: []string{"FR"},
		},
	}
	enqueueAPIRetryPayloadForTest(t, s, webhookURL)

	s.processAPIRetryQueue(t.Context(), nil)

	if items := s.statusTracker.GetRetryItemsByType("api"); len(items) != 1 {
		t.Fatalf("retry queue length = %d, want 1 for filtered item", len(items))
	}
}

func TestProcessAPIRetryQueueRemovesOriginalEntryAfterWebhookURLChange(t *testing.T) {
	s := newTestScheduler(t)
	oldWebhookURL := "https://hooks.slack.com/services/T12345678/B12345678/oldtoken1234567890"
	entry := enqueueAPIRetryPayloadForTest(t, s, oldWebhookURL)

	var slackRequests int
	slackServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slackRequests++
		w.WriteHeader(http.StatusOK)
	}))
	defer slackServer.Close()
	s.config.SlackWebhooks.Ransomware = config.WebhookConfig{Enabled: true, URL: slackServer.URL}

	s.processAPIRetryQueue(t.Context(), nil)

	if slackRequests != 1 {
		t.Fatalf("slack request count = %d, want 1", slackRequests)
	}
	if !s.statusTracker.IsAPIItemSentToWebhook(api.GenerateEntryKey(entry), slackServer.URL) {
		t.Fatal("rotated webhook retry item was not marked sent to current webhook")
	}
	if items := s.statusTracker.GetRetryItemsByType("api"); len(items) != 0 {
		t.Fatalf("retry queue length = %d, want 0 after rotated webhook send", len(items))
	}
}

func TestProcessAPIRetryQueueReplaysPersistedAPIRetryAfterRestart(t *testing.T) {
	dataDir := t.TempDir()
	entry := api.RansomwareEntry{
		ID:      "persisted-retry-1",
		Group:   "lockbit",
		Victim:  "Persisted Replay Corp",
		Country: "DE",
	}
	key := api.GenerateEntryKey(entry)
	payload, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("Marshal(entry) error = %v", err)
	}

	firstTracker, err := status.NewTracker(dataDir)
	if err != nil {
		t.Fatalf("NewTracker(first) error = %v", err)
	}
	if !enqueueRetryForTest(t, firstTracker, key, "slack.ransomware", "slack", "api", api.DisplayRansomwareTitle(entry), "temporary failure", 5, time.Hour, payload) {
		t.Fatal("EnqueueRetryForDestination() unexpectedly failed")
	}
	if err := firstTracker.SavePendingChanges(); err != nil {
		t.Fatalf("SavePendingChanges(first) error = %v", err)
	}

	reloadedTracker, err := status.NewTracker(dataDir)
	if err != nil {
		t.Fatalf("NewTracker(reloaded) error = %v", err)
	}
	s := newTestScheduler(t)
	s.config.DataDir = dataDir
	s.statusTracker = reloadedTracker

	var requestBodies []string
	slackServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("ReadAll(slack body) error = %v", err)
		}
		requestBodies = append(requestBodies, string(body))
		w.WriteHeader(http.StatusOK)
	}))
	defer slackServer.Close()
	s.config.SlackWebhooks.Ransomware = config.WebhookConfig{Enabled: true, URL: slackServer.URL}

	s.processAPIRetryQueue(t.Context(), nil)

	if len(requestBodies) != 1 {
		t.Fatalf("Slack replay request count = %d, want 1", len(requestBodies))
	}
	if !strings.Contains(requestBodies[0], entry.Victim) {
		t.Fatalf("Slack replay body missing victim %q: %s", entry.Victim, requestBodies[0])
	}
	if !s.statusTracker.IsAPIItemSentToDestination(key, "slack.ransomware", slackServer.URL) {
		t.Fatal("replayed API retry item was not marked sent")
	}
	if items := s.statusTracker.GetRetryItemsByType("api"); len(items) != 0 {
		t.Fatalf("retry queue length = %d, want 0 after persisted replay", len(items))
	}

	afterReplay, err := status.NewTracker(dataDir)
	if err != nil {
		t.Fatalf("NewTracker(after replay) error = %v", err)
	}
	if !afterReplay.IsAPIItemSentToDestination(key, "slack.ransomware", slackServer.URL) {
		t.Fatal("replayed sent marker was not persisted")
	}
	if items := afterReplay.GetRetryItemsByType("api"); len(items) != 0 {
		t.Fatalf("persisted retry queue length = %d, want 0 after replay", len(items))
	}
}

func enqueueAPIRetryPayloadForTest(t *testing.T, s *Scheduler, webhookURL string) api.RansomwareEntry {
	t.Helper()
	entry := api.RansomwareEntry{
		ID:      "victim-1",
		Group:   "lockbit",
		Victim:  "Example Corp",
		Country: "DE",
	}
	payload, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("Marshal(entry) error = %v", err)
	}
	if !enqueueRetryForTest(t, s.statusTracker, api.GenerateEntryKey(entry), webhookURL, "slack", "api", "Example Corp", "temporary failure", 5, time.Hour, payload) {
		t.Fatal("EnqueueRetry() unexpectedly failed")
	}
	return entry
}

func assertAPIRetrySkipLog(t *testing.T, hook *logtest.Hook, skipReason string) {
	t.Helper()
	for _, entry := range hook.AllEntries() {
		if entry.Data["skip_reason"] != skipReason {
			continue
		}
		for _, field := range []string{"item_key", "messenger", "retry_count", "queue_key"} {
			if entry.Data[field] == nil || entry.Data[field] == "" {
				t.Fatalf("API retry skip log for %q missing %s: fields=%#v", skipReason, field, entry.Data)
			}
		}
		return
	}
	t.Fatalf("missing API retry skip log with skip_reason %q; entries=%#v", skipReason, hook.AllEntries())
}
