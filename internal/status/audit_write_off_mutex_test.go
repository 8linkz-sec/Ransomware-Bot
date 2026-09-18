package status

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	logtest "github.com/sirupsen/logrus/hooks/test"
)

// auditFileStall lets a test stall the audit-file open indefinitely (as if a
// disk/mount were hung) and release it deterministically, without a fixed
// sleep. entered fires the first time a stalled open is attempted, which is
// always after the mutator that produced the write has already released
// t.mutex and (for a converted entry point) called t.auditFlight.reserve --
// so waiting on entered before asserting anything gives a deterministic
// synchronization point instead of a race-prone sleep.
type auditFileStall struct {
	entered chan struct{}
	proceed chan struct{}
	once    sync.Once
}

func stallAuditFileOpen(t *testing.T) *auditFileStall {
	t.Helper()
	s := &auditFileStall{
		entered: make(chan struct{}),
		proceed: make(chan struct{}),
	}
	original := auditFileOpenFunc
	var enteredOnce sync.Once
	auditFileOpenFunc = func(name string, flag int, perm os.FileMode) (*os.File, error) {
		enteredOnce.Do(func() { close(s.entered) })
		<-s.proceed
		return original(name, flag, perm)
	}
	t.Cleanup(func() {
		auditFileOpenFunc = original
	})
	return s
}

func (s *auditFileStall) release() {
	s.once.Do(func() { close(s.proceed) })
}

// waitOrTimeout blocks on done until it fires or timeout elapses, reporting
// which happened.
func waitOrTimeout(done <-chan struct{}, timeout time.Duration) (fired bool) {
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

// TestSavePendingChangesWaitsForInFlightAuditWrite is the durability probe
// (plan section 2.1/4.4): SavePendingChanges must not persist a state file
// reflecting a mutation whose audit line is not yet durable. RED today
// (before the auditFlight barrier exists): SavePendingChanges returns almost
// immediately while the audit write is still stalled.
func TestSavePendingChangesWaitsForInFlightAuditWrite(t *testing.T) {
	tracker, dir := newTestTrackerWithDir(t)

	if !tracker.enqueueRetryForTest("item-1", "discord.ransomware", "discord", "api", "Example", "boom", 5, time.Hour) {
		t.Fatal("EnqueueRetry() did not queue the item, want a queued retry to dead-letter")
	}
	queueKey := makeCompositeKey("item-1", "discord.ransomware")

	stall := stallAuditFileOpen(t)

	mutatorDone := make(chan struct{})
	go func() {
		defer close(mutatorDone)
		if !tracker.DeadLetterRetryQueueEntryWithErrorInfo(queueKey, "final failure", RetryErrorInfo{}) {
			t.Error("DeadLetterRetryQueueEntryWithErrorInfo() = false, want true")
		}
	}()

	// Wait for the mutator to reach its stalled audit write -- by then its
	// state mutation (and, under the fix, its auditFlight reservation) is
	// already in place.
	if !waitOrTimeout(stall.entered, 5*time.Second) {
		t.Fatal("mutator never reached the stalled audit write")
	}

	saveDone := make(chan struct{})
	go func() {
		defer close(saveDone)
		if err := tracker.SavePendingChanges(); err != nil {
			t.Errorf("SavePendingChanges() error = %v", err)
		}
	}()

	returnedEarly := waitOrTimeout(saveDone, 300*time.Millisecond)
	if returnedEarly {
		t.Fatal("SavePendingChanges() returned while the audit write for the dead-letter it just persisted was still stalled -- durability gap")
	}

	stall.release()

	if !waitOrTimeout(mutatorDone, 5*time.Second) {
		t.Fatal("mutator did not complete after the stall was released")
	}
	if !waitOrTimeout(saveDone, 5*time.Second) {
		t.Fatal("SavePendingChanges() did not complete after the stall was released")
	}

	data, err := os.ReadFile(filepath.Join(dir, statusAuditFileName))
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", statusAuditFileName, err)
	}
	if !strings.Contains(string(data), DeliveryAuditOutcomeDeadLettered) {
		t.Fatalf("delivery_audit.jsonl missing the dead-letter line: %s", data)
	}

	retryData, err := os.ReadFile(filepath.Join(dir, statusRetryFileName))
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", statusRetryFileName, err)
	}
	if !strings.Contains(string(retryData), "item-1") {
		t.Fatalf("retry_status.json missing the dead-lettered item: %s", retryData)
	}
}

// TestCleanupOldEntriesDoesNotBlockConcurrentReaderDuringStalledAuditWrite is
// the blocking probe (plan section 1.2): a stalled audit write must not block
// a concurrent reader that needs only t.mutex. RED today: RSSParsedSortState
// stays blocked for the full stall.
func TestCleanupOldEntriesDoesNotBlockConcurrentReaderDuringStalledAuditWrite(t *testing.T) {
	policy := DefaultRetentionPolicy()
	policy.MaxRetryQueueItems = 1
	dir := t.TempDir()
	tracker, err := NewTrackerWithRetention(dir, policy)
	if err != nil {
		t.Fatalf("NewTrackerWithRetention() error = %v", err)
	}

	if !tracker.enqueueRetryForTest("item-old", "discord.ransomware", "discord", "api", "Old", "boom", 5, 24*time.Hour) {
		t.Fatal("EnqueueRetry() for item-old unexpectedly failed")
	}
	if !tracker.enqueueRetryForTest("item-new", "discord.ransomware2", "discord", "api", "New", "boom", 5, 24*time.Hour) {
		t.Fatal("EnqueueRetry() for item-new unexpectedly failed")
	}
	oldTime := formatStatusTimestamp(statusNow().Add(-time.Hour))
	if !tracker.setQueuedRetryTimes("item-old", "api", oldTime, oldTime) {
		t.Fatal("queued retry item missing")
	}

	stall := stallAuditFileOpen(t)

	cleanupDone := make(chan struct{})
	go func() {
		defer close(cleanupDone)
		tracker.CleanupOldEntriesForRSSDestinations([]string{"discord.rss"})
	}()

	if !waitOrTimeout(stall.entered, 5*time.Second) {
		t.Fatal("cleanup never reached the stalled audit write")
	}

	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		tracker.RSSParsedSortState()
	}()

	if !waitOrTimeout(readDone, 1*time.Second) {
		t.Fatal("RSSParsedSortState() stayed blocked while the audit write was stalled")
	}

	stall.release()
	if !waitOrTimeout(cleanupDone, 5*time.Second) {
		t.Fatal("CleanupOldEntriesForRSSDestinations() did not complete after the stall was released")
	}
}

// sixEntryPointCase describes one of the six t.mutex-holding entry points
// that reaches an audit write (plan section 1.1/4.5).
type sixEntryPointCase struct {
	name    string
	setup   func(t *testing.T, tracker *Tracker) // runs unstalled, before the stall begins
	call    func(t *testing.T, tracker *Tracker) // runs stalled, in its own goroutine
	outcome string
}

func sixEntryPointCases() []sixEntryPointCase {
	return []sixEntryPointCase{
		{
			name: "cleanupOldEntries",
			setup: func(t *testing.T, tracker *Tracker) {
				t.Helper()
				if !tracker.enqueueRetryForTest("old-1", "discord.a", "discord", "api", "Old", "boom", 5, 24*time.Hour) {
					t.Fatal("setup EnqueueRetry() failed")
				}
				if !tracker.enqueueRetryForTest("new-1", "discord.b", "discord", "api", "New", "boom", 5, 24*time.Hour) {
					t.Fatal("setup EnqueueRetry() failed")
				}
				oldTime := formatStatusTimestamp(statusNow().Add(-time.Hour))
				if !tracker.setQueuedRetryTimes("old-1", "api", oldTime, oldTime) {
					t.Fatal("setup setQueuedRetryTimes() failed")
				}
			},
			call: func(t *testing.T, tracker *Tracker) {
				t.Helper()
				tracker.CleanupOldEntries()
			},
			outcome: DeliveryAuditOutcomePruned,
		},
		{
			name: "cleanupOldRSSEntries",
			setup: func(t *testing.T, tracker *Tracker) {
				t.Helper()
				if !tracker.enqueueRetryForTest("old-2", "discord.a", "discord", "rss", "Old", "boom", 5, 24*time.Hour) {
					t.Fatal("setup EnqueueRetry() failed")
				}
				if !tracker.enqueueRetryForTest("new-2", "discord.b", "discord", "rss", "New", "boom", 5, 24*time.Hour) {
					t.Fatal("setup EnqueueRetry() failed")
				}
				oldTime := formatStatusTimestamp(statusNow().Add(-time.Hour))
				if !tracker.setQueuedRetryTimes("old-2", "rss", oldTime, oldTime) {
					t.Fatal("setup setQueuedRetryTimes() failed")
				}
			},
			call: func(t *testing.T, tracker *Tracker) {
				t.Helper()
				tracker.CleanupOldEntriesForRSSDestinations([]string{"discord.rss"})
			},
			outcome: DeliveryAuditOutcomePruned,
		},
		{
			name: "EnqueueRetryForDestination",
			call: func(t *testing.T, tracker *Tracker) {
				t.Helper()
				if !tracker.enqueueRetryForTest("item-enqueue", "discord.c", "discord", "api", "Enqueue", "boom", 5, time.Hour) {
					t.Fatal("EnqueueRetry() unexpectedly failed")
				}
			},
			outcome: DeliveryAuditOutcomeRetryQueued,
		},
		{
			name: "RecordRetryQueueFailureWithErrorInfo",
			setup: func(t *testing.T, tracker *Tracker) {
				t.Helper()
				if !tracker.enqueueRetryForTest("item-fail", "discord.d", "discord", "api", "Fail", "boom", 1, 0) {
					t.Fatal("setup EnqueueRetry() failed")
				}
			},
			call: func(t *testing.T, tracker *Tracker) {
				t.Helper()
				queueKey := makeCompositeKey("item-fail", "discord.d")
				if tracker.RecordRetryQueueFailureWithErrorInfo(queueKey, "boom again", RetryErrorInfo{}, 1, 0) {
					t.Fatal("RecordRetryQueueFailureWithErrorInfo() = true, want dead-lettered (false) at max attempts")
				}
			},
			outcome: DeliveryAuditOutcomeDeadLettered,
		},
		{
			name: "DeadLetterRetryQueueEntryWithErrorInfo",
			setup: func(t *testing.T, tracker *Tracker) {
				t.Helper()
				if !tracker.enqueueRetryForTest("item-dl", "discord.e", "discord", "api", "DL", "boom", 5, time.Hour) {
					t.Fatal("setup EnqueueRetry() failed")
				}
			},
			call: func(t *testing.T, tracker *Tracker) {
				t.Helper()
				queueKey := makeCompositeKey("item-dl", "discord.e")
				if !tracker.DeadLetterRetryQueueEntryWithErrorInfo(queueKey, "final", RetryErrorInfo{}) {
					t.Fatal("DeadLetterRetryQueueEntryWithErrorInfo() = false, want true")
				}
			},
			outcome: DeliveryAuditOutcomeDeadLettered,
		},
		{
			name: "MarkRetryDeadLetterForDestinationWithErrorInfo",
			call: func(t *testing.T, tracker *Tracker) {
				t.Helper()
				tracker.MarkRetryDeadLetterForDestinationWithErrorInfo(
					"item-mark", "discord.f", "discord", "api", "Mark", "final", RetryErrorInfo{},
				)
			},
			outcome: DeliveryAuditOutcomeDeadLettered,
		},
	}
}

// TestSixEntryPointsFlushAfterUnlock is the per-call-site proof (plan section
// 7 item 3): for each of the six t.mutex-holding entry points that reaches an
// audit write, a stalled write must not block a concurrent operation that
// only needs t.mutex, and the audit line must land with the expected outcome
// once the stall is released.
func TestSixEntryPointsFlushAfterUnlock(t *testing.T) {
	for _, tc := range sixEntryPointCases() {
		t.Run(tc.name, func(t *testing.T) {
			policy := DefaultRetentionPolicy()
			policy.MaxRetryQueueItems = 1 // lets the two cleanup cases force count-based pruning deterministically
			dir := t.TempDir()
			tracker, err := NewTrackerWithRetention(dir, policy)
			if err != nil {
				t.Fatalf("NewTrackerWithRetention() error = %v", err)
			}
			if tc.setup != nil {
				tc.setup(t, tracker)
			}
			// Setup itself may already have written audit lines (e.g. the
			// queuing calls that build up a case's fixture); only the lines
			// produced by tc.call matter for this assertion.
			if err := os.Remove(filepath.Join(dir, statusAuditFileName)); err != nil && !os.IsNotExist(err) {
				t.Fatalf("Remove(%s) error = %v", statusAuditFileName, err)
			}

			stall := stallAuditFileOpen(t)

			callDone := make(chan struct{})
			go func() {
				defer close(callDone)
				tc.call(t, tracker)
			}()

			if !waitOrTimeout(stall.entered, 5*time.Second) {
				t.Fatal("entry point never reached the stalled audit write")
			}

			readDone := make(chan struct{})
			go func() {
				defer close(readDone)
				tracker.RSSParsedSortState()
			}()
			if !waitOrTimeout(readDone, 1*time.Second) {
				t.Fatal("concurrent RSSParsedSortState() stayed blocked while the audit write was stalled")
			}

			stall.release()
			if !waitOrTimeout(callDone, 5*time.Second) {
				t.Fatal("entry point did not complete after the stall was released")
			}

			data, err := os.ReadFile(filepath.Join(dir, statusAuditFileName))
			if err != nil {
				t.Fatalf("ReadFile(%s) error = %v", statusAuditFileName, err)
			}
			if !strings.Contains(string(data), tc.outcome) {
				t.Fatalf("delivery_audit.jsonl missing outcome %q: %s", tc.outcome, data)
			}
		})
	}
}

// TestFlushDeliveryAuditEventsPreservesBatchOrder pins invariant 7: events
// collected within one top-level call are written in call order.
func TestFlushDeliveryAuditEventsPreservesBatchOrder(t *testing.T) {
	policy := DefaultRetentionPolicy()
	policy.MaxAPISentItems = 1
	policy.MaxRetryQueueItems = 1
	dir := t.TempDir()
	tracker, err := NewTrackerWithRetention(dir, policy)
	if err != nil {
		t.Fatalf("NewTrackerWithRetention() error = %v", err)
	}

	// cleanupOldEntries calls cleanupOldAPISentItems (API pruning) before
	// cleanupRSSAndRetryLocked (retry-queue pruning) -- force both to produce
	// an event in the same call so on-disk order can be checked against that
	// fixed code order (invariant 7).
	tracker.mutex.Lock()
	tracker.apiStatus.SentItems[makeCompositeKey("old-api", "discord.a")] = webhookSentInfo{SentAt: statusNow().Add(-2 * time.Hour)}
	tracker.apiStatus.SentItems[makeCompositeKey("new-api", "discord.a")] = webhookSentInfo{SentAt: statusNow()}
	tracker.mutex.Unlock()

	if !tracker.enqueueRetryForTest("old-batch", "discord.a", "discord", "api", "Old", "boom", 5, 24*time.Hour) {
		t.Fatal("setup EnqueueRetry() failed")
	}
	if !tracker.enqueueRetryForTest("new-batch", "discord.b", "discord", "api", "New", "boom", 5, 24*time.Hour) {
		t.Fatal("setup EnqueueRetry() failed")
	}
	oldTime := formatStatusTimestamp(statusNow().Add(-time.Hour))
	if !tracker.setQueuedRetryTimes("old-batch", "api", oldTime, oldTime) {
		t.Fatal("setup setQueuedRetryTimes() failed")
	}
	// The setup calls above already wrote their own audit lines; only the
	// lines produced by CleanupOldEntries() itself matter for this assertion.
	if err := os.Remove(filepath.Join(dir, statusAuditFileName)); err != nil && !os.IsNotExist(err) {
		t.Fatalf("Remove(%s) error = %v", statusAuditFileName, err)
	}

	tracker.CleanupOldEntries()

	data, err := os.ReadFile(filepath.Join(dir, statusAuditFileName))
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", statusAuditFileName, err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("audit line count = %d, want exactly 2: %s", len(lines), data)
	}

	var first, second DeliveryAuditEvent
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("Unmarshal(first line) error = %v", err)
	}
	if err := json.Unmarshal([]byte(lines[1]), &second); err != nil {
		t.Fatalf("Unmarshal(second line) error = %v", err)
	}
	// cleanupOldAPISentItems runs before cleanupRSSAndRetryLocked in
	// cleanupOldEntries's fixed code order.
	if first.Reason != "api_sent_items_count_pruned" {
		t.Fatalf("first audit line reason = %q, want the API sent-items pruning event (API pruning runs first)", first.Reason)
	}
	if second.Reason != TerminalReasonRetentionPruned {
		t.Fatalf("second audit line reason = %q, want %q (the retry-queue dead-letter event)", second.Reason, TerminalReasonRetentionPruned)
	}
}

// TestFlushDeliveryAuditEventsLogsAndContinuesOnWriteError pins invariant 4:
// a failed audit write is logged at WARN, dropped, and does not stop the
// mutating call from returning its normal result or leak the auditFlight
// reservation (a later SavePendingChanges must not hang on it).
func TestFlushDeliveryAuditEventsLogsAndContinuesOnWriteError(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()

	tracker, dir := newTestTrackerWithDir(t)
	if err := os.Mkdir(filepath.Join(dir, statusAuditFileName), 0700); err != nil {
		t.Fatalf("Mkdir(audit blocker) error = %v", err)
	}

	if !tracker.enqueueRetryForTest("item-err", "discord.a", "discord", "api", "Err", "boom", 5, time.Hour) {
		t.Fatal("EnqueueRetry() unexpectedly failed")
	}

	found := false
	for _, entry := range hook.AllEntries() {
		if entry.Message == "Failed to append delivery audit event" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("missing warn log for failed audit append")
	}

	// A leaked reservation would hang SavePendingChanges forever; bound the
	// wait so a regression fails the test instead of the test run.
	saveDone := make(chan struct{})
	go func() {
		defer close(saveDone)
		if err := tracker.SavePendingChanges(); err != nil {
			t.Errorf("SavePendingChanges() error = %v", err)
		}
	}()
	if !waitOrTimeout(saveDone, 5*time.Second) {
		t.Fatal("SavePendingChanges() hung after a failed audit write -- auditFlight reservation leaked")
	}
}

// TestAppendDeliveryAuditEventNoopForInMemoryTracker pins
// appendDeliveryAuditEvent's t.inMemory short-circuit: RecordDeliveryAuditEvent
// on an in-memory tracker (dry-run, --healthcheck-style read paths) must
// never attempt any file I/O and never error. With an in-memory tracker's
// dataDir == "", filepath.Join("", statusAuditFileName) resolves to a
// relative path under the process's current working directory, so the
// assertion this test's comment used to call impossible is possible after
// all: stat that path (cwd + statusAuditFileName) before and after. The test
// chdirs into a fresh t.TempDir() first so that working directory -- and the
// pre-test/cleanup Remove calls that probe it -- is never the package
// directory inside the repository checkout.
func TestAppendDeliveryAuditEventNoopForInMemoryTracker(t *testing.T) {
	t.Chdir(t.TempDir())

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	stray := filepath.Join(cwd, statusAuditFileName)
	if err := os.Remove(stray); err != nil && !os.IsNotExist(err) {
		t.Fatalf("pre-test Remove(%s) error = %v", stray, err)
	}
	t.Cleanup(func() { _ = os.Remove(stray) })

	tracker := NewMemoryTracker()

	tracker.RecordDeliveryAuditEvent(DeliveryAuditEvent{
		EventType: DeliveryAuditEventDeliveryState,
		ItemKey:   "item-1",
		Outcome:   DeliveryAuditOutcomeDelivered,
	})

	if _, err := os.Stat(stray); err == nil {
		t.Fatalf("RecordDeliveryAuditEvent on an in-memory tracker wrote a stray %s into the working directory", statusAuditFileName)
	} else if !os.IsNotExist(err) {
		t.Fatalf("Stat(%s) error = %v", stray, err)
	}
}

// TestInMemoryTrackerWritesNoAuditFileThroughRetryPaths pins the same
// t.inMemory guard (F2, review 2026-09-03) driven through the retry and
// dead-letter entry points, not just RecordDeliveryAuditEvent directly: a
// NewMemoryTracker() (--dry-run, --healthcheck) run through
// EnqueueRetryForDestination and MarkRetryDeadLetterForDestinationWithErrorInfo
// must not write a stray delivery_audit.jsonl into the process working
// directory. RED against the unmodified t.inMemory guard removed: a
// filepath.Join("", statusAuditFileName) relative path resolves under cwd
// and the file appears there. The test chdirs into a fresh t.TempDir() first
// so that working directory -- and the pre-test/cleanup Remove calls that
// probe it -- is never the package directory inside the repository checkout.
func TestInMemoryTrackerWritesNoAuditFileThroughRetryPaths(t *testing.T) {
	t.Chdir(t.TempDir())

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	stray := filepath.Join(cwd, statusAuditFileName)
	if err := os.Remove(stray); err != nil && !os.IsNotExist(err) {
		t.Fatalf("pre-test Remove(%s) error = %v", stray, err)
	}
	t.Cleanup(func() { _ = os.Remove(stray) })

	tracker := NewMemoryTracker()
	if !tracker.enqueueRetryForTest("mem-1", "discord.a", "discord", "api", "M", "boom", 5, time.Hour) {
		t.Fatal("EnqueueRetry() unexpectedly failed")
	}
	tracker.MarkRetryDeadLetterForDestinationWithErrorInfo("mem-2", "discord.a", "discord", "api", "M", "final", RetryErrorInfo{})

	if _, err := os.Stat(stray); err == nil {
		t.Fatalf("in-memory tracker wrote a stray %s into the working directory", statusAuditFileName)
	} else if !os.IsNotExist(err) {
		t.Fatalf("Stat(%s) error = %v", stray, err)
	}
}

// TestAppendNormalizedDeliveryAuditEventReturnsWriteError pins the write-error
// branch of appendNormalizedDeliveryAuditEvent (invariant 4, the same
// log-and-continue contract as the open-error branch already pinned by
// TestFlushDeliveryAuditEventsLogsAndContinuesOnWriteError). auditFileOpenFunc
// hands back a file it has already closed, so the open itself succeeds but
// the subsequent Write fails deterministically -- without needing OS-level
// fault injection.
func TestAppendNormalizedDeliveryAuditEventReturnsWriteError(t *testing.T) {
	tracker, _ := newTestTrackerWithDir(t)

	original := auditFileOpenFunc
	auditFileOpenFunc = func(name string, flag int, perm os.FileMode) (*os.File, error) {
		file, err := original(name, flag, perm)
		if err != nil {
			return nil, err
		}
		if closeErr := file.Close(); closeErr != nil {
			t.Fatalf("pre-close(audit file) error = %v", closeErr)
		}
		return file, nil
	}
	t.Cleanup(func() { auditFileOpenFunc = original })

	err := tracker.appendNormalizedDeliveryAuditEvent(normalizeDeliveryAuditEvent(DeliveryAuditEvent{
		EventType: DeliveryAuditEventDeliveryState,
		ItemKey:   "item-1",
		Outcome:   DeliveryAuditOutcomeDelivered,
	}))
	if err == nil {
		t.Fatal("appendNormalizedDeliveryAuditEvent() error = nil, want a write error from the pre-closed file")
	}
	if !strings.Contains(err.Error(), "write delivery audit file") {
		t.Fatalf("appendNormalizedDeliveryAuditEvent() error = %q, want it to name the write step", err)
	}
}

// TestFlushDeliveryAuditEventsSkipsEmptyEventType pins
// flushDeliveryAuditEvents' defensive skip: an event with no EventType (which
// no current call site produces -- every accumulator either checks count > 0
// before calling normalizeDeliveryAuditEvent, as recordCleanupAudit does, or
// sets EventType explicitly) must never reach the disk, and must still count
// toward the auditFlight release so a batch mixing a skippable and a real
// event does not leak a reservation.
func TestFlushDeliveryAuditEventsSkipsEmptyEventType(t *testing.T) {
	tracker, dir := newTestTrackerWithDir(t)

	tracker.auditFlight.reserve(2)
	tracker.flushDeliveryAuditEvents([]DeliveryAuditEvent{
		{}, // no EventType -- must be skipped, not written
		normalizeDeliveryAuditEvent(DeliveryAuditEvent{
			EventType: DeliveryAuditEventDeliveryState,
			ItemKey:   "item-1",
			Outcome:   DeliveryAuditOutcomeDelivered,
		}),
	})

	if !tracker.auditFlight.isZero() {
		t.Fatal("auditFlight did not reach zero after flushDeliveryAuditEvents returned")
	}

	data, err := os.ReadFile(filepath.Join(dir, statusAuditFileName))
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", statusAuditFileName, err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 {
		t.Fatalf("audit line count = %d, want exactly 1 (the empty-EventType event must be skipped): %s", len(lines), data)
	}
}

// TestAuditFlightReserveReleaseWaitForZero is a unit-level test of the
// auditFlight primitive itself (plan section 7 item 6).
func TestAuditFlightReserveReleaseWaitForZero(t *testing.T) {
	var a auditFlight

	// n <= 0 is a no-op for both reserve and release (mirrors
	// flushDeliveryAuditEvents' own len(events) == 0 guard).
	a.reserve(0)
	a.reserve(-1)
	a.release(0)
	a.release(-1)
	if !a.isZero() {
		t.Fatal("isZero() = false after only non-positive reserve/release calls")
	}

	// Zero value: nothing reserved, waitForZero returns immediately.
	waitDone := make(chan struct{})
	go func() {
		defer close(waitDone)
		a.waitForZero()
	}()
	if !waitOrTimeout(waitDone, 1*time.Second) {
		t.Fatal("waitForZero() blocked on a fresh zero-value auditFlight")
	}

	a.reserve(3)
	if a.isZero() {
		t.Fatal("isZero() = true after reserve(3)")
	}

	waitDone = make(chan struct{})
	go func() {
		defer close(waitDone)
		a.waitForZero()
	}()
	if waitOrTimeout(waitDone, 200*time.Millisecond) {
		t.Fatal("waitForZero() returned before all 3 reservations were released")
	}

	a.release(1)
	a.release(1)
	if waitOrTimeout(waitDone, 200*time.Millisecond) {
		t.Fatal("waitForZero() returned after only 2 of 3 reservations were released")
	}
	if a.isZero() {
		t.Fatal("isZero() = true with 1 reservation still outstanding")
	}

	a.release(1)
	if !waitOrTimeout(waitDone, 1*time.Second) {
		t.Fatal("waitForZero() did not return after the final reservation was released")
	}
	if !a.isZero() {
		t.Fatal("isZero() = false after all reservations released")
	}

	// A fresh generation after a full drain blocks waitForZero again.
	a.reserve(1)
	waitDone = make(chan struct{})
	go func() {
		defer close(waitDone)
		a.waitForZero()
	}()
	if waitOrTimeout(waitDone, 200*time.Millisecond) {
		t.Fatal("waitForZero() returned immediately for a fresh generation")
	}
	a.release(1)
	if !waitOrTimeout(waitDone, 1*time.Second) {
		t.Fatal("waitForZero() did not return for the fresh generation after release")
	}
}

// TestAuditFlightReleaseGuardsAgainstNegativeCount pins the LOW guard from the
// 2026-09-03 review: a call-site bug that releases more than it reserved must
// not leave auditFlight permanently non-zero (which would deadlock
// SavePendingChanges forever).
func TestAuditFlightReleaseGuardsAgainstNegativeCount(t *testing.T) {
	var a auditFlight

	a.reserve(1)
	a.release(1)
	a.release(1) // buggy extra release -- must not panic or corrupt state

	if !a.isZero() {
		t.Fatal("isZero() = false after a buggy extra release, want the count clamped at zero")
	}

	// The primitive must still work correctly for the next generation.
	a.reserve(2)
	if a.isZero() {
		t.Fatal("isZero() = true after reserve(2) following the guarded release")
	}
	a.release(2)
	if !a.isZero() {
		t.Fatal("isZero() = false after releasing the next generation's reservations")
	}
}

// TestAuditFlightReleaseGuardJumpsStraightPastZero pins the F3 fix (review
// 2026-09-03, LOW): a single over-sized release that jumps the count straight
// past zero -- reserve(1); release(2), which goes 1 -> -1 without ever being
// exactly 0 -- must still close the generation channel. Before the fix the
// close-on-zero check ran before the negative clamp, so this exact sequence
// left count == 0 (isZero() == true) but the channel from the reserve(1)
// still open, wedging waitForZero() (and therefore SavePendingChanges)
// forever. RED against the pre-fix ordering (waitForZero times out); GREEN
// against the fix (waitForZero returns promptly).
func TestAuditFlightReleaseGuardJumpsStraightPastZero(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()

	var a auditFlight

	a.reserve(1)
	a.release(2) // buggy call site: released more than it reserved, in one call, skipping over count == 0

	if !a.isZero() {
		t.Fatal("isZero() = false after the clamp, want the count clamped at zero")
	}

	found := false
	for _, entry := range hook.AllEntries() {
		if entry.Message == "auditFlight: release count went negative; a call site released more audit events than it reserved" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("missing ERROR log for the negative release")
	}

	waitDone := make(chan struct{})
	go func() {
		defer close(waitDone)
		a.waitForZero()
	}()
	if !waitOrTimeout(waitDone, 2*time.Second) {
		t.Fatal("waitForZero() blocked forever although isZero() == true -- the stale generation channel from reserve(1) was never closed")
	}

	// The primitive must still work correctly for the next generation.
	a.reserve(1)
	if a.isZero() {
		t.Fatal("isZero() = true after reserve(1) following the jump-past-zero release")
	}
	a.release(1)
	if !a.isZero() {
		t.Fatal("isZero() = false after releasing the next generation's reservation")
	}
}

// TestSavePendingChangesRechecksIsZeroAfterAcquiringMutex pins the barrier
// loop's isZero() recheck inside SavePendingChanges (F1, review 2026-09-03):
// waitForZero() and t.mutex.Lock() are two separate steps, so a new
// reservation can appear in the window between them. Without the recheck,
// SavePendingChanges would snapshot and persist state while that new
// reservation's audit write is still outstanding -- exactly the durability
// gap this whole barrier exists to close, and exactly what removing the
// `if t.auditFlight.isZero() { break }` check (keeping only
// `t.auditFlight.waitForZero(); t.mutex.Lock()`) reopens.
//
// This is pinned deterministically, with no scheduling races, using two
// reservations in strict sequence instead of relying on winning a race
// against the saver goroutine's first statement:
//
//  1. Reserve an audit write BEFORE starting SavePendingChanges. Because this
//     completes before the goroutine is even created, the saver's first
//     waitForZero() call is guaranteed (Go memory model: everything before a
//     `go` statement is visible to the new goroutine) to observe the open
//     channel from that reservation and block on it -- it cannot yet have
//     reached t.mutex.Lock().
//  2. Hold t.mutex before starting the goroutine too, so once the first
//     reservation is released and the saver's waitForZero() unblocks, its
//     t.mutex.Lock() call is guaranteed to block on this test's own lock,
//     regardless of scheduling.
//  3. While the saver is provably parked on that Lock() call, reserve a
//     second audit write and then unlock. The Unlock()-then-Lock() edge
//     guarantees the woken saver's isZero() check observes the new
//     reservation, so it is guaranteed to take the false branch.
//
// RED against the mutation that drops the isZero() recheck: SavePendingChanges
// returns almost immediately after acquiring t.mutex instead of re-parking on
// the second reservation.
func TestSavePendingChangesRechecksIsZeroAfterAcquiringMutex(t *testing.T) {
	tracker, _ := newTestTrackerWithDir(t)

	// Step 1: force the saver's first waitForZero() to block, deterministically.
	tracker.auditFlight.reserve(1)
	tracker.mutex.Lock()

	saveDone := make(chan struct{})
	var saveErr error
	go func() {
		defer close(saveDone)
		saveErr = tracker.SavePendingChanges()
	}()

	if waitOrTimeout(saveDone, 300*time.Millisecond) {
		t.Fatal("SavePendingChanges() returned before its first reservation was even released -- setup is broken")
	}

	// Unblock the first waitForZero(); the saver now tries t.mutex.Lock(),
	// which is guaranteed to block since this test still holds it.
	tracker.auditFlight.release(1)

	// Step 3: reserve a second audit write while the saver is provably
	// parked on Lock(), then unlock -- the woken saver must observe it.
	tracker.auditFlight.reserve(1)
	tracker.mutex.Unlock()

	if waitOrTimeout(saveDone, 300*time.Millisecond) {
		t.Fatal("SavePendingChanges() returned right after acquiring t.mutex despite a second outstanding audit reservation -- the isZero() recheck did not re-park the barrier loop")
	}

	tracker.auditFlight.release(1)

	if !waitOrTimeout(saveDone, 5*time.Second) {
		t.Fatal("SavePendingChanges() did not complete after the second reservation was released")
	}
	if saveErr != nil {
		t.Fatalf("SavePendingChanges() error = %v", saveErr)
	}
}

// TestEnqueueRetryForDestinationDoesNotOverReleaseAuditFlight pins the
// reserve/release pairing in EnqueueRetryForDestination's deferred closure
// (F1, review 2026-09-03). flushDeliveryAuditEvents unconditionally releases
// len(events) audit reservations (tracker.go); if the entry point's own
// t.auditFlight.reserve(len(events)) call were ever dropped, every normal
// EnqueueRetryForDestination call would over-release and silently trip the
// negative-count guard (F3) -- silently, because the guard clamps the count
// back to zero instead of panicking or otherwise failing loudly. Assert the
// absence of that ERROR during ordinary operation, using the logtest hook
// already used elsewhere in this file, instead of relying on a crash or a
// hang to surface a dropped reserve() call.
func TestEnqueueRetryForDestinationDoesNotOverReleaseAuditFlight(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()

	tracker, _ := newTestTrackerWithDir(t)
	if !tracker.enqueueRetryForTest("item-1", "discord.a", "discord", "api", "Item", "boom", 5, time.Hour) {
		t.Fatal("EnqueueRetry() unexpectedly failed")
	}

	for _, entry := range hook.AllEntries() {
		if entry.Message == "auditFlight: release count went negative; a call site released more audit events than it reserved" {
			t.Fatalf("EnqueueRetryForDestination over-released auditFlight (fields: %v) -- its reserve(len(events)) call was dropped", entry.Data)
		}
	}
}

// TestAuditWriteOffMutexConcurrency is the race probe (plan section 6/7 item
// 7): concurrent mutators plus a concurrent SavePendingChanges caller must
// never interleave or corrupt delivery_audit.jsonl. Run with -race -count=5.
func TestAuditWriteOffMutexConcurrency(t *testing.T) {
	const goroutines = 40
	tracker, dir := newTestTrackerWithDir(t)

	var wg sync.WaitGroup
	stopSaver := make(chan struct{})
	var saverWG sync.WaitGroup
	saverWG.Add(1)
	go func() {
		defer saverWG.Done()
		for {
			select {
			case <-stopSaver:
				_ = tracker.SavePendingChanges()
				return
			default:
				_ = tracker.SavePendingChanges()
			}
		}
	}()

	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(i int) {
			defer wg.Done()
			itemKey := "race-item"
			destinationID := fmt.Sprintf("discord.%02d", i)
			if !tracker.enqueueRetryForTest(itemKey, destinationID, "discord", "api", "Race", "boom", 1, 0) {
				return
			}
			queueKey := makeCompositeKey(itemKey, destinationID)
			tracker.DeadLetterRetryQueueEntryWithErrorInfo(queueKey, "final", RetryErrorInfo{})
		}(i)
	}
	wg.Wait()
	close(stopSaver)

	// Bound both waits the same way TestFlushDeliveryAuditEventsLogsAndContinuesOnWriteError
	// does: a leaked auditFlight reservation would otherwise hang this test
	// until the go-test timeout (10 minutes in CI) instead of failing fast
	// with a clear message (F7, review 2026-09-03).
	saverDone := make(chan struct{})
	go func() {
		defer close(saverDone)
		saverWG.Wait()
	}()
	if !waitOrTimeout(saverDone, 10*time.Second) {
		t.Fatal("background saver loop's final SavePendingChanges() did not complete -- auditFlight reservation leaked")
	}

	finalSaveDone := make(chan struct{})
	var finalSaveErr error
	go func() {
		defer close(finalSaveDone)
		finalSaveErr = tracker.SavePendingChanges()
	}()
	if !waitOrTimeout(finalSaveDone, 10*time.Second) {
		t.Fatal("final SavePendingChanges() did not complete -- auditFlight reservation leaked")
	}
	if finalSaveErr != nil {
		t.Fatalf("final SavePendingChanges() error = %v", finalSaveErr)
	}

	data, err := os.ReadFile(filepath.Join(dir, statusAuditFileName))
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", statusAuditFileName, err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")

	queuedCount := 0
	deadLetteredCount := 0
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var event DeliveryAuditEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("Unmarshal(audit line) error = %v: %s", err, line)
		}
		switch event.Outcome {
		case DeliveryAuditOutcomeRetryQueued:
			queuedCount++
		case DeliveryAuditOutcomeDeadLettered:
			deadLetteredCount++
		}
	}

	if queuedCount != goroutines {
		t.Fatalf("retry_queued audit line count = %d, want %d", queuedCount, goroutines)
	}
	if deadLetteredCount != goroutines {
		t.Fatalf("dead_lettered audit line count = %d, want %d", deadLetteredCount, goroutines)
	}
}

// TestAuditEntryPointPanicReleasesMutex is the panic-safety proof for review
// Finding A (HIGH, 2026-09-03): every one of the six converted entry points
// must release t.mutex even when its own body panics after t.mutex.Lock().
// RED against the plan's originally-proposed manual per-branch unlocks: the
// mutex stays locked after a mid-function panic. GREEN against the deferred
// single-closure fix.
func TestAuditEntryPointPanicReleasesMutex(t *testing.T) {
	cases := []struct {
		name string
		call func(tracker *Tracker)
	}{
		{"cleanupOldEntries", func(tracker *Tracker) { tracker.CleanupOldEntries() }},
		{"cleanupOldRSSEntries", func(tracker *Tracker) {
			tracker.CleanupOldEntriesForRSSDestinations([]string{"discord.rss"})
		}},
		{"EnqueueRetryForDestination", func(tracker *Tracker) {
			tracker.enqueueRetryForTest("item-panic", "discord.a", "discord", "api", "Panic", "boom", 5, time.Hour)
		}},
		{"RecordRetryQueueFailureWithErrorInfo", func(tracker *Tracker) {
			tracker.RecordRetryQueueFailureWithErrorInfo("missing-key", "boom", RetryErrorInfo{}, 1, 0)
		}},
		{"DeadLetterRetryQueueEntryWithErrorInfo", func(tracker *Tracker) {
			tracker.DeadLetterRetryQueueEntryWithErrorInfo("missing-key", "boom", RetryErrorInfo{})
		}},
		{"MarkRetryDeadLetterForDestinationWithErrorInfo", func(tracker *Tracker) {
			tracker.MarkRetryDeadLetterForDestinationWithErrorInfo(
				"item-panic", "discord.a", "discord", "api", "Panic", "boom", RetryErrorInfo{},
			)
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tracker := newTestTracker(t)

			original := testAuditEntryPointPanicHook
			testAuditEntryPointPanicHook = func() { panic("injected panic for Finding A proof") }
			t.Cleanup(func() { testAuditEntryPointPanicHook = original })

			func() {
				defer func() {
					if r := recover(); r == nil {
						t.Fatal("call did not panic; the injected hook did not fire")
					}
				}()
				tc.call(tracker)
			}()

			testAuditEntryPointPanicHook = nil

			readDone := make(chan struct{})
			go func() {
				defer close(readDone)
				tracker.RSSParsedSortState()
			}()
			if !waitOrTimeout(readDone, 1*time.Second) {
				t.Fatal("t.mutex stayed locked after a recovered panic inside the entry point")
			}
		})
	}
}
