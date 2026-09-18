package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/api"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/config"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/discord"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/feedurl"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/filter"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/notifyfmt"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/rss"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/status"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/webhookhttp"

	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

type recordedRSSSend struct {
	webhookURL string
	entryTitle string
	feedType   string
}

func TestRecoverSchedulerPanicLogsComponentAndStack(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()

	func() {
		defer recoverSchedulerPanic("test component")
		panic("scheduler panic")
	}()

	entry := hook.LastEntry()
	if entry == nil {
		t.Fatal("expected recovered panic log entry")
	}
	if entry.Message != "Recovered panic in scheduler goroutine" {
		t.Fatalf("log message = %q", entry.Message)
	}
	if got := entry.Data["component"]; got != "test component" {
		t.Fatalf("component field = %#v", got)
	}
	if got := entry.Data["panic"]; got != "scheduler panic" {
		t.Fatalf("panic field = %#v", got)
	}
	stack, ok := entry.Data["stack"].(string)
	if !ok || !strings.Contains(stack, "goroutine") {
		t.Fatalf("stack field = %#v, want goroutine stack", entry.Data["stack"])
	}
}

type recordedAPISend struct {
	webhookURL string
	entryID    string
}

type recordingAPIClient struct {
	entries []api.RansomwareEntry
	err     error
	closed  bool
}

func (c *recordingAPIClient) GetLatestEntries(context.Context) ([]api.RansomwareEntry, error) {
	return c.entries, c.err
}

func (c *recordingAPIClient) Close() error {
	c.closed = true
	return nil
}

type recordingRSSParser struct {
	feedURLs []string
	results  *rss.FeedResults
	err      error
}

func (p *recordingRSSParser) ParseMultipleFeedsWithValidators(_ context.Context, feedURLs []string, _ int, _ map[string]rss.FeedHTTPValidators) (*rss.FeedResults, error) {
	p.feedURLs = append([]string(nil), feedURLs...)
	if p.results != nil || p.err != nil {
		return p.results, p.err
	}
	return &rss.FeedResults{
		Entries:         map[string][]rss.Entry{},
		FeedErrors:      map[string]string{},
		FeedValidators:  map[string]rss.FeedHTTPValidators{},
		FeedNotModified: map[string]bool{},
	}, nil
}

type recordingConfigReloader struct {
	cfg     *config.Config
	changed bool
	err     error
	applied bool
}

func (r *recordingConfigReloader) Interval() time.Duration {
	return time.Hour
}

func (r *recordingConfigReloader) Check() (*config.Config, bool, error) {
	return r.cfg, r.changed, r.err
}

func (r *recordingConfigReloader) Reload() (*config.Config, error) {
	return r.cfg, r.err
}

func (r *recordingConfigReloader) MarkApplied() {
	r.applied = true
}

type recordingWebhookSender struct {
	mu       sync.Mutex
	apiErr   error
	apiCalls []recordedAPISend
	rssErr   error
	rssCalls []recordedRSSSend
}

func (s *recordingWebhookSender) SendRansomwareEntry(_ context.Context, webhookURL string, entry api.RansomwareEntry, _ *notifyfmt.FormatOptions) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.apiCalls = append(s.apiCalls, recordedAPISend{
		webhookURL: webhookURL,
		entryID:    entry.ID,
	})
	return s.apiErr
}

func (s *recordingWebhookSender) SendRSSEntry(_ context.Context, webhookURL string, entry rss.Entry, feedType string, _ *notifyfmt.FormatOptions) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rssCalls = append(s.rssCalls, recordedRSSSend{
		webhookURL: webhookURL,
		entryTitle: entry.Title,
		feedType:   feedType,
	})
	return s.rssErr
}

func (s *recordingWebhookSender) Close() error {
	return nil
}

func (s *recordingWebhookSender) apiCallCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.apiCalls)
}

func (s *recordingWebhookSender) apiCallsSnapshot() []recordedAPISend {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]recordedAPISend(nil), s.apiCalls...)
}

func (s *recordingWebhookSender) rssCallCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.rssCalls)
}

func (s *recordingWebhookSender) rssCallsSnapshot() []recordedRSSSend {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]recordedRSSSend(nil), s.rssCalls...)
}

type blockingRSSWebhookSender struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func newBlockingRSSWebhookSender() *blockingRSSWebhookSender {
	return &blockingRSSWebhookSender{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (s *blockingRSSWebhookSender) SendRansomwareEntry(context.Context, string, api.RansomwareEntry, *notifyfmt.FormatOptions) error {
	return nil
}

func (s *blockingRSSWebhookSender) SendRSSEntry(ctx context.Context, _ string, _ rss.Entry, _ string, _ *notifyfmt.FormatOptions) error {
	s.once.Do(func() {
		close(s.started)
	})
	select {
	case <-s.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *blockingRSSWebhookSender) Close() error {
	return nil
}

type blockingAPIWebhookSender struct {
	started     chan struct{}
	release     chan struct{}
	once        sync.Once
	releaseOnce sync.Once
}

func newBlockingAPIWebhookSender() *blockingAPIWebhookSender {
	return &blockingAPIWebhookSender{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (s *blockingAPIWebhookSender) SendRansomwareEntry(ctx context.Context, _ string, _ api.RansomwareEntry, _ *notifyfmt.FormatOptions) error {
	s.once.Do(func() {
		close(s.started)
	})
	select {
	case <-s.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *blockingAPIWebhookSender) SendRSSEntry(context.Context, string, rss.Entry, string, *notifyfmt.FormatOptions) error {
	return nil
}

func (s *blockingAPIWebhookSender) Close() error {
	return nil
}

func (s *blockingAPIWebhookSender) releaseSends() {
	s.releaseOnce.Do(func() {
		close(s.release)
	})
}

type notifyingRSSWebhookSender struct {
	started chan struct{}
	once    sync.Once
}

func newNotifyingRSSWebhookSender() *notifyingRSSWebhookSender {
	return &notifyingRSSWebhookSender{started: make(chan struct{})}
}

func (s *notifyingRSSWebhookSender) SendRansomwareEntry(context.Context, string, api.RansomwareEntry, *notifyfmt.FormatOptions) error {
	return nil
}

func (s *notifyingRSSWebhookSender) SendRSSEntry(context.Context, string, rss.Entry, string, *notifyfmt.FormatOptions) error {
	s.once.Do(func() {
		close(s.started)
	})
	return nil
}

func (s *notifyingRSSWebhookSender) Close() error {
	return nil
}

func hasRecordedRSSSend(calls []recordedRSSSend, webhookURL, entryTitle, feedType string) bool {
	for _, call := range calls {
		if call.webhookURL == webhookURL && call.entryTitle == entryTitle && call.feedType == feedType {
			return true
		}
	}
	return false
}

func TestBuildFeedTypeMap(t *testing.T) {
	cfg := &config.Config{
		Feeds: config.FeedConfig{
			GeneralFeeds:    []string{"https://feed1.com/rss", "https://feed2.com/rss"},
			GovernmentFeeds: []string{"https://gov.com/feed"},
			RansomwareFeeds: []string{"https://ransom.com/feed"},
		},
	}

	feedTypeMap := buildFeedTypeMap(cfg)

	// Check general feeds
	if feedTypeMap["https://feed1.com/rss"] != "general" {
		t.Errorf("expected 'general', got %q", feedTypeMap["https://feed1.com/rss"])
	}
	if feedTypeMap["https://feed2.com/rss"] != "general" {
		t.Errorf("expected 'general', got %q", feedTypeMap["https://feed2.com/rss"])
	}

	// Check government feeds
	if feedTypeMap["https://gov.com/feed"] != "government" {
		t.Errorf("expected 'government', got %q", feedTypeMap["https://gov.com/feed"])
	}

	// Check ransomware feeds
	if feedTypeMap["https://ransom.com/feed"] != "ransomware" {
		t.Errorf("expected 'ransomware', got %q", feedTypeMap["https://ransom.com/feed"])
	}

	// Check unknown URL
	if feedTypeMap["https://unknown.com/feed"] != "" {
		t.Errorf("expected empty string for unknown URL, got %q", feedTypeMap["https://unknown.com/feed"])
	}

	// Check total count
	if len(feedTypeMap) != 4 {
		t.Errorf("expected 4 entries, got %d", len(feedTypeMap))
	}
}

func TestBuildFeedTypeMapEmpty(t *testing.T) {
	cfg := &config.Config{
		Feeds: config.FeedConfig{},
	}

	feedTypeMap := buildFeedTypeMap(cfg)

	if len(feedTypeMap) != 0 {
		t.Errorf("expected empty map, got %d entries", len(feedTypeMap))
	}
}

func TestRSSFeedRoutesCentralizeFeedRouting(t *testing.T) {
	cfg := &config.Config{
		Feeds: config.FeedConfig{
			GeneralFeeds:    []string{"https://general.test/feed.xml"},
			GovernmentFeeds: []string{"https://gov.test/feed.xml"},
			RansomwareFeeds: []string{"https://ransom.test/feed.xml"},
		},
	}

	routes := rssFeedRoutes(cfg)
	if len(routes) != 3 {
		t.Fatalf("len(rssFeedRoutes) = %d, want 3", len(routes))
	}

	want := map[string]struct {
		webhookType string
		url         string
	}{
		config.FeedTypeGeneral:    {webhookType: config.WebhookTypeRSS, url: "https://general.test/feed.xml"},
		config.FeedTypeGovernment: {webhookType: config.WebhookTypeGovernment, url: "https://gov.test/feed.xml"},
		config.FeedTypeRansomware: {webhookType: config.WebhookTypeRansomware, url: "https://ransom.test/feed.xml"},
	}

	for _, route := range routes {
		expected, ok := want[route.feedType]
		if !ok {
			t.Fatalf("unexpected route feed type %q", route.feedType)
		}
		if route.webhookType != expected.webhookType {
			t.Fatalf("route %s webhookType = %q, want %q", route.feedType, route.webhookType, expected.webhookType)
		}
		if len(route.feedURLs) != 1 || route.feedURLs[0] != expected.url {
			t.Fatalf("route %s feedURLs = %#v, want [%q]", route.feedType, route.feedURLs, expected.url)
		}
	}
}

func TestWebhookProgressFields(t *testing.T) {
	fields := webhookProgressFields(1, 4, "slack.rss")

	if fields["current"] != 2 {
		t.Fatalf("current = %v, want 2", fields["current"])
	}
	if fields["total"] != 4 {
		t.Fatalf("total = %v, want 4", fields["total"])
	}
	if fields["remaining"] != 2 {
		t.Fatalf("remaining = %v, want 2", fields["remaining"])
	}
	if fields["percent_complete"] != 50 {
		t.Fatalf("percent_complete = %v, want 50", fields["percent_complete"])
	}
	if fields["destination_id"] != "slack.rss" {
		t.Fatalf("destination_id = %v, want slack.rss", fields["destination_id"])
	}
}

func TestFormatLogTimestampIncludesTimezone(t *testing.T) {
	published := time.Date(2026, 6, 27, 12, 30, 0, 0, time.FixedZone("CEST", 2*60*60))

	got := formatLogTimestamp(published)
	if got != "2026-06-27T12:30:00+02:00" {
		t.Fatalf("formatLogTimestamp() = %q, want RFC3339 timestamp with offset", got)
	}
	if got := formatLogTimestamp(time.Time{}); got != "" {
		t.Fatalf("zero formatLogTimestamp() = %q, want empty", got)
	}
}

func TestLogAPIDataErrorSeparatesHTTPStatus(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()

	logAPIDataError(&api.HTTPStatusError{
		StatusCode: http.StatusServiceUnavailable,
		Status:     "503 Service Unavailable",
		Body:       "raw provider body should not be logged",
	}, log.Fields{"poll_type": pollTypeAPI, "run_id": "api-test-run"})

	if len(hook.Entries) != 1 {
		t.Fatalf("log entry count = %d, want 1", len(hook.Entries))
	}
	entry := hook.LastEntry()
	if entry.Data["status_code"] != http.StatusServiceUnavailable {
		t.Fatalf("status_code = %v, want 503", entry.Data["status_code"])
	}
	assertPollLogContext(t, entry, pollTypeAPI)
	if got := fmt.Sprint(entry.Data["error"]); strings.Contains(got, "raw provider body") {
		t.Fatalf("log error field leaked provider body: %q", got)
	}
}

func TestWebhookFailureFieldsExposeDiscordHTTPStatus(t *testing.T) {
	fields := webhookFailureFields("discord", &discord.WebhookHTTPError{
		StatusCode: http.StatusBadGateway,
		Status:     "502 Bad Gateway",
	}, log.Fields{"item_key": "item-1"})

	if fields["item_key"] != "item-1" {
		t.Fatalf("item_key = %v, want preserved base field", fields["item_key"])
	}
	if fields["status_code"] != http.StatusBadGateway {
		t.Fatalf("status_code = %v, want 502", fields["status_code"])
	}
	if got := fmt.Sprint(fields["error"]); !strings.Contains(got, "provider unavailable") {
		t.Fatalf("error = %q, want normalized provider guidance", got)
	}
}

func TestBulkSendAPIEntriesToDiscordLogsRetryJoinFields(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()

	s := newTestScheduler(t)
	s.config.RetryMaxAttempts = 3
	s.config.RetryWindow = time.Hour
	s.discordWebhookSender = &recordingWebhookSender{apiErr: errors.New("provider failed")}

	entry := api.RansomwareEntry{
		ID:     "victim-1",
		Group:  "lockbit",
		Victim: "Example Corp",
	}
	destinationID := "discord.ransomware"
	itemKey := api.GenerateEntryKey(entry)

	s.sendAPIEntriesIndividuallyToDiscord(
		t.Context(),
		[]api.RansomwareEntry{entry},
		"https://discord.com/api/webhooks/123456789012345678/test-token",
		destinationID,
	)

	var failureEntry *log.Entry
	for _, entry := range hook.AllEntries() {
		if entry.Message == "Failed to send ransomware entry to webhook" {
			failureEntry = entry
			break
		}
	}
	if failureEntry == nil {
		t.Fatal("missing Discord API send failure log entry")
	}
	if failureEntry.Data["item_key"] != itemKey {
		t.Fatalf("item_key = %v, want %q", failureEntry.Data["item_key"], itemKey)
	}
	if failureEntry.Data["destination_id"] != destinationID {
		t.Fatalf("destination_id = %v, want %q", failureEntry.Data["destination_id"], destinationID)
	}
	if failureEntry.Data["item_type"] != "api" {
		t.Fatalf("item_type = %v, want api", failureEntry.Data["item_type"])
	}

	retryItems := s.statusTracker.GetRetryItemsByType("api")
	if len(retryItems) != 1 {
		t.Fatalf("retry queue length = %d, want 1", len(retryItems))
	}
	if retryItems[0].ItemKey != itemKey {
		t.Fatalf("retry item key = %q, want %q", retryItems[0].ItemKey, itemKey)
	}
	if retryItems[0].DestinationID != destinationID {
		t.Fatalf("retry destination_id = %q, want %q", retryItems[0].DestinationID, destinationID)
	}
	if retryItems[0].Messenger != "discord" {
		t.Fatalf("retry messenger = %q, want discord", retryItems[0].Messenger)
	}
	if retryItems[0].ItemType != "api" {
		t.Fatalf("retry item_type = %q, want api", retryItems[0].ItemType)
	}
	if retryItems[0].Title != "lockbit -> Example Corp" {
		t.Fatalf("retry title = %q, want display title", retryItems[0].Title)
	}
	if retryItems[0].PayloadVersion != status.RetryPayloadVersionRansomwareEntryV1 {
		t.Fatalf("retry payload_version = %q, want %q", retryItems[0].PayloadVersion, status.RetryPayloadVersionRansomwareEntryV1)
	}
	var storedEntry api.RansomwareEntry
	if err := json.Unmarshal(retryItems[0].Payload, &storedEntry); err != nil {
		t.Fatalf("retry payload did not contain serialized ransomware entry: %v", err)
	}
	if storedEntry.ID != entry.ID || storedEntry.Victim != entry.Victim {
		t.Fatalf("retry payload entry = %+v, want original %+v", storedEntry, entry)
	}
}

func TestGetWebhookTargets(t *testing.T) {
	s := &Scheduler{
		config: &config.Config{
			DiscordWebhooks: config.DiscordWebhooks{
				RSS:        config.WebhookConfig{Enabled: true, URL: "https://discord.com/api/webhooks/1/rss"},
				Government: config.WebhookConfig{Enabled: true, URL: "https://discord.com/api/webhooks/1/gov"},
				Ransomware: config.WebhookConfig{Enabled: false, URL: ""},
			},
			SlackWebhooks: config.SlackWebhooks{
				RSS:        config.WebhookConfig{Enabled: true, URL: "https://hooks.slack.com/services/T/B/rss"},
				Government: config.WebhookConfig{Enabled: false, URL: ""},
				Ransomware: config.WebhookConfig{Enabled: true, URL: "https://hooks.slack.com/services/T/B/ransom"},
			},
		},
	}

	// General feeds â†’ RSS webhooks
	targets := s.getWebhookTargets("general")
	if len(targets) != 2 {
		t.Fatalf("expected 2 targets for general, got %d", len(targets))
	}
	if targets[0].messenger != "discord" {
		t.Errorf("expected first target to be discord, got %q", targets[0].messenger)
	}
	if targets[1].messenger != "slack" {
		t.Errorf("expected second target to be slack, got %q", targets[1].messenger)
	}

	// Government feeds â†’ only Discord enabled
	targets = s.getWebhookTargets("government")
	if len(targets) != 1 {
		t.Fatalf("expected 1 target for government, got %d", len(targets))
	}
	if targets[0].messenger != "discord" {
		t.Errorf("expected discord, got %q", targets[0].messenger)
	}

	// Ransomware feeds â†’ only Slack enabled
	targets = s.getWebhookTargets("ransomware")
	if len(targets) != 1 {
		t.Fatalf("expected 1 target for ransomware, got %d", len(targets))
	}
	if targets[0].messenger != "slack" {
		t.Errorf("expected slack, got %q", targets[0].messenger)
	}

	// Unknown feed type â†’ no targets
	targets = s.getWebhookTargets("unknown")
	if len(targets) != 0 {
		t.Errorf("expected 0 targets for unknown, got %d", len(targets))
	}
}

func TestGetWebhookTargetsAllDisabled(t *testing.T) {
	s := &Scheduler{
		config: &config.Config{
			DiscordWebhooks: config.DiscordWebhooks{
				RSS:        config.WebhookConfig{Enabled: false},
				Government: config.WebhookConfig{Enabled: false},
				Ransomware: config.WebhookConfig{Enabled: false},
			},
			SlackWebhooks: config.SlackWebhooks{
				RSS:        config.WebhookConfig{Enabled: false},
				Government: config.WebhookConfig{Enabled: false},
				Ransomware: config.WebhookConfig{Enabled: false},
			},
		},
	}

	for _, feedType := range []string{"general", "government", "ransomware"} {
		targets := s.getWebhookTargets(feedType)
		if len(targets) != 0 {
			t.Errorf("expected 0 targets for %q with all disabled, got %d", feedType, len(targets))
		}
	}
}

func TestGetWebhookTargets_IncludesQuietHours(t *testing.T) {
	qh := &config.QuietHours{Enabled: true, Start: "22:00", End: "07:00", Timezone: "Europe/Berlin"}
	s := &Scheduler{
		config: &config.Config{
			DiscordWebhooks: config.DiscordWebhooks{
				RSS: config.WebhookConfig{
					Enabled:    true,
					URL:        "https://discord.com/api/webhooks/1/rss",
					QuietHours: qh,
				},
				Government: config.WebhookConfig{Enabled: false},
				Ransomware: config.WebhookConfig{Enabled: false},
			},
			SlackWebhooks: config.SlackWebhooks{
				RSS:        config.WebhookConfig{Enabled: false},
				Government: config.WebhookConfig{Enabled: false},
				Ransomware: config.WebhookConfig{Enabled: false},
			},
		},
	}

	targets := s.getWebhookTargets("general")
	if len(targets) != 1 {
		t.Fatalf("expected 1 target, got %d", len(targets))
	}
	if targets[0].quietHours == nil {
		t.Fatal("expected quietHours to be set, got nil")
	}
	if targets[0].quietHours.Start != "22:00" {
		t.Errorf("expected start 22:00, got %q", targets[0].quietHours.Start)
	}
	if targets[0].quietHours.End != "07:00" {
		t.Errorf("expected end 07:00, got %q", targets[0].quietHours.End)
	}
	if targets[0].quietHours.Timezone != "Europe/Berlin" {
		t.Errorf("expected timezone Europe/Berlin, got %q", targets[0].quietHours.Timezone)
	}
}

func TestGetWebhookTargets_NilQuietHours(t *testing.T) {
	s := &Scheduler{
		config: &config.Config{
			DiscordWebhooks: config.DiscordWebhooks{
				RSS:        config.WebhookConfig{Enabled: true, URL: "https://discord.com/api/webhooks/1/rss"},
				Government: config.WebhookConfig{Enabled: false},
				Ransomware: config.WebhookConfig{Enabled: false},
			},
			SlackWebhooks: config.SlackWebhooks{
				RSS:        config.WebhookConfig{Enabled: false},
				Government: config.WebhookConfig{Enabled: false},
				Ransomware: config.WebhookConfig{Enabled: false},
			},
		},
	}

	targets := s.getWebhookTargets("general")
	if len(targets) != 1 {
		t.Fatalf("expected 1 target, got %d", len(targets))
	}
	if targets[0].quietHours != nil {
		t.Error("expected quietHours to be nil for webhook without quiet hours config")
	}
}

func TestConvertStoredToRSSEntry(t *testing.T) {
	stored := status.StoredRSSEntry{
		Key:         "test-key",
		FeedURL:     "https://example.com/feed",
		Title:       "Test Article",
		Link:        "https://example.com/article",
		Description: "A test description",
		Published:   "2025-01-15 10:30:00",
		Author:      "Author Name",
		Categories:  []string{"security", "threat"},
		GUID:        "guid-123",
		FeedTitle:   "Test Feed",
	}

	entry, err := status.RSSEntryFromStored(stored)
	if err != nil {
		t.Fatalf("RSSEntryFromStored() error = %v", err)
	}

	if entry.Title != stored.Title {
		t.Errorf("Title = %q, want %q", entry.Title, stored.Title)
	}
	if entry.Link != stored.Link {
		t.Errorf("Link = %q, want %q", entry.Link, stored.Link)
	}
	if entry.Description != stored.Description {
		t.Errorf("Description = %q, want %q", entry.Description, stored.Description)
	}
	if entry.Author != stored.Author {
		t.Errorf("Author = %q, want %q", entry.Author, stored.Author)
	}
	if entry.GUID != stored.GUID {
		t.Errorf("GUID = %q, want %q", entry.GUID, stored.GUID)
	}
	if entry.FeedTitle != stored.FeedTitle {
		t.Errorf("FeedTitle = %q, want %q", entry.FeedTitle, stored.FeedTitle)
	}
	if entry.FeedURL != stored.FeedURL {
		t.Errorf("FeedURL = %q, want %q", entry.FeedURL, stored.FeedURL)
	}
	if len(entry.Categories) != 2 {
		t.Errorf("expected 2 categories, got %d", len(entry.Categories))
	}
	entry.Categories[0] = "mutated"
	if stored.Categories[0] != "security" {
		t.Fatalf("RSSEntryFromStored shared category storage with stored input")
	}
	// Published should be parsed correctly
	expected := time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC)
	if !entry.Published.Equal(expected) {
		t.Errorf("Published = %v, want %v", entry.Published, expected)
	}
}

func TestConvertStoredToRSSEntryWithMicroseconds(t *testing.T) {
	stored := status.StoredRSSEntry{
		Title:     "Article",
		Published: "2025-01-15 10:30:00.123456",
	}

	entry, err := status.RSSEntryFromStored(stored)
	if err != nil {
		t.Fatalf("RSSEntryFromStored() error = %v", err)
	}

	if entry.Published.IsZero() {
		t.Error("expected non-zero published time for microsecond format")
	}
	if entry.Published.Year() != 2025 || entry.Published.Month() != 1 || entry.Published.Day() != 15 {
		t.Errorf("unexpected date: %v", entry.Published)
	}
}

func TestConvertStoredToRSSEntryInvalidPublished(t *testing.T) {
	stored := status.StoredRSSEntry{
		Title:     "Article",
		Published: "not-a-date",
	}

	if _, err := status.RSSEntryFromStored(stored); err == nil {
		t.Fatal("expected error for invalid stored RSS published timestamp")
	} else if !strings.Contains(err.Error(), "invalid stored RSS published timestamp") {
		t.Fatalf("RSSEntryFromStored() error = %v, want invalid timestamp context", err)
	}
}

func TestParseStoredRSSTimestampAcceptsOffsetAwareAndLegacyValues(t *testing.T) {
	offsetTime := time.Date(2026, 1, 2, 3, 4, 5, 123456789, time.FixedZone("UTC+2", 2*60*60))
	parsed, err := status.ParseStoredRSSTimestamp(offsetTime.Format(time.RFC3339Nano))
	if err != nil {
		t.Fatalf("ParseStoredRSSTimestamp(RFC3339Nano) error = %v", err)
	}
	if !parsed.Equal(offsetTime) {
		t.Fatalf("parsed RFC3339Nano = %s, want %s", parsed, offsetTime)
	}

	legacy, err := status.ParseStoredRSSTimestamp("2026-01-02 03:04:05.123456")
	if err != nil {
		t.Fatalf("ParseStoredRSSTimestamp(legacy) error = %v", err)
	}
	if legacy.Location() != time.UTC {
		t.Fatalf("legacy timestamp location = %v, want UTC", legacy.Location())
	}
}

func TestGenerateRSSEntryKey(t *testing.T) {
	feedURL := "https://example.com/feed"

	tests := []struct {
		name       string
		entry      rss.Entry
		wantPrefix string
	}{
		{
			"prefers GUID",
			rss.Entry{GUID: "unique-123", Link: "https://link.com", Title: "Title"},
			"rss:v2:guid:",
		},
		{
			"falls back to link",
			rss.Entry{Link: "https://example.com/article", Title: "Title"},
			"rss:v2:link:",
		},
		{
			"falls back to normalized title",
			rss.Entry{Title: "My Article", Published: time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC)},
			"rss:v2:title:",
		},
		{
			"title with whitespace",
			rss.Entry{Title: "  My Article  "},
			"rss:v2:title:",
		},
		{
			"empty entry",
			rss.Entry{},
			"",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := rss.GenerateEntryKey(feedURL, tt.entry.GUID, tt.entry.Link, tt.entry.Title)
			if tt.wantPrefix == "" {
				if result != "" {
					t.Errorf("GenerateEntryKey() = %q, want empty key", result)
				}
				return
			}
			if !strings.HasPrefix(result, tt.wantPrefix) {
				t.Errorf("GenerateEntryKey() = %q, want prefix %q", result, tt.wantPrefix)
			}
		})
	}
}

func TestFilterAndRecordNewRSSItemsDeduplicatesAndPersistsBatch(t *testing.T) {
	s := newTestScheduler(t)
	feedURL := "https://example.test/feed.xml"
	already := rss.Entry{
		FeedURL:   feedURL,
		Title:     "Already Parsed",
		Link:      "https://example.test/already",
		GUID:      "already-guid",
		Published: time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC),
		FeedTitle: "Example Feed",
	}
	fresh := rss.Entry{
		FeedURL:   feedURL,
		Title:     "Fresh Article",
		Link:      "https://example.test/fresh",
		GUID:      "fresh-guid",
		Published: time.Date(2026, 1, 15, 11, 0, 0, 0, time.UTC),
		FeedTitle: "Example Feed",
	}
	alreadyKey := rss.GenerateEntryKeyForEntry(already)
	freshKey := rss.GenerateEntryKeyForEntry(fresh)
	s.statusTracker.MarkRSSItemParsed(feedURL, alreadyKey, status.StoredRSSEntryFromRSS(already, alreadyKey))

	results := &rss.FeedResults{
		Entries: map[string][]rss.Entry{
			feedURL: {already, fresh},
		},
		FeedErrors: map[string]string{},
	}

	recorded := s.filterAndRecordNewRSSItems(results, config.FeedTypeGeneral)

	if recorded != 1 {
		t.Fatalf("recorded items = %d, want 1", recorded)
	}
	entries := results.Entries[feedURL]
	if len(entries) != 1 || entries[0].Title != "Fresh Article" {
		t.Fatalf("filtered entries = %#v, want only fresh entry", entries)
	}
	for _, key := range []string{alreadyKey, freshKey} {
		if !s.statusTracker.IsRSSItemParsed(feedURL, key) {
			t.Fatalf("RSS item %q was not marked parsed", key)
		}
	}

	unsent := s.statusTracker.GetUnsentRSSItemsForDestinationFeedType(
		"slack.rss.general",
		config.FeedTypeGeneral,
		map[string]struct{}{feedURL: {}},
	)
	if len(unsent) != 2 {
		t.Fatalf("unsent feed-scoped items = %d, want 2", len(unsent))
	}
	var foundFresh bool
	for _, item := range unsent {
		if item.Key == freshKey {
			foundFresh = true
			if item.FeedType != config.FeedTypeGeneral {
				t.Fatalf("fresh item feed type = %q, want %q", item.FeedType, config.FeedTypeGeneral)
			}
		}
	}
	if !foundFresh {
		t.Fatalf("fresh item %q missing from persisted RSS items", freshKey)
	}
}

func TestNewRejectsSecondSchedulerForSameDataDir(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.DataDir = t.TempDir()
	configDir := t.TempDir()

	first, err := New(cfg, configDir, false)
	if err != nil {
		t.Fatalf("New(first) error = %v", err)
	}

	secondCfg := *cfg
	second, err := New(&secondCfg, configDir, false)
	if err == nil {
		_ = second.Stop()
		_ = first.Stop()
		t.Fatal("New(second) succeeded while first scheduler held the data_dir lock")
	}
	if !strings.Contains(err.Error(), "data_dir lock") {
		_ = first.Stop()
		t.Fatalf("New(second) error = %v, want data_dir lock context", err)
	}

	if err := first.Stop(); err != nil {
		t.Fatalf("Stop(first) error = %v", err)
	}

	thirdCfg := *cfg
	third, err := New(&thirdCfg, configDir, false)
	if err != nil {
		t.Fatalf("New(third after first Stop) error = %v", err)
	}
	if err := third.Stop(); err != nil {
		t.Fatalf("Stop(third) error = %v", err)
	}
}

func TestNewFallsBackToMemoryStatusWhenDataDirUnavailable(t *testing.T) {
	dataPath := filepath.Join(t.TempDir(), "data-file")
	if err := os.WriteFile(dataPath, []byte("not a directory"), 0600); err != nil {
		t.Fatalf("WriteFile(dataPath) error = %v", err)
	}

	cfg := config.DefaultConfig()
	cfg.DataDir = dataPath
	s, err := New(cfg, t.TempDir(), false)
	if err != nil {
		t.Fatalf("New() error = %v, want in-memory fallback", err)
	}
	defer func() {
		if err := s.Stop(); err != nil {
			t.Fatalf("Stop() error = %v", err)
		}
	}()

	if s.dataDirLock != nil {
		t.Fatal("scheduler acquired a data_dir lock despite memory fallback")
	}
	s.statusTracker.UpdateAPIStatus(true, 1, "")
	if err := s.statusTracker.SavePendingChanges(); err != nil {
		t.Fatalf("SavePendingChanges() in memory fallback error = %v", err)
	}
	data, err := os.ReadFile(dataPath)
	if err != nil {
		t.Fatalf("ReadFile(dataPath) error = %v", err)
	}
	if string(data) != "not a directory" {
		t.Fatalf("data path was modified by memory fallback: %q", data)
	}
}

func TestPruneRSSFeedStatusForConfigSkipsDryRunAndNilTracker(t *testing.T) {
	cfg := config.DefaultConfig()
	feedURL := "https://example.test/old.xml"
	tracker := status.NewMemoryTracker()
	tracker.UpdateFeedStatus(feedURL, true, 1, "")

	pruneRSSFeedStatusForConfig(tracker, cfg, true)

	if _, ok := tracker.GetRSSFeedInfo(feedURL); !ok {
		t.Fatal("dry-run RSS feed status pruning removed feed status")
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("nil tracker guard panicked: %v", recovered)
		}
	}()
	pruneRSSFeedStatusForConfig(nil, cfg, false)
}

// Helper: newTestScheduler builds a minimal Scheduler with real dependencies
// suitable for tests that exercise logic paths without making HTTP calls.
// All webhook configs are disabled by default so no real network traffic occurs.
func newTestScheduler(t *testing.T) *Scheduler {
	t.Helper()

	dataDir := t.TempDir()
	configDir := t.TempDir()

	cfg := config.DefaultConfig()
	cfg.APIKey = "test-key"
	cfg.DataDir = dataDir
	cfg.APIPollInterval = time.Second
	cfg.RSSPollInterval = time.Second
	cfg.RSSRetryCount = 0
	cfg.RSSRetryDelay = time.Second
	cfg.RSSWorkerTimeout = 5 * time.Second
	cfg.MaxRSSWorkers = 2
	cfg.DiscordDelay = 0
	cfg.SlackDelay = 0

	s, err := New(cfg, configDir, false)
	if err != nil {
		t.Fatalf("New(test scheduler) error = %v", err)
	}
	t.Cleanup(func() {
		if err := s.Stop(); err != nil {
			t.Logf("Stop(test scheduler) cleanup error: %v", err)
		}
	})
	return s
}

func setSchedulerConfigDirForTest(t *testing.T, s *Scheduler, configDir string) {
	t.Helper()
	reloader, err := config.NewReloader(configDir)
	if err != nil {
		t.Logf("NewReloader(%q) initial signature error: %v", configDir, err)
	}
	s.configReloader = reloader
}

func unsentRSSItemsForTest(t *testing.T, items ...status.StoredRSSEntry) []status.UnsentRSSItem {
	t.Helper()
	unsent := make([]status.UnsentRSSItem, 0, len(items))
	for _, item := range items {
		unsent = append(unsent, status.UnsentRSSItemFromStored(item))
	}
	return unsent
}

func deliveryAuditEventsForTest(t *testing.T, dataDir string) []status.DeliveryAuditEvent {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dataDir, "delivery_audit.jsonl"))
	if err != nil {
		t.Fatalf("ReadFile(delivery_audit.jsonl) error = %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	events := make([]status.DeliveryAuditEvent, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var event status.DeliveryAuditEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("Unmarshal(delivery audit event) error = %v for line %q", err, line)
		}
		events = append(events, event)
	}
	return events
}

func hasDeliveryAuditEvent(
	events []status.DeliveryAuditEvent,
	itemKey string,
	outcome string,
	reason string,
) bool {
	for _, event := range events {
		if event.ItemKey == itemKey && event.Outcome == outcome && event.Reason == reason {
			return true
		}
	}
	return false
}

func TestNewWithDependenciesUsesInjectedDependencies(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.APIKey = "test-key"
	cfg.DataDir = t.TempDir()

	apiClient := &recordingAPIClient{}
	rssParser := newRSSParserForConfig(cfg)
	statusTracker := status.NewMemoryTracker()
	discordSender := &recordingWebhookSender{}
	slackSender := &recordingWebhookSender{}
	feedTypeMap := map[string]string{
		"https://example.com/feed.xml": config.FeedTypeGeneral,
	}

	s, err := NewWithDependencies(cfg, t.TempDir(), true, Dependencies{
		APIClient:            apiClient,
		RSSParser:            rssParser,
		DiscordWebhookSender: discordSender,
		SlackWebhookSender:   slackSender,
		StatusTracker:        statusTracker,
		FeedTypeMap:          feedTypeMap,
		ConfigReloader:       &recordingConfigReloader{cfg: cfg},
	})
	if err != nil {
		t.Fatalf("NewWithDependencies() error = %v", err)
	}

	if s.apiClient != APIClient(apiClient) {
		t.Fatal("scheduler did not use injected API client")
	}
	if s.rssParser != RSSParser(rssParser) {
		t.Fatal("scheduler did not use injected RSS parser")
	}
	if s.statusTracker != statusTracker {
		t.Fatal("scheduler did not use injected status tracker")
	}
	if s.discordWebhookSender != WebhookSender(discordSender) {
		t.Fatal("scheduler did not use injected Discord sender")
	}
	if s.slackWebhookSender != WebhookSender(slackSender) {
		t.Fatal("scheduler did not use injected Slack sender")
	}
	if got := s.feedTypeMap["https://example.com/feed.xml"]; got != config.FeedTypeGeneral {
		t.Fatalf("feedTypeMap entry = %q, want %q", got, config.FeedTypeGeneral)
	}
	if err := s.closeSchedulerResources(); err != nil {
		t.Fatalf("closeSchedulerResources() error = %v", err)
	}
	if !apiClient.closed {
		t.Fatal("scheduler did not close injected API client")
	}
}

func TestNewDefersAPIStatusLoadWhenRansomwareDeliveryDisabled(t *testing.T) {
	dataDir := t.TempDir()
	configDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dataDir, "api_status.json"), []byte("{not json"), 0600); err != nil {
		t.Fatalf("WriteFile(api_status.json) error = %v", err)
	}

	cfg := config.DefaultConfig()
	cfg.APIKey = "test-key"
	cfg.DataDir = dataDir
	cfg.DiscordWebhooks.Ransomware.Enabled = false
	cfg.SlackWebhooks.Ransomware.Enabled = false

	s, err := New(cfg, configDir, false)
	if err != nil {
		t.Fatalf("New() with inactive API delivery error = %v", err)
	}
	if s.statusTracker == nil || s.statusTracker.StatusSummary().APISentItems != 0 {
		t.Fatal("scheduler should start without loading API sent history")
	}
	if err := s.Stop(); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
}

func allowLocalRSSFeedsForTest(t *testing.T, parser RSSParser, client *http.Client) {
	t.Helper()
	concreteParser := rssParserForTest(t, parser)
	setRSSParserFieldForTest(t, concreteParser, "feedURLOpts", feedurl.Options{
		AllowPlainHTTP:       true,
		AllowPrivateNetworks: true,
	})
	setRSSParserFieldForTest(t, concreteParser, "httpClient", client)
}

func setRSSParserFieldForTest(t *testing.T, parser *rss.Parser, fieldName string, value any) {
	t.Helper()
	if parser == nil {
		t.Fatal("RSS parser is nil")
	}
	field := reflect.ValueOf(parser).Elem().FieldByName(fieldName)
	if !field.IsValid() {
		t.Fatalf("rss.Parser field %q does not exist", fieldName)
	}
	if !field.CanAddr() {
		t.Fatalf("rss.Parser field %q is not addressable", fieldName)
	}
	reflect.NewAt(field.Type(), unsafe.Pointer(field.UnsafeAddr())).Elem().Set(reflect.ValueOf(value))
}

func schedulerRSSFeedXMLForTest(title, guid, link string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
  <channel>
    <title>Scheduler Test Feed</title>
    <link>https://example.test/</link>
    <item>
      <title>%s</title>
      <link>%s</link>
      <guid>%s</guid>
      <description>Scheduler RSS integration body</description>
      <pubDate>Mon, 05 Jan 2026 11:00:00 +0000</pubDate>
      <category>security</category>
    </item>
  </channel>
</rss>`, title, link, guid)
}

func activeQuietHoursUTCForTest() *config.QuietHours {
	now := time.Now().UTC()
	return &config.QuietHours{
		Enabled:  true,
		Start:    now.Add(-time.Hour).Format("15:04"),
		End:      now.Add(time.Hour).Format("15:04"),
		Timezone: "UTC",
	}
}

func writeReloadGeneralConfig(t *testing.T, dir, apiKey string, retryCount int, retryDelay, workerTimeout time.Duration) {
	t.Helper()
	writeReloadGeneralConfigWithBaseURL(t, dir, apiKey, config.DefaultConfig().APIBaseURL, retryCount, retryDelay, workerTimeout)
}

func writeReloadGeneralConfigWithBaseURL(t *testing.T, dir, apiKey, apiBaseURL string, retryCount int, retryDelay, workerTimeout time.Duration) {
	t.Helper()

	content := fmt.Sprintf(`{
  "log_level": "INFO",
  "api_key": %q,
  "api_base_url": %q,
  "api_poll_interval": "1h",
  "rss_poll_interval": "30m",
  "rss_retry_count": %d,
  "rss_retry_delay": %q,
  "rss_worker_timeout": %q,
  "discord_delay": "2s",
  "slack_delay": "2s",
  "retry_window": "24h"
}`, apiKey, apiBaseURL, retryCount, retryDelay.String(), workerTimeout.String())

	if err := os.WriteFile(filepath.Join(dir, "config_general.json"), []byte(content), 0600); err != nil {
		t.Fatalf("WriteFile(config_general.json) error = %v", err)
	}
}

func writeReloadFeedsConfig(t *testing.T, dir string, generalFeeds []string) {
	t.Helper()

	quotedFeeds := make([]string, 0, len(generalFeeds))
	for _, feed := range generalFeeds {
		quotedFeeds = append(quotedFeeds, fmt.Sprintf("%q", feed))
	}
	content := fmt.Sprintf(`{"general_feeds":[%s]}`, strings.Join(quotedFeeds, ","))

	if err := os.WriteFile(filepath.Join(dir, "config_feeds.json"), []byte(content), 0600); err != nil {
		t.Fatalf("WriteFile(config_feeds.json) error = %v", err)
	}
}

func apiClientForTest(t *testing.T, client APIClient) *api.Client {
	t.Helper()
	concreteClient, ok := client.(*api.Client)
	if !ok {
		t.Fatalf("API client = %T, want *api.Client", client)
	}
	return concreteClient
}

func apiClientKeyForTest(t *testing.T, client APIClient) string {
	t.Helper()
	return reflect.ValueOf(apiClientForTest(t, client)).Elem().FieldByName("apiKey").String()
}

func apiClientBaseURLForTest(t *testing.T, client APIClient) string {
	t.Helper()
	return reflect.ValueOf(apiClientForTest(t, client)).Elem().FieldByName("baseURL").String()
}

func rssParserForTest(t *testing.T, parser RSSParser) *rss.Parser {
	t.Helper()
	concreteParser, ok := parser.(*rss.Parser)
	if !ok {
		t.Fatalf("RSS parser = %T, want *rss.Parser", parser)
	}
	return concreteParser
}

func rssParserSettingsForTest(t *testing.T, parser RSSParser) (int, time.Duration, time.Duration) {
	t.Helper()
	value := reflect.ValueOf(rssParserForTest(t, parser)).Elem()
	retryCount := int(value.FieldByName("retryCount").Int())
	retryDelay := time.Duration(value.FieldByName("retryDelay").Int())
	workerTimeout := time.Duration(value.FieldByName("workerTimeout").Int())
	return retryCount, retryDelay, workerTimeout
}

//nolint:gocyclo // end-to-end hot-reload scenario asserting many sequential state transitions; not a table split candidate
func TestReloadConfigEndToEndAppliesReloadableFiles(t *testing.T) {
	previousLevel := log.GetLevel()
	defer log.SetLevel(previousLevel)

	tmpDir := t.TempDir()
	configDir := filepath.Join(tmpDir, "configs")
	if err := os.Mkdir(configDir, 0o700); err != nil {
		t.Fatalf("Mkdir(configDir) error = %v", err)
	}
	oldDataDir := filepath.Join(tmpDir, "data-old")
	newDataDir := filepath.Join(tmpDir, "data-new")
	oldLogPath := filepath.Join(tmpDir, "logs", "old.log")
	newLogPath := filepath.Join(tmpDir, "logs", "new.log")

	initialGeneral := fmt.Sprintf(`{
  "log_level": "INFO",
  "log_file_path": %q,
  "log_rotation": {"max_size_mb": 10, "max_backups": 3, "max_age_days": 30, "compress": true},
  "data_dir": %q,
  "api_key": "initial-api-key",
  "api_base_url": "http://api.initial.test",
  "api_poll_interval": "1h",
  "rss_poll_interval": "30m",
  "rss_retry_count": 1,
  "rss_retry_delay": "1s",
  "rss_worker_timeout": "5s",
  "discord_delay": "2s",
  "slack_delay": "2s",
  "webhook_request_timeout": "10s",
  "webhook_max_retries": 3,
  "webhook_retry_delay": "1s",
  "retry_max_attempts": 5,
  "retry_window": "24h"
}`, oldLogPath, oldDataDir)
	if err := os.WriteFile(filepath.Join(configDir, "config_general.json"), []byte(initialGeneral), 0600); err != nil {
		t.Fatalf("WriteFile(initial config_general.json) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config_feeds.json"), []byte(`{
  "general_feeds": ["https://feeds.example.test/old-general.xml"],
  "government_feeds": [],
  "ransomware_feeds": []
}`), 0600); err != nil {
		t.Fatalf("WriteFile(initial config_feeds.json) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config_format.json"), []byte(`{}`), 0600); err != nil {
		t.Fatalf("WriteFile(initial config_format.json) error = %v", err)
	}

	cfg, err := config.LoadConfig(configDir)
	if err != nil {
		t.Fatalf("LoadConfig(initial) error = %v", err)
	}
	s, err := New(cfg, configDir, true)
	if err != nil {
		t.Fatalf("New(initial scheduler) error = %v", err)
	}
	defer func() {
		if err := s.Stop(); err != nil {
			t.Fatalf("Stop() error = %v", err)
		}
	}()
	oldLogRotation := cfg.LogRotation
	oldWebhookRequestTimeout := cfg.WebhookRequestTimeout
	oldWebhookMaxRetries := cfg.WebhookMaxRetries
	oldWebhookRetryDelay := cfg.WebhookRetryDelay

	reloadedGeneral := fmt.Sprintf(`{
  "log_level": "DEBUG",
  "log_file_path": %q,
  "log_rotation": {"max_size_mb": 99, "max_backups": 9, "max_age_days": 90, "compress": false},
  "data_dir": %q,
  "api_key": "reloaded-api-key",
  "api_base_url": "http://api.reloaded.test/",
  "api_poll_interval": "2h",
  "rss_poll_interval": "45m",
  "rss_retry_count": 2,
  "rss_retry_delay": "3s",
  "rss_worker_timeout": "8s",
  "max_api_entries_per_cycle": 7,
  "max_rss_entries_per_cycle": 8,
  "discord_delay": "1s",
  "slack_delay": "1500ms",
  "webhook_request_timeout": "20s",
  "webhook_max_retries": 4,
  "webhook_retry_delay": "750ms",
  "retry_max_attempts": 4,
  "retry_window": "12h",
  "discord_webhooks": {
    "ransomware": {
      "enabled": true,
      "url": "https://discord.com/api/webhooks/123456789012345678/abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_",
      "filters": {"include_countries": ["DE"], "include_keywords": ["finance"]},
      "quiet_hours": {"enabled": true, "start": "22:00", "end": "07:00", "timezone": "Europe/Berlin"}
    }
  },
  "slack_webhooks": {
    "rss": {
      "enabled": true,
      "url": "https://hooks.slack.com/services/T12345678/B12345678/abcdefghijklmnopqrstuvwxyz012345",
      "filters": {"include_categories": ["security"]}
    }
  }
}`, newLogPath, newDataDir)
	if err := os.WriteFile(filepath.Join(configDir, "config_general.json"), []byte(reloadedGeneral), 0600); err != nil {
		t.Fatalf("WriteFile(reloaded config_general.json) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config_feeds.json"), []byte(`{
  "general_feeds": ["https://feeds.example.test/new-general.xml"],
  "government_feeds": ["https://feeds.example.test/gov.xml"],
  "ransomware_feeds": []
}`), 0600); err != nil {
		t.Fatalf("WriteFile(reloaded config_feeds.json) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config_format.json"), []byte(`{
  "show_unicode_flags": false,
  "show_empty_fields": true,
  "empty_field_text": "empty",
  "field_order": ["victim", "group", "country", "post_url"],
  "rss": {"title_text": "Reloaded RSS", "field_order": ["title", "link"]},
  "discord": {"show_icons": false, "ransomware_color": "#112233", "rss_color": "#223344", "government_color": "#334455", "description_max_chars": 321},
  "slack": {"title_text": "Reloaded Alert", "rss_text": "Reloaded RSS Slack", "field_order": ["victim", "group", "url"], "description_max_chars": 222}
}`), 0600); err != nil {
		t.Fatalf("WriteFile(reloaded config_format.json) error = %v", err)
	}

	changed, err := s.ReloadConfig()
	if err != nil {
		t.Fatalf("ReloadConfig() error = %v", err)
	}
	if !changed {
		t.Fatal("ReloadConfig() changed = false, want true")
	}

	got := s.getConfig()
	if got.DataDir != oldDataDir {
		t.Fatalf("DataDir = %q, want restart-only old value %q", got.DataDir, oldDataDir)
	}
	if got.LogFilePath != oldLogPath {
		t.Fatalf("LogFilePath = %q, want restart-only old value %q", got.LogFilePath, oldLogPath)
	}
	if got.LogRotation != oldLogRotation {
		t.Fatalf("LogRotation = %+v, want restart-only old value %+v", got.LogRotation, oldLogRotation)
	}
	if got.WebhookRequestTimeout != oldWebhookRequestTimeout ||
		got.WebhookMaxRetries != oldWebhookMaxRetries ||
		got.WebhookRetryDelay != oldWebhookRetryDelay {
		t.Fatalf(
			"webhook HTTP policy = timeout %v attempts %d delay %v, want restart-only %v/%d/%v",
			got.WebhookRequestTimeout,
			got.WebhookMaxRetries,
			got.WebhookRetryDelay,
			oldWebhookRequestTimeout,
			oldWebhookMaxRetries,
			oldWebhookRetryDelay,
		)
	}
	if got.LogLevel != "DEBUG" || log.GetLevel() != log.DebugLevel {
		t.Fatalf("log level = config %q global %v, want DEBUG", got.LogLevel, log.GetLevel())
	}
	if got.APIKey != "reloaded-api-key" || got.APIBaseURL != "http://api.reloaded.test" {
		t.Fatalf("API settings = key %q base %q, want reloaded", got.APIKey, got.APIBaseURL)
	}
	if got.APIPollInterval != 2*time.Hour || got.RSSPollInterval != 45*time.Minute {
		t.Fatalf("poll intervals = API %v RSS %v, want 2h/45m", got.APIPollInterval, got.RSSPollInterval)
	}
	if got.APIMaxEntriesPerCycle != 7 || got.RSSMaxEntriesPerCycle != 8 {
		t.Fatalf("cycle limits = API %d RSS %d, want 7/8", got.APIMaxEntriesPerCycle, got.RSSMaxEntriesPerCycle)
	}
	if got.DiscordDelay != time.Second || got.SlackDelay != 1500*time.Millisecond {
		t.Fatalf("send delays = Discord %v Slack %v, want 1s/1.5s", got.DiscordDelay, got.SlackDelay)
	}
	if got.RetryMaxAttempts != 4 || got.RetryWindow != 12*time.Hour {
		t.Fatalf("retry settings = attempts %d window %v, want 4/12h", got.RetryMaxAttempts, got.RetryWindow)
	}
	if !got.DiscordWebhooks.Ransomware.Enabled || got.DiscordWebhooks.Ransomware.Filters.IncludeCountries[0] != "DE" {
		t.Fatalf("Discord ransomware webhook did not reload filters: %+v", got.DiscordWebhooks.Ransomware)
	}
	if got.DiscordWebhooks.Ransomware.QuietHours == nil || got.DiscordWebhooks.Ransomware.QuietHours.Timezone != "Europe/Berlin" {
		t.Fatalf("Discord ransomware quiet hours did not reload: %+v", got.DiscordWebhooks.Ransomware.QuietHours)
	}
	if !got.SlackWebhooks.RSS.Enabled || got.SlackWebhooks.RSS.Filters.IncludeCategories[0] != "security" {
		t.Fatalf("Slack RSS webhook did not reload filters: %+v", got.SlackWebhooks.RSS)
	}
	if len(got.Feeds.GeneralFeeds) != 1 || got.Feeds.GeneralFeeds[0] != "https://feeds.example.test/new-general.xml" {
		t.Fatalf("general feeds = %+v, want reloaded feed", got.Feeds.GeneralFeeds)
	}
	if got.Format.RSS.TitleText != "Reloaded RSS" || got.Format.Slack.RSSText != "Reloaded RSS Slack" || got.Format.Discord.DescriptionMaxChars != 321 {
		t.Fatalf("format config did not reload: %+v", got.Format)
	}
	if feedType := s.feedTypeMap["https://feeds.example.test/new-general.xml"]; feedType != config.FeedTypeGeneral {
		t.Fatalf("feedTypeMap new general feed = %q, want general", feedType)
	}
	if _, ok := s.feedTypeMap["https://feeds.example.test/old-general.xml"]; ok {
		t.Fatal("feedTypeMap retained old general feed after reload")
	}
	if key := apiClientKeyForTest(t, s.apiClient); key != "reloaded-api-key" {
		t.Fatalf("API client key = %q, want reloaded-api-key", key)
	}
	retryCount, retryDelay, workerTimeout := rssParserSettingsForTest(t, s.rssParser)
	if retryCount != 2 || retryDelay != 3*time.Second || workerTimeout != 8*time.Second {
		t.Fatalf("RSS parser settings = %d/%v/%v, want 2/3s/8s", retryCount, retryDelay, workerTimeout)
	}
}

func TestApplyReloadedConfigUpdatesStatusRetentionPolicy(t *testing.T) {
	s := newTestScheduler(t)

	newCfg := *s.getConfig()
	newCfg.StatusRetention.MaxAPISentItems = 1
	changed, err := s.applyReloadedConfig(&newCfg)
	if err != nil {
		t.Fatalf("applyReloadedConfig() error = %v", err)
	}
	if !changed {
		t.Fatal("applyReloadedConfig() changed = false, want true")
	}

	destinationID := apiDestinationID(status.MessengerSlack)
	s.statusTracker.MarkAPIItemSentToDestination("api-old", "Old", destinationID)
	s.statusTracker.MarkAPIItemSentToDestination("api-new", "New", destinationID)
	s.statusTracker.CleanupOldEntries()

	if got := s.statusTracker.StatusSummary().APISentItems; got != 1 {
		t.Fatalf("API sent item count after reloaded retention cleanup = %d, want 1", got)
	}
}

func TestReloadConfigAuditsAlertRoutingChangesWithoutSecrets(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()

	s := newTestScheduler(t)
	configDir := t.TempDir()
	setSchedulerConfigDirForTest(t, s, configDir)
	s.config.APIKey = "test-key"

	webhookURL := "https://hooks.slack.com/services/T12345678/B12345678/abcdefghijklmnopqrstuvwxyz012345" //nolint:gosec // G101: test fixture, not a real credential
	if err := os.WriteFile(filepath.Join(configDir, "config_general.json"), []byte(fmt.Sprintf(`{
  "api_key": "",
  "slack_webhooks": {
    "rss": {
      "enabled": true,
      "url": %q,
      "filters": {"include_categories": ["security"]},
      "quiet_hours": {"enabled": true, "start": "22:00", "end": "07:00", "timezone": "Europe/Berlin"}
    }
  }
}`, webhookURL)), 0600); err != nil {
		t.Fatalf("WriteFile(config_general.json) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config_feeds.json"), []byte(`{
  "general_feeds": ["https://feeds.example.test/security.xml"],
  "government_feeds": [],
  "ransomware_feeds": []
}`), 0600); err != nil {
		t.Fatalf("WriteFile(config_feeds.json) error = %v", err)
	}

	changed, err := s.ReloadConfig()
	if err != nil {
		t.Fatalf("ReloadConfig() error = %v", err)
	}
	if !changed {
		t.Fatal("ReloadConfig() changed = false, want true")
	}

	enabledEntry := findTestLogEntryWithField(hook, "Config reload alert routing changed", "field", "slack.rss.enabled")
	if enabledEntry == nil {
		t.Fatalf("missing alert-routing audit log for slack.rss.enabled; entries=%#v", hook.AllEntries())
	}
	if enabledEntry.Data["old"] != false || enabledEntry.Data["new"] != true {
		t.Fatalf("enabled audit old/new = %#v/%#v, want false/true; fields=%#v", enabledEntry.Data["old"], enabledEntry.Data["new"], enabledEntry.Data)
	}
	if enabledEntry.Data["component"] != "config_reload" {
		t.Fatalf("component = %#v, want config_reload", enabledEntry.Data["component"])
	}
	if findTestLogEntryWithField(hook, "Config reload alert routing changed", "field", "slack.rss.url_sha256_prefix") == nil {
		t.Fatal("missing webhook URL fingerprint audit entry")
	}
	if findTestLogEntryWithField(hook, "Config reload alert routing changed", "field", "feeds.general_feeds") == nil {
		t.Fatal("missing feed-list audit entry")
	}
	if findTestLogEntryWithField(hook, "Config reload alert routing changed", "field", "slack.rss.filters_sha256_prefix") == nil {
		t.Fatal("missing webhook filter audit entry")
	}
	if findTestLogEntryWithField(hook, "Config reload alert routing changed", "field", "slack.rss.quiet_hours_sha256_prefix") == nil {
		t.Fatal("missing webhook quiet-hours audit entry")
	}
	if findTestLogEntryWithField(hook, "Config reload alert routing changed", "field", "api.key_sha256_prefix") == nil {
		t.Fatal("missing API key fingerprint audit entry")
	}
	if findTestLogEntryWithField(hook, "Config reload alert routing changed", "field", "slack_delay_ms") == nil {
		t.Fatal("missing delivery timing audit entry")
	}

	for _, entry := range hook.AllEntries() {
		rendered := entry.Message + " " + fmt.Sprint(entry.Data)
		if strings.Contains(rendered, webhookURL) || strings.Contains(rendered, "security") || strings.Contains(rendered, "test-key") {
			t.Fatalf("audit log leaked raw routing secret or filter value: %s", rendered)
		}
	}
}

func TestReloadConfigRebuildsAPIClientWhenAPIKeyChanges(t *testing.T) {
	s := newTestScheduler(t)
	configDir := t.TempDir()
	setSchedulerConfigDirForTest(t, s, configDir)
	s.config.APIKey = "old-key"
	client, err := api.NewClient("old-key")
	if err != nil {
		t.Fatalf("NewClient(old-key) error = %v", err)
	}
	s.apiClient = client
	writeReloadGeneralConfig(t, configDir, "new-key", s.config.RSSRetryCount, s.config.RSSRetryDelay, s.config.RSSWorkerTimeout)

	changed, err := s.ReloadConfig()
	if err != nil {
		t.Fatalf("ReloadConfig() error = %v", err)
	}
	if !changed {
		t.Fatal("ReloadConfig() changed = false, want true")
	}
	if got := s.getConfig().APIKey; got != "new-key" {
		t.Fatalf("reloaded config APIKey = %q, want new-key", got)
	}
	if got := apiClientKeyForTest(t, s.apiClient); got != "new-key" {
		t.Fatalf("API client key = %q, want new-key", got)
	}
}

func TestReloadConfigRebuildsAPIClientWhenAPIBaseURLChanges(t *testing.T) {
	s := newTestScheduler(t)
	configDir := t.TempDir()
	setSchedulerConfigDirForTest(t, s, configDir)
	s.config.APIKey = "test-key"
	s.config.APIBaseURL = "http://old-api.test"
	client, err := api.NewClientWithBaseURL("test-key", s.config.APIBaseURL)
	if err != nil {
		t.Fatalf("NewClientWithBaseURL(old) error = %v", err)
	}
	s.apiClient = client

	writeReloadGeneralConfigWithBaseURL(t, configDir, "test-key", "http://new-api.test/", s.config.RSSRetryCount, s.config.RSSRetryDelay, s.config.RSSWorkerTimeout)

	changed, err := s.ReloadConfig()
	if err != nil {
		t.Fatalf("ReloadConfig() error = %v", err)
	}
	if !changed {
		t.Fatal("ReloadConfig() changed = false, want true")
	}
	if got := s.getConfig().APIBaseURL; got != "http://new-api.test" {
		t.Fatalf("reloaded config APIBaseURL = %q, want normalized new URL", got)
	}
	if got := apiClientBaseURLForTest(t, s.apiClient); got != "http://new-api.test" {
		t.Fatalf("API client baseURL = %q, want normalized new URL", got)
	}
}

func TestReloadConfigRebuildsRSSParserSettings(t *testing.T) {
	s := newTestScheduler(t)
	configDir := t.TempDir()
	setSchedulerConfigDirForTest(t, s, configDir)
	writeReloadGeneralConfig(t, configDir, s.config.APIKey, 2, 3*time.Second, 7*time.Second)

	changed, err := s.ReloadConfig()
	if err != nil {
		t.Fatalf("ReloadConfig() error = %v", err)
	}
	if !changed {
		t.Fatal("ReloadConfig() changed = false, want true")
	}
	retryCount, retryDelay, workerTimeout := rssParserSettingsForTest(t, s.rssParser)
	if retryCount != 2 {
		t.Fatalf("RSS parser retryCount = %d, want 2", retryCount)
	}
	if retryDelay != 3*time.Second {
		t.Fatalf("RSS parser retryDelay = %v, want 3s", retryDelay)
	}
	if workerTimeout != 7*time.Second {
		t.Fatalf("RSS parser workerTimeout = %v, want 7s", workerTimeout)
	}
}

func TestReloadConfigInvalidConfigKeepsCurrentConfig(t *testing.T) {
	s := newTestScheduler(t)
	configDir := t.TempDir()
	setSchedulerConfigDirForTest(t, s, configDir)
	s.config.APIKey = "current-key"
	s.config.Feeds.GeneralFeeds = []string{"https://current.example.test/feed.xml"}
	s.feedTypeMap = buildFeedTypeMap(s.config)
	oldCfg := s.getConfig()
	oldFeedType := s.feedTypeMap["https://current.example.test/feed.xml"]

	content := `{
  "api_key": "new-key",
  "api_poll_interval": "not-a-duration",
  "rss_poll_interval": "30m"
}`
	if err := os.WriteFile(filepath.Join(configDir, "config_general.json"), []byte(content), 0600); err != nil {
		t.Fatalf("WriteFile(config_general.json) error = %v", err)
	}

	changed, err := s.ReloadConfig()
	if err == nil {
		t.Fatal("ReloadConfig() error = nil, want validation error")
	}
	if changed {
		t.Fatal("ReloadConfig() changed = true, want false")
	}
	if !strings.Contains(err.Error(), "config reload failed validation") {
		t.Fatalf("ReloadConfig() error = %v, want validation context", err)
	}
	if got := s.getConfig().APIKey; got != oldCfg.APIKey {
		t.Fatalf("APIKey = %q, want unchanged %q", got, oldCfg.APIKey)
	}
	if got := s.feedTypeMap["https://current.example.test/feed.xml"]; got != oldFeedType {
		t.Fatalf("feedTypeMap current feed = %q, want unchanged %q", got, oldFeedType)
	}
}

func TestReloadConfigKeepsRestartOnlyLoggingFields(t *testing.T) {
	s := newTestScheduler(t)
	configDir := t.TempDir()
	setSchedulerConfigDirForTest(t, s, configDir)
	oldLogFilePath := s.config.LogFilePath
	oldLogRotation := s.config.LogRotation

	content := fmt.Sprintf(`{
  "log_level": "INFO",
  "log_file_path": "./logs/reloaded.log",
  "log_rotation": {
    "max_size_mb": 99,
    "max_backups": 9,
    "max_age_days": 90,
    "compress": false
  },
  "api_key": %q,
  "api_base_url": %q,
  "api_poll_interval": "1h",
  "rss_poll_interval": "30m",
  "rss_retry_count": %d,
  "rss_retry_delay": %q,
  "rss_worker_timeout": %q,
  "discord_delay": "2s",
  "slack_delay": "2s",
  "retry_window": "24h"
}`, s.config.APIKey, s.config.APIBaseURL, s.config.RSSRetryCount, s.config.RSSRetryDelay.String(), s.config.RSSWorkerTimeout.String())
	if err := os.WriteFile(filepath.Join(configDir, "config_general.json"), []byte(content), 0600); err != nil {
		t.Fatalf("WriteFile(config_general.json) error = %v", err)
	}

	changed, err := s.ReloadConfig()
	if err != nil {
		t.Fatalf("ReloadConfig() error = %v", err)
	}
	if !changed {
		t.Fatal("ReloadConfig() changed = false, want true")
	}
	if got := s.getConfig().LogFilePath; got != oldLogFilePath {
		t.Fatalf("LogFilePath = %q, want restart-only value %q", got, oldLogFilePath)
	}
	if got := s.getConfig().LogRotation; got != oldLogRotation {
		t.Fatalf("LogRotation = %+v, want restart-only value %+v", got, oldLogRotation)
	}
}

func TestReloadConfigPrunesRemovedRSSFeedStatus(t *testing.T) {
	s := newTestScheduler(t)
	configDir := t.TempDir()
	setSchedulerConfigDirForTest(t, s, configDir)
	activeFeedURL := "https://active.example.test/feed.xml"
	removedFeedURL := "https://removed.example.test/feed.xml"
	s.config.Feeds.GeneralFeeds = []string{activeFeedURL, removedFeedURL}
	s.feedTypeMap = buildFeedTypeMap(s.config)
	s.statusTracker.UpdateFeedStatus(activeFeedURL, true, 1, "")
	s.statusTracker.UpdateFeedStatus(removedFeedURL, false, 0, "gone")
	if err := s.statusTracker.SavePendingChanges(); err != nil {
		t.Fatalf("SavePendingChanges: %v", err)
	}

	writeReloadGeneralConfig(t, configDir, s.config.APIKey, s.config.RSSRetryCount, s.config.RSSRetryDelay, s.config.RSSWorkerTimeout)
	writeReloadFeedsConfig(t, configDir, []string{activeFeedURL})

	changed, err := s.ReloadConfig()
	if err != nil {
		t.Fatalf("ReloadConfig() error = %v", err)
	}
	if !changed {
		t.Fatal("ReloadConfig() changed = false, want true")
	}

	data, err := os.ReadFile(filepath.Join(s.config.DataDir, "rss_status.json"))
	if err != nil {
		t.Fatalf("ReadFile(rss_status.json) error = %v", err)
	}
	if !strings.Contains(string(data), activeFeedURL) {
		t.Fatalf("rss_status.json missing active feed URL %q: %s", activeFeedURL, data)
	}
	if strings.Contains(string(data), removedFeedURL) {
		t.Fatalf("rss_status.json still contains removed feed URL %q: %s", removedFeedURL, data)
	}
}

func TestBulkSendAPIEntriesToSlackSendDelayObservesContextCancellation(t *testing.T) {
	s := newTestScheduler(t)
	s.config.SlackDelay = 500 * time.Millisecond

	received := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- struct{}{}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.sendAPIEntriesIndividuallyToSlack(ctx, []api.RansomwareEntry{{
			ID:      "victim-1",
			Group:   "lockbit",
			Victim:  "Example Corp",
			Country: "DE",
		}}, server.URL)
	}()

	waitForTestSignalWithTimeout(
		t,
		done,
		250*time.Millisecond,
		"sendAPIEntriesIndividuallyToSlack to return after context cancellation during send delay",
	)

	select {
	case <-received:
		t.Fatal("Slack webhook received a request after context cancellation during send delay")
	default:
	}
	if retryItems := s.statusTracker.GetRetryItemsByType("api"); len(retryItems) != 0 {
		t.Fatalf("retry queue length = %d, want 0 because cancellation happened before send", len(retryItems))
	}
}

func TestBulkSendAPIEntriesToSlackPersistsSentMarkerBeforeNextSend(t *testing.T) {
	s := newTestScheduler(t)
	entries := []api.RansomwareEntry{
		{
			ID:      "victim-1",
			Group:   "lockbit",
			Victim:  "Example One",
			Country: "DE",
		},
		{
			ID:      "victim-2",
			Group:   "lockbit",
			Victim:  "Example Two",
			Country: "FR",
		},
	}

	firstKey := api.GenerateEntryKey(entries[0])
	var serverURL string
	var mu sync.Mutex
	requestCount := 0
	errCh := make(chan string, 2)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requestCount++
		currentRequest := requestCount
		mu.Unlock()

		if r.Method != http.MethodPost {
			errCh <- fmt.Sprintf("request method = %s, want POST", r.Method)
		}
		if currentRequest == 2 {
			reloaded, err := status.NewTracker(s.config.DataDir)
			if err != nil {
				errCh <- fmt.Sprintf("NewTracker(reload) error = %v", err)
			} else if !reloaded.IsAPIItemSentToWebhook(firstKey, serverURL) {
				errCh <- "first API sent marker was not persisted before the second webhook send"
			}
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	serverURL = server.URL

	s.sendAPIEntriesIndividuallyToSlack(t.Context(), entries, server.URL)

	mu.Lock()
	gotRequests := requestCount
	mu.Unlock()
	if gotRequests != 2 {
		t.Fatalf("webhook request count = %d, want 2", gotRequests)
	}
	select {
	case msg := <-errCh:
		t.Fatal(msg)
	default:
	}
}

func TestBulkSendAPIEntriesToSlackFlushesRetryOnCancellation(t *testing.T) {
	s := newTestScheduler(t)
	s.config.RetryMaxAttempts = 5
	s.config.RetryWindow = time.Hour

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		cancel()
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("temporary failure"))
	}))
	defer server.Close()

	entries := []api.RansomwareEntry{
		{ID: "victim-1", Group: "lockbit", Victim: "Example One", Country: "DE"},
		{ID: "victim-2", Group: "lockbit", Victim: "Example Two", Country: "FR"},
	}

	s.sendAPIEntriesIndividuallyToSlack(ctx, entries, server.URL)

	if requestCount != 1 {
		t.Fatalf("slack request count = %d, want 1 before cancellation", requestCount)
	}
	reloaded, err := status.NewTracker(s.config.DataDir)
	if err != nil {
		t.Fatalf("NewTracker(reload) error = %v", err)
	}
	retryItems := reloaded.GetRetryItemsByType("api")
	if len(retryItems) != 1 {
		t.Fatalf("persisted retry queue length = %d, want 1", len(retryItems))
	}
	if retryItems[0].ItemKey != api.GenerateEntryKey(entries[0]) {
		t.Fatalf("persisted retry item key = %q, want first entry", retryItems[0].ItemKey)
	}
}

func TestBulkSendAPIEntriesToDiscordDeadLettersPermanentFailure(t *testing.T) {
	s := newTestScheduler(t)
	s.config.RetryMaxAttempts = 5
	s.config.RetryWindow = time.Hour

	entry := api.RansomwareEntry{
		ID:      "discord-failure-1",
		Group:   "lockbit",
		Victim:  "Example Discord Failure",
		Country: "DE",
	}
	webhookURL := "https://discord.com/api/webhooks/"

	s.sendAPIEntriesIndividuallyToDiscord(t.Context(), []api.RansomwareEntry{entry}, webhookURL)

	key := api.GenerateEntryKey(entry)
	if s.statusTracker.IsAPIItemSentToWebhook(key, webhookURL) {
		t.Fatal("failed Discord send was marked sent")
	}
	retryItems := s.statusTracker.GetRetryItemsByType("api")
	if len(retryItems) != 0 {
		t.Fatalf("retry queue length = %d, want 0 for permanent Discord failure", len(retryItems))
	}
	if !s.statusTracker.IsRetryDeadLettered(key, "discord", "api") {
		t.Fatal("permanent Discord failure was not dead-lettered")
	}
}

func TestBulkSendAPIEntriesToSlackEnqueuesRetryOnFailure(t *testing.T) {
	s := newTestScheduler(t)
	s.config.RetryMaxAttempts = 5
	s.config.RetryWindow = time.Hour

	var requestCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("temporary failure"))
	}))
	defer server.Close()

	entry := api.RansomwareEntry{
		ID:      "slack-failure-1",
		Group:   "lockbit",
		Country: "DE",
	}

	s.sendAPIEntriesIndividuallyToSlack(t.Context(), []api.RansomwareEntry{entry}, server.URL)

	if requestCount != 1 {
		t.Fatalf("slack request count = %d, want 1", requestCount)
	}
	key := api.GenerateEntryKey(entry)
	if s.statusTracker.IsAPIItemSentToWebhook(key, server.URL) {
		t.Fatal("failed Slack send was marked sent")
	}
	retryItems := s.statusTracker.GetRetryItemsByType("api")
	if len(retryItems) != 1 {
		t.Fatalf("retry queue length = %d, want 1", len(retryItems))
	}
	if retryItems[0].ItemKey != key {
		t.Fatalf("retry item key = %q, want %q", retryItems[0].ItemKey, key)
	}
	if retryItems[0].Messenger != "slack" {
		t.Fatalf("retry messenger = %q, want slack", retryItems[0].Messenger)
	}
	if retryItems[0].ItemType != "api" {
		t.Fatalf("retry item_type = %q, want api", retryItems[0].ItemType)
	}
	if retryItems[0].Title != "lockbit -> Unknown victim" {
		t.Fatalf("retry title = %q, want display title with unknown victim", retryItems[0].Title)
	}
	if retryItems[0].PayloadVersion != status.RetryPayloadVersionRansomwareEntryV1 {
		t.Fatalf("retry payload_version = %q, want %q", retryItems[0].PayloadVersion, status.RetryPayloadVersionRansomwareEntryV1)
	}
	var storedEntry api.RansomwareEntry
	if err := json.Unmarshal(retryItems[0].Payload, &storedEntry); err != nil {
		t.Fatalf("retry payload did not contain serialized ransomware entry: %v", err)
	}
	if storedEntry.ID != entry.ID || storedEntry.Group != entry.Group {
		t.Fatalf("retry payload entry = %+v, want original %+v", storedEntry, entry)
	}
	if strings.Contains(retryItems[0].LastError, "temporary failure") {
		t.Fatalf("retry last_error kept raw Slack response body: %q", retryItems[0].LastError)
	}
	if !strings.Contains(retryItems[0].LastError, "slack webhook provider unavailable") {
		t.Fatalf("retry last_error = %q, want normalized Slack guidance", retryItems[0].LastError)
	}
	if retryItems[0].ErrorCategory != "provider_unavailable" {
		t.Fatalf("retry error_category = %q, want provider_unavailable", retryItems[0].ErrorCategory)
	}
	if retryItems[0].StatusCode != http.StatusInternalServerError {
		t.Fatalf("retry status_code = %d, want %d", retryItems[0].StatusCode, http.StatusInternalServerError)
	}
	if retryItems[0].Retryable == nil || !*retryItems[0].Retryable {
		t.Fatalf("retry retryable = %v, want true", retryItems[0].Retryable)
	}
}

func TestBulkSendAPIEntriesToSlackDeadLettersPermanentHTTPFailure(t *testing.T) {
	s := newTestScheduler(t)
	s.config.RetryMaxAttempts = 5
	s.config.RetryWindow = time.Hour

	var requestCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("no_service"))
	}))
	defer server.Close()

	entry := api.RansomwareEntry{
		ID:      "slack-permanent-failure-1",
		Group:   "lockbit",
		Victim:  "Example Permanent Failure",
		Country: "DE",
	}

	s.sendAPIEntriesIndividuallyToSlack(t.Context(), []api.RansomwareEntry{entry}, server.URL)

	if requestCount != 1 {
		t.Fatalf("slack request count = %d, want 1", requestCount)
	}
	key := api.GenerateEntryKey(entry)
	if s.statusTracker.IsAPIItemSentToWebhook(key, server.URL) {
		t.Fatal("failed Slack send was marked sent")
	}
	if retryItems := s.statusTracker.GetRetryItemsByType("api"); len(retryItems) != 0 {
		t.Fatalf("retry queue length = %d, want 0 for permanent Slack failure", len(retryItems))
	}
	if !s.statusTracker.IsRetryDeadLettered(key, "slack", "api") {
		t.Fatal("permanent Slack failure was not dead-lettered")
	}
	deadLetters := s.statusTracker.GetDeadLetterItems()
	if len(deadLetters) != 1 {
		t.Fatalf("dead-letter count = %d, want 1", len(deadLetters))
	}
	if deadLetters[0].ErrorCategory != "invalid_webhook" {
		t.Fatalf("dead-letter error_category = %q, want invalid_webhook", deadLetters[0].ErrorCategory)
	}
	if deadLetters[0].StatusCode != http.StatusNotFound {
		t.Fatalf("dead-letter status_code = %d, want %d", deadLetters[0].StatusCode, http.StatusNotFound)
	}
	if deadLetters[0].Retryable == nil || *deadLetters[0].Retryable {
		t.Fatalf("dead-letter retryable = %v, want false", deadLetters[0].Retryable)
	}
}

func TestDeliveryStatusMessageClassifiesWebhookFailures(t *testing.T) {
	tests := []struct {
		name      string
		messenger string
		err       error
		want      string
	}{
		{
			name:      "slack invalid payload",
			messenger: "slack",
			err:       errors.New("slack webhook returned status 400: invalid_blocks"),
			want:      "slack webhook rejected the message payload",
		},
		{
			name:      "discord rate limit",
			messenger: "discord",
			err:       errors.New("HTTP 429 Too Many Requests"),
			want:      "discord webhook rate limited delivery",
		},
		{
			name:      "provider outage",
			messenger: "slack",
			err:       errors.New("HTTP 503 Service Unavailable: raw provider body"),
			want:      "slack webhook provider unavailable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := deliveryStatusMessage(tt.messenger, tt.err)
			if !strings.Contains(got, tt.want) {
				t.Fatalf("deliveryStatusMessage() = %q, want %q", got, tt.want)
			}
			if strings.Contains(got, "raw provider body") || strings.Contains(got, "invalid_blocks") {
				t.Fatalf("deliveryStatusMessage() leaked raw provider detail: %q", got)
			}
		})
	}
}

func TestDeliveryStatusMessageUsesTypedWebhookStatusBeforeBodyText(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "rate limit body mentions 404",
			err: webhookhttp.NewHTTPStatusError(
				"discord",
				http.StatusTooManyRequests,
				"429 Too Many Requests",
				`{"message":"retry token 404 later"}`,
				nil,
			),
			want: "discord webhook rate limited delivery",
		},
		{
			name: "server error body mentions payload",
			err: webhookhttp.NewHTTPStatusError(
				"discord",
				http.StatusServiceUnavailable,
				"503 Service Unavailable",
				`{"message":"invalid payload 400"}`,
				nil,
			),
			want: "discord webhook provider unavailable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := deliveryStatusMessage("discord", tt.err)
			if !strings.Contains(got, tt.want) {
				t.Fatalf("deliveryStatusMessage() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDeliveryStatusMessageDoesNotClassifyPlainErrorByStatusSubstrings(t *testing.T) {
	err := errors.New("provider response body referenced case 404 without an HTTP status")

	got := deliveryStatusMessage("discord", err)

	if strings.Contains(got, "verify or rotate the webhook URL") {
		t.Fatalf("deliveryStatusMessage() = %q, want generic failure for plain error", got)
	}
	if !strings.Contains(got, "discord webhook delivery failed") {
		t.Fatalf("deliveryStatusMessage() = %q, want generic failure", got)
	}
}

func TestBulkSendAPIEntriesToDiscordSkipsStateChangesWhenCanceledOrDryRun(t *testing.T) {
	entry := api.RansomwareEntry{
		ID:      "discord-skip-1",
		Group:   "lockbit",
		Victim:  "Example Skip",
		Country: "DE",
	}
	webhookURL := "https://discord.com/api/webhooks/"

	t.Run("canceled context", func(t *testing.T) {
		s := newTestScheduler(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		s.sendAPIEntriesIndividuallyToDiscord(ctx, []api.RansomwareEntry{entry}, webhookURL)

		if s.statusTracker.IsAPIItemSentToWebhook(api.GenerateEntryKey(entry), webhookURL) {
			t.Fatal("canceled Discord send was marked sent")
		}
		if retryItems := s.statusTracker.GetRetryItemsByType("api"); len(retryItems) != 0 {
			t.Fatalf("retry queue length = %d, want 0 for canceled context", len(retryItems))
		}
	})

	t.Run("dry run", func(t *testing.T) {
		s := newTestScheduler(t)
		s.dryRun = true

		s.sendAPIEntriesIndividuallyToDiscord(t.Context(), []api.RansomwareEntry{entry}, webhookURL)

		if s.statusTracker.IsAPIItemSentToWebhook(api.GenerateEntryKey(entry), webhookURL) {
			t.Fatal("dry-run Discord send was marked sent")
		}
		if retryItems := s.statusTracker.GetRetryItemsByType("api"); len(retryItems) != 0 {
			t.Fatalf("retry queue length = %d, want 0 for dry-run", len(retryItems))
		}
	})
}

func TestCheckAPIOnceRoutesFetchedEntriesToSlackWebhook(t *testing.T) {
	s := newTestScheduler(t)
	s.config.APIKey = "test-api-key"
	s.config.SlackWebhooks.Ransomware = config.WebhookConfig{
		Enabled: true,
		Filters: &config.WebhookFilters{
			IncludeCountries: []string{"DE"},
		},
	}

	newEntry := api.RansomwareEntry{
		ID:         "new-entry",
		Group:      "lockbit",
		Victim:     "New Corp",
		Country:    "DE",
		Discovered: time.Date(2026, 1, 3, 9, 0, 0, 0, time.UTC),
	}
	oldEntry := api.RansomwareEntry{
		ID:         "old-entry",
		Group:      "lockbit",
		Victim:     "Old Corp",
		Country:    "DE",
		Discovered: time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC),
	}
	filteredEntry := api.RansomwareEntry{
		ID:         "filtered-entry",
		Group:      "lockbit",
		Victim:     "Filtered Corp",
		Country:    "FR",
		Discovered: time.Date(2026, 1, 2, 9, 0, 0, 0, time.UTC),
	}
	alreadySentEntry := api.RansomwareEntry{
		ID:         "already-sent-entry",
		Group:      "lockbit",
		Victim:     "Already Sent Corp",
		Country:    "DE",
		Discovered: time.Date(2026, 1, 4, 9, 0, 0, 0, time.UTC),
	}

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/victims/recent" {
			t.Errorf("API path = %q, want /victims/recent", r.URL.Path)
		}
		if got := r.Header.Get("X-API-KEY"); got != "test-api-key" {
			t.Errorf("X-API-KEY = %q, want test-api-key", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"client": "test",
			"count": 4,
			"order": "desc",
			"victims": [
				{"id":"new-entry","group":"lockbit","victim":"New Corp","country":"DE","discovered":"2026-01-03 09:00:00"},
				{"id":"filtered-entry","group":"lockbit","victim":"Filtered Corp","country":"FR","discovered":"2026-01-02 09:00:00"},
				{"id":"already-sent-entry","group":"lockbit","victim":"Already Sent Corp","country":"DE","discovered":"2026-01-04 09:00:00"},
				{"id":"old-entry","group":"lockbit","victim":"Old Corp","country":"DE","discovered":"2026-01-01 09:00:00"}
			]
		}`))
	}))
	defer apiServer.Close()

	client, err := api.NewClientWithBaseURL("test-api-key", apiServer.URL)
	if err != nil {
		t.Fatalf("NewClientWithBaseURL() error = %v", err)
	}
	s.apiClient = client

	var bodies []string
	slackServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("Slack method = %s, want POST", r.Method)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("ReadAll(slack body) error = %v", err)
		}
		bodies = append(bodies, string(body))
		w.WriteHeader(http.StatusOK)
	}))
	defer slackServer.Close()
	s.config.SlackWebhooks.Ransomware.URL = slackServer.URL
	s.statusTracker.MarkAPIItemSentToWebhook(api.GenerateEntryKey(alreadySentEntry), "already sent", slackServer.URL)

	s.checkAPIOnce(t.Context())

	if len(bodies) != 2 {
		t.Fatalf("slack request count = %d, want 2", len(bodies))
	}
	if !strings.Contains(bodies[0], oldEntry.Victim) {
		t.Fatalf("first Slack body does not contain oldest victim %q: %s", oldEntry.Victim, bodies[0])
	}
	if !strings.Contains(bodies[1], newEntry.Victim) {
		t.Fatalf("second Slack body does not contain newest victim %q: %s", newEntry.Victim, bodies[1])
	}
	for _, entry := range []api.RansomwareEntry{oldEntry, newEntry} {
		if !s.statusTracker.IsAPIItemSentToWebhook(api.GenerateEntryKey(entry), slackServer.URL) {
			t.Fatalf("entry %q was not marked sent to Slack", entry.ID)
		}
	}
	if s.statusTracker.IsAPIItemSentToWebhook(api.GenerateEntryKey(filteredEntry), slackServer.URL) {
		t.Fatal("filtered API entry was marked sent")
	}

	reloaded, err := status.NewTracker(s.config.DataDir)
	if err != nil {
		t.Fatalf("NewTracker(reload) error = %v", err)
	}
	for _, entry := range []api.RansomwareEntry{oldEntry, newEntry} {
		if !reloaded.IsAPIItemSentToWebhook(api.GenerateEntryKey(entry), slackServer.URL) {
			t.Fatalf("entry %q sent marker was not persisted", entry.ID)
		}
	}
	apiStatusJSON, err := os.ReadFile(filepath.Join(s.config.DataDir, "api_status.json"))
	if err != nil {
		t.Fatalf("ReadFile(api_status.json) error = %v", err)
	}
	if !strings.Contains(string(apiStatusJSON), `"entries_found": 4`) {
		t.Fatalf("api_status.json did not persist entries_found=4: %s", apiStatusJSON)
	}
}

func TestProcessAPIDeliveryTargetPersistsPendingPayloadBeforeSend(t *testing.T) {
	s := newTestScheduler(t)
	entry := api.RansomwareEntry{
		ID:         "pending-entry",
		Group:      "lockbit",
		Victim:     "Pending Corp",
		Country:    "DE",
		Discovered: time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC),
	}
	destinationID := apiDestinationID(status.MessengerSlack)
	checkedPendingBeforeSend := false
	target := apiDeliveryTarget{
		url:           "https://example.test/slack-webhook",
		destinationID: destinationID,
		messenger:     status.MessengerSlack.String(),
		send: func(_ context.Context, _ string, _ api.RansomwareEntry, _ *notifyfmt.FormatOptions) error {
			reloaded, err := status.NewTracker(s.config.DataDir)
			if err != nil {
				t.Fatalf("NewTracker(reload) error = %v", err)
			}
			records := reloaded.GetQueuedRetryItemsByType(status.RetryItemTypeAPI.String())
			if len(records) != 1 {
				t.Fatalf("queued API retry records before send = %d, want 1", len(records))
			}
			item := records[0].Item
			if item.ItemKey != api.GenerateEntryKey(entry) {
				t.Fatalf("pending item key = %q, want %q", item.ItemKey, api.GenerateEntryKey(entry))
			}
			if item.DestinationID != destinationID {
				t.Fatalf("pending destination = %q, want %q", item.DestinationID, destinationID)
			}
			if item.RetryCount != 0 {
				t.Fatalf("pending retry count = %d, want 0", item.RetryCount)
			}
			var queued api.RansomwareEntry
			if err := json.Unmarshal(item.Payload, &queued); err != nil {
				t.Fatalf("Unmarshal(pending payload) error = %v", err)
			}
			if queued.ID != entry.ID || queued.Victim != entry.Victim {
				t.Fatalf("pending payload = %#v, want entry %#v", queued, entry)
			}
			checkedPendingBeforeSend = true
			return nil
		},
	}

	s.processAPIDeliveryTarget(t.Context(), s.config, []api.RansomwareEntry{entry}, target)

	if !checkedPendingBeforeSend {
		t.Fatal("webhook send did not verify pending payload before delivery")
	}
	if records := s.statusTracker.GetQueuedRetryItemsByType(status.RetryItemTypeAPI.String()); len(records) != 0 {
		t.Fatalf("queued API retry records after successful send = %d, want 0", len(records))
	}
}

func TestProcessAPIDeliveryTargetAuditsFilterAndQuietHours(t *testing.T) {
	s := newTestScheduler(t)
	allowedEntry := api.RansomwareEntry{
		ID:         "quiet-hours-entry",
		Group:      "lockbit",
		Victim:     "Quiet Corp",
		Country:    "DE",
		Discovered: time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC),
	}
	filteredEntry := api.RansomwareEntry{
		ID:         "filtered-entry",
		Group:      "lockbit",
		Victim:     "Filtered Corp",
		Country:    "FR",
		Discovered: time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC),
	}
	target := apiDeliveryTarget{
		url:           "https://hooks.slack.test/services/api-audit",
		destinationID: apiDestinationID(status.MessengerSlack),
		messenger:     status.MessengerSlack.String(),
		filters:       &filter.Rules{IncludeCountries: []string{"DE"}},
		quietHours:    config.QuietHoursPolicy(activeQuietHoursUTCForTest()),
	}

	s.processAPIDeliveryTarget(t.Context(), s.config, []api.RansomwareEntry{allowedEntry, filteredEntry}, target)

	events := deliveryAuditEventsForTest(t, s.config.DataDir)
	if !hasDeliveryAuditEvent(
		events,
		api.GenerateEntryKey(filteredEntry),
		status.DeliveryAuditOutcomeFiltered,
		status.DeliveryAuditReasonFilterMismatch,
	) {
		t.Fatalf("missing filtered API audit event: %#v", events)
	}
	if !hasDeliveryAuditEvent(
		events,
		api.GenerateEntryKey(allowedEntry),
		status.DeliveryAuditOutcomeQuietHours,
		status.DeliveryAuditReasonQuietHours,
	) {
		t.Fatalf("missing quiet-hours API audit event: %#v", events)
	}
}

func TestMigrateLegacyAPIFetchedItemsQueuesPendingPayloads(t *testing.T) {
	s := newTestScheduler(t)
	s.config.SlackWebhooks.Ransomware.Enabled = true
	s.config.SlackWebhooks.Ransomware.URL = "https://hooks.slack.com/services/T12345678/B12345678/testtoken1234567890"

	entry := api.RansomwareEntry{
		ID:         "legacy-fetched-entry",
		Group:      "lockbit",
		Victim:     "Legacy Corp",
		Country:    "DE",
		Discovered: time.Date(2026, 1, 6, 9, 0, 0, 0, time.UTC),
	}
	legacyStatus := map[string]any{
		"last_updated":  time.Date(2026, 1, 6, 10, 0, 0, 0, time.UTC),
		"last_check":    time.Date(2026, 1, 6, 10, 0, 0, 0, time.UTC),
		"entries_found": 1,
		"sent_items":    map[string]any{},
		"fetched_items": []api.RansomwareEntry{entry},
	}
	data, err := json.MarshalIndent(legacyStatus, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent(legacyStatus) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(s.config.DataDir, "api_status.json"), append(data, '\n'), 0600); err != nil {
		t.Fatalf("WriteFile(api_status.json) error = %v", err)
	}
	reloaded, err := status.NewTracker(s.config.DataDir)
	if err != nil {
		t.Fatalf("NewTracker(reload) error = %v", err)
	}
	s.statusTracker = reloaded

	s.migrateLegacyAPIFetchedItemsForConfig(s.config)

	records := s.statusTracker.GetQueuedRetryItemsByType(status.RetryItemTypeAPI.String())
	if len(records) != 1 {
		t.Fatalf("queued API retry records after migration = %d, want 1", len(records))
	}
	item := records[0].Item
	if item.ItemKey != api.GenerateEntryKey(entry) {
		t.Fatalf("migrated item key = %q, want %q", item.ItemKey, api.GenerateEntryKey(entry))
	}
	if item.DestinationID != apiDestinationID(status.MessengerSlack) {
		t.Fatalf("migrated destination = %q, want %q", item.DestinationID, apiDestinationID(status.MessengerSlack))
	}
	var queued api.RansomwareEntry
	if err := json.Unmarshal(item.Payload, &queued); err != nil {
		t.Fatalf("Unmarshal(migrated payload) error = %v", err)
	}
	if queued.ID != entry.ID || queued.Victim != entry.Victim {
		t.Fatalf("migrated payload = %#v, want entry %#v", queued, entry)
	}
	apiStatusJSON, err := os.ReadFile(filepath.Join(s.config.DataDir, "api_status.json"))
	if err != nil {
		t.Fatalf("ReadFile(api_status.json) error = %v", err)
	}
	if strings.Contains(string(apiStatusJSON), "fetched_items") {
		t.Fatalf("api_status.json still contains legacy fetched_items: %s", apiStatusJSON)
	}
}

func TestCheckTimeoutHelpersUseConfiguredValuesWithDefaults(t *testing.T) {
	defaultCfg := config.DefaultConfig()
	if got := apiCheckTimeout(defaultCfg); got != config.DefaultAPICheckTimeout {
		t.Fatalf("apiCheckTimeout(default) = %v, want %v", got, config.DefaultAPICheckTimeout)
	}
	if got := rssCheckTimeout(defaultCfg); got != config.DefaultRSSCheckTimeout {
		t.Fatalf("rssCheckTimeout(default) = %v, want %v", got, config.DefaultRSSCheckTimeout)
	}

	cfg := config.DefaultConfig()
	cfg.APICheckTimeout = 7 * time.Minute
	cfg.RSSCheckTimeout = 12 * time.Minute
	if got := apiCheckTimeout(cfg); got != 7*time.Minute {
		t.Fatalf("apiCheckTimeout(configured) = %v, want 7m", got)
	}
	if got := rssCheckTimeout(cfg); got != 12*time.Minute {
		t.Fatalf("rssCheckTimeout(configured) = %v, want 12m", got)
	}

	zeroCfg := &config.Config{}
	if got := apiCheckTimeout(zeroCfg); got != config.DefaultAPICheckTimeout {
		t.Fatalf("apiCheckTimeout(zero) = %v, want %v", got, config.DefaultAPICheckTimeout)
	}
	if got := rssCheckTimeout(zeroCfg); got != config.DefaultRSSCheckTimeout {
		t.Fatalf("rssCheckTimeout(zero) = %v, want %v", got, config.DefaultRSSCheckTimeout)
	}
}

func TestCheckAPIOnceSkipsFetchWithoutRansomwareTargets(t *testing.T) {
	s := newTestScheduler(t)
	s.config.APIKey = "test-api-key"
	s.config.DiscordWebhooks.Ransomware = config.WebhookConfig{Enabled: false}
	s.config.SlackWebhooks.Ransomware = config.WebhookConfig{Enabled: false}

	var requestCount atomic.Int32
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer apiServer.Close()

	client, err := api.NewClientWithBaseURL("test-api-key", apiServer.URL)
	if err != nil {
		t.Fatalf("NewClientWithBaseURL() error = %v", err)
	}
	s.apiClient = client

	s.checkAPIOnce(t.Context())

	if got := requestCount.Load(); got != 0 {
		t.Fatalf("API request count = %d, want 0 without ransomware delivery targets", got)
	}
}

func TestCheckAPIOnceCapsAPIDeliveryPerCycle(t *testing.T) {
	s := newTestScheduler(t)
	s.config.APIKey = "test-api-key"
	s.config.APIMaxEntriesPerCycle = 2
	s.config.SlackWebhooks.Ransomware = config.WebhookConfig{
		Enabled: true,
		URL:     "https://hooks.slack.test/services/api-cap",
	}
	slackSender := &recordingWebhookSender{}
	s.slackWebhookSender = slackSender

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body := `{
			"client": "test",
			"count": 5,
			"order": "desc",
			"victims": [
				{"id":"entry-5","group":"lockbit","victim":"Corp 5","country":"DE","discovered":"2026-01-05 09:00:00"},
				{"id":"entry-4","group":"lockbit","victim":"Corp 4","country":"DE","discovered":"2026-01-04 09:00:00"},
				{"id":"entry-3","group":"lockbit","victim":"Corp 3","country":"DE","discovered":"2026-01-03 09:00:00"},
				{"id":"entry-2","group":"lockbit","victim":"Corp 2","country":"DE","discovered":"2026-01-02 09:00:00"},
				{"id":"entry-1","group":"lockbit","victim":"Corp 1","country":"DE","discovered":"2026-01-01 09:00:00"}
			]
		}`
		if _, err := w.Write([]byte(body)); err != nil {
			t.Errorf("write API response: %v", err)
		}
	}))
	defer apiServer.Close()

	client, err := api.NewClientWithBaseURL("test-api-key", apiServer.URL)
	if err != nil {
		t.Fatalf("NewClientWithBaseURL() error = %v", err)
	}
	s.apiClient = client

	s.checkAPIOnce(t.Context())

	calls := slackSender.apiCallsSnapshot()
	if len(calls) != 2 {
		t.Fatalf("API send attempts = %d, want 2", len(calls))
	}
	if calls[0].entryID != "entry-1" || calls[1].entryID != "entry-2" {
		t.Fatalf("API send order = %#v, want two oldest entries", calls)
	}
	for _, skippedID := range []string{"entry-3", "entry-4", "entry-5"} {
		if s.statusTracker.IsAPIItemSentToDestination("id:"+skippedID, "slack.ransomware", s.config.SlackWebhooks.Ransomware.URL) {
			t.Fatalf("capped API entry %q was marked sent", skippedID)
		}
	}
}

// newSingleVictimAPIServer serves one ransomware victim so shared-webhook-URL
// tests can observe how many targets deliver it.
func newSingleVictimAPIServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body := `{
			"client": "test",
			"count": 1,
			"order": "desc",
			"victims": [
				{"id":"entry-1","group":"lockbit","victim":"Corp 1","country":"DE","discovered":"2026-01-01 09:00:00"}
			]
		}`
		if _, err := w.Write([]byte(body)); err != nil {
			t.Errorf("write API response: %v", err)
		}
	}))
}

func TestCheckAPIOnceDeliversToBothTargetsSharingWebhookURL(t *testing.T) {
	s := newTestScheduler(t)
	s.config.APIKey = "test-api-key"
	sharedURL := "https://hooks.slack.test/services/shared-url"
	s.config.SlackWebhooks.Ransomware = config.WebhookConfig{
		Enabled: true,
		URL:     sharedURL,
		Targets: []config.WebhookTarget{{URL: sharedURL}},
	}
	slackSender := &recordingWebhookSender{}
	s.slackWebhookSender = slackSender

	apiServer := newSingleVictimAPIServer(t)
	defer apiServer.Close()

	client, err := api.NewClientWithBaseURL("test-api-key", apiServer.URL)
	if err != nil {
		t.Fatalf("NewClientWithBaseURL() error = %v", err)
	}
	s.apiClient = client

	s.checkAPIOnce(t.Context())

	calls := slackSender.apiCallsSnapshot()
	if len(calls) != 2 {
		t.Fatalf("API send attempts = %d (%#v), want 2 for two targets sharing one webhook URL", len(calls), calls)
	}
	for i, call := range calls {
		if call.webhookURL != sharedURL || call.entryID != "entry-1" {
			t.Fatalf("API send %d = %#v, want entry-1 to %q", i, call, sharedURL)
		}
	}
	for _, destinationID := range []string{"slack.ransomware", "slack.ransomware.2"} {
		snapshot, ok := s.statusTracker.APISentItemSnapshot("id:entry-1", destinationID)
		if !ok {
			t.Fatalf("APISentItemSnapshot(%q) missing sent marker", destinationID)
		}
		if snapshot.DestinationID != destinationID {
			t.Fatalf("marker for %q has DestinationID = %q, want %q", destinationID, snapshot.DestinationID, destinationID)
		}
	}
	assertSharedWebhookURLMarkersOnDisk(t, s.config.DataDir)
}

// assertSharedWebhookURLMarkersOnDisk pins the persisted shape the fix changes:
// three api_status.json sent markers (one per destination plus the legacy
// webhook-URL alias), every one of them carrying the destination that owns the
// delivery, and no webhook URL or token anywhere in the file.
func assertSharedWebhookURLMarkersOnDisk(t *testing.T, dataDir string) {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join(dataDir, "api_status.json"))
	if err != nil {
		t.Fatalf("ReadFile(api_status.json) error = %v", err)
	}

	var persisted struct {
		SentItems map[string]struct {
			DestinationID string `json:"destination_id"`
		} `json:"sent_items"`
	}
	if err := json.Unmarshal(raw, &persisted); err != nil {
		t.Fatalf("Unmarshal(api_status.json) error = %v: %s", err, raw)
	}
	if len(persisted.SentItems) != 3 {
		t.Fatalf("persisted sent markers = %d (%+v), want 3 (two destinations plus the legacy webhook-URL alias)",
			len(persisted.SentItems), persisted.SentItems)
	}
	owners := make(map[string]int, 2)
	for compositeKey, marker := range persisted.SentItems {
		switch marker.DestinationID {
		case "slack.ransomware", "slack.ransomware.2":
			owners[marker.DestinationID]++
		default:
			t.Fatalf("persisted marker %s has destination_id = %q, want one of the two delivering destinations",
				compositeKey, marker.DestinationID)
		}
	}
	if owners["slack.ransomware"] == 0 || owners["slack.ransomware.2"] == 0 {
		t.Fatalf("persisted marker owners = %+v, want both destinations represented", owners)
	}
	if strings.Contains(string(raw), "http") {
		t.Fatalf("api_status.json persisted a webhook URL: %s", raw)
	}
}

func TestCheckAPIOnceAppliesPerTargetFiltersOnSharedWebhookURL(t *testing.T) {
	s := newTestScheduler(t)
	s.config.APIKey = "test-api-key"
	sharedURL := "https://hooks.slack.test/services/shared-filtered"
	s.config.SlackWebhooks.Ransomware = config.WebhookConfig{
		Enabled: true,
		URL:     sharedURL,
		Targets: []config.WebhookTarget{{
			URL:     sharedURL,
			Filters: &config.WebhookFilters{IncludeCountries: []string{"FR"}},
		}},
	}
	slackSender := &recordingWebhookSender{}
	s.slackWebhookSender = slackSender

	apiServer := newSingleVictimAPIServer(t)
	defer apiServer.Close()

	client, err := api.NewClientWithBaseURL("test-api-key", apiServer.URL)
	if err != nil {
		t.Fatalf("NewClientWithBaseURL() error = %v", err)
	}
	s.apiClient = client

	s.checkAPIOnce(t.Context())

	calls := slackSender.apiCallsSnapshot()
	if len(calls) != 1 {
		t.Fatalf("API send attempts = %d (%#v), want 1 (second target filters the entry out)", len(calls), calls)
	}
	if calls[0].webhookURL != sharedURL || calls[0].entryID != "entry-1" {
		t.Fatalf("API send = %#v, want entry-1 to %q", calls[0], sharedURL)
	}
	if _, ok := s.statusTracker.APISentItemSnapshot("id:entry-1", "slack.ransomware"); !ok {
		t.Fatal("APISentItemSnapshot(slack.ransomware) missing sent marker for the delivering target")
	}
	if snapshot, ok := s.statusTracker.APISentItemSnapshot("id:entry-1", "slack.ransomware.2"); ok {
		t.Fatalf("filtered target slack.ransomware.2 recorded a delivery that never happened: %+v", snapshot)
	}

	events := deliveryAuditEventsForTest(t, s.config.DataDir)
	filteredForSecondTarget := false
	for _, event := range events {
		if event.ItemKey == "id:entry-1" &&
			event.DestinationID == "slack.ransomware.2" &&
			event.Outcome == status.DeliveryAuditOutcomeFiltered &&
			event.Reason == status.DeliveryAuditReasonFilterMismatch {
			filteredForSecondTarget = true
			break
		}
	}
	if !filteredForSecondTarget {
		t.Fatalf("no filtered/filter_mismatch audit event for slack.ransomware.2; events = %+v", events)
	}
}

func TestPendingAPIEntriesForWebhookSkipsDeadLetteredAPIItem(t *testing.T) {
	s := newTestScheduler(t)
	webhookURL := "https://hooks.slack.test/services/dead-letter"
	entry := api.RansomwareEntry{
		ID:     "dead-entry",
		Group:  "lockbit",
		Victim: "Dead Letter Corp",
	}
	key := api.GenerateEntryKey(entry)
	s.statusTracker.MarkRetryDeadLetter(key, "slack", "api", api.DisplayRansomwareTitle(entry), "terminal failure")

	unsent, filteredCount := s.pendingAPIEntriesForWebhook([]api.RansomwareEntry{entry}, webhookURL, "slack", "slack.ransomware", nil)

	if filteredCount != 0 {
		t.Fatalf("filteredCount = %d, want 0 for dead-letter skip", filteredCount)
	}
	if len(unsent) != 0 {
		t.Fatalf("dead-lettered API entry remained eligible for delivery: %#v", unsent)
	}
}

func TestCheckAPIOnceSkipsFetchWhenNoRansomwareWebhookEnabled(t *testing.T) {
	s := newTestScheduler(t)
	s.config.APIKey = "test-api-key"

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected API request to %s", r.URL.Path)
	}))
	defer apiServer.Close()

	client, err := api.NewClientWithBaseURL("test-api-key", apiServer.URL)
	if err != nil {
		t.Fatalf("NewClientWithBaseURL() error = %v", err)
	}
	s.apiClient = client

	s.checkAPIOnce(t.Context())

	if _, err := os.Stat(filepath.Join(s.config.DataDir, "api_status.json")); !os.IsNotExist(err) {
		t.Fatalf("api_status.json stat error = %v, want not exist", err)
	}
}

func TestCheckAPIOncePersistsAPIStatusWhenFetchFailsWithoutRetryWork(t *testing.T) {
	s := newTestScheduler(t)
	s.config.APIKey = "test-api-key"
	s.config.SlackWebhooks.Ransomware = config.WebhookConfig{
		Enabled: true,
		URL:     "https://hooks.slack.com/services/T000/B000/test",
	}

	apiRequests := 0
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiRequests++
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`<html>raw provider body</html>`))
	}))
	defer apiServer.Close()

	client, err := api.NewClientWithBaseURL("test-api-key", apiServer.URL)
	if err != nil {
		t.Fatalf("NewClientWithBaseURL() error = %v", err)
	}
	s.apiClient = client

	s.checkAPIOnce(t.Context())
	s.checkAPIOnce(t.Context())

	if apiRequests != 1 {
		t.Fatalf("API request count = %d, want 1 after auth failure suspension", apiRequests)
	}

	data, err := os.ReadFile(filepath.Join(s.config.DataDir, "api_status.json"))
	if err != nil {
		t.Fatalf("ReadFile(api_status.json) error = %v", err)
	}
	if !strings.Contains(string(data), `"entries_found": 0`) {
		t.Fatalf("api_status.json did not persist entries_found=0: %s", data)
	}
	if strings.Contains(string(data), "raw provider body") || strings.Contains(string(data), "<html>") {
		t.Fatalf("api_status.json persisted raw provider body: %s", data)
	}
	if !strings.Contains(string(data), "API authentication failed") {
		t.Fatalf("api_status.json did not persist normalized fetch error: %s", data)
	}
}

func TestSendRSSItemsToWebhookPersistsSentMarkerBeforeNextSend(t *testing.T) {
	s := newTestScheduler(t)
	feedURL := "https://example.test/feed.xml"
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	items := []status.StoredRSSEntry{
		{
			Key:        rss.GenerateEntryKey(feedURL, "guid-1", "", "RSS One"),
			FeedURL:    feedURL,
			Title:      "RSS One",
			GUID:       "guid-1",
			Published:  now.Format("2006-01-02 15:04:05.999999"),
			FeedTitle:  "Example Feed",
			Categories: []string{"security"},
		},
		{
			Key:        rss.GenerateEntryKey(feedURL, "guid-2", "", "RSS Two"),
			FeedURL:    feedURL,
			Title:      "RSS Two",
			GUID:       "guid-2",
			Published:  now.Add(time.Minute).Format("2006-01-02 15:04:05.999999"),
			FeedTitle:  "Example Feed",
			Categories: []string{"security"},
		},
	}

	firstKey := items[0].Key
	var serverURL string
	var mu sync.Mutex
	requestCount := 0
	errCh := make(chan string, 2)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requestCount++
		currentRequest := requestCount
		mu.Unlock()

		if r.Method != http.MethodPost {
			errCh <- fmt.Sprintf("request method = %s, want POST", r.Method)
		}
		if currentRequest == 2 {
			reloaded, err := status.NewTracker(s.config.DataDir)
			if err != nil {
				errCh <- fmt.Sprintf("NewTracker(reload) error = %v", err)
			} else if !reloaded.IsRSSItemSentToWebhook(firstKey, serverURL) {
				errCh <- "first RSS sent marker was not persisted before the second webhook send"
			}
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	serverURL = server.URL

	s.sendRSSItemsToWebhook(t.Context(), unsentRSSItemsForTest(t, items...), server.URL, "general", "slack", nil)

	mu.Lock()
	gotRequests := requestCount
	mu.Unlock()
	if gotRequests != 2 {
		t.Fatalf("webhook request count = %d, want 2", gotRequests)
	}
	select {
	case msg := <-errCh:
		t.Fatal(msg)
	default:
	}
}

func TestSendRSSItemsToWebhookDeadLettersInvalidStoredPublished(t *testing.T) {
	s := newTestScheduler(t)
	feedURL := "https://example.test/feed.xml"

	received := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- struct{}{}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	item := status.StoredRSSEntry{
		Key:       "rss-invalid-time",
		FeedURL:   feedURL,
		Title:     "Invalid Stored Time",
		Link:      "https://example.test/invalid-time",
		Published: "not-a-date",
		FeedTitle: "Example Feed",
	}

	s.sendRSSItemsToWebhook(t.Context(), unsentRSSItemsForTest(t, item), server.URL, "general", "slack", nil)

	select {
	case <-received:
		t.Fatal("webhook received RSS recovery item with invalid stored timestamp")
	default:
	}
	if !s.statusTracker.IsRetryDeadLettered(item.Key, "slack", "rss") {
		t.Fatal("invalid stored RSS timestamp was not marked dead-lettered")
	}

	data, err := os.ReadFile(filepath.Join(s.config.DataDir, "retry_status.json"))
	if err != nil {
		t.Fatalf("ReadFile(retry_status.json) error = %v", err)
	}
	got := string(data)
	if !strings.Contains(got, item.Key) || !strings.Contains(got, "invalid stored RSS published timestamp") {
		t.Fatalf("retry_status.json missing invalid timestamp dead-letter: %s", got)
	}
}

func TestSendRSSItemsToWebhookLogsRecoverySkipCounts(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()

	s := newTestScheduler(t)
	s.config.DiscordDelay = 0
	s.config.RSSMaxItemAge = 24 * time.Hour
	webhookURL := "not-a-discord-webhook-url"
	now := time.Now().UTC()
	published := func(t time.Time) string {
		return t.Format("2006-01-02 15:04:05.999999")
	}
	items := []status.StoredRSSEntry{
		{Key: "already", Title: "Already sent", FeedURL: "https://example.test/feed.xml", FeedTitle: "Feed", Published: published(now), Categories: []string{"allow"}},
		{Key: "stale", Title: "Stale", FeedURL: "https://example.test/feed.xml", FeedTitle: "Feed", Published: published(now.Add(-48 * time.Hour)), Categories: []string{"allow"}},
		{Key: "filtered", Title: "Filtered", FeedURL: "https://example.test/feed.xml", FeedTitle: "Feed", Published: published(now), Categories: []string{"deny"}},
		{Key: "stale-filtered", Title: "Stale Filtered", FeedURL: "https://example.test/feed.xml", FeedTitle: "Feed", Published: published(now.Add(-48 * time.Hour)), Categories: []string{"deny"}},
		{Key: "failed", Title: "Failed", FeedURL: "https://example.test/feed.xml", FeedTitle: "Feed", Published: published(now), Categories: []string{"allow"}},
	}
	s.statusTracker.MarkRSSItemSentToWebhook("already", "Already sent", "Feed", webhookURL)

	s.sendRSSItemsToWebhook(t.Context(), unsentRSSItemsForTest(t, items...), webhookURL, "general", "discord", &filter.Rules{
		IncludeCategories: []string{"allow"},
	})

	var summary *log.Entry
	for _, entry := range hook.AllEntries() {
		if entry.Message == "RSS items send to webhook completed" {
			summary = entry
		}
	}
	if summary == nil {
		t.Fatal("missing RSS recovery completion summary log")
	}
	for key, want := range map[string]int{
		"already_sent_count": 1,
		"stale_count":        1,
		"filtered_count":     2,
		"failed_count":       1,
		"total_sent":         0,
		"total_items":        5,
	} {
		if got, ok := summary.Data[key].(int); !ok || got != want {
			t.Fatalf("summary field %s = %#v, want %d; fields=%#v", key, summary.Data[key], want, summary.Data)
		}
	}
}

func TestSendParsedRSSToWebhooksDeliversAndPersistsSlackRSS(t *testing.T) {
	s := newTestScheduler(t)
	feedURL := "https://example.test/feed.xml"
	s.config.Feeds.GeneralFeeds = []string{feedURL}
	s.feedTypeMap = buildFeedTypeMap(s.config)

	var requestBodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("ReadAll(body) error = %v", err)
		}
		requestBodies = append(requestBodies, string(body))
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	s.config.SlackWebhooks.RSS = config.WebhookConfig{Enabled: true, URL: server.URL}

	published := time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC)
	entry := rss.Entry{
		Title:       "RSS Delivery Success",
		Link:        "https://example.test/articles/rss-delivery-success",
		GUID:        "rss-delivery-success",
		Description: "delivery body",
		FeedURL:     feedURL,
		FeedTitle:   "Example Feed",
		Published:   published,
	}
	feedResults := &rss.FeedResults{
		Entries:    map[string][]rss.Entry{feedURL: {entry}},
		FeedErrors: map[string]string{},
	}

	s.sendParsedRSSToWebhooks(t.Context(), feedResults, "general", s.getWebhookTargets("general"), nil)

	if len(requestBodies) != 1 {
		t.Fatalf("request count = %d, want 1", len(requestBodies))
	}
	if !strings.Contains(requestBodies[0], entry.Title) {
		t.Fatalf("Slack RSS body does not contain title %q: %s", entry.Title, requestBodies[0])
	}
	key := rss.GenerateEntryKey(entry.FeedURL, entry.GUID, entry.Link, entry.Title)
	contentSig := rss.GenerateEntryContentSignature(entry)
	if !s.statusTracker.IsRSSItemSentToWebhook(key, server.URL) {
		t.Fatal("RSS primary key was not marked sent")
	}
	if !s.statusTracker.IsRSSItemSentToWebhook(contentSig, server.URL) {
		t.Fatal("RSS content signature was not marked sent")
	}

	reloaded, err := status.NewTracker(s.config.DataDir)
	if err != nil {
		t.Fatalf("NewTracker(reload) error = %v", err)
	}
	if !reloaded.IsRSSItemSentToWebhook(key, server.URL) {
		t.Fatal("RSS primary sent marker was not persisted")
	}
}

func TestSendParsedRSSToWebhooksDeduplicatesSameArticleAcrossFeeds(t *testing.T) {
	s := newTestScheduler(t)
	firstFeedURL := "https://feed-a.example.test/feed.xml"
	secondFeedURL := "https://feed-b.example.test/feed.xml"
	s.config.Feeds.GeneralFeeds = []string{firstFeedURL, secondFeedURL}
	s.feedTypeMap = buildFeedTypeMap(s.config)

	var requestBodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("ReadAll(body) error = %v", err)
		}
		requestBodies = append(requestBodies, string(body))
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	s.config.SlackWebhooks.RSS = config.WebhookConfig{Enabled: true, URL: server.URL}

	published := time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC)
	first := rss.Entry{
		Title:       "Shared Cross Feed Advisory",
		Link:        "https://example.test/articles/shared-cross-feed-advisory",
		Description: "same body",
		FeedURL:     firstFeedURL,
		FeedTitle:   "Feed A",
		Published:   published,
	}
	second := first
	second.FeedURL = secondFeedURL
	second.FeedTitle = "Feed B"
	feedResults := &rss.FeedResults{
		Entries: map[string][]rss.Entry{
			firstFeedURL:  {first},
			secondFeedURL: {second},
		},
		FeedErrors: map[string]string{},
	}

	s.sendParsedRSSToWebhooks(t.Context(), feedResults, "general", s.getWebhookTargets("general"), nil)

	if len(requestBodies) != 1 {
		t.Fatalf("Slack request count = %d, want 1 for cross-feed duplicate; bodies=%#v", len(requestBodies), requestBodies)
	}
	firstKey := rss.GenerateEntryKeyForEntry(first)
	secondKey := rss.GenerateEntryKeyForEntry(second)
	if !s.statusTracker.IsRSSItemSentToWebhook(firstKey, server.URL) {
		t.Fatal("first feed primary key was not marked sent")
	}
	if !s.statusTracker.IsRSSItemSentToWebhook(secondKey, server.URL) {
		t.Fatal("second feed primary key was not marked sent after content-signature dedupe")
	}
}

func TestCheckRSSOnceParsesFeedAndDeliversSlackWebhook(t *testing.T) {
	s := newTestScheduler(t)

	title := "checkRSSOnce Slack integration"
	guid := "check-rss-once-slack"
	link := "https://example.test/articles/check-rss-once-slack"
	feedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("feed method = %s, want GET", r.Method)
		}
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = fmt.Fprint(w, schedulerRSSFeedXMLForTest(title, guid, link))
	}))
	defer feedServer.Close()
	allowLocalRSSFeedsForTest(t, s.rssParser, feedServer.Client())

	var mu sync.Mutex
	var requestBodies []string
	slackServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("Slack method = %s, want POST", r.Method)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("ReadAll(Slack body) error = %v", err)
		}
		mu.Lock()
		requestBodies = append(requestBodies, string(body))
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer slackServer.Close()

	s.config.Feeds.GeneralFeeds = []string{feedServer.URL}
	s.config.SlackWebhooks.RSS = config.WebhookConfig{Enabled: true, URL: slackServer.URL}
	s.config.MaxRSSWorkers = 1
	s.feedTypeMap = buildFeedTypeMap(s.config)

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	s.checkRSSOnce(ctx)

	mu.Lock()
	bodies := append([]string(nil), requestBodies...)
	mu.Unlock()
	if len(bodies) != 1 {
		t.Fatalf("Slack request count = %d, want 1; bodies=%#v", len(bodies), bodies)
	}
	if !strings.Contains(bodies[0], title) {
		t.Fatalf("Slack RSS body missing title %q: %s", title, bodies[0])
	}

	key := rss.GenerateEntryKey(feedServer.URL, guid, link, title)
	if !s.statusTracker.IsRSSItemSentToDestination(key, "slack.rss.general", slackServer.URL) {
		t.Fatal("checkRSSOnce RSS item was not marked sent to Slack destination")
	}
	reloaded, err := status.NewTracker(s.config.DataDir)
	if err != nil {
		t.Fatalf("NewTracker(reload) error = %v", err)
	}
	if !reloaded.IsRSSItemSentToDestination(key, "slack.rss.general", slackServer.URL) {
		t.Fatal("checkRSSOnce Slack sent marker was not persisted")
	}
}

func TestCheckRSSOnceUsesConditionalRSSHTTPValidators(t *testing.T) {
	s := newTestScheduler(t)

	title := "conditional RSS cache item"
	guid := "conditional-rss-cache"
	link := "https://example.test/articles/conditional-rss-cache"
	lastModified := "Wed, 21 Oct 2015 07:28:00 GMT"
	var feedRequests atomic.Int32
	feedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch feedRequests.Add(1) {
		case 1:
			if got := r.Header.Get("If-None-Match"); got != "" {
				t.Errorf("first If-None-Match = %q, want empty", got)
			}
			if got := r.Header.Get("If-Modified-Since"); got != "" {
				t.Errorf("first If-Modified-Since = %q, want empty", got)
			}
			w.Header().Set("Content-Type", "application/rss+xml")
			w.Header().Set("ETag", `"rss-v1"`)
			w.Header().Set("Last-Modified", lastModified)
			_, _ = fmt.Fprint(w, schedulerRSSFeedXMLForTest(title, guid, link))
		case 2:
			if got := r.Header.Get("If-None-Match"); got != `"rss-v1"` {
				t.Errorf("second If-None-Match = %q, want cached etag", got)
			}
			if got := r.Header.Get("If-Modified-Since"); got != lastModified {
				t.Errorf("second If-Modified-Since = %q, want %q", got, lastModified)
			}
			w.Header().Set("ETag", `"rss-v1"`)
			w.Header().Set("Last-Modified", lastModified)
			w.WriteHeader(http.StatusNotModified)
		default:
			t.Errorf("unexpected feed request")
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer feedServer.Close()
	allowLocalRSSFeedsForTest(t, s.rssParser, feedServer.Client())

	var slackRequests atomic.Int32
	slackServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slackRequests.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer slackServer.Close()

	s.config.Feeds.GeneralFeeds = []string{feedServer.URL}
	s.config.SlackWebhooks.RSS = config.WebhookConfig{Enabled: true, URL: slackServer.URL}
	s.config.MaxRSSWorkers = 1
	s.feedTypeMap = buildFeedTypeMap(s.config)

	for i := 0; i < 2; i++ {
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		s.checkRSSOnce(ctx)
		cancel()
	}

	if got := feedRequests.Load(); got != 2 {
		t.Fatalf("feed requests = %d, want 2", got)
	}
	if got := slackRequests.Load(); got != 1 {
		t.Fatalf("Slack requests = %d, want 1 after 304 second poll", got)
	}
	validators := s.statusTracker.GetRSSFeedHTTPValidators([]string{feedServer.URL})
	if got := validators[feedServer.URL]; got.ETag != `"rss-v1"` || got.LastModified != lastModified {
		t.Fatalf("cached validators = %+v, want etag and last-modified", got)
	}
}

func TestCheckRSSOnceParsesFeedTypesConcurrently(t *testing.T) {
	s := newTestScheduler(t)
	slackSender := &recordingWebhookSender{}
	s.slackWebhookSender = slackSender

	var started atomic.Int32
	bothStarted := make(chan struct{})
	releaseFeeds := make(chan struct{})
	newBlockingFeedServer := func(title, guid string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if started.Add(1) == 2 {
				close(bothStarted)
			}
			select {
			case <-releaseFeeds:
			case <-time.After(2 * time.Second):
				t.Errorf("timed out waiting for concurrent feed request")
			}
			w.Header().Set("Content-Type", "application/rss+xml")
			_, _ = fmt.Fprint(w, schedulerRSSFeedXMLForTest(title, guid, "https://example.test/"+guid))
		}))
	}

	generalFeed := newBlockingFeedServer("General Concurrent RSS", "general-concurrent")
	defer generalFeed.Close()
	governmentFeed := newBlockingFeedServer("Government Concurrent RSS", "government-concurrent")
	defer governmentFeed.Close()
	allowLocalRSSFeedsForTest(t, s.rssParser, generalFeed.Client())

	s.config.Feeds.GeneralFeeds = []string{generalFeed.URL}
	s.config.Feeds.GovernmentFeeds = []string{governmentFeed.URL}
	s.config.SlackWebhooks.RSS = config.WebhookConfig{Enabled: true, URL: "https://hooks.slack.test/services/rss"}
	s.config.SlackWebhooks.Government = config.WebhookConfig{Enabled: true, URL: "https://hooks.slack.test/services/government"}
	s.config.MaxRSSWorkers = 2
	s.feedTypeMap = buildFeedTypeMap(s.config)

	done := make(chan struct{})
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	go func() {
		defer close(done)
		s.checkRSSOnce(ctx)
	}()

	select {
	case <-bothStarted:
	case <-time.After(750 * time.Millisecond):
		close(releaseFeeds)
		<-done
		t.Fatal("RSS feed-type parsing was serialized; second feed request did not start while first was blocked")
	}
	close(releaseFeeds)
	<-done

	calls := slackSender.rssCallsSnapshot()
	if !hasRecordedRSSSend(calls, "https://hooks.slack.test/services/rss", "General Concurrent RSS", config.FeedTypeGeneral) {
		t.Fatalf("general RSS send missing after concurrent parse: %#v", calls)
	}
	if !hasRecordedRSSSend(calls, "https://hooks.slack.test/services/government", "Government Concurrent RSS", config.FeedTypeGovernment) {
		t.Fatalf("government RSS send missing after concurrent parse: %#v", calls)
	}
}

func TestCheckRSSOnceQuietHoursSuppressesSlackWebhookDelivery(t *testing.T) {
	s := newTestScheduler(t)

	title := "Quiet-hours checkRSSOnce RSS"
	guid := "quiet-hours-check-rss-once"
	link := "https://example.test/articles/quiet-hours-check-rss-once"
	feedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = fmt.Fprint(w, schedulerRSSFeedXMLForTest(title, guid, link))
	}))
	defer feedServer.Close()
	allowLocalRSSFeedsForTest(t, s.rssParser, feedServer.Client())

	var slackRequests atomic.Int32
	slackServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slackRequests.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer slackServer.Close()

	s.config.Feeds.GeneralFeeds = []string{feedServer.URL}
	s.config.SlackWebhooks.RSS = config.WebhookConfig{
		Enabled:    true,
		URL:        slackServer.URL,
		QuietHours: activeQuietHoursUTCForTest(),
	}
	s.config.MaxRSSWorkers = 1
	s.feedTypeMap = buildFeedTypeMap(s.config)

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	s.checkRSSOnce(ctx)

	if got := slackRequests.Load(); got != 0 {
		t.Fatalf("quiet-hours Slack request count = %d, want 0", got)
	}
	key := rss.GenerateEntryKey(feedServer.URL, guid, link, title)
	if s.statusTracker.IsRSSItemSentToDestination(key, "slack.rss.general", slackServer.URL) {
		t.Fatal("quiet-hours RSS item was marked sent without webhook delivery")
	}
	unsent := s.statusTracker.GetUnsentRSSItemsForDestination("slack.rss.general", slackServer.URL)
	if len(unsent) != 1 {
		t.Fatalf("unsent RSS item count = %d, want 1 during quiet hours: %#v", len(unsent), unsent)
	}
	if unsent[0].Title != title {
		t.Fatalf("unsent RSS title = %q, want %q", unsent[0].Title, title)
	}
	events := deliveryAuditEventsForTest(t, s.config.DataDir)
	if !hasDeliveryAuditEvent(
		events,
		key,
		status.DeliveryAuditOutcomeQuietHours,
		status.DeliveryAuditReasonQuietHours,
	) {
		t.Fatalf("missing RSS quiet-hours audit event: %#v", events)
	}
}

func TestSendRSSItemsToWebhookAuditsFilterSuppression(t *testing.T) {
	s := newTestScheduler(t)
	stored := status.StoredRSSEntry{
		Key:        "rss-filtered",
		FeedURL:    "https://example.test/feed.xml",
		FeedType:   config.FeedTypeGeneral,
		Title:      "Filtered RSS item",
		Link:       "https://example.test/article",
		Published:  time.Date(2026, 1, 6, 9, 0, 0, 0, time.UTC).Format(time.RFC3339),
		Categories: []string{"deny"},
		FeedTitle:  "Example Feed",
		ParsedAt:   time.Date(2026, 1, 6, 9, 0, 0, 0, time.UTC).Format(time.RFC3339),
	}
	target := webhookTarget{
		url:           "https://hooks.slack.test/services/rss-audit",
		destinationID: rssDestinationID(status.MessengerSlack, config.FeedTypeGeneral),
		messenger:     status.MessengerSlack.String(),
		filters:       &filter.Rules{IncludeCategories: []string{"allow"}},
	}

	s.sendRSSItemsToWebhookWithConfig(
		t.Context(),
		s.config,
		unsentRSSItemsForTest(t, stored),
		config.FeedTypeGeneral,
		target,
	)

	events := deliveryAuditEventsForTest(t, s.config.DataDir)
	if !hasDeliveryAuditEvent(
		events,
		stored.Key,
		status.DeliveryAuditOutcomeFiltered,
		status.DeliveryAuditReasonFilterMismatch,
	) {
		t.Fatalf("missing RSS filter audit event: %#v", events)
	}
}

func TestSendParsedRSSToWebhooksSendsUndatedSameTitleLinkVariants(t *testing.T) {
	s := newTestScheduler(t)
	feedURL := "https://example.test/feed.xml"
	s.config.Feeds.GeneralFeeds = []string{feedURL}
	s.feedTypeMap = buildFeedTypeMap(s.config)

	var requestCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	s.config.SlackWebhooks.RSS = config.WebhookConfig{Enabled: true, URL: server.URL}

	entries := []rss.Entry{
		{
			Title:   "Security Advisory",
			Link:    "https://example.test/advisory-one",
			GUID:    "advisory-one",
			FeedURL: feedURL,
		},
		{
			Title:   "Security Advisory",
			Link:    "https://example.test/advisory-two",
			GUID:    "advisory-two",
			FeedURL: feedURL,
		},
	}

	s.sendParsedRSSToWebhooks(t.Context(), &rss.FeedResults{
		Entries:    map[string][]rss.Entry{feedURL: entries},
		FeedErrors: map[string]string{},
	}, config.FeedTypeGeneral, s.getWebhookTargets(config.FeedTypeGeneral), nil)

	if requestCount != len(entries) {
		t.Fatalf("request count = %d, want %d", requestCount, len(entries))
	}
	for _, entry := range entries {
		key := rss.GenerateEntryKey(entry.FeedURL, entry.GUID, entry.Link, entry.Title)
		if !s.statusTracker.IsRSSItemSentToWebhook(key, server.URL) {
			t.Fatalf("entry %q primary key was not marked sent", entry.GUID)
		}
	}
}

func TestSendParsedRSSToWebhooksPersistsFeedFailureWithoutEntries(t *testing.T) {
	s := newTestScheduler(t)
	feedURL := "https://example.test/feed.xml"
	feedResults := &rss.FeedResults{
		Entries: map[string][]rss.Entry{
			feedURL: nil,
		},
		FeedErrors: map[string]string{
			feedURL: "failed to parse RSS feed after 1 attempts: failed to detect feed type\n<html>debug body</html>",
		},
	}

	s.sendParsedRSSToWebhooks(t.Context(), feedResults, "general", nil, nil)

	data, err := os.ReadFile(filepath.Join(s.config.DataDir, "rss_status.json"))
	if err != nil {
		t.Fatalf("ReadFile(rss_status.json) error = %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "RSS feed is not valid RSS or Atom XML") {
		t.Fatalf("rss_status.json did not persist normalized feed error: %s", data)
	}
	if strings.Contains(got, "\\n") || strings.Contains(got, "debug body") {
		t.Fatalf("rss_status.json persisted raw multiline feed error: %s", data)
	}
}

func TestRecordRSSBatchFailurePersistsEachFeedStatus(t *testing.T) {
	s := newTestScheduler(t)
	feedURLs := []string{
		"https://example.test/feed-one.xml",
		"https://example.test/feed-two.xml",
	}

	s.recordRSSBatchFailure(feedURLs, errors.New("worker pool start failed"))

	data, err := os.ReadFile(filepath.Join(s.config.DataDir, "rss_status.json"))
	if err != nil {
		t.Fatalf("ReadFile(rss_status.json) error = %v", err)
	}
	got := string(data)
	for _, feedURL := range feedURLs {
		if !strings.Contains(got, feedURL) {
			t.Fatalf("rss_status.json missing failed feed URL %q: %s", feedURL, got)
		}
	}
	if !strings.Contains(got, "worker pool start failed") {
		t.Fatalf("rss_status.json missing normalized batch failure: %s", got)
	}
}

func TestRecordRSSBatchFailureSkipsShutdownCancellation(t *testing.T) {
	s := newTestScheduler(t)

	s.recordRSSBatchFailure([]string{"https://example.test/feed.xml"}, context.Canceled)

	_, err := os.ReadFile(filepath.Join(s.config.DataDir, "rss_status.json"))
	if !os.IsNotExist(err) {
		t.Fatalf("rss_status.json read error = %v, want file to remain absent", err)
	}
}

func TestRecordRSSBatchFailureIgnoresCycleBudgetDeadline(t *testing.T) {
	s := newTestScheduler(t)

	s.recordRSSBatchFailure([]string{"https://example.test/feed-one.xml", "https://example.test/feed-two.xml"}, context.DeadlineExceeded)

	_, err := os.ReadFile(filepath.Join(s.config.DataDir, "rss_status.json"))
	if !os.IsNotExist(err) {
		t.Fatalf("rss_status.json read error = %v, want file to remain absent", err)
	}
}

func TestProcessRSSFeedBatchesSkipsFeedsInFailureCooldown(t *testing.T) {
	s := newTestScheduler(t)
	cooldownFeed := "https://example.test/broken.xml"
	activeFeed := "https://example.test/active.xml"
	for i := 0; i < 3; i++ {
		s.statusTracker.UpdateFeedStatus(cooldownFeed, false, 0, "temporary failure")
	}

	parser := &recordingRSSParser{}
	s.rssParser = parser
	s.processRSSFeedBatches(
		t.Context(),
		s.config,
		log.Fields{},
		[]rssFeedBatch{{
			route:    rssFeedRoute{feedType: config.FeedTypeGeneral},
			feedURLs: []string{cooldownFeed, activeFeed},
		}},
		[]string{cooldownFeed, activeFeed},
		nil,
	)

	if got, want := strings.Join(parser.feedURLs, ","), activeFeed; got != want {
		t.Fatalf("parser feed URLs = %q, want %q", got, want)
	}
}

func TestSendParsedRSSToWebhooksMarksFilteredRSSAsHandledForWebhook(t *testing.T) {
	s := newTestScheduler(t)
	feedURL := "https://example.test/feed.xml"
	webhookURL := "https://hooks.slack.com/services/T12345678/B12345678/filtertest123456"
	entry := rss.Entry{
		Title:      "Filtered RSS item",
		Link:       "https://example.test/filtered",
		Published:  time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Categories: []string{"deny"},
		FeedTitle:  "Example Feed",
		FeedURL:    feedURL,
	}
	key := rss.GenerateEntryKey(entry.FeedURL, entry.GUID, entry.Link, entry.Title)
	s.statusTracker.MarkRSSItemParsed(feedURL, key, status.StoredRSSEntry{
		Key:        key,
		FeedURL:    feedURL,
		Title:      entry.Title,
		Link:       entry.Link,
		Published:  entry.Published.Format("2006-01-02 15:04:05.999999"),
		Categories: entry.Categories,
		FeedTitle:  entry.FeedTitle,
	})

	s.sendParsedRSSToWebhooks(t.Context(), &rss.FeedResults{
		Entries: map[string][]rss.Entry{feedURL: {entry}},
	}, "general", []webhookTarget{
		{
			url:       webhookURL,
			messenger: "slack",
			filters:   &filter.Rules{IncludeCategories: []string{"allow"}},
		},
	}, nil)

	if unsent := s.statusTracker.GetUnsentRSSItemsForWebhook(webhookURL); len(unsent) != 0 {
		t.Fatalf("filtered RSS item remained in unsent recovery state: %#v", unsent)
	}
}

func TestSendParsedRSSToWebhooksAppliesRSSFiltersWithLocalWebhook(t *testing.T) {
	s := newTestScheduler(t)
	feedURL := "https://example.test/feed.xml"
	s.config.Feeds.GeneralFeeds = []string{feedURL}
	s.feedTypeMap = buildFeedTypeMap(s.config)

	var requestBodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("ReadAll(body) error = %v", err)
		}
		requestBodies = append(requestBodies, string(body))
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	s.config.SlackWebhooks.RSS = config.WebhookConfig{
		Enabled: true,
		URL:     server.URL,
		Filters: &config.WebhookFilters{
			IncludeCategories: []string{"security"},
			ExcludeKeywords:   []string{"draft"},
		},
	}

	allowed := rss.Entry{
		Title:      "Allowed security advisory",
		Link:       "https://example.test/allowed",
		GUID:       "allowed",
		FeedURL:    feedURL,
		FeedTitle:  "Example Feed",
		Published:  time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC),
		Categories: []string{"security"},
	}
	filtered := rss.Entry{
		Title:      "Draft security advisory",
		Link:       "https://example.test/draft",
		GUID:       "draft",
		FeedURL:    feedURL,
		FeedTitle:  "Example Feed",
		Published:  time.Date(2026, 1, 2, 10, 1, 0, 0, time.UTC),
		Categories: []string{"security"},
	}

	s.sendParsedRSSToWebhooks(t.Context(), &rss.FeedResults{
		Entries:    map[string][]rss.Entry{feedURL: {allowed, filtered}},
		FeedErrors: map[string]string{},
	}, config.FeedTypeGeneral, s.getWebhookTargets(config.FeedTypeGeneral), nil)

	if len(requestBodies) != 1 {
		t.Fatalf("request count = %d, want 1", len(requestBodies))
	}
	if !strings.Contains(requestBodies[0], allowed.Title) {
		t.Fatalf("Slack RSS body does not contain allowed title %q: %s", allowed.Title, requestBodies[0])
	}
	if strings.Contains(requestBodies[0], filtered.Title) {
		t.Fatalf("Slack RSS body contains filtered title %q: %s", filtered.Title, requestBodies[0])
	}
	allowedKey := rss.GenerateEntryKeyForEntry(allowed)
	filteredKey := rss.GenerateEntryKeyForEntry(filtered)
	if !s.statusTracker.IsRSSItemSentToDestination(allowedKey, "slack.rss.general", server.URL) {
		t.Fatal("allowed RSS item was not marked sent")
	}
	if unsent := s.statusTracker.GetUnsentRSSItemsForDestination("slack.rss.general", server.URL); len(unsent) != 0 {
		t.Fatalf("filtered RSS item remained unsent after filter handling; filtered key %q, unsent: %#v", filteredKey, unsent)
	}
}

func TestSendParsedRSSToWebhooksCapsFreshRSSPerTarget(t *testing.T) {
	s := newTestScheduler(t)
	slackSender := &recordingWebhookSender{}
	s.slackWebhookSender = slackSender

	feedURL := "https://example.test/feed.xml"
	webhookURL := "https://hooks.slack.test/services/rss-cap"
	s.config.RSSMaxEntriesPerCycle = 2
	s.config.SlackWebhooks.RSS = config.WebhookConfig{Enabled: true, URL: webhookURL}
	s.config.Feeds.GeneralFeeds = []string{feedURL}
	s.feedTypeMap = buildFeedTypeMap(s.config)

	base := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	entries := []rss.Entry{
		{Title: "old", Link: "https://example.test/old", GUID: "old", FeedURL: feedURL, FeedTitle: "Feed", Published: base},
		{Title: "middle", Link: "https://example.test/middle", GUID: "middle", FeedURL: feedURL, FeedTitle: "Feed", Published: base.Add(time.Minute)},
		{Title: "newest", Link: "https://example.test/newest", GUID: "newest", FeedURL: feedURL, FeedTitle: "Feed", Published: base.Add(2 * time.Minute)},
	}
	feedResults := &rss.FeedResults{
		Entries:    map[string][]rss.Entry{feedURL: entries},
		FeedErrors: map[string]string{},
	}
	attempted := rssDeliveryAttemptSet{}

	s.sendParsedRSSToWebhooks(t.Context(), feedResults, config.FeedTypeGeneral, s.getWebhookTargets(config.FeedTypeGeneral), attempted)

	calls := slackSender.rssCallsSnapshot()
	if len(calls) != 2 {
		t.Fatalf("fresh RSS send attempts = %d, want 2", len(calls))
	}
	if calls[0].entryTitle != "old" || calls[1].entryTitle != "middle" {
		t.Fatalf("fresh RSS send order = %#v, want two oldest entries", calls)
	}
	destinationID := "slack.rss.general"
	sentCount := 0
	for _, entry := range entries {
		key := rss.GenerateEntryKeyForEntry(entry)
		if s.statusTracker.IsRSSItemSentToDestination(key, destinationID, webhookURL) {
			sentCount++
		}
	}
	if sentCount != 2 {
		t.Fatalf("fresh RSS sent marker count = %d, want 2", sentCount)
	}
	cappedKey := rssDeliveryAttemptKey(rss.GenerateEntryKeyForEntry(entries[2]), destinationID)
	if _, ok := attempted[cappedKey]; !ok {
		t.Fatal("capped fresh RSS entry was not marked attempted for this cycle")
	}
}

func TestSendUnsentRSSItemsSkipsFreshFailuresFromSamePoll(t *testing.T) {
	s := newTestScheduler(t)
	slackSender := &recordingWebhookSender{rssErr: errors.New("provider failed")}
	s.slackWebhookSender = slackSender

	feedURL := "https://example.test/feed.xml"
	webhookURL := "https://hooks.slack.test/services/rss-failure"
	s.config.RetryMaxAttempts = 3
	s.config.RetryWindow = time.Hour
	s.config.SlackWebhooks.RSS = config.WebhookConfig{Enabled: true, URL: webhookURL}
	s.config.Feeds.GeneralFeeds = []string{feedURL}
	s.feedTypeMap = buildFeedTypeMap(s.config)

	published := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	entry := rss.Entry{
		Title:     "Fresh Failure",
		Link:      "https://example.test/fresh-failure",
		GUID:      "fresh-failure",
		FeedURL:   feedURL,
		FeedTitle: "Feed",
		Published: published,
	}
	itemKey := rss.GenerateEntryKeyForEntry(entry)
	s.statusTracker.MarkRSSItemParsed(feedURL, itemKey, status.StoredRSSEntry{
		Key:       itemKey,
		FeedURL:   feedURL,
		FeedType:  config.FeedTypeGeneral,
		Title:     entry.Title,
		Link:      entry.Link,
		GUID:      entry.GUID,
		FeedTitle: entry.FeedTitle,
		Published: published.Format(time.RFC3339Nano),
	})

	attempted := rssDeliveryAttemptSet{}
	s.sendParsedRSSToWebhooks(t.Context(), &rss.FeedResults{
		Entries:    map[string][]rss.Entry{feedURL: {entry}},
		FeedErrors: map[string]string{},
	}, config.FeedTypeGeneral, s.getWebhookTargets(config.FeedTypeGeneral), attempted)
	s.sendUnsentRSSItems(t.Context(), attempted)

	calls := slackSender.rssCallsSnapshot()
	if len(calls) != 1 {
		t.Fatalf("RSS send attempts in same poll = %d, want only the fresh attempt", len(calls))
	}
	if _, ok := attempted[rssDeliveryAttemptKey(itemKey, "slack.rss.general")]; !ok {
		t.Fatal("fresh RSS failure was not marked attempted for this cycle")
	}
	if retryItems := s.statusTracker.GetRetryItemsByType("rss"); len(retryItems) != 1 {
		t.Fatalf("RSS retry queue length = %d, want one queued item for a later poll", len(retryItems))
	}
}

func TestSendUnsentRSSItemsWithConfigUsesFeedSnapshot(t *testing.T) {
	s := newTestScheduler(t)
	feedURL := "https://snapshot.example.test/feed.xml"
	published := time.Now().UTC()
	item := status.StoredRSSEntry{
		Key:       rss.GenerateEntryKey(feedURL, "snapshot-guid", "", "Snapshot Feed Item"),
		FeedURL:   feedURL,
		Title:     "Snapshot Feed Item",
		GUID:      "snapshot-guid",
		Published: published.Format("2006-01-02 15:04:05.999999"),
		FeedTitle: "Snapshot Feed",
	}
	s.statusTracker.MarkRSSItemParsed(feedURL, item.Key, item)
	if err := s.statusTracker.SavePendingChanges(); err != nil {
		t.Fatalf("SavePendingChanges: %v", err)
	}

	received := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- struct{}{}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	snapshot := *s.config
	snapshot.Feeds.GeneralFeeds = []string{feedURL}
	snapshot.Feeds.GovernmentFeeds = nil
	snapshot.SlackWebhooks.RSS = config.WebhookConfig{Enabled: true, URL: server.URL}
	snapshot.SlackDelay = 0

	s.config.Feeds.GeneralFeeds = nil
	s.config.Feeds.GovernmentFeeds = []string{feedURL}
	s.config.SlackWebhooks.RSS = config.WebhookConfig{Enabled: false}
	s.feedTypeMap = buildFeedTypeMap(s.config)

	s.sendUnsentRSSItemsWithConfig(t.Context(), &snapshot, nil)

	select {
	case <-received:
	default:
		t.Fatal("RSS recovery did not use the provided config snapshot for feed routing and targets")
	}
	if !s.statusTracker.IsRSSItemSentToWebhook(item.Key, server.URL) {
		t.Fatal("snapshot-routed RSS item was not marked sent")
	}
}

func TestSendUnsentRSSItemsLogsRemovedFeedMapping(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()

	s := newTestScheduler(t)
	feedURL := "https://removed.example.test/feed.xml?token=secret"
	item := status.StoredRSSEntry{
		Key:       rss.GenerateEntryKey(feedURL, "removed-feed-guid", "", "Removed Feed Item"),
		FeedURL:   feedURL,
		Title:     "Removed Feed Item",
		GUID:      "removed-feed-guid",
		Published: time.Now().UTC().Format(time.RFC3339Nano),
		FeedTitle: "Removed Feed",
	}
	s.statusTracker.MarkRSSItemParsed(feedURL, item.Key, item)
	s.config.Feeds.GeneralFeeds = nil
	s.config.SlackWebhooks.RSS = config.WebhookConfig{Enabled: true, URL: "https://hooks.slack.test/services/rss"}
	s.feedTypeMap = buildFeedTypeMap(s.config)

	s.sendUnsentRSSItems(t.Context(), nil)

	logEntry := findTestLogEntry(hook, "Skipping RSS recovery item whose feed is no longer mapped to a feed type")
	if logEntry == nil {
		t.Fatal("missing removed-feed RSS recovery log")
	}
	if got := logEntry.Data["skip_reason"]; got != "unmapped_feed_type" {
		t.Fatalf("skip_reason = %#v, want unmapped_feed_type; fields=%#v", got, logEntry.Data)
	}
	if strings.Contains(fmt.Sprint(logEntry.Data), "secret") {
		t.Fatalf("removed-feed recovery log leaked sensitive feed query: %#v", logEntry.Data)
	}
}

func TestSendUnsentRSSItemsDeliversAndMarksRecoveredSlackRSS(t *testing.T) {
	s := newTestScheduler(t)
	feedURL := "https://example.test/feed.xml"
	s.config.Feeds.GeneralFeeds = []string{feedURL}
	s.feedTypeMap = buildFeedTypeMap(s.config)

	var requestBodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("ReadAll(body) error = %v", err)
		}
		requestBodies = append(requestBodies, string(body))
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	s.config.SlackWebhooks.RSS = config.WebhookConfig{Enabled: true, URL: server.URL}

	published := time.Date(2026, 1, 5, 11, 0, 0, 0, time.UTC)
	stored := status.StoredRSSEntry{
		Key:         rss.GenerateEntryKey(feedURL, "rss-recovery-success", "", "RSS Recovery Success"),
		FeedURL:     feedURL,
		Title:       "RSS Recovery Success",
		Link:        "https://example.test/articles/rss-recovery-success",
		Description: "recovery body",
		Published:   published.Format("2006-01-02 15:04:05.999999"),
		GUID:        "rss-recovery-success",
		FeedTitle:   "Example Feed",
		ParsedAt:    time.Now().UTC().Format(time.RFC3339),
	}
	s.statusTracker.MarkRSSItemParsed(feedURL, stored.Key, stored)
	if err := s.statusTracker.SavePendingChanges(); err != nil {
		t.Fatalf("SavePendingChanges: %v", err)
	}

	s.sendUnsentRSSItems(t.Context(), nil)

	if len(requestBodies) != 1 {
		t.Fatalf("request count = %d, want 1", len(requestBodies))
	}
	if !strings.Contains(requestBodies[0], stored.Title) {
		t.Fatalf("Slack RSS body does not contain title %q: %s", stored.Title, requestBodies[0])
	}
	if !s.statusTracker.IsRSSItemSentToWebhook(stored.Key, server.URL) {
		t.Fatal("recovered RSS item was not marked sent")
	}
	if retryItems := s.statusTracker.GetRetryItemsByType("rss"); len(retryItems) != 0 {
		t.Fatalf("RSS retry queue length = %d, want 0", len(retryItems))
	}

	reloaded, err := status.NewTracker(s.config.DataDir)
	if err != nil {
		t.Fatalf("NewTracker(reload) error = %v", err)
	}
	if !reloaded.IsRSSItemSentToWebhook(stored.Key, server.URL) {
		t.Fatal("recovered RSS sent marker was not persisted")
	}
}

func TestRSSQuietHoursPauseFreshSendAndResumeViaRecovery(t *testing.T) {
	s := newTestScheduler(t)
	feedURL := "https://example.test/feed.xml"
	s.config.Feeds.GeneralFeeds = []string{feedURL}
	s.feedTypeMap = buildFeedTypeMap(s.config)

	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	published := time.Date(2026, 1, 5, 11, 0, 0, 0, time.UTC)
	entry := rss.Entry{
		Title:       "Quiet Hours RSS",
		Link:        "https://example.test/articles/quiet-hours",
		Description: "quiet hours body",
		Published:   published,
		GUID:        "quiet-hours-rss",
		FeedTitle:   "Example Feed",
		FeedURL:     feedURL,
	}
	key := rss.GenerateEntryKeyForEntry(entry)
	s.statusTracker.MarkRSSItemParsed(feedURL, key, status.StoredRSSEntry{
		Key:         key,
		FeedURL:     feedURL,
		FeedType:    config.FeedTypeGeneral,
		Title:       entry.Title,
		Link:        entry.Link,
		Description: entry.Description,
		Published:   published.Format(time.RFC3339Nano),
		GUID:        entry.GUID,
		FeedTitle:   entry.FeedTitle,
		ParsedAt:    time.Now().UTC().Format(time.RFC3339Nano),
	})
	if err := s.statusTracker.SavePendingChanges(); err != nil {
		t.Fatalf("SavePendingChanges: %v", err)
	}

	now := time.Now().UTC()
	s.config.SlackWebhooks.RSS = config.WebhookConfig{
		Enabled: true,
		URL:     server.URL,
		QuietHours: &config.QuietHours{
			Enabled:  true,
			Start:    now.Add(-time.Hour).Format("15:04"),
			End:      now.Add(time.Hour).Format("15:04"),
			Timezone: "UTC",
		},
	}

	s.sendParsedRSSToWebhooks(t.Context(), &rss.FeedResults{
		Entries:    map[string][]rss.Entry{feedURL: {entry}},
		FeedErrors: map[string]string{},
	}, config.FeedTypeGeneral, s.getWebhookTargets(config.FeedTypeGeneral), nil)

	if got := requestCount.Load(); got != 0 {
		t.Fatalf("quiet-hours fresh RSS request count = %d, want 0", got)
	}
	if s.statusTracker.IsRSSItemSentToDestination(key, "slack.rss.general", server.URL) {
		t.Fatal("quiet-hours RSS item was marked sent before delivery")
	}

	s.config.SlackWebhooks.RSS.QuietHours = nil
	s.sendUnsentRSSItems(t.Context(), nil)

	if got := requestCount.Load(); got != 1 {
		t.Fatalf("post-quiet-hours RSS recovery request count = %d, want 1", got)
	}
	if !s.statusTracker.IsRSSItemSentToDestination(key, "slack.rss.general", server.URL) {
		t.Fatal("post-quiet-hours RSS recovery did not mark item sent")
	}
}

func TestSendUnsentRSSItemsCapsRecoveryPerTarget(t *testing.T) {
	s := newTestScheduler(t)
	slackSender := &recordingWebhookSender{}
	s.slackWebhookSender = slackSender

	feedURL := "https://example.test/feed.xml"
	webhookURL := "https://hooks.slack.test/services/rss-recovery-cap"
	destinationID := "slack.rss.general"
	s.config.RSSMaxEntriesPerCycle = 2
	s.config.SlackWebhooks.RSS = config.WebhookConfig{Enabled: true, URL: webhookURL}
	s.config.Feeds.GeneralFeeds = []string{feedURL}
	s.feedTypeMap = buildFeedTypeMap(s.config)

	base := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	items := []status.StoredRSSEntry{
		{Key: "rss-cap-1", FeedURL: feedURL, FeedType: config.FeedTypeGeneral, Title: "one", Link: "https://example.test/one", GUID: "one", Published: base.Format("2006-01-02 15:04:05.999999"), FeedTitle: "Feed"},
		{Key: "rss-cap-2", FeedURL: feedURL, FeedType: config.FeedTypeGeneral, Title: "two", Link: "https://example.test/two", GUID: "two", Published: base.Add(time.Minute).Format("2006-01-02 15:04:05.999999"), FeedTitle: "Feed"},
		{Key: "rss-cap-3", FeedURL: feedURL, FeedType: config.FeedTypeGeneral, Title: "three", Link: "https://example.test/three", GUID: "three", Published: base.Add(2 * time.Minute).Format("2006-01-02 15:04:05.999999"), FeedTitle: "Feed"},
	}
	for _, item := range items {
		s.statusTracker.MarkRSSItemParsed(feedURL, item.Key, item)
	}
	if err := s.statusTracker.SavePendingChanges(); err != nil {
		t.Fatalf("SavePendingChanges: %v", err)
	}

	s.sendUnsentRSSItems(t.Context(), nil)

	if calls := slackSender.rssCallCount(); calls != 2 {
		t.Fatalf("RSS recovery send attempts = %d, want 2", calls)
	}
	sentCount := 0
	for _, item := range items {
		if s.statusTracker.IsRSSItemSentToDestination(item.Key, destinationID, webhookURL) {
			sentCount++
		}
	}
	if sentCount != 2 {
		t.Fatalf("RSS recovery sent marker count = %d, want 2", sentCount)
	}
}

func TestSendUnsentRSSItemsSkipsDeadLetteredRSSItem(t *testing.T) {
	s := newTestScheduler(t)
	slackSender := &recordingWebhookSender{}
	s.slackWebhookSender = slackSender

	feedURL := "https://example.test/feed.xml"
	webhookURL := "https://hooks.slack.test/services/rss-dead-letter"
	s.config.SlackWebhooks.RSS = config.WebhookConfig{Enabled: true, URL: webhookURL}
	s.config.Feeds.GeneralFeeds = []string{feedURL}
	s.feedTypeMap = buildFeedTypeMap(s.config)

	item := status.StoredRSSEntry{
		Key:       "rss-dead-letter",
		FeedURL:   feedURL,
		FeedType:  config.FeedTypeGeneral,
		Title:     "dead lettered RSS",
		Link:      "https://example.test/dead",
		GUID:      "dead",
		Published: time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC).Format("2006-01-02 15:04:05.999999"),
		FeedTitle: "Feed",
	}
	s.statusTracker.MarkRSSItemParsed(feedURL, item.Key, item)
	s.statusTracker.MarkRetryDeadLetter(item.Key, "slack", "rss", item.Title, "terminal failure")
	if err := s.statusTracker.SavePendingChanges(); err != nil {
		t.Fatalf("SavePendingChanges: %v", err)
	}

	s.sendUnsentRSSItems(t.Context(), nil)

	if calls := slackSender.rssCallCount(); calls != 0 {
		t.Fatalf("dead-lettered RSS send attempts = %d, want 0", calls)
	}
	if s.statusTracker.IsRSSItemSentToDestination(item.Key, "slack.rss.general", webhookURL) {
		t.Fatal("dead-lettered RSS item was marked sent")
	}
}

func TestSendUnsentRSSItemsUsesStoredFeedTypeBeforeCurrentFeedMap(t *testing.T) {
	s := newTestScheduler(t)
	feedURL := "https://example.test/moved-feed.xml"
	s.config.Feeds.GeneralFeeds = []string{feedURL}
	s.config.Feeds.GovernmentFeeds = nil
	s.config.Feeds.RansomwareFeeds = nil
	s.feedTypeMap = buildFeedTypeMap(s.config)

	var requestBodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("ReadAll(body) error = %v", err)
		}
		requestBodies = append(requestBodies, string(body))
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	s.config.SlackWebhooks.RSS = config.WebhookConfig{Enabled: false}
	s.config.SlackWebhooks.Government = config.WebhookConfig{Enabled: true, URL: server.URL}

	published := time.Date(2026, 1, 5, 11, 0, 0, 0, time.UTC)
	stored := status.StoredRSSEntry{
		Key:         rss.GenerateEntryKey(feedURL, "rss-moved-feed", "", "Moved Feed Alert"),
		FeedURL:     feedURL,
		FeedType:    config.FeedTypeGovernment,
		Title:       "Moved Feed Alert",
		Link:        "https://example.test/articles/moved-feed-alert",
		Description: "government recovery body",
		Published:   published.Format(time.RFC3339Nano),
		GUID:        "rss-moved-feed",
		FeedTitle:   "Moved Feed",
		ParsedAt:    time.Now().UTC().Format(time.RFC3339Nano),
	}
	s.statusTracker.MarkRSSItemParsed(feedURL, stored.Key, stored)
	if err := s.statusTracker.SavePendingChanges(); err != nil {
		t.Fatalf("SavePendingChanges: %v", err)
	}

	s.sendUnsentRSSItems(t.Context(), nil)

	if len(requestBodies) != 1 {
		t.Fatalf("request count = %d, want 1", len(requestBodies))
	}
	if !strings.Contains(requestBodies[0], stored.Title) {
		t.Fatalf("Slack RSS body does not contain title %q: %s", stored.Title, requestBodies[0])
	}
	if !s.statusTracker.IsRSSItemSentToDestination(stored.Key, "slack.rss.government", server.URL) {
		t.Fatal("recovered RSS item was not marked sent to government destination")
	}
}

// Verify the Start/Stop lifecycle completes without panics or hangs.
func TestSchedulerStartStop(t *testing.T) {
	s := newTestScheduler(t)

	// Use a cancellable context so the initial checkAPIOnce / checkRSSOnce
	// that Start() fires can return quickly (no API key set, no feeds).
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start should not panic
	if err := s.Start(ctx); err != nil {
		t.Fatalf("Start() returned error: %v", err)
	}

	// Stop within a reasonable timeout -- if Stop() hangs, the test will
	// be killed by the testing framework's timeout.
	done := make(chan struct{})
	go func() {
		if err := s.Stop(); err != nil {
			t.Errorf("Stop: %v", err)
		}
		close(done)
	}()

	waitForTestSignalWithTimeout(t, done, 10*time.Second, "Stop() clean shutdown")
}

func TestSchedulerStartLogsReadyAfterInitialChecksComplete(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()

	s := newTestScheduler(t)
	s.config.APIKey = ""
	s.config.Feeds.GeneralFeeds = nil
	s.config.Feeds.GovernmentFeeds = nil
	s.config.Feeds.RansomwareFeeds = nil
	s.feedTypeMap = buildFeedTypeMap(s.config)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := s.Start(ctx); err != nil {
		t.Fatalf("Start() returned error: %v", err)
	}
	if err := s.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	completedIndex := -1
	startedIndex := -1
	for i, entry := range hook.AllEntries() {
		switch entry.Message {
		case "Initial scheduler checks completed":
			completedIndex = i
		case "Scheduler started successfully":
			startedIndex = i
		}
	}
	if completedIndex == -1 {
		t.Fatal("missing initial checks completion log")
	}
	if startedIndex == -1 {
		t.Fatal("missing scheduler started log")
	}
	if startedIndex < completedIndex {
		t.Fatalf("scheduler started log index %d came before initial checks completion index %d", startedIndex, completedIndex)
	}
}

func TestSchedulerRunOnceDryRunReturns(t *testing.T) {
	s := newTestScheduler(t)
	s.dryRun = true
	s.config.APIKey = ""
	s.config.Feeds.GeneralFeeds = nil
	s.config.Feeds.GovernmentFeeds = nil
	s.config.Feeds.RansomwareFeeds = nil
	s.feedTypeMap = buildFeedTypeMap(s.config)

	done := make(chan struct{})
	go func() {
		s.RunOnce(context.Background())
		close(done)
	}()

	waitForTestSignalWithTimeout(t, done, 2*time.Second, "RunOnce to return")
}

func TestNewDryRunUsesInMemoryStatusTracker(t *testing.T) {
	dataPath := filepath.Join(t.TempDir(), "data-file")
	if err := os.WriteFile(dataPath, []byte("not a directory"), 0600); err != nil {
		t.Fatalf("WriteFile(dataPath) error = %v", err)
	}
	cfg := config.DefaultConfig()
	cfg.DataDir = dataPath
	cfg.APIKey = ""
	cfg.Feeds.GeneralFeeds = nil
	cfg.Feeds.GovernmentFeeds = nil
	cfg.Feeds.RansomwareFeeds = nil

	s, err := New(cfg, t.TempDir(), true)
	if err != nil {
		t.Fatalf("New(dryRun=true) error = %v", err)
	}
	defer func() {
		if err := s.Stop(); err != nil {
			t.Errorf("Stop: %v", err)
		}
	}()

	s.RunOnce(t.Context())
	if info, err := os.Stat(dataPath); err != nil {
		t.Fatalf("Stat(dataPath) error = %v", err)
	} else if info.IsDir() {
		t.Fatal("dry-run converted data file into a directory")
	}
}

func TestRunOnceDryRunParsesRSSWithoutWebhookOrStatusSideEffects(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()
	previousLevel := log.GetLevel()
	log.SetLevel(log.InfoLevel)
	defer log.SetLevel(previousLevel)

	title := "RunOnce dry-run RSS"
	guid := "run-once-dry-run-rss"
	link := "https://example.test/articles/run-once-dry-run-rss"
	feedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = fmt.Fprint(w, schedulerRSSFeedXMLForTest(title, guid, link))
	}))
	defer feedServer.Close()

	var slackRequests atomic.Int32
	slackServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slackRequests.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer slackServer.Close()

	dataDir := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.APIKey = ""
	cfg.DataDir = dataDir
	cfg.RSSRetryCount = 0
	cfg.RSSWorkerTimeout = time.Second
	cfg.RSSCheckTimeout = 2 * time.Second
	cfg.MaxRSSWorkers = 1
	cfg.Feeds.GeneralFeeds = []string{feedServer.URL}
	cfg.Feeds.GovernmentFeeds = nil
	cfg.Feeds.RansomwareFeeds = nil
	cfg.SlackWebhooks.RSS = config.WebhookConfig{Enabled: true, URL: slackServer.URL}

	s, err := New(cfg, t.TempDir(), true)
	if err != nil {
		t.Fatalf("New(dryRun=true) error = %v", err)
	}
	defer func() {
		if err := s.Stop(); err != nil {
			t.Errorf("Stop: %v", err)
		}
	}()
	allowLocalRSSFeedsForTest(t, s.rssParser, feedServer.Client())

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	s.RunOnce(ctx)

	if got := slackRequests.Load(); got != 0 {
		t.Fatalf("dry-run Slack request count = %d, want 0", got)
	}
	logEntry := findTestLogEntry(hook, "[DRY-RUN] Would send RSS entry to webhook")
	if logEntry == nil {
		t.Fatal("missing dry-run RSS preview log from RunOnce")
	}
	if got, want := logEntry.Data["item_key"], rss.GenerateEntryKey(feedServer.URL, guid, link, title); got != want {
		t.Fatalf("dry-run preview item_key = %#v, want %q; fields=%#v", got, want, logEntry.Data)
	}
	if _, ok := logEntry.Data["rendered_preview"].(map[string]any); !ok {
		t.Fatalf("dry-run rendered_preview missing or non-map: %#v", logEntry.Data["rendered_preview"])
	}
	for _, name := range []string{"api_status.json", "rss_status.json", "retry_status.json"} {
		path := filepath.Join(dataDir, name)
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("dry-run created %s; stat error = %v", path, err)
		}
	}
}

func TestSchedulerDryRunStopDoesNotEmitStopLifecycleEvents(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()
	previousLevel := log.GetLevel()
	log.SetLevel(log.DebugLevel)
	defer log.SetLevel(previousLevel)

	s := newTestScheduler(t)
	s.dryRun = true
	s.config.APIKey = ""

	s.RunOnce(context.Background())
	if err := s.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	for _, entry := range hook.AllEntries() {
		switch entry.Message {
		case "Stopping scheduler", "All goroutines stopped gracefully", "Scheduler stopped":
			t.Fatalf("dry-run cleanup emitted scheduler lifecycle stop event %q", entry.Message)
		}
	}
}

func TestDryRunAPINoUnsentItemsLogsInfoSummary(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()
	previousLevel := log.GetLevel()
	log.SetLevel(log.InfoLevel)
	defer log.SetLevel(previousLevel)

	s := newTestScheduler(t)
	s.dryRun = true
	target := apiDeliveryTarget{
		destinationID: "discord.ransomware",
		messenger:     "discord",
	}

	s.processAPIDeliveryTarget(t.Context(), s.config, nil, target)

	entry := findTestLogEntry(hook, "[DRY-RUN] No API entries to preview")
	if entry == nil {
		t.Fatal("missing dry-run API empty-state summary")
	}
	if entry.Level != log.InfoLevel {
		t.Fatalf("empty-state level = %v, want info", entry.Level)
	}
	for key, want := range map[string]any{
		"destination_id":        "discord.ransomware",
		"messenger":             "discord",
		"item_type":             status.RetryItemTypeAPI.String(),
		"total_fetched":         0,
		"unsent_count":          0,
		"matching_unsent_count": 0,
		"filtered_count":        0,
		"preview_count":         0,
	} {
		if got := entry.Data[key]; got != want {
			t.Fatalf("field %s = %#v, want %#v; fields=%#v", key, got, want, entry.Data)
		}
	}
	if entry.Data["hint"] == "" {
		t.Fatalf("missing operator hint in fields=%#v", entry.Data)
	}
}

func TestDryRunAPIPreviewsRenderedDiscordHierarchy(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()
	previousLevel := log.GetLevel()
	log.SetLevel(log.InfoLevel)
	defer log.SetLevel(previousLevel)

	s := newTestScheduler(t)
	s.dryRun = true
	s.config.Format.FieldOrder = []string{"group", "victim", "country", "attack_date", "discovered", "description"}
	entry := api.RansomwareEntry{
		Group:       "LockBit",
		Victim:      "Example Corp",
		Country:     "DE",
		AttackDate:  "2026-02-03 04:05:06",
		Discovered:  time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC),
		Description: "Preview description",
	}
	target := apiDeliveryTarget{
		destinationID: "discord.ransomware",
		messenger:     "discord",
	}

	s.sendAPIEntriesIndividually(t.Context(), []api.RansomwareEntry{entry}, target)

	logEntry := findTestLogEntry(hook, "[DRY-RUN] Would send ransomware entry")
	if logEntry == nil {
		t.Fatal("missing dry-run ransomware preview log")
	}
	preview, ok := logEntry.Data["rendered_preview"].(map[string]any)
	if !ok {
		t.Fatalf("rendered_preview = %#v, want map", logEntry.Data["rendered_preview"])
	}
	if got := preview["platform"]; got != "discord" {
		t.Fatalf("preview platform = %#v, want discord", got)
	}
	if title := fmt.Sprint(preview["title"]); !strings.Contains(title, "LockBit") {
		t.Fatalf("preview title = %q, want group identity", title)
	}
	fields, ok := preview["fields"].([]map[string]any)
	if !ok || len(fields) == 0 {
		t.Fatalf("preview fields = %#v, want ordered field preview", preview["fields"])
	}
	if !previewFieldsContain(fields, "Group", "LockBit") {
		t.Fatalf("preview fields missing group hierarchy: %#v", fields)
	}
}

func TestDryRunRSSNoNewEntriesLogsInfoSummary(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()
	previousLevel := log.GetLevel()
	log.SetLevel(log.InfoLevel)
	defer log.SetLevel(previousLevel)

	s := newTestScheduler(t)
	s.dryRun = true
	feedResults := &rss.FeedResults{
		Entries:    map[string][]rss.Entry{"https://example.test/feed.xml": {}},
		FeedErrors: map[string]string{},
	}
	targets := []webhookTarget{{destinationID: "discord.rss.general", messenger: "discord"}}

	s.sendParsedRSSToWebhooksWithConfig(t.Context(), s.config, feedResults, config.FeedTypeGeneral, targets, nil)

	entry := findTestLogEntry(hook, "[DRY-RUN] No RSS entries to preview")
	if entry == nil {
		t.Fatal("missing dry-run RSS empty-state summary")
	}
	for key, want := range map[string]any{
		"feed_type":       config.FeedTypeGeneral,
		"feed_count":      1,
		"target_count":    1,
		"new_entry_count": 0,
	} {
		if got := entry.Data[key]; got != want {
			t.Fatalf("field %s = %#v, want %#v; fields=%#v", key, got, want, entry.Data)
		}
	}
	if entry.Data["hint"] == "" {
		t.Fatalf("missing operator hint in fields=%#v", entry.Data)
	}
}

func TestDryRunRSSPreviewsRenderedSlackHierarchy(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()
	previousLevel := log.GetLevel()
	log.SetLevel(log.InfoLevel)
	defer log.SetLevel(previousLevel)

	s := newTestScheduler(t)
	s.dryRun = true
	feedResults := &rss.FeedResults{
		Entries: map[string][]rss.Entry{
			"https://example.test/feed.xml": {
				{
					Title:       "Example Advisory",
					Link:        "https://example.test/advisory",
					Description: "Preview RSS description",
					FeedTitle:   "Example Feed",
					FeedURL:     "https://example.test/feed.xml",
					Published:   time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC),
				},
			},
		},
		FeedErrors: map[string]string{},
	}
	targets := []webhookTarget{{destinationID: "slack.rss.general", messenger: "slack"}}

	s.sendParsedRSSToWebhooksWithConfig(t.Context(), s.config, feedResults, config.FeedTypeGeneral, targets, nil)

	logEntry := findTestLogEntry(hook, "[DRY-RUN] Would send RSS entry to webhook")
	if logEntry == nil {
		t.Fatal("missing dry-run RSS preview log")
	}
	preview, ok := logEntry.Data["rendered_preview"].(map[string]any)
	if !ok {
		t.Fatalf("rendered_preview = %#v, want map", logEntry.Data["rendered_preview"])
	}
	if got := preview["platform"]; got != "slack" {
		t.Fatalf("preview platform = %#v, want slack", got)
	}
	blocks, ok := preview["blocks"].([]map[string]any)
	if !ok || len(blocks) == 0 {
		t.Fatalf("preview blocks = %#v, want Slack block hierarchy", preview["blocks"])
	}
	if !previewBlocksContain(blocks, "Example Advisory") {
		t.Fatalf("preview blocks missing RSS title: %#v", blocks)
	}
}

func TestSendParsedRSSToWebhooksStartsIndependentTargetsConcurrently(t *testing.T) {
	s := newTestScheduler(t)
	discordSender := newBlockingRSSWebhookSender()
	slackSender := newNotifyingRSSWebhookSender()
	s.discordWebhookSender = discordSender
	s.slackWebhookSender = slackSender

	feedResults := &rss.FeedResults{
		Entries: map[string][]rss.Entry{
			"https://example.test/feed.xml": {
				{
					Title:     "Concurrent advisory",
					Link:      "https://example.test/advisory",
					FeedURL:   "https://example.test/feed.xml",
					Published: time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC),
				},
			},
		},
		FeedErrors: map[string]string{},
	}
	targets := []webhookTarget{
		{
			url:           "https://discord.test/webhook",
			destinationID: "discord.rss.general",
			messenger:     "discord",
		},
		{
			url:           "https://slack.test/webhook",
			destinationID: "slack.rss.general",
			messenger:     "slack",
		},
	}

	done := make(chan struct{})
	go func() {
		s.sendParsedRSSToWebhooksWithConfig(t.Context(), s.config, feedResults, config.FeedTypeGeneral, targets, nil)
		close(done)
	}()

	waitForTestSignal(t, discordSender.started, "discord RSS send to start")
	if !waitForTestSignalWithin(slackSender.started, 150*time.Millisecond) {
		close(discordSender.release)
		waitForTestSignal(t, done, "RSS fan-out to finish after releasing blocked sender")
		t.Fatal("slack RSS send did not start while discord RSS send was blocked")
	}
	close(discordSender.release)
	waitForTestSignal(t, done, "RSS fan-out to finish")
}

func TestSendUnsentRSSItemsStartsIndependentTargetsConcurrently(t *testing.T) {
	s := newTestScheduler(t)
	discordSender := newBlockingRSSWebhookSender()
	slackSender := newNotifyingRSSWebhookSender()
	s.discordWebhookSender = discordSender
	s.slackWebhookSender = slackSender

	feedURL := "https://example.test/feed.xml"
	s.config.Feeds.GeneralFeeds = []string{feedURL}
	s.config.DiscordWebhooks.RSS = config.WebhookConfig{
		Enabled: true,
		URL:     "https://discord.test/webhook",
	}
	s.config.SlackWebhooks.RSS = config.WebhookConfig{
		Enabled: true,
		URL:     "https://slack.test/webhook",
	}

	item := status.StoredRSSEntry{
		Key:       "concurrent-recovery",
		FeedURL:   feedURL,
		FeedType:  config.FeedTypeGeneral,
		Title:     "Concurrent recovery advisory",
		Link:      "https://example.test/recovery",
		Published: time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC).Format(time.RFC3339Nano),
	}
	s.statusTracker.MarkRSSItemParsed(feedURL, item.Key, item)

	done := make(chan struct{})
	go func() {
		s.sendUnsentRSSItemsWithConfig(t.Context(), s.config, nil)
		close(done)
	}()

	waitForTestSignal(t, discordSender.started, "discord RSS recovery send to start")
	if !waitForTestSignalWithin(slackSender.started, 150*time.Millisecond) {
		close(discordSender.release)
		waitForTestSignal(t, done, "RSS recovery fan-out to finish after releasing blocked sender")
		t.Fatal("slack RSS recovery send did not start while discord RSS recovery send was blocked")
	}
	close(discordSender.release)
	waitForTestSignal(t, done, "RSS recovery fan-out to finish")
}

func TestProcessAPIRetryQueueCapsAttemptsPerCycle(t *testing.T) {
	s := newTestScheduler(t)
	slackSender := &recordingWebhookSender{}
	s.slackWebhookSender = slackSender
	s.config.APIMaxEntriesPerCycle = 2
	s.config.RetryMaxAttempts = 5
	s.config.RetryWindow = time.Hour
	s.config.SlackWebhooks.Ransomware = config.WebhookConfig{
		Enabled: true,
		URL:     "https://slack.test/webhook",
	}

	for i := 0; i < 3; i++ {
		entry := api.RansomwareEntry{
			ID:     fmt.Sprintf("retry-%d", i),
			Group:  "LockBit",
			Victim: fmt.Sprintf("Example %d", i),
		}
		payload, err := json.Marshal(entry)
		if err != nil {
			t.Fatalf("Marshal(entry) error = %v", err)
		}
		key := api.GenerateEntryKey(entry)
		if !enqueueRetryForTest(t, s.statusTracker,
			key,
			"slack.ransomware",
			"slack",
			"api",
			api.DisplayRansomwareTitle(entry),
			"previous failure",
			s.config.RetryMaxAttempts,
			s.config.RetryWindow,
			payload,
		) {
			t.Fatalf("EnqueueRetryForDestination(%q) moved item to dead letter", key)
		}
	}

	s.processAPIRetryQueue(t.Context(), nil)

	if calls := slackSender.apiCallsSnapshot(); len(calls) != 2 {
		t.Fatalf("API retry send attempts = %d, want 2 capped attempts: %#v", len(calls), calls)
	}
	if remaining := s.statusTracker.GetQueuedRetryItemsByType("api"); len(remaining) != 1 {
		t.Fatalf("remaining API retry queue length = %d, want 1", len(remaining))
	}
}

func TestSendWithWebhookLockSerializesSameMessengerURL(t *testing.T) {
	s := newTestScheduler(t)

	ctx := t.Context()
	webhookURL := "https://hooks.slack.test/services/shared"
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	done := make(chan error, 2)
	var active int32

	send := func() error {
		if current := atomic.AddInt32(&active, 1); current != 1 {
			t.Errorf("concurrent webhook sends active = %d, want 1", current)
		}
		entered <- struct{}{}
		<-release
		atomic.AddInt32(&active, -1)
		return nil
	}

	go func() {
		done <- s.sendWithWebhookLock(ctx, "slack", webhookURL, send)
	}()
	waitForTestSignal(t, entered, "first webhook send")

	go func() {
		done <- s.sendWithWebhookLock(ctx, "slack", webhookURL, send)
	}()
	if waitForTestSignalWithin(entered, 50*time.Millisecond) {
		t.Fatal("second webhook send entered while first send held the same URL lock")
	}

	close(release)
	waitForTestSignal(t, entered, "second webhook send")
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			t.Fatalf("sendWithWebhookLock() error = %v", err)
		}
	}
}

func TestProcessAPIRetryQueueRunsIndependentDestinationsConcurrently(t *testing.T) {
	s := newTestScheduler(t)
	s.config.APIMaxEntriesPerCycle = 10
	s.config.DiscordDelay = 0
	s.config.SlackDelay = 0
	discordWebhookURL := "https://discord.com/api/webhooks/123456789012345678/retry-token"
	slackWebhookURL := "https://hooks.slack.com/services/T12345678/B12345678/retry-token"
	s.config.DiscordWebhooks.Ransomware = config.WebhookConfig{Enabled: true, URL: discordWebhookURL}
	s.config.SlackWebhooks.Ransomware = config.WebhookConfig{Enabled: true, URL: slackWebhookURL}

	discordSender := newBlockingAPIWebhookSender()
	slackSender := newBlockingAPIWebhookSender()
	s.discordWebhookSender = discordSender
	s.slackWebhookSender = slackSender
	defer discordSender.releaseSends()
	defer slackSender.releaseSends()

	enqueueAPIRetryPayloadForDestination(t, s, "victim-discord", apiDestinationID(status.MessengerDiscord), status.MessengerDiscord.String())
	enqueueAPIRetryPayloadForDestination(t, s, "victim-slack", apiDestinationID(status.MessengerSlack), status.MessengerSlack.String())

	done := make(chan struct{})
	go func() {
		s.processAPIRetryQueue(t.Context(), nil)
		close(done)
	}()

	waitForTestSignalWithTimeout(t, discordSender.started, 250*time.Millisecond, "discord API retry send start")
	waitForTestSignalWithTimeout(t, slackSender.started, 250*time.Millisecond, "slack API retry send start")
	discordSender.releaseSends()
	slackSender.releaseSends()
	waitForTestSignalWithTimeout(t, done, time.Second, "API retry queue completion")

	if remaining := s.statusTracker.GetQueuedRetryItemsByType("api"); len(remaining) != 0 {
		t.Fatalf("remaining API retry queue length = %d, want 0", len(remaining))
	}
}

func enqueueAPIRetryPayloadForDestination(
	t *testing.T,
	s *Scheduler,
	entryID, destinationID, messenger string,
) {
	t.Helper()
	entry := api.RansomwareEntry{
		ID:      entryID,
		Group:   "lockbit",
		Victim:  "Example Corp",
		Country: "DE",
	}
	payload, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("Marshal(entry) error = %v", err)
	}
	if !enqueueRetryForTest(
		t,
		s.statusTracker,
		api.GenerateEntryKey(entry),
		destinationID,
		messenger,
		status.RetryItemTypeAPI.String(),
		api.DisplayRansomwareTitle(entry),
		"temporary failure",
		5,
		time.Hour,
		payload,
	) {
		t.Fatal("EnqueueRetry() unexpectedly failed")
	}
}

func TestDryRunRSSFilteredEntriesLogsInfoSummary(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()
	previousLevel := log.GetLevel()
	log.SetLevel(log.InfoLevel)
	defer log.SetLevel(previousLevel)

	s := newTestScheduler(t)
	s.dryRun = true
	feedResults := &rss.FeedResults{
		Entries: map[string][]rss.Entry{
			"https://example.test/feed.xml": {
				{
					Title:      "Filtered item",
					FeedURL:    "https://example.test/feed.xml",
					Categories: []string{"deny"},
					Published:  time.Now(),
				},
			},
		},
		FeedErrors: map[string]string{},
	}
	targets := []webhookTarget{
		{
			destinationID: "discord.rss.general",
			messenger:     "discord",
			filters:       &filter.Rules{IncludeCategories: []string{"allow"}},
		},
	}

	s.sendParsedRSSToWebhooksWithConfig(t.Context(), s.config, feedResults, config.FeedTypeGeneral, targets, nil)

	entry := findTestLogEntry(hook, "[DRY-RUN] No RSS entries matched target preview")
	if entry == nil {
		t.Fatal("missing dry-run RSS filtered empty-state summary")
	}
	for key, want := range map[string]any{
		"feed_type":       config.FeedTypeGeneral,
		"messenger":       "discord",
		"destination_id":  "discord.rss.general",
		"item_type":       status.RetryItemTypeRSS.String(),
		"new_entry_count": 1,
		"preview_count":   0,
		"filtered_count":  1,
	} {
		if got := entry.Data[key]; got != want {
			t.Fatalf("field %s = %#v, want %#v; fields=%#v", key, got, want, entry.Data)
		}
	}
	if entry.Data["hint"] == "" {
		t.Fatalf("missing operator hint in fields=%#v", entry.Data)
	}
}

func findTestLogEntry(hook *logtest.Hook, message string) *log.Entry {
	for _, entry := range hook.AllEntries() {
		if entry.Message == message {
			return entry
		}
	}
	return nil
}

func findTestLogEntryWithField(hook *logtest.Hook, message, field string, value any) *log.Entry {
	for _, entry := range hook.AllEntries() {
		if entry.Message == message && entry.Data[field] == value {
			return entry
		}
	}
	return nil
}

func assertPollLogContext(t *testing.T, entry *log.Entry, pollType string) {
	t.Helper()
	if entry == nil {
		t.Fatalf("missing log entry for poll_type %q", pollType)
	}
	if got := entry.Data["poll_type"]; got != pollType {
		t.Fatalf("poll_type = %#v, want %q; fields=%#v", got, pollType, entry.Data)
	}
	runID, ok := entry.Data["run_id"].(string)
	if !ok || strings.TrimSpace(runID) == "" {
		t.Fatalf("run_id = %#v, want non-empty string; fields=%#v", entry.Data["run_id"], entry.Data)
	}
}

func previewFieldsContain(fields []map[string]any, namePart, valuePart string) bool {
	for _, field := range fields {
		if strings.Contains(fmt.Sprint(field["name"]), namePart) &&
			strings.Contains(fmt.Sprint(field["value"]), valuePart) {
			return true
		}
	}
	return false
}

func previewBlocksContain(blocks []map[string]any, textPart string) bool {
	for _, block := range blocks {
		if strings.Contains(fmt.Sprint(block), textPart) {
			return true
		}
	}
	return false
}

func waitForTestSignal(t *testing.T, ch <-chan struct{}, label string) {
	t.Helper()
	waitForTestSignalWithTimeout(t, ch, time.Second, label)
}

func waitForTestSignalWithTimeout(t *testing.T, ch <-chan struct{}, timeout time.Duration, label string) {
	t.Helper()
	if !waitForTestSignalWithin(ch, timeout) {
		t.Fatalf("timed out waiting for %s", label)
	}
}

func waitForTestSignalWithin(ch <-chan struct{}, timeout time.Duration) bool {
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-ch:
		return true
	case <-timer.C:
		return false
	}
}

// When apiMu is already held, checkAPIOnce must return immediately
// (TryLock fails) instead of blocking.
func TestCheckAPIOnceOverlapProtection(t *testing.T) {
	s := newTestScheduler(t)

	// Acquire the lock externally to simulate an in-progress run
	s.apiMu.Lock()

	ctx := context.Background()

	returned := make(chan struct{})
	go func() {
		s.checkAPIOnce(ctx)
		close(returned)
	}()

	waitForTestSignalWithTimeout(t, returned, 2*time.Second, "checkAPIOnce overlap protection")

	// Release the lock so resources can be cleaned up
	s.apiMu.Unlock()
}

// Same as above but for the RSS mutex.
func TestCheckRSSOnceOverlapProtection(t *testing.T) {
	s := newTestScheduler(t)

	// Acquire the lock externally to simulate an in-progress run
	s.rssMu.Lock()

	ctx := context.Background()

	returned := make(chan struct{})
	go func() {
		s.checkRSSOnce(ctx)
		close(returned)
	}()

	waitForTestSignalWithTimeout(t, returned, 2*time.Second, "checkRSSOnce overlap protection")

	s.rssMu.Unlock()
}

// An item that is already marked as sent to a webhook must be skipped.
// We verify by checking that the method completes without sending
// (no enabled webhooks will actually fire; the dedup check is the key).
func TestSendParsedRSSToWebhooks_Dedup(t *testing.T) {
	s := newTestScheduler(t)
	discordSender := &recordingWebhookSender{rssErr: errors.New("unexpected RSS send")}
	s.discordWebhookSender = discordSender

	feedURL := "https://example.com/feed"
	webhookURL := "https://discord.com/api/webhooks/123/abc"

	// Enable one Discord RSS webhook so getWebhookTargets returns a target
	s.config.DiscordWebhooks.RSS = config.WebhookConfig{Enabled: true, URL: webhookURL}
	s.config.Feeds.GeneralFeeds = []string{feedURL}
	s.feedTypeMap = buildFeedTypeMap(s.config)

	// Create a single RSS entry
	entry := rss.Entry{
		Title:     "Already Sent Article",
		Link:      "https://example.com/article-1",
		GUID:      "guid-already-sent",
		FeedTitle: "Test Feed",
		FeedURL:   feedURL,
		Published: time.Now().Add(-10 * time.Minute),
	}

	// Pre-mark the item (and its content signature) as sent to the webhook
	key := rss.GenerateEntryKey(feedURL, entry.GUID, entry.Link, entry.Title)
	contentSig := rss.GenerateContentSignature(feedURL, entry.Title, entry.Published)
	s.statusTracker.MarkRSSItemSentToWebhook(key, entry.Title, entry.FeedTitle, webhookURL)
	s.statusTracker.MarkRSSItemSentToWebhook(contentSig, entry.Title, entry.FeedTitle, webhookURL)

	// Build FeedResults with the already-sent entry
	feedResults := &rss.FeedResults{
		Entries:    map[string][]rss.Entry{feedURL: {entry}},
		FeedErrors: map[string]string{},
	}

	targets := s.getWebhookTargets("general")
	if len(targets) != 1 {
		t.Fatalf("expected 1 target, got %d", len(targets))
	}

	ctx := context.Background()

	// sendParsedRSSToWebhooks should complete without error (the dedup
	// path skips the entry before attempting any HTTP call).  If the dedup
	// were broken and it tried to send to the webhook URL, the HTTP call
	// would fail -- but we also verify via the tracker that no NEW sent
	// records were created beyond the ones we pre-seeded.
	s.sendParsedRSSToWebhooks(ctx, feedResults, "general", targets, nil)

	// The item was already sent, so no additional sent records should exist
	// beyond the two we created (key + contentSig).  Verify the primary key
	// is still marked as sent (no corruption).
	if !s.statusTracker.IsRSSItemSentToWebhook(key, webhookURL) {
		t.Error("expected primary key to remain marked as sent")
	}
	if !s.statusTracker.IsRSSItemSentToWebhook(contentSig, webhookURL) {
		t.Error("expected content signature to remain marked as sent")
	}
	if calls := discordSender.rssCallCount(); calls != 0 {
		t.Fatalf("dedup skip attempted %d RSS sends, want 0", calls)
	}
}

// Items older than RSSMaxItemAge should be skipped without being
// permanently marked sent.
func TestSendParsedRSSToWebhooks_FreshnessFilter(t *testing.T) {
	s := newTestScheduler(t)
	discordSender := &recordingWebhookSender{rssErr: errors.New("unexpected RSS send")}
	s.discordWebhookSender = discordSender

	feedURL := "https://example.com/feed"
	webhookURL := "https://discord.com/api/webhooks/456/def"

	s.config.DiscordWebhooks.RSS = config.WebhookConfig{Enabled: true, URL: webhookURL}
	s.config.Feeds.GeneralFeeds = []string{feedURL}
	s.config.RSSMaxItemAge = 1 * time.Hour // Only accept items younger than 1 hour
	s.feedTypeMap = buildFeedTypeMap(s.config)

	// Create a stale entry (published 3 hours ago -- exceeds max age)
	staleEntry := rss.Entry{
		Title:     "Old News Article",
		Link:      "https://example.com/old-article",
		GUID:      "guid-stale",
		FeedTitle: "Test Feed",
		FeedURL:   feedURL,
		Published: time.Now().Add(-3 * time.Hour),
	}

	feedResults := &rss.FeedResults{
		Entries:    map[string][]rss.Entry{feedURL: {staleEntry}},
		FeedErrors: map[string]string{},
	}

	targets := s.getWebhookTargets("general")
	ctx := context.Background()

	s.sendParsedRSSToWebhooks(ctx, feedResults, "general", targets, nil)

	// The stale entry should not be sent, but it also should not be marked as
	// sent. Raising rss_max_item_age later must still allow delivery.
	key := rss.GenerateEntryKey(feedURL, staleEntry.GUID, staleEntry.Link, staleEntry.Title)
	contentSig := rss.GenerateContentSignature(feedURL, staleEntry.Title, staleEntry.Published)

	if s.statusTracker.IsRSSItemSentToWebhook(key, webhookURL) {
		t.Error("stale entry primary key should not be marked sent by freshness filtering")
	}
	if s.statusTracker.IsRSSItemSentToWebhook(contentSig, webhookURL) {
		t.Error("stale entry content signature should not be marked sent by freshness filtering")
	}
	if calls := discordSender.rssCallCount(); calls != 0 {
		t.Fatalf("freshness skip attempted %d RSS sends, want 0", calls)
	}

	// Verify a fresh entry in the same batch WOULD NOT be marked yet
	// (because with webhooks disabled it would fail the HTTP call, but the
	// freshness filter path specifically marks stale items as sent).
	// This confirms the filter ran, not the send path.
}

// TestSendParsedRSSToWebhooks_FreshnessFilterDeliversUndatedItems is the
// fresh-path counterpart of TestSendRSSEntryToTargetDeliversUndatedItemWithMaxItemAge.
// "Unknown age" is not "too old": an item whose publication date is absent or
// unparseable used to count as infinitely old, so a feed emitting only such
// items delivered nothing at all while rss_max_item_age was set, and raising
// that limit never recovered them.
func TestSendParsedRSSToWebhooks_FreshnessFilterDeliversUndatedItems(t *testing.T) {
	s := newTestScheduler(t)
	discordSender := &recordingWebhookSender{}
	s.discordWebhookSender = discordSender

	feedURL := "https://example.com/feed"
	webhookURL := "https://discord.com/api/webhooks/freshness-undated/abc"

	s.config.DiscordWebhooks.RSS = config.WebhookConfig{Enabled: true, URL: webhookURL}
	s.config.Feeds.GeneralFeeds = []string{feedURL}
	s.config.RSSMaxItemAge = time.Hour
	s.feedTypeMap = buildFeedTypeMap(s.config)

	undatedEntry := rss.Entry{
		Title:     "Undated Article",
		Link:      "https://example.com/undated",
		GUID:      "guid-undated",
		FeedTitle: "Test Feed",
		FeedURL:   feedURL,
	}
	feedResults := &rss.FeedResults{
		Entries:    map[string][]rss.Entry{feedURL: {undatedEntry}},
		FeedErrors: map[string]string{},
	}

	targets := s.getWebhookTargets("general")
	if len(targets) != 1 {
		t.Fatalf("webhook targets = %d, want 1", len(targets))
	}
	destinationID := targetDestinationID(targets[0])

	s.sendParsedRSSToWebhooks(context.Background(), feedResults, "general", targets, nil)

	if calls := discordSender.rssCallCount(); calls != 1 {
		t.Fatalf("undated entry RSS sends = %d, want 1", calls)
	}
	key := rss.GenerateEntryKey(feedURL, undatedEntry.GUID, undatedEntry.Link, undatedEntry.Title)
	contentSig := rss.GenerateEntryContentSignature(undatedEntry)
	if !s.statusTracker.IsRSSItemSentToDestination(key, destinationID, webhookURL) {
		t.Error("undated entry primary key was not marked sent for the destination")
	}
	if !s.statusTracker.IsRSSItemSentToDestination(contentSig, destinationID, webhookURL) {
		t.Error("undated entry content signature was not marked sent for the destination")
	}
	if retryItems := s.statusTracker.GetRetryItemsByType("rss"); len(retryItems) != 0 {
		t.Fatalf("undated delivery queued %d retry items, want 0", len(retryItems))
	}
}

func TestEffectiveRSSSendMaxAge(t *testing.T) {
	tests := []struct {
		name         string
		sendMaxAge   time.Duration
		dedupHorizon time.Duration
		want         time.Duration
	}{
		{name: "both disabled", sendMaxAge: 0, dedupHorizon: 0, want: 0},
		{name: "only send gate", sendMaxAge: 2 * time.Hour, dedupHorizon: 0, want: 2 * time.Hour},
		{name: "only dedup horizon", sendMaxAge: 0, dedupHorizon: 24 * time.Hour, want: 24 * time.Hour},
		{name: "dedup horizon stricter", sendMaxAge: 48 * time.Hour, dedupHorizon: 24 * time.Hour, want: 24 * time.Hour},
		{name: "send gate stricter", sendMaxAge: 1 * time.Hour, dedupHorizon: 24 * time.Hour, want: 1 * time.Hour},
		{name: "equal", sendMaxAge: 12 * time.Hour, dedupHorizon: 12 * time.Hour, want: 12 * time.Hour},
		{name: "negative treated as disabled", sendMaxAge: -1, dedupHorizon: -1, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := effectiveRSSSendMaxAge(tt.sendMaxAge, tt.dedupHorizon); got != tt.want {
				t.Fatalf("effectiveRSSSendMaxAge(%v, %v) = %v, want %v", tt.sendMaxAge, tt.dedupHorizon, got, tt.want)
			}
		})
	}
}

// Items older than the dedup horizon (status_retention.rss_parsed_max_age)
// must be skipped by the send path even when rss_max_item_age is disabled.
// Sending such an item guarantees a future duplicate: its sent marker is
// pruned together with the parsed item by age while the entry is still present
// upstream, so the next poll re-delivers it. The send freshness gate is
// therefore coupled to the dedup horizon. Regression for the BSI re-delivery
// loop where the same old advisories were re-sent on every poll/restart.
func TestSendParsedRSSToWebhooks_SkipsItemsOlderThanDedupHorizon(t *testing.T) {
	s := newTestScheduler(t)
	discordSender := &recordingWebhookSender{rssErr: errors.New("unexpected RSS send")}
	s.discordWebhookSender = discordSender

	feedURL := "https://example.com/feed"
	webhookURL := "https://discord.com/api/webhooks/789/ghi"

	s.config.DiscordWebhooks.RSS = config.WebhookConfig{Enabled: true, URL: webhookURL}
	s.config.Feeds.GeneralFeeds = []string{feedURL}
	s.config.RSSMaxItemAge = 0                                // send freshness gate disabled
	s.config.StatusRetention.RSSParsedMaxAge = 24 * time.Hour // dedup horizon
	s.feedTypeMap = buildFeedTypeMap(s.config)

	// Older than the dedup horizon but still present in the feed.
	oldEntry := rss.Entry{
		Title:     "Old But Still In Feed",
		Link:      "https://example.com/old-but-present",
		GUID:      "guid-old-present",
		FeedTitle: "Test Feed",
		FeedURL:   feedURL,
		Published: time.Now().Add(-48 * time.Hour),
	}
	feedResults := &rss.FeedResults{
		Entries:    map[string][]rss.Entry{feedURL: {oldEntry}},
		FeedErrors: map[string]string{},
	}

	s.sendParsedRSSToWebhooks(context.Background(), feedResults, "general", s.getWebhookTargets("general"), nil)

	key := rss.GenerateEntryKey(feedURL, oldEntry.GUID, oldEntry.Link, oldEntry.Title)
	contentSig := rss.GenerateEntryContentSignature(oldEntry)
	if s.statusTracker.IsRSSItemSentToWebhook(key, webhookURL) {
		t.Error("item older than dedup horizon should not be marked sent")
	}
	if s.statusTracker.IsRSSItemSentToWebhook(contentSig, webhookURL) {
		t.Error("item older than dedup horizon should not have its content signature marked sent")
	}
	if calls := discordSender.rssCallCount(); calls != 0 {
		t.Fatalf("item older than dedup horizon attempted %d RSS sends, want 0", calls)
	}
	if retryItems := s.statusTracker.GetRetryItemsByType("rss"); len(retryItems) != 0 {
		t.Fatalf("item older than dedup horizon queued %d retry items, want 0", len(retryItems))
	}
}

// Entries younger than RSSMaxItemAge should NOT be filtered out by
// the freshness gate (they should reach the send path).
func TestSendParsedRSSToWebhooks_FreshnessFilterPassesFreshItems(t *testing.T) {
	s := newTestScheduler(t)
	slackSender := &recordingWebhookSender{rssErr: errors.New("forced RSS send failure")}
	s.slackWebhookSender = slackSender

	feedURL := "https://example.com/feed"
	webhookURL := "https://hooks.slack.test/services/freshness"

	s.config.SlackWebhooks.RSS = config.WebhookConfig{Enabled: true, URL: webhookURL}
	s.config.Feeds.GeneralFeeds = []string{feedURL}
	s.config.RSSMaxItemAge = 1 * time.Hour
	s.feedTypeMap = buildFeedTypeMap(s.config)

	freshEntry := rss.Entry{
		Title:     "Breaking News",
		Link:      "https://example.com/fresh-article",
		GUID:      "guid-fresh",
		FeedTitle: "Test Feed",
		FeedURL:   feedURL,
		Published: time.Now().Add(-10 * time.Minute), // 10 min old, well within 1h
	}

	feedResults := &rss.FeedResults{
		Entries:    map[string][]rss.Entry{feedURL: {freshEntry}},
		FeedErrors: map[string]string{},
	}

	targets := s.getWebhookTargets("general")
	ctx := context.Background()

	// The local send fails, but the freshness
	// filter should NOT silently mark the item as sent.
	s.sendParsedRSSToWebhooks(ctx, feedResults, "general", targets, nil)

	key := rss.GenerateEntryKeyForEntry(freshEntry)

	if calls := slackSender.rssCallCount(); calls != 1 {
		t.Fatalf("fresh RSS send attempts = %d, want 1", calls)
	}
	// Because the actual send failed, the
	// item should NOT be marked as sent.  If the freshness filter
	// incorrectly caught it, it would be marked.
	if s.statusTracker.IsRSSItemSentToWebhook(key, webhookURL) {
		t.Error("fresh entry should NOT be marked as sent (webhook send should have failed, " +
			"freshness filter should not have caught it)")
	}
}

// In the recovery path (sendRSSItemsToWebhook), if a content signature
// is already marked as sent, the item with a different key but same
// content should be skipped.
func TestSendRSSItemsToWebhook_RecoveryDedup(t *testing.T) {
	s := newTestScheduler(t)
	discordSender := &recordingWebhookSender{rssErr: errors.New("unexpected RSS send")}
	s.discordWebhookSender = discordSender

	feedURL := "https://example.com/feed"
	webhookURL := "https://discord.com/api/webhooks/recover/abc"

	s.config.DiscordWebhooks.RSS = config.WebhookConfig{Enabled: true, URL: webhookURL}
	s.config.Feeds.GeneralFeeds = []string{feedURL}
	s.feedTypeMap = buildFeedTypeMap(s.config)

	publishedTime := time.Now().Add(-10 * time.Minute)
	publishedStr := publishedTime.Format("2006-01-02 15:04:05.999999")

	// Create a StoredRSSEntry with a DIFFERENT primary key but same content
	storedItem := status.StoredRSSEntry{
		Key:        feedURL + ":https://example.com/article-variant-0",
		FeedURL:    feedURL,
		Title:      "Duplicate Article",
		Link:       "https://example.com/article-variant-0",
		Published:  publishedStr,
		FeedTitle:  "Test Feed",
		Categories: []string{},
	}
	rssEntry, err := status.RSSEntryFromStored(storedItem)
	if err != nil {
		t.Fatalf("RSSEntryFromStored() error = %v", err)
	}
	contentSig := rss.GenerateEntryContentSignature(rssEntry)

	// Pre-mark the content signature as sent (simulates previous successful send
	// of the same article under a different key, e.g. a link variant)
	s.statusTracker.MarkRSSItemSentToWebhook(contentSig, "Duplicate Article", "Test Feed", webhookURL)

	ctx := context.Background()

	// Call the recovery send path
	s.sendRSSItemsToWebhook(ctx, unsentRSSItemsForTest(t, storedItem), webhookURL, "general", "discord", nil)

	// The primary key should be marked as sent/suppressed so recovery does not
	// keep selecting this content-signature duplicate.
	if !s.statusTracker.IsRSSItemSentToWebhook(storedItem.Key, webhookURL) {
		t.Error("item with duplicate content signature should be marked sent after recovery dedupe")
	}
	if calls := discordSender.rssCallCount(); calls != 0 {
		t.Fatalf("recovery dedup attempted %d RSS sends, want 0", calls)
	}
}

// sendUnsentRSSItems should only send items matching the correct feed
// type for each webhook target.
func TestSendUnsentRSSItems_FiltersCorrectly(t *testing.T) {
	s := newTestScheduler(t)
	slackSender := &recordingWebhookSender{}
	s.slackWebhookSender = slackSender

	generalFeedURL := "https://general.example.com/feed"
	govFeedURL := "https://gov.example.com/feed"
	rssWebhookURL := "https://hooks.slack.test/services/rss"
	govWebhookURL := "https://hooks.slack.test/services/government"

	s.config.SlackWebhooks.RSS = config.WebhookConfig{Enabled: true, URL: rssWebhookURL}
	s.config.SlackWebhooks.Government = config.WebhookConfig{Enabled: true, URL: govWebhookURL}
	s.config.Feeds.GeneralFeeds = []string{generalFeedURL}
	s.config.Feeds.GovernmentFeeds = []string{govFeedURL}
	s.feedTypeMap = buildFeedTypeMap(s.config)

	now := time.Now()

	// Add a "general" parsed item to the tracker (unsent)
	generalEntry := status.StoredRSSEntry{
		Key:        rss.GenerateEntryKey(generalFeedURL, "guid-gen-1", "", "General Article"),
		FeedURL:    generalFeedURL,
		Title:      "General Article",
		Link:       "https://general.example.com/a1",
		GUID:       "guid-gen-1",
		Published:  now.Add(-5 * time.Minute).Format("2006-01-02 15:04:05.999999"),
		FeedTitle:  "General Feed",
		Categories: []string{},
	}
	s.statusTracker.MarkRSSItemParsed(generalFeedURL, generalEntry.Key, generalEntry)

	// Add a "government" parsed item to the tracker (unsent)
	govEntry := status.StoredRSSEntry{
		Key:        rss.GenerateEntryKey(govFeedURL, "guid-gov-1", "", "Government Alert"),
		FeedURL:    govFeedURL,
		Title:      "Government Alert",
		Link:       "https://gov.example.com/a1",
		GUID:       "guid-gov-1",
		Published:  now.Add(-5 * time.Minute).Format("2006-01-02 15:04:05.999999"),
		FeedTitle:  "Government Feed",
		Categories: []string{},
	}
	s.statusTracker.MarkRSSItemParsed(govFeedURL, govEntry.Key, govEntry)

	// Flush to disk so GetUnsentRSSItemsForWebhook can find them
	if err := s.statusTracker.SavePendingChanges(); err != nil {
		t.Fatalf("SavePendingChanges: %v", err)
	}

	ctx := context.Background()

	// Verify preconditions: both items should be unsent for their respective webhooks
	unsentForRSS := s.statusTracker.GetUnsentRSSItemsForWebhook(rssWebhookURL)
	unsentForGov := s.statusTracker.GetUnsentRSSItemsForWebhook(govWebhookURL)

	if len(unsentForRSS) < 1 {
		t.Fatalf("expected at least 1 unsent item for RSS webhook, got %d", len(unsentForRSS))
	}
	if len(unsentForGov) < 1 {
		t.Fatalf("expected at least 1 unsent item for Gov webhook, got %d", len(unsentForGov))
	}

	// Run the recovery sender. General items should
	// only be attempted for the RSS webhook, government items only for the
	// government webhook.
	s.sendUnsentRSSItems(ctx, nil)

	calls := slackSender.rssCallsSnapshot()
	if len(calls) != 2 {
		t.Fatalf("RSS recovery send attempts = %d, want 2", len(calls))
	}
	if !hasRecordedRSSSend(calls, rssWebhookURL, "General Article", config.FeedTypeGeneral) {
		t.Fatalf("general RSS webhook was not called with the general article: %#v", calls)
	}
	if !hasRecordedRSSSend(calls, govWebhookURL, "Government Alert", config.FeedTypeGovernment) {
		t.Fatalf("government RSS webhook was not called with the government article: %#v", calls)
	}
	if !s.statusTracker.IsRSSItemSentToWebhook(generalEntry.Key, rssWebhookURL) {
		t.Error("general article should be sent to RSS webhook")
	}
	if !s.statusTracker.IsRSSItemSentToWebhook(govEntry.Key, govWebhookURL) {
		t.Error("government article should be sent to government webhook")
	}
	if s.statusTracker.IsRSSItemSentToWebhook(generalEntry.Key, govWebhookURL) {
		t.Error("general article should NOT be sent to government webhook")
	}
	if s.statusTracker.IsRSSItemSentToWebhook(govEntry.Key, rssWebhookURL) {
		t.Error("government article should NOT be sent to RSS webhook")
	}
}

// When no webhooks are enabled, sendUnsentRSSItems should return
// immediately without errors.
func TestSendUnsentRSSItems_NoTargets(t *testing.T) {
	s := newTestScheduler(t)
	feedURL := "https://example.test/no-targets.xml"
	itemKey := "rss:v2:no-targets"
	s.statusTracker.MarkRSSItemParsed(feedURL, itemKey, status.StoredRSSEntry{
		Key:       itemKey,
		FeedURL:   feedURL,
		Title:     "No Targets Item",
		FeedTitle: "No Targets Feed",
		Published: time.Now().UTC().Format(time.RFC3339),
	})

	// All webhooks are disabled by default in newTestScheduler
	ctx := context.Background()

	s.sendUnsentRSSItems(ctx, nil)

	if !s.statusTracker.IsRSSItemParsed(feedURL, itemKey) {
		t.Fatal("parsed RSS item was removed during no-target recovery")
	}
	if s.statusTracker.IsRSSItemSentToWebhook(itemKey, "https://hooks.slack.com/services/T000/B000/no-targets") {
		t.Fatal("RSS item was marked sent despite no configured targets")
	}
	if retryItems := s.statusTracker.GetRetryItemsByType("rss"); len(retryItems) != 0 {
		t.Fatalf("no-target recovery queued %d retry items, want 0", len(retryItems))
	}
}

// When the API key is empty, checkAPIOnce should return without making
// any network calls or panicking.
func TestCheckAPIOnce_NoAPIKey(t *testing.T) {
	s := newTestScheduler(t)
	s.config.APIKey = "" // Explicitly empty

	ctx := context.Background()

	// Should complete immediately with no errors
	done := make(chan struct{})
	go func() {
		s.checkAPIOnce(ctx)
		close(done)
	}()

	waitForTestSignalWithTimeout(t, done, 5*time.Second, "checkAPIOnce with empty API key")
}

func TestCheckAPIOnceMissingAPIKeyRecordsOperatorStatus(t *testing.T) {
	s := newTestScheduler(t)
	s.config.APIKey = ""
	s.config.DiscordWebhooks.Ransomware = config.WebhookConfig{
		Enabled: true,
		URL:     "https://discord.com/api/webhooks/123/abc",
	}

	s.checkAPIOnce(context.Background())

	apiStatusJSON, err := os.ReadFile(filepath.Join(s.config.DataDir, "api_status.json"))
	if err != nil {
		t.Fatalf("ReadFile(api_status.json) error = %v", err)
	}
	if !strings.Contains(string(apiStatusJSON), "API key not configured") {
		t.Fatalf("api_status.json missing API-key operator error: %s", apiStatusJSON)
	}
}

func TestCheckAPIOnceWithoutAPIKeyKeepsPersistedAPISentItems(t *testing.T) {
	s := newTestScheduler(t)

	const seeded = `{"last_updated":"2026-09-01T10:00:00Z","last_check":"2026-09-01T10:00:00Z",` +
		`"last_success":null,"last_error":null,"entries_found":0,"sent_items":{` +
		`"aaaa":{"sent_at":"2026-09-01T09:00:00Z","destination_id":"slack.ransomware"},` +
		`"bbbb":{"sent_at":"2026-09-01T09:30:00Z","destination_id":"slack.ransomware"}}}`

	apiStatusPath := filepath.Join(s.config.DataDir, "api_status.json")
	if err := os.WriteFile(apiStatusPath, []byte(seeded), 0600); err != nil {
		t.Fatalf("WriteFile(api_status.json) error = %v", err)
	}

	s.config.APIKey = ""
	s.config.SlackWebhooks.Ransomware = config.WebhookConfig{
		Enabled: true,
		URL:     "https://hooks.slack.test/services/T/B/C",
	}

	s.checkAPIOnce(t.Context())

	data, err := os.ReadFile(apiStatusPath)
	if err != nil {
		t.Fatalf("ReadFile(api_status.json) error = %v", err)
	}
	var payload struct {
		SentItems map[string]json.RawMessage `json:"sent_items"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("Unmarshal(api_status.json) error = %v", err)
	}
	if len(payload.SentItems) != 2 {
		t.Fatalf("sent_items = %d, want 2 (persisted API dedup state wiped): %s", len(payload.SentItems), data)
	}
	if !strings.Contains(string(data), "API key not configured") {
		t.Fatalf("api_status.json missing API-key operator error: %s", data)
	}
}

func TestCheckAPIOnceLogsPollContextOnAPIError(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()

	s := newTestScheduler(t)
	s.config.APIKey = "test-api-key"
	s.config.SlackWebhooks.Ransomware = config.WebhookConfig{
		Enabled: true,
		URL:     "https://hooks.slack.test/services/T/B/api-error",
	}
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer apiServer.Close()

	client, err := api.NewClientWithBaseURLAndPolicy("test-api-key", apiServer.URL, api.HTTPPolicy{
		RequestTimeout: time.Second,
		MaxAttempts:    1,
		RetryBaseDelay: 0,
	})
	if err != nil {
		t.Fatalf("NewClientWithBaseURLAndPolicy() error = %v", err)
	}
	s.apiClient = client

	s.checkAPIOnce(t.Context())

	entry := findTestLogEntry(hook, "Failed to get API data")
	assertPollLogContext(t, entry, "api")
}

// When no feeds are configured, checkRSSOnce should complete quickly
// without errors.
func TestCheckRSSOnce_NoFeeds(t *testing.T) {
	s := newTestScheduler(t)
	// Feeds are empty by default in newTestScheduler

	ctx := context.Background()

	done := make(chan struct{})
	go func() {
		s.checkRSSOnce(ctx)
		close(done)
	}()

	waitForTestSignalWithTimeout(t, done, 5*time.Second, "checkRSSOnce with no feeds configured")
}

func TestCheckRSSOnceLogsPollContextOnSchedulerWarning(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()

	s := newTestScheduler(t)
	s.config.SlackWebhooks.RSS = config.WebhookConfig{
		Enabled: true,
		URL:     "https://hooks.slack.test/services/T/B/no-feeds",
	}

	s.checkRSSOnce(t.Context())

	entry := findTestLogEntry(hook, "RSS webhook targets are enabled but no feed URLs are configured for this feed type")
	assertPollLogContext(t, entry, "rss")
}

func TestCheckRSSOnceWarnsWhenRSSWebhookEnabledWithoutGeneralFeeds(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()

	s := newTestScheduler(t)
	s.config.SlackWebhooks.RSS = config.WebhookConfig{
		Enabled: true,
		URL:     "https://hooks.slack.test/services/T/B/no-feeds",
	}

	s.checkRSSOnce(t.Context())

	entry := findTestLogEntry(hook, "RSS webhook targets are enabled but no feed URLs are configured for this feed type")
	if entry == nil {
		t.Fatal("missing enabled-target-without-feeds warning")
	}
	if got := entry.Data["feed_type"]; got != config.FeedTypeGeneral {
		t.Fatalf("feed_type = %#v, want %q; fields=%#v", got, config.FeedTypeGeneral, entry.Data)
	}
	if got := entry.Data["configured_urls"]; got != 0 {
		t.Fatalf("configured_urls = %#v, want 0; fields=%#v", got, entry.Data)
	}
	if got := entry.Data["targets"]; got != 1 {
		t.Fatalf("targets = %#v, want 1; fields=%#v", got, entry.Data)
	}
}

func TestCheckRSSOnceWarnsWhenGeneralFeedsConfiguredWithoutWebhookTargets(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()

	s := newTestScheduler(t)
	s.config.Feeds.GeneralFeeds = []string{"https://feeds.example.test/rss.xml"}
	s.feedTypeMap = buildFeedTypeMap(s.config)

	s.checkRSSOnce(t.Context())

	entry := findTestLogEntry(hook, "RSS feed URLs are configured but no webhook target is enabled for this feed type")
	if entry == nil {
		t.Fatal("missing feeds-without-targets warning")
	}
	if got := entry.Data["feed_type"]; got != config.FeedTypeGeneral {
		t.Fatalf("feed_type = %#v, want %q; fields=%#v", got, config.FeedTypeGeneral, entry.Data)
	}
	if got := entry.Data["configured_urls"]; got != 1 {
		t.Fatalf("configured_urls = %#v, want 1; fields=%#v", got, entry.Data)
	}
}

// Calling Start then Stop multiple times should not panic or deadlock.
func TestStartStopMultipleTimes(t *testing.T) {
	for i := 0; i < 3; i++ {
		s := newTestScheduler(t)
		ctx, cancel := context.WithCancel(context.Background())

		if err := s.Start(ctx); err != nil {
			t.Fatalf("iteration %d: Start() error: %v", i, err)
		}

		cancel()

		done := make(chan struct{})
		go func() {
			if err := s.Stop(); err != nil {
				t.Errorf("Stop: %v", err)
			}
			close(done)
		}()

		waitForTestSignalWithTimeout(t, done, 10*time.Second, fmt.Sprintf("iteration %d Stop()", i))
	}
}

// Fire multiple concurrent calls to checkAPIOnce / checkRSSOnce and
// verify none of them deadlock (only one should run at a time, the
// rest should skip via TryLock).
func TestOverlapProtectionConcurrent(t *testing.T) {
	s := newTestScheduler(t)
	ctx := context.Background()

	const goroutines = 10
	var wg sync.WaitGroup

	// Test API overlap
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			s.checkAPIOnce(ctx)
		}()
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	waitForTestSignalWithTimeout(t, done, 10*time.Second, "concurrent checkAPIOnce calls")

	// Test RSS overlap
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			s.checkRSSOnce(ctx)
		}()
	}

	done2 := make(chan struct{})
	go func() {
		wg.Wait()
		close(done2)
	}()

	waitForTestSignalWithTimeout(t, done2, 10*time.Second, "concurrent checkRSSOnce calls")
}

// Cancelling the context mid-send should stop processing without panic.
func TestSendParsedRSSToWebhooks_ContextCancellation(t *testing.T) {
	s := newTestScheduler(t)

	feedURL := "https://example.com/feed"
	webhookURL := "https://discord.com/api/webhooks/cancel/test"

	s.config.DiscordWebhooks.RSS = config.WebhookConfig{Enabled: true, URL: webhookURL}
	s.config.Feeds.GeneralFeeds = []string{feedURL}
	s.feedTypeMap = buildFeedTypeMap(s.config)

	// Create several entries to process
	var entries []rss.Entry
	for i := 0; i < 5; i++ {
		entries = append(entries, rss.Entry{
			Title:     fmt.Sprintf("Article %d", i),
			Link:      fmt.Sprintf("https://example.com/article-%d", i),
			GUID:      fmt.Sprintf("guid-%d", i),
			FeedTitle: "Test Feed",
			FeedURL:   feedURL,
			Published: time.Now().Add(-time.Duration(i) * time.Minute),
		})
	}

	feedResults := &rss.FeedResults{
		Entries:    map[string][]rss.Entry{feedURL: entries},
		FeedErrors: map[string]string{},
	}

	targets := s.getWebhookTargets("general")
	ctx, cancel := context.WithCancel(context.Background())

	// Cancel immediately so the loop exits on context check
	cancel()

	// Should complete without panic
	done := make(chan struct{})
	go func() {
		s.sendParsedRSSToWebhooks(ctx, feedResults, "general", targets, nil)
		close(done)
	}()

	waitForTestSignalWithTimeout(t, done, 5*time.Second, "sendParsedRSSToWebhooks context cancellation")
}

func TestSendParsedRSSToWebhooksFlushesRetryOnCancellation(t *testing.T) {
	s := newTestScheduler(t)
	s.config.RetryMaxAttempts = 5
	s.config.RetryWindow = time.Hour

	feedURL := "https://example.com/feed"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		cancel()
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("temporary failure"))
	}))
	defer server.Close()

	entries := []rss.Entry{
		{
			Title:     "First RSS Item",
			Link:      "https://example.com/first",
			GUID:      "guid-first",
			FeedTitle: "Test Feed",
			FeedURL:   feedURL,
			Published: time.Now().Add(-2 * time.Minute),
		},
		{
			Title:     "Second RSS Item",
			Link:      "https://example.com/second",
			GUID:      "guid-second",
			FeedTitle: "Test Feed",
			FeedURL:   feedURL,
			Published: time.Now().Add(-time.Minute),
		},
	}
	feedResults := &rss.FeedResults{
		Entries:    map[string][]rss.Entry{feedURL: entries},
		FeedErrors: map[string]string{},
	}
	targets := []webhookTarget{{url: server.URL, messenger: "slack"}}

	s.sendParsedRSSToWebhooks(ctx, feedResults, "general", targets, nil)

	if requestCount != 1 {
		t.Fatalf("slack request count = %d, want 1 before cancellation", requestCount)
	}
	reloaded, err := status.NewTracker(s.config.DataDir)
	if err != nil {
		t.Fatalf("NewTracker(reload) error = %v", err)
	}
	retryItems := reloaded.GetRetryItemsByType("rss")
	if len(retryItems) != 1 {
		t.Fatalf("persisted RSS retry queue length = %d, want 1", len(retryItems))
	}
	wantKey := rss.GenerateEntryKey(entries[0].FeedURL, entries[0].GUID, entries[0].Link, entries[0].Title)
	if retryItems[0].ItemKey != wantKey {
		t.Fatalf("persisted RSS retry item key = %q, want %q", retryItems[0].ItemKey, wantKey)
	}
}

// The recovery path should also apply the freshness filter without
// permanently marking stale items as sent.
func TestSendRSSItemsToWebhook_FreshnessInRecovery(t *testing.T) {
	s := newTestScheduler(t)

	feedURL := "https://example.com/feed"
	webhookURL := "https://discord.com/api/webhooks/fresh-recover/abc"

	s.config.DiscordWebhooks.RSS = config.WebhookConfig{Enabled: true, URL: webhookURL}
	s.config.RSSMaxItemAge = 1 * time.Hour
	s.config.Feeds.GeneralFeeds = []string{feedURL}
	s.feedTypeMap = buildFeedTypeMap(s.config)

	// Create a stale StoredRSSEntry (5 hours old)
	publishedTime := time.Now().Add(-5 * time.Hour)
	staleItem := status.StoredRSSEntry{
		Key:        rss.GenerateEntryKey(feedURL, "guid-recovery-stale", "", "Old Recovery Article"),
		FeedURL:    feedURL,
		Title:      "Old Recovery Article",
		Link:       "https://example.com/old-recovery",
		GUID:       "guid-recovery-stale",
		Published:  publishedTime.Format("2006-01-02 15:04:05.999999"),
		FeedTitle:  "Test Feed",
		Categories: []string{},
	}

	ctx := context.Background()
	if !enqueueRetryForTest(t, s.statusTracker, staleItem.Key, webhookURL, "discord", "rss", staleItem.Title, "old delivery failure", 5, time.Hour) {
		t.Fatal("EnqueueRetry() unexpectedly moved stale item to dead letter")
	}
	s.sendRSSItemsToWebhook(ctx, unsentRSSItemsForTest(t, staleItem), webhookURL, "general", "discord", nil)

	if s.statusTracker.IsRSSItemSentToWebhook(staleItem.Key, webhookURL) {
		t.Error("stale item in recovery path should not be marked sent by freshness filter")
	}

	rssEntry, err := status.RSSEntryFromStored(staleItem)
	if err != nil {
		t.Fatalf("RSSEntryFromStored() error = %v", err)
	}
	contentSig := rss.GenerateEntryContentSignature(rssEntry)
	if s.statusTracker.IsRSSItemSentToWebhook(contentSig, webhookURL) {
		t.Error("stale item content signature in recovery should not be marked sent by freshness filter")
	}
	if retryItems := s.statusTracker.GetRetryItemsByType("rss"); len(retryItems) != 0 {
		t.Fatalf("stale recovery item left %d retry items, want 0", len(retryItems))
	}
}

// TestSendRSSItemsToWebhook_FreshnessDeliversUndatedRecoveryItems is the
// recovery-path counterpart of the fresh-path undated test: an undated item is
// exempt from the age gate on both sides, so recovery delivers it, clears its
// retry row and never dead-letters it. The webhook is a local httptest server:
// the delivery is real now, so a literal discord.com URL would leave the suite
// making an outbound request.
func TestSendRSSItemsToWebhook_FreshnessDeliversUndatedRecoveryItems(t *testing.T) {
	s := newTestScheduler(t)

	feedURL := "https://example.com/feed"

	var mu sync.Mutex
	postCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		postCount++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	s.config.SlackWebhooks.RSS = config.WebhookConfig{Enabled: true, URL: server.URL}
	s.config.RSSMaxItemAge = time.Hour
	s.config.Feeds.GeneralFeeds = []string{feedURL}
	s.feedTypeMap = buildFeedTypeMap(s.config)

	item := status.StoredRSSEntry{
		Key:       rss.GenerateEntryKey(feedURL, "guid-recovery-undated", "", "Undated Recovery Article"),
		FeedURL:   feedURL,
		Title:     "Undated Recovery Article",
		GUID:      "guid-recovery-undated",
		Published: "",
		FeedTitle: "Test Feed",
	}

	if !enqueueRetryForTest(t, s.statusTracker, item.Key, server.URL, "slack", "rss", item.Title, "old delivery failure", 5, time.Hour) {
		t.Fatal("EnqueueRetry() unexpectedly moved undated item to dead letter")
	}
	s.sendRSSItemsToWebhook(context.Background(), unsentRSSItemsForTest(t, item), server.URL, "general", "slack", nil)

	mu.Lock()
	posts := postCount
	mu.Unlock()
	if posts != 1 {
		t.Fatalf("POSTs to the local webhook = %d, want 1", posts)
	}
	if !s.statusTracker.IsRSSItemSentToWebhook(item.Key, server.URL) {
		t.Error("undated recovery item was not marked sent after delivery")
	}
	if retryItems := s.statusTracker.GetRetryItemsByType("rss"); len(retryItems) != 0 {
		t.Fatalf("undated recovery item left %d retry items, want 0", len(retryItems))
	}
	if dead := s.statusTracker.GetDeadLetterItems(); len(dead) != 0 {
		t.Fatalf("undated recovery item produced %d dead letters, want 0", len(dead))
	}
	events := deliveryAuditEventsForTest(t, s.config.DataDir)
	if !hasDeliveryAuditEvent(events, item.Key, status.DeliveryAuditOutcomeDelivered, "") {
		t.Fatalf("no delivered audit event for the undated recovery item: %+v", events)
	}
	for _, event := range events {
		if event.Outcome == status.DeliveryAuditOutcomeStale {
			t.Fatalf("undated recovery item was audited as stale: %+v", event)
		}
	}
}

func TestSendRSSItemsToWebhookClearsRetryWhenRecoveryItemFilteredOut(t *testing.T) {
	s := newTestScheduler(t)

	feedURL := "https://example.com/feed"
	published := time.Now()
	item := status.StoredRSSEntry{
		Key:        rss.GenerateEntryKey(feedURL, "guid-filtered-recovery", "", "Filtered Recovery Article"),
		FeedURL:    feedURL,
		Title:      "Filtered Recovery Article",
		Link:       "https://example.com/filtered-recovery",
		GUID:       "guid-filtered-recovery",
		Published:  published.Format("2006-01-02 15:04:05.999999"),
		FeedTitle:  "Test Feed",
		Categories: []string{"blocked"},
	}

	received := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- struct{}{}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	if !enqueueRetryForTest(t, s.statusTracker, item.Key, server.URL, "slack", "rss", item.Title, "old delivery failure", 5, time.Hour) {
		t.Fatal("EnqueueRetry() unexpectedly moved filtered item to dead letter")
	}

	filters := &filter.Rules{IncludeCategories: []string{"allowed"}}
	s.sendRSSItemsToWebhook(t.Context(), unsentRSSItemsForTest(t, item), server.URL, "general", "slack", filters)

	select {
	case <-received:
		t.Fatal("filtered recovery item was sent")
	default:
	}
	if !s.statusTracker.IsRSSItemSentToWebhook(item.Key, server.URL) {
		t.Fatal("filtered recovery item was not marked as skipped")
	}
	rssEntry, err := status.RSSEntryFromStored(item)
	if err != nil {
		t.Fatalf("RSSEntryFromStored() error = %v", err)
	}
	contentSig := rss.GenerateEntryContentSignature(rssEntry)
	if s.statusTracker.IsRSSItemSentToWebhook(contentSig, server.URL) {
		t.Fatal("filtered recovery content signature must not be marked as sent")
	}
	if retryItems := s.statusTracker.GetRetryItemsByType("rss"); len(retryItems) != 0 {
		t.Fatalf("filtered recovery item left %d retry items, want 0", len(retryItems))
	}
}

// crossFeedFilterEntriesForTest builds two entries that share title, published
// timestamp and description (so they share a content signature) but differ in
// feed URL, GUID and link (so a filter can discriminate between them).
func crossFeedFilterEntriesForTest(feedA, feedB string) (rss.Entry, rss.Entry) {
	published := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	entryA := rss.Entry{
		Title:       "Shared Cross Feed Filter Advisory",
		Link:        "https://feed-a.example.test/articles/shared-filter-advisory",
		Description: "identical body text",
		GUID:        "feed-a-shared-filter-advisory",
		FeedURL:     feedA,
		FeedTitle:   "Feed A",
		Published:   published,
	}
	entryB := entryA
	entryB.Link = "https://feed-b.example.test/articles/shared-filter-advisory"
	entryB.GUID = "feed-b-shared-filter-advisory"
	entryB.FeedURL = feedB
	entryB.FeedTitle = "Feed B"
	return entryA, entryB
}

func TestSendParsedRSSToWebhooksFilteredEntryDoesNotSuppressOtherFeed(t *testing.T) {
	s := newTestScheduler(t)
	feedA := "https://feed-a.example.test/feed.xml"
	feedB := "https://feed-b.example.test/feed.xml"

	var mu sync.Mutex
	var requestBodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("ReadAll(body) error = %v", err)
		}
		mu.Lock()
		requestBodies = append(requestBodies, string(body))
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	targets := []webhookTarget{{
		url:           server.URL,
		destinationID: "slack.rss.general",
		messenger:     "slack",
		filters:       &filter.Rules{ExcludeFields: map[string][]string{"feed_url": {feedA}}},
	}}

	entryA, entryB := crossFeedFilterEntriesForTest(feedA, feedB)
	contentSig := rss.GenerateEntryContentSignature(entryA)

	s.sendParsedRSSToWebhooks(t.Context(), &rss.FeedResults{
		Entries:    map[string][]rss.Entry{feedA: {entryA}},
		FeedErrors: map[string]string{},
	}, config.FeedTypeGeneral, targets, nil)

	mu.Lock()
	afterFirstCycle := len(requestBodies)
	mu.Unlock()
	if afterFirstCycle != 0 {
		t.Fatalf("POST count after filtered cycle = %d, want 0", afterFirstCycle)
	}
	if s.statusTracker.IsRSSItemSentToDestination(contentSig, "slack.rss.general", server.URL) {
		t.Fatal("filter mismatch marked the shared content signature as sent")
	}

	s.sendParsedRSSToWebhooks(t.Context(), &rss.FeedResults{
		Entries:    map[string][]rss.Entry{feedB: {entryB}},
		FeedErrors: map[string]string{},
	}, config.FeedTypeGeneral, targets, nil)

	mu.Lock()
	bodies := append([]string(nil), requestBodies...)
	mu.Unlock()
	if len(bodies) != 1 {
		t.Fatalf("POST count = %d, want 1 for the allowed second feed; bodies=%#v", len(bodies), bodies)
	}
	if !strings.Contains(bodies[0], entryB.Link) {
		t.Fatalf("delivered body does not reference feed B entry %q: %s", entryB.Link, bodies[0])
	}

	keyB := rss.GenerateEntryKeyForEntry(entryB)
	if !s.statusTracker.IsRSSItemSentToDestination(keyB, "slack.rss.general", server.URL) {
		t.Fatal("delivered feed B item key was not marked sent")
	}
	if !s.statusTracker.IsRSSItemSentToDestination(contentSig, "slack.rss.general", server.URL) {
		t.Fatal("real send did not mark the content signature as sent")
	}
	// The two markers must stay distinguishable on disk: only the filtered one
	// carries the skip flag that keeps it out of the content-signature backfill.
	keyA := rss.GenerateEntryKeyForEntry(entryA)
	skipped, ok := s.statusTracker.RSSSentItemSnapshot(keyA, "slack.rss.general")
	if !ok || !skipped.Skipped {
		t.Fatalf("feed A marker snapshot = %+v, ok = %v; want Skipped=true", skipped, ok)
	}
	delivered, ok := s.statusTracker.RSSSentItemSnapshot(keyB, "slack.rss.general")
	if !ok || delivered.Skipped {
		t.Fatalf("feed B marker snapshot = %+v, ok = %v; want Skipped=false", delivered, ok)
	}
	signature, ok := s.statusTracker.RSSSentItemSnapshot(contentSig, "slack.rss.general")
	if !ok || signature.Skipped {
		t.Fatalf("content-signature marker snapshot = %+v, ok = %v; want Skipped=false", signature, ok)
	}
}

func TestSendParsedRSSToWebhooksFilteredEntryStaysSkippedOnNextCycle(t *testing.T) {
	s := newTestScheduler(t)
	feedURL := "https://feed-skip.example.test/feed.xml"

	var mu sync.Mutex
	postCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		postCount++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	targets := []webhookTarget{{
		url:           server.URL,
		destinationID: "slack.rss.general",
		messenger:     "slack",
		filters:       &filter.Rules{ExcludeFields: map[string][]string{"feed_url": {feedURL}}},
	}}

	entry, _ := crossFeedFilterEntriesForTest(feedURL, "https://unused.example.test/feed.xml")
	feedResults := func() *rss.FeedResults {
		return &rss.FeedResults{
			Entries:    map[string][]rss.Entry{feedURL: {entry}},
			FeedErrors: map[string]string{},
		}
	}

	s.sendParsedRSSToWebhooks(t.Context(), feedResults(), config.FeedTypeGeneral, targets, nil)

	itemKey := rss.GenerateEntryKeyForEntry(entry)
	if !s.statusTracker.IsRSSItemSentToDestination(itemKey, "slack.rss.general", server.URL) {
		t.Fatal("filtered item key was not marked as skipped")
	}
	// markRSSItemSkippedForDestination writes the stable destination marker and
	// the legacy webhook-URL marker; the content signature must not be marked.
	afterFirstCycle := s.statusTracker.StatusSummary().RSSSentItems
	if afterFirstCycle != 2 {
		t.Fatalf("RSS sent markers after filtered cycle = %d, want 2 (item key for destination and legacy URL)", afterFirstCycle)
	}
	snapshot, ok := s.statusTracker.RSSSentItemSnapshot(itemKey, "slack.rss.general")
	if !ok || !snapshot.Skipped {
		t.Fatalf("skip marker snapshot = %+v, ok = %v; want a record flagged Skipped", snapshot, ok)
	}
	if s.statusTracker.IsRSSItemSentToDestination(rss.GenerateEntryContentSignature(entry), "slack.rss.general", server.URL) {
		t.Fatal("filter mismatch marked the content signature as sent")
	}

	s.sendParsedRSSToWebhooks(t.Context(), feedResults(), config.FeedTypeGeneral, targets, nil)

	if afterSecondCycle := s.statusTracker.StatusSummary().RSSSentItems; afterSecondCycle != afterFirstCycle {
		t.Fatalf("RSS sent markers after re-skip = %d, want unchanged %d", afterSecondCycle, afterFirstCycle)
	}
	mu.Lock()
	got := postCount
	mu.Unlock()
	if got != 0 {
		t.Fatalf("POST count = %d, want 0 for a filtered entry", got)
	}
	// The marker count alone cannot tell a short-circuit from a rewrite of the
	// same composite key, so assert the audit trail: the entry is evaluated by
	// the filter exactly once and short-circuits as already-sent afterwards.
	outcomes := make([]string, 0, 2)
	for _, event := range deliveryAuditEventsForTest(t, s.config.DataDir) {
		if event.ItemKey == itemKey {
			outcomes = append(outcomes, event.Outcome)
		}
	}
	want := []string{status.DeliveryAuditOutcomeFiltered, status.DeliveryAuditOutcomeDeduplicated}
	if !reflect.DeepEqual(outcomes, want) {
		t.Fatalf("audit outcomes for the re-skipped item = %v, want %v", outcomes, want)
	}
}

func TestSendParsedRSSToWebhooksFilteredEntrySurvivesStatusReload(t *testing.T) {
	s := newTestScheduler(t)
	feedA := "https://feed-a.example.test/feed.xml"
	feedB := "https://feed-b.example.test/feed.xml"

	var mu sync.Mutex
	var requestBodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("ReadAll(body) error = %v", err)
		}
		mu.Lock()
		requestBodies = append(requestBodies, string(body))
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	targets := []webhookTarget{{
		url:           server.URL,
		destinationID: "slack.rss.general",
		messenger:     "slack",
		filters:       &filter.Rules{ExcludeFields: map[string][]string{"feed_url": {feedA}}},
	}}

	entryA, entryB := crossFeedFilterEntriesForTest(feedA, feedB)
	contentSig := rss.GenerateEntryContentSignature(entryA)

	s.sendParsedRSSToWebhooks(t.Context(), &rss.FeedResults{
		Entries:    map[string][]rss.Entry{feedA: {entryA}},
		FeedErrors: map[string]string{},
	}, config.FeedTypeGeneral, targets, nil)

	// Record the parsed item the way checkRSSOnce does, so the load-time
	// content-signature backfill has a parsed row to match the skip marker to.
	keyA := rss.GenerateEntryKeyForEntry(entryA)
	storedA := status.StoredRSSEntryFromRSS(entryA, keyA)
	storedA.FeedType = config.FeedTypeGeneral
	s.statusTracker.MarkRSSItemsParsed(feedA, map[string]status.StoredRSSEntry{keyA: storedA})
	if err := s.statusTracker.SavePendingChanges(); err != nil {
		t.Fatalf("SavePendingChanges() error = %v", err)
	}

	reloaded, err := status.NewTracker(s.config.DataDir)
	if err != nil {
		t.Fatalf("NewTracker(reload) error = %v", err)
	}
	s.statusTracker = reloaded

	if s.statusTracker.IsRSSItemSentToDestination(contentSig, "slack.rss.general", server.URL) {
		t.Fatal("status reload re-created the content-signature marker for a filtered entry")
	}

	s.sendParsedRSSToWebhooks(t.Context(), &rss.FeedResults{
		Entries:    map[string][]rss.Entry{feedB: {entryB}},
		FeedErrors: map[string]string{},
	}, config.FeedTypeGeneral, targets, nil)

	mu.Lock()
	bodies := append([]string(nil), requestBodies...)
	mu.Unlock()
	if len(bodies) != 1 {
		t.Fatalf("POST count after reload = %d, want 1 for the allowed second feed; bodies=%#v", len(bodies), bodies)
	}
	if !strings.Contains(bodies[0], entryB.Link) {
		t.Fatalf("delivered body does not reference feed B entry %q: %s", entryB.Link, bodies[0])
	}
}

// seedStaleAndFreshRSSRecoveryItemsForTest seeds the parsed index of s with
// staleCount items published far outside rss_max_item_age plus one fresh item,
// all unsent for every destination. The tracker returns them oldest-first, so
// the stale items sit at the head of the recovery list, which is the situation
// the per-cycle cap has to survive.
func seedStaleAndFreshRSSRecoveryItemsForTest(
	t *testing.T,
	s *Scheduler,
	feedURL string,
	staleCount int,
) ([]status.StoredRSSEntry, status.StoredRSSEntry) {
	t.Helper()

	now := time.Now().UTC()
	staleItems := make([]status.StoredRSSEntry, 0, staleCount)
	for i := 0; i < staleCount; i++ {
		guid := fmt.Sprintf("stale-recovery-%d", i)
		title := fmt.Sprintf("Stale Recovery %d", i)
		published := now.Add(-48*time.Hour + time.Duration(i)*time.Minute)
		stored := status.StoredRSSEntry{
			Key:         rss.GenerateEntryKey(feedURL, guid, "", title),
			FeedURL:     feedURL,
			FeedType:    config.FeedTypeGeneral,
			Title:       title,
			Link:        "https://example.test/articles/" + guid,
			Description: "stale body",
			Published:   published.Format(time.RFC3339Nano),
			GUID:        guid,
			FeedTitle:   "Example Feed",
			ParsedAt:    now.Format(time.RFC3339Nano),
		}
		s.statusTracker.MarkRSSItemParsed(feedURL, stored.Key, stored)
		staleItems = append(staleItems, stored)
	}

	freshPublished := now.Add(-2 * time.Minute)
	fresh := status.StoredRSSEntry{
		Key:         rss.GenerateEntryKey(feedURL, "fresh-recovery", "", "Fresh Recovery Item"),
		FeedURL:     feedURL,
		FeedType:    config.FeedTypeGeneral,
		Title:       "Fresh Recovery Item",
		Link:        "https://example.test/articles/fresh-recovery",
		Description: "fresh body",
		Published:   freshPublished.Format(time.RFC3339Nano),
		GUID:        "fresh-recovery",
		FeedTitle:   "Example Feed",
		ParsedAt:    now.Format(time.RFC3339Nano),
	}
	s.statusTracker.MarkRSSItemParsed(feedURL, fresh.Key, fresh)

	if err := s.statusTracker.SavePendingChanges(); err != nil {
		t.Fatalf("SavePendingChanges: %v", err)
	}
	return staleItems, fresh
}

// TestSendUnsentRSSItemsStaleItemsDoNotConsumeCycleCap pins the starvation fix:
// stale recovery items are never marked sent, so they stay at the head of the
// oldest-first recovery list. While they were counted against
// max_rss_entries_per_cycle they filled the whole budget every cycle, and the
// fresh item behind them, together with its queued retry row (recovery is the
// only RSS retry driver), never went out. The stale backlog is varied from
// exactly the cycle limit up to ten times the limit, because the starvation is a
// property of the backlog being at least as large as the cap, not of one size.
func TestSendUnsentRSSItemsStaleItemsDoNotConsumeCycleCap(t *testing.T) {
	const cycleLimit = 3

	for _, staleCount := range []int{cycleLimit, cycleLimit + 1, 10 * cycleLimit} {
		t.Run(fmt.Sprintf("stale_backlog_%d", staleCount), func(t *testing.T) {
			s := newTestScheduler(t)
			feedURL := "https://example.test/feed.xml"
			s.config.Feeds.GeneralFeeds = []string{feedURL}
			s.feedTypeMap = buildFeedTypeMap(s.config)
			s.config.RSSMaxEntriesPerCycle = cycleLimit
			s.config.RSSMaxItemAge = time.Hour

			var mu sync.Mutex
			var requestBodies []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Errorf("ReadAll(body) error = %v", err)
				}
				mu.Lock()
				requestBodies = append(requestBodies, string(body))
				mu.Unlock()
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()
			s.config.SlackWebhooks.RSS = config.WebhookConfig{Enabled: true, URL: server.URL}

			staleItems, fresh := seedStaleAndFreshRSSRecoveryItemsForTest(t, s, feedURL, staleCount)

			destinationID := rssDestinationIDForTarget(status.MessengerSlack, config.FeedTypeGeneral, "")
			if !enqueueRetryForTest(
				t,
				s.statusTracker,
				fresh.Key,
				destinationID,
				status.MessengerSlack.String(),
				status.RetryItemTypeRSS.String(),
				fresh.Title,
				"previous webhook failure",
				5,
				time.Hour,
			) {
				t.Fatal("EnqueueRetry() unexpectedly moved the fresh item to dead letter")
			}
			if got := len(s.statusTracker.GetRetryItemsByType("rss")); got != 1 {
				t.Fatalf("queued RSS retry rows before recovery = %d, want 1", got)
			}

			s.sendUnsentRSSItems(t.Context(), nil)

			mu.Lock()
			bodies := append([]string(nil), requestBodies...)
			mu.Unlock()
			if len(bodies) != 1 {
				t.Fatalf("POST count after cycle 1 = %d, want 1 (the fresh item behind %d stale items)", len(bodies), len(staleItems))
			}
			if !strings.Contains(bodies[0], fresh.Title) {
				t.Fatalf("delivered body does not contain the fresh title %q: %s", fresh.Title, bodies[0])
			}
			if !s.statusTracker.IsRSSItemSentToWebhook(fresh.Key, server.URL) {
				t.Fatal("fresh recovery item was not marked sent")
			}
			if got := len(s.statusTracker.GetRetryItemsByType("rss")); got != 0 {
				t.Fatalf("queued RSS retry rows after cycle 1 = %d, want 0 (recovery is the RSS retry driver)", got)
			}

			s.sendUnsentRSSItems(t.Context(), nil)

			mu.Lock()
			bodies = append([]string(nil), requestBodies...)
			mu.Unlock()
			if len(bodies) != 1 {
				t.Fatalf("POST count after cycle 2 = %d, want 1 (no re-send, no stale send)", len(bodies))
			}
		})
	}
}

// TestSendUnsentRSSItemsStaleItemsStayUnmarkedAndAudited is the regression guard
// for the readme.md promise that entries older than rss_max_item_age are skipped
// without a sent marker, so raising the limit later still recovers them. It also
// pins that stale items consume their own budget instead of the send budget.
//
//nolint:gocyclo // long sequential scenario asserting audit/state outcomes across multiple items; not a table split candidate
func TestSendUnsentRSSItemsStaleItemsStayUnmarkedAndAudited(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()

	s := newTestScheduler(t)
	feedURL := "https://example.test/feed.xml"
	s.config.Feeds.GeneralFeeds = []string{feedURL}
	s.feedTypeMap = buildFeedTypeMap(s.config)
	s.config.RSSMaxEntriesPerCycle = 3
	s.config.RSSMaxItemAge = time.Hour

	var mu sync.Mutex
	var requestBodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("ReadAll(body) error = %v", err)
		}
		mu.Lock()
		requestBodies = append(requestBodies, string(body))
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	s.config.SlackWebhooks.RSS = config.WebhookConfig{Enabled: true, URL: server.URL}

	staleItems, _ := seedStaleAndFreshRSSRecoveryItemsForTest(t, s, feedURL, s.config.RSSMaxEntriesPerCycle+1)
	destinationID := rssDestinationIDForTarget(status.MessengerSlack, config.FeedTypeGeneral, "")

	assertStaleItemsUnmarked := func(cycle int) {
		t.Helper()
		mu.Lock()
		bodies := append([]string(nil), requestBodies...)
		mu.Unlock()
		for _, stale := range staleItems {
			for _, body := range bodies {
				if strings.Contains(body, stale.Title) {
					t.Fatalf("cycle %d: stale item %q was delivered: %s", cycle, stale.Title, body)
				}
			}
			if s.statusTracker.IsRSSItemSentToWebhook(stale.Key, server.URL) {
				t.Fatalf("cycle %d: stale item %q was marked sent", cycle, stale.Key)
			}
			if s.statusTracker.IsRSSItemSentToDestination(stale.Key, destinationID, server.URL) {
				t.Fatalf("cycle %d: stale item %q was marked sent for destination %s", cycle, stale.Key, destinationID)
			}
			entry, err := status.RSSEntryFromStored(stale)
			if err != nil {
				t.Fatalf("RSSEntryFromStored() error = %v", err)
			}
			contentSig := rss.GenerateEntryContentSignature(entry)
			if s.statusTracker.IsRSSItemSentToDestination(contentSig, destinationID, server.URL) {
				t.Fatalf("cycle %d: stale item %q content signature was marked sent", cycle, stale.Key)
			}
		}
	}

	staleAuditCount := func(cycle int) int {
		t.Helper()
		events := deliveryAuditEventsForTest(t, s.config.DataDir)
		count := 0
		for _, stale := range staleItems {
			for _, event := range events {
				if event.ItemKey == stale.Key &&
					event.Outcome == status.DeliveryAuditOutcomeStale &&
					event.Reason == status.DeliveryAuditReasonStale {
					count++
				}
			}
		}
		if count == 0 {
			t.Fatalf("cycle %d: no stale audit events recorded", cycle)
		}
		return count
	}

	s.sendUnsentRSSItems(t.Context(), nil)
	assertStaleItemsUnmarked(1)
	afterCycle1 := staleAuditCount(1)
	if afterCycle1 != s.config.RSSMaxEntriesPerCycle {
		t.Fatalf("stale audit events after cycle 1 = %d, want %d (one per stale item in the batch)",
			afterCycle1, s.config.RSSMaxEntriesPerCycle)
	}

	// The stale budget is what discriminates the fix: with 4 stale plus 1 fresh
	// item and a cycle limit of 3 the batch is 3 stale plus 1 fresh and exactly
	// one stale item is deferred. Before the fix it was 3 and 2.
	logEntry := findTestLogEntry(hook, "Found unsent RSS items for webhook, sending")
	if logEntry == nil {
		t.Fatal("missing recovery batch log entry")
	}
	if got := logEntry.Data["send_batch_count"]; got != len(staleItems) {
		t.Fatalf("send_batch_count = %#v, want %d", got, len(staleItems))
	}
	if got := logEntry.Data["deferred_by_cap"]; got != 1 {
		t.Fatalf("deferred_by_cap = %#v, want 1", got)
	}
	if got := logEntry.Data["unsent_count"]; got != len(staleItems)+1 {
		t.Fatalf("unsent_count = %#v, want %d", got, len(staleItems)+1)
	}

	// Second cycle: the stale items are re-evaluated and audited again, because
	// nothing was persisted for them.
	s.sendUnsentRSSItems(t.Context(), nil)
	assertStaleItemsUnmarked(2)
	afterCycle2 := staleAuditCount(2)
	if afterCycle2 != 2*s.config.RSSMaxEntriesPerCycle {
		t.Fatalf("stale audit events after cycle 2 = %d, want %d (audited once per cycle, not once ever)",
			afterCycle2, 2*s.config.RSSMaxEntriesPerCycle)
	}

	// The reason no marker is written: readme.md promises that raising
	// rss_max_item_age later still recovers the skipped items. rss_max_item_age
	// comes from the per-cycle config snapshot, so a hot reload widens the
	// horizon on the next cycle and the backlog goes out, capped as usual.
	s.config.RSSMaxItemAge = 72 * time.Hour

	sentStaleCount := func() int {
		t.Helper()
		count := 0
		for _, stale := range staleItems {
			if s.statusTracker.IsRSSItemSentToWebhook(stale.Key, server.URL) {
				count++
			}
		}
		return count
	}

	s.sendUnsentRSSItems(t.Context(), nil)

	mu.Lock()
	bodies := append([]string(nil), requestBodies...)
	mu.Unlock()
	// One POST for the fresh item in cycle 1, then the cap's worth of recovered
	// former-stale items in cycle 3.
	if len(bodies) != 1+s.config.RSSMaxEntriesPerCycle {
		t.Fatalf("POST count after the horizon grew = %d, want %d", len(bodies), 1+s.config.RSSMaxEntriesPerCycle)
	}
	if got := sentStaleCount(); got != s.config.RSSMaxEntriesPerCycle {
		t.Fatalf("recovered stale items after cycle 3 = %d, want %d (capped by max_rss_entries_per_cycle)",
			got, s.config.RSSMaxEntriesPerCycle)
	}

	s.sendUnsentRSSItems(t.Context(), nil)

	if got := sentStaleCount(); got != len(staleItems) {
		t.Fatalf("recovered stale items after cycle 4 = %d, want %d (all of them)", got, len(staleItems))
	}
}

func TestLimitUnsentRSSItemsForCycleSeparatesStaleBudget(t *testing.T) {
	now := time.Now().UTC()
	cfg := &config.Config{RSSMaxItemAge: time.Hour}
	cfg.StatusRetention.RSSParsedMaxAge = 8760 * time.Hour

	noFreshnessCfg := &config.Config{RSSMaxItemAge: 0}
	noFreshnessCfg.StatusRetention.RSSParsedMaxAge = 8760 * time.Hour

	item := func(key string, published time.Time) status.UnsentRSSItem {
		return status.UnsentRSSItem{Key: key, Published: published}
	}
	fresh := func(key string) status.UnsentRSSItem { return item(key, now.Add(-2*time.Minute)) }
	stale := func(key string) status.UnsentRSSItem { return item(key, now.Add(-48*time.Hour)) }
	undated := func(key string) status.UnsentRSSItem { return item(key, time.Time{}) }

	tests := []struct {
		name         string
		entries      []status.UnsentRSSItem
		limit        int
		cfg          *config.Config
		wantKeys     []string
		wantDeferred int
	}{
		{
			name:         "all fresh under limit",
			entries:      []status.UnsentRSSItem{fresh("f1"), fresh("f2")},
			limit:        3,
			cfg:          cfg,
			wantKeys:     []string{"f1", "f2"},
			wantDeferred: 0,
		},
		{
			name:         "all fresh at limit",
			entries:      []status.UnsentRSSItem{fresh("f1"), fresh("f2"), fresh("f3")},
			limit:        3,
			cfg:          cfg,
			wantKeys:     []string{"f1", "f2", "f3"},
			wantDeferred: 0,
		},
		{
			name:         "all fresh over limit",
			entries:      []status.UnsentRSSItem{fresh("f1"), fresh("f2"), fresh("f3"), fresh("f4"), fresh("f5")},
			limit:        3,
			cfg:          cfg,
			wantKeys:     []string{"f1", "f2", "f3"},
			wantDeferred: 2,
		},
		{
			name:         "stale only over limit",
			entries:      []status.UnsentRSSItem{stale("s1"), stale("s2"), stale("s3"), stale("s4"), stale("s5")},
			limit:        3,
			cfg:          cfg,
			wantKeys:     []string{"s1", "s2", "s3"},
			wantDeferred: 2,
		},
		{
			name: "mixed with stale first keeps order and splits budgets",
			entries: []status.UnsentRSSItem{
				stale("s1"), stale("s2"), stale("s3"), stale("s4"),
				fresh("f1"), fresh("f2"), fresh("f3"), fresh("f4"),
			},
			limit:        3,
			cfg:          cfg,
			wantKeys:     []string{"s1", "s2", "s3", "f1", "f2", "f3"},
			wantDeferred: 2,
		},
		{
			// Undated items are exempt from the age gate, so they will actually
			// be delivered and must consume a delivery slot, not a stale slot.
			name: "undated entries count as sendable",
			entries: []status.UnsentRSSItem{
				undated("u1"), undated("u2"), undated("u3"), undated("u4"),
				fresh("f1"), fresh("f2"),
			},
			limit:        3,
			cfg:          cfg,
			wantKeys:     []string{"u1", "u2", "u3"},
			wantDeferred: 3,
		},
		{
			name:         "freshness disabled keeps the plain cap",
			entries:      []status.UnsentRSSItem{stale("s1"), stale("s2"), stale("s3"), stale("s4"), stale("s5")},
			limit:        3,
			cfg:          noFreshnessCfg,
			wantKeys:     []string{"s1", "s2", "s3"},
			wantDeferred: 2,
		},
		{
			name:         "non positive limit disables capping",
			entries:      []status.UnsentRSSItem{fresh("f1"), fresh("f2"), fresh("f3"), fresh("f4"), fresh("f5")},
			limit:        0,
			cfg:          cfg,
			wantKeys:     []string{"f1", "f2", "f3", "f4", "f5"},
			wantDeferred: 0,
		},
		{
			name:         "nil config falls back to the plain cap",
			entries:      []status.UnsentRSSItem{fresh("f1"), fresh("f2"), fresh("f3"), fresh("f4"), fresh("f5")},
			limit:        3,
			cfg:          nil,
			wantKeys:     []string{"f1", "f2", "f3"},
			wantDeferred: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			batch, deferred := limitUnsentRSSItemsForCycle(tt.entries, tt.limit, tt.cfg)

			gotKeys := make([]string, 0, len(batch))
			for _, entry := range batch {
				gotKeys = append(gotKeys, entry.Key)
			}
			if !reflect.DeepEqual(gotKeys, tt.wantKeys) {
				t.Fatalf("batch keys = %v, want %v", gotKeys, tt.wantKeys)
			}
			if deferred != tt.wantDeferred {
				t.Fatalf("deferred = %d, want %d", deferred, tt.wantDeferred)
			}
			if len(batch)+deferred != len(tt.entries) {
				t.Fatalf("len(batch)+deferred = %d, want len(entries) = %d", len(batch)+deferred, len(tt.entries))
			}
		})
	}
}

// TestSendUnsentRSSItemsRecoveryCapHoldsForNearHorizonItems pins the parity
// between the staleness predicate the recovery limiter uses
// (rssRecoveryItemIsStale) and the one sendRSSEntryToTarget applies. If the
// limiter classified an item as stale that the send path still delivers, the
// item would occupy a stale-budget slot and then be POSTed anyway, so real sends
// per cycle could reach 2 * max_rss_entries_per_cycle. All items here are inside
// rss_max_item_age; half of them sit close to the horizon.
func TestSendUnsentRSSItemsRecoveryCapHoldsForNearHorizonItems(t *testing.T) {
	s := newTestScheduler(t)
	feedURL := "https://example.test/feed.xml"
	s.config.Feeds.GeneralFeeds = []string{feedURL}
	s.feedTypeMap = buildFeedTypeMap(s.config)
	s.config.RSSMaxEntriesPerCycle = 3
	s.config.RSSMaxItemAge = time.Hour

	var mu sync.Mutex
	postCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		postCount++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	s.config.SlackWebhooks.RSS = config.WebhookConfig{Enabled: true, URL: server.URL}

	now := time.Now().UTC()
	ages := []time.Duration{
		50 * time.Minute, 49 * time.Minute, 48 * time.Minute, // just inside the horizon
		3 * time.Minute, 2 * time.Minute, time.Minute, // clearly fresh
	}
	keys := make([]string, 0, len(ages))
	for i, age := range ages {
		guid := fmt.Sprintf("near-horizon-%d", i)
		title := fmt.Sprintf("Near Horizon %d", i)
		stored := status.StoredRSSEntry{
			Key:       rss.GenerateEntryKey(feedURL, guid, "", title),
			FeedURL:   feedURL,
			FeedType:  config.FeedTypeGeneral,
			Title:     title,
			Link:      "https://example.test/articles/" + guid,
			Published: now.Add(-age).Format(time.RFC3339Nano),
			GUID:      guid,
			FeedTitle: "Example Feed",
			ParsedAt:  now.Format(time.RFC3339Nano),
		}
		s.statusTracker.MarkRSSItemParsed(feedURL, stored.Key, stored)
		keys = append(keys, stored.Key)
	}
	if err := s.statusTracker.SavePendingChanges(); err != nil {
		t.Fatalf("SavePendingChanges: %v", err)
	}

	s.sendUnsentRSSItems(t.Context(), nil)

	mu.Lock()
	got := postCount
	mu.Unlock()
	if got != s.config.RSSMaxEntriesPerCycle {
		t.Fatalf("POST count after one recovery cycle = %d, want %d (max_rss_entries_per_cycle)",
			got, s.config.RSSMaxEntriesPerCycle)
	}

	sent := 0
	for _, key := range keys {
		if s.statusTracker.IsRSSItemSentToWebhook(key, server.URL) {
			sent++
		}
	}
	if sent != s.config.RSSMaxEntriesPerCycle {
		t.Fatalf("sent markers after one recovery cycle = %d, want %d", sent, s.config.RSSMaxEntriesPerCycle)
	}

	for _, event := range deliveryAuditEventsForTest(t, s.config.DataDir) {
		if event.Outcome == status.DeliveryAuditOutcomeStale {
			t.Fatalf("item %q inside rss_max_item_age was audited as stale", event.ItemKey)
		}
	}
}

const (
	rssCaseFeedURL = "https://feeds.test/case.xml"
	// rssCaseWebhookURL is the single destination all link-case tests deliver to.
	rssCaseWebhookURL = "https://hooks.example.test/case"
	// rssCasePreNormalizationKey is the key a pre-fix build stored for
	// rssCaseFeedURL plus the link https://Example.test/story. It is hard-coded
	// on purpose: recomputing it would hide exactly the regression under test.
	rssCasePreNormalizationKey = "rss:v2:link:988ce11c31c4db42866ad71ca917335b2ece6454f3cdc370494cd38b11b5d400"
)

func rssCaseTargets() []webhookTarget {
	return []webhookTarget{{
		url:           rssCaseWebhookURL,
		destinationID: "discord.rss.general",
		messenger:     "discord",
	}}
}

// rssCaseEntry builds the same undated story under a given link spelling. The
// missing Published is deliberate: it forces the v4 content signature to embed
// the entry key, so the content-signature check cannot mask a key mismatch.
func rssCaseEntry(link string) rss.Entry {
	return rss.Entry{
		FeedURL:   rssCaseFeedURL,
		Title:     "Story",
		Link:      link,
		FeedTitle: "Case Feed",
	}
}

// rssCaseFeedResults returns a fresh FeedResults per cycle;
// filterAndRecordNewRSSItems mutates Entries in place, so a reused value would
// silently turn a two-cycle test into a one-cycle test.
func rssCaseFeedResults(link string) *rss.FeedResults {
	return &rss.FeedResults{
		Entries:    map[string][]rss.Entry{rssCaseFeedURL: {rssCaseEntry(link)}},
		FeedErrors: map[string]string{},
	}
}

func configureRSSCaseRecoveryRoute(s *Scheduler) {
	s.config.Feeds.GeneralFeeds = []string{rssCaseFeedURL}
	s.config.DiscordWebhooks.RSS = config.WebhookConfig{
		Enabled: true,
		URL:     rssCaseWebhookURL,
	}
}

func seedRSSCaseParsedItem(t *testing.T, s *Scheduler, key string) {
	t.Helper()
	stored := status.StoredRSSEntryFromRSS(rssCaseEntry("https://Example.test/story"), key)
	stored.FeedType = config.FeedTypeGeneral
	s.statusTracker.MarkRSSItemsParsed(rssCaseFeedURL, map[string]status.StoredRSSEntry{key: stored})
}

func rssCaseParsedKeys(t *testing.T, s *Scheduler) []string {
	t.Helper()
	keys := make([]string, 0, 2)
	for key := range s.statusTracker.GetRSSParsedItemKeys(rssCaseFeedURL) {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// TestRSSHostCaseVariantDeliversOnce pins that a feed which changes the case of
// the link host between two fetches produces one delivery, not two.
func TestRSSHostCaseVariantDeliversOnce(t *testing.T) {
	s := newTestScheduler(t)
	sender := &recordingWebhookSender{}
	s.discordWebhookSender = sender
	targets := rssCaseTargets()

	for _, link := range []string{"https://Example.test/story", "https://example.test/story"} {
		feedResults := rssCaseFeedResults(link)
		s.filterAndRecordNewRSSItems(feedResults, config.FeedTypeGeneral)
		s.sendParsedRSSToWebhooksWithConfig(t.Context(), s.config, feedResults, config.FeedTypeGeneral, targets, nil)
	}

	if got := sender.rssCallCount(); got != 1 {
		t.Fatalf("RSS webhook sends for one story under two host spellings = %d, want 1; sends=%#v",
			got, sender.rssCallsSnapshot())
	}
	wantKey := rss.GenerateEntryKeyForEntry(rssCaseEntry("https://example.test/story"))
	if got := rssCaseParsedKeys(t, s); !reflect.DeepEqual(got, []string{wantKey}) {
		t.Fatalf("parsed keys after two host spellings = %#v, want exactly one row keyed %q", got, wantKey)
	}
}

// TestRSSItemSentUnderPreCaseNormalizationKeySurvivesSpellingFlip covers the
// upgrade case the lookup alias alone cannot: the item was delivered by a
// pre-fix build under the mixed-case spelling, and the feed now serves the
// lowercase spelling, whose lookup keys can never contain the old key. The
// load-time content-signature backfill is what closes it, so the tracker is
// re-read from disk exactly as it is on a restart.
func TestRSSItemSentUnderPreCaseNormalizationKeySurvivesSpellingFlip(t *testing.T) {
	s := newTestScheduler(t)
	sender := &recordingWebhookSender{}
	s.discordWebhookSender = sender
	configureRSSCaseRecoveryRoute(s)

	seedRSSCaseParsedItem(t, s, rssCasePreNormalizationKey)
	s.statusTracker.MarkRSSItemSentToDestination(
		rssCasePreNormalizationKey,
		"Story",
		"Case Feed",
		"discord.rss.general",
	)
	if err := s.statusTracker.SavePendingChanges(); err != nil {
		t.Fatalf("SavePendingChanges: %v", err)
	}

	reloaded, err := status.NewTracker(s.config.DataDir)
	if err != nil {
		t.Fatalf("NewTracker(reload) error = %v", err)
	}
	s.statusTracker = reloaded

	feedResults := rssCaseFeedResults("https://example.test/story")
	s.filterAndRecordNewRSSItems(feedResults, config.FeedTypeGeneral)
	s.sendParsedRSSToWebhooksWithConfig(t.Context(), s.config, feedResults, config.FeedTypeGeneral, rssCaseTargets(), nil)
	s.sendUnsentRSSItemsWithConfig(t.Context(), s.config, nil)

	if got := sender.rssCallCount(); got != 0 {
		t.Fatalf("RSS webhook sends after the spelling flipped to lowercase = %d, want 0; sends=%#v",
			got, sender.rssCallsSnapshot())
	}
}

// TestRSSItemSentUnderPreCaseNormalizationKeyIsNotResent pins the upgrade path:
// an item already delivered under the pre-normalisation key must not be
// delivered again, and must not gain a second parsed row.
func TestRSSItemSentUnderPreCaseNormalizationKeyIsNotResent(t *testing.T) {
	s := newTestScheduler(t)
	sender := &recordingWebhookSender{}
	s.discordWebhookSender = sender
	configureRSSCaseRecoveryRoute(s)

	seedRSSCaseParsedItem(t, s, rssCasePreNormalizationKey)
	s.statusTracker.MarkRSSItemSentToDestination(
		rssCasePreNormalizationKey,
		"Story",
		"Case Feed",
		"discord.rss.general",
	)

	feedResults := rssCaseFeedResults("https://Example.test/story")
	recorded := s.filterAndRecordNewRSSItems(feedResults, config.FeedTypeGeneral)
	s.sendParsedRSSToWebhooksWithConfig(t.Context(), s.config, feedResults, config.FeedTypeGeneral, rssCaseTargets(), nil)
	s.sendUnsentRSSItemsWithConfig(t.Context(), s.config, nil)

	if recorded != 0 {
		t.Fatalf("filterAndRecordNewRSSItems(already parsed under pre-fix key) = %d, want 0", recorded)
	}
	if got := rssCaseParsedKeys(t, s); !reflect.DeepEqual(got, []string{rssCasePreNormalizationKey}) {
		t.Fatalf("parsed keys = %#v, want only the stored pre-fix key %q", got, rssCasePreNormalizationKey)
	}
	if got := sender.rssCallCount(); got != 0 {
		t.Fatalf("RSS webhook sends after upgrade of an already delivered item = %d, want 0; sends=%#v",
			got, sender.rssCallsSnapshot())
	}
}

// TestRSSUnsentPreCaseNormalizationItemIsDeliveredOnce pins the other half of
// the upgrade path: an item parsed but never delivered under the pre-fix key is
// still delivered, exactly once, through recovery.
func TestRSSUnsentPreCaseNormalizationItemIsDeliveredOnce(t *testing.T) {
	s := newTestScheduler(t)
	sender := &recordingWebhookSender{}
	s.discordWebhookSender = sender
	configureRSSCaseRecoveryRoute(s)

	seedRSSCaseParsedItem(t, s, rssCasePreNormalizationKey)

	for i := 0; i < 2; i++ {
		feedResults := rssCaseFeedResults("https://Example.test/story")
		s.filterAndRecordNewRSSItems(feedResults, config.FeedTypeGeneral)
		s.sendParsedRSSToWebhooksWithConfig(t.Context(), s.config, feedResults, config.FeedTypeGeneral, rssCaseTargets(), nil)
		s.sendUnsentRSSItemsWithConfig(t.Context(), s.config, nil)
	}

	if got := sender.rssCallCount(); got != 1 {
		t.Fatalf("RSS webhook sends for an item unsent at upgrade = %d, want 1; sends=%#v",
			got, sender.rssCallsSnapshot())
	}
	if got := rssCaseParsedKeys(t, s); !reflect.DeepEqual(got, []string{rssCasePreNormalizationKey}) {
		t.Fatalf("parsed keys = %#v, want only the stored pre-fix key %q", got, rssCasePreNormalizationKey)
	}
}

func TestAPIDeadLetterAfterRetriesKeepsAttemptHistoryAndPayload(t *testing.T) {
	s := newTestScheduler(t)
	s.config.RetryMaxAttempts = 10
	s.config.RetryWindow = time.Hour

	entry := api.RansomwareEntry{
		ID:         "dead-letter-entry",
		Group:      "lockbit",
		Victim:     "Terminal Corp",
		Country:    "DE",
		Discovered: time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC),
	}
	destinationID := apiDestinationID(status.MessengerSlack)
	attempts := 0
	target := apiDeliveryTarget{
		url:           "https://hooks.slack.test/services/dead-letter",
		destinationID: destinationID,
		messenger:     status.MessengerSlack.String(),
		send: func(_ context.Context, _ string, _ api.RansomwareEntry, _ *notifyfmt.FormatOptions) error {
			attempts++
			if attempts < 3 {
				return &webhookhttp.HTTPStatusError{
					Provider:   "slack",
					StatusCode: http.StatusServiceUnavailable,
					Status:     "503 Service Unavailable",
				}
			}
			return &webhookhttp.HTTPStatusError{
				Provider:   "slack",
				StatusCode: http.StatusForbidden,
				Status:     "403 Forbidden",
			}
		},
	}

	for cycle := 0; cycle < 3; cycle++ {
		s.processAPIDeliveryTarget(t.Context(), s.config, []api.RansomwareEntry{entry}, target)
	}

	if attempts != 3 {
		t.Fatalf("webhook send attempts = %d, want 3", attempts)
	}
	deadLetters := s.statusTracker.GetDeadLetterItems()
	if len(deadLetters) != 1 {
		t.Fatalf("dead letter count = %d, want 1", len(deadLetters))
	}
	got := deadLetters[0]
	if got.RetryCount != 2 {
		t.Fatalf("RetryCount = %d, want 2 inherited from the queued retry row", got.RetryCount)
	}
	if got.TerminalReason != status.TerminalReasonTerminalFailure {
		t.Fatalf("TerminalReason = %q, want %q", got.TerminalReason, status.TerminalReasonTerminalFailure)
	}
	if got.StatusCode != http.StatusForbidden {
		t.Fatalf("StatusCode = %d, want %d", got.StatusCode, http.StatusForbidden)
	}
	if got.DestinationID != destinationID {
		t.Fatalf("DestinationID = %q, want %q", got.DestinationID, destinationID)
	}
	var replayed api.RansomwareEntry
	if err := json.Unmarshal(got.Payload, &replayed); err != nil {
		t.Fatalf("Unmarshal(dead letter payload) error = %v (len=%d)", err, len(got.Payload))
	}
	if replayed.ID != entry.ID || replayed.Victim != entry.Victim {
		t.Fatalf("replay payload = %#v, want entry %#v", replayed, entry)
	}
	if records := s.statusTracker.GetQueuedRetryItemsByType(status.RetryItemTypeAPI.String()); len(records) != 0 {
		t.Fatalf("queued API retry records after dead-lettering = %d, want 0", len(records))
	}
}

// TestRSSEntryExceedsMaxAgeDoesNotDropUndatedItems pins the decision that an
// item with no usable publication date is NOT "too old". Treating "unknown age"
// as "infinitely old" dropped 100% of the items of a feed whose timestamps the
// parser could not read, and raising rss_max_item_age never recovered them
// because the skip writes no sent marker. The exemption sits in the one shared
// predicate so the send path (sendRSSEntryToTarget) and the recovery limiter
// (limitUnsentRSSItemsForCycle via rssRecoveryItemIsStale) cannot diverge.
func TestRSSEntryExceedsMaxAgeDoesNotDropUndatedItems(t *testing.T) {
	if rssEntryExceedsMaxAge(time.Time{}, 48*time.Hour) {
		t.Fatal("rssEntryExceedsMaxAge(zero published, 48h) = true, want false (undated is exempt)")
	}

	cfg := &config.Config{RSSMaxItemAge: 48 * time.Hour}
	cfg.StatusRetention.RSSParsedMaxAge = 8760 * time.Hour
	if rssRecoveryItemIsStale(cfg, status.UnsentRSSItem{}) {
		t.Fatal("rssRecoveryItemIsStale(undated item) = true, want false (limiter must agree with the send path)")
	}

	// Dated items keep the old behaviour on both sides.
	if !rssEntryExceedsMaxAge(time.Now().Add(-72*time.Hour), 48*time.Hour) {
		t.Fatal("rssEntryExceedsMaxAge(72h old, 48h) = false, want true")
	}
	if !rssRecoveryItemIsStale(cfg, status.UnsentRSSItem{Published: time.Now().Add(-72 * time.Hour)}) {
		t.Fatal("rssRecoveryItemIsStale(72h old item) = false, want true")
	}
}

// TestSendRSSEntryToTargetDeliversUndatedItemWithMaxItemAge is the end-to-end
// half of the same decision: with rss_max_item_age set, an undated entry reaches
// the webhook, is marked sent for the destination, and is audited as delivered -
// never as stale.
func TestSendRSSEntryToTargetDeliversUndatedItemWithMaxItemAge(t *testing.T) {
	s := newTestScheduler(t)
	s.config.RSSMaxItemAge = 48 * time.Hour

	var mu sync.Mutex
	postCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		postCount++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	feedURL := "https://feeds.test/undated.xml"
	s.config.Feeds.GeneralFeeds = []string{feedURL}
	s.config.SlackWebhooks.RSS = config.WebhookConfig{Enabled: true, URL: server.URL}
	s.feedTypeMap = buildFeedTypeMap(s.config)

	entry := rss.Entry{
		Title:     "Undated Target Article",
		Link:      "https://example.test/undated-target",
		GUID:      "guid-undated-target",
		FeedURL:   feedURL,
		FeedTitle: "Undated Feed",
	}
	target := webhookTarget{
		url:           server.URL,
		destinationID: "slack.rss.general",
		messenger:     "slack",
	}
	item := freshRSSDeliveryItem(entry)

	outcome := s.sendRSSEntryToTarget(
		t.Context(),
		s.config,
		config.FeedTypeGeneral,
		target,
		item,
		rssDeliveryModeFresh,
		0,
		1,
		nil,
	)

	if outcome != rssDeliveryOutcomeSent {
		t.Fatalf("sendRSSEntryToTarget(undated entry) outcome = %v, want sent", outcome)
	}
	mu.Lock()
	posts := postCount
	mu.Unlock()
	if posts != 1 {
		t.Fatalf("POSTs to the local webhook = %d, want 1", posts)
	}
	if !s.statusTracker.IsRSSItemSentToDestination(item.key, target.destinationID, target.url) {
		t.Error("undated entry was not marked sent for the destination")
	}
	contentSig := rss.GenerateEntryContentSignature(entry)
	if !s.statusTracker.IsRSSItemSentToDestination(contentSig, target.destinationID, target.url) {
		t.Error("undated entry content signature was not marked sent for the destination")
	}

	events := deliveryAuditEventsForTest(t, s.config.DataDir)
	if !hasDeliveryAuditEvent(events, item.key, status.DeliveryAuditOutcomeDelivered, "") {
		t.Fatalf("no delivered audit event for the undated entry: %+v", events)
	}
	for _, event := range events {
		if event.Outcome == status.DeliveryAuditOutcomeStale {
			t.Fatalf("undated entry was audited as stale: %+v", event)
		}
	}
}

// TestRecordWebhookDeliveryFailureHonorsRetryBudget pins that a retryable error
// classified by webhookhttp.IsRetryableError reaches the retry-store decision through
// recordWebhookDeliveryFailure and its RetryRequest construction.
func TestRecordWebhookDeliveryFailureHonorsRetryBudget(t *testing.T) {
	newRetryableError := func() error {
		return &webhookhttp.HTTPStatusError{
			Provider:   "discord",
			StatusCode: http.StatusBadGateway,
			Status:     "502 Bad Gateway",
		}
	}

	t.Run("one retry after the first send", func(t *testing.T) {
		s := newTestScheduler(t)

		s.recordWebhookDeliveryFailure(
			"item-1", "discord.ransomware", "discord", "api", "Example",
			newRetryableError(), 1, time.Hour,
		)
		s.recordWebhookDeliveryFailure(
			"item-1", "discord.ransomware", "discord", "api", "Example",
			newRetryableError(), 1, time.Hour,
		)

		snapshot := s.statusTracker.RetryStatusSnapshot()
		if snapshot.RetryQueueItems != 0 {
			t.Fatalf("retry queue items = %d, want 0", snapshot.RetryQueueItems)
		}
		deadLetters := s.statusTracker.GetDeadLetterItems()
		if len(deadLetters) != 1 {
			t.Fatalf("dead letter count = %d, want 1", len(deadLetters))
		}
		if deadLetters[0].TerminalReason != status.TerminalReasonMaxAttempts {
			t.Fatalf("TerminalReason = %q, want %q", deadLetters[0].TerminalReason, status.TerminalReasonMaxAttempts)
		}
		if deadLetters[0].RetryCount != 2 {
			t.Fatalf("RetryCount = %d, want 2", deadLetters[0].RetryCount)
		}
	})

	t.Run("two retries keep the second failure queued", func(t *testing.T) {
		s := newTestScheduler(t)

		s.recordWebhookDeliveryFailure(
			"item-1", "discord.ransomware", "discord", "api", "Example",
			newRetryableError(), 2, time.Hour,
		)
		s.recordWebhookDeliveryFailure(
			"item-1", "discord.ransomware", "discord", "api", "Example",
			newRetryableError(), 2, time.Hour,
		)

		snapshot := s.statusTracker.RetryStatusSnapshot()
		if snapshot.RetryQueueItems != 1 {
			t.Fatalf("retry queue items = %d, want 1", snapshot.RetryQueueItems)
		}
		if deadLetters := s.statusTracker.GetDeadLetterItems(); len(deadLetters) != 0 {
			t.Fatalf("dead letter count = %d, want 0", len(deadLetters))
		}
		queued := s.statusTracker.GetRetryItemsByType("api")
		if len(queued) != 1 {
			t.Fatalf("queued retry items = %d, want 1", len(queued))
		}
		if queued[0].RetryCount != 2 {
			t.Fatalf("queued RetryCount = %d, want 2", queued[0].RetryCount)
		}
	})
}

// reanchorStoryVariantsForTest returns count link variants of one story. They
// share Title, Description and the exact same Published instant, so they share
// one v3 content signature (internal/status hashes the RFC3339Nano timestamp,
// not a day bucket), and they differ in FeedURL/GUID/Link/FeedTitle, so their
// item keys stay distinct. Both facts are asserted, so a change to either key
// derivation fails here instead of silently making the tests tautological.
func reanchorStoryVariantsForTest(t *testing.T, count int) []rss.Entry {
	t.Helper()

	published := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	variants := make([]rss.Entry, 0, count)
	for i := 0; i < count; i++ {
		label := string(rune('A' + i))
		slug := strings.ToLower(label)
		variants = append(variants, rss.Entry{
			Title:       "Shared Reanchor Advisory",
			Link:        "https://feed-" + slug + ".example.test/articles/shared-reanchor-advisory",
			Description: "identical body text",
			GUID:        "feed-" + slug + "-shared-reanchor-advisory",
			FeedURL:     "https://feed-" + slug + ".example.test/feed.xml",
			FeedTitle:   "Feed " + label,
			Published:   published,
		})
	}

	signature := rss.GenerateEntryContentSignature(variants[0])
	itemKeys := make(map[string]struct{}, count)
	for _, entry := range variants {
		if got := rss.GenerateEntryContentSignature(entry); got != signature {
			t.Fatalf("variant %q content signature = %q, want the shared %q", entry.FeedTitle, got, signature)
		}
		itemKeys[rss.GenerateEntryKeyForEntry(entry)] = struct{}{}
	}
	if len(itemKeys) != count {
		t.Fatalf("story variants produced %d distinct item keys, want %d", len(itemKeys), count)
	}
	return variants
}

type reanchorPostRecorder struct {
	mu     sync.Mutex
	bodies []string
}

func (r *reanchorPostRecorder) record(body string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.bodies = append(r.bodies, body)
}

func (r *reanchorPostRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.bodies...)
}

func newReanchorTestServer(t *testing.T) (*httptest.Server, *reanchorPostRecorder) {
	t.Helper()

	recorder := &reanchorPostRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("ReadAll(body) error = %v", err)
		}
		recorder.record(string(body))
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	return server, recorder
}

func reanchorTargetsForTest(serverURL, destinationID string) []webhookTarget {
	return []webhookTarget{{
		url:           serverURL,
		destinationID: destinationID,
		messenger:     "slack",
	}}
}

func sendReanchorVariantForTest(t *testing.T, s *Scheduler, targets []webhookTarget, entry rss.Entry) {
	t.Helper()

	s.sendParsedRSSToWebhooks(t.Context(), &rss.FeedResults{
		Entries:    map[string][]rss.Entry{entry.FeedURL: {entry}},
		FeedErrors: map[string]string{},
	}, config.FeedTypeGeneral, targets, nil)
}

// recordReanchorParsedRowForTest records the parsed row the way checkRSSOnce
// does, so the load-time content-signature backfill has a row to match a sent
// marker to.
func recordReanchorParsedRowForTest(t *testing.T, tracker *status.Tracker, entry rss.Entry) {
	t.Helper()

	key := rss.GenerateEntryKeyForEntry(entry)
	stored := status.StoredRSSEntryFromRSS(entry, key)
	stored.FeedType = config.FeedTypeGeneral
	tracker.MarkRSSItemsParsed(entry.FeedURL, map[string]status.StoredRSSEntry{key: stored})
}

// evictReanchorAnchorForTest ages the anchor row out through the real retention
// path (the count cap plus CleanupOldEntriesForRSSDestinations), not by editing
// rss_status.json, and verifies the intended row is the one that went.
func evictReanchorAnchorForTest(
	t *testing.T,
	s *Scheduler,
	destinationID string,
	maxParsedItems int,
	evictedKey string,
) {
	t.Helper()

	if err := s.statusTracker.SavePendingChanges(); err != nil {
		t.Fatalf("SavePendingChanges(before retention) error = %v", err)
	}
	s.statusTracker.UpdateRetention(status.RetentionPolicy{MaxRSSParsedItems: maxParsedItems})
	s.statusTracker.CleanupOldEntriesForRSSDestinations([]string{destinationID})

	for _, item := range s.statusTracker.RSSParsedItemsSnapshot() {
		if item.Key == evictedKey {
			t.Fatalf("retention did not evict the delivered anchor row %q", evictedKey)
		}
	}
	if err := s.statusTracker.SavePendingChanges(); err != nil {
		t.Fatalf("SavePendingChanges(after retention) error = %v", err)
	}
}

func reloadReanchorTrackerForTest(t *testing.T, s *Scheduler) {
	t.Helper()

	reloaded, err := status.NewTracker(s.config.DataDir)
	if err != nil {
		t.Fatalf("NewTracker(reload) error = %v", err)
	}
	s.statusTracker = reloaded
}

func persistedRSSSentRowsForTest(t *testing.T, dataDir, itemKey string) []map[string]any {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(dataDir, "rss_status.json"))
	if err != nil {
		t.Fatalf("ReadFile(rss_status.json) error = %v", err)
	}
	var persisted struct {
		SentItems map[string]map[string]any `json:"sent_items"`
	}
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatalf("Unmarshal(rss_status.json) error = %v", err)
	}

	rows := make([]map[string]any, 0, 2)
	for _, row := range persisted.SentItems {
		if row["item_key"] == itemKey {
			rows = append(rows, row)
		}
	}
	return rows
}

// runSignatureReanchorChainForTest drives the shared body of the two chain
// regression tests. seedTitle/seedFeedTitle decide which generation of
// contamination is seeded: empty strings are the generation-1 shape written by
// a build of this rework whose filter branch still wrote the content signature,
// and a populated title/feed title is the generation-2 shape the load-time
// backfill leaves behind after one completed turn of the loop.
func runSignatureReanchorChainForTest(t *testing.T, seedTitle, seedFeedTitle string) {
	t.Helper()

	s := newTestScheduler(t)
	server, recorder := newReanchorTestServer(t)
	const destinationID = "slack.rss.general"
	targets := reanchorTargetsForTest(server.URL, destinationID)

	variants := reanchorStoryVariantsForTest(t, 3)
	entryA, entryB, entryC := variants[0], variants[1], variants[2]
	keyA := rss.GenerateEntryKeyForEntry(entryA)
	keyB := rss.GenerateEntryKeyForEntry(entryB)
	contentSig := rss.GenerateEntryContentSignature(entryA)

	// The contamination: an item-key marker and a content-signature marker that
	// never corresponded to a delivery, plus the parsed row they belong to.
	markRSSItemSentForDestination(s.statusTracker, keyA, seedTitle, seedFeedTitle, destinationID, server.URL)
	markRSSItemSentForDestination(s.statusTracker, contentSig, seedTitle, seedFeedTitle, destinationID, server.URL)
	recordReanchorParsedRowForTest(t, s.statusTracker, entryA)

	sendReanchorVariantForTest(t, s, targets, entryB)
	if got := len(recorder.snapshot()); got != 0 {
		t.Fatalf("POST count after the suppressed feed B = %d, want 0", got)
	}
	snapshot, ok := s.statusTracker.RSSSentItemSnapshot(keyB, destinationID, server.URL)
	if !ok {
		t.Fatal("the content-signature hit wrote no marker for the suppressed entry")
	}
	if !snapshot.Derived {
		t.Fatal("the marker written for the suppressed entry is not flagged derived, so the load-time backfill re-anchors it")
	}
	recordReanchorParsedRowForTest(t, s.statusTracker, entryB)

	evictReanchorAnchorForTest(t, s, destinationID, 1, keyA)
	reloadReanchorTrackerForTest(t, s)

	if s.statusTracker.IsRSSItemSentToDestination(contentSig, destinationID, server.URL) {
		t.Fatal("the status reload re-created the content signature from the marker of the suppressed entry")
	}

	sendReanchorVariantForTest(t, s, targets, entryC)
	bodies := recorder.snapshot()
	if len(bodies) != 1 {
		t.Fatalf("POST count after feed C = %d, want 1; bodies=%#v", len(bodies), bodies)
	}
	if !strings.Contains(bodies[0], entryC.Link) {
		t.Fatalf("delivered body does not reference feed C entry %q: %s", entryC.Link, bodies[0])
	}
}

// TestSendParsedRSSToWebhooksSignatureHitDoesNotReanchorLegacyMarker pins the
// generation-1 chain: a title-less, unflagged content-signature marker that was
// never a delivery suppresses feed B, the marker written for B must not become
// a new anchor at load time, and feed C is therefore delivered once the
// contaminated row has aged out.
func TestSendParsedRSSToWebhooksSignatureHitDoesNotReanchorLegacyMarker(t *testing.T) {
	runSignatureReanchorChainForTest(t, "", "")
}

// TestSendParsedRSSToWebhooksSignatureHitTerminatesReanchoredChain pins the
// generation-2 chain: after one completed turn the backfill has patched a title
// and a feed title onto the contamination marker, so it is indistinguishable on
// disk from a genuine delivery marker. Flagging the marker written for the
// suppressed entry terminates the loop regardless, which is why no title or
// wire-shape heuristic may be reintroduced here.
func TestSendParsedRSSToWebhooksSignatureHitTerminatesReanchoredChain(t *testing.T) {
	runSignatureReanchorChainForTest(t, "Shared Reanchor Advisory", "Feed A")
}

// TestSendParsedRSSToWebhooksGenuineSendPersistsUnflagged is the byte-identity
// guard for real deliveries: the item-key and content-signature rows a genuine
// send writes must carry neither "derived" nor "skipped" on disk, on the stable
// destination row and on the legacy webhook-URL alias alike, while both rows of
// each suppressed entry carry "derived": true together with that entry's own
// title and feed title.
func TestSendParsedRSSToWebhooksGenuineSendPersistsUnflagged(t *testing.T) {
	s := newTestScheduler(t)
	server, recorder := newReanchorTestServer(t)
	const destinationID = "slack.rss.general"
	targets := reanchorTargetsForTest(server.URL, destinationID)

	variants := reanchorStoryVariantsForTest(t, 3)
	entryA, entryB, entryC := variants[0], variants[1], variants[2]
	contentSig := rss.GenerateEntryContentSignature(entryA)

	sendReanchorVariantForTest(t, s, targets, entryA)
	sendReanchorVariantForTest(t, s, targets, entryB)
	sendReanchorVariantForTest(t, s, targets, entryC)

	if got := len(recorder.snapshot()); got != 1 {
		t.Fatalf("POST count = %d, want 1 delivery plus two content-signature suppressions", got)
	}
	if err := s.statusTracker.SavePendingChanges(); err != nil {
		t.Fatalf("SavePendingChanges() error = %v", err)
	}

	assertGenuineRows := func(label, itemKey string) {
		t.Helper()
		rows := persistedRSSSentRowsForTest(t, s.config.DataDir, itemKey)
		if len(rows) != 2 {
			t.Fatalf("%s persisted rows = %d, want the stable destination row and the legacy webhook-URL alias", label, len(rows))
		}
		for _, row := range rows {
			if _, flagged := row["derived"]; flagged {
				t.Fatalf("%s row carries a derived key, genuine sends must persist byte-identically: %#v", label, row)
			}
			if _, flagged := row["skipped"]; flagged {
				t.Fatalf("%s row carries a skipped key, genuine sends must persist byte-identically: %#v", label, row)
			}
			if row["title"] != entryA.Title || row["feed_title"] != entryA.FeedTitle {
				t.Fatalf("%s row lost the delivered entry's titles: %#v", label, row)
			}
		}
	}
	assertGenuineRows("genuine item key", rss.GenerateEntryKeyForEntry(entryA))
	assertGenuineRows("genuine content signature", contentSig)

	assertDerivedRows := func(entry rss.Entry) {
		t.Helper()
		itemKey := rss.GenerateEntryKeyForEntry(entry)
		rows := persistedRSSSentRowsForTest(t, s.config.DataDir, itemKey)
		if len(rows) != 2 {
			t.Fatalf("suppressed %s rows = %d, want the stable destination row and the legacy webhook-URL alias", entry.FeedTitle, len(rows))
		}
		for _, row := range rows {
			if row["derived"] != true {
				t.Fatalf("suppressed %s row is not flagged derived: %#v", entry.FeedTitle, row)
			}
			if _, flagged := row["skipped"]; flagged {
				t.Fatalf("suppressed %s row reuses the filter-mismatch skipped flag: %#v", entry.FeedTitle, row)
			}
			// The suppressed entry keeps its OWN titles, not the delivered
			// entry's: the article titles coincide because the fixture shares
			// them, the feed titles do not.
			if row["title"] != entry.Title || row["feed_title"] != entry.FeedTitle {
				t.Fatalf("suppressed %s row lost its own titles: %#v", entry.FeedTitle, row)
			}
		}
	}
	assertDerivedRows(entryB)
	assertDerivedRows(entryC)
}

// TestSendParsedRSSToWebhooksSignatureAnchorIsNotRenewedByASuppression is the
// ACCEPTED-COST test, deliberately not a regression test.
//
// It records the price the operator agreed to pay for the fix. Because the
// marker written for a suppressed entry is no longer a dedup anchor, the only
// anchor a story has is the marker of the entry that was actually delivered,
// and that marker lives exactly as long as that entry's parsed row. Once the
// delivered entry leaves retention -- after status_retention.rss_parsed_max_age
// (8760h in the shipped example config) or through max_rss_parsed_items
// eviction -- the next copy of the same story is delivered one more time.
//
// The expected POST count here is therefore 2, and that 2 is the accepted cost,
// not a defect. Changing it back to 1 means letting a suppression renew the
// anchor again, which is exactly the self-sustaining re-anchoring loop the two
// chain tests above pin as fixed.
func TestSendParsedRSSToWebhooksSignatureAnchorIsNotRenewedByASuppression(t *testing.T) {
	s := newTestScheduler(t)
	server, recorder := newReanchorTestServer(t)
	const destinationID = "slack.rss.general"
	targets := reanchorTargetsForTest(server.URL, destinationID)

	variants := reanchorStoryVariantsForTest(t, 5)
	entryA, entryB, entryC, entryD, entryE := variants[0], variants[1], variants[2], variants[3], variants[4]
	keyA := rss.GenerateEntryKeyForEntry(entryA)

	sendReanchorVariantForTest(t, s, targets, entryA)
	recordReanchorParsedRowForTest(t, s.statusTracker, entryA)
	if got := len(recorder.snapshot()); got != 1 {
		t.Fatalf("POST count after the delivered feed A = %d, want 1", got)
	}

	sendReanchorVariantForTest(t, s, targets, entryB)
	recordReanchorParsedRowForTest(t, s.statusTracker, entryB)
	sendReanchorVariantForTest(t, s, targets, entryC)
	recordReanchorParsedRowForTest(t, s.statusTracker, entryC)
	if got := len(recorder.snapshot()); got != 1 {
		t.Fatalf("POST count while the delivered entry's parsed row still lives = %d, want 1", got)
	}

	evictReanchorAnchorForTest(t, s, destinationID, 2, keyA)
	reloadReanchorTrackerForTest(t, s)

	sendReanchorVariantForTest(t, s, targets, entryD)
	bodies := recorder.snapshot()
	if len(bodies) != 2 {
		t.Fatalf("POST count after the delivered entry left retention = %d, want 2 (the accepted repost); bodies=%#v", len(bodies), bodies)
	}
	if !strings.Contains(bodies[1], entryD.Link) {
		t.Fatalf("the accepted repost does not reference feed D entry %q: %s", entryD.Link, bodies[1])
	}

	// The cost is bounded: ONE repost per eviction, not one per cycle. Feed D
	// was a genuine delivery, so it wrote the story's content signature as a
	// fresh, unflagged anchor, and every further variant is suppressed again --
	// across a restart too, because that anchor is a real delivery marker and
	// the load-time backfill may rebuild it.
	recordReanchorParsedRowForTest(t, s.statusTracker, entryD)
	sendReanchorVariantForTest(t, s, targets, entryE)
	if got := len(recorder.snapshot()); got != 2 {
		t.Fatalf("POST count after feed E = %d, want 2; the delivery of feed D must re-anchor the story, so the repost is once per eviction, not once per cycle", got)
	}
	reloadReanchorTrackerForTest(t, s)
	if !s.statusTracker.IsRSSItemSentToDestination(
		rss.GenerateEntryContentSignature(entryD), destinationID, server.URL) {
		t.Fatal("the content signature of the re-delivered story did not survive a restart, so the story would be posted again every cycle")
	}
	sendReanchorVariantForTest(t, s, targets, entryE)
	if got := len(recorder.snapshot()); got != 2 {
		t.Fatalf("POST count after a restart and feed E = %d, want 2", got)
	}
}

// TestSendParsedRSSToWebhooksSignatureHitAuditUnchanged pins the decision that
// the audit vocabulary does not change: a content-signature suppression stays
// one deduplicated/already_sent event whose details are exactly
// {"dedupe_key_type":"content_signature","mode":"fresh"} ("mode" is added to
// every RSS delivery audit event by auditDetailsWithMode). "derived" would
// appear on every such line and is already implied by dedupe_key_type, so it
// must not be added as a details key.
func TestSendParsedRSSToWebhooksSignatureHitAuditUnchanged(t *testing.T) {
	tests := []struct {
		name string
		seed func(t *testing.T, s *Scheduler, targets []webhookTarget, entryA rss.Entry, serverURL, destinationID string)
	}{
		{
			name: "genuine anchor",
			seed: func(t *testing.T, s *Scheduler, targets []webhookTarget, entryA rss.Entry, _, _ string) {
				t.Helper()
				sendReanchorVariantForTest(t, s, targets, entryA)
			},
		},
		{
			name: "legacy contamination marker",
			seed: func(t *testing.T, s *Scheduler, _ []webhookTarget, entryA rss.Entry, serverURL, destinationID string) {
				t.Helper()
				contentSig := rss.GenerateEntryContentSignature(entryA)
				markRSSItemSentForDestination(s.statusTracker, contentSig, "", "", destinationID, serverURL)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestScheduler(t)
			server, _ := newReanchorTestServer(t)
			const destinationID = "slack.rss.general"
			targets := reanchorTargetsForTest(server.URL, destinationID)

			variants := reanchorStoryVariantsForTest(t, 2)
			entryA, entryB := variants[0], variants[1]
			keyB := rss.GenerateEntryKeyForEntry(entryB)

			tt.seed(t, s, targets, entryA, server.URL, destinationID)
			sendReanchorVariantForTest(t, s, targets, entryB)

			wantDetails := map[string]string{"dedupe_key_type": "content_signature", "mode": "fresh"}
			matched := 0
			for _, event := range deliveryAuditEventsForTest(t, s.config.DataDir) {
				if event.ItemKey != keyB {
					continue
				}
				if event.Outcome != status.DeliveryAuditOutcomeDeduplicated ||
					event.Reason != status.DeliveryAuditReasonAlreadySent {
					t.Fatalf("suppressed entry audit event = %s/%s, want %s/%s",
						event.Outcome, event.Reason,
						status.DeliveryAuditOutcomeDeduplicated, status.DeliveryAuditReasonAlreadySent)
				}
				if !reflect.DeepEqual(event.Details, wantDetails) {
					t.Fatalf("audit details = %#v, want exactly %#v", event.Details, wantDetails)
				}
				matched++
			}
			if matched != 1 {
				t.Fatalf("deduplicated audit events for the suppressed entry = %d, want 1", matched)
			}
		})
	}
}

// TestRSSEntrySortTimePushesUndatedToEnd pins rssEntrySortTime's ordering
// rule directly: a zero Published sorts as "now" rather than as the zero
// value's year-1 instant, so undated entries fall to the end of a batch
// instead of consuming the front of max_rss_entries_per_cycle.
func TestRSSEntrySortTimePushesUndatedToEnd(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	entries := []rss.Entry{
		{Title: "undated-1"},
		{Title: "dated-2024", Published: time.Date(2024, 9, 6, 12, 0, 0, 0, time.UTC)},
		{Title: "undated-2"},
		{Title: "dated-2026", Published: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)},
	}
	sort.Slice(entries, func(i, j int) bool {
		return rssEntrySortTime(entries[i], now).Before(rssEntrySortTime(entries[j], now))
	})

	got := []string{entries[0].Title, entries[1].Title, entries[2].Title, entries[3].Title}
	want := []string{"dated-2024", "dated-2026", "undated-1", "undated-2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

// TestSendParsedRSSPrefersDatedEntriesUnderCycleCap is the call-site
// regression guard for the fresh-batch ordering:
// TestRSSEntrySortTimePushesUndatedToEnd alone
// does not exercise sendParsedRSSToWebhooksWithConfig's actual sort.Slice
// call, so a revert of that call site to the raw
// allEntries[i].Published.Before(allEntries[j].Published) comparator passes
// the helper test above while production stays fully broken. This test
// drives the real per-cycle path (filterAndRecordNewRSSItems then
// sendParsedRSSToWebhooksWithConfig) so a reverted call site is caught.
func TestSendParsedRSSPrefersDatedEntriesUnderCycleCap(t *testing.T) {
	s := newTestScheduler(t)
	sender := &recordingWebhookSender{}
	s.discordWebhookSender = sender
	s.config.RSSMaxEntriesPerCycle = 2

	feed := "https://feeds.test/cap.xml"
	// NOTE: the dates must sit INSIDE the effective send horizon. A literal
	// old date (e.g. 2024) would be dropped as stale, because
	// rss_max_item_age = 0 clamps to status_retention.rss_parsed_max_age
	// (365d) via effectiveRSSSendMaxAge.
	entries := []rss.Entry{
		{FeedURL: feed, GUID: "u1", Title: "undated-1"},
		{FeedURL: feed, GUID: "d1", Title: "dated-old", Published: time.Now().UTC().Add(-30 * 24 * time.Hour)},
		{FeedURL: feed, GUID: "u2", Title: "undated-2"},
		{FeedURL: feed, GUID: "d2", Title: "dated-new", Published: time.Now().UTC().Add(-time.Hour)},
	}
	res := &rss.FeedResults{Entries: map[string][]rss.Entry{feed: entries}, FeedErrors: map[string]string{}}
	s.filterAndRecordNewRSSItems(res, config.FeedTypeGeneral)
	s.sendParsedRSSToWebhooksWithConfig(t.Context(), s.config, res, config.FeedTypeGeneral,
		[]webhookTarget{{url: "https://hooks.example.test/cap", destinationID: "discord.rss.general", messenger: "discord"}}, nil)

	var got []string
	for _, c := range sender.rssCallsSnapshot() {
		got = append(got, c.entryTitle)
	}
	if want := []string{"dated-old", "dated-new"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("delivered under cap=2 = %v, want %v (undated entries must not consume the cap)", got, want)
	}
}

// TestSendRSSEntryToTargetGUIDItemKeyUnaffectedByNewlyParsedDate is a
// regression guard against a future change to GenerateEntryKeyForEntry
// accidentally making GUID-based keys date-sensitive. BSI and CISA (the two
// feeds the 2026-09-04 layout fix targets) carry a GUID on 100% of their
// items, so their item key is guid-hash-based both before and after that
// fix, byte-identical, and sendRSSEntryToTarget checks the item key first --
// an item first delivered while undated must not be delivered again once its
// pubDate becomes parseable.
func TestSendRSSEntryToTargetGUIDItemKeyUnaffectedByNewlyParsedDate(t *testing.T) {
	s := newTestScheduler(t)

	var mu sync.Mutex
	postCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		postCount++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	feedURL := "https://feeds.test/guid-migration.xml"
	target := webhookTarget{
		url:           server.URL,
		destinationID: "slack.rss.general",
		messenger:     "slack",
	}
	s.config.SlackWebhooks.RSS = config.WebhookConfig{Enabled: true, URL: server.URL}

	undated := rss.Entry{
		Title:     "GUID Migration Article",
		Link:      "https://example.test/guid-migration",
		GUID:      "guid-migration-target",
		FeedURL:   feedURL,
		FeedTitle: "Migration Feed",
	}
	keyBefore := rss.GenerateEntryKeyForEntry(undated)
	outcome := s.sendRSSEntryToTarget(
		t.Context(), s.config, config.FeedTypeGeneral, target,
		freshRSSDeliveryItem(undated), rssDeliveryModeFresh, 0, 1, nil,
	)
	if outcome != rssDeliveryOutcomeSent {
		t.Fatalf("first send outcome = %v, want sent", outcome)
	}

	dated := undated
	dated.Published = time.Now().UTC().Add(-time.Hour)
	keyAfter := rss.GenerateEntryKeyForEntry(dated)
	if keyAfter != keyBefore {
		t.Fatalf("item key changed after the date became parseable: before=%q after=%q", keyBefore, keyAfter)
	}

	outcome = s.sendRSSEntryToTarget(
		t.Context(), s.config, config.FeedTypeGeneral, target,
		freshRSSDeliveryItem(dated), rssDeliveryModeFresh, 0, 1, nil,
	)
	if outcome != rssDeliveryOutcomeAlreadySent {
		t.Fatalf("second send outcome = %v, want alreadySent (GUID key must not resend)", outcome)
	}

	mu.Lock()
	posts := postCount
	mu.Unlock()
	if posts != 1 {
		t.Fatalf("POSTs to the local webhook = %d, want 1 (item must not be resent once dated)", posts)
	}
}
