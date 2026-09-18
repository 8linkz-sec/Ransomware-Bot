package scheduler

import (
	"fmt"
	"strings"
	"syscall"
	"testing"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/config"
	"github.com/8linkz-sec/Ransomware-News-Bot/internal/status"

	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

// syntheticDataDirLockError mimics exactly what status.AcquireDataDirLock
// returns on a filesystem that does not support advisory locking at all
// (ENOLCK/EINVAL): the generic wrap branch, "lock data_dir lock %q: %w",
// which preserves the underlying errno for classification via errors.Is.
func syntheticDataDirLockError(errno syscall.Errno) error {
	return fmt.Errorf("lock data_dir lock %q: %w", "/data/.ransomware-bot.lock", errno)
}

// TestDataDirLockUnsupportedIsDistinctFromAlreadyLocked is a compile/behaviour
// pin at the exact grep site scheduler.go's dataDirAlreadyLocked uses: a
// lock-unsupported error must classify as dataDirLockUnsupported and must NOT
// also classify as dataDirAlreadyLocked (which is fatal), and vice versa.
func TestDataDirLockUnsupportedIsDistinctFromAlreadyLocked(t *testing.T) {
	for _, errno := range []syscall.Errno{syscall.ENOLCK, syscall.EINVAL} {
		unsupportedErr := syntheticDataDirLockError(errno)
		if !dataDirLockUnsupported(unsupportedErr) {
			t.Fatalf("dataDirLockUnsupported(%v) = false, want true", unsupportedErr)
		}
		if dataDirAlreadyLocked(unsupportedErr) {
			t.Fatalf("dataDirAlreadyLocked(%v) = true, want false "+
				"(this would make newSchedulerComponents refuse to start, the exact behaviour the operator rejected)",
				unsupportedErr)
		}
	}

	alreadyLockedErr := fmt.Errorf(
		"data_dir %q is already locked by another ransomware-bot process (%s is locked)",
		"/data", "/data/.ransomware-bot.lock",
	)
	if !dataDirAlreadyLocked(alreadyLockedErr) {
		t.Fatalf("dataDirAlreadyLocked(%v) = false, want true", alreadyLockedErr)
	}
	if dataDirLockUnsupported(alreadyLockedErr) {
		t.Fatalf("dataDirLockUnsupported(%v) = true, want false (contention is not the unsupported-filesystem condition)", alreadyLockedErr)
	}
}

// TestNewSchedulerComponentsWarnsAndStartsWhenDataDirLockingUnsupported drives
// the full production path (New -> newSchedulerComponents) with the
// acquireDataDirLockFunc seam forced to the ENOLCK/EINVAL condition, and
// checks every part of the operator's decision: the bot still starts (no
// error, no in-memory-only silent fallback warning instead of the prominent
// one), no real data_dir lock is held (proving the in-memory tracker path was
// taken, same as today's generic-error branch), and the single prominent WARN
// names the path and carries the fixed, reviewed message text.
func TestNewSchedulerComponentsWarnsAndStartsWhenDataDirLockingUnsupported(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()

	original := acquireDataDirLockFunc
	acquireDataDirLockFunc = func(string) (*status.DataDirLock, error) {
		return nil, syntheticDataDirLockError(syscall.ENOLCK)
	}
	t.Cleanup(func() { acquireDataDirLockFunc = original })

	cfg := config.DefaultConfig()
	cfg.APIKey = "test-key"
	dataDir := t.TempDir()
	cfg.DataDir = dataDir

	s, err := New(cfg, t.TempDir(), false)
	if err != nil {
		t.Fatalf("New() error = %v, want the bot to keep starting", err)
	}
	t.Cleanup(func() {
		if err := s.Stop(); err != nil {
			t.Errorf("Stop() error = %v", err)
		}
	})

	if s.dataDirLock != nil {
		t.Fatal("dataDirLock is set, want nil: no real disk lock can exist on a lock-unsupported filesystem")
	}

	entry := findWarnEntryWithField(hook, "data_dir", dataDir)
	if entry == nil {
		t.Fatalf("missing WARN with data_dir=%q; entries: %#v", dataDir, hook.AllEntries())
	}

	// Checked by literal substring, never by comparing entry.Message against
	// dataDirLockUnsupportedWarning itself -- that comparison is the constant
	// checked against itself and can never fail, whatever the constant says.
	// Each fragment below pins one of the operator's four required elements
	// (2026-09-04 decision): a second instance can still
	// write the same data_dir, bot.log itself is affected, the warning's own
	// visibility is stdout, and the remedy path is named. Fragments are
	// chosen short enough to survive reasonable rewording of the sentence
	// around them, but each sits inside the one sentence that carries its
	// consequence, so deleting that sentence -- or replacing the whole
	// message -- still fails this check.
	msg := entry.Message
	requiredFragments := []string{
		"second ransomware-bot instance", // consequence 1: a second instance is not blocked
		"same data_dir",                  // ... from writing the same data_dir
		"bot.log stays",                  // consequence 2: bot.log itself is affected
		"visible only on stdout",         // the warning's own visibility
		"Move data_dir",                  // the remedy path
	}
	for _, fragment := range requiredFragments {
		if !strings.Contains(msg, fragment) {
			t.Fatalf("warning message missing required fragment %q; got: %s", fragment, msg)
		}
	}

	if entry := findTestLogEntry(hook, "Data directory unavailable; continuing with in-memory status only"); entry != nil {
		t.Fatal("the generic data_dir warning was also logged; the classified condition must replace it, not add to it")
	}
}

// findWarnEntryWithField returns the WARN-level entry carrying field=value,
// independent of its message text. Unlike findTestLogEntryWithField, this
// does not require the caller to already know the exact message -- comparing
// a logged message against the very constant that produced it can never
// fail, which is what let the message-content assertion below become a
// tautology before this helper existed.
func findWarnEntryWithField(hook *logtest.Hook, field string, value any) *log.Entry {
	for _, entry := range hook.AllEntries() {
		if entry.Level == log.WarnLevel && entry.Data[field] == value {
			return entry
		}
	}
	return nil
}

// TestNewSchedulerComponentsStillUsesGenericWarnForUnclassifiedLockErrors is
// the regression guard for the branch this finding did not touch: an
// unrelated data_dir lock failure (not contention, not ENOLCK/EINVAL) must
// keep logging the original, pre-existing generic WARN, unchanged.
func TestNewSchedulerComponentsStillUsesGenericWarnForUnclassifiedLockErrors(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()

	original := acquireDataDirLockFunc
	acquireDataDirLockFunc = func(string) (*status.DataDirLock, error) {
		return nil, fmt.Errorf("lock data_dir lock %q: %w", "/data/.ransomware-bot.lock", syscall.EACCES)
	}
	t.Cleanup(func() { acquireDataDirLockFunc = original })

	cfg := config.DefaultConfig()
	cfg.APIKey = "test-key"
	dataDir := t.TempDir()
	cfg.DataDir = dataDir

	s, err := New(cfg, t.TempDir(), false)
	if err != nil {
		t.Fatalf("New() error = %v, want the bot to keep starting", err)
	}
	t.Cleanup(func() {
		if err := s.Stop(); err != nil {
			t.Errorf("Stop() error = %v", err)
		}
	})

	if entry := findTestLogEntryWithField(hook, "Data directory unavailable; continuing with in-memory status only", "data_dir", dataDir); entry == nil {
		t.Fatal("missing the original generic WARN for an unclassified lock error")
	}
	if entry := findTestLogEntry(hook, dataDirLockUnsupportedWarning); entry != nil {
		t.Fatal("the lock-unsupported WARN fired for an unrelated error; classification is too broad")
	}
}
