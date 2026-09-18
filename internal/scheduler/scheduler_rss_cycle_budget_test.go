package scheduler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/config"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/rss"

	logtest "github.com/sirupsen/logrus/hooks/test"
)

// newBudgetTestFastRSSFeedServer answers immediately with one item and an
// ETag, so a cycle-budget test can assert the feed was applied normally.
func newBudgetTestFastRSSFeedServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		w.Header().Set("ETag", `"probe-etag"`)
		_, _ = w.Write([]byte(schedulerRSSFeedXMLForTest("Fast item", "fast-guid", "https://example.test/fast")))
	}))
	t.Cleanup(server.Close)
	return server
}

// newBudgetTestSlowRSSFeedServer blocks until the request's context is done,
// so it never answers within a short cycle budget.
func newBudgetTestSlowRSSFeedServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)
	return server
}

// newBudgetTest500RSSFeedServer answers immediately with HTTP 500, so a feed
// that genuinely failed inside an overrunning cycle can be told apart from one
// that simply did not answer in time.
func newBudgetTest500RSSFeedServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	return server
}

// newRSSCycleBudgetTestScheduler builds a scheduler with a real rss.Parser
// (batch budget far above the cycle budget under test) and a Slack RSS target
// enabled so processRSSFeedBatches builds a real batch; no HTTP is ever sent
// to the target in these tests, since the cycle context is already dead by
// the time delivery would run.
func newRSSCycleBudgetTestScheduler(t *testing.T, cycleBudget time.Duration, feedURLs ...string) *Scheduler {
	t.Helper()
	s := newTestScheduler(t)
	parser := rss.NewParser(0, time.Millisecond, 30*time.Second)
	s.rssParser = parser
	allowLocalRSSFeedsForTest(t, parser, http.DefaultClient)

	s.config.Feeds.GeneralFeeds = feedURLs
	s.config.SlackWebhooks.RSS = config.WebhookConfig{Enabled: true, URL: "https://hooks.slack.com/services/T/B/rss"}
	s.config.MaxRSSWorkers = 2
	s.config.RSSCheckTimeout = cycleBudget
	s.feedTypeMap = buildFeedTypeMap(s.config)

	return s
}

func TestProcessRSSFeedBatchesAppliesPartialResultsWhenCycleBudgetExpires(t *testing.T) {
	fastServer := newBudgetTestFastRSSFeedServer(t)
	slowServer := newBudgetTestSlowRSSFeedServer(t)
	failServer := newBudgetTest500RSSFeedServer(t)
	fastURL := fastServer.URL + "/feed.xml"
	slowURL := slowServer.URL + "/feed.xml"
	failURL := failServer.URL + "/feed.xml"

	s := newRSSCycleBudgetTestScheduler(t, 150*time.Millisecond, fastURL, slowURL, failURL)

	pollFields := newPollLogFields(pollTypeRSS)
	batches, feedURLs := s.rssFeedBatchesForConfig(s.config, pollFields)

	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	s.processRSSFeedBatches(ctx, s.config, pollFields, batches, feedURLs, rssDeliveryAttemptSet{})

	fastInfo, exists := s.statusTracker.GetRSSFeedInfo(fastURL)
	if !exists {
		t.Fatal("fast feed has no recorded status")
	}
	if fastInfo.LastSuccess == nil {
		t.Fatal("fast feed LastSuccess = nil, want set")
	}
	if fastInfo.LastError != nil {
		t.Fatalf("fast feed LastError = %v, want nil", *fastInfo.LastError)
	}
	if fastInfo.ConsecutiveFailures != 0 {
		t.Fatalf("fast feed ConsecutiveFailures = %d, want 0", fastInfo.ConsecutiveFailures)
	}
	if fastInfo.EntriesFound != 1 {
		t.Fatalf("fast feed EntriesFound = %d, want 1", fastInfo.EntriesFound)
	}
	if fastInfo.ETag != `"probe-etag"` {
		t.Fatalf("fast feed ETag = %q, want \"probe-etag\"", fastInfo.ETag)
	}
	if fastInfo.NextAttemptAfter != nil {
		t.Fatalf("fast feed NextAttemptAfter = %v, want nil", *fastInfo.NextAttemptAfter)
	}
	if got := len(s.statusTracker.GetRSSParsedItemKeys(fastURL)); got != 1 {
		t.Fatalf("fast feed parsed item keys = %d, want 1", got)
	}

	if _, exists := s.statusTracker.GetRSSFeedInfo(slowURL); exists {
		t.Fatal("slow feed has a recorded status, want not polled (absent)")
	}

	failInfo, exists := s.statusTracker.GetRSSFeedInfo(failURL)
	if !exists {
		t.Fatal("feed that answered with HTTP 500 has no recorded status")
	}
	if failInfo.LastError == nil || *failInfo.LastError != "RSS feed returned HTTP 500" {
		t.Fatalf("failed feed LastError = %v, want \"RSS feed returned HTTP 500\"", failInfo.LastError)
	}
	if failInfo.ConsecutiveFailures != 1 {
		t.Fatalf("failed feed ConsecutiveFailures = %d, want 1", failInfo.ConsecutiveFailures)
	}
	if failInfo.EntriesFound != 0 {
		t.Fatalf("failed feed EntriesFound = %d, want 0", failInfo.EntriesFound)
	}
	if failInfo.LastSuccess != nil {
		t.Fatalf("failed feed LastSuccess = %v, want nil", *failInfo.LastSuccess)
	}
}

func TestProcessRSSFeedBatchesDoesNotParkFeedsAfterRepeatedCycleBudgetOverruns(t *testing.T) {
	fastServer := newBudgetTestFastRSSFeedServer(t)
	slowServer := newBudgetTestSlowRSSFeedServer(t)
	fastURL := fastServer.URL + "/feed.xml"
	slowURL := slowServer.URL + "/feed.xml"

	s := newRSSCycleBudgetTestScheduler(t, 150*time.Millisecond, fastURL, slowURL)
	pollFields := newPollLogFields(pollTypeRSS)

	for cycle := 0; cycle < 3; cycle++ {
		batches, feedURLs := s.rssFeedBatchesForConfig(s.config, pollFields)
		ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
		s.processRSSFeedBatches(ctx, s.config, pollFields, batches, feedURLs, rssDeliveryAttemptSet{})
		cancel()

		pollable := s.filterRSSFeedURLsForCooldown([]string{fastURL, slowURL}, pollFields)
		if got := len(pollable); got != 2 {
			t.Fatalf("cycle %d: pollable feeds = %d, want 2", cycle+1, got)
		}
	}

	fastInfo, exists := s.statusTracker.GetRSSFeedInfo(fastURL)
	if !exists {
		t.Fatal("fast feed has no recorded status after three cycles")
	}
	if fastInfo.ConsecutiveFailures != 0 {
		t.Fatalf("fast feed ConsecutiveFailures = %d, want 0 after three cycle-budget overruns", fastInfo.ConsecutiveFailures)
	}
}

func TestProcessRSSFeedBatchesWarnsOnceWhenCycleBudgetExhausted(t *testing.T) {
	fastServer := newBudgetTestFastRSSFeedServer(t)
	slowServer := newBudgetTestSlowRSSFeedServer(t)
	fastURL := fastServer.URL + "/feed.xml"
	slowURL := slowServer.URL + "/feed.xml"

	s := newRSSCycleBudgetTestScheduler(t, 150*time.Millisecond, fastURL, slowURL)
	pollFields := newPollLogFields(pollTypeRSS)
	batches, feedURLs := s.rssFeedBatchesForConfig(s.config, pollFields)

	hook := logtest.NewGlobal()
	defer hook.Reset()

	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	s.processRSSFeedBatches(ctx, s.config, pollFields, batches, feedURLs, rssDeliveryAttemptSet{})

	var warnEntries int
	var warn *warnEntryFields
	for _, entry := range hook.AllEntries() {
		if entry.Message != "RSS cycle budget exhausted, remaining feeds were not polled" {
			continue
		}
		warnEntries++
		warn = &warnEntryFields{
			feedsTotal:     entry.Data["feeds_total"],
			feedsCompleted: entry.Data["feeds_completed"],
			feedsNotPolled: entry.Data["feeds_not_polled"],
			budget:         entry.Data["budget"],
		}
	}
	if warnEntries != 1 {
		t.Fatalf("WARN entries = %d, want exactly 1", warnEntries)
	}
	if warn.feedsTotal != 2 {
		t.Fatalf("feeds_total = %v, want 2", warn.feedsTotal)
	}
	if warn.feedsCompleted != 1 {
		t.Fatalf("feeds_completed = %v, want 1", warn.feedsCompleted)
	}
	if warn.feedsNotPolled != 1 {
		t.Fatalf("feeds_not_polled = %v, want 1", warn.feedsNotPolled)
	}
	if warn.budget != "150ms" {
		t.Fatalf("budget = %v, want 150ms", warn.budget)
	}
	if findTestLogEntry(hook, "Failed to parse RSS feed batch") != nil {
		t.Fatal("ERROR \"Failed to parse RSS feed batch\" was logged, want it absent on a cycle-budget expiry")
	}
}

type warnEntryFields struct {
	feedsTotal, feedsCompleted, feedsNotPolled, budget any
}

func TestProcessRSSFeedBatchesLeavesFeedStatusUntouchedOnShutdownCancel(t *testing.T) {
	fastServer := newBudgetTestFastRSSFeedServer(t)
	slowServer := newBudgetTestSlowRSSFeedServer(t)
	fastURL := fastServer.URL + "/feed.xml"
	slowURL := slowServer.URL + "/feed.xml"

	// A generous cycle budget: the shutdown cancel below must be what stops
	// this cycle, not the budget expiring.
	s := newRSSCycleBudgetTestScheduler(t, time.Minute, fastURL, slowURL)
	pollFields := newPollLogFields(pollTypeRSS)
	batches, feedURLs := s.rssFeedBatchesForConfig(s.config, pollFields)

	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		time.Sleep(80 * time.Millisecond)
		cancel()
	}()
	defer cancel()

	s.processRSSFeedBatches(ctx, s.config, pollFields, batches, feedURLs, rssDeliveryAttemptSet{})

	if _, err := os.ReadFile(filepath.Join(s.config.DataDir, "rss_status.json")); !os.IsNotExist(err) {
		t.Fatalf("rss_status.json read error = %v, want file to remain absent", err)
	}
	if got := len(s.statusTracker.GetRSSParsedItemKeys(fastURL)); got != 0 {
		t.Fatalf("GetRSSParsedItemKeys(fast feed) = %d, want 0", got)
	}
}

// TestCheckRSSOnceReportsBudgetOverrunOnEveryCycle is the F1 regression pin:
// before this fix, checkOutcome carried no way to tell a genuine
// cycle-budget overrun apart from a completed pass that found nothing, so
// --healthcheck's empty-data_dir gate (main.go's evaluateRSSHealth) could only
// wait out one further budget and then reported the wrong diagnosis forever
// once a legal config (rss_check_timeout at its floor, slower feeds) made
// every single cycle overrun. Two consecutive full checkRSSOnce cycles must
// both report rssBudgetOverrun == true -- the information must be visible on
// every cycle, not just the first.
func TestCheckRSSOnceReportsBudgetOverrunOnEveryCycle(t *testing.T) {
	slowServer := newBudgetTestSlowRSSFeedServer(t)
	slowURL := slowServer.URL + "/feed.xml"

	s := newRSSCycleBudgetTestScheduler(t, 150*time.Millisecond, slowURL)
	s.config.APIKey = ""

	for cycle := 1; cycle <= 2; cycle++ {
		outcome := s.checkRSSOnce(t.Context())
		if !outcome.ran {
			t.Fatalf("cycle %d: ran = false, want true", cycle)
		}
		if !outcome.ok {
			t.Fatalf("cycle %d: ok = false, want true (a budget overrun is exempted)", cycle)
		}
		if !outcome.rssBudgetOverrun {
			t.Fatalf("cycle %d: rssBudgetOverrun = false, want true", cycle)
		}
		if _, ok := s.statusTracker.GetRSSFeedInfo(slowURL); ok {
			t.Fatalf("cycle %d: the feed IS recorded, so the empty-known state does not recur", cycle)
		}
	}
}

// TestCheckRSSOnceReportsNoBudgetOverrunOnACleanCycle is the guard for the
// other branch: a pass that completes within its budget must never be
// misreported as an overrun, or evaluateRSSHealth's distinct budget-overrun
// message would fire on a genuinely unwritable data_dir instead of the
// correct one.
func TestCheckRSSOnceReportsNoBudgetOverrunOnACleanCycle(t *testing.T) {
	fastServer := newBudgetTestFastRSSFeedServer(t)
	fastURL := fastServer.URL + "/feed.xml"

	s := newRSSCycleBudgetTestScheduler(t, time.Minute, fastURL)
	s.config.APIKey = ""

	outcome := s.checkRSSOnce(t.Context())
	if !outcome.ran || !outcome.ok {
		t.Fatalf("outcome = %+v, want ran=true ok=true", outcome)
	}
	if outcome.rssBudgetOverrun {
		t.Fatal("rssBudgetOverrun = true on a clean cycle, want false")
	}
}
