package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/api"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/config"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/filter"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/notifyfmt"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/rss"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/status"

	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

// doneCallCancelContext is a context that cancels itself on the Nth call to
// Done(). It pins scheduler cancellation branches that sit between two
// consecutive context checks without relying on timing.
type doneCallCancelContext struct {
	mu       sync.Mutex
	cancelAt int
	calls    int
	done     chan struct{}
}

func newDoneCallCancelContext(cancelAt int) *doneCallCancelContext {
	return &doneCallCancelContext{cancelAt: cancelAt, done: make(chan struct{})}
}

func (c *doneCallCancelContext) Deadline() (time.Time, bool) { return time.Time{}, false }

func (c *doneCallCancelContext) Done() <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if c.calls >= c.cancelAt {
		select {
		case <-c.done:
		default:
			close(c.done)
		}
	}
	return c.done
}

func (c *doneCallCancelContext) Err() error {
	select {
	case <-c.done:
		return context.Canceled
	default:
		return nil
	}
}

func (c *doneCallCancelContext) Value(any) any { return nil }

type closeErrorAPIClient struct {
	closeErr error
}

func (c *closeErrorAPIClient) GetLatestEntries(context.Context) ([]api.RansomwareEntry, error) {
	return nil, nil
}

func (c *closeErrorAPIClient) Close() error { return c.closeErr }

type closeErrorWebhookSender struct {
	closeErr error
}

func (s *closeErrorWebhookSender) SendRansomwareEntry(context.Context, string, api.RansomwareEntry, *notifyfmt.FormatOptions) error {
	return nil
}

func (s *closeErrorWebhookSender) SendRSSEntry(context.Context, string, rss.Entry, string, *notifyfmt.FormatOptions) error {
	return nil
}

func (s *closeErrorWebhookSender) Close() error { return s.closeErr }

type signalingAPIClient struct {
	mu       sync.Mutex
	calls    int
	signalAt int
	signal   chan struct{}
	once     sync.Once
}

func (c *signalingAPIClient) GetLatestEntries(context.Context) ([]api.RansomwareEntry, error) {
	c.mu.Lock()
	c.calls++
	reached := c.signal != nil && c.calls >= c.signalAt
	c.mu.Unlock()
	if reached {
		c.once.Do(func() { close(c.signal) })
	}
	return nil, nil
}

func (c *signalingAPIClient) Close() error { return nil }

func (c *signalingAPIClient) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

type signalingRSSParser struct {
	mu       sync.Mutex
	calls    int
	signalAt int
	signal   chan struct{}
	once     sync.Once
}

func (p *signalingRSSParser) ParseMultipleFeedsWithValidators(_ context.Context, _ []string, _ int, _ map[string]rss.FeedHTTPValidators) (*rss.FeedResults, error) {
	p.mu.Lock()
	p.calls++
	reached := p.signal != nil && p.calls >= p.signalAt
	p.mu.Unlock()
	if reached {
		p.once.Do(func() { close(p.signal) })
	}
	return &rss.FeedResults{
		Entries:         map[string][]rss.Entry{},
		FeedErrors:      map[string]string{},
		FeedValidators:  map[string]rss.FeedHTTPValidators{},
		FeedNotModified: map[string]bool{},
	}, nil
}

// scriptedConfigReloader plays back a fixed sequence of Check results and
// signals once every scripted step has been consumed.
type scriptedConfigReloader struct {
	mu        sync.Mutex
	interval  time.Duration
	steps     []func() (*config.Config, bool, error)
	nextStep  int
	applied   int
	exhausted chan struct{}
	once      sync.Once
}

func (r *scriptedConfigReloader) Interval() time.Duration { return r.interval }

func (r *scriptedConfigReloader) Check() (*config.Config, bool, error) {
	r.mu.Lock()
	if r.nextStep >= len(r.steps) {
		r.mu.Unlock()
		r.once.Do(func() { close(r.exhausted) })
		return nil, false, nil
	}
	step := r.steps[r.nextStep]
	r.nextStep++
	r.mu.Unlock()
	return step()
}

func (r *scriptedConfigReloader) Reload() (*config.Config, error) {
	return nil, errors.New("scripted reloader does not support Reload")
}

func (r *scriptedConfigReloader) MarkApplied() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.applied++
}

func (r *scriptedConfigReloader) appliedCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.applied
}

func newFakeDependenciesForTest(cfg *config.Config, reloader ConfigReloader) (Dependencies, *recordingWebhookSender, *recordingWebhookSender) {
	discordSender := &recordingWebhookSender{}
	slackSender := &recordingWebhookSender{}
	if reloader == nil {
		reloader = &recordingConfigReloader{cfg: cfg}
	}
	return Dependencies{
		APIClient:            &recordingAPIClient{},
		RSSParser:            &recordingRSSParser{},
		DiscordWebhookSender: discordSender,
		SlackWebhookSender:   slackSender,
		StatusTracker:        status.NewMemoryTracker(),
		ConfigReloader:       reloader,
	}, discordSender, slackSender
}

func TestSchedulerComponentsFromDependenciesValidation(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Feeds.GeneralFeeds = []string{"https://feed.test/general.xml"}

	mutations := []struct {
		name    string
		mutate  func(*Dependencies)
		wantErr string
	}{
		{name: "missing API client", mutate: func(d *Dependencies) { d.APIClient = nil }, wantErr: "missing API client"},
		{name: "missing RSS parser", mutate: func(d *Dependencies) { d.RSSParser = nil }, wantErr: "missing RSS parser"},
		{name: "missing Discord sender", mutate: func(d *Dependencies) { d.DiscordWebhookSender = nil }, wantErr: "missing Discord webhook sender"},
		{name: "missing Slack sender", mutate: func(d *Dependencies) { d.SlackWebhookSender = nil }, wantErr: "missing Slack webhook sender"},
		{name: "missing status tracker", mutate: func(d *Dependencies) { d.StatusTracker = nil }, wantErr: "missing status tracker"},
		{name: "missing config reloader", mutate: func(d *Dependencies) { d.ConfigReloader = nil }, wantErr: "missing config reloader"},
	}
	for _, tt := range mutations {
		t.Run(tt.name, func(t *testing.T) {
			deps, _, _ := newFakeDependenciesForTest(cfg, nil)
			tt.mutate(&deps)
			if _, err := schedulerComponentsFromDependencies(cfg, deps); err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("schedulerComponentsFromDependencies() error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}

	deps, _, _ := newFakeDependenciesForTest(cfg, nil)
	components, err := schedulerComponentsFromDependencies(cfg, deps)
	if err != nil {
		t.Fatalf("schedulerComponentsFromDependencies(valid) error = %v", err)
	}
	if got := components.feedTypeMap["https://feed.test/general.xml"]; got != config.FeedTypeGeneral {
		t.Fatalf("fallback feed type map entry = %q, want %q", got, config.FeedTypeGeneral)
	}
}

func TestNewWithDependenciesPropagatesValidationError(t *testing.T) {
	cfg := config.DefaultConfig()
	if _, err := NewWithDependencies(cfg, t.TempDir(), true, Dependencies{}); err == nil {
		t.Fatal("NewWithDependencies(empty deps) error = nil, want validation error")
	}
}

func TestSchedulerComponentsCloseReleasesResources(t *testing.T) {
	var nilComponents *schedulerComponents
	nilComponents.close()

	lockDir := t.TempDir()
	lock, err := status.AcquireDataDirLock(lockDir)
	if err != nil {
		t.Fatalf("AcquireDataDirLock() error = %v", err)
	}

	components := &schedulerComponents{
		apiClient:            &closeErrorAPIClient{closeErr: errors.New("api close failed")},
		discordWebhookSender: &closeErrorWebhookSender{closeErr: errors.New("discord close failed")},
		slackWebhookSender:   &closeErrorWebhookSender{closeErr: errors.New("slack close failed")},
		dataDirLock:          lock,
	}
	components.close()

	if components.apiClient != nil || components.discordWebhookSender != nil ||
		components.slackWebhookSender != nil || components.dataDirLock != nil {
		t.Fatalf("schedulerComponents.close() left resources set: %#v", components)
	}

	relock, err := status.AcquireDataDirLock(lockDir)
	if err != nil {
		t.Fatalf("data_dir lock was not released by close(): %v", err)
	}
	if err := relock.Release(); err != nil {
		t.Fatalf("Release(relock) error = %v", err)
	}
}

func TestNewFailsWithInvalidAPIBaseURL(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.APIKey = "test-key"
	cfg.DataDir = t.TempDir()
	cfg.APIBaseURL = "url-without-host"

	if _, err := New(cfg, t.TempDir(), false); err == nil || !strings.Contains(err.Error(), "failed to create API client") {
		t.Fatalf("New(invalid base URL) error = %v, want API client creation failure", err)
	}
}

func TestNewFailsWhenAPIStatusCorruptWithRansomwareDeliveryEnabled(t *testing.T) {
	dataDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dataDir, "api_status.json"), []byte("{corrupt"), 0600); err != nil {
		t.Fatalf("WriteFile(api_status.json) error = %v", err)
	}

	cfg := config.DefaultConfig()
	cfg.APIKey = "test-key"
	cfg.DataDir = dataDir
	cfg.SlackWebhooks.Ransomware = config.WebhookConfig{
		Enabled: true,
		URL:     "https://hooks.slack.com/services/T00000000/B00000000/tokentokentoken",
	}

	if _, err := New(cfg, t.TempDir(), false); err == nil || !strings.Contains(err.Error(), "failed to create status tracker") {
		t.Fatalf("New(corrupt api_status.json) error = %v, want status tracker failure", err)
	}

	// The failed constructor must release the data_dir lock again.
	relock, err := status.AcquireDataDirLock(dataDir)
	if err != nil {
		t.Fatalf("data_dir lock still held after failed New(): %v", err)
	}
	if err := relock.Release(); err != nil {
		t.Fatalf("Release(relock) error = %v", err)
	}
}

func TestStartReturnsContextErrorWhenCancelledDuringInitialChecks(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.APIKey = "test-key"
	cfg.DataDir = t.TempDir()
	deps, _, _ := newFakeDependenciesForTest(cfg, nil)

	s, err := NewWithDependencies(cfg, t.TempDir(), true, deps)
	if err != nil {
		t.Fatalf("NewWithDependencies() error = %v", err)
	}
	t.Cleanup(func() {
		if err := s.Stop(); err != nil {
			t.Errorf("Stop() error = %v", err)
		}
	})

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := s.Start(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Start(cancelled ctx) error = %v, want context.Canceled", err)
	}
	if s.started {
		t.Fatal("scheduler marked started although Start aborted before tickers")
	}
}

func TestStartPollersRunPeriodicChecksFromTickers(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.APIKey = "tick-key"
	cfg.DataDir = t.TempDir()
	cfg.APIPollInterval = 25 * time.Millisecond
	cfg.RSSPollInterval = 25 * time.Millisecond
	cfg.DiscordDelay = 0
	cfg.SlackDelay = 0
	cfg.SlackWebhooks.Ransomware = config.WebhookConfig{
		Enabled: true,
		URL:     "https://hooks.slack.com/services/T00000000/B00000000/ticker",
	}
	cfg.DiscordWebhooks.RSS = config.WebhookConfig{
		Enabled: true,
		URL:     "https://discord.com/api/webhooks/123456789012345678/ticker-token",
	}
	cfg.Feeds.GeneralFeeds = []string{"https://feeds.test/general.xml"}

	apiClient := &signalingAPIClient{signalAt: 2, signal: make(chan struct{})}
	rssParser := &signalingRSSParser{signalAt: 2, signal: make(chan struct{})}
	deps, _, _ := newFakeDependenciesForTest(cfg, nil)
	deps.APIClient = apiClient
	deps.RSSParser = rssParser

	s, err := NewWithDependencies(cfg, t.TempDir(), true, deps)
	if err != nil {
		t.Fatalf("NewWithDependencies() error = %v", err)
	}

	if err := s.Start(t.Context()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	waitForTestSignalWithTimeout(t, apiClient.signal, 10*time.Second, "second API poll from ticker")
	waitForTestSignalWithTimeout(t, rssParser.signal, 10*time.Second, "second RSS poll from ticker")

	if err := s.Stop(); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if got := apiClient.callCount(); got < 2 {
		t.Fatalf("API poll count = %d, want at least 2 (initial check plus ticker)", got)
	}
}

func TestRunConfigWatcherHandlesCheckAndReloadOutcomes(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()

	cfg := config.DefaultConfig()
	cfg.APIKey = "watcher-key"
	cfg.DataDir = t.TempDir()

	badCfg := config.DefaultConfig()
	badCfg.APIKey = "watcher-key-changed"
	badCfg.APIBaseURL = "url-without-host"

	goodCfg := config.DefaultConfig()
	goodCfg.APIKey = "watcher-key"
	goodCfg.RetryMaxAttempts = cfg.RetryMaxAttempts + 1

	reloader := &scriptedConfigReloader{
		interval:  5 * time.Millisecond,
		exhausted: make(chan struct{}),
		steps: []func() (*config.Config, bool, error){
			func() (*config.Config, bool, error) { return nil, false, errors.New("signature probe failed") },
			func() (*config.Config, bool, error) { return nil, true, errors.New("reload validation failed") },
			func() (*config.Config, bool, error) { return badCfg, true, nil },
			func() (*config.Config, bool, error) { return goodCfg, true, nil },
		},
	}

	deps, _, _ := newFakeDependenciesForTest(cfg, reloader)
	s, err := NewWithDependencies(cfg, t.TempDir(), false, deps)
	if err != nil {
		t.Fatalf("NewWithDependencies() error = %v", err)
	}

	s.wg.Add(1)
	go s.runConfigWatcher(t.Context())

	waitForTestSignalWithTimeout(t, reloader.exhausted, 10*time.Second, "config watcher script completion")
	s.signalStop()

	stopped := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(stopped)
	}()
	waitForTestSignalWithTimeout(t, stopped, 10*time.Second, "config watcher shutdown")

	if got := reloader.appliedCount(); got != 1 {
		t.Fatalf("MarkApplied count = %d, want 1 (only the valid reload)", got)
	}
	if entry := findTestLogEntry(hook, "Failed to check config file state"); entry == nil {
		t.Fatal("missing warn log for failed config state check")
	}
	if entry := findTestLogEntry(hook, "Config reload failed, keeping current config"); entry == nil {
		t.Fatal("missing error log for failed config reload")
	}
	if entry := findTestLogEntry(hook, "Config reloaded successfully"); entry == nil {
		t.Fatal("missing success log for applied config reload")
	}
	if got := s.getConfig().RetryMaxAttempts; got != goodCfg.RetryMaxAttempts {
		t.Fatalf("RetryMaxAttempts after watcher reload = %d, want %d", got, goodCfg.RetryMaxAttempts)
	}
	if got := s.getConfig().APIBaseURL; got != cfg.APIBaseURL {
		t.Fatalf("APIBaseURL = %q, want invalid reload to be rejected", got)
	}
}

func TestReloadConfigReturnsReloaderError(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.APIKey = "test-key"
	cfg.DataDir = t.TempDir()
	deps, _, _ := newFakeDependenciesForTest(cfg, &recordingConfigReloader{err: errors.New("reload source unavailable")})

	s, err := NewWithDependencies(cfg, t.TempDir(), true, deps)
	if err != nil {
		t.Fatalf("NewWithDependencies() error = %v", err)
	}

	changed, err := s.ReloadConfig()
	if err == nil || !strings.Contains(err.Error(), "config reload failed validation") {
		t.Fatalf("ReloadConfig() error = %v, want validation failure", err)
	}
	if changed {
		t.Fatal("ReloadConfig() reported change despite reloader failure")
	}
}

func TestReloadConfigReturnsApplyErrorForInvalidAPIBaseURL(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.APIKey = "apply-error-key"
	cfg.DataDir = t.TempDir()

	badCfg := config.DefaultConfig()
	badCfg.APIKey = "apply-error-key-changed"
	badCfg.APIBaseURL = "url-without-host"

	deps, _, _ := newFakeDependenciesForTest(cfg, &recordingConfigReloader{cfg: badCfg, changed: true})
	s, err := NewWithDependencies(cfg, t.TempDir(), true, deps)
	if err != nil {
		t.Fatalf("NewWithDependencies() error = %v", err)
	}

	changed, err := s.ReloadConfig()
	if err == nil || !strings.Contains(err.Error(), "failed to rebuild API client during config reload") {
		t.Fatalf("ReloadConfig() error = %v, want API client rebuild failure", err)
	}
	if changed {
		t.Fatal("ReloadConfig() reported change despite apply failure")
	}
	if got := s.getConfig().APIBaseURL; got != cfg.APIBaseURL {
		t.Fatalf("APIBaseURL = %q, want original config kept after apply failure", got)
	}
}

func TestReloadConfigWarnsWhenReplacedAPIClientCloseFails(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()

	cfg := config.DefaultConfig()
	cfg.APIKey = "old-key"
	cfg.DataDir = t.TempDir()

	newCfg := config.DefaultConfig()
	newCfg.APIKey = "new-key"

	oldClient := &closeErrorAPIClient{closeErr: errors.New("old client close failed")}
	deps, _, _ := newFakeDependenciesForTest(cfg, &recordingConfigReloader{cfg: newCfg, changed: true})
	deps.APIClient = oldClient

	s, err := NewWithDependencies(cfg, t.TempDir(), true, deps)
	if err != nil {
		t.Fatalf("NewWithDependencies() error = %v", err)
	}
	t.Cleanup(func() {
		if err := s.Stop(); err != nil {
			t.Errorf("Stop() error = %v", err)
		}
	})

	changed, err := s.ReloadConfig()
	if err != nil {
		t.Fatalf("ReloadConfig() error = %v", err)
	}
	if !changed {
		t.Fatal("ReloadConfig() changed = false, want true")
	}
	if s.apiClient == APIClient(oldClient) {
		t.Fatal("API client was not replaced after api_key change")
	}
	if entry := findTestLogEntry(hook, "Failed to close replaced API client"); entry == nil {
		t.Fatal("missing warn log for failed close of replaced API client")
	}
}

func TestReloadConfigResetsTickersAndWarnsOnInvalidLogLevel(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()
	previousLevel := log.GetLevel()
	log.SetLevel(log.InfoLevel)
	defer log.SetLevel(previousLevel)

	cfg := config.DefaultConfig()
	cfg.APIKey = "ticker-key"
	cfg.DataDir = t.TempDir()

	newCfg := config.DefaultConfig()
	newCfg.APIKey = "ticker-key"
	newCfg.APIPollInterval = cfg.APIPollInterval + time.Minute
	newCfg.RSSPollInterval = cfg.RSSPollInterval + time.Minute
	newCfg.LogLevel = "definitely-not-a-level"

	deps, _, _ := newFakeDependenciesForTest(cfg, &recordingConfigReloader{cfg: newCfg, changed: true})
	s, err := NewWithDependencies(cfg, t.TempDir(), true, deps)
	if err != nil {
		t.Fatalf("NewWithDependencies() error = %v", err)
	}

	s.apiTicker = time.NewTicker(time.Hour)
	s.rssTicker = time.NewTicker(time.Hour)
	defer s.apiTicker.Stop()
	defer s.rssTicker.Stop()

	changed, err := s.ReloadConfig()
	if err != nil {
		t.Fatalf("ReloadConfig() error = %v", err)
	}
	if !changed {
		t.Fatal("ReloadConfig() changed = false, want true")
	}
	if entry := findTestLogEntry(hook, "API poll interval updated"); entry == nil {
		t.Fatal("missing log for API poll interval ticker reset")
	}
	if entry := findTestLogEntry(hook, "RSS poll interval updated"); entry == nil {
		t.Fatal("missing log for RSS poll interval ticker reset")
	}
	if entry := findTestLogEntry(hook, "Log level update failed"); entry == nil {
		t.Fatal("missing warn log for invalid log level during reload")
	}
	if log.GetLevel() != log.InfoLevel {
		t.Fatalf("log level changed to %v despite invalid value", log.GetLevel())
	}
}

func TestCloseSchedulerResourcesLogsCloseFailures(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()

	cfg := config.DefaultConfig()
	cfg.APIKey = "test-key"
	cfg.DataDir = t.TempDir()
	deps, _, _ := newFakeDependenciesForTest(cfg, nil)
	deps.APIClient = &closeErrorAPIClient{closeErr: errors.New("api close failed")}
	deps.DiscordWebhookSender = &closeErrorWebhookSender{closeErr: errors.New("discord close failed")}
	deps.SlackWebhookSender = &closeErrorWebhookSender{closeErr: errors.New("slack close failed")}

	s, err := NewWithDependencies(cfg, t.TempDir(), true, deps)
	if err != nil {
		t.Fatalf("NewWithDependencies() error = %v", err)
	}

	if err := s.closeSchedulerResources(); err != nil {
		t.Fatalf("closeSchedulerResources() error = %v, want nil (sender failures are logged)", err)
	}
	for _, message := range []string{
		"Failed to close API client",
		"Failed to close Discord webhook sender",
		"Failed to close Slack webhook sender",
	} {
		if entry := findTestLogEntry(hook, message); entry == nil {
			t.Fatalf("missing warn log %q", message)
		}
	}
}

func TestCheckAPIOnceAbortsWhenAPIStatusLoadFails(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()

	s := newTestScheduler(t)
	if err := os.WriteFile(filepath.Join(s.config.DataDir, "api_status.json"), []byte("{corrupt"), 0600); err != nil {
		t.Fatalf("WriteFile(api_status.json) error = %v", err)
	}
	s.config.SlackWebhooks.Ransomware = config.WebhookConfig{
		Enabled: true,
		URL:     "https://hooks.slack.test/services/load-failure",
	}
	client := &signalingAPIClient{}
	s.apiClient = client

	s.checkAPIOnce(t.Context())

	if got := client.callCount(); got != 0 {
		t.Fatalf("API fetch count = %d, want 0 when API status load fails", got)
	}
	if entry := findTestLogEntry(hook, "Failed to load API status before API polling"); entry == nil {
		t.Fatal("missing error log for failed API status load")
	}
}

func TestWebhookLockLazyInitAndReuse(t *testing.T) {
	s := &Scheduler{}
	first := s.webhookLock("slack", "https://hooks.example.test/a")
	if first == nil {
		t.Fatal("webhookLock() returned nil mutex")
	}
	if s.webhookLocks == nil {
		t.Fatal("webhookLock() did not lazily initialize the lock map")
	}
	if second := s.webhookLock("slack", "https://hooks.example.test/a"); second != first {
		t.Fatal("webhookLock() returned a different mutex for the same key")
	}
}

func TestSendWithWebhookLockReturnsErrorWhenCancelledBeforeSend(t *testing.T) {
	s := newTestScheduler(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	called := false
	err := s.sendWithWebhookLock(ctx, "slack", "https://hooks.example.test/a", func() error {
		called = true
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("sendWithWebhookLock(cancelled ctx) error = %v, want context.Canceled", err)
	}
	if called {
		t.Fatal("send function was called despite cancelled context")
	}
}

func TestSendWithWebhookLockReturnsErrorWhenCancelledAfterAcquiringLock(t *testing.T) {
	s := newTestScheduler(t)
	ctx := newDoneCallCancelContext(2)

	called := false
	err := s.sendWithWebhookLock(ctx, "slack", "https://hooks.example.test/b", func() error {
		called = true
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("sendWithWebhookLock(cancel after lock) error = %v, want context.Canceled", err)
	}
	if called {
		t.Fatal("send function was called despite context cancellation after lock acquisition")
	}
}

func TestProcessRSSFeedBatchesRecordsBatchFailureOnParserError(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()

	s := newTestScheduler(t)
	feedURL := "https://feeds.test/failing.xml"
	s.config.Feeds.GeneralFeeds = []string{feedURL}
	s.config.DiscordWebhooks.RSS = config.WebhookConfig{
		Enabled: true,
		URL:     "https://discord.com/api/webhooks/123456789012345678/batch-token",
	}
	s.rssParser = &recordingRSSParser{err: errors.New("worker pool start failed")}

	pollFields := newPollLogFields(pollTypeRSS)
	batches, feedURLs := s.rssFeedBatchesForConfig(s.config, pollFields)
	if len(batches) != 1 || len(feedURLs) != 1 {
		t.Fatalf("batches = %d, feedURLs = %d, want 1 each", len(batches), len(feedURLs))
	}

	s.processRSSFeedBatches(t.Context(), s.config, pollFields, batches, feedURLs, rssDeliveryAttemptSet{})

	if entry := findTestLogEntry(hook, "Failed to parse RSS feed batch"); entry == nil {
		t.Fatal("missing error log for failed RSS feed batch parse")
	}
	info, exists := s.statusTracker.GetRSSFeedInfo(feedURL)
	if !exists {
		t.Fatal("feed status was not recorded after batch failure")
	}
	if info.LastError == nil || *info.LastError == "" {
		t.Fatalf("feed status LastError = %#v, want batch failure message", info.LastError)
	}
}

func TestFilterAndRecordNewRSSItemsGuardsAndEmptyKeys(t *testing.T) {
	s := newTestScheduler(t)

	if got := s.filterAndRecordNewRSSItems(nil, config.FeedTypeGeneral); got != 0 {
		t.Fatalf("filterAndRecordNewRSSItems(nil results) = %d, want 0", got)
	}

	feedURL := "https://feeds.test/empty-key.xml"
	feedResults := &rss.FeedResults{
		Entries: map[string][]rss.Entry{
			feedURL: {{}},
		},
	}
	if got := s.filterAndRecordNewRSSItems(feedResults, config.FeedTypeGeneral); got != 0 {
		t.Fatalf("filterAndRecordNewRSSItems(unkeyed entry) = %d, want 0", got)
	}
	if remaining := feedResults.Entries[feedURL]; len(remaining) != 0 {
		t.Fatalf("unkeyed entry remained in feed results: %#v", remaining)
	}
}

func TestRecordParsedRSSFeedTypesGuardsAndUpdates(t *testing.T) {
	s := newTestScheduler(t)
	feedURL := "https://feeds.test/feed-type.xml"
	entry := rss.Entry{
		Title:     "Feed type entry",
		GUID:      "feed-type-guid",
		FeedURL:   feedURL,
		FeedTitle: "Feed Type Feed",
		Published: time.Now().UTC(),
	}
	key := rss.GenerateEntryKeyForEntry(entry)
	stored := status.StoredRSSEntryFromRSS(entry, key)
	s.statusTracker.MarkRSSItemsParsed(feedURL, map[string]status.StoredRSSEntry{key: stored})

	feedResults := &rss.FeedResults{
		Entries: map[string][]rss.Entry{
			feedURL: {entry, {}},
		},
	}

	if got := s.recordParsedRSSFeedTypes(feedResults, ""); got != 0 {
		t.Fatalf("recordParsedRSSFeedTypes(empty feed type) = %d, want 0", got)
	}
	if got := s.recordParsedRSSFeedTypes(feedResults, config.FeedTypeGeneral); got != 1 {
		t.Fatalf("recordParsedRSSFeedTypes() = %d, want 1 updated item", got)
	}
	if got := s.recordParsedRSSFeedTypes(feedResults, config.FeedTypeGeneral); got != 0 {
		t.Fatalf("recordParsedRSSFeedTypes(second run) = %d, want 0 (already set)", got)
	}
}

func TestMinimizeRSSItemsWithoutMatchingTargetGuards(t *testing.T) {
	s := newTestScheduler(t)

	if got := s.minimizeRSSItemsWithoutMatchingTarget(nil, nil); got != 0 {
		t.Fatalf("minimizeRSSItemsWithoutMatchingTarget(nil results) = %d, want 0", got)
	}

	feedResults := &rss.FeedResults{
		Entries: map[string][]rss.Entry{
			"https://feeds.test/minimize.xml": {{}},
		},
	}
	targets := []webhookTarget{{
		destinationID: "discord.rss.general",
		messenger:     "discord",
		filters:       &filter.Rules{IncludeCategories: []string{"never-matches"}},
	}}
	if got := s.minimizeRSSItemsWithoutMatchingTarget(feedResults, targets); got != 0 {
		t.Fatalf("minimizeRSSItemsWithoutMatchingTarget(unkeyed entry) = %d, want 0", got)
	}
}

func TestSendUnsentRSSItemsWithConfigStopsWhenCancelled(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()
	previousLevel := log.GetLevel()
	log.SetLevel(log.DebugLevel)
	defer log.SetLevel(previousLevel)

	s := newTestScheduler(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	s.sendUnsentRSSItemsWithConfig(ctx, s.config, nil)

	if entry := findTestLogEntry(hook, "RSS unsent items sending cancelled due to context"); entry == nil {
		t.Fatal("missing debug log for cancelled unsent RSS recovery")
	}
}

func TestSendParsedRSSToWebhooksCapsWithoutAttemptTracking(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()

	s := newTestScheduler(t)
	s.dryRun = true
	s.config.RSSMaxEntriesPerCycle = 1

	feedURL := "https://feeds.test/cap.xml"
	now := time.Now().UTC()
	feedResults := &rss.FeedResults{
		Entries: map[string][]rss.Entry{
			feedURL: {
				{Title: "Cap one", GUID: "cap-1", FeedURL: feedURL, Published: now.Add(-2 * time.Minute)},
				{Title: "Cap two", GUID: "cap-2", FeedURL: feedURL, Published: now.Add(-time.Minute)},
			},
		},
		FeedErrors: map[string]string{},
	}
	targets := []webhookTarget{{
		destinationID: "discord.rss.general",
		messenger:     "discord",
	}}

	s.sendParsedRSSToWebhooksWithConfig(t.Context(), s.config, feedResults, config.FeedTypeGeneral, targets, nil)

	entry := findTestLogEntry(hook, "Capping RSS fresh send batch for this webhook")
	if entry == nil {
		t.Fatal("missing cap log for RSS fresh send batch")
	}
	if got := entry.Data["deferred_by_cap"]; got != 1 {
		t.Fatalf("deferred_by_cap = %#v, want 1", got)
	}
}

func TestSendRSSEntryToTargetFailsWithoutMessengerDelivery(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()

	s := newTestScheduler(t)
	entry := rss.Entry{
		Title:     "No sender entry",
		GUID:      "no-sender-guid",
		Link:      "https://example.test/no-sender",
		FeedURL:   "https://feeds.test/no-sender.xml",
		FeedTitle: "No Sender Feed",
		Published: time.Now().UTC(),
	}
	target := webhookTarget{
		url:           "https://hooks.example.test/no-sender",
		destinationID: "bogus.rss.general",
		messenger:     "bogus",
	}

	outcome := s.sendRSSEntryToTarget(
		t.Context(),
		s.config,
		config.FeedTypeGeneral,
		target,
		freshRSSDeliveryItem(entry),
		rssDeliveryModeFresh,
		0,
		1,
		nil,
	)
	if outcome != rssDeliveryOutcomeFailed {
		t.Fatalf("sendRSSEntryToTarget(unknown messenger) outcome = %v, want failed", outcome)
	}
	if entry := findTestLogEntry(hook, "Missing RSS webhook sender"); entry == nil {
		t.Fatal("missing error log for unknown RSS messenger delivery")
	}
}

func TestSendRSSEntryToTargetCancelsDuringSendDelay(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()

	s := newTestScheduler(t)
	s.config.SlackDelay = time.Hour
	slackSender := &recordingWebhookSender{}
	s.slackWebhookSender = slackSender

	entry := rss.Entry{
		Title:     "Delay cancel entry",
		GUID:      "delay-cancel-guid",
		Link:      "https://example.test/delay-cancel",
		FeedURL:   "https://feeds.test/delay-cancel.xml",
		FeedTitle: "Delay Feed",
		Published: time.Now().UTC(),
	}
	target := webhookTarget{
		url:           "https://hooks.slack.test/services/delay-cancel",
		destinationID: "slack.rss.general",
		messenger:     "slack",
	}

	// Done() call 1: entry select, call 2: send-delay select -> cancels there.
	ctx := newDoneCallCancelContext(2)
	outcome := s.sendRSSEntryToTarget(
		ctx,
		s.config,
		config.FeedTypeGeneral,
		target,
		freshRSSDeliveryItem(entry),
		rssDeliveryModeFresh,
		0,
		1,
		nil,
	)
	if outcome != rssDeliveryOutcomeCanceled {
		t.Fatalf("sendRSSEntryToTarget(delay cancel) outcome = %v, want canceled", outcome)
	}
	if got := slackSender.rssCallCount(); got != 0 {
		t.Fatalf("RSS sends = %d, want 0 after cancellation during send delay", got)
	}
	if entry := findTestLogEntry(hook, "Context cancelled during RSS send delay"); entry == nil {
		t.Fatal("missing warn log for cancellation during RSS send delay")
	}
}

func TestSendRSSItemsToWebhookWithConfigEmptyItems(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()
	previousLevel := log.GetLevel()
	log.SetLevel(log.DebugLevel)
	defer log.SetLevel(previousLevel)

	s := newTestScheduler(t)
	target := webhookTarget{
		url:           "https://hooks.slack.test/services/empty",
		destinationID: "slack.rss.general",
		messenger:     "slack",
	}

	s.sendRSSItemsToWebhookWithConfig(t.Context(), s.config, nil, config.FeedTypeGeneral, target)

	if entry := findTestLogEntry(hook, "No RSS items to send"); entry == nil {
		t.Fatal("missing debug log for empty RSS recovery batch")
	}
}

func recoveryRSSItemForTest(t *testing.T, feedURL, guid, title string, published time.Time) status.UnsentRSSItem {
	t.Helper()
	stored := status.StoredRSSEntry{
		Key:       rss.GenerateEntryKey(feedURL, guid, "", title),
		FeedURL:   feedURL,
		Title:     title,
		GUID:      guid,
		Published: published.Format("2006-01-02 15:04:05.999999"),
		FeedTitle: "Recovery Feed",
	}
	return status.UnsentRSSItemFromStored(stored)
}

func TestSendRSSItemsToWebhookWithConfigStopsOnCancelledContext(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()

	s := newTestScheduler(t)
	slackSender := &recordingWebhookSender{}
	s.slackWebhookSender = slackSender
	items := []status.UnsentRSSItem{
		recoveryRSSItemForTest(t, "https://feeds.test/cancel.xml", "cancel-1", "Cancel One", time.Now().UTC()),
	}
	target := webhookTarget{
		url:           "https://hooks.slack.test/services/cancel",
		destinationID: "slack.rss.general",
		messenger:     "slack",
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	s.sendRSSItemsToWebhookWithConfig(ctx, s.config, items, config.FeedTypeGeneral, target)

	if got := slackSender.rssCallCount(); got != 0 {
		t.Fatalf("RSS sends = %d, want 0 with cancelled context", got)
	}
	if entry := findTestLogEntry(hook, "Context cancelled, stopping RSS send"); entry == nil {
		t.Fatal("missing warn log for cancelled RSS recovery loop")
	}
}

func TestSendRSSItemsToWebhookWithConfigSkipsDeadLetteredItems(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()

	s := newTestScheduler(t)
	slackSender := &recordingWebhookSender{}
	s.slackWebhookSender = slackSender

	item := recoveryRSSItemForTest(t, "https://feeds.test/dead.xml", "dead-1", "Dead One", time.Now().UTC())
	target := webhookTarget{
		url:           "https://hooks.slack.test/services/dead",
		destinationID: "slack.rss.general",
		messenger:     "slack",
	}
	s.statusTracker.MarkRetryDeadLetterForDestination(
		item.Key,
		target.destinationID,
		target.messenger,
		status.RetryItemTypeRSS.String(),
		item.Title,
		"terminal failure",
	)

	s.sendRSSItemsToWebhookWithConfig(t.Context(), s.config, []status.UnsentRSSItem{item}, config.FeedTypeGeneral, target)

	if got := slackSender.rssCallCount(); got != 0 {
		t.Fatalf("RSS sends = %d, want 0 for dead-lettered item", got)
	}
	summary := findTestLogEntry(hook, "RSS items send to webhook completed")
	if summary == nil {
		t.Fatal("missing RSS recovery completion summary")
	}
	if got := summary.Data["dead_letter_count"]; got != 1 {
		t.Fatalf("dead_letter_count = %#v, want 1", got)
	}
}

func TestSendRSSItemsToWebhookWithConfigReturnsOnDelayCancel(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()

	s := newTestScheduler(t)
	s.config.SlackDelay = time.Hour
	slackSender := &recordingWebhookSender{}
	s.slackWebhookSender = slackSender

	item := recoveryRSSItemForTest(t, "https://feeds.test/recovery-delay.xml", "recovery-delay-1", "Recovery Delay", time.Now().UTC())
	target := webhookTarget{
		url:           "https://hooks.slack.test/services/recovery-delay",
		destinationID: "slack.rss.general",
		messenger:     "slack",
	}

	// Done() calls: recovery loop select, entry select, send-delay select.
	ctx := newDoneCallCancelContext(3)
	s.sendRSSItemsToWebhookWithConfig(ctx, s.config, []status.UnsentRSSItem{item}, config.FeedTypeGeneral, target)

	if got := slackSender.rssCallCount(); got != 0 {
		t.Fatalf("RSS sends = %d, want 0 after delay cancellation", got)
	}
	if entry := findTestLogEntry(hook, "Context cancelled during RSS recovery send delay"); entry == nil {
		t.Fatal("missing warn log for cancelled recovery send delay")
	}
}

func TestProcessAPIDeliveryTargetLogsWhenNoUnsentItems(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()
	previousLevel := log.GetLevel()
	log.SetLevel(log.DebugLevel)
	defer log.SetLevel(previousLevel)

	s := newTestScheduler(t)
	target := apiDeliveryTarget{
		url:           "https://hooks.slack.test/services/no-unsent",
		destinationID: "slack.ransomware",
		messenger:     "slack",
	}

	s.processAPIDeliveryTarget(t.Context(), s.config, nil, target)

	if entry := findTestLogEntry(hook, "No unsent API items found"); entry == nil {
		t.Fatal("missing debug log for empty non-dry-run API delivery")
	}
}

func TestAPIDeliveryTargetForWebhookConfigRejectsUnknownPlatform(t *testing.T) {
	s := newTestScheduler(t)
	targetConfig := config.WebhookTargetConfig{
		Platform: "matrix",
		Name:     config.WebhookTypeRansomware,
		Webhook:  config.WebhookConfig{Enabled: true, URL: "https://hooks.example.test/x"},
	}

	if _, ok := s.apiDeliveryTargetForWebhookConfig(s.config, targetConfig); ok {
		t.Fatal("apiDeliveryTargetForWebhookConfig(unknown platform) ok = true, want false")
	}
}

func TestQueuePendingAPIEntriesForTargetDefaultsReason(t *testing.T) {
	s := newTestScheduler(t)
	entry := api.RansomwareEntry{
		ID:     "pending-reason",
		Group:  "lockbit",
		Victim: "Reason Corp",
	}
	target := apiDeliveryTarget{
		url:           "https://hooks.slack.test/services/pending",
		destinationID: "slack.ransomware",
		messenger:     "slack",
	}

	if queued := s.queuePendingAPIEntriesForTarget([]api.RansomwareEntry{entry}, target, ""); queued != 1 {
		t.Fatalf("queuePendingAPIEntriesForTarget() = %d, want 1", queued)
	}
	records := s.statusTracker.GetQueuedRetryItemsByType(status.RetryItemTypeAPI.String())
	if len(records) != 1 {
		t.Fatalf("queued retry records = %d, want 1", len(records))
	}
	if got := records[0].Item.LastError; got != "pending API delivery" {
		t.Fatalf("queued reason = %q, want default %q", got, "pending API delivery")
	}
}

func TestSendAPIEntriesIndividuallyNoEntries(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()
	previousLevel := log.GetLevel()
	log.SetLevel(log.DebugLevel)
	defer log.SetLevel(previousLevel)

	s := newTestScheduler(t)
	target := apiDeliveryTarget{
		destinationID: "slack.ransomware",
		messenger:     "slack",
	}

	s.sendAPIEntriesIndividually(t.Context(), nil, target)

	if entry := findTestLogEntry(hook, "No entries to send"); entry == nil {
		t.Fatal("missing debug log for empty API send batch")
	}
}

func TestSendAPIEntriesIndividuallySkipsEntriesWithoutSender(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()

	s := newTestScheduler(t)
	entry := api.RansomwareEntry{
		ID:     "no-sender",
		Group:  "lockbit",
		Victim: "Missing Sender Corp",
	}
	target := apiDeliveryTarget{
		url:           "https://hooks.slack.test/services/no-sender",
		destinationID: "slack.ransomware",
		messenger:     "slack",
		send:          nil,
	}

	s.sendAPIEntriesIndividually(t.Context(), []api.RansomwareEntry{entry}, target)

	if logEntry := findTestLogEntry(hook, "Missing API webhook sender"); logEntry == nil {
		t.Fatal("missing error log for API target without sender")
	}
	key := api.GenerateEntryKey(entry)
	if s.statusTracker.IsAPIItemSentToDestination(key, target.destinationID, target.url) {
		t.Fatal("entry was marked sent although no sender was configured")
	}
}

func TestMigrateLegacyAPIFetchedItemsSkipsMalformedPayloads(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()

	s := newTestScheduler(t)
	s.config.SlackWebhooks.Ransomware = config.WebhookConfig{
		Enabled: true,
		URL:     "https://hooks.slack.com/services/T12345678/B12345678/malformed-migration",
	}

	validEntry := api.RansomwareEntry{
		ID:         "legacy-valid",
		Group:      "lockbit",
		Victim:     "Valid Corp",
		Discovered: time.Date(2026, 1, 6, 9, 0, 0, 0, time.UTC),
	}
	legacyStatus := map[string]any{
		"last_updated":  time.Date(2026, 1, 6, 10, 0, 0, 0, time.UTC),
		"last_check":    time.Date(2026, 1, 6, 10, 0, 0, 0, time.UTC),
		"entries_found": 2,
		"sent_items":    map[string]any{},
		"fetched_items": []any{"malformed-string-payload", validEntry},
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
		t.Fatalf("queued API retry records = %d, want 1 (only the valid legacy item)", len(records))
	}
	if got := records[0].Item.ItemKey; got != api.GenerateEntryKey(validEntry) {
		t.Fatalf("migrated item key = %q, want %q", got, api.GenerateEntryKey(validEntry))
	}
	if entry := findTestLogEntry(hook, "Skipping malformed legacy API fetched item during migration"); entry == nil {
		t.Fatal("missing warn log for malformed legacy fetched item")
	}
	summary := findTestLogEntry(hook, "Migrated legacy API fetched items to pending retry queue")
	if summary == nil {
		t.Fatal("missing migration summary log")
	}
	if got := summary.Data["malformed_items"]; got != 1 {
		t.Fatalf("malformed_items = %#v, want 1", got)
	}
}

func TestMigrateLegacyAPIFetchedItemsWarnsWhenPersistFails(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()

	dataDir := t.TempDir()
	entry := api.RansomwareEntry{
		ID:         "legacy-persist",
		Group:      "lockbit",
		Victim:     "Persist Corp",
		Discovered: time.Date(2026, 1, 6, 9, 0, 0, 0, time.UTC),
	}
	legacyStatus := map[string]any{
		"last_updated":  time.Date(2026, 1, 6, 10, 0, 0, 0, time.UTC),
		"last_check":    time.Date(2026, 1, 6, 10, 0, 0, 0, time.UTC),
		"entries_found": 1,
		"sent_items":    map[string]any{},
		"fetched_items": []any{entry},
	}
	data, err := json.Marshal(legacyStatus)
	if err != nil {
		t.Fatalf("Marshal(legacyStatus) error = %v", err)
	}
	apiStatusPath := filepath.Join(dataDir, "api_status.json")
	if err := os.WriteFile(apiStatusPath, data, 0600); err != nil {
		t.Fatalf("WriteFile(api_status.json) error = %v", err)
	}
	tracker, err := status.NewTracker(dataDir)
	if err != nil {
		t.Fatalf("NewTracker() error = %v", err)
	}

	cfg := config.DefaultConfig()
	cfg.APIKey = "persist-key"
	cfg.DataDir = dataDir
	cfg.SlackWebhooks.Ransomware = config.WebhookConfig{
		Enabled: true,
		URL:     "https://hooks.slack.com/services/T12345678/B12345678/persist-migration",
	}
	s := &Scheduler{
		config:        cfg,
		statusTracker: tracker,
		webhookLocks:  make(map[string]*sync.Mutex),
	}

	// Turn the API status file into a directory so persisting the migration fails.
	if err := os.Remove(apiStatusPath); err != nil {
		t.Fatalf("Remove(api_status.json) error = %v", err)
	}
	if err := os.Mkdir(apiStatusPath, 0700); err != nil {
		t.Fatalf("Mkdir(api_status.json) error = %v", err)
	}

	s.migrateLegacyAPIFetchedItemsForConfig(cfg)

	if entry := findTestLogEntry(hook, "Failed to persist API legacy fetched item migration"); entry == nil {
		t.Fatal("missing warn log for failed legacy migration persistence")
	}
}

func TestProcessAPIRetryQueueSkipRoutes(t *testing.T) {
	t.Run("cancelled context keeps queue", func(t *testing.T) {
		s := newTestScheduler(t)
		s.config.SlackWebhooks.Ransomware = config.WebhookConfig{
			Enabled: true,
			URL:     "https://hooks.slack.test/services/cancelled-retry",
		}
		enqueueAPIRetryPayloadForDestination(t, s, "retry-cancelled", apiDestinationID(status.MessengerSlack), status.MessengerSlack.String())

		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		s.processAPIRetryQueue(ctx, nil)

		if remaining := s.statusTracker.GetQueuedRetryItemsByType(status.RetryItemTypeAPI.String()); len(remaining) != 1 {
			t.Fatalf("remaining retry queue = %d, want 1 after cancellation", len(remaining))
		}
	})

	t.Run("unknown destination is skipped", func(t *testing.T) {
		hook := logtest.NewGlobal()
		defer hook.Reset()

		s := newTestScheduler(t)
		s.config.SlackWebhooks.Ransomware = config.WebhookConfig{
			Enabled: true,
			URL:     "https://hooks.slack.test/services/known-destination",
		}
		enqueueAPIRetryPayloadForDestination(t, s, "retry-unknown", "slack.retired-destination", status.MessengerSlack.String())

		s.processAPIRetryQueue(t.Context(), nil)

		if entry := findTestLogEntry(hook, "Skipping API retry item with unknown destination"); entry == nil {
			t.Fatal("missing warn log for unknown retry destination")
		}
		if remaining := s.statusTracker.GetQueuedRetryItemsByType(status.RetryItemTypeAPI.String()); len(remaining) != 1 {
			t.Fatalf("remaining retry queue = %d, want 1 for unknown destination", len(remaining))
		}
	})

	t.Run("destination without URL is skipped", func(t *testing.T) {
		hook := logtest.NewGlobal()
		defer hook.Reset()

		s := newTestScheduler(t)
		s.config.SlackWebhooks.Ransomware = config.WebhookConfig{Enabled: true, URL: ""}
		enqueueAPIRetryPayloadForDestination(t, s, "retry-no-url", apiDestinationID(status.MessengerSlack), status.MessengerSlack.String())

		s.processAPIRetryQueue(t.Context(), nil)

		if entry := findTestLogEntry(hook, "Skipping API retry item because destination URL is missing"); entry == nil {
			t.Fatal("missing warn log for retry destination without URL")
		}
		if remaining := s.statusTracker.GetQueuedRetryItemsByType(status.RetryItemTypeAPI.String()); len(remaining) != 1 {
			t.Fatalf("remaining retry queue = %d, want 1 for missing URL", len(remaining))
		}
	})
}

func apiRetryJobForTest(entryID, messenger string) apiRetryJob {
	entry := api.RansomwareEntry{
		ID:     entryID,
		Group:  "lockbit",
		Victim: "Retry Job Corp",
	}
	return apiRetryJob{
		item: status.RetryRecord{
			ItemKey:   api.GenerateEntryKey(entry),
			Messenger: messenger,
			ItemType:  status.RetryItemTypeAPI.String(),
		},
		queueKey:      fmt.Sprintf("queue-%s", entryID),
		destinationID: "slack.ransomware",
		webhook:       apiRetryWebhookInfo{url: "https://hooks.slack.test/services/retry-job", enabled: true},
		entry:         entry,
		primaryKey:    api.GenerateEntryKey(entry),
		title:         api.DisplayRansomwareTitle(entry),
	}
}

func TestProcessAPIRetryGroupsStopsGroupWhenContextCancelled(t *testing.T) {
	s := newTestScheduler(t)
	slackSender := &recordingWebhookSender{}
	s.slackWebhookSender = slackSender

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	groups := map[string][]apiRetryJob{
		"group": {apiRetryJobForTest("group-cancel", status.MessengerSlack.String())},
	}

	s.processAPIRetryGroups(ctx, s.config, groups, func() {})

	if got := slackSender.apiCallCount(); got != 0 {
		t.Fatalf("API retry sends = %d, want 0 with cancelled context", got)
	}
}

func TestProcessAPIRetryJobSkipsUnknownMessenger(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()

	s := newTestScheduler(t)
	job := apiRetryJobForTest("unknown-messenger", "matrix")

	if !s.processAPIRetryJob(t.Context(), s.config, job, func() {}) {
		t.Fatal("processAPIRetryJob(unknown messenger) = false, want true (skip and continue)")
	}
	if entry := findTestLogEntry(hook, "Skipping API retry item with unknown messenger"); entry == nil {
		t.Fatal("missing warn log for unknown retry messenger")
	}
}

func TestProcessAPIRetryJobSkipsMissingSender(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()

	s := newTestScheduler(t)
	previousSender := s.slackWebhookSender
	s.slackWebhookSender = nil
	defer func() { s.slackWebhookSender = previousSender }()

	job := apiRetryJobForTest("missing-sender", status.MessengerSlack.String())
	if !s.processAPIRetryJob(t.Context(), s.config, job, func() {}) {
		t.Fatal("processAPIRetryJob(missing sender) = false, want true (skip and continue)")
	}
	if entry := findTestLogEntry(hook, "Skipping API retry item with missing messenger sender"); entry == nil {
		t.Fatal("missing error log for retry messenger without sender")
	}
}

func TestProcessAPIRetryJobStopsWhenCancelledDuringSendDelay(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()

	s := newTestScheduler(t)
	s.config.SlackDelay = time.Hour
	slackSender := &recordingWebhookSender{}
	s.slackWebhookSender = slackSender

	job := apiRetryJobForTest("delay-cancel", status.MessengerSlack.String())
	// Done() call 1: job select, call 2: send-delay select -> cancels there.
	ctx := newDoneCallCancelContext(2)

	if s.processAPIRetryJob(ctx, s.config, job, func() {}) {
		t.Fatal("processAPIRetryJob(delay cancel) = true, want false (stop group)")
	}
	if got := slackSender.apiCallCount(); got != 0 {
		t.Fatalf("API retry sends = %d, want 0 after cancellation during delay", got)
	}
	if entry := findTestLogEntry(hook, "Context cancelled during API retry send delay"); entry == nil {
		t.Fatal("missing warn log for cancelled retry send delay")
	}
}
