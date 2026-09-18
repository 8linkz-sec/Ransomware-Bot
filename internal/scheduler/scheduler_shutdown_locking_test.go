package scheduler

import (
	"sync"
	"testing"
	"time"

	logtest "github.com/sirupsen/logrus/hooks/test"
)

// TestCloseSchedulerResourcesFieldRace pins Finding 6: closeSchedulerResources
// used to nil s.apiClient (and the two webhook senders) with no lock at all,
// while checkAPIOnce reads s.apiClient from inside its own apiMu-guarded
// critical section. A lock held on only one side of a shared field is not
// synchronization. This drives the exact production call sites --
// checkAPIOnce's apiMu.TryLock()-guarded read and closeSchedulerResources'
// write -- concurrently and unsynchronized, many times, so -race observes the
// conflict. RED (fails under -race) before the TryLock fix; GREEN after.
func TestCloseSchedulerResourcesFieldRace(t *testing.T) {
	s := newTestScheduler(t)

	const iterations = 3000
	var wg sync.WaitGroup

	// Goroutine A: exactly checkAPIOnce's own locking pattern around the
	// field it reads at scheduler.go:1288 (s.apiClient.GetLatestEntries),
	// isolated from HTTP timing so the loop runs as fast as possible and
	// maximises the number of interleavings -race can observe.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			if s.apiMu.TryLock() {
				if s.apiClient != nil {
					_ = s.apiClient
				}
				s.apiMu.Unlock()
			}
		}
	}()

	// Goroutine B: the real closeSchedulerResources, repeatedly, restoring a
	// fresh client/senders after each call so goroutine A keeps finding a
	// non-nil field to read. The restore itself takes apiMu -- exactly what
	// any well-behaved writer does -- so the only unsynchronized write this
	// test can observe is the one inside closeSchedulerResources on the
	// pre-fix code (which took no lock at all before touching s.apiClient).
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			_ = s.closeSchedulerResources()
			s.apiMu.Lock()
			s.apiClient = &recordingAPIClient{}
			s.apiMu.Unlock()
			s.rssMu.Lock()
			s.discordWebhookSender = &recordingWebhookSender{}
			s.slackWebhookSender = &recordingWebhookSender{}
			s.rssMu.Unlock()
		}
	}()

	wg.Wait()
}

// TestCloseSchedulerResourcesLeavesResourcesOpenWhenAPICheckStillRunning pins
// the TryLock contract deterministically (no timing): while apiMu is held
// (simulating a stuck checkAPIOnce), closeSchedulerResources must return
// promptly without blocking, must NOT nil s.apiClient, and must log a WARN
// naming the reason. Also covers rssMu and the two webhook senders, which are
// gated by BOTH locks together (see the comment in closeSchedulerResources).
func TestCloseSchedulerResourcesLeavesResourcesOpenWhenAPICheckStillRunning(t *testing.T) {
	s := newTestScheduler(t)
	apiClient := &recordingAPIClient{}
	discordSender := &recordingWebhookSender{}
	slackSender := &recordingWebhookSender{}
	s.apiClient = apiClient
	s.discordWebhookSender = discordSender
	s.slackWebhookSender = slackSender

	hook := logtest.NewGlobal()
	defer hook.Reset()

	s.apiMu.Lock()
	defer s.apiMu.Unlock()

	done := make(chan error, 1)
	go func() { done <- s.closeSchedulerResources() }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("closeSchedulerResources() error = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("closeSchedulerResources() blocked while apiMu was held -- TryLock expected, not Lock")
	}

	if s.apiClient != apiClient {
		t.Fatalf("apiClient = %#v, want unchanged (%#v) while an API check is still running", s.apiClient, apiClient)
	}
	if s.discordWebhookSender != discordSender {
		t.Fatalf("discordWebhookSender = %#v, want unchanged while apiMu is held (gated by both locks)", s.discordWebhookSender)
	}
	if s.slackWebhookSender != slackSender {
		t.Fatalf("slackWebhookSender = %#v, want unchanged while apiMu is held (gated by both locks)", s.slackWebhookSender)
	}

	found := false
	for _, entry := range hook.AllEntries() {
		if entry.Message == "closeSchedulerResources: an API check is still in progress; leaving the API client open" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected a WARN naming that an API check is still in progress")
	}
}

// TestCloseSchedulerResourcesLeavesSendersOpenWhenRSSCheckStillRunning mirrors
// the previous test for rssMu: the two webhook senders are gated by BOTH
// apiMu and rssMu (both delivery paths read them), so holding only rssMu must
// also leave them open, while apiClient (gated by apiMu alone) is still
// closed normally.
func TestCloseSchedulerResourcesLeavesSendersOpenWhenRSSCheckStillRunning(t *testing.T) {
	s := newTestScheduler(t)
	apiClient := &recordingAPIClient{}
	discordSender := &recordingWebhookSender{}
	slackSender := &recordingWebhookSender{}
	s.apiClient = apiClient
	s.discordWebhookSender = discordSender
	s.slackWebhookSender = slackSender

	hook := logtest.NewGlobal()
	defer hook.Reset()

	s.rssMu.Lock()
	defer s.rssMu.Unlock()

	done := make(chan error, 1)
	go func() { done <- s.closeSchedulerResources() }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("closeSchedulerResources() error = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("closeSchedulerResources() blocked while rssMu was held -- TryLock expected, not Lock")
	}

	if s.apiClient != nil {
		t.Fatalf("apiClient = %#v, want nil (apiMu was free)", s.apiClient)
	}
	if s.discordWebhookSender != discordSender {
		t.Fatalf("discordWebhookSender = %#v, want unchanged while rssMu is held", s.discordWebhookSender)
	}
	if s.slackWebhookSender != slackSender {
		t.Fatalf("slackWebhookSender = %#v, want unchanged while rssMu is held", s.slackWebhookSender)
	}

	found := false
	for _, entry := range hook.AllEntries() {
		if entry.Message == "closeSchedulerResources: an RSS check is still in progress; leaving the shared webhook senders open" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected a WARN naming that an RSS check is still in progress")
	}
}

// TestCloseSchedulerResourcesClosesAllResourcesWhenNoCheckIsRunning is the
// positive case: with neither lock held, all resources are closed and nil'd,
// exactly as before this fix.
func TestCloseSchedulerResourcesClosesAllResourcesWhenNoCheckIsRunning(t *testing.T) {
	s := newTestScheduler(t)
	apiClient := &recordingAPIClient{}
	discordSender := &recordingWebhookSender{}
	slackSender := &recordingWebhookSender{}
	s.apiClient = apiClient
	s.discordWebhookSender = discordSender
	s.slackWebhookSender = slackSender

	if err := s.closeSchedulerResources(); err != nil {
		t.Fatalf("closeSchedulerResources() error = %v, want nil", err)
	}

	if s.apiClient != nil {
		t.Fatalf("apiClient = %#v, want nil", s.apiClient)
	}
	if s.discordWebhookSender != nil {
		t.Fatalf("discordWebhookSender = %#v, want nil", s.discordWebhookSender)
	}
	if s.slackWebhookSender != nil {
		t.Fatalf("slackWebhookSender = %#v, want nil", s.slackWebhookSender)
	}
	if !apiClient.closed {
		t.Fatal("apiClient.closed = false, want true")
	}
}

// TestApplyReloadedConfigWritesManifestWithoutAPIOrRSSLocksOnWebhookOnlyChange
// pins Finding 7: applyReloadedConfig's destination-manifest write does NOT
// need apiMu/rssMu for a webhook-only reload (neither replacement client nor
// replacement parser is built, so neither lock is taken). If the manifest
// write genuinely needed either lock, holding both in the test goroutine
// while applyReloadedConfig runs on another goroutine would deadlock (Go
// mutexes are not reentrant, and the two goroutines never coordinate
// release). It does not deadlock.
func TestApplyReloadedConfigWritesManifestWithoutAPIOrRSSLocksOnWebhookOnlyChange(t *testing.T) {
	s := newTestScheduler(t)

	// Webhook-only change, derived from the live config by shallow copy so
	// every api_*/rss_* knob is byte-identical to oldCfg: neither
	// applyReloadedConfig replacement branch builds, and neither lock is
	// taken.
	oldCfg := s.getConfig()
	newCfgValue := *oldCfg
	newCfgValue.DiscordWebhooks.Ransomware.URLs = append(
		append([]string(nil), oldCfg.DiscordWebhooks.Ransomware.URLs...),
		"https://discord.com/api/webhooks/999999999999999999/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	)
	newCfg := &newCfgValue

	s.apiMu.Lock()
	defer s.apiMu.Unlock()
	s.rssMu.Lock()
	defer s.rssMu.Unlock()

	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := s.applyReloadedConfig(newCfg); err != nil {
			t.Errorf("applyReloadedConfig() error = %v, want nil", err)
		}
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("applyReloadedConfig deadlocked while the test goroutine held apiMu and rssMu -- the manifest write needed a lock it does not take")
	}
}
