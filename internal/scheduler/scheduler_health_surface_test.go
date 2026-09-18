package scheduler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/api"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/config"
)

// fakeProgressRecorder is a test double for ProgressRecorder that counts calls
// instead of writing files, so Part (a) tests can assert exactly when the
// scheduler records a completed pass without touching the filesystem.
type fakeProgressRecorder struct {
	mu       sync.Mutex
	apiCalls int
	rssCalls int
	cleared  bool
}

func (f *fakeProgressRecorder) RecordAPIPass(time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.apiCalls++
}

func (f *fakeProgressRecorder) RecordRSSPass(time.Duration, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rssCalls++
}

func (f *fakeProgressRecorder) Clear() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cleared = true
}

func (f *fakeProgressRecorder) snapshot() (apiCalls, rssCalls int, cleared bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.apiCalls, f.rssCalls, f.cleared
}

// --- Part (a): checkOutcome.ran is the wedge signal ---

// TestCheckAPIOnceReportsNotRanWhenAPassIsAlreadyRunning is the pin for the
// defect v1 shipped: under v1's shape a wedged poller holding its own mutex
// still refreshed the readiness marker on exactly this path. checkOutcome.ran
// must be false here, or a poller hung on apiMu would report healthy forever.
func TestCheckAPIOnceReportsNotRanWhenAPassIsAlreadyRunning(t *testing.T) {
	s := newTestScheduler(t)
	s.apiMu.Lock()
	defer s.apiMu.Unlock()

	outcome := s.checkAPIOnce(t.Context())
	if outcome.ran {
		t.Fatal("checkAPIOnce().ran = true while apiMu was already held, want false")
	}
	if !outcome.ok {
		t.Fatal("checkAPIOnce().ok = false on a TryLock skip, want true (a skip is not a failed cycle)")
	}
}

// TestCheckRSSOnceReportsNotRanWhenAPassIsAlreadyRunning mirrors the API case
// for the RSS poller's own mutex.
func TestCheckRSSOnceReportsNotRanWhenAPassIsAlreadyRunning(t *testing.T) {
	s := newTestScheduler(t)
	s.rssMu.Lock()
	defer s.rssMu.Unlock()

	outcome := s.checkRSSOnce(t.Context())
	if outcome.ran {
		t.Fatal("checkRSSOnce().ran = true while rssMu was already held, want false")
	}
	if !outcome.ok {
		t.Fatal("checkRSSOnce().ok = false on a TryLock skip, want true (a skip is not a failed cycle)")
	}
}

// TestSchedulerRecordsProgressOnlyForCompletedPasses drives a real Start()
// with a poller mutex held externally (simulating a wedge that started before
// this process even began recording) and asserts the wedged side never
// records while the healthy side keeps recording on every tick -- the
// one-sided wedge v1 could not detect at all, since it recorded around the
// call rather than on a completed pass.
func TestSchedulerRecordsProgressOnlyForCompletedPasses(t *testing.T) {
	s := newTestScheduler(t)
	s.config.APIPollInterval = 30 * time.Millisecond
	s.config.RSSPollInterval = 30 * time.Millisecond
	s.config.Feeds.GeneralFeeds = nil
	s.config.Feeds.GovernmentFeeds = nil
	s.config.Feeds.RansomwareFeeds = nil
	s.feedTypeMap = buildFeedTypeMap(s.config)

	recorder := &fakeProgressRecorder{}
	s.SetProgressRecorder(recorder)

	// Simulate a poller wedged holding its own mutex before Start() even runs.
	s.apiMu.Lock()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_, rssCalls, _ := recorder.snapshot()
		if rssCalls >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	apiCalls, rssCalls, _ := recorder.snapshot()
	if apiCalls != 0 {
		t.Fatalf("apiCalls = %d, want 0 while apiMu is held (the wedged side must never record)", apiCalls)
	}
	if rssCalls < 2 {
		t.Fatalf("rssCalls = %d, want >= 2 (the healthy side must keep recording)", rssCalls)
	}

	cancel()
	s.apiMu.Unlock()
}

// TestRecordAPIPassAndRecordRSSPassCallTheRecorderWhenNotDryRun is a direct
// unit test of the two recorder wrappers: TestSchedulerRecordsProgressOnlyForCompletedPasses
// above deliberately keeps apiMu wedged for its whole run, so it never
// exercises the actual ProgressRecorder.RecordAPIPass call on a healthy pass.
func TestRecordAPIPassAndRecordRSSPassCallTheRecorderWhenNotDryRun(t *testing.T) {
	s := newTestScheduler(t)
	recorder := &fakeProgressRecorder{}
	s.SetProgressRecorder(recorder)

	s.recordAPIPass(s.config)
	s.recordRSSPass(s.config, false)

	apiCalls, rssCalls, _ := recorder.snapshot()
	if apiCalls != 1 {
		t.Fatalf("apiCalls = %d, want 1", apiCalls)
	}
	if rssCalls != 1 {
		t.Fatalf("rssCalls = %d, want 1", rssCalls)
	}
}

// TestSchedulerStopClearsProgressMarkers pins that Stop() calls Clear() on the
// registered recorder, which is what removes both marker files after
// waitForWorkers returns -- the point at which no poller goroutine can
// recreate one, so no shutdown race is possible.
func TestSchedulerStopClearsProgressMarkers(t *testing.T) {
	s := newTestScheduler(t)
	recorder := &fakeProgressRecorder{}
	s.SetProgressRecorder(recorder)

	if err := s.Stop(); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	_, _, cleared := recorder.snapshot()
	if !cleared {
		t.Fatal("Stop() did not call ProgressRecorder.Clear()")
	}
}

// TestSchedulerStopDoesNotClearProgressMarkersOnAForcedShutdown is the F4
// regression pin: waitForWorkers returning false after schedulerShutdownTimeout
// means a poller goroutine is still running and could still complete a pass
// and write a marker after Clear() removed it, recreating the exact file
// Clear() is meant to remove -- the same forced-shutdown race already fixed
// once in this repo (closeSchedulerResources' TryLock pattern). Clear() must
// therefore be skipped on this path. Mirrors the shape of internal/rss's own
// TestShutdownWorkersAfterTimeoutWarnsWhenWorkersHang: a real wait for the
// production timeout, skipped in short mode.
func TestSchedulerStopDoesNotClearProgressMarkersOnAForcedShutdown(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping ~32s forced-shutdown wait in short mode")
	}

	s := newTestScheduler(t)
	recorder := &fakeProgressRecorder{}
	s.SetProgressRecorder(recorder)
	s.started = true
	s.wg.Add(1) // simulate a poller goroutine that never returns
	defer s.wg.Done()

	if err := s.Stop(); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	_, _, cleared := recorder.snapshot()
	if cleared {
		t.Fatal("Stop() called Clear() after a forced (non-graceful) shutdown, want it skipped")
	}
}

// TestProgressStaleAfterFormulas pins the allowance formulas directly,
// independent of any plumbing: three cycles of (poll interval + pass cost),
// with the API side counting its check timeout twice (fetch, then delivery
// plus the retry queue) and the RSS side once (the whole cycle context).
func TestProgressStaleAfterFormulas(t *testing.T) {
	tests := []struct {
		name    string
		cfg     *config.Config
		wantAPI time.Duration
		wantRSS time.Duration
	}{
		{
			name: "shipped defaults",
			cfg: &config.Config{
				APIPollInterval: time.Hour,
				APICheckTimeout: 5 * time.Minute,
				RSSPollInterval: 30 * time.Minute,
				RSSCheckTimeout: 10 * time.Minute,
			},
			wantAPI: 3*time.Hour + 30*time.Minute,
			wantRSS: 2 * time.Hour,
		},
		{
			name: "one minute intervals with default check timeouts",
			cfg: &config.Config{
				APIPollInterval: time.Minute,
				APICheckTimeout: 5 * time.Minute,
				RSSPollInterval: time.Minute,
				RSSCheckTimeout: 10 * time.Minute,
			},
			wantAPI: 33 * time.Minute,
			wantRSS: 33 * time.Minute,
		},
		{
			name: "everything at its configured floor",
			cfg: &config.Config{
				APIPollInterval: time.Minute,
				APICheckTimeout: time.Minute,
				RSSPollInterval: time.Minute,
				RSSCheckTimeout: time.Minute,
			},
			wantAPI: 9 * time.Minute,
			wantRSS: 6 * time.Minute,
		},
		{
			name:    "nil config",
			cfg:     nil,
			wantAPI: 0,
			wantRSS: 0,
		},
		{
			name:    "zero poll interval",
			cfg:     &config.Config{APIPollInterval: 0, RSSPollInterval: 0},
			wantAPI: 0,
			wantRSS: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := APIProgressStaleAfter(tt.cfg); got != tt.wantAPI {
				t.Errorf("APIProgressStaleAfter() = %v, want %v", got, tt.wantAPI)
			}
			if got := RSSProgressStaleAfter(tt.cfg); got != tt.wantRSS {
				t.Errorf("RSSProgressStaleAfter() = %v, want %v", got, tt.wantRSS)
			}
		})
	}
}

// --- Part (c): RunOnce's bool return ---

// TestSchedulerRunOnceReturnsFalseOnAPIFetchFailure is a compile pin in shape
// (checkOutcome.ok did not exist before this change) but the assertion is the
// defect itself: a genuine fetch failure must not report a clean dry-run.
func TestSchedulerRunOnceReturnsFalseOnAPIFetchFailure(t *testing.T) {
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer apiServer.Close()

	s := newTestScheduler(t)
	s.config.APIKey = "test-key"
	s.config.DiscordWebhooks.Ransomware = config.WebhookConfig{Enabled: true, URL: "https://discord.com/api/webhooks/1/tok"}
	s.config.Feeds.GeneralFeeds = nil
	s.config.Feeds.GovernmentFeeds = nil
	s.config.Feeds.RansomwareFeeds = nil
	s.feedTypeMap = buildFeedTypeMap(s.config)

	client, err := api.NewClientWithBaseURL("test-key", apiServer.URL)
	if err != nil {
		t.Fatalf("NewClientWithBaseURL() error = %v", err)
	}
	s.apiClient = client

	if got := s.RunOnce(t.Context()); got {
		t.Fatal("RunOnce() = true on an API fetch failure, want false")
	}
}

// TestSchedulerRunOnceReturnsFalseOnAPIKeyMissingWithWebhookEnabled is the
// other ok:false branch of checkAPIOnce, a different code path than a fetch
// failure.
func TestSchedulerRunOnceReturnsFalseOnAPIKeyMissingWithWebhookEnabled(t *testing.T) {
	s := newTestScheduler(t)
	s.config.APIKey = ""
	s.config.DiscordWebhooks.Ransomware = config.WebhookConfig{Enabled: true, URL: "https://discord.com/api/webhooks/1/tok"}
	s.config.Feeds.GeneralFeeds = nil
	s.config.Feeds.GovernmentFeeds = nil
	s.config.Feeds.RansomwareFeeds = nil
	s.feedTypeMap = buildFeedTypeMap(s.config)

	if got := s.RunOnce(t.Context()); got {
		t.Fatal("RunOnce() = true when api_key is empty with a ransomware webhook enabled, want false")
	}
}

// TestSchedulerRunOnceReturnsTrueOnCleanCycle is the guard that a working
// config does not newly fail dry-run.
func TestSchedulerRunOnceReturnsTrueOnCleanCycle(t *testing.T) {
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"victims": []}`))
	}))
	defer apiServer.Close()

	s := newTestScheduler(t)
	s.config.APIKey = "test-key"
	s.config.DiscordWebhooks.Ransomware = config.WebhookConfig{Enabled: true, URL: "https://discord.com/api/webhooks/1/tok"}
	s.config.Feeds.GeneralFeeds = nil
	s.config.Feeds.GovernmentFeeds = nil
	s.config.Feeds.RansomwareFeeds = nil
	s.feedTypeMap = buildFeedTypeMap(s.config)

	client, err := api.NewClientWithBaseURL("test-key", apiServer.URL)
	if err != nil {
		t.Fatalf("NewClientWithBaseURL() error = %v", err)
	}
	s.apiClient = client

	if got := s.RunOnce(t.Context()); !got {
		t.Fatal("RunOnce() = false on a clean cycle, want true")
	}
}

// TestRunOnceRunsTheRSSCheckEvenWhenTheAPICheckFails is the short-circuit pin
// from the plan review: a "return a.ok && b.ok" one-liner reads better but
// silently skips the RSS check whenever the API check fails first. Nothing
// else in the suite covers this.
func TestRunOnceRunsTheRSSCheckEvenWhenTheAPICheckFails(t *testing.T) {
	s := newTestScheduler(t)
	s.config.APIKey = "test-key"
	s.config.DiscordWebhooks.Ransomware = config.WebhookConfig{Enabled: true, URL: "https://discord.com/api/webhooks/1/tok"}
	s.config.SlackWebhooks.RSS = config.WebhookConfig{Enabled: true, URL: "https://hooks.slack.com/services/T/B/rss"}
	s.config.Feeds.GeneralFeeds = []string{"https://example.test/feed.xml"}
	s.config.Feeds.GovernmentFeeds = nil
	s.config.Feeds.RansomwareFeeds = nil
	s.feedTypeMap = buildFeedTypeMap(s.config)

	s.apiClient = &recordingAPIClient{err: errors.New("fetch failed")}
	parser := &recordingRSSParser{}
	s.rssParser = parser

	if got := s.RunOnce(t.Context()); got {
		t.Fatal("RunOnce() = true despite the API fetch failure, want false")
	}
	if parser.feedURLs == nil {
		t.Fatal("RSS parser was never called; the API failure short-circuited RunOnce")
	}
}

// TestSchedulerRunOnceReturnsFalseOnRSSBatchParseFailure is a genuine
// batch-level parse failure, not a cycle-budget timeout.
func TestSchedulerRunOnceReturnsFalseOnRSSBatchParseFailure(t *testing.T) {
	s := newTestScheduler(t)
	s.config.APIKey = ""
	s.config.SlackWebhooks.RSS = config.WebhookConfig{Enabled: true, URL: "https://hooks.slack.com/services/T/B/rss"}
	s.config.Feeds.GeneralFeeds = []string{"https://example.test/feed.xml"}
	s.config.Feeds.GovernmentFeeds = nil
	s.config.Feeds.RansomwareFeeds = nil
	s.feedTypeMap = buildFeedTypeMap(s.config)
	s.rssParser = &recordingRSSParser{err: errors.New("boom")}

	if got := s.RunOnce(t.Context()); got {
		t.Fatal("RunOnce() = true on a genuine RSS batch parse failure, want false")
	}
}

// TestSchedulerRunOnceReturnsTrueOnRSSCycleBudgetExhaustion is the guard that
// a slow-but-working cycle must not newly fail dry-run: a cycle-budget
// overrun is exempted the same way recordRSSBatchFailure already exempts it.
func TestSchedulerRunOnceReturnsTrueOnRSSCycleBudgetExhaustion(t *testing.T) {
	fastServer := newBudgetTestFastRSSFeedServer(t)
	slowServer := newBudgetTestSlowRSSFeedServer(t)
	fastURL := fastServer.URL + "/feed.xml"
	slowURL := slowServer.URL + "/feed.xml"

	s := newRSSCycleBudgetTestScheduler(t, 150*time.Millisecond, fastURL, slowURL)
	s.config.APIKey = ""

	if got := s.RunOnce(t.Context()); !got {
		t.Fatal("RunOnce() = false on a cycle-budget overrun, want true (a slow-but-working cycle must not newly fail dry-run)")
	}
}

// TestSchedulerRunOnceReturnsTrueOnContextCancellation guards shutdown: a
// context cancelled mid-cycle must not report a cycle failure. The RSS side
// (a slow feed) and the API side (a ransomware webhook enabled, a slow API
// server) are both configured, so this actually exercises checkAPIOnce's own
// cancellation path -- unlike the version of this test the F5 review found,
// which set api_key="" with no ransomware webhook, so checkAPIOnce returned
// ok:true from the !hasActiveRansomwareWebhook branch before ever touching the
// network and never proved the API side was exempted at all.
func TestSchedulerRunOnceReturnsTrueOnContextCancellation(t *testing.T) {
	slowRSSServer := newBudgetTestSlowRSSFeedServer(t)
	slowRSSURL := slowRSSServer.URL + "/feed.xml"
	slowAPIServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer slowAPIServer.Close()

	s := newRSSCycleBudgetTestScheduler(t, time.Minute, slowRSSURL)
	s.config.APIKey = "test-key"
	s.config.APICheckTimeout = time.Minute
	s.config.DiscordWebhooks.Ransomware = config.WebhookConfig{Enabled: true, URL: "https://discord.com/api/webhooks/1/tok"}

	client, err := api.NewClientWithBaseURL("test-key", slowAPIServer.URL)
	if err != nil {
		t.Fatalf("NewClientWithBaseURL() error = %v", err)
	}
	s.apiClient = client

	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	defer cancel()

	if got := s.RunOnce(ctx); !got {
		t.Fatal("RunOnce() = false on shutdown cancellation, want true (F5: cancellation must be exempted symmetrically on both the API and RSS sides)")
	}
}
