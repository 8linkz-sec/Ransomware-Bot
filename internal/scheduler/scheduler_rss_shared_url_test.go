package scheduler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/config"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/rss"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/status"
)

// rssSharedURLFeedItemXML builds a one-item RSS feed for the shared-webhook-URL
// tests below. published must be recent (see the tests) so rss_max_item_age
// (when enabled) never makes the reproduction meaningless.
func rssSharedURLFeedItemXML(title, guid, link, category string, published time.Time) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
  <channel>
    <title>Shared URL Test Feed</title>
    <link>https://example.test/</link>
    <item>
      <title>%s</title>
      <link>%s</link>
      <guid>%s</guid>
      <description>Shared webhook URL RSS body</description>
      <pubDate>%s</pubDate>
      <category>%s</category>
    </item>
  </channel>
</rss>`, title, link, guid, published.Format(time.RFC1123Z), category)
}

func countingWebhookServerForTest(t *testing.T) (*httptest.Server, func() int) {
	t.Helper()
	var mu sync.Mutex
	count := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		count++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	return server, func() int {
		mu.Lock()
		defer mu.Unlock()
		return count
	}
}

// TestCheckRSSOnceDeliversToBothTargetsSharingWebhookURL covers: one
// webhook block, base URL plus a targets[] entry pointing at the same URL, both
// filters allow the entry. Target 2 is quiet-hours-deferred in cycle 1 so the
// two endpoints are separated in time on purpose (the fresh path fans targets
// out in parallel goroutines, so a single-cycle two-target test would pass even
// on the broken tree). RED today: POST count stays 1 after cycle 2, and the
// phantom marker for slack.rss.general.2 carries target 1's SentAt to the
// microsecond instead of a real, later delivery.
func TestCheckRSSOnceDeliversToBothTargetsSharingWebhookURL(t *testing.T) {
	s := newTestScheduler(t)

	title := "Probe Entry D"
	guid := "probe-entry-d"
	link := "https://example.test/articles/probe-entry-d"
	published := time.Now().Add(-time.Hour)

	feedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = fmt.Fprint(w, rssSharedURLFeedItemXML(title, guid, link, "security", published))
	}))
	defer feedServer.Close()
	allowLocalRSSFeedsForTest(t, s.rssParser, feedServer.Client())

	webhookServer, postCount := countingWebhookServerForTest(t)
	defer webhookServer.Close()
	sharedURL := webhookServer.URL

	s.config.Feeds.GeneralFeeds = []string{feedServer.URL}
	s.config.SlackWebhooks.RSS = config.WebhookConfig{
		Enabled: true,
		URL:     sharedURL,
		Filters: &config.WebhookFilters{IncludeCategories: []string{"security"}},
		Targets: []config.WebhookTarget{{
			URL:        sharedURL,
			Filters:    &config.WebhookFilters{IncludeKeywords: []string{"Probe"}},
			QuietHours: activeQuietHoursUTCForTest(),
		}},
	}
	s.config.MaxRSSWorkers = 1
	s.feedTypeMap = buildFeedTypeMap(s.config)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	key := rss.GenerateEntryKey(feedServer.URL, guid, link, title)

	s.checkRSSOnce(ctx)
	if got := postCount(); got != 1 {
		t.Fatalf("POST count after cycle 1 = %d, want 1 (target 2 deferred by quiet hours)", got)
	}
	// Captured before cycle 2 so the same-URL owner-handoff refresh that
	// cycle 2 performs on target 1's own marker (alias
	// ownership fix: a second, distinct destination superseding a shared
	// legacy alias refreshes the superseded owner's own primary SentAt)
	// cannot mask the ordering check below: the baseline is target 1's real
	// cycle-1 delivery time, not whatever cycle 2 leaves it at.
	firstSnapshotAfterCycle1, ok := s.statusTracker.RSSSentItemSnapshot(key, "slack.rss.general")
	if !ok {
		t.Fatal("RSSSentItemSnapshot(slack.rss.general) missing sent marker after cycle 1")
	}

	s.config.SlackWebhooks.RSS.Targets[0].QuietHours = nil
	s.checkRSSOnce(ctx)
	if got := postCount(); got != 2 {
		t.Fatalf("POST count after cycle 2 = %d, want 2 for two targets sharing one webhook URL", got)
	}

	secondSnapshot, ok := s.statusTracker.RSSSentItemSnapshot(key, "slack.rss.general.2")
	if !ok {
		t.Fatal("RSSSentItemSnapshot(slack.rss.general.2) missing sent marker")
	}
	if secondSnapshot.DestinationID != "slack.rss.general.2" {
		t.Fatalf("second target marker DestinationID = %q, want slack.rss.general.2", secondSnapshot.DestinationID)
	}
	if !secondSnapshot.SentAt.After(firstSnapshotAfterCycle1.SentAt) {
		t.Fatalf("second target SentAt = %v, want strictly after first target's real cycle-1 SentAt %v (a real, later delivery, not the copied phantom)",
			secondSnapshot.SentAt, firstSnapshotAfterCycle1.SentAt)
	}

	events := deliveryAuditEventsForTest(t, s.config.DataDir)
	delivered := false
	for _, event := range events {
		if event.ItemKey == key && event.DestinationID == "slack.rss.general.2" && event.Outcome == status.DeliveryAuditOutcomeDelivered {
			delivered = true
			break
		}
	}
	if !delivered {
		t.Fatalf("no delivered audit event for slack.rss.general.2; events = %+v", events)
	}
}

// TestCheckRSSOnceSkipMarkerDoesNotSuppressSiblingSharingWebhookURL is probe E
// from the plan §3a-2: target 1's filter rejects the entry (a skip marker, not a
// delivery), target 2's filter allows it. Target 2 is quiet-hours-deferred in
// cycle 1 for the same determinism reason as probe D. RED today: POST count
// stays 0 after cycle 2 and the sibling marker reads Skipped: true — target 2
// never evaluated its own filter, it adopted target 1's rejection.
func TestCheckRSSOnceSkipMarkerDoesNotSuppressSiblingSharingWebhookURL(t *testing.T) {
	s := newTestScheduler(t)

	title := "Probe Entry E"
	guid := "probe-entry-e"
	link := "https://example.test/articles/probe-entry-e"
	published := time.Now().Add(-time.Hour)

	feedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = fmt.Fprint(w, rssSharedURLFeedItemXML(title, guid, link, "security", published))
	}))
	defer feedServer.Close()
	allowLocalRSSFeedsForTest(t, s.rssParser, feedServer.Client())

	webhookServer, postCount := countingWebhookServerForTest(t)
	defer webhookServer.Close()
	sharedURL := webhookServer.URL

	s.config.Feeds.GeneralFeeds = []string{feedServer.URL}
	s.config.SlackWebhooks.RSS = config.WebhookConfig{
		Enabled: true,
		URL:     sharedURL,
		Filters: &config.WebhookFilters{IncludeCategories: []string{"finance"}},
		Targets: []config.WebhookTarget{{
			URL:        sharedURL,
			Filters:    &config.WebhookFilters{IncludeCategories: []string{"security"}},
			QuietHours: activeQuietHoursUTCForTest(),
		}},
	}
	s.config.MaxRSSWorkers = 1
	s.feedTypeMap = buildFeedTypeMap(s.config)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	s.checkRSSOnce(ctx)
	if got := postCount(); got != 0 {
		t.Fatalf("POST count after cycle 1 = %d, want 0 (target 1 filters the entry out, target 2 deferred)", got)
	}

	s.config.SlackWebhooks.RSS.Targets[0].QuietHours = nil
	s.checkRSSOnce(ctx)
	if got := postCount(); got != 1 {
		t.Fatalf("POST count after cycle 2 = %d, want 1 (only target 2 delivers)", got)
	}

	key := rss.GenerateEntryKey(feedServer.URL, guid, link, title)
	firstSnapshot, ok := s.statusTracker.RSSSentItemSnapshot(key, "slack.rss.general")
	if !ok {
		t.Fatal("RSSSentItemSnapshot(slack.rss.general) missing skip marker")
	}
	if !firstSnapshot.Skipped {
		t.Fatalf("first target snapshot Skipped = %v, want true (filter mismatch)", firstSnapshot.Skipped)
	}
	secondSnapshot, ok := s.statusTracker.RSSSentItemSnapshot(key, "slack.rss.general.2")
	if !ok {
		t.Fatal("RSSSentItemSnapshot(slack.rss.general.2) missing sent marker")
	}
	if secondSnapshot.Skipped {
		t.Fatalf("second target snapshot Skipped = %v, want false (it delivered)", secondSnapshot.Skipped)
	}
	if secondSnapshot.Title == "" {
		t.Fatal("second target snapshot has no title; want a real delivery marker, not an adopted skip")
	}
}

// TestCheckRSSOnceDeliversSameStoryToRSSAndGovernmentSharingWebhookURL is probe
// C from the plan §3a-3: a general feed and a government feed carry the same
// story (identical title/pubDate/description => one v3 content signature),
// and slack_webhooks.rss.url == slack_webhooks.government.url. General and
// government are different feed types, processed in their own sequential batch
// (processRSSFeedBatches), so this is deterministic in one checkRSSOnce call
// without any quiet-hours separation. RED today: POST count = 1 and the
// government audit event reads deduplicated/already_sent — it adopted the
// general delivery's content-signature alias across feed types, which the
// shared-URL WARN (grouped by webhook kind + URL) does not cover.
func TestCheckRSSOnceDeliversSameStoryToRSSAndGovernmentSharingWebhookURL(t *testing.T) {
	s := newTestScheduler(t)

	title := "Probe Entry C"
	published := time.Now().Add(-time.Hour)
	generalGUID := "probe-entry-c-general"
	generalLink := "https://example.test/articles/probe-entry-c-general"
	governmentGUID := "probe-entry-c-government"
	governmentLink := "https://example.test/articles/probe-entry-c-government"

	generalFeedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = fmt.Fprint(w, rssSharedURLFeedItemXML(title, generalGUID, generalLink, "security", published))
	}))
	defer generalFeedServer.Close()
	governmentFeedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = fmt.Fprint(w, rssSharedURLFeedItemXML(title, governmentGUID, governmentLink, "security", published))
	}))
	defer governmentFeedServer.Close()
	allowLocalRSSFeedsForTest(t, s.rssParser, generalFeedServer.Client())

	webhookServer, postCount := countingWebhookServerForTest(t)
	defer webhookServer.Close()
	sharedURL := webhookServer.URL

	s.config.Feeds.GeneralFeeds = []string{generalFeedServer.URL}
	s.config.Feeds.GovernmentFeeds = []string{governmentFeedServer.URL}
	s.config.SlackWebhooks.RSS = config.WebhookConfig{Enabled: true, URL: sharedURL}
	s.config.SlackWebhooks.Government = config.WebhookConfig{Enabled: true, URL: sharedURL}
	s.config.MaxRSSWorkers = 1
	s.feedTypeMap = buildFeedTypeMap(s.config)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	s.checkRSSOnce(ctx)

	if got := postCount(); got != 2 {
		t.Fatalf("POST count = %d, want 2 (matching story on two feed types sharing one webhook URL)", got)
	}

	events := deliveryAuditEventsForTest(t, s.config.DataDir)
	generalDelivered, governmentDelivered := false, false
	for _, event := range events {
		if event.Outcome != status.DeliveryAuditOutcomeDelivered {
			continue
		}
		switch event.DestinationID {
		case "slack.rss.general":
			generalDelivered = true
		case "slack.rss.government":
			governmentDelivered = true
		}
	}
	if !generalDelivered || !governmentDelivered {
		t.Fatalf("expected delivered audit events for both slack.rss.general and slack.rss.government; events = %+v", events)
	}
}
