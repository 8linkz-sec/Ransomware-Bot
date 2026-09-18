package status

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/8linkz-sec/Ransomware-News-Bot/internal/filelock"
)

func TestAcquireDataDirLockPreventsSecondWriter(t *testing.T) {
	dataDir := t.TempDir()

	lock, err := AcquireDataDirLock(dataDir)
	if err != nil {
		t.Fatalf("AcquireDataDirLock() error = %v", err)
	}
	lockPath := filepath.Join(dataDir, dataDirLockFileName)
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("lock file was not created: %v", err)
	}

	second, err := AcquireDataDirLock(dataDir)
	if err == nil {
		_ = second.Release()
		t.Fatal("second AcquireDataDirLock() succeeded while first lock was held")
	}
	if !strings.Contains(err.Error(), "already locked") {
		t.Fatalf("second AcquireDataDirLock() error = %v, want already locked", err)
	}

	if err := lock.Release(); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("lock file should remain after release for stable advisory locking: %v", err)
	}

	reacquired, err := AcquireDataDirLock(dataDir)
	if err != nil {
		t.Fatalf("AcquireDataDirLock() after release error = %v", err)
	}
	if err := reacquired.Release(); err != nil {
		t.Fatalf("second Release() error = %v", err)
	}
}

// TestAcquireDataDirLockClassifiesLockUnsupportedDistinctlyFromAlreadyLocked
// pins the fix: on a filesystem that does not support advisory locking at all
// (ENOLCK/EINVAL -- 9p, some CIFS/SMB and FUSE mounts, including an Unraid
// /mnt/user/... share), AcquireDataDirLock's error must (a) still be
// classifiable via filelock.IsUnsupported (so a caller can tell this apart
// from both contention and a generic error) and (b) must NOT contain the
// "already locked" substring scheduler.go's dataDirAlreadyLocked greps for --
// that string means "refuse to start", which is not this finding's remedy.
func TestAcquireDataDirLockClassifiesLockUnsupportedDistinctlyFromAlreadyLocked(t *testing.T) {
	dataDir := t.TempDir()

	original := acquireFileLock
	acquireFileLock = func(*os.File) error {
		return fmt.Errorf("flock: %w", syscall.ENOLCK)
	}
	t.Cleanup(func() { acquireFileLock = original })

	_, err := AcquireDataDirLock(dataDir)
	if err == nil {
		t.Fatal("AcquireDataDirLock() error = nil, want the injected ENOLCK failure")
	}
	if strings.Contains(err.Error(), "already locked") {
		t.Fatalf("AcquireDataDirLock() error = %q, must not contain \"already locked\" "+
			"(scheduler.go's dataDirAlreadyLocked would treat this as fatal contention)", err.Error())
	}
	if !filelock.IsUnsupported(err) {
		t.Fatalf("filelock.IsUnsupported(%v) = false, want true (classification must survive the wrap)", err)
	}
}
