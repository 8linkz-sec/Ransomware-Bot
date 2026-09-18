package scheduler

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/config"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/model"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/rss"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/status"
)

// auditFileLineCountForTest counts raw lines in delivery_audit.jsonl,
// deliberately not decoding events -- the volume tests must assert line
// counts, not outcomes, since asserting the outcome is exactly what let the
// original quiet-hours audit tests miss a 10,000-line cycle.
func auditFileLineCountForTest(t *testing.T, dataDir string) int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dataDir, "delivery_audit.jsonl"))
	if err != nil {
		t.Fatalf("ReadFile(delivery_audit.jsonl) error = %v", err)
	}
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return 0
	}
	return len(strings.Split(trimmed, "\n"))
}

func countQuietHoursAuditEvents(events []status.DeliveryAuditEvent) (perItem int, summary int, summaryEvent status.DeliveryAuditEvent) {
	for _, e := range events {
		if e.Outcome != status.DeliveryAuditOutcomeQuietHours {
			continue
		}
		switch e.Reason {
		case status.DeliveryAuditReasonQuietHours:
			perItem++
		case status.DeliveryAuditReasonQuietHoursCycleCapped:
			summary++
			summaryEvent = e
		}
	}
	return perItem, summary, summaryEvent
}

func TestRecordRSSQuietHoursDeferredItemsCapsAuditAtCycleLimit(t *testing.T) {
	s := newTestScheduler(t)
	target := webhookTarget{destinationID: "slack.rss.general", messenger: "slack"}

	items := make([]status.UnsentRSSItem, 250)
	for i := range items {
		items[i] = status.UnsentRSSItem{
			Key:     fmt.Sprintf("rss-cap-item-%d", i),
			FeedURL: "https://example.test/feed.xml",
			Title:   fmt.Sprintf("item %d", i),
		}
	}

	s.recordRSSQuietHoursDeferredItems(items, config.FeedTypeGeneral, target, rssDeliveryModeRecovery, 100)

	events := deliveryAuditEventsForTest(t, s.config.DataDir)
	perItem, summaryCount, summary := countQuietHoursAuditEvents(events)
	if perItem != 100 {
		t.Fatalf("per-item quiet_hours events = %d, want 100", perItem)
	}
	if summaryCount != 1 {
		t.Fatalf("summary events = %d, want 1", summaryCount)
	}
	if summary.Count != 150 {
		t.Fatalf("summary Count = %d, want 150", summary.Count)
	}
	if summary.Details["deferred_total"] != "250" {
		t.Fatalf("summary deferred_total = %q, want %q", summary.Details["deferred_total"], "250")
	}
	if summary.Details["audit_cap"] != "100" {
		t.Fatalf("summary audit_cap = %q, want %q", summary.Details["audit_cap"], "100")
	}
}

func TestRecordRSSQuietHoursDeferredEntriesCapsAuditAtCycleLimit(t *testing.T) {
	s := newTestScheduler(t)
	target := webhookTarget{destinationID: "slack.rss.general", messenger: "slack"}

	entries := make([]model.RSSEntry, 2500)
	for i := range entries {
		entries[i] = model.RSSEntry{
			Title:   fmt.Sprintf("entry %d", i),
			Link:    fmt.Sprintf("https://example.test/articles/%d", i),
			GUID:    fmt.Sprintf("entry-guid-%d", i),
			FeedURL: "https://example.test/feed.xml",
		}
	}

	s.recordRSSQuietHoursDeferredEntries(entries, config.FeedTypeGeneral, target, rssDeliveryModeFresh, 100)

	events := deliveryAuditEventsForTest(t, s.config.DataDir)
	perItem, summaryCount, summary := countQuietHoursAuditEvents(events)
	if perItem != 100 {
		t.Fatalf("per-item quiet_hours events = %d, want 100", perItem)
	}
	if summaryCount != 1 {
		t.Fatalf("summary events = %d, want 1", summaryCount)
	}
	if summary.Count != 2400 {
		t.Fatalf("summary Count = %d, want 2400", summary.Count)
	}
	if summary.Details["deferred_total"] != "2500" {
		t.Fatalf("summary deferred_total = %q, want %q", summary.Details["deferred_total"], "2500")
	}
	if summary.Details["audit_cap"] != "100" {
		t.Fatalf("summary audit_cap = %q, want %q", summary.Details["audit_cap"], "100")
	}
}

func TestRecordRSSQuietHoursDeferredItemsNoSummaryLineWhenUnderCap(t *testing.T) {
	s := newTestScheduler(t)
	target := webhookTarget{destinationID: "slack.rss.general", messenger: "slack"}

	items := make([]status.UnsentRSSItem, 5)
	for i := range items {
		items[i] = status.UnsentRSSItem{Key: fmt.Sprintf("under-cap-%d", i), FeedURL: "https://example.test/feed.xml"}
	}

	s.recordRSSQuietHoursDeferredItems(items, config.FeedTypeGeneral, target, rssDeliveryModeRecovery, 100)

	events := deliveryAuditEventsForTest(t, s.config.DataDir)
	perItem, summaryCount, _ := countQuietHoursAuditEvents(events)
	if perItem != 5 {
		t.Fatalf("per-item quiet_hours events = %d, want 5", perItem)
	}
	if summaryCount != 0 {
		t.Fatalf("summary events = %d, want 0 (must not regress the common case into always emitting a summary)", summaryCount)
	}
}

func TestRecordRSSQuietHoursDeferredItemsZeroOrNegativeLimitDoesNotCap(t *testing.T) {
	for _, limit := range []int{0, -1} {
		t.Run(fmt.Sprintf("limit=%d", limit), func(t *testing.T) {
			s := newTestScheduler(t)
			target := webhookTarget{destinationID: "slack.rss.general", messenger: "slack"}
			items := make([]status.UnsentRSSItem, 150)
			for i := range items {
				items[i] = status.UnsentRSSItem{Key: fmt.Sprintf("no-cap-%d", i), FeedURL: "https://example.test/feed.xml"}
			}

			s.recordRSSQuietHoursDeferredItems(items, config.FeedTypeGeneral, target, rssDeliveryModeRecovery, limit)

			events := deliveryAuditEventsForTest(t, s.config.DataDir)
			perItem, summaryCount, _ := countQuietHoursAuditEvents(events)
			if perItem != 150 {
				t.Fatalf("per-item quiet_hours events = %d, want 150 (uncapped)", perItem)
			}
			if summaryCount != 0 {
				t.Fatalf("summary events = %d, want 0 when the cap is disabled", summaryCount)
			}
		})
	}
}

func TestSendUnsentRSSItemsQuietHoursAuditLineCountMatchesCapPlusSummary(t *testing.T) {
	s := newTestScheduler(t)
	s.config.RSSMaxEntriesPerCycle = 50

	feedURL := "https://example.test/feed.xml"
	s.config.Feeds.GeneralFeeds = []string{feedURL}
	s.feedTypeMap = buildFeedTypeMap(s.config)

	now := time.Now().UTC()
	s.config.SlackWebhooks.RSS = config.WebhookConfig{
		Enabled: true,
		URL:     "https://hooks.slack.test/services/quiet-hours-cap",
		QuietHours: &config.QuietHours{
			Enabled:  true,
			Start:    now.Add(-time.Hour).Format("15:04"),
			End:      now.Add(time.Hour).Format("15:04"),
			Timezone: "UTC",
		},
	}

	base := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	const seeded = 200
	for i := 0; i < seeded; i++ {
		item := status.StoredRSSEntry{
			Key:       fmt.Sprintf("quiet-cap-item-%d", i),
			FeedURL:   feedURL,
			FeedType:  config.FeedTypeGeneral,
			Title:     fmt.Sprintf("item %d", i),
			Link:      fmt.Sprintf("https://example.test/articles/%d", i),
			GUID:      fmt.Sprintf("quiet-cap-guid-%d", i),
			Published: base.Add(time.Duration(i) * time.Minute).Format("2006-01-02 15:04:05.999999"),
			FeedTitle: "Feed",
		}
		s.statusTracker.MarkRSSItemParsed(feedURL, item.Key, item)
	}
	if err := s.statusTracker.SavePendingChanges(); err != nil {
		t.Fatalf("SavePendingChanges: %v", err)
	}

	s.sendUnsentRSSItems(t.Context(), nil)

	lines := auditFileLineCountForTest(t, s.config.DataDir)
	if want := s.config.RSSMaxEntriesPerCycle + 1; lines != want {
		t.Fatalf("delivery_audit.jsonl line count = %d, want %d (cap + 1 summary)", lines, want)
	}
}

func TestSendParsedRSSToWebhooksQuietHoursAuditLineCountMatchesCapPlusSummary(t *testing.T) {
	s := newTestScheduler(t)
	s.config.RSSMaxEntriesPerCycle = 50

	feedURL := "https://example.test/feed.xml"
	s.config.Feeds.GeneralFeeds = []string{feedURL}
	s.feedTypeMap = buildFeedTypeMap(s.config)

	now := time.Now().UTC()
	s.config.SlackWebhooks.RSS = config.WebhookConfig{
		Enabled: true,
		URL:     "https://hooks.slack.test/services/quiet-hours-fresh-cap",
		QuietHours: &config.QuietHours{
			Enabled:  true,
			Start:    now.Add(-time.Hour).Format("15:04"),
			End:      now.Add(time.Hour).Format("15:04"),
			Timezone: "UTC",
		},
	}

	base := time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC)
	const seeded = 200
	entries := make([]model.RSSEntry, seeded)
	for i := 0; i < seeded; i++ {
		entries[i] = model.RSSEntry{
			Title:     fmt.Sprintf("fresh item %d", i),
			Link:      fmt.Sprintf("https://example.test/articles/fresh-%d", i),
			GUID:      fmt.Sprintf("quiet-cap-fresh-guid-%d", i),
			FeedURL:   feedURL,
			FeedTitle: "Feed",
			Published: base.Add(time.Duration(i) * time.Minute),
		}
	}

	feedResults := &rss.FeedResults{
		Entries:    map[string][]rss.Entry{feedURL: entries},
		FeedErrors: map[string]string{},
	}
	s.sendParsedRSSToWebhooks(t.Context(), feedResults, config.FeedTypeGeneral, s.getWebhookTargets(config.FeedTypeGeneral), nil)

	lines := auditFileLineCountForTest(t, s.config.DataDir)
	if want := s.config.RSSMaxEntriesPerCycle + 1; lines != want {
		t.Fatalf("delivery_audit.jsonl line count = %d, want %d (cap + 1 summary)", lines, want)
	}
}
