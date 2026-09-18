package logger

import (
	"os"
	"path/filepath"
	"testing"
)

// TestOpenLockedDoesNotReleaseALockItDidNotAcquire pins finding 7: openLocked
// must not release the owner lock on a failed reopen when this call did not
// acquire it (w.lockFile != nil on entry, i.e. a reopen after rotation or a
// lazy reopen from Write, not a fresh construction). Before the fix, a
// transient reopen failure dropped the lock of a writer that was still
// logically alive, letting a second, fully independent process (emulated
// here the same way internal/filelock's own tests do: two separate
// os.OpenFile-backed handles in one process, since flock/LockFileEx
// semantics are per-open-file-description) write to the same log file while
// the first writer was still running.
func TestOpenLockedDoesNotReleaseALockItDidNotAcquire(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "bot.log")

	writer, err := newRotatingFileWriter(logPath, LogRotationConfig{MaxSizeMB: 1})
	if err != nil {
		t.Fatalf("newRotatingFileWriter() error = %v", err)
	}
	defer writer.Close()

	writer.mu.Lock()
	if err := writer.closeFileLocked(); err != nil {
		writer.mu.Unlock()
		t.Fatalf("closeFileLocked() error = %v", err)
	}
	// Reproduce the state rotateLocked leaves right after a successful
	// rename: w.file == nil, w.lockFile still held.
	if writer.lockFile == nil {
		writer.mu.Unlock()
		t.Fatal("owner lock handle is nil before the reopen attempt")
	}

	// Make the log path unopenable: remove the file and put a directory in
	// its place, so the reopen's os.OpenFile fails with "is a directory".
	if err := os.Remove(logPath); err != nil {
		writer.mu.Unlock()
		t.Fatalf("Remove(%q) error = %v", logPath, err)
	}
	if err := os.Mkdir(logPath, 0o750); err != nil {
		writer.mu.Unlock()
		t.Fatalf("Mkdir(%q) error = %v", logPath, err)
	}

	reopenErr := writer.openLocked()
	lockStillHeld := writer.lockFile != nil
	writer.mu.Unlock()

	if reopenErr == nil {
		t.Fatal("openLocked() error = nil, want the simulated open failure")
	}
	if !lockStillHeld {
		t.Fatalf("owner lock was released after a failed reopen (openLocked() error = %v); "+
			"the single-writer guarantee is broken", reopenErr)
	}

	// The real single-writer guarantee: a second, independent construction
	// against the same path must still be refused while the first writer (w)
	// is live and unclosed. Restore an openable path first so the failure
	// this second attempt hits is the lock, not the directory.
	if err := os.Remove(logPath); err != nil {
		t.Fatalf("Remove(%q) error = %v", logPath, err)
	}

	if second, err := newRotatingFileWriter(logPath, LogRotationConfig{MaxSizeMB: 1}); err == nil {
		_ = second.Close()
		t.Fatal("a second writer was admitted to the log file while the first writer is still live and unclosed")
	}
}

// TestOpenLockedStillReleasesOnFreshConstructionFailure pins that the common
// case -- a fresh construction whose first open fails -- still releases the
// lock it just took, so a subsequent, corrected attempt is not spuriously
// refused.
func TestOpenLockedStillReleasesOnFreshConstructionFailure(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "bot.log")

	if err := os.Mkdir(logPath, 0o750); err != nil {
		t.Fatalf("Mkdir(%q) error = %v", logPath, err)
	}

	if _, err := newRotatingFileWriter(logPath, LogRotationConfig{MaxSizeMB: 1}); err == nil {
		t.Fatal("newRotatingFileWriter() error = nil, want the directory-in-place-of-file failure")
	}

	if err := os.Remove(logPath); err != nil {
		t.Fatalf("Remove(%q) error = %v", logPath, err)
	}

	writer, err := newRotatingFileWriter(logPath, LogRotationConfig{MaxSizeMB: 1})
	if err != nil {
		t.Fatalf("newRotatingFileWriter() after the underlying problem is fixed: error = %v, "+
			"want success (the failed fresh construction must have released its lock)", err)
	}
	defer writer.Close()
}
